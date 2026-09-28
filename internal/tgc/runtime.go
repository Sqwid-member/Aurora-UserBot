// Package tgc wraps the MTProto client with everything Aurora needs:
// connection, interactive login, peer resolution and a small typed façade
// over the generated API.
package tgc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

// Options configures the Telegram runtime.
type Options struct {
	AppID       int
	AppHash     string
	Phone       string
	SessionPath string
	TestDC      bool

	BlockedMode bool
	MTProxy     string
	Socks5      string

	DeviceName     string
	DeviceModel    string
	DeviceSystem   string
	DeviceVersion  string
	DeviceLanguage string

	PFS       bool
	NoUpdates bool
	Logger    *logx.Logger
	// OnState fires whenever the connection state changes.
	OnState func(proto.SessionState, string)
	// OnEvent fans normalized events out to the plugin host.
	OnEvent func(name string, data any)
}

// AuthStep describes what the login flow is currently waiting for.
type AuthStep = proto.AuthStep

// Login steps exposed to the CLI and the web panel.
const (
	AuthNone     = proto.AuthNone
	AuthPhone    = proto.AuthPhone
	AuthCode     = proto.AuthCode
	AuthPassword = proto.AuthPassword
	AuthSignedIn = proto.AuthSignedIn
)

// Runtime owns the MTProto connection.
type Runtime struct {
	opts Options
	log  *logx.Logger

	mu     sync.RWMutex
	client *telegram.Client
	pman   *peers.Manager
	sender *message.Sender
	me     *proto.User
	state  proto.SessionState
	lastEr string
	step   AuthStep

	codeHash string
	authDone chan struct{}

	namesMu sync.RWMutex
	names   map[int64]proto.PeerInfo
}

// New builds an unconnected runtime.
func New(opts Options) *Runtime {
	if opts.Logger == nil {
		opts.Logger = logx.New(logx.Options{}, "tgc")
	}
	return &Runtime{
		opts:     opts,
		log:      opts.Logger.Scoped("tgc"),
		state:    proto.StateOffline,
		step:     AuthNone,
		names:    make(map[int64]proto.PeerInfo, 128),
		authDone: make(chan struct{}, 1),
	}
}

// State returns the connection state.
func (r *Runtime) State() proto.SessionState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

// LastError returns the last connection error, if any.
func (r *Runtime) LastError() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastEr
}

// Step returns the current login step.
func (r *Runtime) Step() AuthStep {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.step
}

// Me returns the authenticated account, or nil.
func (r *Runtime) Me() *proto.User {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.me
}

// Ready reports whether Telegram calls can be served right now.
func (r *Runtime) Ready() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sender != nil && r.state == proto.StateAuthorized
}

func (r *Runtime) setState(s proto.SessionState, err string) {
	r.mu.Lock()
	changed := r.state != s
	r.state = s
	r.lastEr = err
	if s != proto.StateAuthorized && s != proto.StateUnauth {
		r.sender = nil
	}
	r.mu.Unlock()
	if changed && r.opts.OnState != nil {
		r.opts.OnState(s, err)
	}
}

