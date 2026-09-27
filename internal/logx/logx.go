// Package logx is a tiny structured logger tuned for phones.
//
// Design goals, in order:
//
//  1. Never block the caller. A full ring buffer drops the oldest record
//     instead of stalling the MTProto update loop.
//  2. Tiny allocations. Records are pre-rendered strings.
//  3. Human-readable on a terminal, parseable when piped to a file.
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
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
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Scope   string    `json:"scope,omitempty"`
	Msg     string    `json:"msg"`
	Fields  []Field   `json:"fields,omitempty"`
	Dropped uint64    `json:"dropped,omitempty"`
}

const (
	defaultCapacity = 1024
	// queueDepth bounds the async writer. Beyond this, records are dropped.
	queueDepth = 4096
)

// Logger is a level-filtered, ring-buffered logger.
type Logger struct {
	level Level
	scope string
	color bool
	sink  io.Writer

	mu      sync.RWMutex
	ring    []Record
	head    int
	count   int
	dropped uint64

	subs   map[uint64]chan Record
	nextID uint64
}

// Options configures a Logger.
type Options struct {
	// Level is the minimum severity to record.
	Level Level
	// Color enables ANSI colours; defaults to auto-detect on stderr.
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
	return &Logger{
		level: opts.Level,
		scope: scope,
		color: opts.Color,
		sink:  opts.Sink,
		ring:  make([]Record, opts.Capacity),
		subs:  make(map[uint64]chan Record),
	}
}

// Scoped returns a child logger with an extended scope.
func (l *Logger) Scoped(scope string) *Logger {
	if l == nil {
		return nil
	}
	child := *l
	child.subs = nil // subscriptions are owned by the root logger
	if child.scope == "" {
		child.scope = scope
	} else {
		child.scope = child.scope + "." + scope
	}
	return &child
}

// Level reports the current minimum severity.
func (l *Logger) Level() Level { return l.level }

// SetLevel changes the minimum severity.
func (l *Logger) SetLevel(v Level) { l.level = v }

func (l *Logger) log(lv Level, msg string, fields ...Field) {
	if l == nil || lv < l.level {
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

func (l *Logger) store(rec Record) {
	l.mu.Lock()
	l.ring[l.head] = rec
	l.head = (l.head + 1) % len(l.ring)
	if l.count < len(l.ring) {
		l.count++
	}
	subs := make([]chan Record, 0, len(l.subs))
	for _, c := range l.subs {
		subs = append(subs, c)
	}
	l.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- rec:
		default: // slow consumer: never block the producer
		}
	}
}

func (l *Logger) write(rec Record) {
	if l.sink == nil {
		return
	}
	var b strings.Builder
	b.Grow(len(rec.Msg) + 48)
	b.WriteString(rec.Time.Format("15:04:05.000"))
	b.WriteByte(' ')
	if l.color {
		c, ok := levelColor[Level(len(rec.Level))-LevelTrace]
		if !ok {
			c = ""
		}
		b.WriteString(c)
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

// Tail returns up to n most recent records, oldest first.
func (l *Logger) Tail(n int) []Record {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n <= 0 || n > l.count {
		n = l.count
	}
	out := make([]Record, 0, n)
	start := (l.head - n + len(l.ring)) % len(l.ring)
	for i := 0; i < n; i++ {
		out = append(out, l.ring[(start+i)%len(l.ring)])
	}
	return out
}

// Dropped reports how many records were discarded because a subscriber was slow.
func (l *Logger) Dropped() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dropped
}

// Subscribe registers a listener. The returned function unsubscribes and
// closes the channel.
func (l *Logger) Subscribe() (<-chan Record, func()) {
	ch := make(chan Record, queueDepth)
	l.mu.Lock()
	id := l.nextID
	l.nextID++
	l.subs[id] = ch
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		if c, ok := l.subs[id]; ok {
			delete(l.subs, id)
			close(c)
		}
		l.mu.Unlock()
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

// SortedKeys returns sorted field keys; used by deterministic tests.
func SortedKeys(fields []Field) []string {
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	sort.Strings(keys)
	return keys
}
