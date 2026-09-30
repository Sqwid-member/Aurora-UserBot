//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// reexecSelf replaces the running CLI process with the (possibly just
// updated) aurora binary. After `aurora update` the menu process still runs
// the old code from memory; without this the user keeps managing the bot
// with a stale CLI until they exit manually.
func reexecSelf() error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("reexec: %w", err)
	}
	return syscall.Exec(bin, os.Args, os.Environ())
}
