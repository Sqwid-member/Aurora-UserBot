// Package web serves Aurora's local control panel.
//
// The whole panel is a single embedded HTML file with no external assets, so
// it works on a phone in Termux with no network, no CDN and no build step.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/plugins"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
)

// AuthState is the login state the panel renders.
type AuthState struct {
	State    proto.AuthStep `json:"step"`
	Phone    string         `json:"phone,omitempty"`
	HasHint  bool           `json:"hint"`
	Message  string         `json:"message,omitempty"`
	SignedIn bool           `json:"signed_in"`
	// Connected reports that the MTProto link is up: the CLI and the panel
	// wait for it before offering the first login step.
	Connected bool `json:"connected"`
	// AppOnly is set when Telegram delivered the code to other app sessions
	// and offers no SMS/call fallback: a third-party client cannot receive
	// it, so the session has to be imported from an official client.
	AppOnly bool               `json:"app_only"`
	Session proto.SessionState `json:"session,omitempty"`
}

// QRState describes the login token currently waiting for approval in an
// already authorized Telegram app.
type QRState struct {
	URL     string    `json:"url,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	Running bool      `json:"running"`
}

// Backend is everything the control panel needs from the core.
type Backend interface {
	Status() proto.Status
	Config() config.Config
	SaveConfig(config.Config) error

	// Multi-account management
	Accounts() []proto.AccountInfo
	ActiveAccountID() string
	SetActiveAccount(id string) error
	AddAccount(title, phone string, appID int, appHash string) (proto.AccountInfo, error)
	RemoveAccount(id string) error
	ToggleAccountPlugin(accID, pluginName string) (bool, error)

	// PluginStats returns a snapshot of every installed plugin.
	PluginStats() []plugins.Stats
	PluginAction(ctx context.Context, name, action string) (string, error)
	// PluginSettings returns the settings form schema with values.
	PluginSettings(name string) ([]plugins.SettingValue, error)
	SavePluginSettings(ctx context.Context, name string, values map[string]any) ([]plugins.SettingValue, error)
	ResetPluginSettings(name string) error
	PluginInstall(ctx context.Context, source, name string) (string, error)
	PluginUninstall(name string) error
	Commands() []plugins.CommandSpec
	Command(ctx context.Context, name, text string) (string, error)

	Send(ctx context.Context, req proto.SendRequest) (proto.SendResult, error)

	Auth() AuthState
	AuthForAccount(accID string) AuthState
	RequestCode(ctx context.Context, phone string) error
	RequestCodeForAccount(ctx context.Context, accID string, phone string) error
	RequestCodeSMS(ctx context.Context, phone string) error
	RequestCodeSMSForAccount(ctx context.Context, accID string, phone string) error
	SubmitCode(code string) error
	SubmitCodeForAccount(accID string, code string) error
	SignUp(firstName, lastName string) error
	SignUpForAccount(accID string, firstName, lastName string) error
	ResendCode(ctx context.Context) error
	ResendCodeForAccount(ctx context.Context, accID string) error
	StartQRLogin(ctx context.Context) error
	StartQRLoginForAccount(ctx context.Context, accID string) error
	QRState() QRState
	QRStateForAccount(accID string) QRState
	SubmitPassword(password string) error
	SubmitPasswordForAccount(accID string, password string) error
	Session() proto.SessionInfo
	SessionForAccount(accID string) proto.SessionInfo
	ImportSession(session string) error
	ImportSessionForAccount(accID string, session string) error
	ImportWebSession(dc int, payload string) (int, error)
	ImportWebSessionForAccount(accID string, dc int, payload string) (int, error)
	QRImage() ([]byte, error)
	QRImageForAccount(accID string) ([]byte, error)
	Profile(ctx context.Context) (tgc.FullProfile, error)
	Sessions(ctx context.Context) ([]tgc.AuthSession, error)
	TerminateSession(ctx context.Context, hash int64) (bool, error)
	UpdateProfile(ctx context.Context, first, last, about string) (proto.User, error)
	UpdateUsername(ctx context.Context, username string) (proto.User, error)
	UploadAvatar(ctx context.Context, name string, data []byte) (proto.User, error)
	Logout(ctx context.Context) error
	LogoutForAccount(ctx context.Context, accID string) error

	LogTail(n int) []logx.Record
	SubscribeLogs() (<-chan logx.Record, func())
	SubscribeEvents() (<-chan proto.Event, func())

	Notify(title, text, level string)
	Shutdown(ctx context.Context) error
	Restart(ctx context.Context) error
}

// Options configures the panel server.
type Options struct {
	Host  string
	Port  int
	Token string
	// ReadOnly refuses mutating endpoints.
	ReadOnly bool
	Logger   *logx.Logger
	Backend  Backend
	// OpenBrowser attempts to launch a browser on start (Termux-friendly).
	OpenBrowser bool
}

// Server is the control panel HTTP server.
type Server struct {
	opts Options
	log  *logx.Logger
	srv  *http.Server
	ln   net.Listener
	url  string
}

// New builds a panel server.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = logx.New(logx.Options{}, "web")
	}
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	return &Server{opts: opts, log: opts.Logger.Scoped("web")}
}

// URL returns the panel URL once the server is listening.
func (s *Server) URL() string { return s.url }

// Addr returns the listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Start binds the listener and begins serving.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.opts.Host, s.opts.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: listen on %s: %w", addr, err)
	}
	s.ln = ln

	display := addr
	if s.opts.Host == "0.0.0.0" || s.opts.Host == "::" {
		display = "127.0.0.1:" + itoa(s.opts.Port)
	}
	s.url = "http://" + display

	if s.opts.Host == "0.0.0.0" || s.opts.Host == "::" {
		s.log.Warn("panel listens on all interfaces — LAN devices will reach the login page; keep a strong web.token")
	}

	mux := http.NewServeMux()
	s.routes(mux)
	s.srv = &http.Server{
		Handler:           s.middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // SSE streams must not be cut off
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("panel stopped", logx.F("error", err))
		}
	}()

	s.log.Info("control panel ready",
		logx.F("url", s.url),
	)
	if s.opts.OpenBrowser {
		go openBrowser(s.url + "/?token=" + s.opts.Token)
	}
	return nil
}

// Stop shuts the panel down.
func (s *Server) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/token", s.handleToken)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)

	mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	mux.HandleFunc("POST /api/accounts", s.handleCreateAccount)
	mux.HandleFunc("POST /api/accounts/{id}/activate", s.handleActivateAccount)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.handleDeleteAccount)
	mux.HandleFunc("POST /api/accounts/{id}/plugins/{name}/toggle", s.handleToggleAccountPlugin)
	mux.HandleFunc("POST /api/accounts/{id}/auth/code-request", s.handleAccountCodeRequest)
	mux.HandleFunc("POST /api/accounts/{id}/auth/code-request/sms", s.handleAccountCodeRequestSMS)
	mux.HandleFunc("POST /api/accounts/{id}/auth/code", s.handleAccountCode)
	mux.HandleFunc("POST /api/accounts/{id}/auth/signup", s.handleAccountSignUp)
	mux.HandleFunc("POST /api/accounts/{id}/auth/resend", s.handleAccountResendCode)
	mux.HandleFunc("POST /api/accounts/{id}/auth/qr", s.handleAccountStartQR)
	mux.HandleFunc("GET /api/accounts/{id}/auth/qr", s.handleAccountQRState)
	mux.HandleFunc("POST /api/accounts/{id}/auth/password", s.handleAccountPassword)
	mux.HandleFunc("POST /api/accounts/{id}/session/import", s.handleAccountSessionImport)
	mux.HandleFunc("POST /api/accounts/{id}/session/import-web", s.handleAccountSessionImportWeb)
	mux.HandleFunc("POST /api/accounts/{id}/logout", s.handleAccountLogout)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/config", s.handlePutConfig)

	mux.HandleFunc("GET /api/plugins", s.handlePlugins)
	mux.HandleFunc("POST /api/plugins/{name}/{action}", s.handlePluginAction)
	mux.HandleFunc("GET /api/plugins/{name}/settings", s.handlePluginSettings)
	mux.HandleFunc("POST /api/plugins/{name}/settings", s.handlePluginSettingsSave)
	mux.HandleFunc("DELETE /api/plugins/{name}/settings", s.handlePluginSettingsReset)
	mux.HandleFunc("POST /api/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("POST /api/plugins/{name}/uninstall", s.handlePluginUninstall)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("POST /api/command", s.handleCommand)
	mux.HandleFunc("POST /api/send", s.handleSend)

	mux.HandleFunc("GET /api/auth", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/code-request", s.handleCodeRequest)
	mux.HandleFunc("POST /api/auth/code-request/sms", s.handleCodeRequestSMS)
	mux.HandleFunc("POST /api/auth/code", s.handleCode)
	mux.HandleFunc("POST /api/auth/signup", s.handleSignUp)
	mux.HandleFunc("POST /api/auth/resend", s.handleResendCode)
	mux.HandleFunc("POST /api/auth/qr", s.handleStartQR)
	mux.HandleFunc("GET /api/auth/qr", s.handleQRState)
	mux.HandleFunc("GET /api/auth/qr/image", s.handleQRImage)
	mux.HandleFunc("GET /api/accounts/{id}/auth/qr/image", s.handleAccountQRImage)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("POST /api/sessions/{hash}/terminate", s.handleTerminateSession)
	mux.HandleFunc("GET /api/profile", s.handleProfile)
	mux.HandleFunc("POST /api/profile", s.handleUpdateProfile)
	mux.HandleFunc("POST /api/profile/username", s.handleUpdateUsername)
	mux.HandleFunc("POST /api/profile/avatar", s.handleUploadAvatar)
	mux.HandleFunc("POST /api/auth/password", s.handlePassword)
	mux.HandleFunc("GET /api/session", s.handleSessionInfo)
	mux.HandleFunc("POST /api/session/import", s.handleSessionImport)
	mux.HandleFunc("POST /api/session/import-web", s.handleSessionImportWeb)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/logs/stream", s.handleLogStream)
	mux.HandleFunc("GET /api/events", s.handleEventStream)

	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /api/restart", s.handleRestart)
	mux.HandleFunc("POST /api/gc", s.handleGC)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Token is injected into the shell only for authenticated callers or
	// loopback convenience. Remote unauthenticated visitors get the shell
	// without the token so LAN exposure of host=0.0.0.0 does not leak it.
	withToken := staticHandler(s.opts.Token)
	withoutToken := staticHandler("")
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authState(r) != authMissing || s.isLocalRequest(r) {
			withToken.ServeHTTP(w, r)
			return
		}
		withoutToken.ServeHTTP(w, r)
	}))
}

func (s *Server) isAllowedHost(hostPort string) bool {
	if s.opts.Host == "0.0.0.0" || s.opts.Host == "::" {
		return true
	}
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "" {
		return true
	}
	if s.opts.Host != "" && (host == s.opts.Host || strings.EqualFold(host, s.opts.Host)) {
		return true
	}
	return false
}

func (s *Server) isCrossSite(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		if sfs == "cross-site" {
			return true
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return true
		}
		if !s.isAllowedHost(u.Host) {
			return true
		}
		return false
	}
	if ref := r.Referer(); ref != "" {
		u, err := url.Parse(ref)
		if err != nil {
			return true
		}
		if !s.isAllowedHost(u.Host) {
			return true
		}
	}
	return false
}

// middleware enforces security headers, Host validation (DNS rebinding protection),
// CSRF protection for mutating verbs and token authentication.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")

		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		if !s.isAllowedHost(r.Host) {
			http.Error(w, "invalid host header", http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if s.isCrossSite(r) {
				http.Error(w, "cross-site request forbidden", http.StatusForbidden)
				return
			}
		}

		switch s.authState(r) {
		case authOK:
			next.ServeHTTP(w, r)
		case authNeedCookie:
			if s.opts.Token != "" {
				http.SetCookie(w, &http.Cookie{
					Name:     "aurora_token",
					Value:    s.opts.Token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   31536000,
				})
			}
			next.ServeHTTP(w, r)
		default:
			// Local loopback convenience only: a process on the same phone
			// (Termux, adb, first-run wizard) gets in without a token.
			// Everything else — including LAN peers when host=0.0.0.0 —
			// must present a valid bearer/cookie/query token.
			if s.isLocalRequest(r) {
				if s.opts.Token != "" {
					http.SetCookie(w, &http.Cookie{
						Name:     "aurora_token",
						Value:    s.opts.Token,
						Path:     "/",
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
						MaxAge:   31536000,
					})
				}
				next.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "unauthorized",
					"hint":  "open the panel URL with ?token=<token>",
				})
				return
			}
			// Static shell for remote visitors: served WITHOUT the token
			// (see routes: token is only injected for authenticated/loopback).
			// The SPA will show its own login prompt.
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) isLocalRequest(r *http.Request) bool {
	h := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		h = host
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	if h == "127.0.0.1" || h == "::1" || h == "localhost" {
		return true
	}
	// Strictly loopback only. Private/LAN ranges (192.168.x, 10.x, …)
	// must NOT bypass auth — otherwise host=0.0.0.0 exposes the panel
	// with its token to the whole local network.
	ip := net.ParseIP(h)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

type authResult int

const (
	authOK authResult = iota
	authNeedCookie
	authMissing
)

// authState classifies the request credentials. The token may arrive as a
// bearer header, a query parameter (for the very first visit) or a cookie
// (for everything after that).
func (s *Server) authState(r *http.Request) authResult {
	want := s.opts.Token
	if want == "" {
		return authOK
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(want)) == 1 {
			return authOK
		}
	}
	if c, err := r.Cookie("aurora_token"); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(want)) == 1 {
			return authOK
		}
	}
	if v := r.URL.Query().Get("token"); v != "" {
		if subtle.ConstantTimeCompare([]byte(v), []byte(want)) == 1 {
			return authNeedCookie
		}
	}
	return authMissing
}

// ---- handlers ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.Status())
}

// handleToken hands the panel's own token to an already-authenticated caller.
// This is how the SPA can offer "copy the token for curl" without ever having
// it embedded in the page source for anonymous visitors.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"token": s.opts.Token})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.opts.Backend.Config()
	// Never echo the API hash or the panel token in plaintext over the wire.
	redact(&cfg)
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var incoming config.Config
	if !decodeJSON(w, r, &incoming) {
		return
	}
	cur := s.opts.Backend.Config()
	// Keep secrets the panel never received.
	if incoming.Telegram.AppHash == masked {
		incoming.Telegram.AppHash = cur.Telegram.AppHash
	}
	if incoming.Web.Token == masked {
		incoming.Web.Token = cur.Web.Token
	}
	if err := s.opts.Backend.SaveConfig(incoming); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Backend.Notify("Aurora", "Конфігурацію збережено. Перезапустіть ядро, щоб зміни набули чинності.", "info")
	cfg := s.opts.Backend.Config()
	redact(&cfg)
	writeJSON(w, http.StatusOK, cfg)
}

const masked = "••••••"

func redact(c *config.Config) {
	if c.Telegram.AppHash != "" {
		c.Telegram.AppHash = masked
	}
	if c.Web.Token != "" {
		c.Web.Token = masked
	}
	for i := range c.Accounts {
		if c.Accounts[i].AppHash != "" {
			c.Accounts[i].AppHash = masked
		}
	}
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins": s.opts.Backend.PluginStats(),
		"events":  proto.AllEvents,
	})
}

func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	name := r.PathValue("name")
	action := r.PathValue("action")
	msg, err := s.opts.Backend.PluginAction(r.Context(), name, action)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// handlePluginSettings returns the settings form schema with values.
func (s *Server) handlePluginSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	values, err := s.opts.Backend.PluginSettings(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if values == nil {
		values = []plugins.SettingValue{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": values})
}

// handlePluginSettingsSave validates and stores settings form values.
func (s *Server) handlePluginSettingsSave(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	name := r.PathValue("name")
	var req struct {
		Values map[string]any `json:"values"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Values == nil {
		writeErr(w, http.StatusBadRequest, "values are required")
		return
	}
	values, err := s.opts.Backend.SavePluginSettings(r.Context(), name, req.Values)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fields": values})
}

