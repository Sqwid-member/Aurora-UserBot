package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tui"
)

// ---- plugins ----

type pluginsView struct {
	c    *ctl
	back tui.View

	mu      sync.Mutex
	list    tui.List
	notice  string
	noticeE bool
	input   tui.Input
	install bool
	spins   int
}

func newPluginsView(c *ctl) *pluginsView {
	v := &pluginsView{c: c, back: newMainView(c)}
	v.input.Title = " git URL плагіна "
	v.input.Placeholder = "https://github.com/user/repo"
	return v
}

func (v *pluginsView) Tick() {
	v.mu.Lock()
	v.spins++
	v.mu.Unlock()
	v.c.tick()
}

// syncList rebuilds the list from the fresh snapshot, keeping the cursor on
// the same plugin id when possible.
func (v *pluginsView) syncList(items []tui.ListItem) tui.List {
	v.mu.Lock()
	defer v.mu.Unlock()
	prevID := currentID(v.list, v.list.Sel)
	if len(v.list.Items) != len(items) || !sameIDs(v.list.Items, items) {
		sel := 0
		if len(v.list.Items) > 0 && v.list.Sel < len(v.list.Items) {
			sel = v.list.Sel
		}
		v.list = tui.List{Items: items}
		if idx := indexOf(items, prevID); idx >= 0 {
			v.list.Sel = idx
		} else if sel < len(items) {
			v.list.Sel = sel
		}
	} else {
		v.list.Items = items
		if v.list.Sel >= len(items) && len(items) > 0 {
			v.list.Sel = len(items) - 1
		}
	}
	return v.list
}

func currentID(l tui.List, fallback int) string {
	if it, ok := l.Current(); ok {
		return it.ID
	}
	return idAt(l.Items, fallback)
}

func idAt(items []tui.ListItem, i int) string {
	if i < 0 || i >= len(items) {
		return ""
	}
	return items[i].ID
}

func (v *pluginsView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	v.mu.Lock()
	notice, noticeE, installing, input, spins :=
		v.notice, v.noticeE, v.install, v.input, v.spins
	v.mu.Unlock()
	s := v.c.snapshot()

	header := " ПЛАГІНИ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})
	right := fmt.Sprintf("%d шт.", len(s.plugins))
	f.FillLine(w-tui.Width(right)-1, 0, tui.Width(right)+1, right,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})

	items := itemsOf(s.plugins)
	running := 0
	for _, p := range s.plugins {
		if p.isRunning() {
			running++
		}
	}
	list := v.syncList(items)

	// A notice (usually an error) wraps over up to two lines; the list
	// above shrinks so no row hides underneath it.
	noteLines := []string{}
	if notice != "" {
		noteLines = tui.WrapLines(notice, w-4)
		if len(noteLines) > 2 {
			noteLines = noteLines[:2]
		}
	}
	body := h - 6 - (len(noteLines) - 1)
	if len(noteLines) == 0 {
		body = h - 6
	}
	if body < 3 {
		body = 3
	}
	switch {
	case len(items) > 0:
		list.Draw(f, 2, 2, w-4, body, true)
	case s.plugE != "":
		f.FillLine(2, 3, w-4, " ✖ "+tui.Truncate(s.plugE, w-6), tui.Style{Fg: tui.ColorBad})
	default:
		f.FillLine(2, 3, w-4, " плагінів ще немає — встановіть перший (клавіша i)",
			tui.Style{Fg: tui.ColorDim})
	}

	y := h - 4
	if installing {
		input.Draw(f, 2, y, w-4, true)
	} else if len(noteLines) > 0 {
		col := tui.ColorGood
		if noticeE {
			col = tui.ColorBad
		}
		for i, ln := range noteLines {
			f.FillLine(2, y-(len(noteLines)-1)+i, w-4, ln, tui.Style{Fg: col})
		}
	} else {
		summary := fmt.Sprintf("активних: %d / %d", running, len(items))
		if s.busy != "" {
			summary = tui.Spinner(spins) + " " + s.busy
		}
		f.FillLine(2, y, w-4, tui.Truncate(summary, w-4), tui.Style{Fg: tui.ColorDim})
	}

	if installing {
		f.FillLine(0, h-2, w, " Enter — встановити · Esc — скасувати", tui.Style{Fg: tui.ColorDim})
	} else {
		f.FillLine(0, h-2, w,
			" ↑↓ — вибір · Enter — увімкнути/вимкнути · ←→ — гортання опису · i — встановити · r — оновити · Esc — назад",
			tui.Style{Fg: tui.ColorDim})
	}
	f.FillLine(0, h-1, w, " "+tui.Truncate(s.url, w-2), tui.Style{Fg: tui.ColorFaint})
}

