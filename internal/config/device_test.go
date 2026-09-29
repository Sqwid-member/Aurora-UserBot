package config

import "testing"

func TestParseLang(t *testing.T) {
	cases := map[string]string{
		"uk_UA.UTF-8": "uk",
		"en_US":       "en",
		"ru-RU":       "ru",
		"de@euro":     "de",
		"uk":          "uk",
		"":            "",
		"C":           "",
		"POSIX":       "",
		"12":          "",
		"toolongcode": "",
		"e n":         "",
	}
	for in, want := range cases {
		if got := parseLang(in); got != want {
			t.Errorf("parseLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinModel(t *testing.T) {
	cases := []struct{ brand, model, want string }{
		{"Xiaomi", "2602EPTC0G", "Xiaomi 2602EPTC0G"},
		{"Xiaomi", "Xiaomi 13", "Xiaomi 13"},
		{"", "Pixel 8", "Pixel 8"},
		{"Xiaomi", "", "Xiaomi"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := joinModel(c.brand, c.model); got != c.want {
			t.Errorf("joinModel(%q, %q) = %q, want %q", c.brand, c.model, got, c.want)
		}
	}
}

func TestJoinSystem(t *testing.T) {
	cases := map[string]string{
		"16":         "Android 16",
		"14":         "Android 14",
		"Android 14": "Android 14",
		"":           "",
	}
	for in, want := range cases {
		if got := joinSystem(in); got != want {
			t.Errorf("joinSystem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyDetectedDeviceKeepsExplicit(t *testing.T) {
	var c Config
	c.Telegram.DeviceModel = "Мій Пристрій"
	c.ApplyDetectedDevice()
	if c.Telegram.DeviceModel != "Мій Пристрій" {
		t.Fatal("explicit device model was overwritten")
	}
	// Must not crash when nothing is set; worst case fields stay empty.
	var empty Config
	empty.ApplyDetectedDevice()
}

func TestDetectDeviceNoCrash(t *testing.T) {
	_ = DetectDevice()
	_ = DetectedDeviceSummary()
}

func TestSnapshotDeviceFillsOnlyEmpty(t *testing.T) {
	var c Config
	changed := c.SnapshotDevice(DeviceInfo{Model: "Xiaomi 2602EPTC0G", System: "Android 16", Language: "uk"})
	if len(changed) != 3 {
		t.Fatalf("changed = %v, want 3 fields", changed)
	}
	if c.Telegram.DeviceModel != "Xiaomi 2602EPTC0G" || c.Telegram.DeviceSystem != "Android 16" || c.Telegram.DeviceLanguage != "uk" {
		t.Fatalf("fields not filled: %+v", c.Telegram)
	}

	// Second snapshot with different data must not overwrite.
	again := c.SnapshotDevice(DeviceInfo{Model: "Other", System: "Android 1", Language: "en"})
	if len(again) != 0 {
		t.Fatalf("second snapshot changed %v, want none", again)
	}
	if c.Telegram.DeviceModel != "Xiaomi 2602EPTC0G" {
		t.Fatal("explicit value was overwritten")
	}
}

func TestSnapshotDevicePartial(t *testing.T) {
	var c Config
	c.Telegram.DeviceModel = "Custom"
	changed := c.SnapshotDevice(DeviceInfo{Model: "Phone", System: "Android 16", Language: ""})
	if len(changed) != 1 || changed[0] != "device_system" {
		t.Fatalf("changed = %v, want [device_system]", changed)
	}
}

func TestSnapshotDeviceReplacesOldPlaceholders(t *testing.T) {
	var c Config
	c.Telegram.DeviceModel = "Android"
	c.Telegram.DeviceSystem = "android"
	c.Telegram.DeviceLanguage = "en"
	changed := c.SnapshotDevice(DeviceInfo{Model: "Xiaomi 2602EPTC0G", System: "Android 16", Language: "uk"})
	if len(changed) != 3 {
		t.Fatalf("changed = %v, want all 3 fields", changed)
	}
	if c.Telegram.DeviceModel != "Xiaomi 2602EPTC0G" || c.Telegram.DeviceSystem != "Android 16" || c.Telegram.DeviceLanguage != "uk" {
		t.Fatalf("placeholders not replaced: %+v", c.Telegram)
	}
}

func TestDefaultDeviceFieldsEmpty(t *testing.T) {
	d := Default()
	if d.Telegram.DeviceModel != "" || d.Telegram.DeviceSystem != "" || d.Telegram.DeviceLanguage != "" {
		t.Fatalf("defaults must leave device identity empty, got %+v", d.Telegram)
	}
	if d.Telegram.DeviceName == "" {
		t.Fatal("device name must keep an app identity")
	}
}
