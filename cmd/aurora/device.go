package main

import (
	"fmt"
	"strings"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/paths"
)

// cmdDevice reports the phone identity Aurora presents to Telegram and,
// with --save, snapshots it into config.json. The installer runs
// "aurora device --save" right after placing the binary, so every phone
// carries its own real model/Android instead of a generic placeholder.
// Explicit values are never overwritten, and an empty detection is a
// no-op (the runtime falls back to built-in defaults).
func cmdDevice(layout paths.Layout, args []string) error {
	save := false
	for _, a := range args {
		if a == "--save" || a == "-s" || a == "save" {
			save = true
		}
	}

	if err := layout.Ensure(); err != nil {
		return err
	}
	store, err := config.Open(layout.ConfigFile(), nil)
	if err != nil {
		return err
	}

	detected := config.DetectDevice()
	if !save {
		c := store.Get()
		fmt.Println("🌌 Пристрій, який Telegram бачить при вході:")
		fmt.Printf("   модель:   %s\n", effective(c.Telegram.DeviceModel, detected.Model))
		fmt.Printf("   система:  %s\n", effective(c.Telegram.DeviceSystem, detected.System))
		fmt.Printf("   мова:     %s\n", effective(c.Telegram.DeviceLanguage, detected.Language))
		fmt.Printf("   виявлено: %s\n", config.DetectedDeviceSummary())
		fmt.Println("   (свої значення в конфігу завжди перемагають; 'aurora device --save' записує виявлені)")
		return nil
	}

	var changed []string
	if err := store.Update(func(c *config.Config) {
		changed = c.SnapshotDevice(detected)
	}); err != nil {
		return fmt.Errorf("збереження зліпка пристрою: %w", err)
	}
	if len(changed) == 0 {
		fmt.Println("✓ Зліпок пристрою: нічого нового (поля вже заповнені або пристрій не визначено)")
		return nil
	}
	fmt.Printf("✓ Зліпок пристрою збережено (%s): %s\n", strings.Join(changed, ", "), store.Path())
	return nil
}

func effective(configured, detected string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	if strings.TrimSpace(detected) != "" {
		return detected + " (авто)"
	}
	return "за замовчуванням"
}
