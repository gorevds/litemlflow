// Package version exposes build-time version metadata.
//
// The values are populated via -ldflags -X by the Makefile / release pipeline.
package version

// Version is the human-readable release version (e.g., "v0.1.0", "dev").
var Version = "dev"

// Commit is the short git SHA of the build.
var Commit = "unknown"

// Date is the RFC 3339 timestamp of the build.
var Date = "unknown"

// String returns "version (commit, date)".
func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
