// Package logx is a tiny structured logger tuned for phones.
//
// Design goals, in order:
//
//  1. Never block the caller. A bounded ring buffer and non-blocking
//     subscribers mean logging can never stall the MTProto update loop.
//  2. Tiny allocations. Records are pre-rendered strings.
//  3. Human-readable on a terminal, parseable when piped to a file.
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level is a log severity.
type Level int8

// Severity levels, ordered from most to least verbose.
const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// ParseLevel maps a case-insensitive string to a Level.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, true
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

var levelColor = map[Level]string{
	LevelTrace: "\x1b[90m",
	LevelDebug: "\x1b[36m",
	LevelInfo:  "\x1b[32m",
	LevelWarn:  "\x1b[33m",
	LevelError: "\x1b[31m",
}

const reset = "\x1b[0m"

// Field is a structured key/value pair attached to a record.
type Field struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// F builds a Field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// Record is a single log entry.
type Record struct {
	Time   time.Time `json:"time"`
	Level  string    `json:"level"`
	Scope  string    `json:"scope,omitempty"`
	Msg    string    `json:"msg"`
	Fields []Field   `json:"fields,omitempty"`
}

const (
	defaultCapacity = 1024
	// queueDepth bounds each subscriber. Beyond this, records are dropped.
	queueDepth = 4096
)

// shared is the mutable state every scoped logger points at. Keeping it
// behind a pointer is what lets Scoped build a new Logger without copying a
// mutex — and why the web log viewer sees records from all scopes.
type shared struct {
	level atomic.Int32

	mu      sync.RWMutex
	ring    []Record
	head    int
	count   int
	dropped uint64

	// sinkMu serialises renders into the shared sink. Without it two
	// goroutines can interleave halfway through a line and produce a
	// half-record in the log file.
	sinkMu sync.Mutex

	subs   map[uint64]chan Record
	nextID uint64
}

// Logger is a level-filtered, ring-buffered logger.
type Logger struct {
	scope string
	color bool
	sink  io.Writer
	st    *shared
}

// Options configures a Logger.
type Options struct {
	// Level is the minimum severity to record.
	Level Level
	// Color enables ANSI colours.
	Color bool
	// Sink receives rendered text. Defaults to os.Stderr.
	Sink io.Writer
	// Capacity is the in-memory ring size used by the web log viewer.
	Capacity int
}

// New builds a Logger. scope is a dotted prefix such as "plugins" or "tgc".
func New(opts Options, scope string) *Logger {
	if opts.Capacity <= 0 {
		opts.Capacity = defaultCapacity
	}
	if opts.Sink == nil {
		opts.Sink = os.Stderr
	}
	st := &shared{
		ring: make([]Record, opts.Capacity),
		subs: make(map[uint64]chan Record),
	}
	st.level.Store(int32(opts.Level))
	return &Logger{scope: scope, color: opts.Color, sink: opts.Sink, st: st}
}

// Scoped returns a child logger with an extended scope. Both loggers share one
// ring buffer, one level and one subscriber set.
func (l *Logger) Scoped(scope string) *Logger {
	if l == nil {
		return nil
	}
	child := *l // copies only value fields: scope/color/sink/*shared
	if child.scope == "" {
		child.scope = scope
	} else {
		child.scope = child.scope + "." + scope
	}
	return &child
}

// Level reports the current minimum severity.
func (l *Logger) Level() Level {
	if l == nil {
		return LevelInfo
	}
	return Level(l.st.level.Load())
}

// SetLevel changes the minimum severity for this logger and every scope.
func (l *Logger) SetLevel(v Level) {
	if l == nil {
		return
	}
	l.st.level.Store(int32(v))
}

func (l *Logger) log(lv Level, msg string, fields ...Field) {
	if l == nil || int32(lv) < l.st.level.Load() {
		return
	}
	rec := Record{
		Time:   time.Now(),
		Level:  lv.String(),
		Scope:  l.scope,
		Msg:    msg,
		Fields: fields,
	}
	l.store(rec)
	l.write(rec)
}

