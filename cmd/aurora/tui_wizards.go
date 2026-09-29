package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tui"
)

// ---- login wizard ----

type loginView struct {
	c    *ctl
	back tui.View

	mu sync.Mutex
	// step: 0 phone, 1 code, 2 sign-up, 3 password, 4 success
	step    int
	input   tui.Input
	last    tui.Input
	lastOn  bool // the surname field has focus
	phone   string
	msg     string
	err     string
	waiting bool
	signed  bool
	resent  int
	spins   int
}

func newLoginView(c *ctl) *loginView {
	v := &loginView{c: c, back: newMainView(c)}
	v.input.Title = " Номер телефону "
	v.input.Placeholder = "+380 50 123 4567"
	v.last.Title = " Прізвище (необов'язково) "
	v.msg = "підготовка…"
	if s := c.snapshot(); !s.running {
		c.run("запуск ядра для входу", func() error {
			return quiet(func() error { return cmdStart(c.layout) })
		})
	}
	go v.resume()
	return v
}

// resume picks the wizard up at whatever step the core already reached, so
// a code sent from another front-end (or a previous attempt) is not lost.
func (v *loginView) resume() {
	client, err := newDaemonClient(v.c.layout)
	if err != nil {
		return
	}
	st, err := client.getAuth()
	if err != nil {
		return
	}
	v.mu.Lock()
	switch st.State {
	case proto.AuthCode:
		v.step = 1
		v.phone = st.Phone
		v.input.Title = " Код підтвердження "
		v.input.Placeholder = "12345"
		v.msg = "код уже надіслано — введіть його"
	case proto.AuthPassword:
		v.step = 3
		v.input.Mask = '\u2022'
		v.input.Title = " Пароль 2FA "
		v.input.Placeholder = "пароль"
		v.msg = "потрібен пароль 2FA"
	case proto.AuthSignedIn:
		v.step = 4
		v.signed = true
		v.msg = ""
		v.err = ""
	}
	if v.msg == "" && st.Message != "" && st.State == proto.AuthPhone {
		v.msg = st.Message
	}
	v.mu.Unlock()
	v.c.app.Wake()
}

func (v *loginView) Tick() {
	v.mu.Lock()
	v.spins++
	v.mu.Unlock()
	v.c.tick()
}

