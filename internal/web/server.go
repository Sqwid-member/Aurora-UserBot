// Package web serves Aurora's local control panel.
//
// The whole panel is a single embedded HTML file with no external assets, so
// it works on a phone in Termux with no network, no CDN and no build step.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/plugins"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

// AuthState is the login state the panel renders.
type AuthState struct {
	State    proto.AuthStep `json:"step"`
	Phone    string         `json:"phone,omitempty"`
	HasHint  bool           `json:"hint"`
	Message  string         `json:"message,omitempty"`
	SignedIn bool           `json:"signed_in"`
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
	PluginInstall(ctx context.Context, source, name string) (string, error)
	PluginUninstall(name string) error
	Commands() []plugins.CommandSpec
	Command(ctx context.Context, name, text string) (string, error)

	Send(ctx context.Context, req proto.SendRequest) (proto.SendResult, error)

	Auth() AuthState
	AuthForAccount(accID string) AuthState
	RequestCode(ctx context.Context, phone string) error
	RequestCodeForAccount(ctx context.Context, accID string, phone string) error
	SubmitCode(code string) error
	SubmitCodeForAccount(accID string, code string) error
	SubmitPassword(password string) error
	SubmitPasswordForAccount(accID string, password string) error
	Session() proto.SessionInfo
	SessionForAccount(accID string) proto.SessionInfo
	ImportSession(session string) error
	ImportSessionForAccount(accID string, session string) error
	Logout(ctx context.Context) error
	LogoutForAccount(ctx context.Context, accID string) error

	LogTail(n int) []logx.Record
	SubscribeLogs() (<-chan logx.Record, func())
	SubscribeEvents() (<-chan proto.Event, func())

	Notify(title, text, level string)
	Shutdown(ctx context.Context) error
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
	mux.HandleFunc("POST /api/accounts/{id}/auth/code", s.handleAccountCode)
	mux.HandleFunc("POST /api/accounts/{id}/auth/password", s.handleAccountPassword)
	mux.HandleFunc("POST /api/accounts/{id}/session/import", s.handleAccountSessionImport)
	mux.HandleFunc("POST /api/accounts/{id}/logout", s.handleAccountLogout)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/config", s.handlePutConfig)

	mux.HandleFunc("GET /api/plugins", s.handlePlugins)
	mux.HandleFunc("POST /api/plugins/{name}/{action}", s.handlePluginAction)
	mux.HandleFunc("POST /api/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("POST /api/plugins/{name}/uninstall", s.handlePluginUninstall)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("POST /api/command", s.handleCommand)
	mux.HandleFunc("POST /api/send", s.handleSend)

	mux.HandleFunc("GET /api/auth", s.handleAuthState)
	mux.HandleFunc("POST /api/auth/code-request", s.handleCodeRequest)
	mux.HandleFunc("POST /api/auth/code", s.handleCode)
	mux.HandleFunc("POST /api/auth/password", s.handlePassword)
	mux.HandleFunc("GET /api/session", s.handleSessionInfo)
	mux.HandleFunc("POST /api/session/import", s.handleSessionImport)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/logs/stream", s.handleLogStream)
	mux.HandleFunc("GET /api/events", s.handleEventStream)

	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /api/restart", s.handleShutdown)
	mux.HandleFunc("POST /api/gc", s.handleGC)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.Handle("/", staticHandler())
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
			// A correct ?token= was supplied: move it into an HttpOnly cookie
			// and redirect, so it stops living in history and in the URL bar.
			http.SetCookie(w, &http.Cookie{
				Name:     "aurora_token",
				Value:    s.opts.Token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			if r.URL.Path == "/" {
				// Serve the sign-in shell; it carries no secrets.
				serveGate(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "unauthorized",
					"hint":  "open the panel URL with ?token=<token>",
				})
				return
			}
			http.Redirect(w, r, "/", http.StatusFound)
		}
	})
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
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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

func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeErr(w, http.StatusForbidden, "core is in read-only mode")
		return
	}
	var req struct {
		Source string `json:"source"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.RequestCode(r.Context(), req.Phone); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.SubmitCode(req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.ImportSession(req.Session); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// ---- helpers ----

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

// openBrowser does the right thing in Termux, where there is no xdg-open.
func openBrowser(url string) {
	var candidates [][]string
	if runtime.GOOS == "android" || isTermux() {
		candidates = append(candidates, []string{"termux-open-url", url})
	}
	candidates = append(candidates,
		[]string{"xdg-open", url},
		[]string{"sensible-browser", url},
		[]string{"open", url},
	)
	for _, c := range candidates {
		if path, err := exec.LookPath(c[0]); err == nil {
			_ = exec.Command(path, c[1:]...).Start()
			return
		}
	}
}

func isTermux() bool {
	_, err := exec.LookPath("termux-open-url")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.RequestCodeForAccount(r.Context(), id, req.Phone); err != nil {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.SubmitCodeForAccount(id, req.Code); err != nil {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.opts.Backend.ImportSessionForAccount(id, req.Session); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountLogout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Backend.LogoutForAccount(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
