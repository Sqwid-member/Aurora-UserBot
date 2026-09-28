package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	var binName string
	switch opsys + "/" + arch {
	case "android/arm64", "linux/arm64", "darwin/arm64":
		binName = "aurora-arm64"
	case "linux/amd64", "darwin/amd64":
		binName = "aurora-amd64"
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
	client := &http.Client{Timeout: 30 * time.Second}

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

	// Find asset for our platform
	var asset *releaseAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == binName || rel.Assets[i].Name == binName+".tar.gz" || rel.Assets[i].Name == binName+".sha256" {
			asset = &rel.Assets[i]
			break
		}
	}
	if asset == nil {
		return fmt.Errorf("реліз %s не містить бінарника для %s", rel.TagName, binName)
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

	// Download to temp file with size limit
	tmpFile := targetPath + ".new"
	out, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("створення файлу оновлення: %w", err)
	}

	// Compute SHA256 while downloading
	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)
	n, err := io.Copy(mw, io.LimitReader(resp.Body, maxUpdateBytes+1))
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

	// Verify checksum if .sha256 asset exists
	shaAssetName := binName + ".sha256"
	for i := range rel.Assets {
		if rel.Assets[i].Name == shaAssetName {
			shaResp, err := client.Get(rel.Assets[i].BrowserURL)
			if err == nil {
				defer shaResp.Body.Close()
				if shaResp.StatusCode == http.StatusOK {
					expectedBytes, _ := io.ReadAll(shaResp.Body)
					expected := strings.TrimSpace(string(expectedBytes))
					actual := hex.EncodeToString(hasher.Sum(nil))
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

func restoreBackup(target, backup string) {
	data, err := os.ReadFile(backup)
	if err != nil {
		return
	}
	_ = os.WriteFile(target, data, 0o755)
}
