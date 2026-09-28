package plugins

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if got := isBlockedIP(ip); got != tc.want {
			t.Errorf("isBlockedIP(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestUninstallRefusesEscapingDir(t *testing.T) {
	root := t.TempDir()
	store, err := kv.Open(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	h := New(nil, root, store,
		logx.New(logx.Options{Level: logx.LevelError, Sink: os.Stderr}, "test"),
		Options{})

	// Manually inject an instance pointing outside root
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.insts["outside"] = newInstance(&Manifest{Name: "outside"}, outsideDir, h.log, Options{})
	h.order = append(h.order, "outside")

	err = h.Uninstall("outside", true)
	if err == nil {
		t.Fatal("Uninstall should have returned an error for directory outside root")
	}
	if _, err := os.Stat(outsideDir); os.IsNotExist(err) {
		t.Fatal("Uninstall deleted outside directory despite security check")
	}
}
