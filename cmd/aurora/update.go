package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
)

const maxUpdateBytes = 100 << 20 // 100 MiB: prebuilt is ~18 MiB, anything more is an attack.

var updateSpinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

type releaseAsset struct {
	Name       string `json:"name"`
	BrowserURL string `json:"browser_download_url"`
	Size       int64  `json:"size"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type progressWriter struct {
	dst        io.Writer
	hasher     hash.Hash
	total      int64
	downloaded int64
	startTime  time.Time
	lastPrint  time.Time
	frame      int
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.dst.Write(p)
	if n > 0 {
		pw.downloaded += int64(n)
		if pw.hasher != nil {
			pw.hasher.Write(p[:n])
		}
		now := time.Now()
		if now.Sub(pw.lastPrint) >= 100*time.Millisecond {
			pw.lastPrint = now
			pw.frame++
			pw.printProgress()
		}
	}
	return n, err
}

func (pw *progressWriter) printProgress() {
	elapsed := time.Since(pw.startTime).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	speed := float64(pw.downloaded) / elapsed / (1024 * 1024)
	currMB := float64(pw.downloaded) / (1024 * 1024)
	spinner := updateSpinnerFrames[pw.frame%len(updateSpinnerFrames)]

	if pw.total > 0 {
		totalMB := float64(pw.total) / (1024 * 1024)
		pct := float64(pw.downloaded) / float64(pw.total) * 100
		if pct > 100 {
			pct = 100
		}
		barWidth := 20
		filled := int(float64(barWidth) * float64(pw.downloaded) / float64(pw.total))
		if filled > barWidth {
			filled = barWidth
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		fmt.Printf("\r  \033[36m%c\033[0m Завантаження: %5.1f / %5.1f MB [%s] %3.0f%% (%4.1f MB/s)  ",
			spinner, currMB, totalMB, bar, pct, speed)
	} else {
		fmt.Printf("\r  \033[36m%c\033[0m Завантаження: %5.1f MB (%4.1f MB/s)  ",
			spinner, currMB, speed)
	}
}

func (pw *progressWriter) finish() {
	elapsed := time.Since(pw.startTime).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}
	speed := float64(pw.downloaded) / elapsed / (1024 * 1024)
	currMB := float64(pw.downloaded) / (1024 * 1024)
	fmt.Printf("\r  \033[32m✓\033[0m Завантажено %5.1f MB за %.1fс (%4.1f MB/s)                    \n",
		currMB, elapsed, speed)
}

func cmdUpdate(layout paths.Layout, args ...string) error {
	fmt.Println("\n\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")
	fmt.Printf("  \033[1;37m🚀 АВТОМАТИЧНЕ ОНОВЛЕННЯ AURORA USERBOT\033[0m (поточна версія: \033[33m%s\033[0m)\n", buildinfo.Version)
	fmt.Println("\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")

	force := false
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
			break
		}
	}
	for _, a := range os.Args[1:] {
		if a == "--force" || a == "-f" {
			force = true
			break
		}
	}

	opsys := runtime.GOOS
	arch := runtime.GOARCH

	var candidates []string
	switch opsys + "/" + arch {
	case "android/arm64", "linux/arm64":
		candidates = []string{
			"aurora-arm64",
			"aurora-android-arm64.tar.gz",
			"aurora-linux-arm64.tar.gz",
			"aurora-arm64.tar.gz",
			"aurora-linux-arm64",
		}
	case "linux/amd64":
		candidates = []string{
			"aurora-amd64",
			"aurora-linux-amd64",
			"aurora-amd64.tar.gz",
			"aurora-linux-amd64.tar.gz",
		}
	case "darwin/arm64":
		candidates = []string{
			"aurora-darwin-arm64",
			"aurora-arm64",
		}
	case "darwin/amd64":
		candidates = []string{
			"aurora-darwin-amd64",
			"aurora-amd64",
		}
	case "linux/arm":
		candidates = []string{
			"aurora-linux-arm.tar.gz",
			"aurora-linux-arm",
			"aurora-arm.tar.gz",
			"aurora-arm",
		}
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
	if resolved, err := filepath.EvalSymlinks(targetPath); err == nil && resolved != "" {
		targetPath = resolved
	}

	fmt.Printf("→ Отримання інформації про останній реліз...\n")
	client := updateHTTPClient()

	rel, err := fetchReleaseInfo(client)
	if err != nil {
		return fmt.Errorf("отримання релізу: %w", err)
	}

	if !force && buildinfo.Version != "dev" && rel.TagName == buildinfo.Version {
		fmt.Printf("\n\033[1;32m✓ У вас вже встановлено останню версію Aurora (%s).\033[0m\n", rel.TagName)
		fmt.Println("  Оновлення не потрібне. Для примусового перевстановлення виконайте:")
		fmt.Println("  \033[36maurora update --force\033[0m")
		return nil
	}

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

	fmt.Printf("→ Завантаження %s з релізу %s...\n", asset.Name, rel.TagName)
	resp, err := client.Get(asset.BrowserURL)
	if err != nil && isCertificateError(err) {
		insecureTr := http.DefaultTransport.(*http.Transport).Clone()
		insecureTr.DialContext = dialUpdateWithDNSFallback
		insecureTr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client = &http.Client{Timeout: 90 * time.Second, Transport: insecureTr}
		resp, err = client.Get(asset.BrowserURL)
	}
	if err != nil {
		return fmt.Errorf("помилка мережі при завантаженні: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервер повернув HTTP %d при завантаженні бінарника", resp.StatusCode)
	}

	tmpFile := targetPath + ".new"
	_ = os.Remove(tmpFile)
	out, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("створення файлу оновлення: %w", err)
	}

	hasher := sha256.New()
	pw := &progressWriter{
		dst:       out,
		hasher:    hasher,
		total:     resp.ContentLength,
		startTime: time.Now(),
		lastPrint: time.Now(),
	}

	var n int64
	if strings.HasSuffix(asset.Name, ".tar.gz") {
		// When unpacking tar.gz, hash the extracted binary stream
		n, err = extractTarGz(pw, resp.Body)
	} else {
		n, err = io.Copy(pw, io.LimitReader(resp.Body, maxUpdateBytes+1))
	}
	_ = out.Close()
	pw.finish()

	if err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("запис оновлення: %w", err)
	}
	if n > maxUpdateBytes {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("завантаження завелике (%d байт), оновлення скасовано", n)
	}
	if n < 1000000 {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("завантажений файл занадто малий (%d байт), оновлення скасовано", n)
	}
	_ = os.Chmod(tmpFile, 0o755)

	// Verify checksum if .sha256 exists
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
					if expected != "" && expected != actual {
						_ = os.Remove(tmpFile)
						return fmt.Errorf("перевірка SHA256 не пройшла: очікувався %s, отримано %s", expected, actual)
					}
					fmt.Printf("  \033[32m✓\033[0m Контрольну суму SHA256 перевірено\n")
				}
			}
			break
		}
	}

	// Smoke test downloaded binary
	if out, err := sysx.Command(tmpFile, "version").CombinedOutput(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("новий бінарник не запускається (%v): %s", err, strings.TrimSpace(string(out)))
	}

	backup := targetPath + ".bak"
	_ = os.Remove(backup)
	if cur, err := os.ReadFile(targetPath); err == nil {
		_ = os.WriteFile(backup, cur, 0o755)
	}

	pid, wasRunning := checkPidRunning(layout.PidFile())
	if wasRunning {
		fmt.Printf("→ Зупиняю фоновий процес (PID %d)...\n", pid)
		_ = cmdStop(layout)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if !isPidAlive(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Unlink before replace to prevent Linux "Text file busy"
	_ = os.Remove(targetPath)
	if err := os.Rename(tmpFile, targetPath); err != nil {
		data, readErr := os.ReadFile(tmpFile)
		if readErr != nil {
			restoreBackup(targetPath, backup)
			return fmt.Errorf("читання оновленого файлу: %w", readErr)
		}
		if writeErr := os.WriteFile(targetPath, data, 0o755); writeErr != nil {
			restoreBackup(targetPath, backup)
			return fmt.Errorf("заміна бінарника: %w", writeErr)
		}
		_ = os.Remove(tmpFile)
	}
	_ = os.Chmod(targetPath, 0o755)

	fmt.Printf("\n\033[1;32m✓ Бінарник успішно оновлено до %s:\033[0m %s\n", rel.TagName, targetPath)

	if wasRunning {
		fmt.Println("→ Перезапускаю фонову службу...")
		time.Sleep(300 * time.Millisecond)
		if err := cmdStart(layout); err != nil {
			fmt.Printf("⚠ Не вдалося автоматично перезапустити службу: %v\n", err)
		} else {
			if newPid, ok := checkPidRunning(layout.PidFile()); ok {
				fmt.Printf("  \033[32m✓\033[0m Службу запущено (PID %d)\n", newPid)
			}
		}
	}

	fmt.Println("\n\033[1;32m🎉 ОНОВЛЕННЯ ЗАВЕРШЕНО УСПІШНО!\033[0m")
	return nil
}

func fetchReleaseInfo(client *http.Client) (*release, error) {
	apiURL := "https://api.github.com/repos/Sqwid-member/Aurora-UserBot/releases/latest"
	resp, err := client.Get(apiURL)
	if err != nil && isCertificateError(err) {
		insecureTr := http.DefaultTransport.(*http.Transport).Clone()
		insecureTr.DialContext = dialUpdateWithDNSFallback
		insecureTr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client = &http.Client{Timeout: 30 * time.Second, Transport: insecureTr}
		resp, err = client.Get(apiURL)
	}

	// If API succeeded and returned 200, decode JSON
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var rel release
		if err := json.NewDecoder(resp.Body).Decode(&rel); err == nil && rel.TagName != "" {
			return &rel, nil
		}
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	// Fallback: Resolve latest tag via github.com redirect (bypasses GitHub API rate limits)
	webURL := "https://github.com/Sqwid-member/Aurora-UserBot/releases/latest"
	noRedirectClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: dialUpdateWithDNSFallback,
			TLSClientConfig: &tls.Config{
				RootCAs:            sysx.RootCertPool(),
				InsecureSkipVerify: true,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	webResp, webErr := noRedirectClient.Get(webURL)
	if webErr == nil {
		defer webResp.Body.Close()
		loc := webResp.Header.Get("Location")
		if loc != "" {
			tag := filepath.Base(loc)
			if strings.HasPrefix(tag, "v") {
				// Synthesize release with direct download URLs
				rel := &release{
					TagName: tag,
				}
				knownAssets := []string{
					"aurora-arm64", "aurora-arm64.tar.gz",
					"aurora-android-arm64.tar.gz", "aurora-linux-arm64.tar.gz",
					"aurora-linux-arm64", "aurora-amd64", "aurora-linux-amd64",
					"aurora-amd64.tar.gz", "aurora-linux-amd64.tar.gz",
					"aurora-darwin-arm64", "aurora-darwin-amd64",
					"aurora-linux-arm.tar.gz", "aurora-linux-arm",
					"aurora-arm64.sha256", "aurora-amd64.sha256",
				}
				for _, name := range knownAssets {
					rel.Assets = append(rel.Assets, releaseAsset{
						Name:       name,
						BrowserURL: fmt.Sprintf("https://github.com/Sqwid-member/Aurora-UserBot/releases/download/%s/%s", tag, name),
					})
				}
				return rel, nil
			}
		}
	}

	return nil, fmt.Errorf("не вдалося отримати дані релізу з GitHub API або web")
}

func extractTarGz(dst io.Writer, src io.Reader) (int64, error) {
	gz, err := gzip.NewReader(io.LimitReader(src, maxUpdateBytes+1))
	if err != nil {
		return 0, fmt.Errorf("розпакування tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("архів не містить виконуваного файлу")
		}
		if err != nil {
			return 0, fmt.Errorf("читання tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") {
			continue
		}
		return io.Copy(dst, io.LimitReader(tr, maxUpdateBytes+1))
	}
}

var fallbackNameservers = []string{"8.8.8.8:53", "1.1.1.1:53"}

func updateHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialUpdateWithDNSFallback
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.TLSClientConfig = &tls.Config{
		RootCAs: sysx.RootCertPool(),
	}
	return &http.Client{Timeout: 90 * time.Second, Transport: tr}
}

func isCertificateError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "certificate") || strings.Contains(s, "x509") || strings.Contains(s, "unknown authority")
}

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
	_ = os.Remove(target)
	_ = os.WriteFile(target, data, 0o755)
}
