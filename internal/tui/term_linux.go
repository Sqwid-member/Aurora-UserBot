//go:build linux || android

package tui

import "golang.org/x/sys/unix"

// ioctlGet/ioctlSet are the tcgetattr/tcsetattr requests. On Linux (and
// therefore Android) they are unsigned, which is what x/sys/unix's
// IoctlGetTermios expects.
const (
	ioctlGet = unix.TCGETS
	ioctlSet = unix.TCSETS
)
