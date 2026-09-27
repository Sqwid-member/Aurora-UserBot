// Package plugins implements Aurora's out-of-process plugin system.
//
// Every plugin is an independent executable speaking newline-delimited
// JSON-RPC over stdio. That buys three things at once:
//
//   - a plugin can be written in any language with a JSON codec;
//   - a crashing plugin takes nothing down with it;
//   - the host can enforce real process-level limits (address space, CPU,
//     file size) which is what actually keeps RAM in check on a phone.
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ManifestName is the file every plugin directory must contain.
const ManifestName = "aurora.plugin.json"

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// Manifest is the on-disk plugin descriptor.
type Manifest struct {
	// Name is the plugin identifier. Must match the directory name.
	Name string `json:"name"`
	// Version is a free-form semantic version.
	Version string `json:"version"`
	// Description is shown in the control panel.
	Description string `json:"description,omitempty"`
	// Author is shown in the control panel.
	Author string `json:"author,omitempty"`
	// License is the plugin license identifier.
	License string `json:"license,omitempty"`
	// Homepage points at the source repository.
	Homepage string `json:"homepage,omitempty"`
	// Language is a hint for the UI and installer ("go", "python", "lua"...).
	Language string `json:"language,omitempty"`
	// Tags are free-form labels used for filtering in the panel.
	Tags []string `json:"tags,omitempty"`

	// Runtime describes how to start the plugin process.
	Runtime Runtime `json:"runtime"`
	// Protocol is the plugin protocol revision. Defaults to 1.
	Protocol int `json:"protocol,omitempty"`
	// Events lists the event names the plugin wants to receive.
	// Use "*" for everything.
	Events []string `json:"events,omitempty"`
	// Commands declares chat/panel commands the plugin answers to.
	Commands []CommandSpec `json:"commands,omitempty"`
	// RPCMethods declares extra methods the host may call on the plugin.
	RPCMethods []string `json:"rpc_methods,omitempty"`
	// Permissions is the capability allowlist.
	Permissions Permissions `json:"permissions,omitempty"`
	// Limits caps the resources the plugin may consume.
	Limits Limits `json:"limits,omitempty"`
}

// Runtime describes the plugin process.
type Runtime struct {
	// Command is either a bare executable name resolved in PATH, or a path
	// relative to the plugin directory. Absolute paths and ".." are rejected.
	Command string `json:"command"`
	// Args are passed verbatim, relative paths resolve against the plugin dir.
	Args []string `json:"args,omitempty"`
	// Env adds extra environment variables on top of the host allowlist.
	Env map[string]string `json:"env,omitempty"`
	// Interpreter is an optional wrapper, e.g. "proot" with the right args.
	// Use it to gain real filesystem isolation on Android:
	//
	//   "command": "./run.sh", "wrap": "proot"
	Wrap string `json:"wrap,omitempty"`
}

// CommandSpec declares a user-facing command.
type CommandSpec struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage,omitempty"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	// InChat allows the command to be triggered by typing it in a chat.
	InChat bool `json:"in_chat,omitempty"`
}

// Permissions is the capability allowlist enforced by the host API.
type Permissions struct {
	// FS lists subdirectories of the plugin dir the plugin declared as
	// writable. Purely informational for the host (enforced by the process
	// sandbox, not by path checks) but it documents intent.
	FS []string `json:"fs,omitempty"`
	// Net enables http.request.
	Net bool `json:"net,omitempty"`
	// TG lists allowed Telegram capabilities: "send", "read", "resolve".
	TG []string `json:"tg,omitempty"`
	// Env lists extra environment variable names to pass through.
	Env []string `json:"env,omitempty"`
	// Config allows reading and writing the Aurora config.
	Config bool `json:"config,omitempty"`
}

// Limits caps plugin resource usage.
type Limits struct {
	// MemoryMB caps the address space via RLIMIT_AS.
	MemoryMB int `json:"memory_mb,omitempty"`
	// CPUSeconds caps CPU time via RLIMIT_CPU.
	CPUSeconds int `json:"cpu_seconds,omitempty"`
	// FileMB caps individual file writes via RLIMIT_FSIZE.
	FileMB int `json:"file_mb,omitempty"`
	// OutputKB caps how much stderr output the host will store per plugin.
	OutputKB int `json:"output_kb,omitempty"`
	// IdleTimeoutSec kills a plugin that produces no activity for that long.
	// 0 disables the watchdog.
	IdleTimeoutSec int `json:"idle_timeout_sec,omitempty"`
}

// Hard ceilings the host will never let a manifest exceed.
const (
	MaxMemoryMB     = 2048
	MinMemoryMB     = 64
	MaxCPUSeconds   = 3600
	MaxOutputKB     = 8192
	MaxIdleTimeout  = 86400
	MaxEventRatePer = 200 // events/second
)

