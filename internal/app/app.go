// Package app wires every Aurora subsystem together.
//
// It is the only place that knows about all of them: the plugin host needs the
// Telegram runtime, the control panel needs the plugin host, and the Telegram
// runtime needs to publish events into the plugin host. Keeping that graph in
// one small file beats an event bus framework in a program whose selling point
// is "it does not waste your RAM".
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/plugins"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

// App is the assembled Aurora core.
type App struct {
	Paths   paths.Layout
	Cfg     *config.Store
	Log     *logx.Logger
	KV      *kv.Store
	Plugins *plugins.Host
	TG      *tgc.Runtime
	Web     *web.Server

	startedAt time.Time

	mu          sync.Mutex
	logSubs     map[uint64]chan logx.Record
	eventSubs   map[uint64]chan proto.Event
	nextSub     uint64
	notifyQueue []proto.Event

	shutdownOnce sync.Once
	stopFn       context.CancelFunc
	stopped      chan struct{}
}

// New builds an application from the resolved directory layout.
func New(layout paths.Layout, level logx.Level, color bool, quiet bool) (*App, error) {
	if err := layout.Ensure(); err != nil {
		return nil, fmt.Errorf("create data directories: %w", err)
	}

	var sink *logx.Logger
	if quiet {
		sink = logx.New(logx.Options{Level: level, Color: color, Sink: os.Stderr}, "core")
	} else {
		sink = logx.New(logx.Options{Level: level, Color: color, Sink: os.Stderr}, "core")
	}

	cfg, err := config.Open(layout.ConfigFile(), sink)
	if err != nil {
		return nil, err
	}
	if lv, ok := logx.ParseLevel(cfg.Get().Runtime.LogLevel); ok && !quiet {
		sink.SetLevel(lv)
	}

	store, err := kv.Open(layout.DBFile())
	if err != nil {
		return nil, fmt.Errorf("open kv: %w", err)
	}

	// The single most effective RAM knob available to a Go program on a
	// phone: tell the runtime to collect harder once we approach the cap.
	if mb := cfg.Get().Runtime.MemLimitMB; mb > 0 {
		limit := int64(mb) * 1024 * 1024
		debugSetMemoryLimit(limit)
		sink.Info("heap soft limit set", logx.F("mb", mb))
	}

	a := &App{
		Paths:     layout,
		Cfg:       cfg,
		Log:       sink,
		KV:        store,
		startedAt: time.Now(),
		logSubs:   map[uint64]chan logx.Record{},
		eventSubs: map[uint64]chan proto.Event{},
		stopped:   make(chan struct{}),
	}

	a.TG = tgc.New(tgcOptions(cfg, sink, a))
	a.Plugins = plugins.New(a, pluginDir(layout, cfg), store, sink, plugins.Options{
		Version:      buildinfo.Version,
		MemoryMB:     cfg.Get().Plugins.DefaultMemoryMB,
		StartTimeout: time.Duration(cfg.Get().Plugins.StartTimeoutSec) * time.Second,
		StopGrace:    3 * time.Second,
		MaxRestarts:  5,
	})

	return a, nil
}

func pluginDir(layout paths.Layout, cfg *config.Store) string {
	if d := cfg.Get().Plugins.Dir; d != "" {
		if strings.HasPrefix(d, "/") {
			return d
		}
		return filepath.Join(layout.Home, d)
	}
	return layout.Plugins
}

