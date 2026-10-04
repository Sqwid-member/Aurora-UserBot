// Package config loads, validates and persists the Aurora configuration file.
//
// The on-disk format is plain JSON with a stable top-level shape, so a user
// can edit it by hand inside Termux without fighting a schema.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
)

// Version is the current config schema version.
const Version = 1

// Config is the root configuration object.
type Config struct {
	Version int `json:"version"`

	// Telegram holds credentials and connection tuning.
	Telegram Telegram `json:"telegram"`

	// Web holds the local control panel settings.
	Web Web `json:"web"`

	// Runtime holds process-level knobs.
	Runtime Runtime `json:"runtime"`

	// Plugins holds the plugin manager settings.
	Plugins Plugins `json:"plugins"`

	// Accounts holds multi-account configuration.
	Accounts []AccountConfig `json:"accounts,omitempty"`

	// ActiveAccount is the ID of the currently selected account in the UI.
	ActiveAccount string `json:"active_account,omitempty"`

	// Snoop watches deleted and edited messages.
	Snoop Snoop `json:"snoop"`
}

// Snoop holds the deleted/edited message watcher settings.
type Snoop struct {
	// Enabled records messages and reports deletions/edits.
	Enabled bool `json:"enabled"`
	// Target is where catches are posted ("me" by default).
	Target string `json:"target,omitempty"`
	// NotifyDelete posts a message when something is deleted.
	NotifyDelete bool `json:"notify_delete"`
	// NotifyEdit posts a message when something is edited.
	NotifyEdit bool `json:"notify_edit"`
	// KeepDays bounds how long messages are remembered.
	KeepDays int `json:"keep_days,omitempty"`
	// MaxMessages caps remembered messages.
	MaxMessages int `json:"max_messages,omitempty"`
}

// Telegram holds Telegram-specific configuration.

// AccountConfig holds configuration for an individual Telegram account.
type AccountConfig struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Phone          string   `json:"phone,omitempty"`
	AppID          int      `json:"app_id,omitempty"`
	AppHash        string   `json:"app_hash,omitempty"`
	EnabledPlugins []string `json:"enabled_plugins,omitempty"`
}

// EffectiveAppID returns account-specific AppID or global/default AppID.
func (a AccountConfig) EffectiveAppID(globalDefault int) int {
	if a.AppID > 0 {
		return a.AppID
	}
	if globalDefault > 0 {
		return globalDefault
	}
	return DefaultAppID
}

// EffectiveAppHash returns account-specific AppHash or global/default AppHash.
func (a AccountConfig) EffectiveAppHash(globalDefault string) string {
	if h := strings.TrimSpace(a.AppHash); h != "" {
		return h
	}
	if h := strings.TrimSpace(globalDefault); h != "" {
		return h
	}
	return DefaultAppHash
}

// IsPluginEnabled reports whether a plugin is enabled for this account.
// If EnabledPlugins is nil, all installed plugins are enabled by default.
func (a AccountConfig) IsPluginEnabled(pluginName string) bool {
	if a.EnabledPlugins == nil {
		return true
	}
	for _, p := range a.EnabledPlugins {
		if strings.EqualFold(p, pluginName) {
			return true
		}
	}
	return false
}

type Telegram struct {
	// AppID and AppHash are required. Get them at https://my.telegram.org.
	AppID   int    `json:"app_id"`
	AppHash string `json:"app_hash"`

	// Phone is prefilled for the login flow (+380...).
	Phone string `json:"phone,omitempty"`

	// TestDC points at Telegram's test environment.
	TestDC bool `json:"test_dc,omitempty"`

	// BlockedMode enables MTProxy, useful in censored regions.
	BlockedMode bool `json:"blocked_mode,omitempty"`

	// MTProxy is an optional proxy URL, e.g.
	// "tcp+secret://hexsecret@1.2.3.4:443".
	MTProxy string `json:"mtproxy,omitempty"`

	// Socks5 is an optional socks5 proxy URL.
	Socks5 string `json:"socks5,omitempty"`

	// Device fields identify this installation in Telegram's active sessions.
	DeviceName     string `json:"device_name"`
	DeviceModel    string `json:"device_model"`
	DeviceSystem   string `json:"device_system"`
	DeviceVersion  string `json:"device_version"`
	DeviceLanguage string `json:"device_language"`

	// PFS enables perfect forward secrecy (slightly more CPU, less RAM churn).
	PFS bool `json:"pfs,omitempty"`

	// DisableUpdates stops the client from receiving updates. Plugins that only
	// send messages can use it to cut RAM usage.
	DisableUpdates bool `json:"disable_updates,omitempty"`
}

