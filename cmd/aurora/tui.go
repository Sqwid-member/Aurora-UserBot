package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tui"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

// ---- entry ----

// cmdTUI runs the full-screen dashboard. When the process has no terminal
// it falls back to the plain numbered menu, so scripts keep working.
func cmdTUI(layout paths.Layout) error {
	app, err := tui.NewApp()
	if err != nil {
		return cmdLegacyMenu(layout)
	}
	c := newCtl(layout, app)
	c.refresh()
	if err := app.Run(newMainView(c)); err != nil {
		return err
	}
	fmt.Println("До зустрічі!")
	return nil
}

// ---- controller ----

// ctl is the shared state behind every screen: it polls the core over the
// panel API and keeps a snapshot the views can render without blocking.
type ctl struct {
	app    *tui.App
	layout paths.Layout

	mu          sync.Mutex
	status      *statusResponse
	statusErr   string
	auth        *web.AuthState
	authErr     string
	plugins     []pluginItem
	pluginErr   string
	pid         int
	running     bool
	panel       string
	authAppOnly bool
	refreshing  bool
	spins       int
	ticks       int
	notice      string
	noticeErr   bool
	noticeAt    time.Time
	busy        string
}

// snap is an immutable copy of the controller state for one frame.
type snap struct {
	running  bool
	pid      int
	rss      string
	status   *statusResponse
	statusE  string
	auth     *web.AuthState
	authE    string
	plugins  []pluginItem
	plugE    string
	notice   string
	nErr     bool
	nOn      bool
	busy     string
	spins    int
	url      string
	authOnly bool
}

func newCtl(layout paths.Layout, app *tui.App) *ctl {
	return &ctl{app: app, layout: layout}
}

func (c *ctl) snapshot() snap {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := snap{
		running:  c.running,
		pid:      c.pid,
		status:   c.status,
		statusE:  c.statusErr,
		auth:     c.auth,
		authE:    c.authErr,
		plugins:  c.plugins,
		plugE:    c.pluginErr,
		notice:   c.notice,
		nErr:     c.noticeErr,
		busy:     c.busy,
		spins:    c.spins,
		url:      c.panel,
		authOnly: c.authAppOnly,
	}
	if time.Since(c.noticeAt) < 5*time.Second {
		s.nOn = c.notice != ""
	}
	if s.running {
		s.rss = readProcessRSS(c.pid)
	}
	return s
}

// toast shows a transient message on the status line.
func (c *ctl) toast(msg string, isErr bool) {
	c.mu.Lock()
	c.notice, c.noticeErr, c.noticeAt = msg, isErr, time.Now()
	c.mu.Unlock()
	c.app.Wake()
}

// refresh polls the core in the background; concurrent calls are folded
// into the one already in flight.
func (c *ctl) refresh() {
	c.mu.Lock()
	if c.refreshing {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.refreshing = false
			c.mu.Unlock()
			c.app.Wake()
		}()

		client, cErr := newDaemonClient(c.layout)
		pid, running := checkPidRunning(c.layout.PidFile())

		var (
			st      *statusResponse
			stErr   string
			auth    *web.AuthState
			authErr string
			plugins []pluginItem
			plugErr string
			url     string
			// authOnly mirrors AuthState.AppOnly: the code has nowhere to go.
			authOnly bool
		)
		switch {
		case cErr != nil:
			stErr, authErr = cErr.Error(), cErr.Error()
		default:
			url = client.baseURL + "/?token=" + client.token
			if client.isAlive() {
				if s, err := client.getStatus(); err == nil {
					st = s
				} else {
					stErr = err.Error()
				}
				if a, err := client.getAuth(); err == nil {
					auth = a
					authOnly = a.AppOnly
				} else {
					authErr = err.Error()
				}
				if p, err := client.getPlugins(); err == nil {
					plugins = p
				} else {
					plugErr = err.Error()
				}
			}
		}

		c.mu.Lock()
		c.status, c.statusErr = st, stErr
		c.auth, c.authErr = auth, authErr
		c.plugins, c.pluginErr = plugins, plugErr
		c.pid, c.running = pid, running
		c.panel = url
		c.authAppOnly = authOnly
		c.mu.Unlock()
	}()
}

// tick advances the spinner at 100ms and refreshes daemon state every ~1.5s.
func (c *ctl) tick() {
	c.mu.Lock()
	c.spins++
	c.ticks++
	do := c.ticks%15 == 0
	c.mu.Unlock()
	if do {
		c.refresh()
	}
}

