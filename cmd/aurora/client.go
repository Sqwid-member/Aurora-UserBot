package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/web"
)

type statusResponse struct {
	Core          string  `json:"core"`
	Version       string  `json:"version"`
	MemoryMB      float64 `json:"memory_mb"`
	Session       string  `json:"session"`
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

// pluginAction starts or stops a plugin by name.
func (c *daemonClient) pluginAction(name, action string) error {
	return c.request("POST", fmt.Sprintf("/api/plugins/%s/%s", name, action), nil, nil)
}

func (c *daemonClient) togglePlugin(accountID, pluginName string) error {
	return c.request("POST", fmt.Sprintf("/api/accounts/%s/plugins/%s/toggle", accountID, pluginName), nil, nil)
}

func (c *daemonClient) send(peer, text string) error {
	return c.request("POST", "/api/send", map[string]string{"peer": peer, "text": text}, nil)
}
