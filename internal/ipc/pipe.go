package ipc

import (
	"bufio"
	"io"
)

// StderrPipe forwards each line written by a plugin's stderr to fn.
//
// A plugin that crashes usually explains why on stderr, so this is the single
// most useful debugging affordance in the whole host.
func StderrPipe(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
}
