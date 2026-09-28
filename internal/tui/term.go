// Package tui is a tiny, dependency-free terminal UI toolkit built for
// Termux: raw mode, an alternate screen, a rune framebuffer and just enough
// widgets to build a dashboard without pulling in a TUI framework.
package tui

import (
	"errors"
	"os"
)

// ErrNotTTY is returned when stdin/stdout cannot drive a full-screen UI.
var ErrNotTTY = errors.New("термінал не підтримує повноекранний режим")

// terminal holds the state needed to enter and leave raw mode.
type terminal struct {
	in      *os.File
	out     *os.File
	restore func()
	w, h    int
}

func openTerminal() (*terminal, error) {
	in, out := os.Stdin, os.Stdout
	if !isTTY(in) || !isTTY(out) {
		return nil, ErrNotTTY
	}
	restore, err := enterRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	t := &terminal{in: in, out: out, restore: restore}
	t.resize()
	return t, nil
}

func (t *terminal) resize() {
	if w, h, err := terminalSize(int(t.in.Fd())); err == nil && w > 0 && h > 0 {
		t.w, t.h = w, h
	}
	if t.w <= 0 {
		t.w = 80
	}
	if t.h <= 0 {
		t.h = 24
	}
}

func (t *terminal) close() {
	if t.restore != nil {
		t.restore()
		t.restore = nil
	}
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
