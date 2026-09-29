package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
)

const settingsManifest = `{
  "name": "settest",
  "version": "1.0.0",
  "runtime": {"command": "python3", "args": ["main.py"]},
  "events": ["message.new"],
  "settings": [
    {"key": "title", "type": "text", "title": "Title", "default": "hi"},
    {"key": "secret", "type": "password", "title": "Secret"},
    {"key": "count", "type": "number", "title": "Count", "min": 1, "max": 10, "default": 3},
    {"key": "flag", "type": "bool", "title": "Flag", "default": true},
    {"key": "mode", "type": "select", "title": "Mode",
     "options": [{"value": "a"}, {"value": "b", "label": "Bee"}],
     "default": "a"}
  ]
}`

func newSettingsHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "settest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(settingsManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := kv.Open(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := logx.New(logx.Options{Level: logx.LevelError, Sink: os.Stderr}, "test")
	h := New(nil, root, store, log, Options{})
	if _, err := h.Install(dir); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestValidateSettingsCases(t *testing.T) {
	good := []SettingField{
		{Key: "a", Type: SettingText, Default: "x"},
		{Key: "b", Type: SettingNumber, Min: floatPtr(1), Max: floatPtr(2), Default: 1.5},
		{Key: "c", Type: SettingBool, Default: false},
		{Key: "d", Type: SettingSelect, Options: []SettingOption{{Value: "x"}}, Default: "x"},
		{Key: "e", Type: SettingPassword},
	}
	if err := validateSettings(good); err != nil {
		t.Fatalf("good schema: %v", err)
	}

	bad := []struct {
		name   string
		fields []SettingField
	}{
		{"bad key", []SettingField{{Key: "UPPER", Type: SettingText}}},
		{"dup key", []SettingField{{Key: "a", Type: SettingText}, {Key: "a", Type: SettingBool}}},
		{"unknown type", []SettingField{{Key: "a", Type: "slider"}}},
		{"text default type", []SettingField{{Key: "a", Type: SettingText, Default: 5}}},
		{"number default type", []SettingField{{Key: "a", Type: SettingNumber, Default: "x"}}},
		{"min>max", []SettingField{{Key: "a", Type: SettingNumber, Min: floatPtr(5), Max: floatPtr(1)}}},
		{"bool default type", []SettingField{{Key: "a", Type: SettingBool, Default: "x"}}},
		{"select no options", []SettingField{{Key: "a", Type: SettingSelect}}},
		{"select bad default", []SettingField{{Key: "a", Type: SettingSelect, Options: []SettingOption{{Value: "x"}}, Default: "y"}}},
	}
	for _, tc := range bad {
		if err := validateSettings(tc.fields); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}

	many := make([]SettingField, MaxSettingFields+1)
	for i := range many {
		many[i] = SettingField{Key: "k", Type: SettingText}
	}
	// Duplicate keys error first; make them unique to hit the count cap.
	for i := range many {
		many[i].Key = "k" + string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	if err := validateSettings(many); err == nil {
		t.Error("expected too-many-fields error")
	}
}

func floatPtr(f float64) *float64 { return &f }

func TestPluginSettingsRoundTrip(t *testing.T) {
	h := newSettingsHost(t)

	// Defaults before anything is stored.
	fields, err := h.PluginSettings("settest")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatalf("fields = %d, want 5", len(fields))
	}
	for _, f := range fields {
		if f.Stored {
			t.Errorf("%s unexpectedly stored", f.Field.Key)
		}
	}
	if fields[0].Value != "hi" {
		t.Errorf("title default = %v, want hi", fields[0].Value)
	}

	// Unknown plugin and unknown keys fail.
	if _, err := h.PluginSettings("nope"); err == nil {
		t.Error("expected error for unknown plugin")
	}
	if _, err := h.SetPluginSettings("settest", map[string]any{"nope": 1}); err == nil {
		t.Error("expected error for unknown key")
	}
	if _, err := h.SetPluginSettings("settest", map[string]any{"count": 99}); err == nil {
		t.Error("expected error above max")
	}
	if _, err := h.SetPluginSettings("settest", map[string]any{"mode": "z"}); err == nil {
		t.Error("expected error for bad option")
	}

	saved, err := h.SetPluginSettings("settest", map[string]any{
		"title": "hello",
		"count": 7,
		"flag":  false,
		"mode":  "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]SettingValue{}
	for _, f := range saved {
		byKey[f.Field.Key] = f
	}
	if byKey["title"].Value != "hello" || !byKey["title"].Stored {
		t.Errorf("title not saved: %+v", byKey["title"])
	}
	if byKey["count"].Value != 7.0 {
		t.Errorf("count = %v, want 7", byKey["count"].Value)
	}

	// Reset drops back to defaults.
	if err := h.ResetPluginSettings("settest"); err != nil {
		t.Fatal(err)
	}
	after, err := h.PluginSettings("settest")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range after {
		if f.Stored {
			t.Errorf("%s still stored after reset", f.Field.Key)
		}
	}

	// Plugin without schema.
	if _, err := h.PluginSettings("settest"); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicSchemaMergeReplace(t *testing.T) {
	h := newSettingsHost(t)

	extra := []SettingField{
		{Key: "dyn", Type: SettingText, Title: "Dyn", Default: "d"},
		{Key: "title", Type: SettingText, Title: "Overridden", Default: "o"},
	}
	eff, err := h.SetDynamicSchema("settest", extra, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(eff) != 6 {
		t.Fatalf("effective = %d fields, want 6 (5 manifest + 1 new)", len(eff))
	}
	if eff[0].Key != "title" || eff[0].Title != "Overridden" {
		t.Errorf("manifest field not overridden in place: %+v", eff[0])
	}
	if eff[5].Key != "dyn" {
		t.Errorf("new dynamic field not appended: %+v", eff[5])
	}

	// Unknown mode and unknown plugin fail.
	if _, err := h.SetDynamicSchema("settest", extra, "bogus"); err == nil {
		t.Error("expected error for bad mode")
	}
	if _, err := h.SetDynamicSchema("nope", extra, "merge"); err == nil {
		t.Error("expected error for unknown plugin")
	}

	// Replace clears back to manifest.
	eff, err = h.SetDynamicSchema("settest", nil, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if len(eff) != 5 {
		t.Fatalf("after clear: %d fields, want 5", len(eff))
	}

	// Validation applies to the merged whole.
	many := make([]SettingField, MaxSettingFields)
	for i := range many {
		many[i] = SettingField{Key: "x" + string(rune('a'+i/26)) + string(rune('a'+i%26)), Type: SettingText}
	}
	if _, err := h.SetDynamicSchema("settest", many, "merge"); err == nil {
		t.Error("expected field-cap error")
	}
	if _, err := h.SetDynamicSchema("settest", []SettingField{{Key: "bad!", Type: SettingText}}, "merge"); err == nil {
		t.Error("expected key error")
	}
}

func TestDynamicSchemaValuesAndReset(t *testing.T) {
	h := newSettingsHost(t)

	if _, err := h.SetDynamicSchema("settest",
		[]SettingField{{Key: "level", Type: SettingNumber, Min: floatPtr(0), Max: floatPtr(9), Default: 1}},
		"merge"); err != nil {
		t.Fatal(err)
	}
	fields, err := h.PluginSettings("settest")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fields {
		if f.Field.Key == "level" {
			found = true
			n, ok := toFloat(f.Value)
			if f.Stored || !ok || n != 1 {
				t.Errorf("dynamic default not served: %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("dynamic field missing from settings view")
	}

	if _, err := h.SetPluginSettings("settest", map[string]any{"level": 7}); err != nil {
		t.Fatalf("set dynamic value: %v", err)
	}
	// Reset clears the value but keeps the dynamic schema.
	if err := h.ResetPluginSettings("settest"); err != nil {
		t.Fatal(err)
	}
	after, err := h.PluginSettings("settest")
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, f := range after {
		if f.Field.Key == "level" {
			kept = true
			if f.Stored {
				t.Error("dynamic value survived reset")
			}
		}
	}
	if !kept {
		t.Error("dynamic schema lost on reset")
	}
}

func TestPluginSettingsNoSchema(t *testing.T) {
	h := newSettingsHost(t)
	dir := filepath.Join(h.Root(), "plain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	plain := `{"name": "plain", "version": "1.0.0", "runtime": {"command": "python3", "args": ["m.py"]}}`
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(dir); err != nil {
		t.Fatal(err)
	}
	if fields, err := h.PluginSettings("plain"); err != nil || len(fields) != 0 {
		t.Fatalf("plain plugin: fields=%v err=%v", fields, err)
	}
	if err := h.ResetPluginSettings("plain"); err == nil {
		t.Error("expected error resetting schema-less plugin")
	}
}
