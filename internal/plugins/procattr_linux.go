//go:build linux

package plugins

import "syscall"

// procAttr returns the process attributes used for every plugin.
//
// Setpgid puts the plugin (and anything it spawns) into its own process
// group, so one kill takes down the whole tree. Pdeathsig makes the kernel
// kill the plugin the moment Aurora exits, even if Aurora is SIGKILLed and
// never gets to run a shutdown path — which on a phone matters, because the
// user swipes the app away.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}
