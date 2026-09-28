//go:build !linux && !android && !darwin

package tui

import (
	"runtime"
)

// enterRaw is a no-op on platforms Aurora does not ship a terminal UI for.
func enterRaw(fd int) (func(), error) {
	return func() {}, nil
}

// terminalSize reports a sane default so callers never divide by zero.
func terminalSize(fd int) (int, int, error) {
	_, _, _ = fd, runtime.GOOS, runtime.GOARCH
	return 80, 24, nil
}