func (v *loginView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	v.mu.Lock()
	step, input, last, lastOn := v.step, v.input, v.last, v.lastOn
	msg, errText, waiting, signed, phone, spins, resent :=
		v.msg, v.err, v.waiting, v.signed, v.phone, v.spins, v.resent
	v.mu.Unlock()
	s := v.c.snapshot()

	header := " ВХІД У TELEGRAM "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})
	phase := fmt.Sprintf("Крок %d/4", min(step+1, 4))
	if signed {
		phase = "готово"
	}
	f.FillLine(w-tui.Width(phase)-1, 0, tui.Width(phase)+1, phase,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	y := 2
	switch {
	case signed:
		f.FillLine(2, y, w-4, " ✓ Сесію підтверджено", tui.Style{Fg: tui.ColorGood, Bold: true})
	case s.auth == nil && s.authE != "":
		f.FillLine(2, y, w-4, " ✖ "+tui.Truncate("ядро не відповідає: "+s.authE, w-8),
			tui.Style{Fg: tui.ColorBad})
	case s.auth == nil && s.running:
		f.FillLine(2, y, w-4, " "+tui.Spinner(spins)+" ядро запускається…", tui.Style{Fg: tui.ColorAccent})
	case s.auth == nil:
		f.FillLine(2, y, w-4, " ✖ служба не запущена — оберіть «Запустити у фоні»",
			tui.Style{Fg: tui.ColorBad})
	case s.auth.Connected || s.auth.SignedIn:
		f.FillLine(2, y, w-4, " ● з'єднання з Telegram встановлено", tui.Style{Fg: tui.ColorGood})
	default:
		f.FillLine(2, y, w-4, " "+tui.Spinner(spins)+" очікування з'єднання з Telegram…",
			tui.Style{Fg: tui.ColorAccent})
	}
	y += 2

	bw := w - 4
	if bw > 72 {
		bw = 72
	}
	bx := (w - bw) / 2
	if bx < 0 {
		bx = 0
	}

	switch step {
	case 0:
		f.Center(bx, bw, y, "Введіть номер телефону у міжнародному форматі", tui.Style{Fg: tui.ColorText})
		y += 2
		input.Draw(f, bx, y, bw, !waiting)
		y += input.Height() + 1
		f.Center(bx, bw, y, "Telegram надішле код у застосунок або на номер", tui.Style{Fg: tui.ColorDim})
	case 1:
		f.Center(bx, bw, y, "Код підтвердження із Telegram", tui.Style{Fg: tui.ColorText})
		y += 2
		input.Draw(f, bx, y, bw, !waiting)
		y += input.Height() + 1
		f.Center(bx, bw, y, tui.Truncate("Код надіслано на "+phone, bw-2), tui.Style{Fg: tui.ColorDim})
		y++
		note := "клавіша r — надіслати ще раз (зазвичай SMS)"
		if resent > 0 {
			note = fmt.Sprintf("повторних запитів: %d · r — ще раз", resent)
		}
		f.Center(bx, bw, y, note, tui.Style{Fg: tui.ColorFaint})
		if v.c.appOnly() {
			y += 2
			f.Center(bx, bw, y, "Telegram не віддає коди стороннім клієнтам (з 2023) —",
				tui.Style{Fg: tui.ColorWarn})
			y++
			f.Center(bx, bw, y, "код прийде лише там, де акаунт уже відкритий",
				tui.Style{Fg: tui.ColorFaint})
			y++
			f.Center(bx, bw, y, "вихід: QR-вхід тапом на цьому телефоні або імпорт сесії",
				tui.Style{Fg: tui.ColorFaint})
		}
	case 2:
		f.Center(bx, bw, y, "🆕 Цей номер ще не зареєстровано в Telegram",
			tui.Style{Fg: tui.ColorAccent, Bold: true})
		y++
		f.Center(bx, bw, y, "Створюємо новий акаунт — вкажіть ім'я", tui.Style{Fg: tui.ColorDim})
		y += 2
		input.Draw(f, bx, y, bw, !waiting && !lastOn)
		y += input.Height()
		last.Draw(f, bx, y, bw, !waiting && lastOn)
		y += last.Height() + 1
	case 3:
		f.Center(bx, bw, y, "🔒 Акаунт захищено хмарним паролем (2FA)",
			tui.Style{Fg: tui.ColorWarn, Bold: true})
		y += 2
		input.Draw(f, bx, y, bw, !waiting)
		y += input.Height() + 1
		f.Center(bx, bw, y, "Пароль зберігається лише в пам'яті процесу", tui.Style{Fg: tui.ColorDim})
	case 4:
		edge := "╭" + strings.Repeat("─", bw-2) + "╮"
		f.Center(bx, bw, y, edge, tui.Style{Fg: tui.ColorGood})
		f.Center(bx, bw, y+1, "│"+center("УСПІШНО АВТОРИЗОВАНО", bw-2)+"│",
			tui.Style{Fg: tui.ColorGood, Bold: true})
		f.Center(bx, bw, y+2, "│"+center("сесію збережено, юзербот готовий", bw-2)+"│",
			tui.Style{Fg: tui.ColorGood})
		f.Center(bx, bw, y+3, "╰"+strings.Repeat("─", bw-2)+"╯", tui.Style{Fg: tui.ColorGood})
		y += 4
	}
	y += 2

	line := ""
	lst := tui.Style{Fg: tui.ColorDim}
	switch {
	case waiting:
		line = " " + tui.Spinner(spins) + " " + msg
		lst = tui.Style{Fg: tui.ColorAccent, Bold: true}
	case errText != "":
		line = " ✖ " + tui.Truncate(errText, w-4)
		lst = tui.Style{Fg: tui.ColorBad}
	case msg != "":
		line = " " + tui.Truncate(msg, w-4)
	}
	if y < h-3 {
		f.FillLine(2, h-3, w-4, line, lst)
	}

	hint := " Enter — підтвердити · Esc — назад "
	switch step {
	case 1:
		hint = " Enter — підтвердити · r — код ще раз · Backspace — змінити номер · Esc — назад "
	case 2:
		hint = " Tab — прізвище · Enter — створити акаунт · Backspace — назад · Esc — вийти "
	case 3:
		hint = " Enter — підтвердити · Backspace — почати з номера · Esc — назад "
	case 4:
		hint = " Enter — назад до меню "
	}
	f.FillLine(0, h-1, w, tui.Truncate(hint, w), tui.Style{Fg: tui.ColorDim})
}

