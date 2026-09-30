package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
)

const maxUpdateBytes = 100 << 20 // 100 MiB: prebuilt is ~18 MiB, anything more is an attack.

type releaseAsset struct {
	Name       string `json:"name"`
	BrowserURL string `json:"browser_download_url"`
	Size       int64  `json:"size"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

func cmdUpdate(layout paths.Layout) error {
	fmt.Println("\n\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")
	fmt.Printf("  \033[1;37m🚀 АВТОМАТИЧНЕ ОНОВЛЕННЯ AURORA USERBOT\033[0m (поточна версія: \033[33m%s\033[0m)\n", buildinfo.Version)
	fmt.Println("\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")

	opsys := runtime.GOOS
	arch := runtime.GOARCH
	// Candidate asset names in preference order: raw binaries first
	// (a .sha256 sidecar must never win the match), then tarballs.
	var candidates []string
	switch opsys + "/" + arch {
	case "android/arm64", "linux/arm64", "darwin/arm64":
		candidates = []string{"aurora-arm64", "aurora-arm64.tar.gz"}
	case "linux/amd64", "darwin/amd64":
		candidates = []string{"aurora-amd64", "aurora-amd64.tar.gz"}
	case "linux/arm":
		candidates = []string{"aurora-linux-arm.tar.gz", "aurora-linux-arm"}
	default:
		return fmt.Errorf("автоматичне оновлення не підтримується для платформи: %s/%s", opsys, arch)
	}

	targetPath, err := os.Executable()
	if err != nil || !strings.Contains(targetPath, string(os.PathSeparator)) {
		if lp, err := sysx.LookPath("aurora"); err == nil {
			targetPath = lp
		} else {
			if paths.IsTermux() {
				targetPath = "/data/data/com.termux/files/usr/bin/aurora"
			} else {
				targetPath = filepath.Join(os.Getenv("HOME"), "bin", "aurora")
			}
		}
	}

	fmt.Printf("→ Отримання інформації про останній реліз...\n")
	client := updateHTTPClient()

	// Fetch latest release from GitHub API
	resp, err := client.Get("https://api.github.com/repos/Sqwid-member/Aurora-UserBot/releases/latest")
	if err != nil {
		return fmt.Errorf("помилка мережі при запиті релізу: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API повернув HTTP %d", resp.StatusCode)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return fmt.Errorf("розбір відповіді GitHub API: %w", err)
	}

	// Find asset for our platform, trying candidate names in preference
	// order. Checksum sidecars (.sha256) never match a candidate, so they
	// can never win.
	var asset *releaseAsset
	for _, want := range candidates {
		for i := range rel.Assets {
			if rel.Assets[i].Name == want {
				asset = &rel.Assets[i]
				break
			}
		}
		if asset != nil {
			break
		}
	}
	if asset == nil {
		return fmt.Errorf("реліз %s не містить бінарника для %s/%s", rel.TagName, opsys, arch)
	}

	// Download the binary
	fmt.Printf("→ Завантаження %s з релізу %s...\n", asset.Name, rel.TagName)
	resp, err = client.Get(asset.BrowserURL)
	if err != nil {
		return fmt.Errorf("помилка мережі при завантаженні: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub повернув HTTP %d при завантаженні бінарника", resp.StatusCode)
	}

	// Download to temp file with size limit. A tarball asset carries the
	// binary inside, so extract its first regular file instead.
	tmpFile := targetPath + ".new"
	out, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("створення файлу оновлення: %w", err)
	}

	// Compute SHA256 while downloading
	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)
	var n int64
	if strings.HasSuffix(asset.Name, ".tar.gz") {
		n, err = extractTarGz(mw, resp.Body)
	} else {
		n, err = io.Copy(mw, io.LimitReader(resp.Body, maxUpdateBytes+1))
	}
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("запис оновлення: %w", err)
	}
	if n > maxUpdateBytes {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("завантаження завелике (%d байт), оновлення скасовано", n)
	}
	if n < 1000000 { // less than 1MB is invalid
		_ = os.Remove(tmpFile)
		return fmt.Errorf("завантажений файл занадто малий (%d байт), оновлення скасовано", n)
	}
	if err := os.Chmod(tmpFile, 0o755); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("chmod нового бінарника: %w", err)
	}

	// Verify checksum if .sha256 asset exists (named after the raw
	// binary, so strip a tarball suffix first).
	shaAssetName := strings.TrimSuffix(asset.Name, ".tar.gz") + ".sha256"
	for i := range rel.Assets {
		if rel.Assets[i].Name == shaAssetName {
			shaResp, err := client.Get(rel.Assets[i].BrowserURL)
			if err == nil {
				defer shaResp.Body.Close()
				if shaResp.StatusCode == http.StatusOK {
					expectedBytes, _ := io.ReadAll(shaResp.Body)
					expectedRaw := strings.TrimSpace(string(expectedBytes))
					expected := ""
					if fields := strings.Fields(expectedRaw); len(fields) > 0 {
						expected = strings.ToLower(fields[0])
					}
					actual := strings.ToLower(hex.EncodeToString(hasher.Sum(nil)))
					if expected != actual {
						_ = os.Remove(tmpFile)
						return fmt.Errorf("перевірка SHA256 не пройшла: очікувався %s, отримано %s", expected, actual)
					}
					fmt.Printf("  ✓ SHA256 перевірено\n")
				}
			}
			break
		}
	}

	// Smoke-test the download before touching the running install.
	if out, err := exec.Command(tmpFile, "version").CombinedOutput(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("новий бінарник не запускається (%v): %s", err, strings.TrimSpace(string(out)))
	}

	// Keep a rollback copy of the current binary.
	backup := targetPath + ".bak"
	_ = os.Remove(backup)
	if cur, err := os.ReadFile(targetPath); err == nil {
		_ = os.WriteFile(backup, cur, 0o755)
	}

	// Was daemon running?
	pid, wasRunning := checkPidRunning(layout.PidFile())
	if wasRunning {
		fmt.Printf("→ Зупиняю фоновий процес (PID %d)...\n", pid)
		_ = cmdStop(layout)
	}

	// Replace executable
	if err := os.Rename(tmpFile, targetPath); err != nil {
		// Fallback copy for some filesystems
		data, readErr := os.ReadFile(tmpFile)
		if readErr != nil {
			restoreBackup(targetPath, backup)
			return fmt.Errorf("заміна бінарника: %w", err)
		}
		if writeErr := os.WriteFile(targetPath, data, 0o755); writeErr != nil {
			restoreBackup(targetPath, backup)
			return fmt.Errorf("заміна бінарника: %w", writeErr)
		}
		_ = os.Remove(tmpFile)
	}
	if err := os.Chmod(targetPath, 0o755); err != nil {
		restoreBackup(targetPath, backup)
		return fmt.Errorf("chmod встановленого бінарника: %w", err)
	}

	fmt.Printf("\033[1;32m✓ Бінарник успішно оновлено до %s:\033[0m %s\n", rel.TagName, targetPath)
	fmt.Printf("  резервна копія: %s (видаліть після перевірки)\n", backup)

	if wasRunning {
		fmt.Println("→ Перезапускаю фонову службу...")
		time.Sleep(500 * time.Millisecond)
		if err := cmdStart(layout); err != nil {
			fmt.Printf("⚠ Не вдалося автоматично перезапустити службу: %v\n", err)
		}
	}

	fmt.Println("\n\033[1;32m🎉 ОНОВЛЕННЯ ЗАВЕРШЕНО УСПІШНО!\033[0m")
	return nil
}

// extractTarGz streams the first regular file out of a .tar.gz archive,
// capped at maxUpdateBytes+1. It rejects absolute paths and ".." escapes so
// a crafted asset cannot write outside the temp file (we only stream bytes,
// but staying strict costs nothing).
func extractTarGz(dst io.Writer, src io.Reader) (int64, error) {
	gz, err := gzip.NewReader(io.LimitReader(src, maxUpdateBytes+1))
	if err != nil {
		return 0, fmt.Errorf("розпакування tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return 0, fmt.Errorf("tar.gz не містить файлів")
		}
		if err != nil {
			return 0, fmt.Errorf("читання tar.gz: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := strings.TrimSpace(hdr.Name)
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			continue
		}
		return io.Copy(dst, io.LimitReader(tr, maxUpdateBytes+1))
	}
}

// fallbackNameservers are used only when the system resolver fails.
// A static Go binary on Termux/Android often sees no /etc/resolv.conf
// (/etc points at /system/etc there), so the system lookup dies with
// "connection refused" on localhost while curl and git keep working.
var fallbackNameservers = []string{"8.8.8.8:53", "1.1.1.1:53"}

// updateHTTPClient builds the release-download client with a DNS fallback.
func updateHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialUpdateWithDNSFallback
	tr.TLSHandshakeTimeout = 15 * time.Second
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// dialUpdateWithDNSFallback dials normally first and only falls back to
// public DNS when the system resolver itself errors out. TLS SNI and cert
// verification are unaffected: we still dial on behalf of the original
// hostname, just with an IP we resolved ourselves.
func dialUpdateWithDNSFallback(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err == nil {
		return conn, nil
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return nil, err
	}
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, err
	}
	for _, ns := range fallbackNameservers {
		for _, proto := range []string{"udp", "tcp"} {
			resolver := &net.Resolver{
				PreferGo:     true,
				StrictErrors: true,
				Dial: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
					d := &net.Dialer{Timeout: 5 * time.Second}
					return d.DialContext(dialCtx, proto, ns)
				},
			}
			ips, rerr := resolver.LookupIPAddr(ctx, host)
			if rerr != nil || len(ips) == 0 {
				continue
			}
			for _, ip := range ips {
				if c, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port)); derr == nil {
					return c, nil
				}
			}
		}
	}
	return nil, err
}

func restoreBackup(target, backup string) {
	data, err := os.ReadFile(backup)
	if err != nil {
		return
	}
	_ = os.WriteFile(target, data, 0o755)
}
