// Package tgc wraps the MTProto client with everything Aurora needs:
// connection, interactive login, peer resolution and a small typed façade
// over the generated API.
package tgc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
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

// ErrSignupRequired is returned by SubmitCode when Telegram reports the
// number is not registered yet: the caller has to collect a name and call
// SubmitSignup to create the account.
var ErrSignupRequired = errors.New("цей номер ще не зареєстровано в Telegram — введіть ім'я для реєстрації")

// AuthStep describes what the login flow is currently waiting for.
type AuthStep = proto.AuthStep

// Login steps exposed to the CLI and the web panel.
const (
	AuthNone     = proto.AuthNone
	AuthPhone    = proto.AuthPhone
	AuthCode     = proto.AuthCode
	AuthSignup   = proto.AuthSignup
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
	// connected reports that the MTProto client is inside its Run callback,
	// i.e. the connection is up and RPCs can be issued. It is the gate the
	// login flow waits on before talking to Telegram.
	connected bool

	// dispatcher sees raw updates so the QR login flow can listen for
	// UpdateLoginToken alongside the plugin-facing message stream.
	dispatcher *tg.UpdateDispatcher

	// qrURL is the last exported login token, shown to the user so an already
	// authorized Telegram app can approve this session.
	qrURL     string
	qrExpires time.Time
	qrRunning bool

	codeHash string
	// codeInfo is a human-readable description of where Telegram delivered
	// the last login code (SMS, app, call, ...).
	codeInfo string
	// codeNext is the channel Telegram will switch to if the code is not
	// entered within codeWait seconds, and codeNextAt is when that becomes
	// possible (auth.resendCode only works after the timeout).
	codeNext   string
	codeWait   time.Duration
	codeNextAt time.Time
	authDone   chan struct{}

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

// Connected reports whether the MTProto link is up. Login steps must not be
// issued before this is true, otherwise the RPC fails or blocks forever.
func (r *Runtime) Connected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connected
}

func (r *Runtime) setConnected(v bool) {
	r.mu.Lock()
	r.connected = v
	r.mu.Unlock()
}

