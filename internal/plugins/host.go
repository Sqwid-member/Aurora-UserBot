package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

// Services is the host functionality exposed to plugins. It is implemented by
// the Telegram runtime; keeping it an interface here is what lets the plugin
// host be unit-tested without a phone or a network.
type Services interface {
	// Send delivers a text message.
	Send(ctx context.Context, req proto.SendRequest) (proto.SendResult, error)
	// GetMe returns the authenticated account.
	GetMe(ctx context.Context) (proto.User, error)
	// History returns recent messages from a peer.
	History(ctx context.Context, peer string, limit int) ([]proto.Message, error)
	// Resolve turns a username, phone, link or numeric ID into a peer.
	Resolve(ctx context.Context, peer string) (proto.PeerInfo, error)
	// Notify pushes a toast to the control panel.
	Notify(title, text, level string)
	// ConfigValue reads a single config value by dotted path.
	ConfigValue(key string) (any, bool)
	// Ready reports whether the Telegram runtime can serve requests.
	Ready() bool
}

// Errors surfaced by the host.
var (
	ErrNotFound    = errors.New("plugins: not found")
	ErrExists      = errors.New("plugins: already installed")
	ErrNotRunning  = errors.New("plugins: not running")
	ErrNoCommand   = errors.New("plugins: unknown command")
	ErrRateLimited = errors.New("plugins: too many restarts")
)

// Host owns every plugin instance.
type Host struct {
	root     string
	services Services
	kv       *kv.Store
	log      *logx.Logger
	opts     Options

	mu      sync.RWMutex
	insts   map[string]*Instance
	order   []string
	stopCh  chan struct{}
	stopped bool
	wg      sync.WaitGroup
}

// New creates a plugin host rooted at dir.
func New(services Services, dir string, store *kv.Store, log *logx.Logger, opts Options) *Host {
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 15 * time.Second
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = 3 * time.Second
	}
	if opts.MaxRestarts <= 0 {
		opts.MaxRestarts = 5
	}
	if opts.MemoryMB <= 0 {
		// 256, not 128: V8 (Node) cannot even reserve its code range under
		// a 128 MB RLIMIT_DATA and dies with SIGTRAP on start. The cap only
		// triggers on genuine growth, so a higher default costs no RAM.
		opts.MemoryMB = 256
	}
	h := &Host{
		root:     dir,
		services: services,
		kv:       store,
		log:      log.Scoped("plugins"),
		opts:     opts,
		insts:    make(map[string]*Instance, 8),
		stopCh:   make(chan struct{}),
	}
	if h.opts.Connect == nil {
		h.opts.Connect = h.BindAPI
	}
	return h
}

// Root returns the plugin directory.
func (h *Host) Root() string { return h.root }

// Discover scans the plugin directory and returns every valid manifest.
// Directories without a manifest are ignored; broken manifests are reported.
func (h *Host) Discover() (ok []*Manifest, broken map[string]string) {
	broken = map[string]string{}
	entries, err := os.ReadDir(h.root)
	if err != nil {
		return nil, broken
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(h.root, e.Name())
		m, err := LoadManifest(dir)
		if err != nil {
			if !errors.Is(err, ErrNoManifest) {
				broken[e.Name()] = err.Error()
			}
			continue
		}
		ok = append(ok, m)
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].Name < ok[j].Name })
	return ok, broken
}

// Install registers a plugin that is already on disk.
// EnsureInstalled registers every discovered plugin without starting it,
// so offline tools (CLI) see the same set as the running daemon, which
// installs everything in StartAll. Already-registered plugins are skipped.
// It returns per-plugin problems (broken manifests, install errors).
func (h *Host) EnsureInstalled() map[string]string {
	broken := map[string]string{}
	manifests, br := h.Discover()
	for name, why := range br {
		broken[name] = why
	}
	for _, m := range manifests {
		if _, ok := h.Get(m.Name); ok {
			continue
		}
		if _, err := h.Install(filepath.Join(h.root, m.Name)); err != nil {
			broken[m.Name] = err.Error()
		}
	}
	return broken
}

func (h *Host) Install(dir string) (*Instance, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if _, exists := h.insts[m.Name]; exists {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrExists, m.Name)
	}
	inst := newInstance(m, dir, h.log, h.opts)
	h.insts[m.Name] = inst
	h.order = append(h.order, m.Name)
	h.mu.Unlock()

	h.log.Info("plugin discovered",
		logx.F("plugin", m.Name),
		logx.F("version", orDefault(m.Version, "0")),
		logx.F("language", orDefault(m.Language, "unknown")),
	)
	return inst, nil
}