// center pads s so it sits in the middle of a w cell wide row.
func center(s string, w int) string {
	if tui.Width(s) >= w {
		return tui.Truncate(s, w)
	}
	return strings.Repeat(" ", (w-tui.Width(s))/2) + s
}

func (v *loginView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}

	v.mu.Lock()
	waiting, step := v.waiting, v.step
	v.mu.Unlock()
	if waiting {
		return
	}

	if step == 4 {
		if k.Type == tui.KeyEnter {
			v.c.refresh()
			v.c.app.SetView(v.back)
		}
		return
	}
	if k.Type == tui.KeyEnter {
		v.submit()
		return
	}
	if (k.Is('r') || k.Is('R')) && step == 1 {
		v.resend()
		return
	}
	if (k.Is('s') || k.Is('S')) && step == 0 {
		v.sendSMS()
		return
	}
	// Backspace on an empty field walks back to phone entry instead of
	// trapping the user: wrong number is the most common reason to go back.
	if k.Type == tui.KeyBackspace && step >= 1 && step <= 3 {
		v.mu.Lock()
		empty := v.input.String() == "" && v.last.String() == ""
		v.mu.Unlock()
		if empty {
			v.backToPhone("змініть номер і запросіть код знову")
			return
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if step != 2 {
		v.input.Key(k)
		return
	}
	switch k.Type {
	case tui.KeyTab, tui.KeyDown:
		v.lastOn = true
	case tui.KeyUp:
		v.lastOn = false
	case tui.KeyBackspace:
		if v.lastOn && len(v.last.Value) == 0 {
			v.lastOn = false
		} else if v.lastOn {
			v.last.Key(k)
		} else {
			v.input.Key(k)
		}
	default:
		if v.lastOn {
			v.last.Key(k)
		} else {
			v.input.Key(k)
		}
	}
}

// backToPhone drops the wizard back to the phone-number step, keeping the
// last phone so the user edits instead of retyping. Used for explicit
// Backspace navigation and when the core lost its in-memory codeHash
// (e.g. after a restart) and can no longer resend or verify a code.
func (v *loginView) backToPhone(msg string) {
	v.mu.Lock()
	v.step = 0
	v.waiting = false
	v.signed = false
	v.resent = 0
	v.msg = msg
	v.err = ""
	v.input.Reset()
	v.input.Mask = 0
	v.input.Title = " Номер телефону "
	v.input.Placeholder = "+380 50 123 4567"
	if v.phone != "" {
		v.input.Set(v.phone)
	}
	v.last.Reset()
	v.lastOn = false
	v.mu.Unlock()
	v.c.app.Wake()
}

// isCodeHashGone reports the core error meaning its in-memory login state
// (phone code hash) vanished — typically a core restart mid-login.
func isCodeHashGone(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "ще не запитувався")
}

// fail records an error for the status line.
func (v *loginView) fail(err error) {
	v.mu.Lock()
	v.waiting = false
	v.msg = ""
	v.err = ""
	if err != nil {
		v.err = err.Error()
	}
	v.mu.Unlock()
	v.c.app.Wake()
}