// run executes a core action off the UI thread, with a busy indicator.
func (c *ctl) run(label string, fn func() error) {
	c.mu.Lock()
	if c.busy != "" {
		running := c.busy
		c.mu.Unlock()
		c.toast("зачекайте: "+running, true)
		return
	}
	c.busy = label
	c.mu.Unlock()
	c.app.Wake()

	go func() {
		err := fn()
		c.mu.Lock()
		c.busy = ""
		c.mu.Unlock()
		if err != nil {
			c.toast(label+": "+err.Error(), true)
		} else {
			c.toast(label+" ✓", false)
		}
		c.refresh()
		c.app.Wake()
	}()
}

// withClient builds a client for the current config and runs fn with it.
func (c *ctl) withClient(fn func(*daemonClient) error) func() error {
	return func() error {
		client, err := newDaemonClient(c.layout)
		if err != nil {
			return err
		}
		return fn(client)
	}
}

// panelURL returns the tokened panel address, or "" when the config cannot
// be read.
func (c *ctl) panelURL() string {
	client, err := newDaemonClient(c.layout)
	if err != nil {
		return ""
	}
	return client.baseURL + "/?token=" + client.token
}

// quiet redirects stdout while fn runs so CLI helpers do not scribble over
// the alternate screen.
func quiet(fn func() error) error {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fn()
	}
	saved := os.Stdout
	os.Stdout = devnull
	defer func() {
		os.Stdout = saved
		_ = devnull.Close()
	}()
	return fn()
}

// ---- main screen ----

type mainView struct {
	c    *ctl
	list tui.List
}

func (v *mainView) Tick() {
	v.c.tick()
}

func newMainView(c *ctl) *mainView {
	v := &mainView{c: c}
	v.list = tui.List{Items: []tui.ListItem{
		{ID: "login", Title: "Вхід у Telegram", Desc: "номер → код → 2FA", Badge: "1", BadgeColor: tui.ColorKey},
		{ID: "qr", Title: "Вхід по QR", Desc: "без номера і коду — підтвердіть у Telegram", Badge: "w", BadgeColor: tui.ColorKey},
		{ID: "panel", Title: "Відкрити веб-панель", Desc: "керування у браузері", Badge: "2", BadgeColor: tui.ColorKey},
		{ID: "start", Title: "Запустити у фоні", Desc: "aurora start", Badge: "3", BadgeColor: tui.ColorKey},
		{ID: "stop", Title: "Зупинити", Desc: "aurora stop", Badge: "4", BadgeColor: tui.ColorKey},
		{ID: "restart", Title: "Перезапустити", Desc: "aurora restart", Badge: "5", BadgeColor: tui.ColorKey},
		{ID: "logs", Title: "Живий журнал логів", Desc: "останні рядки у вікні", Badge: "6", BadgeColor: tui.ColorKey},
		{ID: "plugins", Title: "Плагіни", Desc: "список і перемикання", Badge: "7", BadgeColor: tui.ColorKey},
		{ID: "gc", Title: "Очистити пам'ять (GC)", Desc: "примусовий збір сміття", Badge: "8", BadgeColor: tui.ColorKey},
		{ID: "setup", Title: "Налаштувати API ключі", Desc: "my.telegram.org", Badge: "9", BadgeColor: tui.ColorKey},
		{ID: "info", Title: "Сеанс і діагностика", Desc: "aurora doctor", Badge: "i", BadgeColor: tui.ColorKey},
		{ID: "update", Title: "Оновити юзербота", Desc: "aurora update", Badge: "u", BadgeColor: tui.ColorKey},
		{ID: "logout", Title: "Вийти з акаунта", Desc: "завершити сесію Telegram", Badge: "0", BadgeColor: tui.ColorKey},
		{ID: "quit", Title: "Вийти з меню", Desc: "aurora exit", Badge: "q", BadgeColor: tui.ColorKey},
	}}
	return v
}