// Uninstall stops and removes a plugin directory.
func (h *Host) Uninstall(name string, removeFiles bool) error {
	inst, ok := h.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = inst.Stop(ctx)

	h.mu.Lock()
	delete(h.insts, name)
	for i, n := range h.order {
		if n == name {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
	h.mu.Unlock()

	if removeFiles {
		cleanRoot := filepath.Clean(h.root)
		cleanDir := filepath.Clean(inst.Dir)
		rel, err := filepath.Rel(cleanRoot, cleanDir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing to remove directory outside plugin root: %s", inst.Dir)
		}
		return os.RemoveAll(inst.Dir)
	}
	return nil
}

// Get returns an instance by name.
func (h *Host) Get(name string) (*Instance, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	inst, ok := h.insts[name]
	return inst, ok
}

// Names returns the installed plugin names in discovery order.
func (h *Host) Names() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.order...)
}

// Start launches a plugin, registering the host API on its connection.
func (h *Host) Start(ctx context.Context, name string) error {
	inst, ok := h.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err := inst.Start(ctx); err != nil {
		return err
	}
	h.BindAPI(inst)
	inst.restarts.Store(0)
	go inst.Watchdog()
	return nil
}

// Stop shuts a plugin down.
func (h *Host) Stop(ctx context.Context, name string) error {
	inst, ok := h.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return inst.Stop(ctx)
}

// Restart stops then starts a plugin.
func (h *Host) Restart(ctx context.Context, name string) error {
	if inst, ok := h.Get(name); ok {
		_ = inst.Stop(ctx)
	}
	return h.Start(ctx, name)
}

// StartAll installs and starts every discovered plugin, skipping the names in
// skip. One broken plugin never stops the rest from coming up.
func (h *Host) StartAll(ctx context.Context, skip map[string]bool) (started []string, failed map[string]string) {
	manifests, broken := h.Discover()
	failed = map[string]string{}
	for name, why := range broken {
		failed[name] = why
	}
	for _, m := range manifests {
		if skip[m.Name] {
			continue
		}
		inst, err := h.Install(filepath.Join(h.root, m.Name))
		if err != nil {
			failed[m.Name] = err.Error()
			continue
		}
		if err := h.Start(ctx, m.Name); err != nil {
			failed[m.Name] = err.Error()
			continue
		}
		h.log.Debug("plugin up", logx.F("plugin", m.Name), logx.F("dir", inst.Dir))
		started = append(started, m.Name)
	}
	h.wg.Add(1)
	go h.supervise()
	return started, failed
}

// StopAll stops every running plugin concurrently.
func (h *Host) StopAll(ctx context.Context) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	close(h.stopCh)
	h.mu.Unlock()

	names := h.Names()
	var wg sync.WaitGroup
	for _, name := range names {
		inst, ok := h.Get(name)
		if !ok || !inst.Running() {
			continue
		}
		wg.Add(1)
		go func(p *Instance) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			_ = p.Stop(sctx)
		}(inst)
	}
	wg.Wait()
	h.wg.Wait()
}

// supervise restarts crashed plugins with exponential backoff.
//
// A userbot whose plugin died at 3am is not a userbot that works, but an
// infinite crash loop is worse — so restarts are capped and then the plugin
// is parked for a human to look at.
func (h *Host) supervise() {
	defer h.wg.Done()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-t.C:
		}

		for _, name := range h.Names() {
			inst, ok := h.Get(name)
			if !ok {
				continue
			}
			// Forgive old crashes: a plugin that has been healthy for a
			// while earns its restart budget back, so transient failures
			// spread over days can never park it permanently.
			if inst.State() == StateRunning && inst.restarts.Load() > 0 &&
				inst.healthySince() > 10*time.Minute {
				inst.restarts.Store(0)
			}
			if inst.State() != StateFailed {
				continue
			}
			n := inst.restarts.Load()
			if n >= int32(h.opts.MaxRestarts) {
				continue
			}
			backoff := time.Duration(1<<min(n, 5)) * time.Second
			if time.Since(inst.finishedAt()) < backoff {
				continue
			}
			inst.restarts.Store(n + 1)
			h.log.Warn("restarting plugin",
				logx.F("plugin", name),
				logx.F("attempt", n+1),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := h.Start(ctx, name); err != nil {
				h.log.Warn("plugin restart failed",
					logx.F("plugin", name),
					logx.F("error", err),
				)
			}
			cancel()
		}
	}
}

// Emit fans an event out to every subscribed plugin. Never blocks.
func (h *Host) Emit(name string, data any) {
	h.mu.RLock()
	insts := make([]*Instance, 0, len(h.insts))
	for _, inst := range h.insts {
		insts = append(insts, inst)
	}
	h.mu.RUnlock()

	for _, inst := range insts {
		inst.Emit(name, data)
	}
}