// waitConnected blocks until the client is inside its Run callback or the
// timeout/context expires. It turns the classic "клієнт ще підключається"
// dead end into a short, bounded wait.
func (r *Runtime) waitConnected(ctx context.Context, timeout time.Duration) error {
	if r.Connected() {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if r.Connected() {
			return nil
		}
		if time.Now().After(deadline) {
			if last := r.LastError(); last != "" {
				return fmt.Errorf("ядро Telegram не підключилося: %s", last)
			}
			return errors.New("ядро Telegram не підключилося до серверів — перевірте мережу та 'aurora logs'")
		}
	}
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

	disp := tg.NewUpdateDispatcher()
	r.mu.Lock()
	r.dispatcher = &disp
	r.mu.Unlock()

	sink := telegram.UpdateHandlerFunc(func(ctx context.Context, u tg.UpdatesClass) error {
		// The QR flow needs the raw updates; plugins need the parsed ones.
		if err := disp.Handle(ctx, u); err != nil {
			return err
		}
		return r.handleUpdates(ctx, u)
	})

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
			// gotd calls Self() right after the connection comes up. Before the
			// first login that always fails with AUTH_KEY_UNREGISTERED — it is
			// the normal state, not a reason to tear the client down. Returning
			// an error here aborts client.Run, which used to kill the runtime
			// seconds after start and made login impossible.
			if r.isAuthError(err) {
				r.log.Debug("self check: not authorized yet", logx.F("error", errString(err)))
			} else if err != nil {
				r.log.Debug("self check failed", logx.F("error", errString(err)))
			}
			return nil
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
	// From here on the client object exists, but RPCs are only safe once the
	// Run callback fires: that is the point gotd reports "session init done".
	r.setConnected(false)
	defer r.setConnected(false)

	return client.Run(ctx, func(ctx context.Context) error {
		r.setConnected(true)
		defer r.setConnected(false)

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
	if auth.IsUnauthorized(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// Telegram writes AUTH_KEY_UNREGISTERED; keep both spellings covered.
	for _, key := range []string{"auth key", "auth_key", "unauthorized", "unauthorised"} {
		if strings.Contains(msg, key) {
			return true
		}
	}
	return false
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
	// Generic E.164: keep an explicit "+", expand "00" prefix, otherwise
	// assume the digits are already a full international number.
	// No country-specific rewrites (the old 0→+38 / 48→+48 branches broke
	// every other country).
	if strings.HasPrefix(s, "+") {
		return s
	}
	if strings.HasPrefix(s, "00") && len(s) > 2 {
		return "+" + s[2:]
	}
	return "+" + s
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
	case strings.Contains(u, "SEND_CODE_UNAVAILABLE"):
		return errors.New("Telegram не може надіслати код іншим способом для цього номера — код приходить лише в застосунок Telegram. Якщо акаунт не відкрито на жодному пристрої, вкажіть власні API ключі (aurora setup)")
	case strings.Contains(u, "PHONE_NUMBER_UNOCCUPLICATED"):
		return errors.New("цей номер не зареєстрований у Telegram — потрібно створити акаунт (введіть ім'я)")
	case strings.Contains(u, "FIRST_NAME_INVALID"), strings.Contains(u, "NAME_INVALID"):
		return errors.New("некоректне ім'я — вкажіть від 1 до 64 символів")
	default:
		return err
	}
}

// RequestCodeSMS asks for the login code over SMS even when Telegram's first
// choice is the in-app delivery. It sends the raw auth.sendCode with
// codeSettings that claim the number is unknown, which is what makes
// Telegram fall back to SMS / a phone call. Needed for numbers whose code
// would otherwise be delivered only into an app session that is not open.
func (r *Runtime) RequestCodeSMS(ctx context.Context, phone string) error {
	phone = CleanPhone(phone)
	if phone == "" {
		return errors.New("номер телефону не може бути порожнім")
	}
	if r.Step() == AuthSignedIn {
		return errors.New("акаунт уже авторизовано")
	}

	r.mu.Lock()
	r.opts.Phone = phone
	client := r.client
	appID, appHash := r.opts.AppID, r.opts.AppHash
	r.mu.Unlock()

	if client == nil {
		return errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return err
	}

	var settings tg.CodeSettings
	settings.SetAllowAppHash(true)
	settings.SetUnknownNumber(true)
	settings.SetAllowMissedCall(true)
	settings.SetAllowFlashcall(true)
	settings.SetAllowFirebase(true)

	r.log.Info("requesting login code via sms", logx.F("phone", maskPhone(phone)))
	sent, err := client.API().AuthSendCode(ctx, &tg.AuthSendCodeRequest{
		PhoneNumber: phone,
		APIID:       appID,
		APIHash:     appHash,
		Settings:    settings,
	})
	if err != nil {
		r.mu.Lock()
		r.step = AuthPhone
		r.codeHash = ""
		r.mu.Unlock()
		return translateTelegramErr(err)
	}
	return r.acceptSentCode(sent)
}

// acceptSentCode stores the result of auth.sendCode and moves the flow on.
func (r *Runtime) acceptSentCode(sent tg.AuthSentCodeClass) error {
	if sent == nil {
		return errors.New("Telegram не повернув відповідь на запит коду")
	}
	if suc, ok := sent.(*tg.AuthSentCodeSuccess); ok {
		if _, ok := suc.Authorization.(*tg.AuthAuthorization); ok {
			r.markSignedIn()
			return nil
		}
	}
	sc, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return fmt.Errorf("неочікувана відповідь від Telegram: %T", sent)
	}

	info := sentCodeChannel(sc.Type, sc.NextType, sc.Timeout)
	wait := time.Duration(sc.Timeout) * time.Second
	if wait <= 0 {
		wait = 60 * time.Second
	}
	next := codeTypeName(sc.NextType)

	r.mu.Lock()
	r.codeHash = sc.PhoneCodeHash
	r.codeInfo = info
	r.codeWait = wait
	r.codeNext = next
	r.codeNextAt = time.Now().Add(wait)
	r.step = AuthCode
	r.mu.Unlock()

	r.log.Info("login code sent",
		logx.F("via", info),
		logx.F("next", next),
		logx.F("wait", wait.String()))
	return nil
}

// CodeAppOnly reports the dead-end Telegram hands third-party clients: the
// code went to the in-app service chat of other sessions and there is no
// next channel to fall back to. Since 18.02.2023 Telegram no longer sends
// SMS codes to third-party clients at all, so a number whose account has no
// active session cannot be verified from here — the session has to be
// imported instead (see ImportSession).
func (r *Runtime) CodeAppOnly() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.step == AuthCode && r.codeNext == "" &&
		strings.Contains(r.codeInfo, "застосунок")
}

