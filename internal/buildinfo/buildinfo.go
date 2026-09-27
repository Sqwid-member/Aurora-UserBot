// Package buildinfo holds build-time metadata injected via -ldflags.
package buildinfo

import "runtime"

var (
	// Version is the semantic version of the build.
	Version = "dev"
	// Commit is the git revision of the build.
	Commit = "none"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
)

// GoVersion is the toolchain used to build the binary.
var GoVersion = runtime.Version()

// String renders a single-line build banner.
func String() string {
	return Version + " (" + Commit + ", " + GoVersion + ", " + Date + ")"
}
