//go:build !linux

package plugins

import "syscall"

// procAttr returns the process attributes used for every plugin.
//
// Setpgid is available everywhere; Pdeathsig is Linux-only (it is what stops
// orphaned plugins from surviving the host on Android), so it is omitted here.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