// TG capability names.
const (
	CapSend    = "send"
	CapRead    = "read"
	CapResolve = "resolve"
)

// Errors returned by LoadManifest.
var (
	ErrNoManifest = errors.New("plugins: aurora.plugin.json not found")
	ErrBadName    = errors.New("plugins: invalid plugin name")
	ErrBadCommand = errors.New("plugins: invalid runtime.command")
)

// LoadManifest reads and validates the manifest in dir.
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoManifest
		}
		return nil, err
	}
	m := &Manifest{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(m); err != nil {
		return nil, fmt.Errorf("plugins: %s: %w", filepath.Join(dir, ManifestName), err)
	}
	m.Name = strings.TrimSpace(m.Name)
	if err := m.Validate(dir); err != nil {
		return nil, err
	}
	return m, nil
}

// Validate checks a manifest and clamps limits.
func (m *Manifest) Validate(dir string) error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("%w: %q", ErrBadName, m.Name)
	}
	if dir != "" {
		if base := filepath.Base(filepath.Clean(dir)); base != m.Name {
			return fmt.Errorf("plugins: directory %q must match plugin name %q", base, m.Name)
		}
	}
	if strings.TrimSpace(m.Runtime.Command) == "" {
		return fmt.Errorf("%w: empty", ErrBadCommand)
	}
	if err := checkRelative(m.Runtime.Command); err != nil {
		return fmt.Errorf("%w: %v", ErrBadCommand, err)
	}
	for _, a := range m.Runtime.Args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			continue // flags and absolute paths are the plugin author's business
		}
		if strings.Contains(a, "..") {
			return fmt.Errorf("%w: argument %q escapes the plugin directory", ErrBadCommand, a)
		}
	}
	if m.Protocol == 0 {
		m.Protocol = 1
	}
	if m.Protocol != 1 {
		return fmt.Errorf("plugins: unsupported protocol %d", m.Protocol)
	}
	m.Events = normalize(m.Events)
	m.Commands = dedupeCommands(m.Commands)
	m.RPCMethods = dedupeStrings(m.RPCMethods)
	m.Limits.clamp()
	m.Permissions.TG = normalize(m.Permissions.TG)
	return nil
}

func (l *Limits) clamp() {
	// Below this a Go/Python/Node plugin cannot even reach its first line of
	// user code, so treat anything smaller as "unspecified" and let the host
	// default apply instead of killing the process instantly.
	if l.MemoryMB < MinMemoryMB {
		l.MemoryMB = 0
	}
	if l.MemoryMB > MaxMemoryMB {
		l.MemoryMB = MaxMemoryMB
	}
	if l.CPUSeconds < 0 || l.CPUSeconds > MaxCPUSeconds {
		l.CPUSeconds = 0
	}
	if l.FileMB < 0 {
		l.FileMB = 0
	}
	if l.FileMB > 1024 {
		l.FileMB = 1024
	}
	if l.OutputKB < 0 {
		l.OutputKB = 0
	}
	if l.OutputKB > MaxOutputKB {
		l.OutputKB = MaxOutputKB
	}
	if l.IdleTimeoutSec < 0 || l.IdleTimeoutSec > MaxIdleTimeout {
		l.IdleTimeoutSec = 0
	}
}

// HasEvent reports whether the plugin subscribed to an event name.
func (m *Manifest) HasEvent(name string) bool {
	for _, e := range m.Events {
		if e == "*" || e == name {
			return true
		}
	}
	return false
}

// HasCapability reports whether the plugin may use a Telegram capability.
func (m *Manifest) HasCapability(cap string) bool {
	for _, c := range m.Permissions.TG {
		if c == "*" || c == cap {
			return true
		}
	}
	return false
}

// Command looks a command spec up by name or alias.
func (m *Manifest) Command(name string) (CommandSpec, bool) {
	for _, c := range m.Commands {
		if c.Name == name {
			return c, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c, true
			}
		}
	}
	return CommandSpec{}, false
}

// checkRelative rejects absolute paths and parent traversal.
func checkRelative(p string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("absolute path %q is not allowed", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("path %q escapes the plugin directory", p)
	}
	return nil
}

func normalize(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func dedupeStrings(in []string) []string { return normalize(in) }

func dedupeCommands(in []CommandSpec) []CommandSpec {
	seen := map[string]bool{}
	out := make([]CommandSpec, 0, len(in))
	for _, c := range in {
		c.Name = strings.TrimSpace(strings.ToLower(c.Name))
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		c.Aliases = normalize(c.Aliases)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
