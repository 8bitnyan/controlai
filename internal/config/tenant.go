// Package config defines the canonical Go types for tenant.yaml and site.yaml,
// including full validation logic.
package config

import (
	"fmt"
	"regexp"

	"controlai/internal/version"
)

// Retention windows allowed in tenant.yaml.
type Retention string

const (
	Retention1m  Retention = "1m"
	Retention1h  Retention = "1h"
	Retention1d  Retention = "1d"
	Retention7d  Retention = "7d"
	Retention30d Retention = "30d"
)

var validRetentions = map[Retention]bool{
	Retention1m: true, Retention1h: true, Retention1d: true,
	Retention7d: true, Retention30d: true,
}

// SlugPattern matches valid tenant and site slug input (before prefix).
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

// tenantIDPattern matches the stored tnt_-prefixed tenant ID.
var tenantIDPattern = regexp.MustCompile(`^tnt_[a-z][a-z0-9-]{0,40}$`)

// TenantResources holds optional resource overrides for the TSDB compose service.
type TenantResources struct {
	// MemoryLimit overrides the default "256m" container memory limit.
	MemoryLimit string `yaml:"memory_limit,omitempty"`
}

// Tenant mirrors the on-disk tenant.yaml structure.
type Tenant struct {
	SchemaVersion int             `yaml:"schema_version"`
	ID            string          `yaml:"id"`             // tnt_<slug>
	Name          string          `yaml:"name,omitempty"` // human-readable
	Domain        string          `yaml:"domain"`         // base domain for SNI routing
	Retention     Retention       `yaml:"retention"`
	Resources     TenantResources `yaml:"resources,omitempty"`
}

// Validate checks the Tenant for consistency and schema-version support.
func (t *Tenant) Validate() error {
	if t.SchemaVersion < 1 || t.SchemaVersion > version.MaxSupportedYAMLSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (binary supports up to %d)",
			t.SchemaVersion, version.MaxSupportedYAMLSchemaVersion)
	}
	if !tenantIDPattern.MatchString(t.ID) {
		return fmt.Errorf("invalid tenant ID %q: must match tnt_[a-z][a-z0-9-]{0,40}", t.ID)
	}
	if t.Domain == "" {
		return fmt.Errorf("tenant domain must be set")
	}
	if !validRetentions[t.Retention] {
		return fmt.Errorf("invalid retention %q: must be one of 1m 1h 1d 7d 30d", t.Retention)
	}
	return nil
}

// ValidateSlug validates a raw slug (before prefix) matches the allowed pattern.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("invalid slug %q: must match ^[a-z][a-z0-9-]{0,40}$ (lowercase letters, digits, hyphens; must start with a letter)", slug)
	}
	return nil
}

// TenantIDFromSlug returns the canonical tnt_-prefixed tenant ID.
func TenantIDFromSlug(slug string) string { return "tnt_" + slug }

// SiteIDFromSlug returns the canonical ste_-prefixed site ID.
func SiteIDFromSlug(slug string) string { return "ste_" + slug }

// CompressionEnabled returns true when the retention window is long enough to
// benefit from TimescaleDB compression (≥7d).
func (t *Tenant) CompressionEnabled() bool {
	return t.Retention == Retention7d || t.Retention == Retention30d
}

// ChunkTimeInterval returns the recommended chunk_time_interval for the
// telemetry hypertable based on the configured retention window.
func (t *Tenant) ChunkTimeInterval() string {
	switch t.Retention {
	case Retention1m, Retention1h:
		return "1 minute"
	case Retention1d:
		return "15 minutes"
	case Retention7d:
		return "1 hour"
	case Retention30d:
		return "6 hours"
	default:
		return "1 hour"
	}
}
