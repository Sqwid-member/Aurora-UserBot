package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
)

// backupVersion is the bundle format revision.
const backupVersion = 1

// backupBundle is a portable snapshot for moving between phones: config
// (with API keys) plus session blobs and the KV database holding plugin
// settings. It is equivalent to full account access — keep it private.
type backupBundle struct {
	Version int               `json:"version"`
	Created string            `json:"created"`
	Core    string            `json:"core"`
	Files   map[string]string `json:"files"`
}

// backupSources lists layout-relative files to snapshot when present.
func backupSources(layout paths.Layout) map[string]string {
	return map[string]string{
		"config": layout.ConfigFile(),
		"kv":     layout.DBFile(),
	}
}

// createBackup writes a bundle of config + sessions + KV to dest.
func createBackup(layout paths.Layout, dest string) error {
	if err := layout.Ensure(); err != nil {
		return err
	}
	files := map[string]string{}
	addFile := func(rel, abs string) error {
		raw, err := os.ReadFile(abs)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("читання %s: %w", rel, err)
		}
		files[rel] = base64.StdEncoding.EncodeToString(raw)
		return nil
	}
	for rel, abs := range backupSources(layout) {
		if err := addFile(rel, abs); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(layout.Data)
	if err != nil {
		return fmt.Errorf("читання каталогу сесій: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "session") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if err := addFile("data/"+name, filepath.Join(layout.Data, name)); err != nil {
			return err
		}
	}
	if len(files) == 0 {
		return errors.New("немає чого зберігати: ні конфігу, ні сесій")
	}
	bundle := backupBundle{
		Version: backupVersion,
		Created: time.Now().UTC().Format(time.RFC3339),
		Core:    buildinfo.Version,
		Files:   files,
	}
	buf, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, buf, 0o600); err != nil {
		return fmt.Errorf("запис бекапа: %w", err)
	}
	return nil
}

// restoreBundleFiles writes bundle files back into the layout, keeping .bak
// copies of anything it overwrites.
func restoreBundleFiles(layout paths.Layout, src string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("читання бекапа: %w", err)
	}
	var bundle backupBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("бекап пошкоджено: %w", err)
	}
	if bundle.Version != backupVersion {
		return fmt.Errorf("невідома версія бекапа %d", bundle.Version)
	}
	if len(bundle.Files) == 0 {
		return errors.New("бекап порожній")
	}
	targets := map[string]string{
		"config": layout.ConfigFile(),
		"kv":     layout.DBFile(),
	}
	if err := layout.Ensure(); err != nil {
		return err
	}
	restored := 0
	for rel, b64 := range bundle.Files {
		abs, ok := targets[rel]
		if !ok {
			if name, ok := strings.CutPrefix(rel, "data/"); ok && strings.HasPrefix(name, "session") && strings.HasSuffix(name, ".json") && !strings.Contains(name, "/") {
				abs = filepath.Join(layout.Data, name)
			} else {
				return fmt.Errorf("бекап містить невідомий файл %q", rel)
			}
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("файл %q пошкоджено: %w", rel, err)
		}
		if cur, err := os.ReadFile(abs); err == nil {
			_ = os.WriteFile(abs+".bak", cur, 0o600)
		}
		if err := os.WriteFile(abs, data, 0o600); err != nil {
			return fmt.Errorf("запис %s: %w", rel, err)
		}
		restored++
	}
	fmt.Printf("✓ відновлено файлів: %d (попередні збережено як .bak)\n", restored)
	return nil
}

// cmdBackup creates a bundle; cmdRestore applies one. Restore refuses while
// the daemon runs so live state is never clobbered mid-flight.
func cmdBackup(layout paths.Layout, args []string) error {
	dest := fmt.Sprintf("aurora-backup-%s.json", time.Now().Format("20060102-150405"))
	if len(args) > 0 {
		dest = args[0]
	}
	if err := createBackup(layout, dest); err != nil {
		return err
	}
	fi, _ := os.Stat(dest)
	fmt.Printf("✓ бекап збережено: %s", dest)
	if fi != nil {
		fmt.Printf(" (%d байт)", fi.Size())
	}
	fmt.Println("\n  ⚠ Це повний доступ до акаунтів — нікому не пересилайте файл.")
	return nil
}

func cmdRestore(layout paths.Layout, args []string) error {
	if len(args) == 0 {
		return errors.New("використання: aurora restore <файл-бекапа>")
	}
	if pid, running := checkPidRunning(layout.PidFile()); running {
		return fmt.Errorf("ядро працює (PID %d) — зупиніть його (aurora stop) перед відновленням", pid)
	}
	if err := restoreBundleFiles(layout, args[0]); err != nil {
		return err
	}
	fmt.Println("  Перезапустіть ядро: aurora start")
	return nil
}
