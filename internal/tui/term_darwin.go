//go:build darwin

package tui

import "golang.org/x/sys/unix"

// ioctlGet/ioctlSet are the TIOCGETA/TIOCSETA requests used by tcgetattr and
// tcsetattr on BSD and macOS.
const (
	ioctlGet = unix.TIOCGETA
	ioctlSet = unix.TIOCSETA
)