// Run connects, authorizes and then pumps updates until ctx is done.
func (r *Runtime) Run(ctx context.Context) error {
	if r.opts.AppID == 0 || strings.TrimSpace(r.opts.AppHash) == "" {
		return errors.New("tgc: app_id and app_hash are required")
	}

	resolver, err := r.resolver()
	if err != nil {
		return err
	}

	var sink telegram.UpdateHandler = telegram.UpdateHandlerFunc(r.handleUpdates)

	client := telegram.NewClient(r.opts.AppID, r.opts.AppHash, telegram.Options{
		Resolver: resolver,
		SessionStorage: &session.FileStorage{
			Path: r.opts.SessionPath,
		},
		UpdateHandler: sink,
		NoUpdates:     r.opts.NoUpdates,
		Device: telegram.DeviceConfig{
			// gotd has no separate "device name" field, so the name and model
			// travel together in DeviceModel — that is what shows up in
			// Settings → Devices on the phone.
			DeviceModel:    deviceModel(r.opts.DeviceName, r.opts.DeviceModel),
			SystemVersion:  r.opts.DeviceSystem,
			AppVersion:     r.opts.DeviceVersion,
			SystemLangCode: r.opts.DeviceLanguage,
			LangCode:       r.opts.DeviceLanguage,
		},
		EnablePFS: r.opts.PFS,
		OnConnectionState: func(s telegram.ConnectionState) {
			r.log.Debug("connection state", logx.F("state", s.String()))
		},
		OnSelfError: func(ctx context.Context, err error) error {
			if r.isAuthError(err) {
				// Not being authorized is normal before the first login.
				return nil
			}
			return err
		},
		OnDead: func(err error) {
			r.log.Warn("connection lost", logx.F("error", err))
			r.setState(proto.StateOffline, errString(err))
		},
	})

	r.mu.Lock()
	r.client = client
	r.mu.Unlock()

	r.setState(proto.StateConnecting, "")

	return client.Run(ctx, func(ctx context.Context) error {
		aclient := client.Auth()
		status, err := aclient.Status(ctx)
		if err == nil && status.Authorized {
			return r.serve(ctx)
		}

		r.setState(proto.StateUnauth, "")
		r.mu.Lock()
		if r.step == AuthNone || r.step == AuthSignedIn {
			r.step = AuthPhone
		}
		r.mu.Unlock()

		// Wait until login completes via Web/CLI or context cancels
		for {
			select {
			case <-r.authDone:
				status, err := aclient.Status(ctx)
				if err == nil && status.Authorized {
					return r.serve(ctx)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}

func (r *Runtime) isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "auth key") || strings.Contains(msg, "unauthorized")
}

func (r *Runtime) serve(ctx context.Context) error {
	api := r.client.API()

	pm := peers.Options{}.Build(api)
	if err := pm.Init(ctx); err != nil {
		return fmt.Errorf("peers init: %w", err)
	}

	self, err := pm.Self(ctx)
	if err != nil {
		return fmt.Errorf("self: %w", err)
	}
	r.remember(proto.PeerInfo{ID: self.ID(), Type: "user", Title: self.VisibleName()})

	username, _ := self.Username()

	r.mu.Lock()
	r.pman = pm
	r.sender = message.NewSender(api).WithResolver(peerAdapter{m: pm})
	r.me = &proto.User{
		ID:       self.ID(),
		Username: username,
		First:    self.Raw().FirstName,
		Last:     self.Raw().LastName,
		Phone:    self.Raw().Phone,
		Bot:      self.Raw().Bot,
		Premium:  self.Raw().Premium,
	}
	r.step = AuthSignedIn
	r.mu.Unlock()

	r.setState(proto.StateAuthorized, "")
	r.log.Info("session ready",
		logx.F("user", r.me.Display()),
		logx.F("id", r.me.ID),
	)

	<-ctx.Done()
	return nil
}

// ---- login ----

// CleanPhone formats any phone number into international E.164 format (+380..., +48...).
// ErrLoginAborted is returned when login cannot continue.
var ErrLoginAborted = errors.New("tgc: login aborted")

func CleanPhone(phone string) string {
	var b strings.Builder
	for _, ch := range phone {
		if ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
		} else if ch == '+' && b.Len() == 0 {
			b.WriteRune(ch)
		}
	}
	s := b.String()
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "0") && len(s) == 10 {
		return "+38" + s
	}
	if strings.HasPrefix(s, "380") {
		return "+" + s
	}
	if strings.HasPrefix(s, "48") && len(s) == 11 {
		return "+" + s
	}
	if !strings.HasPrefix(s, "+") {
		return "+" + s
	}
	return s
}

func translateTelegramErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	u := strings.ToUpper(msg)
	switch {
	case strings.Contains(u, "PHONE_NUMBER_INVALID"):
		return errors.New("неправильний номер телефону. Вкажіть у міжнародному форматі (+380XXXXXXXXX або +48XXXXXXXXX)")
	case strings.Contains(u, "PHONE_CODE_INVALID"):
		return errors.New("невірний код підтвердження")
	case strings.Contains(u, "PHONE_CODE_EXPIRED"):
		return errors.New("термін дії коду вичерпано. Надішліть код повторно")
	case strings.Contains(u, "FLOOD_WAIT"):
		return errors.New("забагато спроб від Telegram (FLOOD_WAIT). Зачекайте деякий час перед наступною спробою")
	case strings.Contains(u, "PASSWORD_HASH_INVALID"):
		return errors.New("невірний 2FA пароль")
	case strings.Contains(u, "API_ID_INVALID"):
		return errors.New("Telegram відхилив API ключі (API_ID_INVALID). Виконайте 'aurora setup' для власних ключів")
	case strings.Contains(u, "PHONE_NUMBER_BANNED"):
		return errors.New("цей номер телефону заблоковано в Telegram")
	default:
		return err
	}
}

