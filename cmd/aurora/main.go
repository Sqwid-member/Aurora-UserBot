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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/app"
	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
)

const usage = `🌌 Aurora — модульний Telegram-юзербот

Використання:
  aurora [команда] [аргументи]

Команди:
  menu                інтерактивний центр керування в терміналі (за замовчуванням)
  run                 запустити ядро (Telegram + плагіни + панель) у відкритій консолі
  start               запустити у фоновому режимі (демон для Termux/Linux)
  stop                зупинити фоновий процес
  restart             перезапустити фоновий процес
  status              перевірити стан процесу та RAM
  logs                перегляд живого журналу логів
  setup               інтерактивне первинне налаштування (app_id, app_hash)
  login               авторизація в Telegram (номер → код → 2FA)
  login qr            вхід тапом по посиланню на цьому ж телефоні (без номера/SMS)
  login web           авторизація у веб-панелі (QR / код / імпорт сесії)
  update              автоматично оновити юзербота до найновішої версії з GitHub
  panel               надрукувати адресу панелі та токен
  send <peer> <текст>  надіслати повідомлення
  plugins             список плагінів
  plugin ls           те саме
  plugin start <ім'я> запустити плагін
  plugin stop <ім'я>  зупинити плагін
  plugin restart <ім'я>
  plugin install <git-url> [ім'я]
  plugin remove <ім'я>
  plugin settings <ім'я> [к=зн...]
                      показати/зберегти налаштування плагіна
	session             інформація про локальну сесію
  session export      надрукувати StringSession (Telethon/Pyrogram)
  backup [файл]       зв'язка конфіг+сесії для переїзду
  restore <файл>      відновити зі зв'язки (ядро має бути зупинене)
  device [--save]     показати/зберегти зліпок пристрою для маскування входу
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
	cmd := ""
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			cmd = "help"
			args = args[1:]
		} else if args[0] == "-v" || args[0] == "--version" {
			cmd = "version"
			args = args[1:]
		} else if !strings.HasPrefix(args[0], "-") {
			cmd, args = args[0], args[1:]
		}
	} else if isCharDevice(os.Stdin) {
		cmd = "menu"
	} else {
		cmd = "run"
	}

	layout, err := paths.Resolve(paths.OSEnv)
	if err != nil {
		return fmt.Errorf("cannot resolve data directory: %w", err)
	}

	switch cmd {
	case "menu", "tui", "dashboard":
		return cmdTUI(layout)
	case "run":
		return cmdRun(layout)
	case "start":
		return cmdStart(layout)
	case "stop":
		return cmdStop(layout)
	case "restart":
		_ = cmdStop(layout)
		time.Sleep(500 * time.Millisecond)
		return cmdStart(layout)
	case "status":
		return cmdStatus(layout)
	case "logs":
		return cmdLogs(layout)
	case "setup":
		return cmdSetup(layout)
	case "login":
		return cmdLogin(layout, args)
	case "panel", "web", "gui", "open":
		return cmdPanel(layout)
	case "send":
		return cmdSend(layout, args)
	case "plugins", "plugin":
		return cmdPlugins(layout, args)
	case "session":
		return cmdSession(layout, args)
	case "backup":
		return cmdBackup(layout, args)
	case "restore":
		return cmdRestore(layout, args)
	case "device":
		return cmdDevice(layout, args)
	case "logout":
		return cmdLogout(layout)
	case "config":
		return cmdConfig(layout)
	case "doctor":
		return cmdDoctor(layout)
	case "update", "upgrade":
		return cmdUpdate(layout, args...)
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
	// Note: defaults (Web API keys) are functional, so Validate() always
	// passes after normalize. The real onboarding gate is sessionExists()
	// below, not the API keys.
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
					if err := a.RequestCode(ctx, phone); err != nil {
						fmt.Fprintf(os.Stderr, "  ✖ Помилка запиту коду: %v\n", err)
						if strings.Contains(strings.ToUpper(err.Error()), "API_ID") || strings.Contains(strings.ToUpper(err.Error()), "FLOOD") {
							if !a.Cfg.Get().UsingCustomAPIKeys() {
								fmt.Fprintln(os.Stderr, "  ⚠ Стандартні ключі Telegram обмежено. Запустіть 'aurora setup' для встановлення власних ключів з my.telegram.org!")
							}
						}
					}
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

func cmdLogin(layout paths.Layout, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "qr", "--qr", "-w":
			return cmdQRLogin(layout)
		}
	}
	if len(args) > 0 && (args[0] == "--web" || args[0] == "web" || args[0] == "-w") {
		return cmdLoginWeb(layout)
	}
	return cmdTerminalLogin(layout)
}

func cmdLoginWeb(layout paths.Layout) error {
	store, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}
	c := store.Get()
	host := c.Web.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d/?token=%s#auth", host, c.Web.Port, c.Web.Token)

	if pid, running := checkPidRunning(layout.PidFile()); running {
		fmt.Printf("✓ Aurora вже працює у фоні (PID %d)\n", pid)
	} else {
		fmt.Println("→ Запускаю фоновий процес Aurora для веб-панелі...")
		if err := cmdStart(layout); err != nil {
			return err
		}
	}

	fmt.Println("\n🌐 Відкрийте посилання для входу у браузері:")
	fmt.Printf("   %s\n\n", url)
	openBrowserCLI(url)
	return nil
}

func openBrowserCLI(url string) {
	var candidates [][]string
	if runtime.GOOS == "android" || paths.IsTermux() {
		candidates = append(candidates,
			[]string{"termux-open-url", url},
			[]string{"termux-open", url},
		)
		if _, err := os.Stat("/system/bin/am"); err == nil {
			candidates = append(candidates, []string{"/system/bin/am", "start", "-a", "android.intent.action.VIEW", "-d", url})
		}
	}
	candidates = append(candidates,
		[]string{"xdg-open", url},
		[]string{"sensible-browser", url},
		[]string{"x-www-browser", url},
		[]string{"open", url},
	)
	for _, c := range candidates {
		if path, err := sysx.LookPath(c[0]); err == nil {
			_ = sysx.Command(path, c[1:]...).Start()
			return
		} else if strings.HasPrefix(c[0], "/") {
			if _, err := os.Stat(c[0]); err == nil {
				_ = sysx.Command(c[0], c[1:]...).Start()
				return
			}
		}
	}
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

	if pid, running := checkPidRunning(layout.PidFile()); running {
		fmt.Printf("✓ Aurora працює у фоні (PID %d)\n", pid)
	} else {
		fmt.Println("→ Запускаю фоновий процес Aurora для веб-панелі...")
		if err := cmdStart(layout); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Println("\n🌐 Відкриваю веб-панель керування:")
	fmt.Printf("   %s\n", url)
	fmt.Println("   (Токен введено автоматично)")
	openBrowserCLI(url)
	return nil
}

// ---- send ----

func cmdSend(layout paths.Layout, args []string) error {
	if len(args) < 2 {
		return errors.New("використання: aurora send <peer> <текст>")
	}
	peer := args[0]
	text := strings.Join(args[1:], " ")

	if client, err := newDaemonClient(layout); err == nil && client.isAlive() {
		if err := client.send(peer, text); err != nil {
			return fmt.Errorf("помилка надсилання через фоновий процес: %w", err)
		}
		fmt.Printf("✓ надіслано до %s (через фоновий процес)\n", peer)
		return nil
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
	// Register on-disk plugins without starting them: Discover alone
	// leaves the host empty, so offline ls/start/settings saw nothing.
	_ = a.Plugins.EnsureInstalled()

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

	case "settings":
		if len(args) == 0 {
			return errors.New("використання: aurora plugin settings <ім'я> [ключ=значення ...]")
		}
		name := args[0]
		if len(args) == 1 {
			fields, err := a.Plugins.PluginSettings(name)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				fmt.Printf("у плагіна %s немає налаштувань\n", name)
				return nil
			}
			for _, f := range fields {
				mark := ""
				if !f.Stored {
					mark = " (дефолт)"
				}
				title := f.Field.Title
				if title == "" {
					title = f.Field.Key
				}
				fmt.Printf("%-20s = %v%s\n  %s\n", f.Field.Key, f.Value, mark, title)
			}
			return nil
		}
		values := make(map[string]any, len(args)-1)
		for _, kv := range args[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return fmt.Errorf("очікується ключ=значення, отримано %q", kv)
			}
			values[strings.TrimSpace(k)] = v
		}
		saved, err := a.Plugins.SetPluginSettings(name, values)
		if err != nil {
			return err
		}
		// The KV store flushes with a debounce; an offline CLI exits
		// immediately, so persist synchronously or the write is lost.
		if err := a.KV.Flush(); err != nil {
			return fmt.Errorf("збережено в пам'яті, але flush на диск не вдався: %w", err)
		}
		fmt.Printf("✓ збережено (%d полів)\n", len(saved))
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
			check{"API ключі", true, func() string {
				if c.UsingCustomAPIKeys() {
					return fmt.Sprintf("власні (%d / %s)", c.Telegram.AppID, maskSecret(c.Telegram.AppHash))
				}
				return "стандартні (Telegram Web K)"
			}()},
			check{"пристрій (маскування)", true, config.DetectedDeviceSummary()},
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

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func checkPidRunning(pidFile string) (int, bool) {
	buf, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, isPidAlive(pid)
}

func isPidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ---- setup ----

func cmdSetup(layout paths.Layout) error {
	store, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}
	c := store.Get()

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("🌌 Налаштування Telegram API ключів Aurora")
	if c.UsingCustomAPIKeys() {
		fmt.Printf("   Зараз активні: ВЛАСНІ ключі (App ID: %d, App Hash: %s)\n", c.Telegram.AppID, maskSecret(c.Telegram.AppHash))
	} else {
		fmt.Println("   Зараз активні: СТАНДАРТНІ публічні ключі Telegram (Web K)")
	}
	fmt.Println("   (За замовчуванням активні стандартні ключі: вхід лише за номером, кодом і паролем)")
	fmt.Println("   Якщо у вас виникають блокування або помилки, ви можете вказати власні ключі з my.telegram.org")
	fmt.Printf("   Пристрій для маскування входу: %s\n", config.DetectedDeviceSummary())
	fmt.Println("   (порожні поля пристрою в конфігу підміняються даними цього телефону автоматично)")
	fmt.Println()

	fmt.Print("Бажаєте встановити власні ключі з my.telegram.org? [y/N]: ")
	ansLine, _ := reader.ReadString('\n')
	ans := strings.TrimSpace(strings.ToLower(ansLine))

	var appID int
	var appHash string

	if ans == "y" || ans == "yes" || ans == "т" || ans == "так" {
		for {
			fmt.Print("Введіть Telegram App ID: ")
			line, _ := reader.ReadString('\n')
			val, err := strconv.Atoi(strings.TrimSpace(line))
			if err == nil && val > 0 {
				appID = val
				break
			}
			fmt.Println("✖ App ID має бути додатним числом")
		}
		for {
			fmt.Print("Введіть Telegram App Hash: ")
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			if len(line) >= 16 {
				appHash = line
				break
			}
			fmt.Println("✖ App Hash має містити щонайменше 16 символів")
		}
	} else {
		appID = 0
		appHash = ""
		fmt.Println("✓ Обрано стандартні ключі Telegram (без потреби в my.telegram.org)")
	}

	fmt.Print("Номер телефону для входу (+380..., або Enter щоб пропустити): ")
	phoneLine, _ := reader.ReadString('\n')
	phone := strings.TrimSpace(phoneLine)

	err = store.Update(func(cfg *config.Config) {
		cfg.Telegram.AppID = appID
		cfg.Telegram.AppHash = appHash
		if phone != "" {
			cfg.Telegram.Phone = phone
		}
	})
	if err != nil {
		return fmt.Errorf("помилка збереження: %w", err)
	}

	fmt.Println("\n✓ Конфігурацію збережено в:", layout.ConfigFile())
	fmt.Println("  Вхід у Telegram (номер -> код -> пароль):")
	fmt.Println("    aurora run     (у терміналі)")
	fmt.Println("    aurora start   (у фоні)")
	return nil
}

// ---- start (daemon) ----

func cmdStart(layout paths.Layout) error {
	if pid, running := checkPidRunning(layout.PidFile()); running {
		return fmt.Errorf("aurora вже працює (PID %d)", pid)
	}

	store, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}
	c := store.Get()
	if err := c.Validate(); err != nil {
		return fmt.Errorf("конфігурація не налаштована: виконайте 'aurora setup'")
	}

	if err := layout.Ensure(); err != nil {
		return err
	}

	// Ensure binary path has leading slash to avoid any LookPath
	bin, err := os.Executable()
	if err != nil || !strings.Contains(bin, "/") {
		if lp, err := sysx.LookPath("aurora"); err == nil {
			bin = lp
		} else {
			bin = "/data/data/com.termux/files/usr/bin/aurora"
		}
	}

	logFile, err := os.OpenFile(layout.LogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("відкриття лог-файлу: %w", err)
	}
	defer logFile.Close()

	cmd := sysx.Command(bin, "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = sysProcAttrDaemon()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск фонового процесу: %w", err)
	}

	fmt.Printf("✓ Aurora запущена у фоні (PID %d)\n", cmd.Process.Pid)
	fmt.Printf("  Логи:    aurora logs\n")
	fmt.Printf("  Статус:  aurora status\n")
	host := c.Web.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	fmt.Printf("  Панель:  http://%s:%d/?token=%s\n", host, c.Web.Port, c.Web.Token)
	fmt.Printf("  Зупинка: aurora stop\n")
	return nil
}

// ---- stop ----

func cmdStop(layout paths.Layout) error {
	pid, running := checkPidRunning(layout.PidFile())
	if !running {
		_ = os.Remove(layout.PidFile())
		fmt.Println("aurora не запущена")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("сигнал зупинки: %w", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if !isPidAlive(pid) {
			break
		}
	}

	if isPidAlive(pid) {
		_ = proc.Kill()
	}
	_ = os.Remove(layout.PidFile())

	fmt.Printf("✓ Aurora (PID %d) зупинена\n", pid)
	return nil
}

// ---- status ----

func cmdStatus(layout paths.Layout) error {
	pid, running := checkPidRunning(layout.PidFile())
	if !running {
		fmt.Println("● Стан: зупинено (inactive)")
		return nil
	}

	fmt.Printf("● Стан: активний (PID %d)\n", pid)
	if rss := readProcessRSS(pid); rss != "" {
		fmt.Printf("  Пам'ять:  %s\n", rss)
	}
	if sessionExists(layout) {
		fmt.Printf("  Сесія:    авторизовано (%s)\n", layout.SessionFile())
	} else {
		fmt.Printf("  Сесія:    не авторизовано (виконайте 'aurora login')\n")
	}

	store, err := config.Open(layout.ConfigFile(), nil)
	if err == nil {
		c := store.Get()
		if c.Web.Enabled {
			host := c.Web.Host
			if host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			fmt.Printf("  Панель:   http://%s:%d/?token=%s\n", host, c.Web.Port, c.Web.Token)
		}
	}
	return nil
}

// ---- logs ----

func cmdLogs(layout paths.Layout) error {
	logPath := layout.LogFile()
	if _, err := os.Stat(logPath); err != nil {
		fmt.Println("Файл логів ще не створений:", logPath)
		return nil
	}

	if tail, err := sysx.LookPath("tail"); err == nil {
		cmd := sysx.Command(tail, "-n", "50", "-f", logPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}
	for _, l := range lines[start:] {
		fmt.Println(l)
	}
	return nil
}
