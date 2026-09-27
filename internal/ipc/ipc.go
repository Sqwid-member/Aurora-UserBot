// Package ipc implements the newline-delimited JSON-RPC 2.0 transport that
// Aurora uses to talk to plugins.
//
// The wire format is intentionally boring: one JSON object per line on
// stdin/stdout. That choice is what makes plugins writable in any language
// with a JSON encoder — Python, Node, Lua, Ruby, PHP, C, Go, Rust — instead
// of forcing everyone through a native module loader.
//
// Direction convention (from the host's point of view):
//
//	host  --write-->  plugin stdin
//	host  <--read---  plugin stdout
//
// Anything a plugin prints to stderr is captured and forwarded to the Aurora
// log, so plugins can log freely without corrupting the protocol stream.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol version advertised in the handshake.
const ProtocolVersion = 1

// MaxLineSize guards against a runaway peer flooding memory with one line.
const MaxLineSize = 8 << 20 // 8 MiB

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	// CodeForbidden is Aurora-specific: the plugin lacks a permission.
	CodeForbidden = -32001
	// CodeUnavailable is Aurora-specific: a required service is not ready.
	CodeUnavailable = -32002
)

// Message is a JSON-RPC 2.0 frame. A frame with an ID is a request, without
// an ID it is a notification.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("jsonrpc: %s (code %d)", e.Message, e.Code)
}

// NewError builds a JSON-RPC error.
func NewError(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// HandlerFunc handles an incoming request and returns the result value.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (any, *Error)

// Conn is a bidirectional JSON-RPC connection over a byte stream.
//
// A single Conn can be used by either side of the plugin boundary: the host
// passes (plugin stdout, plugin stdin), a plugin passes (stdin, stdout).
type Conn struct {
	r  *bufio.Reader
	w  io.Writer
	wm sync.Mutex

	seq       atomic.Int64
	pending   map[string]chan Message
	pendingMu sync.Mutex

	handlersMu sync.RWMutex
	handlers   map[string]HandlerFunc
	onStderr   func(line string)

	closed   atomic.Bool
	closeErr error
	done     chan struct{}
}

// NewConn wraps a reader/writer pair.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{
		r:        bufio.NewReaderSize(r, 64*1024),
		w:        w,
		pending:  make(map[string]chan Message, 8),
		handlers: make(map[string]HandlerFunc, 8),
		done:     make(chan struct{}),
	}
}

// SetStderrHandler installs a callback invoked for every stderr line.
// Only meaningful on the host side, where r is the plugin's stderr pipe.
func (c *Conn) SetStderrHandler(fn func(string)) { c.onStderr = fn }

// Handle registers a method handler. Re-registering a method replaces it.
func (c *Conn) Handle(method string, fn HandlerFunc) {
	c.handlersMu.Lock()
	c.handlers[method] = fn
	c.handlersMu.Unlock()
}

// HandleFunc registers a handler that discards params and returns nil.
func (c *Conn) HandleFunc(method string, fn func(ctx context.Context, params json.RawMessage)) {
	c.Handle(method, func(ctx context.Context, p json.RawMessage) (any, *Error) {
		fn(ctx, p)
		return nil, nil
	})
}

// Serve reads frames until EOF or Close. It blocks.
func (c *Conn) Serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return c.closeErr
		default:
		}

		line, err := c.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = c.write(Message{
				JSONRPC: "2.0",
				Error:   NewError(CodeParseError, "parse: %v", err),
			})
			continue
		}
		c.route(ctx, msg)
	}
}

