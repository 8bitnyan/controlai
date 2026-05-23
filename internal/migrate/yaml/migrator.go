// Package yaml provides the YAML schema-version migration framework for
// tenant.yaml and site.yaml files under /var/lib/controlai/.
//
// Each migration is a function that transforms a map[string]any from one
// schema_version to the next. MigrateFile applies all pending migrations in
// sequence and writes the result back to disk with a .bak side-car.
package yaml

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	yamlv3 "gopkg.in/yaml.v3"
)

// TransformFn is a version-to-version transformer.
// It receives the parsed YAML map at version N and returns the map at version N+1.
type TransformFn func(m map[string]any) (map[string]any, error)

// registry maps from_version → transformer.
var registry = map[int]TransformFn{}

// Register registers a migration from version N to N+1.
// Called by init() in migration files.
func Register(fromVersion int, fn TransformFn) {
	if _, dup := registry[fromVersion]; dup {
		panic(fmt.Sprintf("duplicate migration registration for schema_version %d", fromVersion))
	}
	registry[fromVersion] = fn
}

// MigrateFile reads the YAML file at path, applies all registered migrations
// from its current schema_version up to targetVersion, writes a .bak side-car,
// then writes the migrated content back.
//
// Returns (false, nil) when no migration was needed.
func MigrateFile(path string, targetVersion int) (migrated bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	var m map[string]any
	if err := yamlv3.Unmarshal(raw, &m); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	current, _ := schemaVersion(m)
	if current >= targetVersion {
		return false, nil
	}

	// Write backup before modifying.
	bakPath := path + ".bak"
	if err := os.WriteFile(bakPath, raw, 0o600); err != nil {
		return false, fmt.Errorf("write backup %s: %w", bakPath, err)
	}

	// Apply migrations sequentially.
	for v := current; v < targetVersion; v++ {
		fn, ok := registry[v]
		if !ok {
			return false, fmt.Errorf("no migration registered from schema_version %d to %d", v, v+1)
		}
		m, err = fn(m)
		if err != nil {
			return false, fmt.Errorf("migration %d→%d on %s: %w", v, v+1, path, err)
		}
		m["schema_version"] = v + 1
	}

	out, err := yamlv3.Marshal(m)
	if err != nil {
		return false, fmt.Errorf("marshal migrated %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return false, fmt.Errorf("write migrated %s: %w", path, err)
	}
	return true, nil
}

// WalkAndMigrate walks the directory tree rooted at root, migrates every
// tenant.yaml and site.yaml found, and returns the list of files that were
// actually migrated.
func WalkAndMigrate(root string, targetVersion int, dryRun bool) ([]string, error) {
	var migrated []string
	patterns := []string{"tenant.yaml", "site.yaml"}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		for _, p := range patterns {
			if base == p {
				if dryRun {
					// Check but don't modify.
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					var m map[string]any
					if yerr := yamlv3.Unmarshal(raw, &m); yerr != nil {
						return yerr
					}
					v, _ := schemaVersion(m)
					if v < targetVersion {
						migrated = append(migrated, path)
					}
					return nil
				}
				did, merr := MigrateFile(path, targetVersion)
				if merr != nil {
					return merr
				}
				if did {
					migrated = append(migrated, path)
				}
				return nil
			}
		}
		return nil
	})
	return migrated, err
}

// CheckSchemaVersions walks the tenant registry tree rooted at root (typically
// /var/lib/controlai/tenants) and returns an error listing every tenant.yaml
// and site.yaml whose schema_version exceeds maxVersion. This is called by the
// daemon at startup before opening any listener so that it exits cleanly when
// the on-disk YAML is newer than the binary understands.
func CheckSchemaVersions(root string, maxVersion int) error {
	var offenders []string
	patterns := []string{"tenant.yaml", "site.yaml"}

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr
		}
		base := filepath.Base(path)
		for _, p := range patterns {
			if base != p {
				continue
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				offenders = append(offenders, path+": "+readErr.Error())
				return nil
			}
			var m map[string]any
			if yerr := yamlv3.Unmarshal(raw, &m); yerr != nil {
				offenders = append(offenders, path+": "+yerr.Error())
				return nil
			}
			v, _ := schemaVersion(m)
			if v > maxVersion {
				offenders = append(offenders,
					fmt.Sprintf("%s: schema_version=%d > supported=%d", path, v, maxVersion))
			}
		}
		return nil
	})

	if len(offenders) > 0 {
		msg := fmt.Sprintf("daemon refuses to start: %d YAML file(s) declare a schema_version "+
			"newer than this binary supports (max=%d). Run 'controlai migrate' or upgrade the binary.\n",
			len(offenders), maxVersion)
		for _, o := range offenders {
			msg += "  " + o + "\n"
		}
		return errors.New(msg)
	}
	return nil
}

// schemaVersion extracts the schema_version field from a parsed YAML map.
func schemaVersion(m map[string]any) (int, error) {
	v, ok := m["schema_version"]
	if !ok {
		return 0, errors.New("schema_version missing")
	}
	switch sv := v.(type) {
	case int:
		return sv, nil
	case float64:
		return int(sv), nil
	default:
		return 0, fmt.Errorf("schema_version is not a number: %T", v)
	}
}
