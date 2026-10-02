package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tui"
)

// This file holds the second wave of TUI screens that close the panel
// parity gap: accounts, command runner, sessions and profile. All of them
// read from the controller snapshot; the controller fetches each endpoint
// only while its view is on screen (see refreshExtra).

// ---- accounts ----

type accountsView struct {
	c    *ctl
	back tui.View
	list tui.List
}

func newAccountsView(c *ctl) *accountsView {
	v := &accountsView{c: c, back: newMainView(c)}
	c.refresh()
	return v
}

func (v *accountsView) Tick() { v.c.tick() }

func accountItems(accs []proto.AccountInfo) []tui.ListItem {
	items := make([]tui.ListItem, 0, len(accs))
	for _, a := range accs {
		title := a.Title
		if title == "" {
			title = a.Phone
		}
		if title == "" && a.User != nil {
			title = strings.TrimSpace(a.User.First + " " + a.User.Last)
		}
		if title == "" {
			title = a.ID
		}
		badge, col := string(a.Session), tui.ColorWarn
		switch a.Session {
		case proto.StateAuthorized:
			badge, col = "ON", tui.ColorGood
		case proto.StateOffline:
			badge, col = "OFF", tui.ColorBad
		}
		if a.IsActive {
			badge = "* " + badge
		}
		desc := a.Phone
		if a.User != nil && a.User.Username != "" {
			desc = "@" + a.User.Username
		}
		items = append(items, tui.ListItem{ID: a.ID, Title: title, Desc: desc, Badge: badge, BadgeColor: col})
	}
	return items
}

func (v *accountsView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	s := v.c.snapshot()

	header := " АКАУНТИ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	body := h - 4
	if body < 3 {
		body = 3
	}
	switch {
	case len(s.accounts) > 0:
		v.list.Items = accountItems(s.accounts)
		if v.list.Sel >= len(v.list.Items) {
			v.list.Sel = len(v.list.Items) - 1
		}
		v.list.Draw(f, 2, 2, w-4, body, true)
	case s.accE != "":
		f.FillLine(2, 3, w-4, " ✖ "+tui.Truncate(s.accE, w-6), tui.Style{Fg: tui.ColorBad})
	default:
		f.FillLine(2, 3, w-4, " акаунтів немає — виконайте «Вхід у Telegram»",
			tui.Style{Fg: tui.ColorDim})
	}

	if s.busy != "" {
		f.FillLine(2, h-3, w-4, " "+tui.Spinner(s.spins)+" "+s.busy,
			tui.Style{Fg: tui.ColorAccent, Bold: true})
	}
	f.FillLine(0, h-2, w, " ↑↓ — вибір · Enter — зробити активним · r — оновити · Esc — назад",
		tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " "+tui.Truncate(s.url, w-2), tui.Style{Fg: tui.ColorFaint})
}

func (v *accountsView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	if k.Is('r') || k.Is('R') {
		v.c.refresh()
		return
	}
	if v.list.Key(k) {
		v.c.app.Wake()
		return
	}
	if k.Type != tui.KeyEnter {
		return
	}
	it, ok := v.list.Current()
	if !ok {
		return
	}
	id := it.ID
	v.c.run("перемикання акаунта", v.c.withClient(func(cl *daemonClient) error {
		return cl.activateAccount(id)
	}))
}

// ---- command runner ----

type commandsView struct {
	c      *ctl
	back   tui.View
	mu     sync.Mutex // guards result (written from worker goroutines)
	list   tui.List
	args   tui.Input
	onArgs bool
	result string
}

func (v *commandsView) setResult(s string) {
	v.mu.Lock()
	v.result = s
	v.mu.Unlock()
}

func (v *commandsView) getResult() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.result
}

func newCommandsView(c *ctl) *commandsView {
	v := &commandsView{c: c, back: newMainView(c)}
	v.args.Title = " Аргументи "
	v.args.Placeholder = "текст команди (може бути порожнім)"
	c.refresh()
	return v
}

func (v *commandsView) Tick() { v.c.tick() }

func commandItems(cmds []commandSpec) []tui.ListItem {
	items := make([]tui.ListItem, 0, len(cmds))
	for _, c := range cmds {
		plug := c.Plugin
		if plug == "" {
			plug = "core"
		}
		items = append(items, tui.ListItem{ID: c.Name, Title: "/" + c.Name, Desc: c.Description, Badge: plug, BadgeColor: tui.ColorAccent2})
	}
	return items
}

