// Command aurora is a modular Telegram userbot that runs anywhere Go does —
// including Termux on a phone.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/app"
	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
)

const usage = `🌌 Aurora — модульний Telegram-юзербот

Використання:
  aurora [команда] [аргументи]

Команди:
  run                 запустити ядро (Telegram + плагіни + панель) — за замовчуванням
  login               інтерактивний вхід у Telegram
  panel               надрукувати адресу панелі та токен
  send <peer> <текст>  надіслати повідомлення
  plugins             список плагінів
  plugin ls           те саме
  plugin start <ім'я> запустити плагін
  plugin stop <ім'я>  зупинити плагін
  plugin restart <ім'я>
  plugin install <git-url> [ім'я]
  plugin remove <ім'я>
  session             інформація про локальну сесію
  session export      надрукувати StringSession (Telethon/Pyrogram)
  logout              завершити сесію на стороні Telegram
  config              показати шлях і вміст конфігурації
  doctor              перевірка оточення
  version             версія
  help                ця довідка

Змінні оточення:
  AURORA_HOME         каталог даних (за замовчуванням ~/.local/share/aurora)

Приклади:
  aurora run
  aurora send @durov привіт
  aurora plugin install https://github.com/me/aurora-plugin-echo
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "✖ "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	layout, err := paths.Resolve(paths.OSEnv)
	if err != nil {
		return fmt.Errorf("cannot resolve data directory: %w", err)
	}

	switch cmd {
	case "run":
		return cmdRun(layout)
	case "login":
		return cmdLogin(layout)
	case "panel":
		return cmdPanel(layout)
	case "send":
		return cmdSend(layout, args)
	case "plugins", "plugin":
		return cmdPlugins(layout, args)
	case "session":
		return cmdSession(layout, args)
	case "logout":
		return cmdLogout(layout)
	case "config":
		return cmdConfig(layout)
	case "doctor":
		return cmdDoctor(layout)
	case "version":
		fmt.Println("aurora " + buildinfo.String())
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("невідома команда: %s", cmd)
	}
}

func newLogger(level logx.Level, scope string) *logx.Logger {
	return logx.New(logx.Options{
		Level: level,
		Color: shouldColor(),
		Sink:  os.Stderr,
	}, scope)
}

func shouldColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func mustApp(layout paths.Layout, scope string) (*app.App, error) {
	if err := layout.Ensure(); err != nil {
		return nil, err
	}
	return app.New(layout, logx.LevelInfo, shouldColor(), false)
}

// ---- run ----

func cmdRun(layout paths.Layout) error {
	a, err := mustApp(layout, "core")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// When the session is not authorized yet, ask for the code on stdin so
	// a fresh Termux install works with no browser and no extra steps.
	if a.Cfg.Get().Validate() == nil && !sessionExists(layout) {
		go promptForLogin(ctx, a)
	}

	fmt.Fprintln(os.Stderr, "  Ctrl+C — зупинити Aurora")
	return a.Run(ctx)
}

func sessionExists(layout paths.Layout) bool {
	return tgc.InspectSession(layout.SessionFile()).Exists
}

// promptForLogin watches the auth state and prompts on stdin when needed.
func promptForLogin(ctx context.Context, a *app.App) {
	reader := bufio.NewReader(os.Stdin)
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()

	askedCode, askedPass, askedPhone := false, false, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			switch a.Auth().State {
			case tgc.AuthPhone:
				if askedPhone {
					continue
				}
				askedPhone = true
				fmt.Fprint(os.Stderr, "  Номер телефону (+380…): ")
				line, _ := reader.ReadString('\n')
				phone := strings.TrimSpace(line)
				if phone != "" {
					_ = a.RequestCode(ctx, phone)
				}
				askedCode = false
			case tgc.AuthCode:
				if askedCode {
					continue
				}
				askedCode = true
				fmt.Fprint(os.Stderr, "  Код із Telegram: ")
				line, _ := reader.ReadString('\n')
				_ = a.SubmitCode(strings.TrimSpace(line))
			case tgc.AuthPassword:
				if askedPass {
					continue
				}
				askedPass = true
				fmt.Fprint(os.Stderr, "  Пароль 2FA: ")
				line, _ := reader.ReadString('\n')
				_ = a.SubmitPassword(strings.TrimSpace(line))
			case tgc.AuthSignedIn:
				fmt.Fprintln(os.Stderr, "  ✓ авторизовано")
				return
			}
		}
	}
}

// ---- login ----

func cmdLogin(layout paths.Layout) error {
	a, err := mustApp(layout, "core")
	if err != nil {
		return err
	}
	if err := a.Cfg.Get().Validate(); err != nil {
		return err
	}
	if sessionExists(layout) {
		fmt.Println("✓ сесія вже існує:", layout.SessionFile())
		fmt.Println("  Щоб увійти заново: aurora logout && rm", layout.SessionFile())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go promptForLogin(ctx, a)

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Minute):
			fmt.Fprintln(os.Stderr, "⏱  час вичерпано")
			stop()
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := a.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	fmt.Println("✓ готово")
	return nil
}

// ---- panel ----

func cmdPanel(layout paths.Layout) error {
	cfgStore, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}
	c := cfgStore.Get()
	if !c.Web.Enabled {
		return errors.New("панель вимкнена: web.enabled = true у config.json")
	}
	host := c.Web.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d/?token=%s", host, c.Web.Port, c.Web.Token)
	fmt.Println(url)
	if runtime.GOOS != "android" {
		fmt.Fprintln(os.Stderr, "\nВідкрийте посилання у браузері. Ядро має бути запущене (aurora run).")
	}
	return nil
}

// ---- send ----

func cmdSend(layout paths.Layout, args []string) error {
	if len(args) < 2 {
		return errors.New("використання: aurora send <peer> <текст>")
	}
	a, err := mustApp(layout, "core")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if !a.Ready() {
		// Bring the session up just for this call.
		go func() { _ = a.TG.Run(ctx) }()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) && !a.Ready() {
			time.Sleep(500 * time.Millisecond)
		}
		if !a.Ready() {
			return errors.New("сесія Telegram не готова — перевірте логін")
		}
	}
	res, err := a.Send(ctx, proto.SendRequest{Peer: args[0], Text: strings.Join(args[1:], " ")})
	if err != nil {
		return err
	}
	fmt.Printf("✓ надіслано (peer %d, msg %d)\n", res.PeerID, res.ID)
	return nil
}

// ---- plugins ----

func cmdPlugins(layout paths.Layout, args []string) error {
	a, err := mustApp(layout, "core")
	if err != nil {
		return err
	}
	_, _ = a.Plugins.Discover()

	sub := "ls"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	switch sub {
	case "ls", "list":
		stats := a.Plugins.Stats()
		if len(stats) == 0 {
			fmt.Println("Плагінів не знайдено у", a.Plugins.Root())
			return nil
		}
		fmt.Printf("%-20s %-10s %-10s %-8s %s\n", "ІМ'Я", "ВЕРСІЯ", "СТАТУС", "МОВА", "ОПИС")
		for _, s := range stats {
			fmt.Printf("%-20s %-10s %-10s %-8s %s\n", s.Name, s.Version, s.State, s.Language, s.Description)
		}
		return nil

	case "start", "stop", "restart":
		if len(args) == 0 {
			return fmt.Errorf("використання: aurora plugin %s <ім'я>", sub)
		}
		if _, ok := a.Plugins.Get(args[0]); !ok {
			return fmt.Errorf("плагін %q не встановлено", args[0])
		}
		var msg string
		switch sub {
		case "start":
			msg, err = a.PluginAction(ctx, args[0], "start")
		case "stop":
			msg, err = a.PluginAction(ctx, args[0], "stop")
		case "restart":
			msg, err = a.PluginAction(ctx, args[0], "restart")
		}
		if err != nil {
			return err
		}
		fmt.Println("✓", msg)
		return nil

	case "install":
		if len(args) == 0 {
			return errors.New("використання: aurora plugin install <git-url> [ім'я]")
		}
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		msg, err := a.PluginInstall(ctx, args[0], name)
		if err != nil {
			return err
		}
		fmt.Println("✓", msg)
		return nil

	case "remove", "rm", "uninstall":
		if len(args) == 0 {
			return errors.New("використання: aurora plugin remove <ім'я>")
		}
		if err := a.PluginUninstall(args[0]); err != nil {
			return err
		}
		fmt.Printf("✓ плагін %s видалено\n", args[0])
		return nil

	default:
		return fmt.Errorf("невідома підкоманда: plugin %s", sub)
	}
}

// ---- session ----

func cmdSession(layout paths.Layout, args []string) error {
	if len(args) > 0 && args[0] == "export" {
		s, err := tgc.ExportSession(layout.SessionFile())
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	}
	if len(args) > 0 && args[0] == "import" {
		if len(args) < 2 {
			return errors.New("використання: aurora session import <StringSession>")
		}
		a, err := mustApp(layout, "core")
		if err != nil {
			return err
		}
		if err := a.ImportSession(args[1]); err != nil {
			return err
		}
		fmt.Println("✓ сесію імпортовано")
		return nil
	}
	info := tgc.InspectSession(layout.SessionFile())
	if !info.Exists {
		fmt.Println("сесії немає:", layout.SessionFile())
		return nil
	}
	fmt.Printf("DC        %d\nАдреса    %s\nauth_key  %s\nФайл      %s\n",
		info.DC, info.Address, info.AuthKeyID, layout.SessionFile())
	return nil
}

func cmdLogout(layout paths.Layout) error {
	a, err := mustApp(layout, "core")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	go func() { _ = a.TG.Run(ctx) }()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && a.TG.State() == proto.StateOffline {
		time.Sleep(500 * time.Millisecond)
	}
	if err := a.Logout(ctx); err != nil {
		return err
	}
	fmt.Println("✓ сесію закрито")
	return nil
}

// ---- config ----

func cmdConfig(layout paths.Layout) error {
	store, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}
	c := store.Get()
	fmt.Println("#", store.Path())
	buf, _ := proto.MarshalIndent(c)
	fmt.Println(string(buf))
	if err := c.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "\n⚠ "+err.Error())
		fmt.Fprintln(os.Stderr, "  Отримайте app_id і app_hash на https://my.telegram.org → API development tools")
		return nil
	}
	fmt.Println("\n✓ конфігурація готова")
	return nil
}

// ---- doctor ----

func cmdDoctor(layout paths.Layout) error {
	type check struct {
		name string
		ok   bool
		info string
	}
	checks := []check{
		{"архітектура", true, runtime.GOARCH + " / " + runtime.GOOS},
		{"каталог даних", dirOK(layout.Home), layout.Home},
		{"конфіг", fileOK(layout.ConfigFile()), layout.ConfigFile()},
		{"KV", fileOK(layout.DBFile()), layout.DBFile()},
		{"каталог плагінів", dirOK(layout.Plugins), layout.Plugins},
	}

	store, err := config.Open(layout.ConfigFile(), nil)
	if err == nil {
		c := store.Get()
		checks = append(checks,
			check{"app_id", c.Telegram.AppID > 0, fmt.Sprintf("%d", c.Telegram.AppID)},
			check{"app_hash", strings.TrimSpace(c.Telegram.AppHash) != "", maskSecret(c.Telegram.AppHash)},
			check{"сесія", tgc.InspectSession(layout.SessionFile()).Exists, layout.SessionFile()},
		)
	}
	for _, b := range []string{"git", "python", "node", "lua"} {
		_, err := lookPath(b)
		checks = append(checks, check{"runtime: " + b, err == nil, runtimeNote(b, err)})
	}

	fmt.Println("🌌 Aurora doctor —", buildinfo.String())
	fmt.Println()
	okAll := true
	for _, c := range checks {
		mark := "✖"
		if c.ok {
			mark = "✔"
		} else {
			okAll = false
		}
		fmt.Printf("  %s %-20s %s\n", mark, c.name, c.info)
	}

	if m := runtimeMem(); m != "" {
		fmt.Println("\n  пам'ять процесу:", m)
	}
	fmt.Println()
	if okAll {
		fmt.Println("  все готово. Запуск: aurora run")
	} else {
		fmt.Println("  є зауваження — виправте їх перед стартом (див. aurora help)")
	}
	return nil
}

func runtimeNote(bin string, err error) string {
	if err == nil {
		return "знайдено"
	}
	switch bin {
	case "git":
		return "встановіть: pkg install git"
	case "python":
		return "встановіть: pkg install python"
	case "node":
		return "встановіть: pkg install nodejs"
	case "lua":
		return "встановіть: pkg install lua"
	default:
		return "не знайдено"
	}
}

func dirOK(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileOK(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func maskSecret(s string) string {
	if len(s) <= 6 {
		return "••••"
	}
	return s[:3] + "…" + s[len(s)-3:]
}
