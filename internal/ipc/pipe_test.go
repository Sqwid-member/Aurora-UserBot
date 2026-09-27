package ipc

import (
	"io"
	"sync"
	"testing"
)

// newDuplex returns the two ends of a unidirectional byte stream.
func newDuplex(t *testing.T) (w io.WriteCloser, r io.ReadCloser) {
	t.Helper()
	pr, pw := io.Pipe()
	return pw, pr
}

var _ = sync.Mutex{}
