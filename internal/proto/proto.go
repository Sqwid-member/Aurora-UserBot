// Package proto holds the wire types shared between Aurora's internal layers:
// the plugin protocol, the web API and the Telegram runtime.
//
// Keeping them in one place means a payload added for plugins is automatically
// available to the control panel, and vice versa.
package proto

import (
	"encoding/json"
	"time"
)

// ProtocolVersion is the plugin protocol revision understood by this build.
const ProtocolVersion = 1

// ---------- events ----------

// Event is the envelope the host pushes to plugins.
type Event struct {
	AccountID string `json:"account_id,omitempty"`
	Name      string `json:"name"`
	Data      any    `json:"data"`
}

// Event names. Plugins subscribe by declaring them in their manifest, or by
// using the catch-all "*".
const (
	EventCoreStart     = "core.start"
	EventCoreStop      = "core.stop"
	EventSessionStart  = "session.started"
	EventSessionEnd    = "session.ended"
	EventMessageNew    = "message.new"
	EventMessageEdit   = "message.edited"
	EventMessageDelete = "message.deleted"
	EventUserTyping    = "user.typing"
	EventChatAction    = "chat.action"
	EventCommand       = "command.received"
)

// AllEvents is the full catalogue, used by the web panel help view.
var AllEvents = []string{
	EventCoreStart, EventCoreStop,
	EventSessionStart, EventSessionEnd,
	EventMessageNew, EventMessageEdit, EventMessageDelete,
	EventUserTyping, EventChatAction, EventCommand,
}

// Message is the canonical shape of a Telegram message as seen by plugins
// and by the control panel.
type Message struct {
	ID         int    `json:"message_id"`
	PeerID     int64  `json:"peer_id"`
	PeerType   string `json:"peer_type"` // user | chat | channel
	PeerTitle  string `json:"peer_title"`
	FromID     int64  `json:"from_id"`
	FromName   string `json:"from_name"`
	FromBot    bool   `json:"from_bot"`
	Text       string `json:"text"`
	Date       int64  `json:"date"`
	Out        bool   `json:"out"`
	ReplyTo    int    `json:"reply_to,omitempty"`
	Media      string `json:"media,omitempty"`
	IsPrivate  bool   `json:"is_private"`
	MentionsMe bool   `json:"mentions_me,omitempty"`
}

// Time returns the message date as a time.Time in local time.
func (m Message) Time() time.Time { return time.Unix(m.Date, 0) }

// User is a Telegram user profile.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
	First    string `json:"first_name,omitempty"`
	Last     string `json:"last_name,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
	Premium  bool   `json:"premium,omitempty"`
}

// Display renders the friendliest available name.
func (u User) Display() string {
	switch {
	case u.Username != "":
		return "@" + u.Username
	case u.First != "" || u.Last != "":
		return trimSpace(u.First + " " + u.Last)
	default:
		return "user:" + itoa(u.ID)
	}
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// PeerInfo describes a resolved peer.
type PeerInfo struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
}

// ---------- host API payloads ----------

// SendRequest is the payload of the tg.send method.
type SendRequest struct {
	AccountID string `json:"account_id,omitempty"`
	Peer      string `json:"peer"`
	Text      string `json:"text"`
	ReplyTo   int    `json:"reply_to,omitempty"`
	Silent    bool   `json:"silent,omitempty"`
	NoPreview bool   `json:"no_preview,omitempty"`
	Schedule  int64  `json:"schedule,omitempty"`
	ParseMode string `json:"parse_mode,omitempty"` // "", "html", "markdown"
}

// SendResult is the result of tg.send.
type SendResult struct {
	ID     int    `json:"id"`
	PeerID int64  `json:"peer_id"`
	Date   int64  `json:"date"`
	Text   string `json:"text,omitempty"`
}

// LogRequest is the payload of the log method.
type LogRequest struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Plugin string `json:"plugin,omitempty"`
	Fields any    `json:"fields,omitempty"`
}

// KVRequest is the payload of kv.get / kv.set / kv.delete.
type KVRequest struct {
	Key   string `json:"key"`
	Value any    `json:"value,omitempty"`
}

// KVResult is the result of kv.get.
type KVResult struct {
	Value json.RawMessage `json:"value,omitempty"`
	Found bool            `json:"found"`
}

// HTTPRequest is the payload of http.request.
type HTTPRequest struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds
}

// HTTPResult is the result of http.request.
type HTTPResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
	Trunc   bool              `json:"truncated,omitempty"`
}

// SubscribeRequest is the payload of event.subscribe.
type SubscribeRequest struct {
	Names []string `json:"names"`
}

// CommandRequest is what the host sends to a plugin's command handler.
type CommandRequest struct {
	Name string   `json:"name"`
	Text string   `json:"text"`
	Args []string `json:"args,omitempty"`
}

// CommandResult is what a command handler returns.
type CommandResult struct {
	Text string `json:"text"`
}

// NotifyRequest is the payload of ui.notify.
type NotifyRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level,omitempty"` // info | warn | error
}

// ---------- login & session ----------

// AuthStep describes what the login flow is currently waiting for. It lives
// here rather than in the transport package so the control panel can render it
// without pulling in MTProto.
type AuthStep string

// Login steps.
const (
	AuthNone     AuthStep = "none"
	AuthPhone    AuthStep = "phone"
	AuthCode     AuthStep = "code"
	AuthPassword AuthStep = "password"
	AuthSignedIn AuthStep = "signed_in"
)

// SessionInfo is a safe summary of the stored session: it never contains the
// auth key itself.
type SessionInfo struct {
	Exists    bool   `json:"exists"`
	DC        int    `json:"dc,omitempty"`
	Address   string `json:"address,omitempty"`
	AuthKeyID string `json:"auth_key_id,omitempty"`
}

// ---------- runtime status ----------

// SessionState describes the Telegram connection.
type SessionState string

// Session states.
const (
	StateOffline    SessionState = "offline"
	StateConnecting SessionState = "connecting"
	StateUnauth     SessionState = "unauthorized"
	StateAuthorized SessionState = "authorized"
	StateError      SessionState = "error"
)

// AccountInfo describes a Telegram account in multi-account mode.
type AccountInfo struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Phone          string       `json:"phone,omitempty"`
	User           *User        `json:"user,omitempty"`
	Session        SessionState `json:"session"`
	SessionError   string       `json:"session_error,omitempty"`
	EnabledPlugins []string     `json:"enabled_plugins"`
	IsActive       bool         `json:"is_active"`
}

// Status is the payload behind GET /api/status.
type Status struct {
	Core          string           `json:"core"`
	Version       string           `json:"version"`
	GoVersion     string           `json:"go_version"`
	Uptime        string           `json:"uptime"`
	UptimeSec     int64            `json:"uptime_sec"`
	MemoryMB      float64          `json:"memory_mb"`
	Goroutines    int              `json:"goroutines"`
	Session       SessionState     `json:"session"`
	User          *User            `json:"user,omitempty"`
	PluginCount   int              `json:"plugin_count"`
	PluginsUp     int              `json:"plugins_running"`
	Web           WebStatus        `json:"web"`
	MemLimitMB    int              `json:"mem_limit_mb"`
	Counters      map[string]int64 `json:"counters,omitempty"`
	ActiveAccount string           `json:"active_account,omitempty"`
	Accounts      []AccountInfo    `json:"accounts,omitempty"`
}

// WebStatus describes the control panel endpoint.
type WebStatus struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`
}

// MarshalIndent is a convenience for API responses.
func MarshalIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
