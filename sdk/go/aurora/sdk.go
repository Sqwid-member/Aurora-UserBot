// Package aurora is the official Go SDK for writing Aurora plugins.
//
// A plugin is an ordinary executable. It speaks newline-delimited JSON-RPC 2.0
// with the Aurora host over stdin/stdout, and writes human-readable logs to
// stderr. There is no shared library, no dynamic loading and no ABI to match —
// which is exactly why a plugin can also be written in Python, Node, Lua, Ruby
// or C using the same protocol.
//
// Minimal plugin:
//
//	package main
//
//	import (
//	    "context"
//	    "os"
//
//	    "github.com/aurora/aurora-sdk-go/aurora"
//	)
//
//	func main() {
//	    p := aurora.New()
//	    p.OnEvent("message.new", func(ctx context.Context, e aurora.Event) error {
//	        p.Infof("got: %s", e.Text())
//	        return nil
//	    })
//	    _ = p.Run(nil)
//	}
//
// Build it statically and drop it next to aurora.plugin.json:
//
//	go build -o hello .
package aurora

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProtocolVersion must match the host's.
const ProtocolVersion = 1

// Event is a message pushed by the host.
type Event struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// Unmarshal decodes the event payload into v.
func (e Event) Unmarshal(v any) error { return json.Unmarshal(e.Data, v) }

// Text returns the event payload as a Message-ish struct when the event is a
// message event, or an empty string otherwise.
func (e Event) Text() string {
	var m Message
	if err := json.Unmarshal(e.Data, &m); err == nil {
		return m.Text
	}
	return ""
}

// Peer renders a peer reference accepted by Send.
func (e Event) Peer() string {
	var m Message
	if err := json.Unmarshal(e.Data, &m); err != nil || m.PeerID == 0 {
		return ""
	}
	return PeerRef(m.PeerID, m.PeerType)
}

// Command is a command invocation from the web panel, CLI or another plugin.
type Command struct {
	Name string   `json:"name"`
	Text string   `json:"text"`
	Args []string `json:"args,omitempty"`
}

// Split returns the parsed arguments, falling back to a naive split.
func (c Command) Split() []string {
	if len(c.Args) > 0 {
		return c.Args
	}
	return Fields(c.Text)
}

// Message is the payload of message events and the result of Send.
type Message struct {
	PeerID     int64  `json:"peer_id"`
	PeerType   string `json:"peer_type"` // user | chat | channel
	PeerTitle  string `json:"peer_title,omitempty"`
	FromID     int64  `json:"from_id"`
	FromName   string `json:"from_name,omitempty"`
	FromBot    bool   `json:"from_bot,omitempty"`
	Text       string `json:"text"`
	MessageID  int    `json:"message_id"`
	Date       int64  `json:"date"`
	Out        bool   `json:"out,omitempty"`
	ReplyTo    int    `json:"reply_to,omitempty"`
	Media      string `json:"media,omitempty"`
	IsPrivate  bool   `json:"is_private,omitempty"`
	MentionsMe bool   `json:"mentions_me,omitempty"`
}