func (v *mainView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	s := v.c.snapshot()
	accent := tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect}

	// header
	f.FillLine(0, 0, w, "", accent)
	title := " AURORA USERBOT "
	f.TextLimit(1, 0, tui.Width(title), title, tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})
	f.TextLimit(1+tui.Width(title)+2, 0, 40, "центр керування",
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	right := buildinfo.Version
	f.FillLine(w-tui.Width(right)-1, 0, tui.Width(right)+1, right,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	// status panel
	rows := statusRows(s)
	switch {
	case h >= 20:
	case h >= 16:
		if len(rows) > 3 {
			rows = rows[:3]
		}
	default:
		if len(rows) > 2 {
			rows = rows[:2]
		}
	}
	ph := len(rows) + 2
	py := 2
	if h < 16 {
		py = 1
	}
	px, pw := 2, w-4
	if pw < 12 {
		px, pw = 0, w
	}
	f.Box(px, py, pw, ph, tui.Style{Fg: tui.ColorBorder}, " Стан ")
	for i, r := range rows {
		y := py + 1 + i
		f.TextLimit(px+2, y, 16, tui.Pad(r.label, 16), tui.Style{Fg: tui.ColorDim})
		f.TextLimit(px+19, y, pw-21, r.value, r.style)
	}

	// menu
	ruleY := py + ph
	f.Rule(ruleY)
	listTop := ruleY + 1
	if h >= 26 {
		f.FillLine(2, listTop, w-4, " ДІЇ ", tui.Style{Fg: tui.ColorAccent, Bold: true})
		listTop++
	}
	listH := h - 2 - listTop
	if listH < 3 {
		listH = 3
	}
	v.list.Draw(f, 2, listTop, w-4, listH, true)

	// footer
	hint := " ↑↓ навігація · Enter — обрати · 1-9, 0, w, i, u — швидкий вибір · q — вихід"
	f.FillLine(0, h-2, w, tui.Truncate(hint, w), tui.Style{Fg: tui.ColorDim})

	line := " " + tui.Truncate("Enter — обрати дію", w-2)
	lineStyle := tui.Style{Fg: tui.ColorDim}
	switch {
	case s.busy != "":
		line = " " + tui.Spinner(s.spins) + " " + s.busy
		lineStyle = tui.Style{Fg: tui.ColorAccent, Bold: true}
	case s.nOn && s.nErr:
		line = " ✖ " + tui.Truncate(s.notice, w-4)
		lineStyle = tui.Style{Fg: tui.ColorBad}
	case s.nOn:
		line = " ✓ " + tui.Truncate(s.notice, w-4)
		lineStyle = tui.Style{Fg: tui.ColorGood}
	}
	f.FillLine(0, h-1, w, line, lineStyle)
}

type statusRow struct {
	label string
	value string
	style tui.Style
}

func statusRows(s snap) []statusRow {
	rows := []statusRow{}

	if s.running {
		info := fmt.Sprintf("активний (PID %d", s.pid)
		if s.rss != "" {
			info += ", RAM " + s.rss
		}
		info += ")"
		rows = append(rows, statusRow{"Фоновий процес", info, tui.Style{Fg: tui.ColorGood, Bold: true}})
	} else {
		rows = append(rows, statusRow{"Фоновий процес", "зупинено (inactive)", tui.Style{Fg: tui.ColorBad, Bold: true}})
	}

	switch {
	case s.auth == nil && s.authE != "":
		rows = append(rows, statusRow{"Telegram", "немає відповіді: " + s.authE, tui.Style{Fg: tui.ColorBad}})
	case s.auth == nil:
		rows = append(rows, statusRow{"Telegram", "запустіть службу для перевірки", tui.Style{Fg: tui.ColorFaint}})
	case s.auth.SignedIn:
		who := s.auth.Phone
		if who == "" {
			who = "авторизовано"
		}
		rows = append(rows, statusRow{"Telegram", "✓ " + who, tui.Style{Fg: tui.ColorGood, Bold: true}})
	case s.auth.Connected:
		rows = append(rows, statusRow{"Telegram",
			fmt.Sprintf("не авторизовано (стан: %s)", s.auth.State),
			tui.Style{Fg: tui.ColorWarn, Bold: true}})
	default:
		rows = append(rows, statusRow{"Telegram", "підключення до Telegram…", tui.Style{Fg: tui.ColorDim}})
	}

	switch {
	case s.status != nil:
		core := s.status.Core
		if core == "" {
			core = "—"
		}
		rows = append(rows, statusRow{"Ядро", core + " · " + s.status.Version, tui.Style{Fg: tui.ColorText}})
	case s.statusE != "":
		rows = append(rows, statusRow{"Ядро", s.statusE, tui.Style{Fg: tui.ColorBad}})
	default:
		rows = append(rows, statusRow{"Ядро", "не запущено", tui.Style{Fg: tui.ColorFaint}})
	}

	if s.url != "" {
		rows = append(rows, statusRow{"Веб-панель", s.url, tui.Style{Fg: tui.ColorAccent2, Under: true}})
	}
	return rows
}

func (v *mainView) OnKey(k tui.Key) {
	if k.Type == tui.KeyRune {
		switch k.Rune {
		case 'q', 'Q':
			v.c.app.Quit()
			return
		case 'r', 'R':
			v.c.refresh()
			v.c.toast("оновлено", false)
			return
		}
		for i, it := range v.list.Items {
			if it.Badge == string(k.Rune) {
				v.list.Sel = i
				v.activate(it.ID)
				return
			}
		}
	}
	if v.list.Key(k) {
		return
	}
	if k.Type == tui.KeyEnter {
		if it, ok := v.list.Current(); ok {
			v.activate(it.ID)
		}
	}
}

func (v *mainView) activate(id string) {
	c := v.c
	switch id {
	case "login":
		c.app.SetView(newLoginView(c))
	case "qr":
		c.app.SetView(newQRView(c))
	case "panel":
		v.openPanel()
	case "start":
		if c.snapshot().running {
			c.toast("юзербот уже запущений", true)
			return
		}
		c.run("запуск у фоні", func() error { return quiet(func() error { return cmdStart(c.layout) }) })
	case "stop":
		c.run("зупинка", func() error { return quiet(func() error { return cmdStop(c.layout) }) })
	case "restart":
		c.run("перезапуск", func() error {
			_ = quiet(func() error { return cmdStop(c.layout) })
			time.Sleep(300 * time.Millisecond)
			return quiet(func() error { return cmdStart(c.layout) })
		})
	case "logs":
		c.app.SetView(newLogsView(c))
	case "plugins":
		c.app.SetView(newPluginsView(c))
	case "gc":
		c.run("очищення пам'яті", c.withClient(func(cl *daemonClient) error { return cl.triggerGC() }))
	case "setup":
		c.app.SetView(newSetupView(c))
	case "info":
		c.app.SetView(newInfoView(c))
	case "update":
		c.run("оновлення", func() error {
			if err := quiet(func() error { return cmdUpdate(c.layout) }); err != nil {
				return err
			}
			// The binary on disk is new but this menu still runs the old
			// code: replace ourselves so the whole CLI reloads.
			return reexecSelf()
		})
	case "logout":
		back := newMainView(c)
		c.app.SetView(newConfirmView(c, "Вийти з акаунта Telegram?",
			"Сесію буде видалено з цього пристрою.",
			func() {
				c.run("вихід з акаунта", c.withClient(func(cl *daemonClient) error {
					return cl.logout()
				}))
			}, back))
	case "quit":
		c.app.Quit()
	}
}

func (v *mainView) openPanel() {
	c := v.c
	url := c.panelURL()
	if url == "" {
		c.toast("конфіг недоступний", true)
		return
	}
	if !c.snapshot().running {
		c.toast("запускаю службу для панелі…", false)
		c.run("запуск панелі", func() error {
			if err := quiet(func() error { return cmdStart(c.layout) }); err != nil {
				return err
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if cl, err := newDaemonClient(c.layout); err == nil && cl.isAlive() {
					openBrowserCLI(cl.baseURL + "/?token=" + cl.token)
					return nil
				}
				time.Sleep(300 * time.Millisecond)
			}
			return nil
		})
		return
	}
	openBrowserCLI(url)
	c.toast("панель відкрито в браузері", false)
}

// ---- confirm dialog ----

type confirmView struct {
	c     *ctl
	title string
	hint  string
	onYes func()
	back  tui.View
}

func newConfirmView(c *ctl, title, hint string, onYes func(), back tui.View) *confirmView {
	return &confirmView{c: c, title: title, hint: hint, onYes: onYes, back: back}
}

func (v *confirmView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	f.FillLine(0, 0, w, " ПІДТВЕРДЖЕННЯ ",
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorWarn, Bold: true})
	bw := w - 4
	if bw < 30 {
		bw = w
	}
	bx := (w - bw) / 2
	bh := 7
	by := (h - bh) / 2
	if by < 3 {
		by = 3
	}
	f.Box(bx, by, bw, bh, tui.Style{Fg: tui.ColorWarn}, "")
	f.Center(bx, bw, by+1, tui.Truncate(v.title, bw-4), tui.Style{Fg: tui.ColorText, Bold: true})
	f.Center(bx, bw, by+2, tui.Truncate(v.hint, bw-4), tui.Style{Fg: tui.ColorDim})
	f.Center(bx, bw, by+4, "y — так      n / Esc — ні", tui.Style{Fg: tui.ColorAccent, Bold: true})
	f.FillLine(0, h-1, w, " Esc — скасувати ", tui.Style{Fg: tui.ColorDim})
}