// CodeNext reports the channel Telegram will fall back to and when the
// fallback becomes available.
func (r *Runtime) CodeNext() (string, time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.codeNext, r.codeNextAt
}

// codeTypeName renders auth.codeType so the UI can show the next channel.
func codeTypeName(t tg.AuthCodeTypeClass) string {
	if t == nil {
		return ""
	}
	return channelName(t.TypeName())
}

// RequestCode initiates login by sending a verification code to the phone number.
func (r *Runtime) RequestCode(ctx context.Context, phone string) error {
	phone = CleanPhone(phone)
	if phone == "" {
		return errors.New("номер телефону не може бути порожнім")
	}
	if r.Step() == AuthSignedIn {
		return errors.New("акаунт вже авторизовано")
	}

	r.mu.Lock()
	r.opts.Phone = phone
	client := r.client
	r.mu.Unlock()

	if client == nil {
		return errors.New("ядро Telegram ще не запущено — зачекайте кілька секунд")
	}
	// The connection comes up asynchronously; asking for a code before it is
	// up fails with a confusing "not connected" error.
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return err
	}

	aclient := client.Auth()
	r.log.Info("requesting login code", logx.F("phone", maskPhone(phone)))

	// AllowAppHash lets the standard public keys work; AllowFlashCall keeps
	// the phone-call channel open for numbers that cannot receive SMS.
	sent, err := aclient.SendCode(ctx, phone, auth.SendCodeOptions{
		AllowAppHash:   true,
		AllowFlashCall: true,
	})
	if err != nil {
		r.mu.Lock()
		r.step = AuthPhone
		r.codeHash = ""
		r.mu.Unlock()
		return translateTelegramErr(err)
	}

	sc, ok := sent.(*tg.AuthSentCode)
	if !ok {
		// auth.SentCodeSuccess means Telegram already knows this session.
		if suc, ok := sent.(*tg.AuthSentCodeSuccess); ok {
			if _, ok := suc.Authorization.(*tg.AuthAuthorization); ok {
				r.markSignedIn()
				return nil
			}
		}
		return fmt.Errorf("неочікувана відповідь від Telegram: %T", sent)
	}

	info := sentCodeChannel(sc.Type, sc.NextType, sc.Timeout)
	r.mu.Lock()
	r.codeHash = sc.PhoneCodeHash
	r.codeInfo = info
	r.step = AuthCode
	r.mu.Unlock()
	r.log.Info("login code sent", logx.F("via", info))

	return nil
}

// CodeInfo describes where Telegram delivered the last login code.
func (r *Runtime) CodeInfo() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.codeInfo
}

// sentCodeChannel renders where Telegram delivered the login code, so the
// user knows whether to look in the Telegram app, SMS or a phone call.
func sentCodeChannel(t tg.AuthSentCodeTypeClass, next tg.AuthCodeTypeClass, timeout int) string {
	info := channelName(t.TypeName())
	if next != nil {
		info += fmt.Sprintf(" → далі %s (≈%d c)", channelName(next.TypeName()), timeout)
	}
	return info
}

