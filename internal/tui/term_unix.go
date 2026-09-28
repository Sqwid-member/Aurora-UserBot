//go:build linux || android || darwin

package tui

import (
	"golang.org/x/sys/unix"
)

// enterRaw switches the tty to raw mode (no echo, no line buffering, no
// signal generation) and returns a function that restores the previous state.
func enterRaw(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSet, &raw); err != nil {
		return nil, err
	}
	return func() {
		_ = unix.IoctlSetTermios(fd, ioctlSet, old)
	}, nil
}

func terminalSize(fd int) (int, int, error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}
