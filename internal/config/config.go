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

	"github.com/aurora/aurora/internal/logx"
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
}

// Telegram holds Telegram-specific configuration.
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
	// DefaultMemoryMB is the per-plugin address-space limit when unspecified.
	DefaultMemoryMB int `json:"default_memory_mb"`
}

// Default returns a configuration with sane, phone-friendly defaults.
func Default() *Config {
	return &Config{
		Version: Version,
		Telegram: Telegram{
			DeviceName:     "Aurora",
			DeviceModel:    "Android",
			DeviceSystem:   "Android",
			DeviceVersion:  "14",
			DeviceLanguage: "en",
		},
		Web: Web{
			Enabled:     true,
			Host:        "127.0.0.1",
			Port:        8420,
			Token:       NewToken(),
			OpenBrowser: true,
		},
		Runtime: Runtime{
			LogLevel:    "info",
			MemLimitMB:  96,
			EventQueue:  1024,
			ReadOnly:    false,
		},
		Plugins: Plugins{
			StartTimeoutSec:   15,
			Sandbox:          true,
			DefaultMemoryMB:   128,
			Disabled:         []string{},
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
	path string
	mu   sync.RWMutex
	cfg  *Config
	log  *logx.Logger
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
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, err
	}

	cfg := Default()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("config %s is invalid: %w", path, err)
	}
	cfg.normalize()
	s.cfg = cfg
	return s, nil
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Get returns a snapshot of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.cfg
}

// Update mutates the config under lock and persists the result.
func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	fn(s.cfg)
	s.cfg.normalize()
	c := *s.cfg
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
	if len(c.Web.Token) < 16 {
		c.Web.Token = NewToken()
	}
	if c.Runtime.LogLevel == "" {
		c.Runtime.LogLevel = d.Runtime.LogLevel
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
}

// Validate reports whether the config is ready to connect to Telegram.
func (c Config) Validate() error {
	var missing []string
	if c.Telegram.AppID <= 0 {
		missing = append(missing, "telegram.app_id")
	}
	if strings.TrimSpace(c.Telegram.AppHash) == "" {
		missing = append(missing, "telegram.app_hash")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
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