// RequestCode initiates login by sending a verification code to the phone number.
func (r *Runtime) RequestCode(ctx context.Context, phone string) error {
	phone = CleanPhone(phone)
	if phone == "" {
		return errors.New("номер телефону не може бути порожнім")
	}

	r.mu.Lock()
	r.opts.Phone = phone
	client := r.client
	r.mu.Unlock()

	if client == nil {
		return errors.New("Telegram клієнт ще підключається... Зачекайте пару секунд")
	}

	aclient := client.Auth()
	r.log.Info("requesting login code", logx.F("phone", maskPhone(phone)))

	sent, err := aclient.SendCode(ctx, phone, auth.SendCodeOptions{AllowAppHash: true})
	if err != nil {
		r.mu.Lock()
		r.step = AuthPhone
		r.mu.Unlock()
		return translateTelegramErr(err)
	}

	sc, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return fmt.Errorf("неочікувана відповідь від Telegram: %T", sent)
	}

	r.mu.Lock()
	r.codeHash = sc.PhoneCodeHash
	r.step = AuthCode
	r.mu.Unlock()

	return nil
}

// Phone returns the configured phone number.
func (r *Runtime) Phone() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opts.Phone
}

// SetPhone updates the phone number used by the login flow.
func (r *Runtime) SetPhone(phone string) {
	r.mu.Lock()
	r.opts.Phone = CleanPhone(phone)
	r.mu.Unlock()
}

func (r *Runtime) setStep(s AuthStep) {
	r.mu.Lock()
	r.step = s
	r.mu.Unlock()
}

// SubmitCode hands the login code to Telegram.
func (r *Runtime) SubmitCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("код підтвердження не може бути порожнім")
	}

	r.mu.RLock()
	client := r.client
	phone := r.opts.Phone
	codeHash := r.codeHash
	r.mu.RUnlock()

	if client == nil {
		return errors.New("клієнт Telegram не підключено")
	}

	aclient := client.Auth()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, signErr := aclient.SignIn(ctx, phone, code, codeHash)
	switch {
	case signErr == nil:
		r.mu.Lock()
		r.step = AuthSignedIn
		r.mu.Unlock()
		select {
		case r.authDone <- struct{}{}:
		default:
		}
		return nil
	case errors.Is(signErr, auth.ErrPasswordAuthNeeded) || strings.Contains(strings.ToUpper(signErr.Error()), "SESSION_PASSWORD_NEEDED"):
		r.log.Info("two-factor password required")
		r.setStep(AuthPassword)
		return errors.New("SESSION_PASSWORD_NEEDED")
	default:
		var needSignUp *auth.SignUpRequired
		if errors.As(signErr, &needSignUp) {
			return errors.New("цей номер не зареєстрований у Telegram")
		}
		return translateTelegramErr(signErr)
	}
}