func (v *commandsView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	s := v.c.snapshot()

	header := " КОМАНДИ ПЛАГІНІВ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	v.list.Items = commandItems(s.commands)
	if v.list.Sel >= len(v.list.Items) && len(v.list.Items) > 0 {
		v.list.Sel = len(v.list.Items) - 1
	}

	resText := v.getResult()
	resLines := strings.Split(resText, "\n")
	resH := len(resLines) + 2
	if resText == "" {
		resH = 3
	}
	if resH > 8 {
		resH = 8
	}
	listH := h - 4 - v.args.Height() - 1 - resH - 1
	if listH < 3 {
		listH = 3
	}
	y := 2
	switch {
	case len(v.list.Items) > 0:
		v.list.Draw(f, 2, y, w-4, listH, !v.onArgs)
		y += listH
	case s.cmdE != "":
		f.FillLine(2, y, w-4, " ✖ "+tui.Truncate(s.cmdE, w-6), tui.Style{Fg: tui.ColorBad})
		y += listH
	default:
		f.FillLine(2, y, w-4, " команд немає — запустіть плагіни",
			tui.Style{Fg: tui.ColorDim})
		y += listH
	}
	v.args.Draw(f, 2, y, w-4, v.onArgs)
	y += v.args.Height() + 1

	f.Box(2, y, w-4, resH, tui.Style{Fg: tui.ColorBorder}, " Результат ")
	if resText == "" {
		f.FillLine(4, y+1, w-8, "оберіть команду і натисніть Enter", tui.Style{Fg: tui.ColorFaint})
	} else {
		start := 0
		if len(resLines) > resH-2 {
			start = len(resLines) - (resH - 2)
		}
		for i, ln := range resLines[start:] {
			f.FillLine(4, y+1+i, w-8, tui.Truncate(ln, w-8), tui.Style{Fg: tui.ColorText})
		}
	}

	if s.busy != "" {
		f.FillLine(0, h-2, w, " "+tui.Spinner(s.spins)+" "+s.busy,
			tui.Style{Fg: tui.ColorAccent, Bold: true})
	} else {
		f.FillLine(0, h-2, w, " ↑↓ — команда · Tab — поле аргументів · Enter — виконати · Esc — назад",
			tui.Style{Fg: tui.ColorDim})
	}
}

func (v *commandsView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	switch k.Type {
	case tui.KeyTab, tui.KeyDown:
		if !v.onArgs {
			v.onArgs = true
			v.c.app.Wake()
			return
		}
	case tui.KeyUp:
		if v.onArgs {
			v.onArgs = false
			v.c.app.Wake()
			return
		}
	}
	if v.onArgs {
		if k.Type == tui.KeyEnter {
			v.runSelected()
			return
		}
		v.args.Key(k)
		v.c.app.Wake()
		return
	}
	if v.list.Key(k) {
		v.c.app.Wake()
		return
	}
	if k.Type == tui.KeyEnter {
		v.runSelected()
	}
}

func (v *commandsView) runSelected() {
	it, ok := v.list.Current()
	if !ok {
		return
	}
	name, args := it.ID, v.args.String()
	v.c.run("виконання "+name, func() error {
		client, err := newDaemonClient(v.c.layout)
		if err != nil {
			return err
		}
		out, err := client.runCommand(name, args)
		if err != nil {
			return err
		}
		if out == "" {
			out = "(порожня відповідь)"
		}
		v.setResult(out)
		return nil
	})
}

// ---- sessions ----

type sessionsView struct {
	c    *ctl
	back tui.View
	list tui.List
}

func newSessionsView(c *ctl) *sessionsView {
	v := &sessionsView{c: c, back: newMainView(c)}
	c.refresh()
	return v
}

func (v *sessionsView) Tick() { v.c.tick() }

func sessionItems(ss []tgc.AuthSession) []tui.ListItem {
	items := make([]tui.ListItem, 0, len(ss))
	for _, s := range ss {
		title := s.Device
		if title == "" {
			title = s.Platform
		}
		if title == "" {
			title = fmt.Sprintf("hash %d", s.Hash)
		}
		var tags []string
		if s.Current {
			tags = append(tags, "поточна")
		}
		if !s.OfficialApp {
			tags = append(tags, "сторонній клієнт")
		}
		if s.PasswordPending {
			tags = append(tags, "чекає 2FA")
		}
		badge := strings.Join(tags, ",")
		if badge == "" {
			badge = s.App
		}
		items = append(items, tui.ListItem{
			ID: fmt.Sprintf("%d", s.Hash), Title: title,
			Desc:  strings.TrimSpace(s.App + " " + s.AppVersion),
			Badge: badge, BadgeColor: tui.ColorWarn,
		})
	}
	return items
}

