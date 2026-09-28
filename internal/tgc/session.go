package tgc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram/dcs"

	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
)

// Telethon string session layout (see Telethon's StringSession):
//
//	version byte '1' | base64url( dc_id:u8, ip:4B, port:u16, auth_key:256B )
//
// Aurora keeps the same format so a session can move in either direction
// between Aurora, Telethon and Pyrogram without any conversion dance.
const telethonVersion = '1'

// ExportSession renders the local session as a Telethon-compatible string.
func ExportSession(path string) (string, error) {
	loader := session.Loader{Storage: &session.FileStorage{Path: path}}
	data, err := loader.Load(context.Background())
	if err != nil {
		return "", err
	}
	return encodeTelethon(data)
}

func encodeTelethon(data *session.Data) (string, error) {
	if len(data.AuthKey) != 256 {
		return "", fmt.Errorf("unexpected auth key length %d", len(data.AuthKey))
	}
	// Stored sessions may keep the address as bare IP or as "ip:port"
	// (gotd's own StringSession decoder writes the latter).
	addr := strings.TrimSpace(data.Addr)
	ip := net.ParseIP(addr)
	if ip == nil {
		if h, _, err := net.SplitHostPort(addr); err == nil {
			ip = net.ParseIP(strings.TrimSpace(h))
		}
	}
	if ip == nil {
		return "", fmt.Errorf("session address %q has no IPv4 form", data.Addr)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", fmt.Errorf("session address %q has no IPv4 form", data.Addr)
	}
	port := 443
	if _, p, err := net.SplitHostPort(data.Addr); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	var buf bytes.Buffer
	buf.WriteByte(byte(data.DC))
	buf.Write(ip4.To4())
	_ = binary.Write(&buf, binary.BigEndian, uint16(port))
	buf.Write(data.AuthKey)

	return string(telethonVersion) +
		base64.URLEncoding.EncodeToString(buf.Bytes()), nil
}

// webKeyRe matches Telegram Web (localStorage) auth key entries:
// "dc2_auth_key", "dc4_auth_key", ... The value is a 512-char hex string
// (256 bytes) and "dc" holds the active data-center id.
var webKeyRe = regexp.MustCompile(`(?i)^dc(\d+)_auth_key$`)

// ParseWebExport extracts (dc, authKey) from a Telegram Web localStorage
// dump. Accepted shapes:
//
//	{"dc4_auth_key": "<512 hex>", "dc": 4, ...}  (bookmarklet output)
//	{"dc": 4, "auth_key": "<512 hex>"}
//	"<512 hex>"                                  (bare key, dc from override)
//
// overrideDC (1..5) wins over anything found in the payload; pass 0 for
// auto-detect. The returned key is always exactly 256 bytes.
func ParseWebExport(input string, overrideDC int) (int, []byte, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return 0, nil, fmt.Errorf("порожній експорт — вставте JSON з букмарклета")
	}
	if overrideDC < 0 || overrideDC > 5 {
		return 0, nil, fmt.Errorf("некоректний DC %d (допустимо 1..5 або авто)", overrideDC)
	}

	// Bare key fast path.
	if key, err := decodeAuthKey(s); err == nil {
		if overrideDC == 0 {
			return 0, nil, fmt.Errorf("знайдено ключ, але невідомо який DC — виберіть DC 1..5 вручну")
		}
		return overrideDC, key, nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return 0, nil, fmt.Errorf("не схоже ні на StringSession, ні на JSON з Telegram Web")
	}

	candidates := map[int][]byte{}
	for k, v := range raw {
		m := webKeyRe.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		dc, err := strconv.Atoi(m[1])
		if err != nil || dc < 1 || dc > 5 {
			continue
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		key, err := decodeAuthKey(strings.TrimSpace(str))
		if err != nil {
			continue
		}
		candidates[dc] = key
	}
	// Explicit {"auth_key": ...} shape.
	if len(candidates) == 0 {
		if v, ok := raw["auth_key"].(string); ok {
			if key, err := decodeAuthKey(strings.TrimSpace(v)); err == nil {
				if dc, ok := explicitDC2(raw); ok {
					candidates[dc] = key
				} else if overrideDC != 0 {
					return overrideDC, key, nil
				} else {
					return 0, nil, fmt.Errorf("знайдено ключ, але невідомо який DC — виберіть DC 1..5 вручну")
				}
			}
		}
	}
	if len(candidates) == 0 {
		return 0, nil, fmt.Errorf("у JSON немає жодного dcN_auth_key — ви точно залогінені на web.telegram.org і запускали букмарклет там?")
	}

	if overrideDC != 0 {
		if key, ok := candidates[overrideDC]; ok {
			return overrideDC, key, nil
		}
		return 0, nil, fmt.Errorf("для DC %d ключа в експорті немає (є DC %s)", overrideDC, dcList(candidates))
	}
	if dc, ok := explicitDC2(raw); ok {
		if key, ok := candidates[dc]; ok {
			return dc, key, nil
		}
	}
	if len(candidates) == 1 {
		for dc, key := range candidates {
			return dc, key, nil
		}
	}
	return 0, nil, fmt.Errorf("в експорті ключі кількох DC (%s) — виберіть потрібний DC вручну", dcList(candidates))
}

