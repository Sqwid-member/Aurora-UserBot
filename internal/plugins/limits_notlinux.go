//go:build !linux

package plugins

// applyLimits is a no-op on platforms without POSIX resource limits.
func applyLimits(_ int, _ Limits) []string { return nil }

// rssKB is unsupported off Linux; stats show 0 there.
func rssKB(_ int) uint64 { return 0 }