// submit runs the current step against the core in the background.
func (v *loginView) submit() {
	v.mu.Lock()
	step := v.step
	raw := v.input.String()
	v.err = ""
	v.mu.Unlock()

	switch step {
	case 0:
		phone := tgc.CleanPhone(raw)
		if phone == "" {
			v.fail(fmt.Errorf("введіть номер у міжнародному форматі, напр. +380501234567"))
			return
		}
		v.mu.Lock()
		v.phone = phone
		v.waiting = true
		v.msg = "надсилаю код на " + phone + "…"
		v.mu.Unlock()
		v.c.app.Wake()
		v.goCall(func(cl *daemonClient) error { return cl.requestCode(phone) }, func(err error) {
			if err != nil {
				v.fail(err)
				return
			}
			v.mu.Lock()
			v.step = 1
			v.waiting = false
			v.msg = "код надіслано — перевірте Telegram"
			v.input.Reset()
			v.input.Title = " Код підтвердження "
			v.input.Placeholder = "12345"
			v.mu.Unlock()
			v.c.app.Wake()
		})

	case 1:
		code := strings.TrimSpace(raw)
		if code == "" {
			v.fail(fmt.Errorf("введіть код із повідомлення"))
			return
		}
		v.mu.Lock()
		v.waiting = true
		v.msg = "перевіряю код…"
		v.mu.Unlock()
		v.c.app.Wake()
		v.goCall(func(cl *daemonClient) error { return cl.submitCode(code) }, func(err error) {
			if err == nil {
				v.finishSign()
				return
			}
			if isCodeHashGone(err) {
				v.backToPhone("ядро перезапустилось і забуло запит — введіть номер ще раз")
				return
			}
			if isSignupNeeded(err) {
				v.mu.Lock()
				v.step = 2
				v.waiting = false
				v.msg = "номер не зареєстровано — вкажіть ім'я для створення акаунта"
				v.input.Reset()
				v.input.Mask = 0
				v.input.Title = " Ім'я (обов'язково) "
				v.input.Placeholder = "Aurora"
				v.last.Reset()
				v.lastOn = false
				v.mu.Unlock()
				v.c.app.Wake()
				return
			}
			if isPasswordNeeded(err) {
				v.mu.Lock()
				v.step = 3
				v.waiting = false
				v.msg = "потрібен пароль 2FA"
				v.input.Reset()
				v.input.Mask = '•'
				v.input.Title = " Пароль 2FA "
				v.input.Placeholder = "пароль"
				v.mu.Unlock()
				v.c.app.Wake()
				return
			}
			v.fail(err)
		})

	case 2:
		first := strings.TrimSpace(raw)
		last := strings.TrimSpace(v.last.String())
		if first == "" {
			v.fail(fmt.Errorf("введіть ім'я — воно обов'язкове для реєстрації"))
			return
		}
		v.mu.Lock()
		v.waiting = true
		v.msg = "створюю акаунт " + first + "…"
		v.mu.Unlock()
		v.c.app.Wake()
		v.goCall(func(cl *daemonClient) error { return cl.signUp(first, last) }, func(err error) {
			if err != nil {
				v.fail(err)
				return
			}
			v.finishSign()
		})

	case 3:
		pass := raw
		if pass == "" {
			v.fail(fmt.Errorf("пароль не може бути порожнім"))
			return
		}
		v.mu.Lock()
		v.waiting = true
		v.msg = "перевіряю пароль…"
		v.mu.Unlock()
		v.c.app.Wake()
		v.goCall(func(cl *daemonClient) error { return cl.submitPassword(pass) }, func(err error) {
			if err != nil {
				v.fail(err)
				return
			}
			v.finishSign()
		})
	}
}

// sendSMS asks Telegram to deliver the code over SMS, which is what numbers
// without an open app session need.
func (v *loginView) sendSMS() {
	phone := tgc.CleanPhone(v.input.String())
	if phone == "" {
		v.fail(fmt.Errorf("введіть номер у міжнародному форматі, напр. +380501234567"))
		return
	}
	v.mu.Lock()
	v.waiting = true
	v.err = ""
	v.msg = "надсилаю код по SMS на " + phone + "…"
	v.mu.Unlock()
	v.c.app.Wake()
	v.goCall(func(cl *daemonClient) error { return cl.requestCodeSMS(phone) }, func(err error) {
		if err != nil {
			v.fail(err)
			return
		}
		v.mu.Lock()
		v.step = 1
		v.waiting = false
		v.phone = phone
		v.msg = "код надіслано по SMS"
		v.input.Reset()
		v.input.Title = " Код із SMS "
		v.input.Placeholder = "12345"
		v.mu.Unlock()
		v.c.app.Wake()
	})
}

