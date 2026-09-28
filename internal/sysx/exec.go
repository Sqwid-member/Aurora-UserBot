// Package sysx provides syscall and process execution utilities designed to
// be safe on Android/Termux where seccomp filters kill faccessat2 with SIGSYS.
package sysx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when an executable is not found in PATH.
var ErrNotFound = errors.New("executable file not found in $PATH")

// LookPath searches for an executable named file in the directories named by the
// PATH environment variable using os.Stat instead of faccessat2.
// Standard Go exec.LookPath calls faccessat2, which triggers SIGSYS on Android/Termux.
func LookPath(file string) (string, error) {
	if strings.Contains(file, "/") {
		fi, err := os.Stat(file)
		if err != nil {
			return "", err
		}
		if fi.IsDir() || fi.Mode()&0111 == 0 {
			return "", fs.ErrPermission
		}
		return file, nil
	}

	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/data/data/com.termux/files/usr/bin:/bin:/usr/bin"
	}

	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		target := filepath.Join(dir, file)
		fi, err := os.Stat(target)
		if err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return target, nil
		}
	}

	// Extra check for standard Termux bin location
	termuxTarget := filepath.Join("/data/data/com.termux/files/usr/bin", file)
	if fi, err := os.Stat(termuxTarget); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
		return termuxTarget, nil
	}

	return "", &exec.Error{Name: file, Err: ErrNotFound}
}

// Command returns the Cmd struct to execute the named program with the given arguments.
// It pre-resolves bare binary names using our safe LookPath so Go never calls faccessat2.
func Command(name string, args ...string) *exec.Cmd {
	resolved := name
	var lookErr error
	if !strings.Contains(name, "/") {
		if lp, err := LookPath(name); err == nil {
			resolved = lp
		} else {
			lookErr = err
		}
	}

	cmd := exec.Command(resolved, args...)
	if lookErr != nil {
		// Set LookPathErr directly so cmd.Run()/Start() returns clean error
		// without calling Go's LookPath
		cmd.Args[0] = name
	}
	return cmd
}

// CommandContext is like Command but includes a context.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	resolved := name
	var lookErr error
	if !strings.Contains(name, "/") {
		if lp, err := LookPath(name); err == nil {
			resolved = lp
		} else {
			lookErr = err
		}
	}

	cmd := exec.CommandContext(ctx, resolved, args...)
	if lookErr != nil {
		cmd.Args[0] = name
	}
	return cmd
}