// Web holds the local control panel configuration.
type Web struct {
	// Enabled turns the panel on.
	Enabled bool `json:"enabled"`
	// Host is the bind address. Keep it on loopback unless you know the risk.
	Host string `json:"host"`
	// Port is the TCP port.
	Port int `json:"port"`
	// Token is the bearer token required by the API and the panel.
	Token string `json:"token"`
	// OpenBrowser tries to launch the system browser on start.
	OpenBrowser bool `json:"open_browser"`
}

// Runtime holds process-level tuning.
type Runtime struct {
	// LogLevel is one of trace|debug|info|warn|error.
	LogLevel string `json:"log_level"`
	// MemLimitMB caps the Go heap soft limit. 0 disables the cap.
	// This is the single biggest lever for "lowest RAM possible".
	MemLimitMB int `json:"mem_limit_mb"`
	// EventQueue bounds the in-process event bus.
	EventQueue int `json:"event_queue"`
	// ReadOnly refuses every mutating API call from the panel.
	ReadOnly bool `json:"read_only,omitempty"`
}

// Plugins holds the plugin manager configuration.
type Plugins struct {
	// Dir overrides the plugin root directory.
	Dir string `json:"dir,omitempty"`
	// Disabled lists plugin names that must not be auto-started.
	Disabled []string `json:"disabled,omitempty"`
	// StartTimeoutSec is how long a plugin gets to answer plugin.hello.
	StartTimeoutSec int `json:"start_timeout_sec"`
	// Sandbox enables Landlock filesystem isolation when the kernel supports it.
	Sandbox bool `json:"sandbox"`
	// DefaultMemoryMB is the per-plugin memory limit (RLIMIT_DATA) applied
	// when a manifest does not set one. Keep it generous: it caps a runaway
	// plugin, it is not a target to squeeze plugins into.
	DefaultMemoryMB int `json:"default_memory_mb"`
}

// Default returns a configuration with sane, phone-friendly defaults.
func Default() *Config {
	return &Config{
		Version: Version,
		Telegram: Telegram{
			// Model/system/language stay empty on purpose: the installer
			// snapshots the real phone ("aurora device --save") and the
			// runtime falls back to live detection. A generic placeholder
			// here would shadow the genuine hardware forever.
			DeviceName:     "Aurora",
			DeviceModel:    "",
			DeviceSystem:   "",
			DeviceVersion:  "14",
			DeviceLanguage: "",
		},
		Web: Web{
			Enabled:     true,
			Host:        "127.0.0.1",
			Port:        8420,
			Token:       NewToken(),
			OpenBrowser: true,
		},
		Runtime: Runtime{
			LogLevel:   "info",
			MemLimitMB: 96,
			EventQueue: 1024,
			ReadOnly:   false,
		},
		Plugins: Plugins{
			StartTimeoutSec: 15,
			Sandbox:         true,
			DefaultMemoryMB: 256,
			Disabled:        []string{},
		},
		Snoop: Snoop{
			Enabled:      true,
			Target:       "me",
			NotifyDelete: true,
			NotifyEdit:   true,
			KeepDays:     7,
			MaxMessages:  2000,
		},
	}
}

// NewToken returns a fresh 32-hex-char API token.
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not realistically fail; fall back to a time seed.
		return "aurora" + strconv.FormatInt(timeNowUnix(), 36)
	}
	return hex.EncodeToString(b)
}

// Store is a concurrency-safe, atomically-persisted config holder.
type Store struct {
	path    string
	writeMu sync.Mutex
	mu      sync.RWMutex
	cfg     *Config
	log     *logx.Logger
}

