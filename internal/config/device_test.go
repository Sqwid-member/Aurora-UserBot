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