func (v *sessionsView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	s := v.c.snapshot()

	header := " СЕСІЇ TELEGRAM "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	body := h - 5
	if body < 3 {
		body = 3
	}
	switch {
	case len(s.sessions) > 0:
		v.list.Items = sessionItems(s.sessions)
		if v.list.Sel >= len(v.list.Items) {
			v.list.Sel = len(v.list.Items) - 1
		}
		v.list.Draw(f, 2, 2, w-4, body, true)
	case s.sesE != "":
		f.FillLine(2, 3, w-4, " ✖ "+tui.Truncate(s.sesE, w-6), tui.Style{Fg: tui.ColorBad})
	default:
		f.FillLine(2, 3, w-4, " сесій немає", tui.Style{Fg: tui.ColorDim})
	}

	if s.busy != "" {
		f.FillLine(2, h-3, w-4, " "+tui.Spinner(s.spins)+" "+s.busy,
			tui.Style{Fg: tui.ColorAccent, Bold: true})
	}
	f.FillLine(0, h-2, w, " ↑↓ — вибір · Enter — завершити сесію · r — оновити · Esc — назад",
		tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " "+tui.Truncate(s.url, w-2), tui.Style{Fg: tui.ColorFaint})
}

func (v *sessionsView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	if k.Is('r') || k.Is('R') {
		v.c.refresh()
		return
	}
	if v.list.Key(k) {
		v.c.app.Wake()
		return
	}
	if k.Type != tui.KeyEnter {
		return
	}
	it, ok := v.list.Current()
	if !ok {
		return
	}
	hashID := it.ID
	var hash int64
	fmt.Sscanf(hashID, "%d", &hash)
	back := v
	v.c.app.SetView(newConfirmView(v.c,
		"Завершити сесію "+it.Title+"?",
		"Пристрій буде відключено від акаунта.",
		func() {
			v.c.run("завершення сесії", v.c.withClient(func(cl *daemonClient) error {
				return cl.terminateSession(hash)
			}))
		}, back))
}

// ---- profile (read-only mirror of the panel) ----

type profileView struct {
	c    *ctl
	back tui.View
}

func newProfileView(c *ctl) *profileView {
	v := &profileView{c: c, back: newMainView(c)}
	c.refresh()
	return v
}

func (v *profileView) Tick() { v.c.tick() }

func (v *profileView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	s := v.c.snapshot()

	header := " ПРОФІЛЬ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	y := 2
	rows := [][2]string{}
	if s.profile != nil {
		u := s.profile.User
		name := strings.TrimSpace(u.First + " " + u.Last)
		rows = append(rows, [2]string{"Ім'я", name})
		if u.Username != "" {
			rows = append(rows, [2]string{"Юзернейм", "@" + u.Username})
		}
		if u.Phone != "" {
			rows = append(rows, [2]string{"Телефон", u.Phone})
		}
		if s.profile.About != "" {
			rows = append(rows, [2]string{"Біо", s.profile.About})
		}
	} else if s.profE != "" {
		f.FillLine(2, y, w-4, " ✖ "+tui.Truncate(s.profE, w-6), tui.Style{Fg: tui.ColorBad})
		y++
	} else {
		f.FillLine(2, y, w-4, " завантаження… (запустіть ядро)", tui.Style{Fg: tui.ColorDim})
		y++
	}
	for _, r := range rows {
		if y >= h-3 {
			break
		}
		f.FillLine(2, y, 16, tui.Pad(r[0], 14), tui.Style{Fg: tui.ColorDim})
		f.TextLimit(18, y, w-20, tui.Truncate(r[1], w-20), tui.Style{Fg: tui.ColorText})
		y++
	}
	f.FillLine(0, h-2, w, " r — оновити · Esc — назад (редагування — у веб-панелі)",
		tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " "+tui.Truncate(s.url, w-2), tui.Style{Fg: tui.ColorFaint})
}

func (v *profileView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	if k.Is('r') || k.Is('R') {
		v.c.refresh()
		v.c.toast("оновлено", false)
	}
}
