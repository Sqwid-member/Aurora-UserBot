//go:build !windows

package app

import (
	"os"
	"syscall"
)

// syscallExec replaces the current process image (Unix).
// On Windows syscall.Exec does not exist — fall back to spawning a child
// and exiting, which is good enough for `aurora start` there.
func syscallExec(exe string, args, env []string) error {
	if exe == "" {
		return os.ErrNotExist
	}
	return syscall.Exec(exe, args, env)
}