// explicitDC2 reads an optional "dc" field of a web export object.
func explicitDC2(raw map[string]any) (int, bool) {
	v, ok := raw["dc"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n >= 1 && n <= 5 {
			return int(n), true
		}
	case string:
		if dc, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && dc >= 1 && dc <= 5 {
			return dc, true
		}
	}
	return 0, false
}

func dcList(candidates map[int][]byte) string {
	var b strings.Builder
	first := true
	for dc := 1; dc <= 5; dc++ {
		if _, ok := candidates[dc]; !ok {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(dc))
		first = false
	}
	return b.String()
}

// decodeAuthKey accepts a 256-byte auth key as hex (what Telegram Web
// stores) or base64, and rejects anything else.
func decodeAuthKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == 512 {
		key, err := hex.DecodeString(s)
		if err == nil && len(key) == 256 {
			return key, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == 256 {
			return key, nil
		}
	}
	return nil, fmt.Errorf("ключ має бути 256 байт (hex 512 символи)")
}

// ImportWebExport parses a Telegram Web export and stores it as the local
// session, converting the web auth key into the same gotd format Aurora
// uses for StringSession imports.
func ImportWebExport(path, input string, overrideDC int) (int, error) {
	dc, key, err := ParseWebExport(input, overrideDC)
	if err != nil {
		return 0, err
	}
	addr := webDCAddr(dc)
	if addr == "" {
		return 0, fmt.Errorf("немає адреси для DC %d", dc)
	}
	var k crypto.Key
	copy(k[:], key)
	id := k.WithID().ID
	data := &session.Data{
		DC:        dc,
		Addr:      addr,
		AuthKey:   k[:],
		AuthKeyID: id[:],
	}
	loader := session.Loader{Storage: &session.FileStorage{Path: path}}
	if err := loader.Save(context.Background(), data); err != nil {
		return 0, err
	}
	return dc, nil
}

// webDCAddr resolves a production DC id to its bare IPv4 address using
// gotd's built-in list, skipping IPv6-only, CDN, media-only and
// obfuscated-only entries. Bare IP is what Aurora's own export expects;
// gotd itself reconnects through its resolver, not this field.
func webDCAddr(dc int) string {
	for _, opt := range dcs.Prod().Options {
		if opt.ID != dc || opt.Ipv6 || opt.CDN || opt.MediaOnly || opt.TCPObfuscatedOnly {
			continue
		}
		if ip := strings.TrimSpace(opt.IPAddress); ip != "" {
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
				return ip
			}
		}
	}
	return ""
}

// SessionInfo is a safe summary of the stored session.
type SessionInfo = proto.SessionInfo

// InspectSession reports what is stored without exposing the auth key.
func InspectSession(path string) SessionInfo {
	loader := session.Loader{Storage: &session.FileStorage{Path: path}}
	data, err := loader.Load(context.Background())
	if err != nil {
		return SessionInfo{}
	}
	info := SessionInfo{
		Exists:  true,
		DC:      data.DC,
		Address: data.Addr,
	}
	if len(data.AuthKeyID) > 0 {
		info.AuthKeyID = strings.ToUpper(fmt.Sprintf("%x", data.AuthKeyID))
	}
	return info
}
