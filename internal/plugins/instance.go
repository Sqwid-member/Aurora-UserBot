package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/ipc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
)

// State is the lifecycle state of a plugin instance.
type State string

// Plugin lifecycle states.
const (
	StateCreated  State = "created"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// BaseEnv is the minimal environment every plugin gets. Everything else is
// opt-in through the manifest, which keeps a hostile plugin from walking up
// the environment to find credentials.
var BaseEnv = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "PREFIX", "LD_LIBRARY_PATH"}

const eventQueueSize = 512

type queuedEvent struct {
	name string
	data any
}

// Instance is a single running plugin.
type Instance struct {
	Name     string
	Dir      string
	Manifest *Manifest

	log  *logx.Logger
	opts Options

	mu        sync.RWMutex
	state     State
	cmd       *exec.Cmd
	conn      *ipc.Conn
	startedAt time.Time
	finished  time.Time
	lastErr   string
	exitErr   error
	hello     map[string]any

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	queue     chan queuedEvent
	writerEnd chan struct{}

	lastActivity atomic.Int64 // unix nanos
	dropped      atomic.Uint64
	delivered    atomic.Uint64
	outputBytes  atomic.Uint64
	restarts     atomic.Int32

	rateWindow atomic.Int64 // unix second
	rateCount  atomic.Int32
}

// Options configures the plugin host.
type Options struct {
	// Version is reported to plugins during the handshake.
	Version string
	// MemoryMB is the default address-space limit when a manifest omits one.
	MemoryMB int
	// StartTimeout bounds the handshake.
	StartTimeout time.Duration
	// StopGrace is how long a plugin gets to exit after plugin.unload.
	StopGrace time.Duration
	// MaxRestarts bounds automatic restarts before a plugin is parked.
	MaxRestarts int
	// Env is the host environment used to resolve the allowlist.
	Env func(string) string
	// Sandbox is a hook for platform-specific extra isolation.
	Sandbox func(dir string, cmd *exec.Cmd) error
	// StderrSink receives plugin stderr lines before they hit the log.
	StderrSink func(plugin, line string)
}

// newInstance builds an instance from a validated manifest.
func newInstance(m *Manifest, dir string, log *logx.Logger, opts Options) *Instance {
	return &Instance{
		Name:     m.Name,
		Dir:      dir,
		Manifest: m,
		log:      log.Scoped("plugin:" + m.Name),
		opts:     opts,
		state:    StateCreated,
		queue:    make(chan queuedEvent, eventQueueSize),
		done:     make(chan struct{}),
	}
}

// State returns the current lifecycle state.
func (p *Instance) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *Instance) setState(s State) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
}

// Running reports whether the instance is started and healthy.
func (p *Instance) Running() bool { return p.State() == StateRunning }