func channelName(typeName string) string {
	short := typeName
	for _, p := range []string{"auth.sentCodeType", "auth.codeType"} {
		short = strings.TrimPrefix(short, p)
	}
	switch short {
	case "App":
		return "застосунок Telegram (службовий чат)"
	case "Sms", "FirebaseSms":
		return "SMS"
	case "FragmentSms":
		return "SMS (Fragment)"
	case "Call":
		return "дзвінок"
	case "FlashCall":
		return "flash-дзвінок"
	case "MissedCall":
		return "пропущений дзвінок"
	case "EmailCode", "SetUpEmailRequired":
		return "email"
	default:
		return typeName
	}
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
		return errors.New("ядро Telegram ще не запущено")
	}
	if codeHash == "" {
		return errors.New("код ще не запитувався — спочатку введіть номер телефону")
	}

	if err := r.waitConnected(context.Background(), 30*time.Second); err != nil {
		return err
	}

	aclient := client.Auth()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, signErr := aclient.SignIn(ctx, phone, code, codeHash)
	switch {
	case signErr == nil:
		r.markSignedIn()
		return nil
	case errors.Is(signErr, auth.ErrPasswordAuthNeeded) || strings.Contains(strings.ToUpper(signErr.Error()), "SESSION_PASSWORD_NEEDED"):
		r.log.Info("two-factor password required")
		r.setStep(AuthPassword)
		return errors.New("SESSION_PASSWORD_NEEDED")
	default:
		var needSignUp *auth.SignUpRequired
		if errors.As(signErr, &needSignUp) {
			// The number is not registered yet: Telegram wants a profile
			// before it will create the account.
			r.log.Info("phone not registered, sign-up required", logx.F("phone", maskPhone(phone)))
			r.setStep(AuthSignup)
			return ErrSignupRequired
		}
		return translateTelegramErr(signErr)
	}
}

// QRToken returns the login-token link currently waiting for approval.
func (r *Runtime) QRToken() (string, time.Time, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.qrURL == "" || time.Now().After(r.qrExpires) {
		return "", time.Time{}, false
	}
	return r.qrURL, r.qrExpires, r.qrRunning
}

// StartQR exports a login token and waits until it is approved in an already
// authorized Telegram app. It is the only way in for numbers that cannot
// receive SMS, and it never touches the phone number at all.
func (r *Runtime) StartQR(ctx context.Context) error {
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()

	if client == nil {
		return errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return err
	}

	r.mu.RLock()
	disp := r.dispatcher
	r.mu.RUnlock()
	if disp == nil {
		return errors.New("qr-вхід недоступний: оновлення ядра ще не запущені")
	}

	r.mu.Lock()
	if r.qrRunning {
		r.mu.Unlock()
		return nil
	}
	r.qrRunning = true
	r.qrURL = ""
	r.step = AuthCode
	r.mu.Unlock()

	loggedIn := qrlogin.OnLoginToken(*disp)

	r.log.Info("qr login started")
	_, err := client.QR().Auth(ctx, loggedIn, func(_ context.Context, token qrlogin.Token) error {
		r.mu.Lock()
		r.qrURL = token.URL()
		r.qrExpires = token.Expires()
		r.mu.Unlock()
		r.log.Info("qr login token exported", logx.F("expires", r.qrExpires.Format(time.RFC3339)))
		return nil
	})

	r.mu.Lock()
	r.qrRunning = false
	r.qrURL = ""
	r.mu.Unlock()

	if err != nil {
		if ctx.Err() != nil {
			return errors.New("qr-вхід скасовано або вичерпав час")
		}
		return translateTelegramErr(err)
	}
	r.markSignedIn()
	return nil
}

// ResendCode asks Telegram to deliver the login code again, usually through
// the next available channel (SMS or a phone call). Users whose code stays
// in the app need this to break out of that loop.
func (r *Runtime) ResendCode(ctx context.Context) error {
	r.mu.RLock()
	client := r.client
	phone := r.opts.Phone
	codeHash := r.codeHash
	r.mu.RUnlock()

	if client == nil {
		return errors.New("ядро Telegram ще не запущено")
	}
	if codeHash == "" {
		return errors.New("код ще не запитувався — спочатку введіть номер телефону")
	}
	if err := r.waitConnected(ctx, 30*time.Second); err != nil {
		return err
	}

	r.mu.RLock()
	nextAt := r.codeNextAt
	nextCh := r.codeNext
	r.mu.RUnlock()
	if !nextAt.IsZero() && time.Now().Before(nextAt) {
		return fmt.Errorf("Telegram ще не готовий наступний канал (%s) — зачекайте до %s",
			nextCh, nextAt.Format("15:04:05"))
	}

	sent, err := client.Auth().ResendCode(ctx, phone, codeHash)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordAuthNeeded) {
			r.setStep(AuthPassword)
			return errors.New("SESSION_PASSWORD_NEEDED")
		}
		return translateTelegramErr(err)
	}
	if _, ok := sent.(*tg.AuthSentCodeSuccess); ok {
		// Telegram accepted the session without a code this time.
		r.markSignedIn()
		return nil
	}
	return r.acceptSentCode(sent)
}

