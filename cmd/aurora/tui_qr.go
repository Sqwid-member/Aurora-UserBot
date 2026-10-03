package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"

	"github.com/Sqwid-member/Aurora-UserBot/internal/tui"
)

// qrView logs the userbot in by QR: the already authorized Telegram app
// approves the session, so no phone number, SMS or 2FA is involved.
type qrView struct {
	c    *ctl
	back tui.View

	mu      sync.Mutex
	url     string
	expires time.Time
	started bool
	err     string
	spins   int
	polling bool // a qrState fetch is already in flight (Tick fires fast)
}

func newQRView(c *ctl) *qrView {
	v := &qrView{c: c, back: newMainView(c)}
	go v.start()
	return v
}

func (v *qrView) Tick() {
	v.mu.Lock()
	v.spins++
	started, hasURL, busy := v.started, v.url != "", v.polling
	if started && hasURL && !busy {
		v.polling = true
	}
	v.mu.Unlock()
	v.c.tick()
	if !started || !hasURL || busy {
		return
	}
	// Stop polling once the core reports the session: otherwise this view
	// keeps hitting /api/auth/qr forever after success (web №8 analog).
	if s := v.c.snapshot(); s.auth != nil && s.auth.SignedIn {
		v.mu.Lock()
		v.polling = false
		v.mu.Unlock()
		return
	}
	// Pull the token while waiting: Telegram expires and re-exports it.
	go func() {
		defer func() {
			v.mu.Lock()
			v.polling = false
			v.mu.Unlock()
		}()
		client, err := newDaemonClient(v.c.layout)
		if err != nil {
			return
		}
		st, err := client.qrState()
		if err != nil {
			return
		}
		v.mu.Lock()
		if st.URL != "" {
			v.url, v.expires = st.URL, st.Expires
		}
		v.started = st.Running
		v.mu.Unlock()
		v.c.app.Wake()
	}()
}

func (v *qrView) Draw(f *tui.Frame) {
	w, h := f.Width(), f.Height()
	v.mu.Lock()
	url, expires, started, errText, spins := v.url, v.expires, v.started, v.err, v.spins
	v.mu.Unlock()
	s := v.c.snapshot()

	header := " ВХІД ПО QR "
	f.FillLine(0, 0, w, "", tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect})
	f.TextLimit(1, 0, tui.Width(header), header,
		tui.Style{Fg: tui.ColorSelectF, Bg: tui.ColorSelect, Bold: true})

	bw := w - 6
	if bw > 68 {
		bw = 68
	}
	bx := (w - bw) / 2
	if bx < 1 {
		bx = 1
	}
	y := 3

	f.Center(bx, bw, y, "Юзербот входить у ваш акаунт без номера та SMS",
		tui.Style{Fg: tui.ColorText, Bold: true})
	y += 2

	switch {
	case errText != "":
		nlines := tui.WrapLines(errText, bw)
		if len(nlines) > 3 {
			nlines = nlines[:3]
		}
		for _, ln := range nlines {
			f.Center(bx, bw, y, ln, tui.Style{Fg: tui.ColorBad})
			y++
		}
		y++
	case s.auth != nil && s.auth.SignedIn:
		f.Center(bx, bw, y, "✓ Сесію авторизовано", tui.Style{Fg: tui.ColorGood, Bold: true})
		y += 2
	case s.auth == nil || !s.auth.Connected:
		f.Center(bx, bw, y, " "+tui.Spinner(spins)+" очікування з'єднання з Telegram…",
			tui.Style{Fg: tui.ColorAccent})
		y += 2
	case !started:
		f.Center(bx, bw, y, " "+tui.Spinner(spins)+" запитую токен входу…", tui.Style{Fg: tui.ColorAccent})
		y += 2
	case url == "":
		f.Center(bx, bw, y, " "+tui.Spinner(spins)+" Telegram готує посилання…", tui.Style{Fg: tui.ColorAccent})
		y += 2
	default:
		// The link is the primary path on a single phone: tap it (or press
		// o) and Telegram asks to confirm the login. It must stay intact on
		// one line, otherwise the terminal can no longer make it tappable.
		f.Box(bx, y, bw, 4, tui.Style{Fg: tui.ColorAccent}, " Посилання — тапніть його ")
		f.TextLimit(bx+2, y+1, bw-4, url, tui.Style{Fg: tui.ColorText, Bold: true})
		left := time.Until(expires)
		f.Center(bx, bw, y+2, fmt.Sprintf("діє ще %s · клавіша o відкриє його сама",
			left.Round(time.Second)), tui.Style{Fg: tui.ColorWarn})
		y += 5

		// The QR is only useful on a second screen; draw it if it fits.
		if rows := qrLines(url, bw-2); len(rows) > 0 && y+2*len(rows)+6 < h {
			f.Center(bx, bw, y, "або відскануйте з іншого екрана:",
				tui.Style{Fg: tui.ColorDim})
			y++
			for i, line := range rows {
				f.Center(bx, bw, y+i, line, tui.Style{Fg: tui.ColorText})
			}
			y += len(rows) + 1
		}

		f.Center(bx, bw, y, " "+tui.Spinner(spins)+" чекаю підтвердження у Telegram…",
			tui.Style{Fg: tui.ColorAccent})
	}

	hint := " o — відкрити в Telegram · r — оновити посилання · Esc — назад "
	f.FillLine(0, h-1, w, tui.Truncate(hint, w), tui.Style{Fg: tui.ColorDim})
}