// SubmitPassword hands the 2FA password to Telegram.
func (r *Runtime) SubmitPassword(pass string) error {
	pass = strings.TrimSpace(pass)
	if pass == "" {
		return errors.New("пароль не може бути порожнім")
	}

	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("клієнт Telegram не підключено")
	}

	aclient := client.Auth()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := aclient.Password(ctx, pass); err != nil {
		return translateTelegramErr(err)
	}

	r.mu.Lock()
	r.step = AuthSignedIn
	r.mu.Unlock()

	select {
	case r.authDone <- struct{}{}:
	default:
	}
	return nil
}



// Logout terminates the current Telegram session and drops the local one.
func (r *Runtime) Logout(ctx context.Context) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		return errors.New("tgc: not connected")
	}
	_, err := client.API().AuthLogOut(ctx)
	return err
}

// ImportSession replaces the stored session from a Telethon StringSession.
func (r *Runtime) ImportSession(telethonSession string) error {
	data, err := session.TelethonSession(strings.TrimSpace(telethonSession))
	if err != nil {
		return fmt.Errorf("parse StringSession: %w", err)
	}
	loader := session.Loader{Storage: &session.FileStorage{Path: r.opts.SessionPath}}
	if err := loader.Save(context.Background(), data); err != nil {
		return err
	}
	return nil
}

// ExportSession renders the stored session as a Telethon string.
func (r *Runtime) ExportSession() (string, error) { return ExportSession(r.opts.SessionPath) }

// SessionInfo summarises the stored session without leaking the auth key.
func (r *Runtime) SessionInfo() SessionInfo { return InspectSession(r.opts.SessionPath) }

// ---- api ----

// GetMe returns the authenticated account.
func (r *Runtime) GetMe(ctx context.Context) (proto.User, error) {
	if me := r.Me(); me != nil {
		return *me, nil
	}
	return proto.User{}, errors.New("tgc: not authorized")
}

// Resolve turns a reference into a peer.
func (r *Runtime) Resolve(ctx context.Context, ref string) (proto.PeerInfo, error) {
	p, err := r.resolvePeer(ctx, ref)
	if err != nil {
		return proto.PeerInfo{}, err
	}
	return r.infoOf(p), nil
}

// Send delivers a text message.
func (r *Runtime) Send(ctx context.Context, req proto.SendRequest) (proto.SendResult, error) {
	r.mu.RLock()
	sender := r.sender
	r.mu.RUnlock()
	if sender == nil {
		return proto.SendResult{}, errors.New("tgc: session is not ready")
	}

	peer, err := r.resolvePeer(ctx, req.Peer)
	if err != nil {
		return proto.SendResult{}, err
	}
	info := r.infoOf(peer)

	rb := sender.To(peer.InputPeer())
	// RequestBuilder embeds Builder by value; every option returns a fresh
	// *Builder, so build the chain from a pointer to the embedded value.
	b := &rb.Builder
	if req.ReplyTo > 0 {
		b = b.Reply(req.ReplyTo)
	}
	if req.Silent {
		b = b.Silent()
	}
	if req.NoPreview {
		b = b.NoWebpage()
	}
	if req.Schedule > 0 {
		b = b.Schedule(time.Unix(req.Schedule, 0))
	}

	var (
		upd  tg.UpdatesClass
		err2 error
	)
	switch strings.ToLower(req.ParseMode) {
	case "html":
		resolver := r.pman.UserResolveHook(ctx)
		upd, err2 = b.StyledText(ctx, html.String(resolver, req.Text))
	default:
		upd, err2 = b.Text(ctx, req.Text)
	}
	if err2 != nil {
		return proto.SendResult{}, err2
	}

	if msg, ok := firstMessage(upd); ok {
		return proto.SendResult{
			ID:     msg.ID,
			PeerID: info.ID,
			Date:   int64(msg.Date),
			Text:   msg.Message,
		}, nil
	}
	return proto.SendResult{PeerID: info.ID, Text: req.Text}, nil
}

