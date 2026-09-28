package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
)

func cmdUpdate(layout paths.Layout) error {
	fmt.Println("\n\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")
	fmt.Printf("  \033[1;37m🚀 АВТОМАТИЧНЕ ОНОВЛЕННЯ AURORA USERBOT\033[0m (поточна версія: \033[33m%s\033[0m)\n", buildinfo.Version)
	fmt.Println("\033[1;36m════════════════════════════════════════════════════════════════════\033[0m")

	arch := runtime.GOARCH
	var binName string
	switch arch {
	case "arm64":
		binName = "aurora-arm64"
	case "amd64":
		binName = "aurora-amd64"
	default:
		return fmt.Errorf("автоматичне оновлення не підтримується для архітектури: %s", arch)
	}

	url := fmt.Sprintf("https://raw.githubusercontent.com/Sqwid-member/Aurora-UserBot/main/prebuilt/%s", binName)

	targetPath, err := os.Executable()
	if err != nil || !strings.Contains(targetPath, "/") {
		if lp, err := sysx.LookPath("aurora"); err == nil {
			targetPath = lp
		} else {
			if paths.IsTermux() {
				targetPath = "/data/data/com.termux/files/usr/bin/aurora"
			} else {
				targetPath = os.Getenv("HOME") + "/bin/aurora"
			}
		}
	}

	fmt.Printf("→ Завантаження свіжого бінарника (\033[1;32m%s\033[0m)...\n", binName)
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("помилка мережі при завантаженні: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub повернув HTTP %d під час завантаження оновлення", resp.StatusCode)
	}

	// Write to temporary file
	tmpFile := targetPath + ".new"
	out, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("створення файлу оновлення: %w", err)
	}

	n, err := io.Copy(out, resp.Body)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("запис оновлення: %w", err)
	}

	if n < 1000000 { // less than 1MB is invalid
		_ = os.Remove(tmpFile)
		return fmt.Errorf("завантажений файл занадто малий (%d байт), оновлення скасовано", n)
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
			return fmt.Errorf("заміна бінарника: %w", err)
		}
		if writeErr := os.WriteFile(targetPath, data, 0o755); writeErr != nil {
			return fmt.Errorf("заміна бінарника: %w", writeErr)
		}
		_ = os.Remove(tmpFile)
	}
	_ = os.Chmod(targetPath, 0o755)

	fmt.Printf("\033[1;32m✓ Бінарник успішно оновлено:\033[0m %s\n", targetPath)

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