func (v *qrView) OnKey(k tui.Key) {
	if k.Type == tui.KeyCtrlC || k.Type == tui.KeyEsc {
		v.c.app.SetView(v.back)
		return
	}
	v.mu.Lock()
	url := v.url
	v.mu.Unlock()

	switch {
	case k.Is('r') || k.Is('R'):
		v.start()
	case k.Is('o') || k.Is('O'):
		if url == "" {
			v.mu.Lock()
			v.err = "посилання ще не готове — зачекайте"
			v.mu.Unlock()
			v.c.app.Wake()
			return
		}
		openBrowserCLI(url)
		v.c.toast("відкрив посилання у Telegram", false)
	}
}

func (v *qrView) start() {
	v.mu.Lock()
	v.started = false
	v.err = ""
	v.mu.Unlock()

	c := v.c
	// NOTE: success here means "token received", not "authorized" — the
	// approval still happens in Telegram (the view shows it live).
	c.run("запит токена", func() error {
		client, err := newDaemonClient(c.layout)
		if err != nil {
			return err
		}
		// The core blocks until the token is approved; the HTTP call returns
		// as soon as the flow is running, so poll the token in parallel.
		go func() {
			_ = client.startQR()
		}()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			st, err := client.qrState()
			if err == nil && (st.URL != "" || !st.Running) {
				v.mu.Lock()
				v.started = st.Running
				v.url, v.expires = st.URL, st.Expires
				v.mu.Unlock()
				c.app.Wake()
				return nil
			}
			time.Sleep(400 * time.Millisecond)
		}
		return fmt.Errorf("Telegram не віддав токен — спробуйте ще раз")
	})
}

// appOnly reports the Telegram dead-end: the login code was pushed to other
// app sessions and no SMS/call fallback exists for third-party clients.
func (c *ctl) appOnly() bool { return c.snapshot().authOnly }

// qrLines renders url as a scannable QR using half-block characters, which
// keeps it compact enough for a phone terminal.
func qrLines(url string, maxWidth int) []string {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return nil
	}
	size := code.Size
	// Fit the panel: prefer a normal quiet zone, fall back to a tight one.
	quiet := 2
	if maxWidth > 0 && size+quiet*2 > maxWidth {
		quiet = 1
	}
	if maxWidth > 0 && size+quiet*2 > maxWidth {
		return nil
	}
	total := size + quiet*2
	// Each character carries two vertical modules, so pad to an even height.
	if total%2 == 1 {
		total++
	}
	var out []string
	for y := 0; y < total; y += 2 {
		var b strings.Builder
		for x := 0; x < total; x++ {
			top := blockAt(code, x, y, quiet)
			bottom := blockAt(code, x, y+1, quiet)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		out = append(out, b.String())
	}
	// Pad every line to the same width so the frame below stays aligned.
	width := 0
	for _, l := range out {
		if tui.Width(l) > width {
			width = tui.Width(l)
		}
	}
	for i, l := range out {
		out[i] = tui.Pad(l, width)
	}
	return out
}

// blockAt reports whether the module at (x, y) is dark, with a quiet zone.
func blockAt(code *qr.Code, x, y, quiet int) bool {
	size := code.Size
	sx, sy := x-quiet, y-quiet
	if sx < 0 || sy < 0 || sx >= size || sy >= size {
		return false
	}
	return code.Black(sx, sy)
}
