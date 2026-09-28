// Package paths resolves the on-disk layout of an Aurora installation.
//
// Everything is Termux-aware: on Termux the XDG variables usually point
// somewhere inside the app sandbox, so we fall back to $HOME/.local/share.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Layout holds every directory Aurora works with.
type Layout struct {
	// Home is the data root, e.g. $HOME/.local/share/aurora.
	Home string
	// Config is the directory holding config.json and aurora.db.
	Config string
	// Data is the directory for mutable state (sessions, plugin data).
	Data string
	// Plugins is the root directory with installed plugins.
	Plugins string
	// Logs is the directory for rotated log files.
	Logs string
	// Cache is the directory for regenerable files.
	Cache string
	// Run holds the pid file.
	Run string
}

// Env is a small subset of os.Getenv, injected for testability.
type Env func(string) string

// OSEnv reads from the process environment.
func OSEnv(k string) string { return os.Getenv(k) }

// Resolve computes the layout, honouring AURORA_HOME and XDG_* first.
func Resolve(env Env) (Layout, error) {
	if env == nil {
		env = OSEnv
	}

	// Explicit AURORA_HOME is used verbatim (cleaned). Everything else gets
	// the "/aurora" suffix so XDG_DATA_HOME or ~/.local/share are not
	// polluted with our files directly.
	var home string
	if v := strings.TrimSpace(env("AURORA_HOME")); v != "" {
		home = v
	} else if v := strings.TrimSpace(env("XDG_DATA_HOME")); v != "" {
		home = filepath.Join(v, "aurora")
	} else if v := strings.TrimSpace(env("HOME")); v != "" {
		home = filepath.Join(v, ".local", "share", "aurora")
	}
	if home == "" {
		return Layout{}, os.ErrNotExist
	}
	home = filepath.Clean(home)
	abs, err := filepath.Abs(home)
	if err != nil {
		return Layout{}, err
	}

	l := Layout{
		Home:    abs,
		Config:  filepath.Join(abs, "etc"),
		Data:    filepath.Join(abs, "data"),
		Plugins: filepath.Join(abs, "plugins"),
		Logs:    filepath.Join(abs, "logs"),
		Cache:   filepath.Join(abs, "cache"),
		Run:     filepath.Join(abs, "run"),
	}
	return l, nil
}

// ConfigFile is the path of the main configuration file.
func (l Layout) ConfigFile() string { return filepath.Join(l.Config, "config.json") }

// DBFile is the path of the tiny key-value database.
func (l Layout) DBFile() string { return filepath.Join(l.Config, "aurora.db") }

// SessionFile is the path of the MTProto session blob.
func (l Layout) SessionFile() string { return filepath.Join(l.Data, "session.json") }

// AccountSessionFile returns the session file path for a specific account.
func (l Layout) AccountSessionFile(id string) string {
	if id == "" || id == "default" {
		return l.SessionFile()
	}
	clean := filepath.Base(id)
	return filepath.Join(l.Data, "session_"+clean+".json")
}

// LogFile is the path of the main log file.
func (l Layout) LogFile() string { return filepath.Join(l.Logs, "aurora.log") }

// PidFile is the path of the pid file.
func (l Layout) PidFile() string { return filepath.Join(l.Run, "aurora.pid") }

// PluginDir is the directory of a single plugin.
func (l Layout) PluginDir(name string) string { return filepath.Join(l.Plugins, name) }

// Ensure creates every directory with owner-only permissions where it matters.
func (l Layout) Ensure() error {
	for _, d := range []struct {
		dir  string
		mode os.FileMode
	}{
		{l.Home, 0o700},
		{l.Config, 0o700},
		{l.Data, 0o700},
		{l.Plugins, 0o700},
		{l.Logs, 0o700},
		{l.Cache, 0o700},
		{l.Run, 0o700},
	} {
		if err := os.MkdirAll(d.dir, d.mode); err != nil {
			return err
		}
	}
	return nil
}

// IsTermux reports whether the process is executing inside the Termux environment.
func IsTermux() bool {
	return os.Getenv("PREFIX") == "/data/data/com.termux/files/usr"
}