// handlePluginSettingsReset drops stored values back to schema defaults.
func (s *Server) handlePluginSettingsReset(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	if err := s.opts.Backend.ResetPluginSettings(r.PathValue("name")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		Source string `json:"source"`
		Name   string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	msg, err := s.opts.Backend.PluginInstall(r.Context(), req.Source, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

func (s *Server) handlePluginUninstall(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	name := r.PathValue("name")
	if err := s.opts.Backend.PluginUninstall(name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.Commands())
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	out, err := s.opts.Backend.Command(ctx, req.Name, req.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": out})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req proto.SendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.opts.Backend.Send(ctx, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.Auth())
}

func (s *Server) handleCodeRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.opts.Backend.RequestCode(ctx, req.Phone); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.SubmitCode(req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCodeRequestSMS asks Telegram to deliver the code over SMS, which is
// what numbers without an active app session need.
func (s *Server) handleCodeRequestSMS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.RequestCodeSMS(r.Context(), req.Phone); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountCodeRequestSMS is the per-account variant.
func (s *Server) handleAccountCodeRequestSMS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.RequestCodeSMSForAccount(r.Context(), r.PathValue("id"), req.Phone); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleResendCode asks Telegram to deliver the login code again, usually
// through the next available channel.
func (s *Server) handleResendCode(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	if err := s.opts.Backend.ResendCode(r.Context()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleStartQR exports a login token and waits for approval in an already
// authorized Telegram app. The token itself is polled from GET /api/auth/qr.
func (s *Server) handleStartQR(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	// The flow lives far longer than this request, so it must not be tied to
	// the HTTP context: a browser or CLI closing the call cannot cancel it.
	go s.runDetached(s.opts.Backend.StartQRLogin)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// runDetached runs a long-lived core action on its own context so the
// originating request can be closed without tearing the action down.
func (s *Server) runDetached(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	go func() {
		defer cancel()
		if err := fn(ctx); err != nil {
			s.log.Warn("background action failed", logx.F("err", err.Error()))
		}
	}()
}

// handleQRState reports the token waiting for approval.
func (s *Server) handleQRState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.QRState())
}

// handleQRImage serves the pending login token as a PNG QR code, so a
// second screen can scan it while the phone taps the link itself.
func (s *Server) handleQRImage(w http.ResponseWriter, r *http.Request) {
	s.serveQRImage(w, r, "")
}

// handleAccountQRImage is the per-account QR PNG.
func (s *Server) handleAccountQRImage(w http.ResponseWriter, r *http.Request) {
	s.serveQRImage(w, r, r.PathValue("id"))
}

func (s *Server) serveQRImage(w http.ResponseWriter, r *http.Request, accID string) {
	var (
		img []byte
		err error
	)
	if accID == "" {
		img, err = s.opts.Backend.QRImage()
	} else {
		img, err = s.opts.Backend.QRImageForAccount(accID)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

// handleSessions lists active Telegram logins (Settings → Devices).
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	sessions, err := s.opts.Backend.Sessions(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if sessions == nil {
		sessions = []tgc.AuthSession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleTerminateSession ends one login by hash.
func (s *Server) handleTerminateSession(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	hash, err := atoi64(r.PathValue("hash"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad session hash")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	ok, err := s.opts.Backend.TerminateSession(ctx, hash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}

// handleProfile returns the full self profile for the panel form.
func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	prof, err := s.opts.Backend.Profile(ctx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, prof)
}

// handleUpdateProfile changes first name, last name and bio.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		About     string `json:"about"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	user, err := s.opts.Backend.UpdateProfile(ctx, req.FirstName, req.LastName, req.About)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// handleUpdateUsername changes the @username.
func (s *Server) handleUpdateUsername(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	user, err := s.opts.Backend.UpdateUsername(ctx, req.Username)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// maxAvatarBody caps avatar uploads (base64 inflates ~4/3 over the 5 MiB file).
const maxAvatarBody = 8 << 20

// handleUploadAvatar accepts a JPEG/PNG photo as a data URL and sets it.
func (s *Server) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBody)
	var req struct {
		Image string `json:"image"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data, name, err := decodeDataURL(req.Image, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	user, err := s.opts.Backend.UploadAvatar(ctx, name, data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// decodeDataURL splits "data:image/jpeg;base64,..." into bytes and a name.
func decodeDataURL(s, name string) ([]byte, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", errors.New("порожнє зображення")
	}
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i >= 0 {
		s = s[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if data, err = base64.URLEncoding.DecodeString(s); err != nil {
			return nil, "", errors.New("зображення не base64")
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "avatar.jpg"
	}
	return data, name, nil
}

// handleSignUp creates a Telegram account for a number that is not
// registered yet, using the code that was already confirmed.
func (s *Server) handleSignUp(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FirstName) == "" {
		writeErr(w, http.StatusBadRequest, "ім'я не може бути порожнім")
		return
	}
	if err := s.opts.Backend.SignUp(req.FirstName, req.LastName); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.SubmitPassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.Session())
}

func (s *Server) handleSessionImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.ImportSession(req.Session); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSessionImportWeb stores a Telegram Web localStorage export
// (see the panel's session tab for how to produce it) as the session.
func (s *Server) handleSessionImportWeb(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		DC   int    `json:"dc"`
		Data string `json:"data"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	dc, err := s.opts.Backend.ImportWebSession(req.DC, req.Data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dc": dc})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	if err := s.opts.Backend.Logout(r.Context()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := atoi(v); err == nil && parsed > 0 && parsed <= 5000 {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, s.opts.Backend.LogTail(n))
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	records, cancel := s.opts.Backend.SubscribeLogs()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case rec, ok := <-records:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", rec.JSON())
			flusher.Flush()
		case <-ping.C:
			// Keep-alive so Termux's doze mode does not silently drop the tab.
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	events, cancel := s.opts.Backend.SubscribeEvents()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	runtime.GC()
	writeJSON(w, http.StatusOK, s.opts.Backend.Status())
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.opts.Backend.Shutdown(ctx)
	}()
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.opts.Backend.Restart(ctx)
	}()
}

// ---- helpers ----

const maxJSONBody = 1 << 20 // 1 MiB: enough for config/send, stops RAM exhaustion.

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func atoi(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func atoi64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// openBrowser does the right thing in Termux, where there is no xdg-open.
func openBrowser(url string) {
	var candidates [][]string
	if runtime.GOOS == "android" || isTermux() {
		candidates = append(candidates,
			[]string{"termux-open-url", url},
			[]string{"termux-open", url},
		)
		if _, err := os.Stat("/system/bin/am"); err == nil {
			candidates = append(candidates, []string{"/system/bin/am", "start", "-a", "android.intent.action.VIEW", "-d", url})
		}
	}
	candidates = append(candidates,
		[]string{"xdg-open", url},
		[]string{"sensible-browser", url},
		[]string{"x-www-browser", url},
		[]string{"open", url},
	)
	for _, c := range candidates {
		if path, err := sysx.LookPath(c[0]); err == nil {
			_ = sysx.Command(path, c[1:]...).Start()
			return
		} else if strings.HasPrefix(c[0], "/") {
			if _, err := os.Stat(c[0]); err == nil {
				_ = sysx.Command(c[0], c[1:]...).Start()
				return
			}
		}
	}
}

func isTermux() bool {
	_, err := sysx.LookPath("termux-open-url")
	return err == nil
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.Accounts())
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		Title   string `json:"title"`
		Phone   string `json:"phone"`
		AppID   int    `json:"app_id"`
		AppHash string `json:"app_hash"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	acc, err := s.opts.Backend.AddAccount(req.Title, req.Phone, req.AppID, req.AppHash)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleActivateAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	id := r.PathValue("id")
	if err := s.opts.Backend.SetActiveAccount(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active_account": id})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	id := r.PathValue("id")
	if err := s.opts.Backend.RemoveAccount(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleToggleAccountPlugin(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	enabled, err := s.opts.Backend.ToggleAccountPlugin(id, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "account_id": id, "plugin": name})
}

func (s *Server) handleAccountCodeRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Phone string `json:"phone"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.opts.Backend.RequestCodeForAccount(ctx, id, req.Phone); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountCode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.SubmitCodeForAccount(id, req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountStartQR starts the QR flow for one account.
func (s *Server) handleAccountStartQR(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	id := r.PathValue("id")
	go s.runDetached(func(ctx context.Context) error {
		return s.opts.Backend.StartQRLoginForAccount(ctx, id)
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountQRState reports the login token waiting for approval.
func (s *Server) handleAccountQRState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Backend.QRStateForAccount(r.PathValue("id")))
}

// handleAccountResendCode resends the code for one account.
func (s *Server) handleAccountResendCode(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	if err := s.opts.Backend.ResendCodeForAccount(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountSignUp registers a not-yet-known number for one account.
func (s *Server) handleAccountSignUp(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	id := r.PathValue("id")
	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FirstName) == "" {
		writeErr(w, http.StatusBadRequest, "ім'я не може бути порожнім")
		return
	}
	if err := s.opts.Backend.SignUpForAccount(id, req.FirstName, req.LastName); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.SubmitPasswordForAccount(id, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountSessionImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Session string `json:"session"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.opts.Backend.ImportSessionForAccount(id, req.Session); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountSessionImportWeb(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		DC   int    `json:"dc"`
		Data string `json:"data"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	dc, err := s.opts.Backend.ImportWebSessionForAccount(r.PathValue("id"), req.DC, req.Data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dc": dc})
}

func (s *Server) handleAccountLogout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Backend.LogoutForAccount(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