func (v *pluginsView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC {
		v.c.app.SetView(v.back)
		return
	}

	v.mu.Lock()
	if v.install {
		switch {
		case k.Type == tui.KeyEsc:
			v.install = false
			v.input.Reset()
		case k.Type == tui.KeyEnter:
			url := strings.TrimSpace(v.input.String())
			if url == "" {
				v.notice, v.noticeE = "введіть git URL", true
				v.mu.Unlock()
				v.c.app.Wake()
				return
			}
			v.install = false
			v.input.Reset()
			layout := v.c.layout
			v.mu.Unlock()
			v.c.run("встановлення плагіна", func() error {
				return quiet(func() error { return cmdPlugins(layout, []string{"install", url}) })
			})
			return
		default:
			v.input.Key(k)
		}
		v.mu.Unlock()
		v.c.app.Wake()
		return
	}
	v.mu.Unlock()

	switch {
	case k.Type == tui.KeyEsc:
		v.c.app.SetView(v.back)
		return
	case k.Is('i') || k.Is('I'):
		v.mu.Lock()
		v.install = true
		v.notice = ""
		v.mu.Unlock()
		v.c.app.Wake()
		return
	case k.Is('r') || k.Is('R'):
		v.c.refresh()
		v.c.toast("список плагінів оновлено", false)
		return
	}

	v.mu.Lock()
	used := v.list.Key(k)
	it, ok := v.list.Current()
	var name string
	if ok {
		name = it.ID
	}
	v.mu.Unlock()
	if used {
		v.c.app.Wake()
		return
	}
	if k.Type != tui.KeyEnter || !ok {
		return
	}

	action := "start"
	for _, p := range v.c.snapshot().plugins {
		if p.Name == name && p.isRunning() {
			action = "stop"
		}
	}
	label := "запуск " + name
	if action == "stop" {
		label = "зупинка " + name
	}
	v.c.run(label, v.c.withClient(func(cl *daemonClient) error {
		return cl.pluginAction(name, action)
	}))
}