func (v *confirmView) OnKey(k tui.Key) {
	switch {
	case k.Type == tui.KeyEsc || k.Is('n') || k.Is('N') || k.Is('q'):
		if v.back != nil {
			v.c.app.SetView(v.back)
		}
	case k.Is('y') || k.Is('Y') || k.Type == tui.KeyEnter:
		if v.back != nil {
			v.c.app.SetView(v.back)
		}
		if v.onYes != nil {
			v.onYes()
		}
	}
}

// ---- legacy numbered menu (no TTY) ----

func cmdLegacyMenu(layout paths.Layout) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		client, _ := newDaemonClient(layout)
		pid, running := checkPidRunning(layout.PidFile())

		fmt.Print("\033[H\033[2J")
		fmt.Printf("\033[1;35m┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓\033[0m\n")
		fmt.Printf("\033[1;35m┃\033[0m  \033[1;37mAURORA USERBOT\033[0m \033[36m%s\033[0m — ЦЕНТР КЕРУВАННЯ В ТЕРМІНАЛІ      \033[1;35m┃\033[0m\n", buildinfo.Version)
		fmt.Printf("\033[1;35m┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛\033[0m\n\n")

		if running {
			rss := readProcessRSS(pid)
			if rss == "" {
				rss = "активний"
			}
			fmt.Printf("  ● \033[1;32mФоновий процес:\033[0m  АКТИВНИЙ (PID %d, RAM: %s)\n", pid, rss)
		} else {
			fmt.Printf("  ● \033[1;31mФоновий процес:\033[0m  ЗУПИНЕНО (inactive)\n")
		}

		tgState := "\033[33mПеревірка...\033[0m"
		webURL := ""
		if client != nil {
			webURL = client.baseURL + "/?token=" + client.token
			if client.isAlive() {
				if authSt, err := client.getAuth(); err == nil {
					if authSt.SignedIn {
						phone := authSt.Phone
						if phone == "" {
							phone = "авторизовано"
						}
						tgState = fmt.Sprintf("\033[1;32m✓ АВТОРИЗОВАНО\033[0m (%s)", phone)
					} else {
						tgState = fmt.Sprintf("\033[1;33m⚠ НЕ АВТОРИЗОВАНО\033[0m (стан: %s)", authSt.State)
					}
				} else {
					tgState = "\033[31mнемає відповіді від ядра\033[0m"
				}
			} else {
				tgState = "\033[2m(запустіть службу для перевірки)\033[0m"
			}
		}
		fmt.Printf("  ● \033[1;36mTelegram:\033[0m        %s\n", tgState)
		if webURL != "" {
			fmt.Printf("  ● \033[1;34mВеб-панель:\033[0m      \033[4;34m%s\033[0m\n", webURL)
		}

		fmt.Println("\n\033[2m────────────────────────────────────────────────────────────────────\033[0m")
		fmt.Println("  \033[1;33m[1]\033[0m Вхід у Telegram (номер -> код -> 2FA)")
		fmt.Println("  \033[1;36m[w]\033[0m Вхід по QR (без номера і SMS)")
		fmt.Println("  \033[1;34m[2]\033[0m Відкрити Веб-панель у браузері")
		fmt.Println("  \033[1;32m[3]\033[0m Запустити юзербота у фоні")
		fmt.Println("  \033[1;31m[4]\033[0m Зупинити юзербота")
		fmt.Println("  \033[1;36m[5]\033[0m Перезапустити юзербота")
		fmt.Println("  \033[1;35m[6]\033[0m Живий журнал логів")
		fmt.Println("  \033[1;36m[7]\033[0m Керування плагінами")
		fmt.Println("  \033[1;32m[8]\033[0m Очищення пам'яті (RAM GC)")
		fmt.Println("  \033[1;33m[9]\033[0m Налаштувати власні API ключі")
		fmt.Println("  \033[1;36m[u]\033[0m Оновити юзербота")
		fmt.Println("  \033[1;31m[0]\033[0m Вийти з Telegram акаунта")
		fmt.Println("  \033[2m[q]  Вийти з меню\033[0m")
		fmt.Println("\033[2m────────────────────────────────────────────────────────────────────\033[0m")
		fmt.Print("  \033[1;37mОберіть дію [1-9, 0, q]: \033[0m")

		input, err := reader.ReadString('\n')
		if err != nil {
			return nil
		}
		choice := strings.TrimSpace(input)

		switch choice {
		case "1":
			_ = cmdTerminalLogin(layout)
			pressEnterToContinue(reader)
		case "w", "W":
			_ = cmdQRLogin(layout)
			pressEnterToContinue(reader)
		case "2":
			if !running {
				fmt.Println("→ Запускаю фонову службу...")
				_ = cmdStart(layout)
				time.Sleep(1 * time.Second)
			}
			client, _ = newDaemonClient(layout)
			targetURL := ""
			if client != nil {
				targetURL = client.baseURL + "/?token=" + client.token
			}
			if targetURL == "" {
				targetURL = webURL
			}
			openBrowserCLI(targetURL)
			fmt.Printf("\n\033[1;32m✓ Відкрито у браузері:\033[0m %s\n", targetURL)
			pressEnterToContinue(reader)
		case "3":
			if running {
				fmt.Println("✓ Юзербот уже запущений!")
			} else {
				_ = cmdStart(layout)
			}
			time.Sleep(1 * time.Second)
		case "4":
			_ = cmdStop(layout)
			time.Sleep(1 * time.Second)
		case "5":
			_ = cmdStop(layout)
			time.Sleep(500 * time.Millisecond)
			_ = cmdStart(layout)
			time.Sleep(1 * time.Second)
		case "6":
			_ = cmdLogs(layout)
		case "7":
			menuManagePlugins(client, reader)
		case "8":
			if client != nil && client.isAlive() {
				if err := client.triggerGC(); err == nil {
					fmt.Println("\033[32m✓ Очищення пам'яті (GC) успішно виконано!\033[0m")
				} else {
					fmt.Println("✖ Помилка:", err)
				}
			} else {
				fmt.Println("✖ Помилка: служба не запущена")
			}
			pressEnterToContinue(reader)
		case "9":
			_ = cmdSetup(layout)
			pressEnterToContinue(reader)
		case "u", "update":
			if err := cmdUpdate(layout); err != nil {
				fmt.Println("✖ Помилка:", err)
			} else if err := reexecSelf(); err != nil {
				fmt.Println("✖ Помилка:", err)
			}
			pressEnterToContinue(reader)
		case "0":
			fmt.Print("Ви дійсно бажаєте вийти з акаунта Telegram? [y/N]: ")
			ans, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(ans)) == "y" {
				if client != nil && client.isAlive() {
					_ = client.logout()
				}
				_ = cmdLogout(layout)
				fmt.Println("✓ Сесію завершено!")
			}
			pressEnterToContinue(reader)
		case "q", "exit", "quit":
			fmt.Println("До зустрічі!")
			return nil
		}
	}
}

func pressEnterToContinue(r *bufio.Reader) {
	fmt.Print("\n\033[2mНатисніть Enter для повернення в меню...\033[0m")
	_, _ = r.ReadString('\n')
}

func menuManagePlugins(client *daemonClient, reader *bufio.Reader) {
	if client == nil || !client.isAlive() {
		fmt.Println("✖ Служба не запущена. Запустіть юзербота спочатку!")
		pressEnterToContinue(reader)
		return
	}
	plugins, err := client.getPlugins()
	if err != nil {
		fmt.Printf("✖ Помилка отримання плагінів: %v\n", err)
		pressEnterToContinue(reader)
		return
	}
	fmt.Println("\n\033[1;36m🧩 СПИСОК ПЛАГІНІВ:\033[0m")
	if len(plugins) == 0 {
		fmt.Println("  (плагінів поки немає у папці ~/.local/share/aurora/plugins)")
	}
	for i, p := range plugins {
		status := "\033[31mзупинено\033[0m"
		if p.Running {
			status = "\033[32mактивний\033[0m"
		}
		fmt.Printf("  [%d] %-15s [%s] — %s\n", i+1, p.Name, status, p.Desc)
	}
	pressEnterToContinue(reader)
}
