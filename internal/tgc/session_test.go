package tgc

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func testKeyHex(t *testing.T, seed byte) string {
	t.Helper()
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return hex.EncodeToString(raw)
}

func TestParseWebExportSingleKey(t *testing.T) {
	key := testKeyHex(t, 0x11)
	payload := `{"dc4_auth_key": "` + key + `", "dc4_server_salt": "aabbcc", "dc": 4}`
	dc, out, err := ParseWebExport(payload, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dc != 4 {
		t.Fatalf("dc = %d, want 4", dc)
	}
	if hex.EncodeToString(out) != key {
		t.Fatal("auth key mismatch")
	}
}

func TestParseWebExportOverrideWins(t *testing.T) {
	k2 := testKeyHex(t, 0x22)
	k4 := testKeyHex(t, 0x44)
	payload := `{"dc2_auth_key": "` + k2 + `", "dc4_auth_key": "` + k4 + `", "dc": 2}`
	dc, out, err := ParseWebExport(payload, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dc != 4 || hex.EncodeToString(out) != k4 {
		t.Fatal("override DC was not honoured")
	}
}

func TestParseWebExportMultiKeyNeedsDC(t *testing.T) {
	payload := `{"dc2_auth_key": "` + testKeyHex(t, 0x22) + `", "dc4_auth_key": "` + testKeyHex(t, 0x44) + `"}`
	if _, _, err := ParseWebExport(payload, 0); err == nil {
		t.Fatal("expected error for ambiguous DCs")
	} else if !strings.Contains(err.Error(), "вручну") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseWebExportBareKey(t *testing.T) {
	key := testKeyHex(t, 0x77)
	dc, out, err := ParseWebExport(key, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dc != 2 || hex.EncodeToString(out) != key {
		t.Fatal("bare key import mismatch")
	}
	if _, _, err := ParseWebExport(key, 0); err == nil {
		t.Fatal("expected error for bare key without DC")
	}
}

func TestParseWebExportGarbage(t *testing.T) {
	for _, in := range []string{"", "hello", "{}", `{"foo": "bar"}`, `{"dc2_auth_key": "short"}`} {
		if _, _, err := ParseWebExport(in, 0); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestWebDCAddrKnown(t *testing.T) {
	for dc := 1; dc <= 5; dc++ {
		if addr := webDCAddr(dc); addr == "" {
			t.Fatalf("no address for DC %d", dc)
		}
	}
	if addr := webDCAddr(0); addr != "" {
		t.Fatalf("unexpected address for DC 0: %s", addr)
	}
}

func TestImportWebExportRoundTrip(t *testing.T) {
	key := testKeyHex(t, 0x99)
	payload := `{"dc2_auth_key": "` + key + `", "dc": 2}`
	path := filepath.Join(t.TempDir(), "web.session")
	dc, err := ImportWebExport(path, payload, 0)
	if err != nil {
		t.Fatalf("import error: %v", err)
	}
	if dc != 2 {
		t.Fatalf("dc = %d, want 2", dc)
	}
	info := InspectSession(path)
	if !info.Exists || info.DC != 2 {
		t.Fatalf("session not stored: %+v", info)
	}
	str, err := ExportSession(path)
	if err != nil {
		t.Fatalf("export error: %v", err)
	}
	if !strings.HasPrefix(str, "1") {
		t.Fatalf("export is not a Telethon string: %.10s", str)
	}
}
