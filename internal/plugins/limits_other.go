//go:build !(linux || darwin || freebsd)

package plugins

// applyLimits is a no-op on platforms without POSIX resource limits.
func applyLimits(_ int, _ Limits) []string { return nil }