// EmitForAccount delivers an event only to plugins that are enabled for the given account.
func (h *Host) EmitForAccount(accountID string, name string, data any, isEnabled func(accID, pluginName string) bool) {
	h.mu.RLock()
	insts := make([]*Instance, 0, len(h.insts))
	for pluginName, inst := range h.insts {
		if isEnabled != nil && !isEnabled(accountID, pluginName) {
			continue
		}
		insts = append(insts, inst)
	}
	h.mu.RUnlock()

	for _, inst := range insts {
		inst.Emit(name, data)
	}
}

// Commands returns every command exposed by running plugins.
func (h *Host) Commands() []CommandSpec {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []CommandSpec
	for _, name := range h.order {
		inst := h.insts[name]
		if inst == nil || !inst.Running() {
			continue
		}
		for _, c := range inst.Manifest.Commands {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Command routes a user command to the plugin that declared it.
//
// text is the raw line the user typed. The leading word may or may not be the
// command name itself ("/ping go" from a chat, "ping go" from the panel), so a
// matching first word is stripped before the arguments are handed over.
func (h *Host) Command(ctx context.Context, name, text string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if name == "" {
		return "", ErrNoCommand
	}

	// "plugin.command" addresses a specific plugin.
	target, cmdName, scoped := strings.Cut(name, ".")
	if !scoped {
		target, cmdName = "", name
	}

	// Drop the command name if the caller included it.
	argText := strings.TrimSpace(text)
	if first, rest, found := strings.Cut(argText, " "); found {
		if strings.EqualFold(strings.TrimPrefix(first, "/"), cmdName) ||
			strings.EqualFold(strings.TrimPrefix(first, "/"), target+"."+cmdName) {
			argText = strings.TrimSpace(rest)
		}
	} else if strings.EqualFold(strings.TrimPrefix(argText, "/"), cmdName) {
		argText = ""
	}

	h.mu.RLock()
	var (
		chosen *Instance
		spec   CommandSpec
	)
	for _, pname := range h.order {
		inst := h.insts[pname]
		if inst == nil || !inst.Running() {
			continue
		}
		if scoped && pname != target {
			continue
		}
		if c, found := inst.Manifest.Command(cmdName); found {
			chosen, spec = inst, c
			break
		}
	}
	h.mu.RUnlock()

	if chosen == nil {
		return "", fmt.Errorf("%w: %s", ErrNoCommand, name)
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var res proto.CommandResult
	if err := chosen.Call(cctx, "command", proto.CommandRequest{
		Name: spec.Name, Text: argText, Args: auroraFields(argText),
	}, &res); err != nil {
		return "", err
	}
	return res.Text, nil
}

// ChatCommand routes a command typed by the owner in a chat to the plugin
// that declared it with InChat=true.
//
// ok=false means no running plugin offers this command for chat use — the
// caller must leave the message alone (it might be addressed to BotFather
// or just start with a slash). An error with ok=true means the command
// exists but failed; the caller should log it, not the message.
func (h *Host) ChatCommand(ctx context.Context, enabled func(plugin string) bool, name, text string) (res string, ok bool, err error) {
	cmd, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(name), "/"), "@")
	if cmd == "" {
		return "", false, nil
	}

	h.mu.RLock()
	var found bool
	for _, pname := range h.order {
		if enabled != nil && !enabled(pname) {
			continue
		}
		inst := h.insts[pname]
		if inst == nil || !inst.Running() {
			continue
		}
		if c, ok := inst.Manifest.Command(cmd); ok && c.InChat {
			found = true
			break
		}
	}
	h.mu.RUnlock()

	if !found {
		return "", false, nil
	}
	res, err = h.Command(ctx, cmd, text)
	if err != nil {
		return "", true, err
	}
	return res, true, nil
}

// Stats returns a snapshot of every installed plugin.
func (h *Host) Stats() []Stats {
	h.mu.RLock()
	names := append([]string(nil), h.order...)
	h.mu.RUnlock()

	out := make([]Stats, 0, len(names))
	for _, n := range names {
		if inst, ok := h.Get(n); ok {
			st := inst.Stats()
			// The form also exists when only runtime-registered fields do.
			st.HasSettings = len(h.effectiveSettings(inst)) > 0
			out = append(out, st)
		}
	}
	return out
}

// Running returns the names of plugins that are up.
func (h *Host) Running() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for _, n := range h.order {
		if inst := h.insts[n]; inst != nil && inst.Running() {
			out = append(out, n)
		}
	}
	return out
}

// auroraFields splits a command tail into arguments, honouring quotes.
func auroraFields(s string) []string {
	var (
		out   []string
		cur   []rune
		quote rune
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// HostAPIMethods lists the RPC methods a plugin may call.
var HostAPIMethods = []string{
	"log", "kv.get", "kv.set", "kv.delete", "kv.keys",
	"settings.get", "settings.set", "settings.schema",
	"config.get", "config.set",
	"tg.get_me", "tg.send", "tg.history", "tg.resolve",
	"ui.notify", "http.request", "event.subscribe", "core.info",
}
