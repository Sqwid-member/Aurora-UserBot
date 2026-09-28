package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Desc    string `json:"description"`
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

func (c *daemonClient) submitPassword(password string) error {
	return c.request("POST", "/api/auth/password", map[string]string{"password": password}, nil)
}

func (c *daemonClient) triggerGC() error {
	return c.request("POST", "/api/gc", nil, nil)
}

func (c *daemonClient) logout() error {
	return c.request("POST", "/api/logout", nil, nil)
}

func (c *daemonClient) getPlugins() ([]pluginItem, error) {
	var items []pluginItem
	err := c.request("GET", "/api/plugins", nil, &items)
	return items, err
}

func (c *daemonClient) togglePlugin(accountID, pluginName string) error {
	return c.request("POST", fmt.Sprintf("/api/accounts/%s/plugins/%s/toggle", accountID, pluginName), nil, nil)
}