// store writes the record into the ring and fans it out to subscribers.
//
// The fan-out happens while the lock is still held. Every send is
// non-blocking, so holding the lock cannot stall the caller, and it is what
// makes closing a subscriber channel in cancel() safe: the channel can only
// be closed by a goroutine that already holds the same lock, so a producer can
// never be mid-send on a channel that is about to be closed.
func (l *Logger) store(rec Record) {
	st := l.st

	st.mu.Lock()
	defer st.mu.Unlock()

	st.ring[st.head] = rec
	st.head = (st.head + 1) % len(st.ring)
	if st.count < len(st.ring) {
		st.count++
	}
	for _, c := range st.subs {
		select {
		case c <- rec:
		default: // slow consumer: never block the producer
			st.dropped++
		}
	}
}

func (l *Logger) write(rec Record) {
	if l.sink == nil {
		return
	}
	l.st.sinkMu.Lock()
	defer l.st.sinkMu.Unlock()
	var b strings.Builder
	b.Grow(len(rec.Msg) + 48)
	b.WriteString(rec.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	if l.color {
		if c, ok := levelColor[parseLevel(rec.Level)]; ok {
			b.WriteString(c)
		}
	}
	b.WriteString(fmt.Sprintf("%-5s", rec.Level))
	if l.color {
		b.WriteString(reset)
	}
	b.WriteByte(' ')
	if rec.Scope != "" {
		b.WriteString(rec.Scope)
		b.WriteByte(' ')
	}
	b.WriteString(rec.Msg)
	for _, f := range rec.Fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		fmt.Fprintf(&b, "%v", f.Value)
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(l.sink, b.String())
}

func parseLevel(s string) Level {
	lvl, _ := ParseLevel(s)
	return lvl
}

// Log records a message at an explicit level.
func (l *Logger) Log(lv Level, msg string, f ...Field) { l.log(lv, msg, f...) }

// Trace logs at trace level.
func (l *Logger) Trace(msg string, f ...Field) { l.log(LevelTrace, msg, f...) }

// Debug logs at debug level.
func (l *Logger) Debug(msg string, f ...Field) { l.log(LevelDebug, msg, f...) }

// Info logs at info level.
func (l *Logger) Info(msg string, f ...Field) { l.log(LevelInfo, msg, f...) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, f ...Field) { l.log(LevelWarn, msg, f...) }

// Error logs at error level.
func (l *Logger) Error(msg string, f ...Field) { l.log(LevelError, msg, f...) }

// Fatal logs at error level and terminates the process.
func (l *Logger) Fatal(msg string, f ...Field) {
	l.log(LevelError, msg, f...)
	os.Exit(1)
}

// Tail returns up to n most recent records across every scope, oldest first.
func (l *Logger) Tail(n int) []Record {
	if l == nil {
		return nil
	}
	st := l.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	if n <= 0 || n > st.count {
		n = st.count
	}
	out := make([]Record, 0, n)
	start := (st.head - n + len(st.ring)) % len(st.ring)
	for i := 0; i < n; i++ {
		out = append(out, st.ring[(start+i)%len(st.ring)])
	}
	return out
}

// Dropped reports how many records were discarded because a subscriber was slow.
func (l *Logger) Dropped() uint64 {
	if l == nil {
		return 0
	}
	l.st.mu.RLock()
	defer l.st.mu.RUnlock()
	return l.st.dropped
}

// Subscribe registers a listener. The returned function unsubscribes and
// closes the channel.
func (l *Logger) Subscribe() (<-chan Record, func()) {
	if l == nil {
		closed := make(chan Record)
		close(closed)
		return closed, func() {}
	}
	ch := make(chan Record, queueDepth)
	st := l.st

	st.mu.Lock()
	id := st.nextID
	st.nextID++
	st.subs[id] = ch
	st.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			st.mu.Lock()
			if c, ok := st.subs[id]; ok {
				delete(st.subs, id)
				close(c)
			}
			st.mu.Unlock()
		})
	}
}

// JSON renders a record as a single JSON line (used by the web log stream).
func (r Record) JSON() []byte {
	b, err := json.Marshal(r)
	if err != nil {
		return []byte(`{"level":"ERROR","msg":"log marshal failed"}`)
	}
	return b
}