func tgcOptions(cfg *config.Store, log *logx.Logger, a *App) tgc.Options {
	c := cfg.Get()
	return tgc.Options{
		AppID:          c.Telegram.AppID,
		AppHash:        c.Telegram.AppHash,
		Phone:          c.Telegram.Phone,
		SessionPath:    a.Paths.SessionFile(),
		TestDC:         c.Telegram.TestDC,
		BlockedMode:    c.Telegram.BlockedMode,
		MTProxy:        c.Telegram.MTProxy,
		Socks5:         c.Telegram.Socks5,
		DeviceName:     c.Telegram.DeviceName,
		DeviceModel:    c.Telegram.DeviceModel,
		DeviceSystem:   c.Telegram.DeviceSystem,
		DeviceVersion:  c.Telegram.DeviceVersion,
		DeviceLanguage: c.Telegram.DeviceLanguage,
		PFS:            c.Telegram.PFS,
		NoUpdates:      c.Telegram.DisableUpdates,
		Logger:         log,
		OnState: func(s proto.SessionState, err string) {
			log.Info("session state", logx.F("state", string(s)), logx.F("error", err))
			switch s {
			case proto.StateAuthorized:
				a.Plugins.Emit(proto.EventSessionStart, a.TG.Me())
			case proto.StateOffline, proto.StateError:
				a.Plugins.Emit(proto.EventSessionEnd, map[string]string{"state": string(s), "error": err})
			}
			a.publish(proto.Event{Name: "core.state", Data: map[string]string{"state": string(s), "error": err}})
		},
		OnEvent: func(name string, data any) {
			a.Plugins.Emit(name, data)
			a.publish(proto.Event{Name: name, Data: data})
		},
	}
}

// Run boots the core and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.stopFn = cancel
	defer cancel()

	if err := a.writePidFile(); err != nil {
		a.Log.Warn("pid file not written", logx.F("error", err))
	}
	defer a.removePidFile()

	cfg := a.Cfg.Get()
	if err := cfg.Validate(); err != nil {
		a.printBanner(cfg, "потрібна налаштовування")
		a.Log.Error("cannot connect", logx.F("error", err))
		return err
	}
	a.printBanner(cfg, "")

	if err := a.startWeb(); err != nil {
		return err
	}

	// Plugins come up before the Telegram session so they are ready the
	// moment it does: a userbot that misses the first message after login
	// is a userbot people file bug reports about.
	skip := map[string]bool{}
	for _, n := range cfg.Plugins.Disabled {
		skip[n] = true
	}
	started, failed := a.Plugins.StartAll(ctx, skip)
	a.Log.Info("plugins ready",
		logx.F("started", len(started)),
		logx.F("failed", len(failed)),
	)
	a.Plugins.Emit(proto.EventCoreStart, map[string]any{
		"version": buildinfo.Version,
		"plugins": started,
	})
	a.publish(proto.Event{Name: proto.EventCoreStart, Data: started})

	go a.runTelegram(ctx)
	<-ctx.Done()

	a.shutdown()
	return nil
}

func (a *App) runTelegram(ctx context.Context) {
	backoff := time.Second
	for {
		err := a.TG.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			// Run returned cleanly — the context ended or the client closed.
			return
		}
		if errors.Is(err, tgc.ErrLoginAborted) {
			a.Log.Warn("login aborted, retrying in 10s")
			backoff = 10 * time.Second
		} else {
			a.Log.Error("telegram runtime stopped", logx.F("error", err), logx.F("retry_in", backoff))
			backoff = nextBackoff(backoff, 2*time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, 2*time.Minute)
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		cur = max
	}
	return cur
}

func (a *App) startWeb() error {
	cfg := a.Cfg.Get()
	if !cfg.Web.Enabled {
		a.Log.Info("control panel disabled")
		return nil
	}
	a.Web = web.New(web.Options{
		Host:        cfg.Web.Host,
		Port:        cfg.Web.Port,
		Token:       cfg.Web.Token,
		ReadOnly:    cfg.Runtime.ReadOnly,
		Logger:      a.Log,
		Backend:     a,
		OpenBrowser: cfg.Web.OpenBrowser,
	})
	if err := a.Web.Start(); err != nil {
		// A busy port must not stop the bot; the CLI still works.
		a.Log.Warn("control panel unavailable", logx.F("error", err))
		a.Web = nil
	}
	return nil
}

