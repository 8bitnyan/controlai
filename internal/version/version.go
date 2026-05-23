// Package version exposes the build-time version string injected via ldflags.
package version

// Version is set at build time via -ldflags "-X controlai/internal/version.Version=<tag>".
var Version = "dev"

// MaxSupportedYAMLSchemaVersion is the highest schema_version that this binary
// can read. The daemon MUST refuse to start if any tenant.yaml or site.yaml
// declares a schema_version higher than this value.
const MaxSupportedYAMLSchemaVersion = 1