// Open loads the config from path, creating it with defaults if missing.
func Open(path string, log *logx.Logger) (*Store, error) {
	s := &Store{path: path, cfg: Default(), log: log}
	if log == nil {
		log = logx.New(logx.Options{Sink: os.Stderr}, "")
		s.log = log
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Normalize before the first save: a fresh install must already carry
		// the default account, otherwise the core starts with no Telegram
		// runtime at all and every auth call dereferences a nil client.
		s.cfg.normalize()
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, err
	}

	cfg := Default()
	// Tolerant reader: unknown fields must not brick the core after
	// update/downgrade. Strictness belongs in `aurora config check`, not here.
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config %s is invalid: %w", path, err)
	}
	cfg.normalize()
	s.cfg = cfg
	return s, nil
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// clone returns a deep copy of the configuration.
func (c *Config) clone() Config {
	out := *c
	out.Accounts = make([]AccountConfig, len(c.Accounts))
	for i := range c.Accounts {
		out.Accounts[i] = c.Accounts[i]
		if c.Accounts[i].EnabledPlugins != nil {
			out.Accounts[i].EnabledPlugins = append([]string(nil), c.Accounts[i].EnabledPlugins...)
		}
	}
	out.Plugins.Disabled = append([]string(nil), c.Plugins.Disabled...)
	return out
}

// Get returns a snapshot of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.clone()
}

// Update mutates the config under lock and persists the result.
func (s *Store) Update(fn func(*Config)) (err error) {
	s.mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			s.mu.Unlock()
			panic(r)
		}
	}()
	fn(s.cfg)
	s.cfg.normalize()
	c := s.cfg.clone()
	s.mu.Unlock()
	return s.write(c)
}

// Replace swaps the whole config and persists it.
func (s *Store) Replace(c Config) error {
	c.normalize()
	s.mu.Lock()
	s.cfg = &c
	s.mu.Unlock()
	return s.write(c)
}

func (s *Store) save() error { return s.write(*s.cfg) }

func (s *Store) write(c Config) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	c.Version = Version
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// normalize fills in blanks and clamps out-of-range values.
func (c *Config) normalize() {
	d := Default()

	// Migrate installs that pinned the retired Telegram Desktop credentials:
	// Telegram now answers API_ID_INVALID for them, so fall back to the
	// working defaults instead of silently breaking the login flow.
	if c.Telegram.AppID == 2040 && strings.EqualFold(strings.TrimSpace(c.Telegram.AppHash), "b1844f235887e4c988483c31679563b1") {
		c.Telegram.AppID = 0
		c.Telegram.AppHash = ""
	}

	if c.Version == 0 {
		c.Version = Version
	}
	if c.Telegram.DeviceName == "" {
		c.Telegram.DeviceName = d.Telegram.DeviceName
	}
	if c.Telegram.DeviceModel == "" {
		c.Telegram.DeviceModel = d.Telegram.DeviceModel
	}
	if c.Telegram.DeviceSystem == "" {
		c.Telegram.DeviceSystem = d.Telegram.DeviceSystem
	}
	if c.Telegram.DeviceVersion == "" {
		c.Telegram.DeviceVersion = d.Telegram.DeviceVersion
	}
	if c.Telegram.DeviceLanguage == "" {
		c.Telegram.DeviceLanguage = d.Telegram.DeviceLanguage
	}
	if c.Web.Host == "" {
		c.Web.Host = d.Web.Host
	}
	if c.Web.Port <= 0 || c.Web.Port > 65535 {
		c.Web.Port = d.Web.Port
	}
	// Only generate when empty: silently replacing a short-but-explicit
	// user token breaks every saved panel link on next save.
	if len(c.Web.Token) == 0 {
		c.Web.Token = NewToken()
	}
	if c.Runtime.LogLevel == "" {
		c.Runtime.LogLevel = d.Runtime.LogLevel
	}
	// Snoop knobs that must never stay empty/zero when the watcher runs.
	if c.Snoop.Target == "" {
		c.Snoop.Target = "me"
	}
	if c.Snoop.KeepDays <= 0 {
		c.Snoop.KeepDays = 7
	}
	if c.Snoop.MaxMessages <= 0 {
		c.Snoop.MaxMessages = 2000
	}
	if c.Runtime.MemLimitMB < 0 {
		c.Runtime.MemLimitMB = 0
	}
	if c.Runtime.EventQueue <= 0 {
		c.Runtime.EventQueue = d.Runtime.EventQueue
	}
	if c.Plugins.StartTimeoutSec <= 0 {
		c.Plugins.StartTimeoutSec = d.Plugins.StartTimeoutSec
	}
	if c.Plugins.DefaultMemoryMB <= 0 {
		c.Plugins.DefaultMemoryMB = d.Plugins.DefaultMemoryMB
	}
	if c.Plugins.Disabled == nil {
		c.Plugins.Disabled = []string{}
	}

	for i := range c.Accounts {
		acc := &c.Accounts[i]
		if acc.AppID == 2040 && strings.EqualFold(strings.TrimSpace(acc.AppHash), "b1844f235887e4c988483c31679563b1") {
			acc.AppID = 0
			acc.AppHash = ""
		}
	}

	if len(c.Accounts) == 0 {
		c.Accounts = []AccountConfig{
			{
				ID:             "default",
				Title:          "Основний акаунт",
				Phone:          c.Telegram.Phone,
				AppID:          c.Telegram.AppID,
				AppHash:        c.Telegram.AppHash,
				EnabledPlugins: nil,
			},
		}
	}
	if c.ActiveAccount == "" && len(c.Accounts) > 0 {
		c.ActiveAccount = c.Accounts[0].ID
	}
	foundActive := false
	for _, acc := range c.Accounts {
		if acc.ID == c.ActiveAccount {
			foundActive = true
			break
		}
	}
	if !foundActive && len(c.Accounts) > 0 {
		c.ActiveAccount = c.Accounts[0].ID
	}
}