// SubmitSignup registers a phone number that Telegram does not know yet.
// The code entered at the previous step has already been accepted; this
// creates the account with the given profile.
func (r *Runtime) SubmitSignup(firstName, lastName string) error {
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	if firstName == "" {
		return errors.New("ім'я не може бути порожнім")
	}

	r.mu.RLock()
	client := r.client
	phone := r.opts.Phone
	codeHash := r.codeHash
	r.mu.RUnlock()

	if client == nil {
		return errors.New("ядро Telegram ще не запущено")
	}
	if codeHash == "" {
		return errors.New("код ще не підтверджено — спочатку введіть код із Telegram")
	}
	if err := r.waitConnected(context.Background(), 30*time.Second); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r.log.Info("registering new account", logx.F("name", firstName))
	_, err := client.Auth().SignUp(ctx, auth.SignUp{
		PhoneNumber:   phone,
		PhoneCodeHash: codeHash,
		FirstName:     firstName,
		LastName:      lastName,
	})
	if err != nil {
		return translateTelegramErr(err)
	}
	r.markSignedIn()
	return nil
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
		return errors.New("ядро Telegram ще не запущено")
	}
	if err := r.waitConnected(context.Background(), 30*time.Second); err != nil {
		return err
	}

	aclient := client.Auth()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := aclient.Password(ctx, pass); err != nil {
		return translateTelegramErr(err)
	}

	r.markSignedIn()
	return nil
}

// markSignedIn records a successful login and wakes the Run loop so it can
// move on to serving the session.
func (r *Runtime) markSignedIn() {
	r.mu.Lock()
	r.step = AuthSignedIn
	r.mu.Unlock()
	select {
	case r.authDone <- struct{}{}:
	default:
	}
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

// ImportWebSession replaces the stored session from a Telegram Web
// localStorage export (see ParseWebExport). overrideDC is 0 for auto.
func (r *Runtime) ImportWebSession(payload string, overrideDC int) (int, error) {
	return ImportWebExport(r.opts.SessionPath, payload, overrideDC)
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
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		return nil, fmt.Errorf("tgc: limit %d exceeds Telegram maximum of 100", limit)
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
	if pm == nil {
		return nil, errors.New("tgc: peer manager is not ready yet — зачекайте кілька секунд")
	}

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
		// Telegram channel IDs are often written as -1001234567890.
		// Strip the -100 prefix so cache lookup sees the bare ID.
		bare := id
		if bare < 0 && bare <= -1000000000000 {
			if v := -(bare + 1000000000000); v > 0 {
				bare = v
			}
		}
		// Try the most specific interpretation first: channels, chats, users.
		if id < 0 {
			if p, err := pm.ResolveChannelID(ctx, -id); err == nil {
				return p, nil
			}
			if bare != id {
				if p, err := pm.ResolveChannelID(ctx, bare); err == nil {
					return p, nil
				}
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

// parseMTProxy accepts "host:port:hexsecret" or "scheme://hex@host:port".
func parseMTProxy(s string) (addr string, secret []byte, err error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		// Split scheme off first: "tcp+secret://hex@host:port".
		rest := s[strings.Index(s, "://")+3:]
		sec := ""
		host := rest
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			sec, host = rest[:i], rest[i+1:]
		}
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		host = strings.TrimSpace(host)
		sec = strings.TrimSpace(sec)
		if host == "" {
			return "", nil, errors.New("mtproxy: cannot parse address from " + s)
		}
		if _, _, err := net.SplitHostPort(host); err != nil {
			return "", nil, fmt.Errorf("mtproxy address: %w", err)
		}
		secret, err = hex.DecodeString(sec)
		if err != nil {
			return "", nil, fmt.Errorf("mtproxy secret: %w", err)
		}
		return host, secret, nil
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", nil, errors.New("mtproxy: expected host:port:hexsecret")
	}
	addr = parts[0] + ":" + parts[1]
	secret, err = hex.DecodeString(strings.TrimSpace(parts[2]))
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
