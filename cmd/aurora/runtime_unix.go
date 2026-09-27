package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func lookPath(bin string) (string, error) { return exec.LookPath(bin) }

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