// resend asks Telegram to deliver the code again, normally over SMS.
// appOnly reports the Telegram dead-end where the code has nowhere to go.
func (v *loginView) appOnly() bool {
	return v.c.snapshot().authOnly
}

func (v *loginView) resend() {
	v.mu.Lock()
	v.waiting = true
	v.err = ""
	v.msg = "надсилаю код повторно…"
	v.mu.Unlock()
	v.c.app.Wake()
	v.goCall(func(cl *daemonClient) error { return cl.resendCode() }, func(err error) {
		if err != nil && isCodeHashGone(err) {
			v.backToPhone("ядро перезапустилось і забуло запит — введіть номер ще раз")
			return
		}
		v.mu.Lock()
		v.waiting = false
		if err != nil {
			v.err = err.Error()
		} else {
			v.resent++
			v.msg = "код надіслано повторно"
		}
		v.mu.Unlock()
		v.c.app.Wake()
	})
}

// finishSign waits for the core to report an authorized session.
func (v *loginView) finishSign() {
	v.mu.Lock()
	v.msg = "підтверджую сесію…"
	v.mu.Unlock()
	v.c.app.Wake()
	go func() {
		client, err := newDaemonClient(v.c.layout)
		ok := err == nil && waitSignedIn(client, 20*time.Second)
		v.mu.Lock()
		v.waiting = false
		if ok {
			v.step = 4
			v.signed = true
			v.msg = ""
			v.err = ""
			v.input.Mask = 0
		} else {
			v.err = "сесія не підтвердилася — подивіться журнали логів"
		}
		v.mu.Unlock()
		v.c.refresh()
		v.c.app.Wake()
	}()
}

// goCall executes fn against the core and delivers the result to done.
func (v *loginView) goCall(fn func(*daemonClient) error, done func(error)) {
	go func() {
		client, err := newDaemonClient(v.c.layout)
		if err == nil {
			err = fn(client)
		}
		done(err)
	}()
}

// ---- API keys screen ----

type setupView struct {
	c    *ctl
	back tui.View

	mu     sync.Mutex
	id     tui.Input
	hash   tui.Input
	focus  int // 0 = app id, 1 = app hash
	msg    string
	err    string
	saving bool
	custom bool
}

func newSetupView(c *ctl) *setupView {
	v := &setupView{c: c, back: newMainView(c)}
	v.id.Title = " App ID "
	v.id.Placeholder = "611335"
	v.hash.Title = " App Hash "
	v.hash.Placeholder = "d524b414d21f4d37f08684c1df41ac9c"

	if store, err := config.Open(v.c.layout.ConfigFile(), nil); err == nil {
		cfg := store.Get()
		v.custom = cfg.UsingCustomAPIKeys()
		if cfg.Telegram.AppID != 0 {
			v.id.Set(strconv.Itoa(cfg.Telegram.AppID))
		}
		if cfg.Telegram.AppHash != "" {
			v.hash.Set(cfg.Telegram.AppHash)
		}
	}
	return v
}

func (v *setupView) Tick() { v.c.tick() }

