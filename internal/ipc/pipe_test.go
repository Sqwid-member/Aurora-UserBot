package ipc

import (
	"io"
	"net"
	"sync"
	"time"
)

// memConn is an in-memory net.Conn with a large buffer, which is what a real
// OS pipe behaves like. Using net.Pipe() directly would be misleading: it is
// strictly unbuffered, so a writer blocks until the peer calls Read, and a
// test using it would deadlock for reasons a real plugin never hits.
type memConn struct {
	in     chan []byte
	out    chan []byte
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newMemPair(buf int) (*memConn, *memConn) {
	ab := make(chan []byte, buf)
	ba := make(chan []byte, buf)
	a := &memConn{in: ba, out: ab, done: make(chan struct{})}
	b := &memConn{in: ab, out: ba, done: make(chan struct{})}
	return a, b
}

func (c *memConn) Read(p []byte) (int, error) {
	select {
	case b, ok := <-c.in:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, b), nil
	case <-c.done:
		return 0, io.ErrClosedPipe
	}
}

func (c *memConn) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, io.ErrClosedPipe
	default:
	}
	buf := make([]byte, len(p))
	copy(buf, p)
	select {
	case c.out <- buf:
		return len(p), nil
	case <-c.done:
		return 0, io.ErrClosedPipe
	}
}

func (c *memConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *memConn) LocalAddr() net.Addr              { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr             { return memAddr{} }
func (c *memConn) SetDeadline(time.Time) error      { return nil }
func (c *memConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem" }

// memConn must satisfy net.Conn.
var _ net.Conn = (*memConn)(nil)
