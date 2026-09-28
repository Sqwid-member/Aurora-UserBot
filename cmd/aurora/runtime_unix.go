package main

import (
	"fmt"
	"os"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
	"runtime"
	"strings"
	"syscall"
)

func lookPath(bin string) (string, error) { return sysx.LookPath(bin) }

func runtimeMem() string {
	if s := readStatusKB(); s != "" {
		return s
	}
	return ""
}

func readStatusKB() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	buf, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(buf), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[0] + " " + fields[1]
			}
		}
	}
	return ""
}

func sysProcAttrDaemon() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}

func readProcessRSS(pid int) string {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return ""
	}
	buf, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(buf), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[0] + " " + fields[1]
			}
		}
	}
	return ""
}