const (
	// DefaultAppID is the public API ID of the official Telegram Web client.
	// Telegram rejects the old Telegram Desktop ID (2040) for new logins with
	// API_ID_INVALID, so the working Web credentials are the sane default.
	// Users can (and for serious use should) override them with `aurora setup`.
	DefaultAppID = 611335
	// DefaultAppHash is the matching public API hash.
	DefaultAppHash = "d524b414d21f4d37f08684c1df41ac9c"
)

// EffectiveAppID returns the user-configured AppID or DefaultAppID if unset.
func (c Config) EffectiveAppID() int {
	if c.Telegram.AppID > 0 {
		return c.Telegram.AppID
	}
	return DefaultAppID
}

// EffectiveAppHash returns the user-configured AppHash or DefaultAppHash if unset.
func (c Config) EffectiveAppHash() string {
	if h := strings.TrimSpace(c.Telegram.AppHash); h != "" {
		return h
	}
	return DefaultAppHash
}

// UsingCustomAPIKeys reports whether the user configured their own Telegram credentials.
func (c Config) UsingCustomAPIKeys() bool {
	return c.Telegram.AppID > 0 && strings.TrimSpace(c.Telegram.AppHash) != ""
}

// Validate reports whether the config is ready to connect to Telegram.
func (c Config) Validate() error {
	if c.EffectiveAppID() <= 0 || c.EffectiveAppHash() == "" {
		return errors.New("missing required settings: telegram.app_id, telegram.app_hash")
	}
	return nil
}

// IsDisabled reports whether a plugin name is on the disabled list.
func (c Config) IsDisabled(name string) bool {
	for _, n := range c.Plugins.Disabled {
		if n == name {
			return true
		}
	}
	return false
}

// GetAccount finds an account by ID or returns the active account if id is empty.
func (c Config) GetAccount(id string) (AccountConfig, bool) {
	target := id
	if target == "" {
		target = c.ActiveAccount
	}
	for _, a := range c.Accounts {
		if a.ID == target {
			return a, true
		}
	}
	if len(c.Accounts) > 0 {
		return c.Accounts[0], true
	}
	return AccountConfig{ID: "default", Title: "Основний акаунт"}, false
}

// ToggleAccountPlugin toggles whether pluginName is enabled for account accID.
func (c *Config) ToggleAccountPlugin(accID, pluginName string, allPlugins []string) (bool, error) {
	for i := range c.Accounts {
		if c.Accounts[i].ID == accID {
			acc := &c.Accounts[i]
			if acc.EnabledPlugins == nil {
				// initialize with all except the one we are toggling off
				var list []string
				for _, p := range allPlugins {
					if !strings.EqualFold(p, pluginName) {
						list = append(list, p)
					}
				}
				acc.EnabledPlugins = list
				return false, nil
			}

			// check if present
			idx := -1
			for j, p := range acc.EnabledPlugins {
				if strings.EqualFold(p, pluginName) {
					idx = j
					break
				}
			}
			if idx >= 0 {
				// remove without mutating slice in-place
				newList := make([]string, 0, len(acc.EnabledPlugins)-1)
				newList = append(newList, acc.EnabledPlugins[:idx]...)
				newList = append(newList, acc.EnabledPlugins[idx+1:]...)
				acc.EnabledPlugins = newList
				return false, nil
			}
			// add
			acc.EnabledPlugins = append(acc.EnabledPlugins, pluginName)
			return true, nil
		}
	}
	return false, fmt.Errorf("account %q not found", accID)
}
