package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const goodManifest = `{
  "name": "demo",
  "version": "1.0.0",
  "description": "demo",
  "language": "go",
  "runtime": {"command": "./demo"},
  "events": ["message.new"],
  "commands": [{"name": "Demo", "aliases": ["d", "d"], "description": "x"}],
  "permissions": {"tg": ["send", "send"]},
  "limits": {"memory_mb": 99999, "cpu_seconds": -5}
}`

func TestLoadValidManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeManifest(t, dir, goodManifest)

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.Name != "demo" || m.Version != "1.0.0" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	// Commands are lowercased and de-duplicated, aliases too.
	if m.Commands[0].Name != "demo" {
		t.Errorf("command name = %q, want lowercased", m.Commands[0].Name)
	}
	if len(m.Commands[0].Aliases) != 1 {
		t.Errorf("aliases = %v, want one", m.Commands[0].Aliases)
	}
	// Permissions are normalised.
	if len(m.Permissions.TG) != 1 {
		t.Errorf("capabilities = %v, want one", m.Permissions.TG)
	}
	// Limits are clamped, never trusted.
	if m.Limits.MemoryMB != MaxMemoryMB {
		t.Errorf("memory limit = %d, want it clamped to %d", m.Limits.MemoryMB, MaxMemoryMB)
	}
	if m.Limits.CPUSeconds != 0 {
		t.Errorf("negative cpu limit should be neutralised, got %d", m.Limits.CPUSeconds)
	}
}

func TestDirectoryNameMustMatchManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "other")
	writeManifest(t, dir, goodManifest)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("a mismatched directory name must be rejected")
	}
}

func TestPathEscapeIsRejected(t *testing.T) {
	cases := map[string]string{
		"absolute command": `{"name":"demo","runtime":{"command":"/bin/sh"}}`,
		"traversal":        `{"name":"demo","runtime":{"command":"../../evil"}}`,
		"arg escape":       `{"name":"demo","runtime":{"command":"./demo","args":["../../etc/passwd"]}}`,
		"empty command":    `{"name":"demo","runtime":{"command":"  "}}`,
		"no runtime":       `{"name":"demo"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			writeManifest(t, dir, body)
			if _, err := LoadManifest(dir); err == nil {
				t.Fatalf("%s: expected an error", name)
			}
		})
	}
}

func TestWrapperEscapeIsRejected(t *testing.T) {
	cases := map[string]string{
		"absolute wrapper": `{"name":"demo","runtime":{"command":"./demo","wrap":"/bin/sh"}}`,
		"traversal":        `{"name":"demo","runtime":{"command":"./demo","wrap":"../evil"}}`,
		"nested path":      `{"name":"demo","runtime":{"command":"./demo","wrap":"a/b"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "demo")
			writeManifest(t, dir, body)
			if _, err := LoadManifest(dir); err == nil {
				t.Fatalf("%s: expected an error", name)
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "demo")
	writeManifest(t, dir, `{"name":"demo","runtime":{"command":"./demo","wrap":"proot"}}`)
	if _, err := LoadManifest(dir); err != nil {
		t.Fatalf("bare wrapper must pass: %v", err)
	}
}

func TestBadNameIsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Bad Name")
	writeManifest(t, dir, `{"name":"Bad Name","runtime":{"command":"./x"}}`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("plugin names must be filesystem-safe")
	}
}

func TestMissingManifest(t *testing.T) {
	if _, err := LoadManifest(t.TempDir()); err != ErrNoManifest {
		t.Fatalf("err = %v, want ErrNoManifest", err)
	}
}

func TestEventAndCapabilityMatching(t *testing.T) {
	m := &Manifest{Events: []string{"message.new"}, Permissions: Permissions{TG: []string{"send"}}}
	if !m.HasEvent("message.new") {
		t.Error("subscribed event not matched")
	}
	if m.HasEvent("message.edited") {
		t.Error("unsubscribed event matched")
	}
	if !m.HasCapability(CapSend) || m.HasCapability(CapRead) {
		t.Error("capability check is wrong")
	}

	all := &Manifest{Events: []string{"*"}, Permissions: Permissions{TG: []string{"*"}}}
	if !all.HasEvent("anything") || !all.HasCapability(CapRead) {
		t.Error("wildcards must work")
	}
}

func TestCommandLookupByAlias(t *testing.T) {
	m := &Manifest{Commands: []CommandSpec{{Name: "ping", Aliases: []string{"p", "пі"}}}}
	if _, ok := m.Command("ping"); !ok {
		t.Error("name lookup failed")
	}
	spec, ok := m.Command("пі")
	if !ok || spec.Name != "ping" {
		t.Error("alias lookup failed")
	}
	if _, ok := m.Command("nope"); ok {
		t.Error("unknown command must not resolve")
	}
}