func (a *App) shutdown() {
	a.shutdownOnce.Do(func() {
		a.Log.Info("shutting down")
		// Give plugins a clean unload before the session goes away.
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		a.Plugins.StopAll(ctx)
		cancel()
		if a.Web != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.Web.Stop(ctx)
			cancel()
		}
		a.Plugins.Emit(proto.EventCoreStop, map[string]any{"version": buildinfo.Version})
		_ = a.KV.Close()
		close(a.stopped)
		a.Log.Info("bye")
	})
}

// Shutdown asks the core to stop. It is what the panel's restart button calls.
func (a *App) Shutdown(ctx context.Context) error {
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.closeSubscribers()
		if a.stopFn != nil {
			a.stopFn()
		}
	}()
	<-a.stopped
	return nil
}

func (a *App) closeSubscribers() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, ch := range a.logSubs {
		delete(a.logSubs, id)
		close(ch)
	}
	for id, ch := range a.eventSubs {
		delete(a.eventSubs, id)
		close(ch)
	}
}

func (a *App) writePidFile() error {
	if err := os.MkdirAll(a.Paths.Run, 0o700); err != nil {
		return err
	}
	return os.WriteFile(a.Paths.PidFile(), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

func (a *App) removePidFile() { _ = os.Remove(a.Paths.PidFile()) }

func (a *App) printBanner(cfg config.Config, note string) {
	var b strings.Builder
	b.WriteString("\n  \x1b[35m▚▚▚\x1b[0m  Aurora ")
	b.WriteString("\x1b[2mv" + buildinfo.Version + "\x1b[0m\n")
	if note != "" {
		b.WriteString("  ⚠ " + note + "\n\n")
	}
	b.WriteString("  дані      " + a.Paths.Home + "\n")
	b.WriteString("  плагіни   " + a.Plugins.Root() + "\n")
	if a.Web != nil && a.Web.URL() != "" {
		b.WriteString("  панель     \x1b[4m" + a.Web.URL() + "?token=" + cfg.Web.Token + "\x1b[0m\n")
	} else if cfg.Web.Enabled {
		b.WriteString("  панель     \x1b[2mнедоступна\x1b[0m\n")
	}
	b.WriteString("  RAM-ліміт  " + memLimitText(cfg.Runtime.MemLimitMB) + "\n")
	b.WriteString("\n")
	fmt.Fprint(os.Stderr, b.String())
}

func memLimitText(mb int) string {
	if mb <= 0 {
		return "не обмежено"
	}
	return fmt.Sprintf("%d МБ", mb)
}

// ---- web.Backend ----

// Status implements web.Backend.
func (a *App) Status() proto.Status {
	cfg := a.Cfg.Get()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	st := proto.Status{
		Core:        "aurora",
		Version:     buildinfo.Version,
		GoVersion:   runtime.Version(),
		Uptime:      time.Since(a.startedAt).Round(time.Second).String(),
		UptimeSec:   int64(time.Since(a.startedAt).Seconds()),
		MemoryMB:    float64(m.HeapAlloc) / (1024 * 1024),
		Goroutines:  runtime.NumGoroutine(),
		Session:     a.TG.State(),
		User:        a.TG.Me(),
		PluginCount: len(a.Plugins.Names()),
		PluginsUp:   len(a.Plugins.Running()),
		MemLimitMB:  cfg.Runtime.MemLimitMB,
	}
	if a.Web != nil {
		st.Web = proto.WebStatus{Enabled: true, URL: a.Web.URL()}
	} else {
		st.Web = proto.WebStatus{Enabled: cfg.Web.Enabled}
	}
	return st
}

// Config implements web.Backend.
func (a *App) Config() config.Config { return a.Cfg.Get() }

// SaveConfig implements web.Backend.
func (a *App) SaveConfig(c config.Config) error {
	if err := a.Cfg.Replace(c); err != nil {
		return err
	}
	a.Log.Info("config updated", logx.F("path", a.Cfg.Path()))
	return nil
}

// PluginStats implements web.Backend.
func (a *App) PluginStats() []plugins.Stats { return a.Plugins.Stats() }

// Commands implements web.Backend.
func (a *App) Commands() []plugins.CommandSpec { return a.Plugins.Commands() }

// Command implements web.Backend.
func (a *App) Command(ctx context.Context, name, text string) (string, error) {
	return a.Plugins.Command(ctx, name, text)
}

// Send implements web.Backend.
func (a *App) Send(ctx context.Context, req proto.SendRequest) (proto.SendResult, error) {
	return a.TG.Send(ctx, req)
}

// PluginAction implements web.Backend.
func (a *App) PluginAction(ctx context.Context, name, action string) (string, error) {
	switch action {
	case "start":
		if err := a.Plugins.Start(ctx, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("«%s» запущено", name), nil
	case "stop":
		if err := a.Plugins.Stop(ctx, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("«%s» зупинено", name), nil
	case "restart":
		if err := a.Plugins.Restart(ctx, name); err != nil {
			return "", err
		}
		return fmt.Sprintf("«%s» перезапущено", name), nil
	default:
		return "", fmt.Errorf("невідома дія: %s", action)
	}
}

// PluginInstall implements web.Backend. It clones a git repository into the
// plugin directory and starts it if a valid manifest is present.
func (a *App) PluginInstall(ctx context.Context, source, name string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git не встановлено: pkg install git")
	}
	if name == "" {
		name = guessPluginName(source)
	}
	if name == "" {
		return "", errors.New("не вдалося визначити ім’я плагіна — вкажіть його вручну")
	}
	if _, ok := a.Plugins.Get(name); ok {
		return "", fmt.Errorf("плагін %q вже встановлено", name)
	}
	dst := filepath.Join(a.Plugins.Root(), name)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("каталог %s вже існує", dst)
	}

	a.Log.Info("installing plugin", logx.F("name", name), logx.F("source", source))
	gctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	git := exec.CommandContext(gctx, "git", "clone", "--depth", "1", source, dst)
	if out, err := git.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}

	inst, err := a.Plugins.Install(dst)
	if err != nil {
		return "", err
	}
	if err := a.Plugins.Start(ctx, name); err != nil {
		return fmt.Sprintf("встановлено, але не запустився: %v", err), nil
	}
	return fmt.Sprintf("«%s» %s встановлено та запущено", name, inst.Manifest.Version), nil
}

func guessPluginName(source string) string {
	s := strings.TrimSuffix(strings.TrimSuffix(source, "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(s)
}

// PluginUninstall implements web.Backend.
func (a *App) PluginUninstall(name string) error { return a.Plugins.Uninstall(name, true) }

// Auth implements web.Backend.
func (a *App) Auth() web.AuthState {
	step := a.TG.Step()
	st := web.AuthState{State: step, SignedIn: step == tgc.AuthSignedIn}
	st.Phone = maskPhone(a.Cfg.Get().Telegram.Phone)
	if st.State == tgc.AuthSignedIn && a.TG.State() == proto.StateAuthorized {
		st.Message = "авторизовано"
	}
	return st
}

func maskPhone(p string) string {
	p = strings.TrimSpace(p)
	if len(p) <= 4 {
		return p
	}
	return "***" + p[len(p)-4:]
}

// RequestCode implements web.Backend.
func (a *App) RequestCode(ctx context.Context, phone string) error {
	if phone != "" {
		a.TG.SetPhone(phone)
		_ = a.Cfg.Update(func(c *config.Config) { c.Telegram.Phone = phone })
		a.Log.Info("login phone updated", logx.F("phone", maskPhone(phone)))
	}
	return a.TG.RequestCode(ctx, phone)
}

// SubmitCode implements web.Backend.
func (a *App) SubmitCode(code string) error { return a.TG.SubmitCode(code) }

// SubmitPassword implements web.Backend.
func (a *App) SubmitPassword(password string) error { return a.TG.SubmitPassword(password) }

// Session implements web.Backend.
func (a *App) Session() tgc.SessionInfo { return tgc.InspectSession(a.Paths.SessionFile()) }

// ImportSession implements web.Backend.
func (a *App) ImportSession(s string) error { return a.TG.ImportSession(s) }

// Logout implements web.Backend.
func (a *App) Logout(ctx context.Context) error { return a.TG.Logout(ctx) }

// LogTail implements web.Backend.
func (a *App) LogTail(n int) []logx.Record { return a.Log.Tail(n) }

// SubscribeLogs implements web.Backend.
func (a *App) SubscribeLogs() (<-chan logx.Record, func()) {
	return a.Log.Subscribe()
}

// SubscribeEvents implements web.Backend.
func (a *App) SubscribeEvents() (<-chan proto.Event, func()) {
	ch := make(chan proto.Event, 256)
	a.mu.Lock()
	id := a.nextSub
	a.nextSub++
	a.eventSubs[id] = ch
	// Replay a short backlog so a freshly opened tab is not blind.
	backlog := append([]proto.Event(nil), a.notifyQueue...)
	a.mu.Unlock()

	for _, ev := range backlog {
		select {
		case ch <- ev:
		default:
		}
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			a.mu.Lock()
			if cur, ok := a.eventSubs[id]; ok && cur == ch {
				delete(a.eventSubs, id)
				close(ch)
			}
			a.mu.Unlock()
		})
	}
	return ch, cancel
}

// Notify implements plugins.Services.
func (a *App) Notify(title, text, level string) {
	a.Log.Info("plugin notification", logx.F("title", title), logx.F("level", level))
	a.publish(proto.Event{Name: "ui.notify", Data: proto.NotifyRequest{Title: title, Text: text, Level: level}})
}

func (a *App) publish(ev proto.Event) {
	a.mu.Lock()
	a.notifyQueue = append(a.notifyQueue, ev)
	if len(a.notifyQueue) > 200 {
		a.notifyQueue = a.notifyQueue[len(a.notifyQueue)-200:]
	}
	subs := make([]chan proto.Event, 0, len(a.eventSubs))
	for _, ch := range a.eventSubs {
		subs = append(subs, ch)
	}
	a.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // never block the producer
		}
	}
}