// User is the payload of tg.get_me.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
	First    string `json:"first_name,omitempty"`
	Last     string `json:"last_name,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
	Premium  bool   `json:"premium,omitempty"`
}

// Name renders the best available human name.
func (u User) Name() string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return strings.TrimSpace(u.First + " " + u.Last)
}

// SendOptions tunes an outgoing message.
type SendOptions struct {
	ReplyTo   int   `json:"reply_to,omitempty"`
	Silent    bool  `json:"silent,omitempty"`
	NoPreview bool  `json:"no_preview,omitempty"`
	Schedule  int64 `json:"schedule,omitempty"`
}

// HostInfo is sent by the host in the plugin.hello handshake.
type HostInfo struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Core     string `json:"core"`
	Dir      string `json:"dir"`
	Plugin   string `json:"plugin"`
	Now      int64  `json:"now"`
}

// Error is a JSON-RPC error returned by the host.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("aurora: %s (code %d)", e.Message, e.Code)
}

// EventHandler processes an event.
type EventHandler func(ctx context.Context, e Event) error

// CommandHandler processes a command and returns text for the caller.
type CommandHandler func(ctx context.Context, c Command) (string, error)

// HookFunc is a generic lifecycle hook.
type HookFunc func(ctx context.Context) error

// Plugin is the plugin-side runtime handle.
type Plugin struct {
	name string

	in  *bufio.Reader
	out *os.File

	writeMu sync.Mutex
	seq     int64

	mu         sync.Mutex
	pending    map[string]chan rpcMessage
	events     map[string]EventHandler
	commands   map[string]CommandHandler
	methods    map[string]CommandHandler
	onStart    HookFunc
	onStop     HookFunc
	callTimout time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
}

// New creates a Plugin bound to stdin/stdout.
func New() *Plugin {
	return &Plugin{
		pending:    map[string]chan rpcMessage{},
		events:     map[string]EventHandler{},
		commands:   map[string]CommandHandler{},
		methods:    map[string]CommandHandler{},
		callTimout: 30 * time.Second,
	}
}

// Name returns the plugin name announced by the host.
func (p *Plugin) Name() string { return p.name }

// SetCallTimeout bounds every host API call. Default 30s.
func (p *Plugin) SetCallTimeout(d time.Duration) { p.callTimout = d }

// OnEvent registers a handler for a host event, e.g. "message.new".
func (p *Plugin) OnEvent(name string, h EventHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events[name] = h
}

// OnAnyEvent registers a catch-all handler invoked for every event.
func (p *Plugin) OnAnyEvent(h EventHandler) { p.OnEvent("*", h) }

// OnCommand registers a command handler.
func (p *Plugin) OnCommand(name string, h CommandHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands[name] = h
}

// OnMethod registers a handler for a custom method exposed to the host,
// declared via the "rpc_methods" field of aurora.plugin.json.
func (p *Plugin) OnMethod(name string, h CommandHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.methods[name] = h
}

// OnStart registers a hook run right after the handshake.
func (p *Plugin) OnStart(h HookFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onStart = h
}

// OnStop registers a hook run when the host asks the plugin to unload.
func (p *Plugin) OnStop(h HookFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onStop = h
}

// Log writes a line to stderr; the host forwards it into the Aurora log with
// this plugin's scope. Logging never goes through the RPC channel, so a slow
// or wedged host can never deadlock the plugin.
func (p *Plugin) Log(level, msg string) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", strings.ToUpper(level), msg)
}

func (p *Plugin) logf(level, format string, args ...any) {
	p.Log(level, fmt.Sprintf(format, args...))
}

// Debugf, Infof, Warnf and Errorf are formatted logging shortcuts.
func (p *Plugin) Debugf(f string, a ...any) { p.logf("debug", f, a...) }
func (p *Plugin) Infof(f string, a ...any)  { p.logf("info", f, a...) }
func (p *Plugin) Warnf(f string, a ...any)  { p.logf("warn", f, a...) }
func (p *Plugin) Errorf(f string, a ...any) { p.logf("error", f, a...) }

// KVGet reads a value from the shared store.
func (p *Plugin) KVGet(key string, out any) error {
	var res struct {
		Value json.RawMessage `json:"value"`
		Found bool            `json:"found"`
	}
	if err := p.call("kv.get", map[string]any{"key": key}, &res); err != nil {
		return err
	}
	if !res.Found || out == nil {
		return nil
	}
	return json.Unmarshal(res.Value, out)
}

// KVSet writes a value to the shared store.
func (p *Plugin) KVSet(key string, value any) error {
	return p.call("kv.set", map[string]any{"key": key, "value": value}, nil)
}

// KVDelete removes a key from the shared store.
func (p *Plugin) KVDelete(key string) error {
	return p.call("kv.delete", map[string]any{"key": key}, nil)
}

// Setting reads a plugin-scoped setting, namespaced under this plugin name.
func (p *Plugin) Setting(key string, out any) error {
	var res struct {
		Value json.RawMessage `json:"value"`
		Found bool            `json:"found"`
	}
	if err := p.call("settings.get", map[string]any{"key": key}, &res); err != nil {
		return err
	}
	if !res.Found || out == nil {
		return nil
	}
	return json.Unmarshal(res.Value, out)
}

// SetSetting writes a plugin-scoped setting.
func (p *Plugin) SetSetting(key string, value any) error {
	return p.call("settings.set", map[string]any{"key": key, "value": value}, nil)
}

// Send sends a text message. peer accepts a username, phone number, numeric
// ID or any t.me link.
func (p *Plugin) Send(peer, text string, opts SendOptions) (Message, error) {
	var m Message
	req := map[string]any{"peer": peer, "text": text}
	if opts.ReplyTo > 0 {
		req["reply_to"] = opts.ReplyTo
	}
	if opts.Silent {
		req["silent"] = true
	}
	if opts.NoPreview {
		req["no_preview"] = true
	}
	if opts.Schedule > 0 {
		req["schedule"] = opts.Schedule
	}
	err := p.call("tg.send", req, &m)
	return m, err
}

// Reply replies to the message that produced the event.
func (p *Plugin) Reply(e Event, text string) (Message, error) {
	var m Message
	if err := e.Unmarshal(&m); err != nil {
		return Message{}, err
	}
	return p.Send(PeerRef(m.PeerID, m.PeerType), text, SendOptions{ReplyTo: m.MessageID})
}

// PeerRef renders an InputPeer-compatible reference for a numeric peer.
func PeerRef(id int64, typ string) string {
	if typ == "channel" {
		return "-" + strconv.FormatInt(id, 10)
	}
	return strconv.FormatInt(id, 10)
}

// GetMe returns the authenticated account.
func (p *Plugin) GetMe() (User, error) {
	var u User
	err := p.call("tg.get_me", nil, &u)
	return u, err
}

// History returns up to limit recent messages from a peer.
func (p *Plugin) History(peer string, limit int) ([]Message, error) {
	var res struct {
		Messages []Message `json:"messages"`
	}
	err := p.call("tg.history", map[string]any{"peer": peer, "limit": limit}, &res)
	return res.Messages, err
}

// Notify shows a toast in the Aurora web panel.
func (p *Plugin) Notify(title, text, level string) error {
	return p.call("ui.notify", map[string]any{
		"title": title, "text": text, "level": level,
	}, nil)
}

// HTTPGet performs a GET request. Requires the "net" permission.
func (p *Plugin) HTTPGet(url string, headers map[string]string) (status int, body string, err error) {
	var res struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	req := map[string]any{"url": url}
	if len(headers) > 0 {
		req["headers"] = headers
	}
	err = p.call("http.request", req, &res)
	return res.Status, res.Body, err
}

// Subscribe asks the host to forward additional events. Optional: the host
// already forwards everything named in the manifest.
func (p *Plugin) Subscribe(names ...string) error {
	if len(names) == 0 {
		names = []string{"*"}
	}
	return p.call("event.subscribe", map[string]any{"names": names}, nil)
}

// Run performs the handshake and serves until the host closes the pipe.
func (p *Plugin) Run(args []string) error {
	if len(args) == 0 {
		args = os.Args[1:]
	}
	p.in = bufio.NewReader(os.Stdin)
	p.out = os.Stdout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx, p.cancel = ctx, cancel

	go p.readLoop()

	var hello HostInfo
	if err := p.call("plugin.hello", map[string]any{
		"protocol": ProtocolVersion,
		"args":     args,
		"pid":      os.Getpid(),
	}, &hello); err != nil {
		return err
	}
	if hello.Protocol != ProtocolVersion {
		return fmt.Errorf("aurora: protocol mismatch (host=%d, plugin=%d)", hello.Protocol, ProtocolVersion)
	}
	p.name = hello.Plugin

	p.mu.Lock()
	hook := p.onStart
	p.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return err
		}
	}

	<-ctx.Done()
	return nil
}

func (p *Plugin) readLoop() {
	dec := json.NewDecoder(bufio.NewReaderSize(p.in, 256*1024))
	dec.UseNumber()
	for {
		var msg rpcMessage
		if err := dec.Decode(&msg); err != nil {
			p.cancel()
			return
		}
		p.route(msg)
	}
}

func (p *Plugin) route(msg rpcMessage) {
	switch msg.Method {
	case "":
		// Response to one of our calls.
		if msg.ID == nil {
			return
		}
		key := string(*msg.ID)
		p.mu.Lock()
		ch, ok := p.pending[key]
		delete(p.pending, key)
		p.mu.Unlock()
		if ok {
			ch <- msg
		}

	case "event":
		ev := Event{}
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		p.dispatchEvent(ev)

	case "command":
		p.dispatchCommand(msg)

	default:
		// Host request: plugin.hello / plugin.load / plugin.unload / custom.
		p.dispatchHostRequest(msg)
	}
}

func (p *Plugin) dispatchEvent(ev Event) {
	p.mu.Lock()
	h := p.events[ev.Name]
	anyH := p.events["*"]
	p.mu.Unlock()
	if anyH != nil {
		go func() { _ = anyH(context.Background(), ev) }()
	}
	if h != nil {
		go func() { _ = h(context.Background(), ev) }()
	}
}

func (p *Plugin) dispatchCommand(msg rpcMessage) {
	var c Command
	_ = json.Unmarshal(msg.Params, &c)
	if c.Name == "" {
		c.Name = msg.Method
	}
	p.mu.Lock()
	h := p.commands[c.Name]
	if h == nil {
		// Accept the ".suffix" form used by the web panel and CLI.
		for key, cand := range p.commands {
			if name, ok := trimCommandSuffix(c.Name); ok && key == name {
				h = cand
				break
			}
		}
	}
	p.mu.Unlock()

	var result string
	var err error
	if h != nil {
		result, err = h(context.Background(), c)
	} else {
		err = fmt.Errorf("aurora: unknown command %q", c.Name)
	}
	p.reply(msg, result, err)
}

// trimCommandSuffix turns "hello.world" into ("hello", true).
func trimCommandSuffix(name string) (string, bool) {
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			return name[:i], true
		}
	}
	return "", false
}

func (p *Plugin) dispatchHostRequest(msg rpcMessage) {
	var result any
	var handleErr error

	switch msg.Method {
	case "plugin.load":
		p.mu.Lock()
		hook := p.onStart
		p.mu.Unlock()
		if hook != nil {
			handleErr = hook(context.Background())
		}
		result = map[string]any{"ok": handleErr == nil, "name": p.name}

	case "plugin.unload":
		p.mu.Lock()
		hook := p.onStop
		p.mu.Unlock()
		if hook != nil {
			handleErr = hook(context.Background())
		}
		result = map[string]any{"ok": handleErr == nil, "name": p.name}

	case "ping":
		result = map[string]any{"ok": true, "ts": time.Now().UnixMilli()}

	default:
		p.mu.Lock()
		h := p.methods[msg.Method]
		p.mu.Unlock()
		if h == nil {
			p.reply(msg, nil, fmt.Errorf("unknown method %q", msg.Method))
			return
		}
		var c Command
		_ = json.Unmarshal(msg.Params, &c)
		c.Name = msg.Method
		s, err := h(context.Background(), c)
		handleErr = err
		result = s
	}

	p.reply(msg, result, handleErr)
}

func (p *Plugin) reply(msg rpcMessage, result any, err error) {
	if msg.ID == nil {
		return
	}
	frame := map[string]any{"jsonrpc": "2.0", "id": *msg.ID}
	if err != nil {
		frame["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else if result == nil {
		frame["result"] = nil
	} else {
		frame["result"] = result
	}
	buf, mErr := json.Marshal(frame)
	if mErr != nil {
		return
	}
	p.writeMu.Lock()
	_, _ = p.out.Write(append(buf, '\n'))
	p.writeMu.Unlock()
}

func (p *Plugin) call(method string, params, out any) error {
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}

	p.writeMu.Lock()
	p.seq++
	id := json.RawMessage(strconv.FormatInt(p.seq, 10))
	p.mu.Lock()
	ch := make(chan rpcMessage, 1)
	p.pending[string(id)] = ch
	p.mu.Unlock()

	frame := map[string]any{"jsonrpc": "2.0", "method": method, "id": json.RawMessage(id)}
	if raw != nil {
		frame["params"] = json.RawMessage(raw)
	}
	buf, err := json.Marshal(frame)
	if err == nil {
		_, err = p.out.Write(append(buf, '\n'))
	}
	p.writeMu.Unlock()
	if err != nil {
		p.mu.Lock()
		delete(p.pending, string(id))
		p.mu.Unlock()
		return err
	}

	timer := time.NewTimer(p.callTimout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, string(id))
		p.mu.Unlock()
		return ctx.Err()
	case <-timer.C:
		p.mu.Lock()
		delete(p.pending, string(id))
		p.mu.Unlock()
		return fmt.Errorf("aurora: %s timed out after %s", method, p.callTimout)
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

// Fields splits a command string on whitespace, honouring quotes.
func Fields(s string) []string {
	var (
		out   []string
		cur   []rune
		quote rune
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur = append(cur, r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}
