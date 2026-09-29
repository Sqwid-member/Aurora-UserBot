package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
)

func testLayout(t *testing.T) paths.Layout {
	t.Helper()
	home := t.TempDir()
	return paths.Layout{
		Home:    home,
		Config:  filepath.Join(home, "etc"),
		Data:    filepath.Join(home, "data"),
		Plugins: filepath.Join(home, "plugins"),
		Logs:    filepath.Join(home, "logs"),
		Cache:   filepath.Join(home, "cache"),
		Run:     filepath.Join(home, "run"),
	}
}

func writePerm(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	src := testLayout(t)
	writePerm(t, src.ConfigFile(), `{"version":1}`, 0o600)
	writePerm(t, filepath.Join(src.Data, "session.json"), `{"dc":4}`, 0o600)
	writePerm(t, filepath.Join(src.Data, "session_acc2.json"), `{"dc":2}`, 0o600)
	writePerm(t, filepath.Join(src.Data, "notes.txt"), `not a session`, 0o600)
	writePerm(t, src.DBFile(), `{"k":"v"}`, 0o600)

	dest := filepath.Join(t.TempDir(), "b.json")
	if err := createBackup(src, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("backup mode = %o, want 600", mode)
	}

	var bundle backupBundle
	raw, _ := os.ReadFile(dest)
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Version != backupVersion {
		t.Fatalf("version = %d", bundle.Version)
	}
	for _, want := range []string{"config", "kv", "data/session.json", "data/session_acc2.json"} {
		if _, ok := bundle.Files[want]; !ok {
			t.Errorf("bundle misses %s (has %v)", want, keys(bundle.Files))
		}
	}
	if _, ok := bundle.Files["data/notes.txt"]; ok {
		t.Error("non-session file leaked into bundle")
	}

	dst := testLayout(t)
	if err := restoreBundleFiles(dst, dest); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dst.ConfigFile(), dst.DBFile(), filepath.Join(dst.Data, "session.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not restored: %s", p)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dst.Data, "session.json"))
	if string(got) != `{"dc":4}` {
		t.Errorf("session content = %q", got)
	}

	// Second restore keeps .bak of the first.
	if err := restoreBundleFiles(dst, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst.Data, "session.json.bak")); err != nil {
		t.Error("no .bak kept")
	}
}

func TestBackupRejectsGarbage(t *testing.T) {
	dst := testLayout(t)
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreBundleFiles(dst, bad); err == nil {
		t.Error("expected error for corrupt bundle")
	}
	evil := backupBundle{Version: backupVersion, Files: map[string]string{
		"data/../../evil": base64.StdEncoding.EncodeToString([]byte("x")),
	}}
	raw, _ := json.Marshal(evil)
	evilPath := filepath.Join(t.TempDir(), "evil.json")
	if err := os.WriteFile(evilPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreBundleFiles(dst, evilPath); err == nil {
		t.Error("expected error for path escape")
	}
	// Unknown top-level key form is rejected, not written anywhere.
	if _, err := os.Stat(filepath.Join(dst.Home, "evil")); !os.IsNotExist(err) {
		t.Error("escape file was written")
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
