package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/ipc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

const (
	maxHTTPBody = 1 << 20 // 1 MiB
	settingsNS  = "plugin:"
)

// BindAPI registers every host method on a freshly started plugin connection.
//
// It is called twice per start — once the moment the transport is up (via
// Options.Connect) and once after the handshake (via Host.Start). Registering
// is idempotent, and the early call closes the race where a plugin's
// plugin.load handler fires kv.get/ui.notify before the late call runs.
//
// Every method re-checks the plugin's manifest permissions, because a plugin
// is just an untrusted process: it can write anything it likes to its own
// stdout, and the only real barrier is what the host agrees to do on its
// behalf.
func (h *Host) BindAPI(p *Instance) {
	m := p.Manifest
	conn := func() *ipc.Conn {
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.conn
	}
	if c := conn(); c == nil {
		return
	}
	c := conn()
	denied := func(cap string) *ipc.Error {
		return ipc.NewError(ipc.CodeForbidden, "plugin %q lacks the %q permission", m.Name, cap)
	}
	notReady := func() *ipc.Error {
		return ipc.NewError(ipc.CodeUnavailable, "telegram session is not ready yet")
	}

	// ---- logging -------------------------------------------------------
	c.Handle("log", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.LogRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		lvl, ok := logx.ParseLevel(req.Level)
		if !ok {
			lvl = logx.LevelInfo
		}
		p.log.Log(lvl, req.Msg)
		return map[string]any{"ok": true}, nil
	})

	// ---- shared key-value store ---------------------------------------
	// The "plugin:" namespace is reserved for plugin-private settings:
	// without this, any plugin could read (or wipe) every other plugin's
	// secrets with kv.get/kv.keys, bypassing the settings.get boundary.
	c.Handle("kv.get", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Key == "" {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "key is required")
		}
		if isReservedKey(req.Key) {
			return nil, ipc.NewError(ipc.CodeForbidden, "key namespace %q is reserved; use settings.*", settingsNS)
		}
		value, found := h.kv.GetRaw(req.Key)
		return proto.KVResult{Value: value, Found: found}, nil
	})
	c.Handle("kv.set", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Key == "" {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "key is required")
		}
		if isReservedKey(req.Key) {
			return nil, ipc.NewError(ipc.CodeForbidden, "key namespace %q is reserved; use settings.*", settingsNS)
		}
		if err := h.kv.Set(req.Key, req.Value); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "%v", err)
		}
		return map[string]any{"ok": true}, nil
	})
	c.Handle("kv.delete", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Key == "" {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "key is required")
		}
		if isReservedKey(req.Key) {
			return nil, ipc.NewError(ipc.CodeForbidden, "key namespace %q is reserved; use settings.*", settingsNS)
		}
		h.kv.Delete(req.Key)
		return map[string]any{"ok": true}, nil
	})
	c.Handle("kv.keys", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if isReservedKey(req.Key) {
			return nil, ipc.NewError(ipc.CodeForbidden, "key namespace %q is reserved; use settings.*", settingsNS)
		}
		keys := h.kv.Keys(req.Key)
		// Defense in depth: an empty prefix lists everything, so strip the
		// reserved namespace from results no matter what was asked.
		visible := keys[:0]
		for _, k := range keys {
			if !isReservedKey(k) {
				visible = append(visible, k)
			}
		}
		return map[string]any{"keys": visible}, nil
	})

	// ---- plugin-private settings --------------------------------------
	c.Handle("settings.get", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		value, found := h.kv.GetRaw(settingsNS + m.Name + ":" + req.Key)
		return proto.KVResult{Value: value, Found: found}, nil
	})
	c.Handle("settings.set", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if err := h.kv.Set(settingsNS+m.Name+":"+req.Key, req.Value); err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "%v", err)
		}
		return map[string]any{"ok": true}, nil
	})

	c.Handle("settings.schema", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req struct {
			Fields []SettingField `json:"fields"`
			Mode   string         `json:"mode"`
		}
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		effective, err := h.SetDynamicSchema(m.Name, req.Fields, req.Mode)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "%v", err)
		}
		return map[string]any{"ok": true, "fields": effective}, nil
	})

	// ---- config (opt-in) ----------------------------------------------
	c.Handle("config.get", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.Permissions.Config {
			return nil, denied("config")
		}
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		v, ok := h.services.ConfigValue(req.Key)
		return proto.KVResult{Value: toRaw(v), Found: ok}, nil
	})
	c.Handle("config.set", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.Permissions.Config {
			return nil, denied("config")
		}
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		return nil, ipc.NewError(ipc.CodeInvalidRequest, "config.set is not supported; edit config.json instead")
	})

	// ---- telegram ------------------------------------------------------
	c.Handle("tg.get_me", func(ctx context.Context, _ json.RawMessage) (any, *ipc.Error) {
		if !h.services.Ready() {
			return nil, notReady()
		}
		u, err := h.services.GetMe(ctx)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
		}
		return u, nil
	})
	c.Handle("tg.send", func(ctx context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.HasCapability(CapSend) {
			return nil, denied("tg.send")
		}
		if !h.services.Ready() {
			return nil, notReady()
		}
		var req proto.SendRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.Text) == "" {
			return nil, ipc.NewError(ipc.CodeInvalidParams, "text is required")
		}
		res, err := h.services.Send(ctx, req)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
		}
		return res, nil
	})
	c.Handle("tg.history", func(ctx context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.HasCapability(CapRead) {
			return nil, denied("tg.history")
		}
		if !h.services.Ready() {
			return nil, notReady()
		}
		var req struct {
			Peer  string `json:"peer"`
			Limit int    `json:"limit"`
		}
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Limit <= 0 || req.Limit > 100 {
			req.Limit = 20
		}
		msgs, err := h.services.History(ctx, req.Peer, req.Limit)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
		}
		return map[string]any{"messages": msgs}, nil
	})
	c.Handle("tg.resolve", func(ctx context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.HasCapability(CapResolve) {
			return nil, denied("tg.resolve")
		}
		if !h.services.Ready() {
			return nil, notReady()
		}
		var req proto.KVRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		info, err := h.services.Resolve(ctx, req.Key)
		if err != nil {
			return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
		}
		return info, nil
	})

	// ---- control panel --------------------------------------------------
	c.Handle("ui.notify", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.NotifyRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		if req.Level == "" {
			req.Level = "info"
		}
		h.services.Notify(req.Title, req.Text, req.Level)
		return map[string]any{"ok": true}, nil
	})

	// ---- outbound HTTP (opt-in) -----------------------------------------
	c.Handle("http.request", func(ctx context.Context, raw json.RawMessage) (any, *ipc.Error) {
		if !m.Permissions.Net {
			return nil, denied("net")
		}
		var req proto.HTTPRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		return doHTTP(ctx, req)
	})

	// ---- events and introspection ---------------------------------------
	c.Handle("event.subscribe", func(_ context.Context, raw json.RawMessage) (any, *ipc.Error) {
		var req proto.SubscribeRequest
		if err := decode(raw, &req); err != nil {
			return nil, err
		}
		events := m.SubscribeEvents(req.Names)
		return map[string]any{"ok": true, "events": events}, nil
	})
	c.Handle("core.info", func(_ context.Context, _ json.RawMessage) (any, *ipc.Error) {
		return map[string]any{
			"version":  h.opts.Version,
			"protocol": ipc.ProtocolVersion,
			"plugin":   m.Name,
			"methods":  HostAPIMethods,
			"events":   proto.AllEvents,
			"pid":      p.pid(),
		}, nil
	})
}

