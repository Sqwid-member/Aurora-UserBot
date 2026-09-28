//go:build windows

package app

import (
	"os"
	"os/exec"
)

// syscallExec fallback for Windows: spawn a detached copy and exit.
func syscallExec(exe string, args, env []string) error {
	if exe == "" {
		return os.ErrNotExist
	}
	cmd := exec.Command(exe, args[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
