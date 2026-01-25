// Package version provides build-time version information.
package version

var (
	// Version is the semantic version, set via ldflags.
	Version = "dev"
	// Commit is the short git commit hash, set via ldflags.
	Commit = "unknown"
	// GitTime is the commit timestamp in ISO 8601 UTC format, set via ldflags.
	GitTime = "unknown"
)