func indexOf(items []tui.ListItem, id string) int {
	for i, it := range items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

func sameIDs(a, b []tui.ListItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

func itemsOf(plugins []pluginItem) []tui.ListItem {
	items := make([]tui.ListItem, 0, len(plugins))
	for _, p := range plugins {
		badge, col := "СТОП", tui.ColorBad
		switch {
		case p.isRunning():
			badge, col = "ON", tui.ColorGood
		case strings.EqualFold(p.State, "starting"), strings.EqualFold(p.State, "stopping"):
			badge, col = "…", tui.ColorWarn
		case strings.EqualFold(p.State, "failed"):
			badge, col = "ERR", tui.ColorBad
		case p.State != "":
			badge = strings.ToUpper(tui.Truncate(p.State, 4))
		}
		items = append(items, tui.ListItem{ID: p.Name, Title: p.Name, Desc: p.Desc, Badge: badge, BadgeColor: col})
	}
	return items
}

// ---- logs ----

type logsView struct {
	c    *ctl
	back tui.View

	mu      sync.Mutex
	lines   []string
	scroll  int // lines counted up from the bottom; 0 = following the tail
	err     string
	updated time.Time
}

func newLogsView(c *ctl) *logsView {
	v := &logsView{c: c, back: newMainView(c)}
	v.load()
	return v
}

func (v *logsView) load() {
	lines, err := tailFile(v.c.layout.LogFile(), 1<<20, 2000)
	v.mu.Lock()
	if err != nil {
		if !os.IsNotExist(err) {
			v.err = err.Error()
		}
	} else {
		v.err = ""
		v.lines = lines
	}
	v.updated = time.Now()
	v.mu.Unlock()
}

func (v *logsView) Tick() {
	v.load()
	v.c.tick()
	v.c.app.Wake()
}

func (v *logsView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	v.mu.Lock()
	lines, scroll, errText, updated := v.lines, v.scroll, v.err, v.updated
	v.mu.Unlock()

	header := " ЖУРНАЛ ЛОГІВ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})
	right := v.c.layout.LogFile()
	f.FillLine(w-tui.Width(right)-1, 0, tui.Width(right)+1, right,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})

	body := h - 3
	if body < 1 {
		body = 1
	}
	switch {
	case errText != "":
		for i, ln := range tui.WrapLines(" ✖ "+errText, w-4) {
			if 2+i >= 1+body {
				break
			}
			f.FillLine(2, 2+i, w-4, ln, tui.Style{Fg: tui.ColorBad})
		}
	case len(lines) == 0:
		f.FillLine(2, 2, w-4, " журнал порожній — запустіть юзербота (клавіша 3 у меню)",
			tui.Style{Fg: tui.ColorDim})
	default:
		// Long lines wrap instead of being cut: every physical row is
		// scrollable, so the whole journal stays readable on a phone.
		type logRow struct {
			text string
			st   tui.Style
		}
		rows := make([]logRow, 0, len(lines)+16)
		for _, ln := range lines {
			st := tui.Style{Fg: tui.ColorText}
			up := strings.ToUpper(ln)
			switch {
			case strings.Contains(up, "ERROR") || strings.Contains(up, "FATAL"):
				st = tui.Style{Fg: tui.ColorBad}
			case strings.Contains(up, "WARN"):
				st = tui.Style{Fg: tui.ColorWarn}
			case strings.Contains(up, "DEBUG"):
				st = tui.Style{Fg: tui.ColorDim}
			}
			for _, r := range tui.WrapLines(ln, w) {
				rows = append(rows, logRow{r, st})
			}
		}
		if limit := len(rows) - 1; limit >= 0 && scroll > limit {
			scroll = limit
			v.mu.Lock()
			v.scroll = scroll
			v.mu.Unlock()
		}
		bottom := len(rows) - scroll
		if bottom > len(rows) {
			bottom = len(rows)
		}
		top := bottom - body
		if top < 0 {
			top = 0
		}
		for row := 0; row < body; row++ {
			idx := top + row
			if idx >= bottom {
				break
			}
			f.FillLine(0, 1+row, w, rows[idx].text, rows[idx].st)
		}
	}

	follow := "● слідкування"
	if scroll != 0 {
		follow = fmt.Sprintf("↑ перегляд: %d рядків знизу", scroll)
	}
	meta := fmt.Sprintf("%s · %d рядків", follow, len(lines))
	if !updated.IsZero() {
		meta += " · " + updated.Format("15:04:05")
	}
	f.FillLine(0, h-2, w, " "+tui.Truncate(meta, w-2), tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " ↑↓ PgUp/PgDn — прокрутка · y — копіювати журнал · Esc — назад",
		tui.Style{Fg: tui.ColorFaint})
}

// yank copies the whole loaded journal (up to 500 tail lines) to the
// clipboard: termux-clipboard-set when present, otherwise an OSC 52
// sequence straight to the tty (understood by most terminal emulators).
func (v *logsView) yank() {
	v.mu.Lock()
	text := strings.Join(v.lines, "\n")
	v.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		v.c.toast("журнал порожній — нічого копіювати", true)
		return
	}
	if path, err := sysx.LookPath("termux-clipboard-set"); err == nil {
		cmd := sysx.Command(path)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			v.c.toast("журнал скопійовано в буфер обміну", false)
			return
		}
	}
	if err := osc52Copy(text); err != nil {
		v.c.toast("буфер недоступний: "+err.Error(), true)
		return
	}
	v.c.toast("журнал скопійовано в буфер обміну", false)
}

