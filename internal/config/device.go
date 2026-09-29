package config

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/sysx"
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
	switch {
	case runtime.GOOS == "android" || isTermuxEnv():
		props := getProps(map[string]string{
			"manufacturer": "ro.product.manufacturer",
			"model":        "ro.product.model",
			"release":      "ro.build.version.release",
		})
		d.Model = joinModel(props["manufacturer"], props["model"])
		d.System = joinSystem(props["release"])
	case runtime.GOOS == "linux":
		d.Model, d.System = detectLinuxDevice()
	}
	d.Language = systemLanguage()
	return d
}

// dmiBase and osReleasePath are variables (not constants) so tests can
// point them at fixture files.
var (
	dmiBase       = "/sys/devices/virtual/dmi/id"
	osReleasePath = "/etc/os-release"
)

// detectLinuxDevice identifies a Linux box from DMI data and os-release:
// model like "Dell Inc. XPS 15 9520", system like "Ubuntu 24.04.1 LTS".
// Containers usually lack DMI — then the hostname (or arch) is used, so
// the result is still stable per machine instead of a shared placeholder.
func detectLinuxDevice() (model, system string) {
	model = joinModel(
		readFirstLine(dmiBase+"/sys_vendor"),
		readFirstLine(dmiBase+"/product_name"),
	)
	if model == "" {
		if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" && h != "localhost" {
			model = strings.TrimSpace(h)
		} else {
			model = "Linux " + runtime.GOARCH
		}
	}
	system = parseOSRelease(readFileLimited(osReleasePath, 8192))
	return model, system
}

// parseOSRelease extracts a display name from os-release content:
// PRETTY_NAME wins, otherwise NAME + VERSION_ID.
func parseOSRelease(content string) string {
	var name, version, pretty string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "PRETTY_NAME":
			pretty = val
		case "NAME":
			name = val
		case "VERSION_ID":
			version = val
		}
	}
	if pretty != "" {
		return pretty
	}
	if name == "" {
		return ""
	}
	if version != "" {
		return name + " " + version
	}
	return name
}

// readFirstLine returns the trimmed first line of a sysfs-style file.
func readFirstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}

// readFileLimited reads a small config file, tolerating absence.
func readFileLimited(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	return string(buf[:n])
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

// getProps reads several Android system properties via getprop.
// It must never use the standard exec.LookPath/exec.Command: those call
// faccessat2, which Android's seccomp answers with SIGSYS — instant death,
// no error to handle. sysx wrappers stat instead.
func getProps(names map[string]string) map[string]string {
	out := map[string]string{}
	path, err := sysx.LookPath("getprop")
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
		cmd := sysx.CommandContext(ctx, path, prop)
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
	c.SnapshotDevice(DetectDevice())
}

// SnapshotDevice merges a detected DeviceInfo into the config, filling only
// empty fields. It reports which config keys changed, so installers and CLI
// can tell the user what was snapshotted. Explicit values are never touched.
func (c *Config) SnapshotDevice(d DeviceInfo) []string {
	var changed []string
	if isDevicePlaceholder("device_model", c.Telegram.DeviceModel) && d.Model != "" {
		c.Telegram.DeviceModel = d.Model
		changed = append(changed, "device_model")
	}
	if isDevicePlaceholder("device_system", c.Telegram.DeviceSystem) && d.System != "" {
		c.Telegram.DeviceSystem = d.System
		changed = append(changed, "device_system")
	}
	if isDevicePlaceholder("device_language", c.Telegram.DeviceLanguage) && d.Language != "" {
		c.Telegram.DeviceLanguage = d.Language
		changed = append(changed, "device_language")
	}
	return changed
}

// isDevicePlaceholder reports values that carry no real identity: empties
// and the generic "Android" placeholder older installs defaulted to. No
// genuine phone reports its model or OS as bare "Android", so treating it
// as empty lets the real hardware take over on re-snapshot (and at runtime).
func isDevicePlaceholder(field, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	switch field {
	case "device_model", "device_system":
		return v == "" || v == "android"
	case "device_language":
		// "en" was the old unconditional default; a genuine explicit
		// choice survives because re-snapshot with the same real
		// language is a no-op change.
		return v == "" || v == "en"
	default:
		return v == ""
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