func (v *setupView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	v.mu.Lock()
	id, hash, focus, msg, errText, saving, custom :=
		v.id, v.hash, v.focus, v.msg, v.err, v.saving, v.custom
	spins := int(time.Now().UnixNano() / 1e8)
	v.mu.Unlock()

	header := " API КЛЮЧІ "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	y := 2
	state := "зараз: стандартні ключі Telegram (Web K)"
	st := tui.Style{Fg: tui.ColorAccent2}
	if custom {
		state = "зараз: власні ключі з my.telegram.org"
		st = tui.Style{Fg: tui.ColorGood}
	}
	f.FillLine(2, y, w-4, state, st)
	y += 2
	f.FillLine(2, y, w-4,
		tui.Truncate("Власні ключі рекомендовано, якщо стандартні відхиляються (API_ID_INVALID).", w-4),
		tui.Style{Fg: tui.ColorDim})
	y += 2

	bw := w - 6
	if bw > 64 {
		bw = 64
	}
	bx := (w - bw) / 2
	if bx < 1 {
		bx = 1
	}
	if y+id.Height()+hash.Height()+6 < h {
		id.Draw(f, bx, y, bw, focus == 0 && !saving)
		y += id.Height() + 1
		hash.Draw(f, bx, y, bw, focus == 1 && !saving)
		y += hash.Height() + 1
	} else {
		f.FillLine(2, y, w-4, "занадто маленький термінал для форм", tui.Style{Fg: tui.ColorBad})
	}

	y++
	switch {
	case errText != "":
		f.FillLine(2, y, w-4, " ✖ "+tui.Truncate(errText, w-6), tui.Style{Fg: tui.ColorBad})
	case msg != "":
		f.FillLine(2, y, w-4, " ✓ "+tui.Truncate(msg, w-6), tui.Style{Fg: tui.ColorGood})
	case saving:
		f.FillLine(2, y, w-4, " "+tui.Spinner(spins)+" зберігаю…", tui.Style{Fg: tui.ColorAccent})
	}

	if y < h-4 {
		f.FillLine(2, h-4, w-4, " Порожні поля → повернути стандартні ключі", tui.Style{Fg: tui.ColorFaint})
	}
	f.FillLine(0, h-2, w, " Tab — наступне поле · Enter — зберегти · Esc — назад", tui.Style{Fg: tui.ColorDim})
	f.FillLine(0, h-1, w, " Ключі: my.telegram.org → API development tools", tui.Style{Fg: tui.ColorFaint})
}

func (v *setupView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC {
		v.c.app.SetView(v.back)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.saving {
		return
	}
	switch {
	case k.Type == tui.KeyEsc:
		v.c.app.SetView(v.back)
	case k.Type == tui.KeyTab || k.Type == tui.KeyDown:
		v.focus = 1
	case k.Type == tui.KeyUp:
		v.focus = 0
	case k.Type == tui.KeyEnter:
		v.save()
	default:
		if v.focus == 0 {
			v.id.Key(k)
		} else {
			v.hash.Key(k)
		}
	}
}

// save persists the keys. It runs while the view mutex is held: the file
// write is tiny and prevents a second Enter from racing it.
func (v *setupView) save() {
	idStr := strings.TrimSpace(v.id.String())
	hashStr := strings.TrimSpace(v.hash.String())
	v.err, v.msg = "", ""

	appID := 0
	if idStr != "" {
		n, err := strconv.Atoi(idStr)
		if err != nil || n <= 0 {
			v.err = "App ID має бути додатним числом"
			return
		}
		appID = n
	}
	if hashStr != "" && len(hashStr) < 16 {
		v.err = "App Hash має містити щонайменше 16 символів"
		return
	}
	if appID != 0 && hashStr == "" {
		v.err = "разом з App ID вкажіть App Hash"
		return
	}

	store, err := config.Open(v.c.layout.ConfigFile(), nil)
	if err != nil {
		v.err = err.Error()
		return
	}
	v.saving = true
	err = store.Update(func(cfg *config.Config) {
		cfg.Telegram.AppID = appID
		cfg.Telegram.AppHash = hashStr
	})
	v.saving = false
	if err != nil {
		v.err = err.Error()
		return
	}
	if appID == 0 {
		v.msg = "ключі скинуто до стандартних (Web K)"
		v.custom = false
	} else {
		v.msg = fmt.Sprintf("збережено: %d / %s…", appID, maskSecret(hashStr))
		v.custom = true
	}
	v.c.toast("API ключі збережено", false)
}