// History returns recent messages from a peer.
func (r *Runtime) History(ctx context.Context, ref string, limit int) ([]proto.Message, error) {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil || !r.Ready() {
		return nil, errors.New("tgc: session is not ready")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	peer, err := r.resolvePeer(ctx, ref)
	if err != nil {
		return nil, err
	}
	hist, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  peer.InputPeer(),
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	messages, ok := hist.(*tg.MessagesMessages)
	if !ok {
		return nil, fmt.Errorf("unexpected history response %T", hist)
	}
	out := make([]proto.Message, 0, len(messages.Messages))
	for _, mc := range messages.Messages {
		m, ok := mc.(*tg.Message)
		if !ok {
			continue
		}
		out = append(out, r.toMessage(ctx, m))
	}
	// Telegram returns newest first; plugins read better the other way.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ---- internals ----

// peerAdapter bridges peers.Manager to the message package resolver interface.
type peerAdapter struct{ m *peers.Manager }

func (a peerAdapter) ResolveDomain(ctx context.Context, domain string) (tg.InputPeerClass, error) {
	p, err := a.m.ResolveDomain(ctx, domain)
	if err != nil {
		return nil, err
	}
	return p.InputPeer(), nil
}

func (a peerAdapter) ResolvePhone(ctx context.Context, phone string) (tg.InputPeerClass, error) {
	u, err := a.m.ResolvePhone(ctx, phone)
	if err != nil {
		return nil, err
	}
	return u.InputPeer(), nil
}

// resolvePeer accepts usernames, phones, links, numeric IDs and "me".
func (r *Runtime) resolvePeer(ctx context.Context, ref string) (peers.Peer, error) {
	r.mu.RLock()
	pm := r.pman
	r.mu.RUnlock()

	s := strings.TrimSpace(ref)
	if s == "" {
		return nil, errors.New("tgc: empty peer")
	}
	lowered := strings.ToLower(s)
	if lowered == "me" || lowered == "self" {
		return pm.Self(ctx)
	}
	s = strings.TrimPrefix(s, "@")

	if id, err := parseID(s); err == nil {
		// Try the most specific interpretation first: channels, chats, users.
		if id < 0 {
			if p, err := pm.ResolveChannelID(ctx, -id); err == nil {
				return p, nil
			}
		} else {
			if p, err := pm.ResolveChannelID(ctx, id); err == nil {
				return p, nil
			}
			if p, err := pm.ResolveUserID(ctx, id); err == nil {
				return p, nil
			}
			if p, err := pm.ResolveChatID(ctx, id); err == nil {
				return p, nil
			}
		}
		return nil, fmt.Errorf("peer %q is not in the local cache: open a chat with it once, or address it by @username", ref)
	}

	if strings.HasPrefix(s, "+") {
		return pm.ResolvePhone(ctx, s)
	}
	if strings.Contains(s, "t.me") || strings.Contains(s, "tg://") {
		return pm.Resolve(ctx, s)
	}
	return pm.ResolveDomain(ctx, s)
}

func parseID(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg, i = true, 1
	}
	if i >= len(s) {
		return 0, errors.New("empty")
	}
	var v int64
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("not numeric")
		}
		v = v*10 + int64(c-'0')
		if v > 1<<52 {
			return 0, errors.New("too large")
		}
	}
	if neg {
		v = -v
	}
	return v, nil
}

func (r *Runtime) infoOf(p peers.Peer) proto.PeerInfo {
	info := proto.PeerInfo{
		ID:    p.ID(),
		Type:  peerType(p),
		Title: p.VisibleName(),
	}
	if u, ok := p.Username(); ok {
		info.Username = u
	}
	r.remember(info)
	return info
}

func peerType(p peers.Peer) string {
	switch p.(type) {
	case peers.User:
		return "user"
	case peers.Chat:
		return "chat"
	case peers.Channel:
		return "channel"
	default:
		return "user"
	}
}