// ConfigValue implements plugins.Services (dotted-path lookup, e.g.
// "telegram.app_id" or "runtime.log_level").
func (a *App) ConfigValue(key string) (any, bool) {
	return lookupJSON(key, a.Cfg.Get())
}

func lookupJSON(key string, v config.Config) (any, bool) {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var root map[string]any
	if json.Unmarshal(buf, &root) != nil {
		return nil, false
	}
	var cur any = root
	for _, part := range strings.Split(key, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[part]
		if !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

// Ready implements plugins.Services.
func (a *App) Ready() bool { return a.TG.Ready() }

// SendServices implements plugins.Services.
func (a *App) Resolve(ctx context.Context, peer string) (proto.PeerInfo, error) {
	return a.TG.Resolve(ctx, peer)
}

// GetMe implements plugins.Services.
func (a *App) GetMe(ctx context.Context) (proto.User, error) { return a.TG.GetMe(ctx) }

// History implements plugins.Services.
func (a *App) History(ctx context.Context, peer string, limit int) ([]proto.Message, error) {
	return a.TG.History(ctx, peer, limit)
}

// LoggedIn reports whether the account is authorized.
func (a *App) LoggedIn() bool { return a.TG.State() == proto.StateAuthorized }

// Restart triggers a full core restart.
func (a *App) Restart() error { return a.Shutdown(context.Background()) }

// PluginNames returns installed plugin names, sorted.
func (a *App) PluginNames() []string {
	names := a.Plugins.Names()
	sort.Strings(names)
	return names
}