// Start spawns the plugin process and performs the handshake.
func (p *Instance) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.state == StateRunning || p.state == StateStarting {
		p.mu.Unlock()
		return errors.New("plugins: already running")
	}
	p.state = StateStarting
	p.lastErr = ""
	p.mu.Unlock()

	bin, args, err := p.resolveCommand()
	if err != nil {
		p.fail(err)
		return err
	}

	// WithoutCancel so that a caller giving up (say, the panel closing an HTTP
	// request) does not kill a plugin the user asked to keep running; the
	// plugin's lifetime is owned by Start/Stop, not by whoever called Start.
	p.ctx, p.cancel = context.WithCancel(context.WithoutCancel(ctx))
	p.done = make(chan struct{})
	p.writerEnd = make(chan struct{})

	// Cancel and WaitDelay may only be set on a Command created with
	// CommandContext — exec enforces that, and rightly so.
	cmd := sysx.CommandContext(p.ctx, bin, args...)
	cmd.Dir = p.Dir
	cmd.Env = p.buildEnv()
	cmd.SysProcAttr = procAttr()
	cmd.Cancel = func() error {
		return killGroup(cmd.Process, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	limits := p.Manifest.Limits
	if limits.MemoryMB == 0 {
		limits.MemoryMB = p.opts.MemoryMB
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.fail(err)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.fail(err)
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.fail(err)
		return err
	}

	if err := cmd.Start(); err != nil {
		p.fail(err)
		return err
	}

	for _, w := range applyLimits(cmd.Process.Pid, limits) {
		p.log.Warn("resource limit not applied", logx.F("detail", w))
	}

	conn := ipc.NewConn(stdout, stdin)
	p.mu.Lock()
	p.cmd = cmd
	p.conn = conn
	p.startedAt = time.Now()
	p.mu.Unlock()
	p.touch()

	// stderr: never let a plugin's noisy debug output wedge the host.
	go ipc.StderrPipe(stderr, func(line string) {
		if p.opts.StderrSink != nil {
			p.opts.StderrSink(p.Name, line)
		}
		p.outputBytes.Add(uint64(len(line)))
		level := logx.LevelDebug
		if lim := p.Manifest.Limits.OutputKB; lim > 0 && p.outputBytes.Load() > uint64(lim)*1024 {
			level = logx.LevelWarn
		}
		p.log.Log(level, line)
	})

	go func() {
		_ = conn.Serve(p.ctx)
		_ = conn.Close()
	}()
	p.mu.RLock()
	writerEnd := p.writerEnd
	p.mu.RUnlock()
	go p.dispatchLoop(writerEnd)
	go p.supervise()

	timeout := p.opts.StartTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	helloCtx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()

	var info map[string]any
	if err := conn.Call(helloCtx, "plugin.hello", map[string]any{
		"protocol": ipc.ProtocolVersion,
		"core":     "aurora",
		"version":  p.opts.Version,
		"dir":      p.Dir,
		"plugin":   p.Name,
		"now":      time.Now().Unix(),
	}, &info); err != nil {
		_ = p.stop(context.WithoutCancel(ctx))
		wrapped := fmt.Errorf("handshake failed: %w", err)
		p.fail(wrapped)
		return wrapped
	}

	p.mu.Lock()
	p.hello = info
	p.mu.Unlock()

	loadCtx, cancelLoad := context.WithTimeout(p.ctx, timeout)
	defer cancelLoad()
	var loaded map[string]any
	if err := conn.Call(loadCtx, "plugin.load", map[string]any{
		"plugin": p.Name,
		"events": p.Manifest.Subscribed(),
		"commands": func() []map[string]any {
			out := make([]map[string]any, 0, len(p.Manifest.Commands))
			for _, c := range p.Manifest.Commands {
				out = append(out, map[string]any{
					"name": c.Name, "usage": c.Usage,
					"description": c.Description, "aliases": c.Aliases,
				})
			}
			return out
		}(),
	}, &loaded); err != nil {
		_ = p.stop(context.WithoutCancel(ctx))
		wrapped := fmt.Errorf("plugin.load failed: %w", err)
		p.fail(wrapped)
		return wrapped
	}

	p.setState(StateRunning)
	p.touch()
	p.log.Info("plugin started",
		logx.F("pid", cmd.Process.Pid),
		logx.F("language", orDefault(p.Manifest.Language, "?")),
	)
	return nil
}

func (p *Instance) fail(err error) {
	p.mu.Lock()
	p.lastErr = err.Error()
	p.state = StateFailed
	p.mu.Unlock()
	p.log.Error("plugin failed", logx.F("error", err))
}

// Stop asks the plugin to shut down, then escalates to signals.
func (p *Instance) Stop(ctx context.Context) error { return p.stop(ctx) }

func (p *Instance) stop(ctx context.Context) error {
	p.mu.RLock()
	conn, cmd := p.conn, p.cmd
	wasRunning := p.state == StateRunning || p.state == StateStarting
	p.mu.RUnlock()

	if conn == nil || !wasRunning {
		p.setState(StateStopped)
		return nil
	}

	p.setState(StateStopping)

	grace := p.opts.StopGrace
	if grace <= 0 {
		grace = 3 * time.Second
	}
	byeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	_ = conn.Call(byeCtx, "plugin.unload", map[string]any{"plugin": p.Name}, nil)
	cancel()

	select {
	case <-p.done:
	case <-time.After(grace):
		if cmd != nil && cmd.Process != nil {
			_ = killGroup(cmd.Process, syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(grace):
				_ = killGroup(cmd.Process, syscall.SIGKILL)
				<-p.done
			}
		}
	}

	p.cancel()
	_ = conn.Close()
	p.mu.Lock()
	select {
	case <-p.writerEnd:
		// Already closed by a concurrent Stop: closing twice panics.
	default:
		close(p.writerEnd)
	}
	p.state = StateStopped
	p.mu.Unlock()
	p.log.Info("plugin stopped")
	return nil
}

// supervise waits for the process and records the exit reason.
func (p *Instance) supervise() {
	defer close(p.done)
	p.mu.RLock()
	cmd := p.cmd
	p.mu.RUnlock()
	err := cmd.Wait()

	p.mu.Lock()
	p.exitErr = err
	p.finished = time.Now()
	p.conn = nil
	p.cmd = nil
	p.mu.Unlock()

	state := p.State()
	var exitMsg string
	switch {
	case state == StateStopping || p.ctx.Err() != nil:
		p.setState(StateStopped)
		return
	case err == nil:
		p.mu.Lock()
		p.lastErr = "exited cleanly without being asked to stop"
		exitMsg = p.lastErr
		p.mu.Unlock()
	default:
		p.mu.Lock()
		p.lastErr = err.Error()
		exitMsg = p.lastErr
		p.mu.Unlock()
	}
	p.setState(StateFailed)
	p.log.Warn("plugin process exited", logx.F("error", orDefault(exitMsg, "unknown")))
}

// dispatchLoop drains the event queue into the plugin.
func (p *Instance) dispatchLoop(writerEnd <-chan struct{}) {
	for {
		select {
		case <-writerEnd:
			return
		case ev := <-p.queue:
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()
			if conn == nil || conn.Closed() {
				return
			}
			if !p.allowEvent() {
				continue
			}
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			err := conn.NotifyWithContext(ctx, "event", map[string]any{"name": ev.name, "data": ev.data})
			cancel()
			if err != nil {
				p.log.Debug("event delivery failed", logx.F("event", ev.name), logx.F("error", err))
				continue
			}
			p.delivered.Add(1)
			p.touch()
		}
	}
}

// allowEvent implements a simple per-second token bucket.
func (p *Instance) allowEvent() bool {
	now := time.Now().Unix()
	last := p.rateWindow.Load()
	if last != now {
		p.rateWindow.Store(now)
		p.rateCount.Store(0)
	}
	if p.rateCount.Add(1) > MaxEventRatePer {
		p.dropped.Add(1)
		return false
	}
	return true
}

func (p *Instance) touch() { p.lastActivity.Store(time.Now().UnixNano()) }

// finishedAt reports when the last process exit happened, for backoff.
func (p *Instance) finishedAt() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.finished
}

