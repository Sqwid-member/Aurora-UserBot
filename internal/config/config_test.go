package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsArePhoneFriendly(t *testing.T) {
	c := Default()
	if c.Web.Port != 8420 {
		t.Errorf("panel port = %d, want 8420", c.Web.Port)
	}
	if c.Web.Host != "127.0.0.1" {
		t.Errorf("panel must bind loopback by default, got %q", c.Web.Host)
	}
	if c.Runtime.MemLimitMB <= 0 {
		t.Error("a memory limit should be on by default; this is a phone product")
	}
	if len(c.Web.Token) < 16 {
		t.Errorf("token is too short to brute force: %q", c.Web.Token)
	}
}

func TestValidateRequiresCredentials(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error with empty credentials")
	}
	c.Telegram.AppID = 12345
	c.Telegram.AppHash = "deadbeef"
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenCreatesAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(c *Config) {
		c.Telegram.AppID = 777
		c.Telegram.AppHash = "hash"
		c.Telegram.Phone = "+380501234567"
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Get()
	if got.Telegram.AppID != 777 || got.Telegram.Phone != "+380501234567" {
		t.Fatalf("config did not round-trip: %+v", got.Telegram)
	}
	if got.Web.Token != s.Get().Web.Token {
		t.Fatal("the panel token must survive a restart, or the bookmark breaks")
	}
}

func TestNormalizeClampsNonsense(t *testing.T) {
	c := Default()
	c.Web.Port = -5
	c.Web.Token = "short"
	c.Runtime.EventQueue = 0
	c.Plugins.StartTimeoutSec = -1
	c.normalize()

	if c.Web.Port != Default().Web.Port {
		t.Errorf("port = %d", c.Web.Port)
	}
	if len(c.Web.Token) < 16 {
		t.Errorf("token = %q", c.Web.Token)
	}
	if c.Runtime.EventQueue <= 0 {
		t.Errorf("event queue = %d", c.Runtime.EventQueue)
	}
	if c.Plugins.StartTimeoutSec <= 0 {
		t.Errorf("start timeout = %d", c.Plugins.StartTimeoutSec)
	}
}

func TestInvalidJSONIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFile(path, `{"version": 1, "unknown_field": 3}`); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unknown fields should be caught loudly, got %v", err)
	}
}

func TestIsDisabled(t *testing.T) {
	c := Default()
	c.Plugins.Disabled = []string{"noisy"}
	if !c.IsDisabled("noisy") || c.IsDisabled("quiet") {
		t.Fatal("IsDisabled is wrong")
	}
}