func (r *Runtime) remember(info proto.PeerInfo) {
	if info.ID == 0 {
		return
	}
	r.namesMu.Lock()
	r.names[info.ID] = info
	// Bound the cache: a busy account can see a lot of one-off peers.
	if len(r.names) > 4096 {
		clear(r.names)
	}
	r.namesMu.Unlock()
}

func (r *Runtime) lookup(id int64) (proto.PeerInfo, bool) {
	r.namesMu.RLock()
	defer r.namesMu.RUnlock()
	info, ok := r.names[id]
	return info, ok
}

// firstMessage digs the sent message out of an updates result.
func firstMessage(u tg.UpdatesClass) (*tg.Message, bool) {
	switch v := u.(type) {
	case *tg.Updates:
		return scanUpdates(v.Updates)
	case *tg.UpdatesCombined:
		return scanUpdates(v.Updates)
	case *tg.UpdateShort:
		return scanUpdates([]tg.UpdateClass{v.Update})
	case *tg.UpdateShortSentMessage:
		// UpdateShortSentMessage carries no peer, only the new message id.
		return &tg.Message{ID: v.ID, Date: v.Date, Out: true}, true
	default:
		return nil, false
	}
}

func scanUpdates(ups []tg.UpdateClass) (*tg.Message, bool) {
	for _, u := range ups {
		switch v := u.(type) {
		case *tg.UpdateNewMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				return m, true
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := v.Message.(*tg.Message); ok {
				return m, true
			}
		}
	}
	return nil, false
}

func (r *Runtime) resolver() (dcs.Resolver, error) {
	if s := strings.TrimSpace(r.opts.MTProxy); s != "" {
		addr, secret, err := parseMTProxy(s)
		if err != nil {
			return nil, err
		}
		res, err := dcs.MTProxy(addr, secret, dcs.MTProxyOptions{})
		if err != nil {
			return nil, fmt.Errorf("mtproxy: %w", err)
		}
		r.log.Info("using MTProxy", logx.F("addr", addr))
		return res, nil
	}

	opts := dcs.PlainOptions{}
	if s := strings.TrimSpace(r.opts.Socks5); s != "" {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		dialer, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5: %w", err)
		}
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5: dialer does not support contexts")
		}
		opts.Dial = ctxDialer.DialContext
		r.log.Info("using SOCKS5 proxy")
	}
	return dcs.Plain(opts), nil
}

// parseMTProxy accepts "host:port:hexsecret" or "tcp+secret://hex@host:port".
func parseMTProxy(s string) (addr string, secret []byte, err error) {
	if strings.Contains(s, "://") {
		host := s
		sec := ""
		if i := strings.LastIndex(s, "@"); i >= 0 {
			sec, host = s[i+1:], s[:i]
		}
		host = strings.TrimPrefix(host, "//")
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		if host == "" {
			return "", nil, errors.New("mtproxy: cannot parse address from " + s)
		}
		secret, err = hex.DecodeString(sec)
		if err != nil {
			return "", nil, fmt.Errorf("mtproxy secret: %w", err)
		}
		return host, secret, nil
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", nil, errors.New("mtproxy: expected host:port:hexsecret")
	}
	addr = parts[0] + ":" + parts[1]
	hexPart := ""
	if i := strings.Index(s, parts[1]+":"); i >= 0 {
		hexPart = s[i+len(parts[1])+1:]
	}
	secret, err = hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, fmt.Errorf("mtproxy secret: %w", err)
	}
	return addr, secret, nil
}

func maskPhone(p string) string {
	if len(p) <= 4 {
		return "****"
	}
	return "***" + p[len(p)-4:]
}

// deviceModel folds the device name into the model string.
func deviceModel(name, model string) string {
	name, model = strings.TrimSpace(name), strings.TrimSpace(model)
	switch {
	case name == "" && model == "":
		return ""
	case name == "":
		return model
	case model == "":
		return name
	default:
		return name + " (" + model + ")"
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