func (p *Instance) pid() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func decode(raw json.RawMessage, v any) *ipc.Error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return ipc.NewError(ipc.CodeInvalidParams, "%v", err)
	}
	return nil
}

func toRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// Private/LAN ranges must not be reachable from plugins: router admin,
	// NAS, cameras, Termux itself. Covers IPv4 + IPv6 ULA + CGNAT.
	if ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 127 || ip4[0] == 0 || (ip4[0] == 169 && ip4[1] == 254) {
			return true
		}
		// 100.64.0.0/10 CGNAT (not covered by IsPrivate on older Go).
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return false
	}
	// IPv6 extras: fe80::/10 link-local, fc00::/7 ULA, ::ffff:0:0/96 mapped.
	if strings.HasPrefix(strings.ToLower(ip.String()), "fe80:") {
		return true
	}
	return false
}

var (
	safeTransportOnce sync.Once
	safeTransport     *http.Transport
)

func getSafeTransport() *http.Transport {
	safeTransportOnce.Do(func() {
		dialer := &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		safeTransport = &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("no IP addresses resolved for host %q", host)
				}
				var chosenIP net.IP
				for _, ip := range ips {
					if isBlockedIP(ip) {
						return nil, fmt.Errorf("ssrf blocked: access to address %s is prohibited", ip.String())
					}
					if chosenIP == nil {
						chosenIP = ip
					}
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(chosenIP.String(), port))
			},
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	})
	return safeTransport
}

func newSafeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: getSafeTransport(),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to non-http(s) scheme prohibited")
			}
			if strings.EqualFold(req.URL.Hostname(), "localhost") {
				return errors.New("ssrf blocked: redirect to localhost is prohibited")
			}
			return nil
		},
	}
}

// doHTTP performs a bounded outbound request on behalf of a plugin with SSRF protection.
func doHTTP(ctx context.Context, req proto.HTTPRequest) (any, *ipc.Error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, ipc.NewError(ipc.CodeInvalidParams, "invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ipc.NewError(ipc.CodeForbidden, "only http and https are allowed")
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return nil, ipc.NewError(ipc.CodeForbidden, "ssrf blocked: access to localhost is prohibited")
	}
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(rctx, method, req.URL, body)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInvalidParams, "%v", err)
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	client := newSafeHTTPClient(timeout)
	resp, err := client.Do(hreq)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return nil, ipc.NewError(ipc.CodeInternalError, "%v", err)
	}
	truncated := false
	if len(raw) > maxHTTPBody {
		raw = raw[:maxHTTPBody]
		truncated = true
	}
	headers := make(map[string]string, 4)
	for k := range resp.Header {
		headers[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return proto.HTTPResult{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    string(raw),
		Trunc:   truncated,
	}, nil
}

// describe renders a plugin for CLI help.
func describe(m *Manifest) string {
	return fmt.Sprintf("%s %s (%s) — %s", m.Name, orDefault(m.Version, "0"), orDefault(m.Language, "?"), orDefault(m.Description, "no description"))
}
