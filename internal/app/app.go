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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

// App is the assembled Aurora core.
type accountRuntime struct {
	id     string
	title  string
	phone  string
	tg     *tgc.Runtime
	cancel context.CancelFunc
}

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

	accountsMu sync.RWMutex
	accounts   map[string]*accountRuntime
	activeAcc  string

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
		accounts:  make(map[string]*accountRuntime),
	}

	c := cfg.Get()
	for _, accCfg := range c.Accounts {
		a.initAccountRuntime(accCfg)
	}
	a.activeAcc = c.ActiveAccount
	if rt, ok := a.accounts[a.activeAcc]; ok {
		a.TG = rt.tg
	} else if len(a.accounts) > 0 {
		for id, rt := range a.accounts {
			a.activeAcc = id
			a.TG = rt.tg
			break
		}
	}
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

	a.accountsMu.RLock()
	for _, rt := range a.accounts {
		accCtx, accCancel := context.WithCancel(ctx)
		rt.cancel = accCancel
		go a.runTelegramForAccount(accCtx, rt.id, rt.tg)
	}
	a.accountsMu.RUnlock()

	<-ctx.Done()

	a.shutdown()
	return nil
}

func (a *App) runTelegramForAccount(ctx context.Context, id string, tg *tgc.Runtime) {
	backoff := time.Second
	for {
		err := tg.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			return
		}

		// An unauthorized account restarts cheaply and the user is usually
		// watching a login prompt, so keep the delay short: a 2 minute backoff
		// while someone is waiting for a code looks exactly like a hang.
		maxBackoff := 2 * time.Minute
		if tg.State() != proto.StateAuthorized {
			maxBackoff = 8 * time.Second
		}
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		switch {
		case errors.Is(err, tgc.ErrLoginAborted):
			a.Log.Warn("login aborted", logx.F("acc", id))
			backoff = 10 * time.Second
		default:
			a.Log.Error("account runtime stopped",
				logx.F("acc", id),
				logx.F("error", err),
				logx.F("retry_in", backoff),
			)
			backoff = nextBackoff(backoff, maxBackoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
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

// Shutdown asks the core to stop. It is what the panel's stop button calls.
//
// It honours ctx: if the core was never started (or is wedged in its own
// shutdown path) the caller gets ctx.Err() instead of blocking forever.
func (a *App) Shutdown(ctx context.Context) error {
	if a.stopFn == nil {
		return errors.New("core is not running")
	}
	a.closeSubscribers()
	a.stopFn()

	select {
	case <-a.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Restart stops the core and replaces the running process with a fresh one,
// keeping the same argv, the same open file descriptors (so the daemon's log
// file stays attached) and the same pid.
//
// The panel has no parent process to respawn us — `aurora start` exits
// immediately after fork — so a genuine restart has to be done in place.
func (a *App) Restart(ctx context.Context) error {
	if a.stopFn == nil {
		return errors.New("core is not running")
	}
	if err := a.Shutdown(ctx); err != nil {
		return fmt.Errorf("stop before restart: %w", err)
	}
	a.Log.Info("restarting in place", logx.F("version", buildinfo.Version))
	return restartSelf()
}

// closeSubscribers tears down every live log/event channel. It must only be
// called once, from Shutdown, and only with a.mu held by the caller path that
// also serialises publish.
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

	tg := a.getTG("")
	var session proto.SessionState = proto.StateOffline
	var user *proto.User
	if tg != nil {
		session = tg.State()
		user = tg.Me()
	}

	st := proto.Status{
		Core:          "aurora",
		Version:       buildinfo.Version,
		GoVersion:     runtime.Version(),
		Uptime:        time.Since(a.startedAt).Round(time.Second).String(),
		UptimeSec:     int64(time.Since(a.startedAt).Seconds()),
		MemoryMB:      float64(m.HeapAlloc) / (1024 * 1024),
		Goroutines:    runtime.NumGoroutine(),
		Session:       session,
		User:          user,
		PluginCount:   len(a.Plugins.Names()),
		PluginsUp:     len(a.Plugins.Running()),
		MemLimitMB:    cfg.Runtime.MemLimitMB,
		ActiveAccount: a.ActiveAccountID(),
		Accounts:      a.Accounts(),
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
	tg := a.getTG(req.AccountID)
	if tg == nil {
		return proto.SendResult{}, errors.New("no active Telegram account")
	}
	return tg.Send(ctx, req)
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

var safePluginNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

func validatePluginName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("назва плагіна не може бути порожньою")
	}
	if name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return errors.New("некоректна назва плагіна: неприпустимі символи або шлях")
	}
	if !safePluginNameRe.MatchString(name) {
		return fmt.Errorf("некоректна назва плагіна %q: дозволені лише [a-z0-9_.-], до 32 символів", name)
	}
	return nil
}

func validatePluginSource(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return errors.New("порожнє джерело плагіна")
	}
	if strings.HasPrefix(source, "-") {
		return errors.New("некоректне джерело плагіна: аргументи-прапорці заборонені")
	}
	if strings.HasPrefix(source, "git@") {
		return nil
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("некоректний URL плагіна: очікується http(s)://, git:// або ssh://")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http", "git", "ssh":
		return nil
	default:
		return fmt.Errorf("непідтримуваний протокол URL: %q (дозволено лише https, http, git, ssh)", u.Scheme)
	}
}

// PluginInstall implements web.Backend. It clones a git repository into the
// plugin directory and starts it if a valid manifest is present.
func (a *App) PluginInstall(ctx context.Context, source, name string) (string, error) {
	if _, err := sysx.LookPath("git"); err != nil {
		return "", errors.New("git не встановлено: pkg install git")
	}
	if err := validatePluginSource(source); err != nil {
		return "", err
	}
	if name == "" {
		name = guessPluginName(source)
	}
	if err := validatePluginName(name); err != nil {
		return "", err
	}
	if _, ok := a.Plugins.Get(name); ok {
		return "", fmt.Errorf("плагін %q вже встановлено", name)
	}
	dst := filepath.Join(a.Plugins.Root(), name)
	cleanRoot := filepath.Clean(a.Plugins.Root())
	cleanDst := filepath.Clean(dst)
	rel, err := filepath.Rel(cleanRoot, cleanDst)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("каталог призначення плагіна виходить за межі дозволеного")
	}
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("каталог %s вже існує", dst)
	}

	a.Log.Info("installing plugin", logx.F("name", name), logx.F("source", source))
	gctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	git := sysx.CommandContext(gctx, "git", "clone", "--depth", "1", "--", source, dst)
	if out, err := git.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}

	inst, err := a.Plugins.Install(dst)
	if err != nil {
		_ = os.RemoveAll(dst)
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

// Core returns the runtime of the active account, or nil when no account is
// configured. Every caller must handle nil — a half-written config used to
// make the whole panel panic.
func (a *App) Core() *tgc.Runtime { return a.getTG("") }

// tg returns the active runtime or an error suitable for an API response.
func (a *App) tg() (*tgc.Runtime, error) {
	tg := a.getTG("")
	if tg == nil {
		return nil, errors.New("акаунт Telegram не налаштовано — виконайте 'aurora setup'")
	}
	return tg, nil
}

// Auth implements web.Backend.
func (a *App) Auth() web.AuthState { return a.AuthForAccount("") }

func maskPhone(p string) string {
	p = strings.TrimSpace(p)
	if len(p) <= 4 {
		return p
	}
	return "***" + p[len(p)-4:]
}

// RequestCode implements web.Backend.
func (a *App) RequestCode(ctx context.Context, phone string) error {
	return a.RequestCodeForAccount(ctx, "", phone)
}

// SubmitCode implements web.Backend.
func (a *App) SubmitCode(code string) error { return a.SubmitCodeForAccount("", code) }

// SubmitPassword implements web.Backend.
// StartQRLoginForAccount exports a login token and blocks until the user
// approves it in an already authorized Telegram app. It is a no-op when a
// QR flow is already in progress.
func (a *App) StartQRLoginForAccount(ctx context.Context, accID string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.StartQR(ctx)
}

// QRStateForAccount reports the pending login token.
func (a *App) QRStateForAccount(accID string) web.QRState {
	tg := a.getTG(accID)
	if tg == nil {
		return web.QRState{}
	}
	url, expires, running := tg.QRToken()
	return web.QRState{URL: url, Expires: expires, Running: running}
}

// RequestCodeSMSForAccount asks Telegram to deliver the login code over SMS
// instead of the in-app channel.
func (a *App) RequestCodeSMSForAccount(ctx context.Context, accID string, phone string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	if phone != "" {
		tg.SetPhone(phone)
		_ = a.Cfg.Update(func(c *config.Config) {
			if accID == "" || accID == a.activeAcc {
				c.Telegram.Phone = phone
			}
			for i := range c.Accounts {
				if c.Accounts[i].ID == accID || (accID == "" && c.Accounts[i].ID == a.activeAcc) {
					c.Accounts[i].Phone = phone
				}
			}
		})
	}
	return tg.RequestCodeSMS(ctx, tg.Phone())
}

// ResendCodeForAccount asks Telegram to send the login code again through
// the next available channel (SMS / call).
func (a *App) ResendCodeForAccount(ctx context.Context, accID string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.ResendCode(ctx)
}

func (a *App) SignUp(firstName, lastName string) error {
	return a.SignUpForAccount("", firstName, lastName)
}

func (a *App) ResendCode(ctx context.Context) error { return a.ResendCodeForAccount(ctx, "") }

func (a *App) RequestCodeSMS(ctx context.Context, phone string) error {
	return a.RequestCodeSMSForAccount(ctx, "", phone)
}

func (a *App) StartQRLogin(ctx context.Context) error { return a.StartQRLoginForAccount(ctx, "") }

func (a *App) QRState() web.QRState { return a.QRStateForAccount("") }

func (a *App) SubmitPassword(password string) error { return a.SubmitPasswordForAccount("", password) }

// Session implements web.Backend.
func (a *App) Session() tgc.SessionInfo { return tgc.InspectSession(a.Paths.SessionFile()) }

// ImportSession implements web.Backend.
func (a *App) ImportSession(s string) error {
	tg, err := a.tg()
	if err != nil {
		return err
	}
	return tg.ImportSession(s)
}

// ImportWebSession implements web.Backend: stores a Telegram Web
// localStorage export as the local session. Returns the DC used.
func (a *App) ImportWebSession(dc int, payload string) (int, error) {
	tg, err := a.tg()
	if err != nil {
		return 0, err
	}
	return tg.ImportWebSession(payload, dc)
}

// Logout implements web.Backend.
func (a *App) Logout(ctx context.Context) error {
	tg, err := a.tg()
	if err != nil {
		return err
	}
	return tg.Logout(ctx)
}

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

// publish records the event in the replay backlog and hands it to every
// subscriber.
//
// The fan-out happens while a.mu is still held. Every send is non-blocking, so
// this cannot stall the MTProto update loop, and it is what makes
// closeSubscribers safe: a subscriber channel is only ever closed by a
// goroutine that already holds a.mu, so a producer can never be mid-send on a
// channel that is about to be closed.
func (a *App) publish(ev proto.Event) {
	a.mu.Lock()
	a.notifyQueue = append(a.notifyQueue, ev)
	if len(a.notifyQueue) > 200 {
		a.notifyQueue = a.notifyQueue[len(a.notifyQueue)-200:]
	}
	for _, ch := range a.eventSubs {
		select {
		case ch <- ev:
		default: // never block the producer
		}
	}
	a.mu.Unlock()
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
func (a *App) Ready() bool {
	tg := a.getTG("")
	return tg != nil && tg.Ready()
}

// SendServices implements plugins.Services.
func (a *App) Resolve(ctx context.Context, peer string) (proto.PeerInfo, error) {
	tg, err := a.tg()
	if err != nil {
		return proto.PeerInfo{}, err
	}
	return tg.Resolve(ctx, peer)
}

// GetMe implements plugins.Services.
func (a *App) GetMe(ctx context.Context) (proto.User, error) {
	tg, err := a.tg()
	if err != nil {
		return proto.User{}, err
	}
	return tg.GetMe(ctx)
}

// History implements plugins.Services.
func (a *App) History(ctx context.Context, peer string, limit int) ([]proto.Message, error) {
	tg, err := a.tg()
	if err != nil {
		return nil, err
	}
	return tg.History(ctx, peer, limit)
}

// LoggedIn reports whether the account is authorized.
func (a *App) LoggedIn() bool {
	tg := a.getTG("")
	return tg != nil && tg.State() == proto.StateAuthorized
}

// RestartInPlace stops the core and re-execs this binary in place.
func (a *App) RestartInPlace(ctx context.Context) error { return a.Restart(ctx) }

// restartSelf re-execs the current binary with the same argv.
// Used by Restart: the panel has no supervisor to respawn us.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{exe}, os.Args[1:]...)
	return syscallExec(exe, args, os.Environ())
}

// PluginNames returns installed plugin names, sorted.
func (a *App) PluginNames() []string {
	names := a.Plugins.Names()
	sort.Strings(names)
	return names
}

func (a *App) initAccountRuntime(accCfg config.AccountConfig) *accountRuntime {
	opts := a.tgcOptionsForAccount(accCfg)
	rt := &accountRuntime{
		id:    accCfg.ID,
		title: accCfg.Title,
		phone: accCfg.Phone,
		tg:    tgc.New(opts),
	}
	a.accounts[accCfg.ID] = rt
	return rt
}

func (a *App) tgcOptionsForAccount(acc config.AccountConfig) tgc.Options {
	c := a.Cfg.Get()
	sink := a.Log.Scoped("acc:" + acc.ID)
	return tgc.Options{
		AppID:          acc.EffectiveAppID(c.Telegram.AppID),
		AppHash:        acc.EffectiveAppHash(c.Telegram.AppHash),
		Phone:          acc.Phone,
		SessionPath:    a.Paths.AccountSessionFile(acc.ID),
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
		Logger:         sink,
		OnState: func(s proto.SessionState, err string) {
			sink.Info("session state", logx.F("state", string(s)), logx.F("error", err))
			if s == proto.StateError || strings.Contains(strings.ToLower(err), "api_id") || strings.Contains(strings.ToLower(err), "flood") {
				if !c.UsingCustomAPIKeys() {
					sink.Warn("⚠ Можлива проблема зі стандартними ключами Telegram. Встановіть власні через 'aurora setup'", logx.F("error", err))
				}
			}
			switch s {
			case proto.StateAuthorized:
				tg := a.getTG(acc.ID)
				if tg != nil {
					a.emitForAccount(acc.ID, proto.EventSessionStart, tg.Me())
				}
			case proto.StateOffline, proto.StateError:
				a.emitForAccount(acc.ID, proto.EventSessionEnd, map[string]string{"account_id": acc.ID, "state": string(s), "error": err})
			}
			a.publish(proto.Event{
				AccountID: acc.ID,
				Name:      "core.state",
				Data:      map[string]string{"account_id": acc.ID, "state": string(s), "error": err},
			})
		},
		OnEvent: func(name string, data any) {
			a.emitForAccount(acc.ID, name, data)
		},
	}
}

func (a *App) getTG(id string) *tgc.Runtime {
	a.accountsMu.RLock()
	defer a.accountsMu.RUnlock()
	if id == "" {
		id = a.activeAcc
	}
	if rt, ok := a.accounts[id]; ok && rt.tg != nil {
		return rt.tg
	}
	return a.TG
}

func (a *App) emitForAccount(accID string, name string, data any) {
	a.Plugins.EmitForAccount(accID, name, data, func(accountID, pluginName string) bool {
		return a.IsPluginEnabledForAccount(accountID, pluginName)
	})
	a.publish(proto.Event{
		AccountID: accID,
		Name:      name,
		Data:      data,
	})
}

func (a *App) IsPluginEnabledForAccount(accID, pluginName string) bool {
	c := a.Cfg.Get()
	acc, ok := c.GetAccount(accID)
	if !ok {
		return true
	}
	return acc.IsPluginEnabled(pluginName)
}

func (a *App) Accounts() []proto.AccountInfo {
	a.accountsMu.RLock()
	defer a.accountsMu.RUnlock()

	c := a.Cfg.Get()
	var out []proto.AccountInfo
	for _, accCfg := range c.Accounts {
		info := proto.AccountInfo{
			ID:             accCfg.ID,
			Title:          accCfg.Title,
			Phone:          accCfg.Phone,
			EnabledPlugins: accCfg.EnabledPlugins,
			IsActive:       accCfg.ID == a.activeAcc,
		}
		if rt, ok := a.accounts[accCfg.ID]; ok && rt.tg != nil {
			info.Session = rt.tg.State()
			info.User = rt.tg.Me()
		} else {
			info.Session = proto.StateOffline
		}
		out = append(out, info)
	}
	return out
}

func (a *App) ActiveAccountID() string {
	a.accountsMu.RLock()
	defer a.accountsMu.RUnlock()
	return a.activeAcc
}

func (a *App) SetActiveAccount(id string) error {
	a.accountsMu.Lock()
	rt, ok := a.accounts[id]
	if !ok {
		a.accountsMu.Unlock()
		return fmt.Errorf("account %q not found", id)
	}
	a.activeAcc = id
	a.TG = rt.tg
	a.accountsMu.Unlock()

	_ = a.Cfg.Update(func(c *config.Config) {
		c.ActiveAccount = id
	})

	a.publish(proto.Event{
		AccountID: id,
		Name:      "account.activated",
		Data:      map[string]any{"id": id},
	})
	return nil
}

func (a *App) AddAccount(title, phone string, appID int, appHash string) (proto.AccountInfo, error) {
	id := fmt.Sprintf("acc_%d", time.Now().UnixNano()/1e6)
	if title == "" {
		if phone != "" {
			title = phone
		} else {
			title = "Акаунт " + id[4:8]
		}
	}

	accCfg := config.AccountConfig{
		ID:             id,
		Title:          title,
		Phone:          phone,
		AppID:          appID,
		AppHash:        appHash,
		EnabledPlugins: nil,
	}

	err := a.Cfg.Update(func(c *config.Config) {
		c.Accounts = append(c.Accounts, accCfg)
		c.ActiveAccount = id
	})
	if err != nil {
		return proto.AccountInfo{}, err
	}

	a.accountsMu.Lock()
	rt := a.initAccountRuntime(accCfg)
	a.activeAcc = id
	a.TG = rt.tg
	a.accountsMu.Unlock()

	ctx := context.Background()
	accCtx, accCancel := context.WithCancel(ctx)
	rt.cancel = accCancel
	go a.runTelegramForAccount(accCtx, id, rt.tg)

	a.publish(proto.Event{
		AccountID: id,
		Name:      "account.added",
		Data:      map[string]any{"id": id, "title": title, "phone": phone},
	})

	return proto.AccountInfo{
		ID:             id,
		Title:          title,
		Phone:          phone,
		Session:        proto.StateOffline,
		EnabledPlugins: nil,
		IsActive:       true,
	}, nil
}

func (a *App) RemoveAccount(id string) error {
	a.accountsMu.Lock()
	defer a.accountsMu.Unlock()

	if len(a.accounts) <= 1 {
		return errors.New("не можна видалити єдиний активний акаунт")
	}

	rt, ok := a.accounts[id]
	if !ok {
		return fmt.Errorf("акаунт %q не знайдено", id)
	}

	if rt.cancel != nil {
		rt.cancel()
	}
	_ = rt.tg.Logout(context.Background())
	_ = os.Remove(a.Paths.AccountSessionFile(id))
	delete(a.accounts, id)

	var nextActive string
	for otherID := range a.accounts {
		nextActive = otherID
		break
	}
	if a.activeAcc == id {
		a.activeAcc = nextActive
		if nextRT, ok := a.accounts[nextActive]; ok {
			a.TG = nextRT.tg
		}
	}

	_ = a.Cfg.Update(func(c *config.Config) {
		var newAccs []config.AccountConfig
		for _, acc := range c.Accounts {
			if acc.ID != id {
				newAccs = append(newAccs, acc)
			}
		}
		c.Accounts = newAccs
		c.ActiveAccount = a.activeAcc
	})

	a.publish(proto.Event{
		AccountID: id,
		Name:      "account.removed",
		Data:      map[string]any{"id": id, "active": a.activeAcc},
	})
	return nil
}

func (a *App) ToggleAccountPlugin(accID, pluginName string) (bool, error) {
	var enabled bool
	err := a.Cfg.Update(func(c *config.Config) {
		var errToggle error
		enabled, errToggle = c.ToggleAccountPlugin(accID, pluginName, a.PluginNames())
		if errToggle != nil {
			a.Log.Warn("toggle plugin error", logx.F("error", errToggle))
		}
	})
	if err != nil {
		return false, err
	}
	a.publish(proto.Event{
		AccountID: accID,
		Name:      "account.plugin.toggle",
		Data: map[string]any{
			"account_id": accID,
			"plugin":     pluginName,
			"enabled":    enabled,
		},
	})
	return enabled, nil
}

func (a *App) AuthForAccount(accID string) web.AuthState {
	tg := a.getTG(accID)
	if tg == nil {
		return web.AuthState{State: proto.AuthNone, Message: "акаунт не налаштовано"}
	}
	step := tg.Step()
	st := web.AuthState{
		State:     step,
		SignedIn:  step == tgc.AuthSignedIn,
		Connected: tg.Connected(),
		AppOnly:   tg.CodeAppOnly(),
		Session:   tg.State(),
	}
	st.Phone = maskPhone(tg.Phone())
	switch {
	case step == tgc.AuthSignedIn && tg.State() == proto.StateAuthorized:
		st.Message = "авторизовано"
	case !st.Connected:
		st.Message = "підключення до Telegram…"
	case step == tgc.AuthCode:
		st.Message = "чекаємо на код підтвердження"
		if info := tg.CodeInfo(); info != "" {
			st.Message += " (" + info + ")"
		}
		// Telegram only allows the fallback channel after the code times
		// out, so the UI can say when resend becomes useful.
		if next, at := tg.CodeNext(); next != "" {
			st.Message += "; наступний канал " + next + " з " + at.Format("15:04:05")
		} else if tg.CodeAppOnly() {
			st.Message += "; SMS для сторонніх клієнтів вимкнено Telegram — " +
				"потрібен вхід на офіційному клієнті та aurora session import"
		}
	case step == tgc.AuthSignup:
		st.Message = "номер не зареєстровано — введіть ім'я для створення акаунта"
	case step == tgc.AuthPassword:
		st.Message = "потрібен пароль 2FA"
	}
	return st
}

func (a *App) RequestCodeForAccount(ctx context.Context, accID string, phone string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано — виконайте 'aurora setup'")
	}
	if phone != "" {
		tg.SetPhone(phone)
		_ = a.Cfg.Update(func(c *config.Config) {
			if accID == "" || accID == a.activeAcc {
				c.Telegram.Phone = phone
			}
			for i := range c.Accounts {
				if c.Accounts[i].ID == accID || (accID == "" && c.Accounts[i].ID == a.activeAcc) {
					c.Accounts[i].Phone = phone
					break
				}
			}
		})
		a.Log.Info("login phone updated", logx.F("phone", maskPhone(phone)))
	}
	return tg.RequestCode(ctx, phone)
}

func (a *App) SubmitCodeForAccount(accID string, code string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.SubmitCode(code)
}

// SignUpForAccount registers a phone number that Telegram does not know yet
// and finishes the login for it.
func (a *App) SignUpForAccount(accID string, firstName, lastName string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.SubmitSignup(firstName, lastName)
}

func (a *App) SubmitPasswordForAccount(accID string, password string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.SubmitPassword(password)
}

func (a *App) SessionForAccount(accID string) proto.SessionInfo {
	return tgc.InspectSession(a.Paths.AccountSessionFile(accID))
}

func (a *App) ImportSessionForAccount(accID string, s string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.ImportSession(s)
}

// ImportWebSessionForAccount is the per-account Telegram Web import.
func (a *App) ImportWebSessionForAccount(accID string, dc int, payload string) (int, error) {
	tg := a.getTG(accID)
	if tg == nil {
		return 0, errors.New("акаунт Telegram не налаштовано")
	}
	return tg.ImportWebSession(payload, dc)
}

func (a *App) LogoutForAccount(ctx context.Context, accID string) error {
	tg := a.getTG(accID)
	if tg == nil {
		return errors.New("акаунт Telegram не налаштовано")
	}
	return tg.Logout(ctx)
}
