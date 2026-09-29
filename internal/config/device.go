package config

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DeviceInfo describes the physical phone Aurora runs on. It is used to
// identify this installation in Telegram's Settings → Devices instead of a
// generic placeholder: the login request then looks like it comes from the
// user's real phone, which is both more honest and less likely to be
// throttled than a fake default.
type DeviceInfo struct {
	// Model is e.g. "Xiaomi 2602EPTC0G".
	Model string
	// System is e.g. "Android 16".
	System string
	// Language is a BCP-47-ish code Telegram expects, e.g. "uk".
	Language string
}

var (
	deviceOnce sync.Once
	deviceMemo DeviceInfo
)

// DetectDevice reads the real phone data (Android system properties via
// getprop plus $LANG). It never fails: on non-Android systems or without
// getprop it returns whatever could be determined, possibly empty. The
// result is memoized per process.
func DetectDevice() DeviceInfo {
	deviceOnce.Do(func() {
		deviceMemo = detectDevice()
	})
	return deviceMemo
}

func detectDevice() DeviceInfo {
	var d DeviceInfo
	if runtime.GOOS == "android" || isTermuxEnv() {
		props := getProps(map[string]string{
			"manufacturer": "ro.product.manufacturer",
			"model":        "ro.product.model",
			"release":      "ro.build.version.release",
		})
		d.Model = joinModel(props["manufacturer"], props["model"])
		d.System = joinSystem(props["release"])
	}
	d.Language = systemLanguage()
	return d
}

// joinModel folds manufacturer and model into one display string without
// duplicating the brand when the model already carries it.
func joinModel(manufacturer, model string) string {
	manufacturer, model = strings.TrimSpace(manufacturer), strings.TrimSpace(model)
	if model == "" {
		return manufacturer
	}
	if manufacturer == "" || strings.HasPrefix(strings.ToLower(model), strings.ToLower(manufacturer)) {
		return model
	}
	return manufacturer + " " + model
}

// joinSystem renders the Android release as Telegram shows it.
func joinSystem(release string) string {
	release = strings.TrimSpace(release)
	if release == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(release), "android") {
		return release
	}
	return "Android " + release
}

// systemLanguage derives Telegram's language code from $LANG/LANGUAGE,
// e.g. "uk_UA.UTF-8" -> "uk". Empty when nothing usable is set.
func systemLanguage() string {
	for _, env := range []string{"LANGUAGE", "LANG"} {
		if code := parseLang(os.Getenv(env)); code != "" {
			return code
		}
	}
	return ""
}

// parseLang keeps the primary subtag of a locale string.
func parseLang(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || locale == "C" || locale == "POSIX" {
		return ""
	}
	// Strip encoding ("uk_UA.UTF-8") and modifier ("ca_ES@valencia").
	if i := strings.IndexAny(locale, ".@"); i >= 0 {
		locale = locale[:i]
	}
	// Keep "uk" out of "uk_UA" / "uk-UA".
	if i := strings.IndexAny(locale, "_-"); i >= 0 {
		locale = locale[:i]
	}
	locale = strings.ToLower(strings.TrimSpace(locale))
	if len(locale) < 2 || len(locale) > 3 {
		return ""
	}
	for _, r := range locale {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return locale
}

// getProps reads several Android system properties in one getprop call.
func getProps(names map[string]string) map[string]string {
	out := map[string]string{}
	path, err := exec.LookPath("getprop")
	if err != nil {
		if _, serr := os.Stat("/system/bin/getprop"); serr != nil {
			return out
		}
		path = "/system/bin/getprop"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// One process for all keys: "getprop" with no args dumps everything,
	// but parsing that is fragile across vendors, so query per key.
	for key, prop := range names {
		cmd := exec.CommandContext(ctx, path, prop)
		raw, err := cmd.Output()
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(raw)); v != "" {
			out[key] = v
		}
	}
	return out
}

// isTermuxEnv reports whether we run inside Termux on Android.
func isTermuxEnv() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return strings.HasPrefix(os.Getenv("PREFIX"), "/data/data/com.termux/")
}

// ApplyDetectedDevice fills empty device fields from the real phone.
// Explicit user values always win; app version is never touched here.
func (c *Config) ApplyDetectedDevice() {
	d := DetectDevice()
	if strings.TrimSpace(c.Telegram.DeviceModel) == "" && d.Model != "" {
		c.Telegram.DeviceModel = d.Model
	}
	if strings.TrimSpace(c.Telegram.DeviceSystem) == "" && d.System != "" {
		c.Telegram.DeviceSystem = d.System
	}
	if strings.TrimSpace(c.Telegram.DeviceLanguage) == "" && d.Language != "" {
		c.Telegram.DeviceLanguage = d.Language
	}
}

// DetectedDeviceSummary renders one line for setup/doctor output.
func DetectedDeviceSummary() string {
	d := DetectDevice()
	if d.Model == "" && d.System == "" {
		return "не визначено (не Android або немає getprop)"
	}
	s := d.Model
	if d.System != "" {
		if s != "" {
			s += ", "
		}
		s += d.System
	}
	if d.Language != "" {
		s += " [" + d.Language + "]"
	}
	return s
}
