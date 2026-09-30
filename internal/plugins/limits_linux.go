//go:build linux

package plugins

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// applyLimits installs resource limits on a freshly started plugin process.
//
// Why RLIMIT_DATA and not RLIMIT_AS
//
// RLIMIT_AS caps *virtual address space*. Go, Python and Node all reserve
// hundreds of megabytes of address space at startup for runtime bookkeeping —
// the Go runtime alone dies with "failed to reserve page summary memory" under
// a 512 MB AS cap, and so does CPython and V8. So an AS cap does not limit a
// plugin's real memory use; it just makes the plugin impossible to run.
//
// RLIMIT_DATA, since Linux 4.7, also applies to anonymous mmap, which is where
// a managed runtime actually keeps its heap. It lets a plugin start normally and
// then kills it with a real OOM once it genuinely grows past the cap. That is
// the behaviour you want on a phone: a runaway plugin dies, the host and every
// other plugin keep running.
func applyLimits(pid int, l Limits) []string {
	var warnings []string

	hard := func(cur uint64) *unix.Rlimit {
		return &unix.Rlimit{Cur: cur, Max: cur}
	}

	if l.MemoryMB > 0 {
		v := uint64(l.MemoryMB) * 1024 * 1024
		if err := unix.Prlimit(pid, unix.RLIMIT_DATA, hard(v), nil); err != nil {
			warnings = append(warnings, fmt.Sprintf("RLIMIT_DATA: %v", err))
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

// rssKB returns the resident memory of a process in kilobytes by reading
// /proc/<pid>/statm. It returns 0 when the process is gone or unreadable —
// stats must never fail just because a plugin exited a moment ago.
func rssKB(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0
	}
	var size, resident uint64
	if _, err := fmt.Sscanf(string(data), "%d %d", &size, &resident); err != nil {
		return 0
	}
	return resident * uint64(os.Getpagesize()) / 1024
}
