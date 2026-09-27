//go:build linux

package plugins

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyLimits installs resource limits on a freshly started plugin process.
//
// RLIMIT_AS is the one that matters on Android: it caps the whole address
// space, so a runaway plugin hits an allocation failure and dies instead of
// pushing the kernel into the OOM killer and taking Aurora with it.
func applyLimits(pid int, l Limits) []string {
	var warnings []string

	hard := func(cur uint64) *unix.Rlimit {
		return &unix.Rlimit{Cur: cur, Max: cur}
	}

	if l.MemoryMB > 0 {
		v := uint64(l.MemoryMB) * 1024 * 1024
		if err := unix.Prlimit(pid, unix.RLIMIT_AS, hard(v), nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("RLIMIT_AS: %v", err))
		}
	}
	if l.CPUSeconds > 0 {
		v := uint64(l.CPUSeconds)
		if err := unix.Prlimit(pid, unix.RLIMIT_CPU, hard(v), nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("RLIMIT_CPU: %v", err))
		}
	}
	if l.FileMB > 0 {
		v := uint64(l.FileMB) * 1024 * 1024
		if err := unix.Prlimit(pid, unix.RLIMIT_FSIZE, hard(v), nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("RLIMIT_FSIZE: %v", err))
		}
	}
	// Never let a plugin dump core onto a phone's storage.
	if err := unix.Prlimit(pid, unix.RLIMIT_CORE, hard(0), nil); err != nil {
		warnings = append(warnings, fmt.Sprintf("RLIMIT_CORE: %v", err))
	}
	// No new files, no growing the plugin dir into a data lake.
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, hard(256), nil); err != nil {
		warnings = append(warnings, fmt.Sprintf("RLIMIT_NOFILE: %v", err))
	}

	return warnings
}