func (c *Conn) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return nil, err
		}
		if cap(buf)-len(buf) < len(chunk) {
			grown := make([]byte, len(buf), len(buf)+len(chunk)+4096)
			copy(grown, buf)
			buf = grown
		}
		buf = append(buf, chunk...)
		if len(buf) > MaxLineSize {
			return nil, errors.New("ipc: line exceeds maximum size")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func (c *Conn) route(ctx context.Context, msg Message) {
	if msg.Method == "" {
		// A response: hand it to whoever is waiting.
		if msg.ID != nil {
			key := string(*msg.ID)
			c.pendingMu.Lock()
			ch, ok := c.pending[key]
			delete(c.pending, key)
			c.pendingMu.Unlock()
			if ok {
				// Never close here: Close may be closing the same channel
				// concurrently. The caller's context and c.done cover the
				// abandonment case, and a full buffer means "already gone".
				select {
				case ch <- msg:
				default:
				}
			}
		}
		return
	}

	// A request or notification from the peer.
	if msg.ID == nil {
		go c.invoke(ctx, msg, nil)
		return
	}
	// Reply off the read loop. If we wrote synchronously here, a peer that is
	// slow to read would wedge this side completely: it would stop draining
	// its input while blocked on its output, and the two would deadlock.
	// JSON-RPC matches responses by id, so out-of-order replies are fine.
	go c.invoke(ctx, msg, msg.ID)
}

func (c *Conn) invoke(ctx context.Context, msg Message, id *json.RawMessage) {
	c.handlersMu.RLock()
	h := c.handlers[msg.Method]
	c.handlersMu.RUnlock()

	if h == nil {
		if id != nil {
			_ = c.write(Message{
				JSONRPC: "2.0",
				ID:      id,
				Error:   NewError(CodeMethodNotFound, "unknown method %q", msg.Method),
			})
		}
		return
	}

	result, rerr := func() (result any, rerr *Error) {
		defer func() {
			if r := recover(); r != nil {
				result, rerr = nil, NewError(CodeInternalError, "panic: %v", r)
			}
		}()
		return h(ctx, msg.Params)
	}()

	if id == nil {
		return // notification: no reply
	}
	resp := Message{JSONRPC: "2.0", ID: id}
	if rerr != nil {
		resp.Error = rerr
	} else {
		buf, err := json.Marshal(result)
		if err != nil {
			resp.Error = NewError(CodeInternalError, "marshal result: %v", err)
		} else {
			resp.Result = buf
		}
	}
	_ = c.write(resp)
}

func (c *Conn) write(msg Message) error {
	msg.JSONRPC = "2.0"
	buf, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')

	c.wm.Lock()
	defer c.wm.Unlock()
	if c.closed.Load() {
		return errors.New("ipc: connection closed")
	}
	_, err = c.w.Write(buf)
	return err
}

// Notify sends a fire-and-forget message.
func (c *Conn) Notify(method string, params any) error {
	return c.NotifyWithContext(context.Background(), method, params)
}

// NotifyWithContext sends a fire-and-forget message that aborts if ctx is
// done. Useful for ordering-sensitive calls such as event delivery.
func (c *Conn) NotifyWithContext(ctx context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.write(Message{JSONRPC: "2.0", Method: method, Params: raw}) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call performs a request/response round trip.
func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	id := c.seq.Add(1)
	idRaw := json.RawMessage(fmt.Sprintf("%d", id))

	ch := make(chan Message, 1)
	c.pendingMu.Lock()
	c.pending[string(idRaw)] = ch
	c.pendingMu.Unlock()

	cleanup := func() {
		c.pendingMu.Lock()
		delete(c.pending, string(idRaw))
		c.pendingMu.Unlock()
	}

	if err := c.write(Message{JSONRPC: "2.0", Method: method, Params: raw, ID: &idRaw}); err != nil {
		cleanup()
		return err
	}

	select {
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	case <-c.done:
		cleanup()
		return errors.New("ipc: connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

// Close shuts the connection down and unblocks every pending call.
//
// Pending channels are dropped rather than closed: a response may still be in
// flight, and sending on a closed channel would panic the whole host.
func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	close(c.done)

	c.pendingMu.Lock()
	c.pending = make(map[string]chan Message)
	c.pendingMu.Unlock()
	return c.closeErr
}

// Closed reports whether Close has been called.
func (c *Conn) Closed() bool { return c.closed.Load() }

// WithTimeout wraps ctx with a default call deadline.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	switch v := params.(type) {
	case json.RawMessage:
		return v, nil
	case []byte:
		return json.RawMessage(v), nil
	default:
		return json.Marshal(params)
	}
}