// Emit queues an event for delivery. It never blocks: if the plugin is slow
// the event is dropped and counted, because stalling the MTProto update loop
// would be far worse than losing one event.
func (p *Instance) Emit(name string, data any) {
	if p.State() != StateRunning || !p.Manifest.HasEvent(name) {
		return
	}
	select {
	case p.queue <- queuedEvent{name: name, data: data}:
	default:
		p.dropped.Add(1)
	}
}

// Call invokes a host-to-plugin request.
func (p *Instance) Call(ctx context.Context, method string, params, out any) error {
	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("plugins: %s is not running", p.Name)
	}
	p.touch()
	return conn.Call(ctx, method, params, out)
}

// Watchdog kills a plugin that has been silent for longer than its limit.
// A wedged plugin that never drains its stdout will otherwise sit there
// holding memory forever.
func (p *Instance) Watchdog() {
	idle := p.Manifest.Limits.IdleTimeoutSec
	if idle <= 0 {
		return
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-p.writerEnd:
			return
		case <-tick.C:
			if p.State() != StateRunning {
				continue
			}
			last := time.Unix(0, p.lastActivity.Load())
			if time.Since(last) > time.Duration(idle)*time.Second {
				p.log.Warn("plugin idle watchdog fired", logx.F("idle_sec", idle))
				p.mu.RLock()
				cmd := p.cmd
				p.mu.RUnlock()
				if cmd != nil && cmd.Process != nil {
					_ = killGroup(cmd.Process, syscall.SIGKILL)
				}
				return
			}
		}
	}
}

