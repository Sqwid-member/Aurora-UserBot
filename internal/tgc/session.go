package tgc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/gotd/td/session"

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
	ip := net.ParseIP(data.Addr)
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