// osc52Copy asks the terminal emulator to take text into its clipboard.
func osc52Copy(text string) error {
	// Cap the payload: some emulators truncate around 100 KiB.
	if len(text) > 200<<10 {
		text = text[len(text)-(200<<10):]
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}

func (v *logsView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	if k.Is('y') || k.Is('Y') {
		v.yank()
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	// Upper bound is loose on purpose: lines wrap on screen, so the real
	// range (in physical rows) is bigger — Draw clamps it exactly.
	const huge = 1 << 30
	switch k.Type {
	case tui.KeyUp:
		v.scroll++
	case tui.KeyDown:
		if v.scroll > 0 {
			v.scroll--
		}
	case tui.KeyPgUp:
		v.scroll += 20
	case tui.KeyPgDn:
		v.scroll -= 20
	case tui.KeyHome:
		v.scroll = huge
	case tui.KeyEnd:
		v.scroll = 0
	default:
		return
	}
	if v.scroll > huge {
		v.scroll = huge
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
}

// tailFile reads at most maxBytes from the end of path and returns the last
// maxLines lines.
func tailFile(path string, maxBytes int64, maxLines int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var start int64
	if st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	buf := make([]byte, st.Size()-start)
	if len(buf) > 0 {
		if _, err := f.ReadAt(buf, start); err != nil && len(buf) == 0 {
			return nil, err
		}
	}
	text := string(buf)
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 && i+1 < len(text) {
			text = text[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

// ---- info / diagnostics ----

type infoView struct {
	c    *ctl
	back tui.View
}

func newInfoView(c *ctl) *infoView { return &infoView{c: c, back: newMainView(c)} }

func (v *infoView) Tick() { v.c.tick() }

type infoRow struct {
	label string
	value string
	style tui.Style
}

func (v *infoView) rows() []infoRow {
	s := v.c.snapshot()
	rows := []infoRow{
		{"Версія", buildinfo.Version, tui.Style{Fg: tui.ColorText, Bold: true}},
		{"Система", runtime.GOOS + "/" + runtime.GOARCH, tui.Style{Fg: tui.ColorText}},
		{"Каталог", v.c.layout.Home, tui.Style{Fg: tui.ColorText}},
		{"Конфіг", v.c.layout.ConfigFile(), tui.Style{Fg: tui.ColorText}},
		{"Логи", v.c.layout.LogFile(), tui.Style{Fg: tui.ColorText}},
		{"Сесія", sessionLine(v.c.layout.SessionFile()), tui.Style{Fg: tui.ColorText}},
	}

	if store, err := config.Open(v.c.layout.ConfigFile(), nil); err == nil {
		cfg := store.Get()
		keys := "стандартні (Telegram Web K)"
		kst := tui.Style{Fg: tui.ColorAccent2}
		if cfg.UsingCustomAPIKeys() {
			keys = fmt.Sprintf("власні: %d / %s", cfg.Telegram.AppID, maskSecret(cfg.Telegram.AppHash))
			kst = tui.Style{Fg: tui.ColorGood}
		}
		rows = append(rows, infoRow{"API ключі", keys, kst})
		if cfg.Web.Enabled {
			rows = append(rows, infoRow{"Панель", v.c.panelURL(), tui.Style{Fg: tui.ColorAccent2, Under: true}})
		}
	}

	if s.running {
		info := fmt.Sprintf("активний (PID %d", s.pid)
		if s.rss != "" {
			info += ", RAM " + s.rss
		}
		info += ")"
		rows = append(rows, infoRow{"Процес", info, tui.Style{Fg: tui.ColorGood, Bold: true}})
	} else {
		rows = append(rows, infoRow{"Процес", "зупинено", tui.Style{Fg: tui.ColorBad}})
	}

	switch {
	case s.auth == nil:
		rows = append(rows, infoRow{"Telegram", "служба не запущена", tui.Style{Fg: tui.ColorFaint}})
	case s.auth.SignedIn:
		who := s.auth.Phone
		if who == "" {
			who = "авторизовано"
		}
		rows = append(rows, infoRow{"Telegram", "✓ " + who, tui.Style{Fg: tui.ColorGood, Bold: true}})
	default:
		rows = append(rows, infoRow{"Telegram",
			fmt.Sprintf("не авторизовано (стан: %s, connected=%v)", s.auth.State, s.auth.Connected),
			tui.Style{Fg: tui.ColorWarn}})
	}

	if s.status != nil {
		rows = append(rows, infoRow{"Активний акаунт", s.status.ActiveAccount, tui.Style{Fg: tui.ColorText}})
	}
	rows = append(rows, infoRow{"Плагіни", fmt.Sprintf("%d", len(s.plugins)), tui.Style{Fg: tui.ColorText}})
	if s.statusE != "" {
		rows = append(rows, infoRow{"Ядро", s.statusE, tui.Style{Fg: tui.ColorBad}})
	}
	return rows
}

func sessionLine(path string) string {
	info := tgc.InspectSession(path)
	if !info.Exists {
		return "немає — виконайте «Вхід у Telegram»"
	}
	return fmt.Sprintf("авторизовано · DC %d · %s", info.DC, info.AuthKeyID)
}

func (v *infoView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	header := " СЕАНС І ДІАГНОСТИКА "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})
	right := "aurora doctor"
	f.FillLine(w-tui.Width(right)-1, 0, tui.Width(right)+1, right,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})

	rows := v.rows()
	y := 2
	for _, r := range rows {
		if y >= h-3 {
			break
		}
		f.FillLine(2, y, 18, tui.Pad(r.label, 16), tui.Style{Fg: tui.ColorDim})
		f.TextLimit(20, y, w-22, tui.Truncate(r.value, w-22), r.style)
		y++
	}
	f.FillLine(0, h-2, w, " r — оновити · Esc — назад", tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " "+tui.Truncate(v.c.panelURL(), w-2), tui.Style{Fg: tui.ColorFaint})
}

func (v *infoView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	if k.Is('r') || k.Is('R') {
		v.c.refresh()
		v.c.toast("оновлено", false)
	}
}