// Stats is the per-plugin snapshot shown in the control panel.
type Stats struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Language    string   `json:"language"`
	State       string   `json:"state"`
	PID         int      `json:"pid,omitempty"`
	UptimeSec   int64    `json:"uptime_sec"`
	Restarts    int32    `json:"restarts"`
	Events      uint64   `json:"events_delivered"`
	Dropped     uint64   `json:"events_dropped"`
	OutputKB    uint64   `json:"output_kb"`
	LastError   string   `json:"last_error,omitempty"`
	Subscribed  []string `json:"events,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Permissions any      `json:"permissions,omitempty"`
	Path        string   `json:"path"`
}

// Stats returns a snapshot of the instance.
func (p *Instance) Stats() Stats {
	p.mu.RLock()
	state := p.state
	pid := 0
	if p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	started := p.startedAt
	lastErr := p.lastErr
	p.mu.RUnlock()

	s := Stats{
		Name:        p.Name,
		Version:     p.Manifest.Version,
		Description: p.Manifest.Description,
		Author:      p.Manifest.Author,
		Language:    orDefault(p.Manifest.Language, "unknown"),
		State:       string(state),
		PID:         pid,
		Restarts:    p.restarts.Load(),
		Events:      p.delivered.Load(),
		Dropped:     p.dropped.Load(),
		OutputKB:    p.outputBytes.Load() / 1024,
		LastError:   lastErr,
		Subscribed:  p.Manifest.Subscribed(),
		Permissions: p.Manifest.Permissions,
		Path:        p.Dir,
	}
	if !started.IsZero() && state == StateRunning {
		s.UptimeSec = int64(time.Since(started).Seconds())
	}
	for _, c := range p.Manifest.Commands {
		s.Commands = append(s.Commands, c.Name)
	}
	return s
}

// resolveCommand turns the manifest runtime into an absolute executable path
// plus arguments, rejecting anything that escapes the plugin directory.
func (p *Instance) resolveCommand() (string, []string, error) {
	raw := p.Manifest.Runtime.Command
	var bin string

	if strings.ContainsRune(raw, os.PathSeparator) {
		abs := filepath.Join(p.Dir, raw)
		if err := p.checkExecutable(abs); err != nil {
			return "", nil, err
		}
		bin = abs
	} else {
		lookup := raw
		if wrap := p.Manifest.Runtime.Wrap; wrap != "" {
			// The wrapper is resolved from PATH, the real command is relative
			// to the plugin dir, so `proot -r / ./run.sh` just works.
			bin = lookup
		} else {
			found, err := sysx.LookPath(raw)
			if err != nil {
				return "", nil, fmt.Errorf("plugins: %s: %w", raw, err)
			}
			bin = found
		}
	}

	args := make([]string, 0, len(p.Manifest.Runtime.Args)+1)
	if wrap := p.Manifest.Runtime.Wrap; wrap != "" {
		args = append(args, p.Manifest.Runtime.Args...)
		args = append(args, absArg(p.Dir, raw))
		return bin, args, nil
	}
	for _, a := range p.Manifest.Runtime.Args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			args = append(args, a)
			continue
		}
		args = append(args, filepath.Join(p.Dir, a))
	}
	return bin, args, nil
}

func absArg(dir, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(dir, rel)
}

func (p *Instance) checkExecutable(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("plugins: %s: %w", p.Manifest.Runtime.Command, err)
	}
	if st.IsDir() {
		return fmt.Errorf("plugins: %s is a directory", p.Manifest.Runtime.Command)
	}
	if st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("plugins: %s is not executable (chmod +x it)", p.Manifest.Runtime.Command)
	}
	return nil
}

// buildEnv assembles a minimal, explicitly allowlisted environment.
func (p *Instance) buildEnv() []string {
	env := map[string]string{}
	get := p.opts.Env
	if get == nil {
		get = os.Getenv
	}
	for _, k := range BaseEnv {
		if v := get(k); v != "" {
			env[k] = k + "=" + v
		}
	}
	// The plugin's HOME is its own directory: no accidental ~/.ssh access.
	env["HOME"] = p.Dir
	env["PWD"] = p.Dir
	env["AURORA_PLUGIN"] = p.Name
	env["AURORA_PLUGIN_DIR"] = p.Dir
	env["AURORA_PROTOCOL"] = strconv.Itoa(ipc.ProtocolVersion)
	if tmp := get("TMPDIR"); tmp != "" {
		env["TMPDIR"] = tmp
	} else {
		env["TMPDIR"] = filepath.Join(p.Dir, ".tmp")
		_ = os.MkdirAll(env["TMPDIR"], 0o700)
	}
	for _, k := range p.Manifest.Permissions.Env {
		if v := get(k); v != "" {
			env[k] = v
		}
	}
	for k, v := range p.Manifest.Runtime.Env {
		env[k] = v
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, env[k])
	}
	return out
}

func killGroup(proc *os.Process, sig syscall.Signal) error {
	if proc == nil {
		return nil
	}
	// Negative pid targets the process group created with Setpgid.
	if err := syscall.Kill(-proc.Pid, sig); err == nil {
		return nil
	}
	return proc.Signal(sig)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
