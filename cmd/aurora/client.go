package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/tgc"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

type statusResponse struct {
	Core          string  `json:"core"`
	Version       string  `json:"version"`
	MemoryMB      float64 `json:"memory_mb"`
	MemLimitMB    int     `json:"mem_limit_mb"`
	Session       string  `json:"session"`
	PluginCount   int     `json:"plugin_count"`
	PluginsUp     int     `json:"plugins_running"`
	ActiveAccount string  `json:"active_account"`
}

type pluginItem struct {
	Name string `json:"name"`
	Desc string `json:"description"`
	// State is the runtime state reported by the plugin host
	// (running, starting, stopping, stopped, failed, created).
	State string `json:"state"`
	// Running is kept for older payloads that report a boolean directly.
	Running bool `json:"running"`
}

// isRunning reports whether the plugin instance is currently up.
func (p pluginItem) isRunning() bool {
	if p.Running {
		return true
	}
	return strings.EqualFold(p.State, "running")
}

type daemonClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func newDaemonClient(layout paths.Layout) (*daemonClient, error) {
	cfgStore, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return nil, err
	}
	c := cfgStore.Get()
	host := c.Web.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := c.Web.Port
	if port == 0 {
		port = 8420
	}
	return &daemonClient{
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		token:   c.Web.Token,
		client:  &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (c *daemonClient) request(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respData, &errResp) == nil && errResp.Error != "" {
			return errors.New(errResp.Error)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respData))
	}

	if out != nil {
		return json.Unmarshal(respData, out)
	}
	return nil
}

func (c *daemonClient) isAlive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (c *daemonClient) getStatus() (*statusResponse, error) {
	var st statusResponse
	err := c.request("GET", "/api/status", nil, &st)
	return &st, err
}

func (c *daemonClient) getAuth() (*web.AuthState, error) {
	var st web.AuthState
	err := c.request("GET", "/api/auth", nil, &st)
	return &st, err
}

func (c *daemonClient) requestCode(phone string) error {
	return c.request("POST", "/api/auth/code-request", map[string]string{"phone": phone}, nil)
}

func (c *daemonClient) submitCode(code string) error {
	return c.request("POST", "/api/auth/code", map[string]string{"code": code}, nil)
}

// requestCodeSMS asks Telegram to deliver the code over SMS.
func (c *daemonClient) requestCodeSMS(phone string) error {
	return c.request("POST", "/api/auth/code-request/sms", map[string]string{"phone": phone}, nil)
}

// resendCode asks Telegram to deliver the login code again, usually via SMS.
func (c *daemonClient) resendCode() error {
	return c.request("POST", "/api/auth/resend", nil, nil)
}

// startQR asks the core to export a login token.
func (c *daemonClient) startQR() error {
	return c.request("POST", "/api/auth/qr", nil, nil)
}

// qrState reports the token waiting for approval.
func (c *daemonClient) qrState() (web.QRState, error) {
	var st web.QRState
	err := c.request("GET", "/api/auth/qr", nil, &st)
	return st, err
}

// signUp registers a phone number that Telegram does not know yet.
func (c *daemonClient) signUp(firstName, lastName string) error {
	return c.request("POST", "/api/auth/signup", map[string]string{
		"first_name": firstName,
		"last_name":  lastName,
	}, nil)
}

func (c *daemonClient) submitPassword(password string) error {
	return c.request("POST", "/api/auth/password", map[string]string{"password": password}, nil)
}

func (c *daemonClient) triggerGC() error {
	return c.request("POST", "/api/gc", nil, nil)
}

func (c *daemonClient) logout() error {
	return c.request("POST", "/api/logout", nil, nil)
}

// getPlugins reads the plugin list. The panel returns an object wrapper
// ({"plugins": [...], "events": [...]}) while older builds returned a bare
// array, so both shapes are accepted.
func (c *daemonClient) getPlugins() ([]pluginItem, error) {
	var raw json.RawMessage
	if err := c.request("GET", "/api/plugins", nil, &raw); err != nil {
		return nil, err
	}
	var items []pluginItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var wrapped struct {
		Plugins []pluginItem `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		return wrapped.Plugins, nil
	}
	return nil, errors.New("неочікувана відповідь /api/plugins")
}

// pluginAction starts or stops a plugin by name. Names are path-escaped
// (the panel uses encodeURIComponent) so spaces and slashes survive routing.
func (c *daemonClient) pluginAction(name, action string) error {
	return c.request("POST", fmt.Sprintf("/api/plugins/%s/%s", url.PathEscape(name), action), nil, nil)
}

func (c *daemonClient) togglePlugin(accountID, pluginName string) error {
	return c.request("POST", fmt.Sprintf("/api/accounts/%s/plugins/%s/toggle", url.PathEscape(accountID), url.PathEscape(pluginName)), nil, nil)
}

// resetPluginSettings drops a plugin's stored settings back to schema
// defaults (mirrors the panel's "Скинути" button).
func (c *daemonClient) resetPluginSettings(name string) error {
	return c.request("DELETE", "/api/plugins/"+url.PathEscape(name)+"/settings", nil, nil)
}

// commandSpec mirrors plugins.CommandSpec for the /api/commands listing.
type commandSpec struct {
	Name        string   `json:"name"`
	Plugin      string   `json:"plugin"`
	Usage       string   `json:"usage"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases"`
}

// getCommands lists every command exposed by running plugins.
func (c *daemonClient) getCommands() ([]commandSpec, error) {
	var cmds []commandSpec
	if err := c.request("GET", "/api/commands", nil, &cmds); err != nil {
		return nil, err
	}
	return cmds, nil
}

// runCommand executes a plugin command and returns its text output.
func (c *daemonClient) runCommand(name, text string) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	if err := c.request("POST", "/api/command",
		map[string]string{"name": name, "text": text}, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

// getAccounts lists Telegram accounts known to the core.
func (c *daemonClient) getAccounts() ([]proto.AccountInfo, error) {
	var accs []proto.AccountInfo
	if err := c.request("GET", "/api/accounts", nil, &accs); err != nil {
		return nil, err
	}
	return accs, nil
}

// activateAccount switches the core to another account.
func (c *daemonClient) activateAccount(id string) error {
	return c.request("POST", "/api/accounts/"+url.PathEscape(id)+"/activate", nil, nil)
}

// getSessions lists active Telegram sessions (other devices).
func (c *daemonClient) getSessions() ([]tgc.AuthSession, error) {
	var wrapped struct {
		Sessions []tgc.AuthSession `json:"sessions"`
	}
	if err := c.request("GET", "/api/sessions", nil, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Sessions, nil
}

// terminateSession ends a session by hash (0 = current is rejected server-side).
func (c *daemonClient) terminateSession(hash int64) error {
	return c.request("POST", fmt.Sprintf("/api/sessions/%d/terminate", hash), nil, nil)
}

// getProfile reads the self profile (name, username, bio).
func (c *daemonClient) getProfile() (tgc.FullProfile, error) {
	var p tgc.FullProfile
	if err := c.request("GET", "/api/profile", nil, &p); err != nil {
		return p, err
	}
	return p, nil
}

func (c *daemonClient) send(peer, text string) error {
	return c.request("POST", "/api/send", map[string]string{"peer": peer, "text": text}, nil)
}
