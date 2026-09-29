package plugins

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SettingEvent is delivered to a plugin after its settings change in the
// panel. Delivery is opt-in: the plugin must subscribe to it (manifest
// events or event.subscribe), like any other event.
const SettingEvent = "settings.changed"

// SettingValue is one schema field paired with its effective value:
// the stored one, or the schema default when nothing is stored yet.
type SettingValue struct {
	Field SettingField `json:"field"`
	Value any          `json:"value,omitempty"`
	// Stored reports whether the value came from storage (false = default).
	Stored bool `json:"stored"`
}

// settingKey scopes a field key to the plugin-private settings namespace,
// the same one settings.get/settings.set use.
func settingKey(name, key string) string { return settingsNS + name + ":" + key }

// PluginSettings returns the schema of a plugin with effective values.
// A nil slice means the plugin declares no settings form.
func (h *Host) PluginSettings(name string) ([]SettingValue, error) {
	inst, ok := h.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	fields := inst.Manifest.Settings
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]SettingValue, 0, len(fields))
	for _, f := range fields {
		raw, found := h.kv.GetRaw(settingKey(name, f.Key))
		v := SettingValue{Field: f}
		if found {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				v.Value = decoded
				v.Stored = true
			}
		}
		if !v.Stored && f.Default != nil {
			v.Value = f.Default
		}
		out = append(out, v)
	}
	return out, nil
}

// SetPluginSettings validates values against the plugin's schema and stores
// them. Unknown keys are rejected; every value must match its field type.
// It returns the effective values after the write.
func (h *Host) SetPluginSettings(name string, values map[string]any) ([]SettingValue, error) {
	inst, ok := h.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	fields := inst.Manifest.Settings
	if len(fields) == 0 {
		return nil, fmt.Errorf("plugin %q declares no settings", name)
	}
	byKey := make(map[string]SettingField, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f
	}
	for key, val := range values {
		f, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("unknown setting %q for plugin %q", key, name)
		}
		raw, err := encodeSettingValue(f, val)
		if err != nil {
			return nil, fmt.Errorf("setting %q: %w", key, err)
		}
		if err := h.kv.Set(settingKey(name, key), raw); err != nil {
			return nil, fmt.Errorf("setting %q: %w", key, err)
		}
	}
	changed := make([]string, 0, len(values))
	for key := range values {
		changed = append(changed, key)
	}
	h.notifySettingsChanged(inst, changed)
	return h.PluginSettings(name)
}

// ResetPluginSettings drops stored values so schema defaults take over.
func (h *Host) ResetPluginSettings(name string) error {
	inst, ok := h.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	fields := inst.Manifest.Settings
	if len(fields) == 0 {
		return fmt.Errorf("plugin %q declares no settings", name)
	}
	for _, f := range fields {
		h.kv.Delete(settingKey(name, f.Key))
	}
	changed := make([]string, 0, len(fields))
	for _, f := range fields {
		changed = append(changed, f.Key)
	}
	h.notifySettingsChanged(inst, changed)
	return nil
}

// notifySettingsChanged delivers SettingEvent to the owning instance only.
// Unsubscribed (or stopped) plugins simply never see it; they read the
// values via settings.get on next start or next use.
func (h *Host) notifySettingsChanged(inst *Instance, keys []string) {
	if inst == nil {
		return
	}
	inst.Emit(SettingEvent, map[string]any{"plugin": inst.Name, "keys": keys})
}

// encodeSettingValue normalizes one incoming value to storable JSON.
func encodeSettingValue(f SettingField, val any) (json.RawMessage, error) {
	switch f.Type {
	case SettingText, SettingPassword:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		if len(s) > MaxSettingText {
			return nil, fmt.Errorf("too long (max %d chars)", MaxSettingText)
		}
		return json.Marshal(s)
	case SettingNumber:
		n, ok := toFloat(val)
		if !ok {
			return nil, fmt.Errorf("must be a number")
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("below minimum %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("above maximum %v", *f.Max)
		}
		return json.Marshal(n)
	case SettingBool:
		b, ok := toBool(val)
		if !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
		return json.Marshal(b)
	case SettingSelect:
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("must be one of the options")
		}
		for _, o := range f.Options {
			if o.Value == s {
				return json.Marshal(s)
			}
		}
		return nil, fmt.Errorf("%q is not an allowed option", strings.TrimSpace(s))
	default:
		return nil, fmt.Errorf("unknown field type %q", f.Type)
	}
}
