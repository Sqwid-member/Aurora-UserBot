package tui

import (
	"time"
	"unicode/utf8"
)

// KeyType enumerates the non-printable keys Aurora cares about.
type KeyType int

const (
	KeyRune KeyType = iota
	KeyEnter
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyBackspace
	KeyDelete
	KeyTab
	KeyCtrlC
	KeyCtrlD
	KeyCtrlL
	KeyCtrlR
	KeyCtrlU
	KeyCtrlW
)

// Key is a single decoded keypress.
type Key struct {
	Type KeyType
	Rune rune
}

// RuneKey builds a printable key.
func RuneKey(r rune) Key { return Key{Type: KeyRune, Rune: r} }

// Is reports whether this key matches a printable rune.
func (k Key) Is(r rune) bool { return k.Type == KeyRune && k.Rune == r }

// Name returns a short human-readable name, used in help bars.
func (k Key) Name() string {
	if k.Type == KeyRune {
		return string(k.Rune)
	}
	switch k.Type {
	case KeyEnter:
		return "Enter"
	case KeyEsc:
		return "Esc"
	case KeyUp:
		return "↑"
	case KeyDown:
		return "↓"
	case KeyLeft:
		return "←"
	case KeyRight:
		return "→"
	case KeyHome:
		return "Home"
	case KeyEnd:
		return "End"
	case KeyPgUp:
		return "PgUp"
	case KeyPgDn:
		return "PgDn"
	case KeyBackspace:
		return "⌫"
	case KeyDelete:
		return "Del"
	case KeyTab:
		return "Tab"
	}
	return "?"
}

// decodeKeys parses as many complete keypresses as possible out of buf and
// returns them together with the unconsumed remainder. complete is false
// when the remainder is an escape sequence that still needs more bytes.
func decodeKeys(buf []byte) (keys []Key, rest []byte, complete bool) {
	for len(buf) > 0 {
		b := buf[0]
		switch {
		case b == 0x1b:
			k, n, done := decodeEscape(buf)
			if !done {
				return keys, buf, false
			}
			keys = append(keys, k)
			buf = buf[n:]
		case b == 0x0d || b == 0x0a:
			keys = append(keys, Key{Type: KeyEnter})
			buf = buf[1:]
		case b == 0x09:
			keys = append(keys, Key{Type: KeyTab})
			buf = buf[1:]
		case b == 0x7f || b == 0x08:
			keys = append(keys, Key{Type: KeyBackspace})
			buf = buf[1:]
		case b < 0x20:
			keys = append(keys, ctrlKey(b))
			buf = buf[1:]
		default:
			r, size := utf8.DecodeRune(buf)
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(buf) && len(buf) < 4 {
					// Probably a multi-byte rune split across reads.
					return keys, buf, false
				}
				buf = buf[1:]
				continue
			}
			keys = append(keys, RuneKey(r))
			buf = buf[size:]
		}
	}
	return keys, buf, true
}

func ctrlKey(b byte) Key {
	switch b {
	case 0x03:
		return Key{Type: KeyCtrlC}
	case 0x04:
		return Key{Type: KeyCtrlD}
	case 0x0c:
		return Key{Type: KeyCtrlL}
	case 0x12:
		return Key{Type: KeyCtrlR}
	case 0x15:
		return Key{Type: KeyCtrlU}
	case 0x17:
		return Key{Type: KeyCtrlW}
	default:
		return Key{Type: KeyRune, Rune: rune(b)}
	}
}

// decodeEscape decodes one escape sequence. done=false means "need more
// bytes"; n is the number of bytes consumed.
func decodeEscape(buf []byte) (Key, int, bool) {
	if len(buf) == 1 {
		return Key{}, 0, false
	}
	switch buf[1] {
	case '[':
		// CSI: parameters then a final byte in 0x40..0x7E.
		for i := 2; i < len(buf); i++ {
			if buf[i] >= 0x40 && buf[i] <= 0x7e {
				return csiKey(buf[2 : i+1]), i + 1, true
			}
		}
		return Key{}, 0, false
	case 'O':
		if len(buf) < 3 {
			return Key{}, 0, false
		}
		return csiKey([]byte{buf[2]}), 3, true
	default:
		// Alt+key: report Escape and let the next byte decode normally, so
		// "ESC x" surfaces as Esc followed by "x".
		return Key{Type: KeyEsc}, 1, true
	}
}

func csiKey(seq []byte) Key {
	if len(seq) == 0 {
		return Key{Type: KeyEsc}
	}
	// Strip numeric parameters: "1;5A" → "A".
	final := seq[len(seq)-1]
	switch final {
	case 'A':
		return Key{Type: KeyUp}
	case 'B':
		return Key{Type: KeyDown}
	case 'C':
		return Key{Type: KeyRight}
	case 'D':
		return Key{Type: KeyLeft}
	case 'H':
		return Key{Type: KeyHome}
	case 'F':
		return Key{Type: KeyEnd}
	case 'Z':
		return Key{Type: KeyTab}
	case '~':
		switch string(seq[:len(seq)-1]) {
		case "1", "7":
			return Key{Type: KeyHome}
		case "4", "8":
			return Key{Type: KeyEnd}
		case "3":
			return Key{Type: KeyDelete}
		case "5":
			return Key{Type: KeyPgUp}
		case "6":
			return Key{Type: KeyPgDn}
		}
		return Key{Type: KeyEsc}
	default:
		return Key{Type: KeyEsc}
	}
}

// escapeTimeout is how long a bare ESC byte waits for the rest of a
// sequence before it is reported as the Escape key.
const escapeTimeout = 35 * time.Millisecond

// keyReader turns raw tty bytes into Key values. It owns a parser goroutine
// so that an Escape can be distinguished from the start of an arrow key
// without blocking the UI loop.
func keyReader(read func([]byte) (int, error), out chan<- Key) {
	raw := make(chan []byte, 16)
	go func() {
		defer close(raw)
		buf := make([]byte, 64)
		for {
			n, err := read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				raw <- chunk
			}
			if err != nil {
				return
			}
		}
	}()

	var pending []byte
	var timer *time.Timer
	var timeout <-chan time.Time

	flushEscape := func() {
		if len(pending) > 0 && pending[0] == 0x1b {
			out <- Key{Type: KeyEsc}
			pending = pending[1:]
		}
		keys, rest, _ := decodeKeys(pending)
		for _, k := range keys {
			out <- k
		}
		pending = rest
	}

	for {
		select {
		case chunk, ok := <-raw:
			if !ok {
				if len(pending) > 0 {
					flushEscape()
				}
				return
			}
			pending = append(pending, chunk...)
			keys, rest, complete := decodeKeys(pending)
			for _, k := range keys {
				out <- k
			}
			pending = rest
			if complete {
				if timer != nil {
					timer.Stop()
					timeout = nil
				}
				continue
			}
			if timeout == nil {
				timer = time.NewTimer(escapeTimeout)
				timeout = timer.C
			}
		case <-timeout:
			timeout = nil
			flushEscape()
		}
	}
}
