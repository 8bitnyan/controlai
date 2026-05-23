// Package backup provides per-tenant PostgreSQL backup and restore utilities.
// Backups are produced by running `pg_dump` inside the tenant's TSDB container
// via `docker compose exec`, piped through gzip, and written to
// /var/backups/controlai/<tenant_id>/<YYYYMMDD>.sql.gz.
// A rolling window of the 7 most-recent daily dumps is kept.
package backup

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// BackupDir is the base directory for all backup archives.
	BackupDir = "/var/backups/controlai"
	// KeepCount is the number of daily dumps to retain per tenant.
	KeepCount = 7
	// DefaultDB is the database name inside the TSDB container.
	DefaultDB = "controlai"
	// TSDBService is the compose service name for the TimescaleDB container.
	TSDBService = "tsdb"
)

// Manager performs pg_dump-based backups for a single tenant.
type Manager struct {
	// DataDir is the controlai data root (e.g. /var/lib/controlai).
	DataDir string
	// BackupRoot is the directory under which tenant backup dirs live.
	// Defaults to BackupDir when empty.
	BackupRoot string
}

func (m *Manager) backupRoot() string {
	if m.BackupRoot != "" {
		return m.BackupRoot
	}
	return BackupDir
}

// tenantComposeFile returns the path to the tenant TSDB docker-compose.yml.
func (m *Manager) tenantComposeFile(tenantID string) string {
	return filepath.Join(m.DataDir, "tenants", tenantID, "tsdb", "docker-compose.yml")
}

// tenantBackupDir returns (and creates) the per-tenant backup directory.
func (m *Manager) tenantBackupDir(tenantID string) (string, error) {
	dir := filepath.Join(m.backupRoot(), tenantID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create backup dir %s: %w", dir, err)
	}
	return dir, nil
}

// Run performs an immediate pg_dump for the given tenant and compresses it.
// It returns the path of the written archive.
func (m *Manager) Run(ctx context.Context, tenantID string) (string, error) {
	composeFile := m.tenantComposeFile(tenantID)
	if _, err := os.Stat(composeFile); err != nil {
		return "", fmt.Errorf("tenant TSDB compose file not found: %w", err)
	}

	backupDir, err := m.tenantBackupDir(tenantID)
	if err != nil {
		return "", err
	}

	stamp := time.Now().UTC().Format("20060102")
	outPath := filepath.Join(backupDir, stamp+".sql.gz")

	// Build docker compose exec command:
	// docker compose -p <project> -f <file> exec -T tsdb pg_dump -U postgres <db>
	projectID := tenantID + "-tsdb"
	args := []string{
		"compose",
		"-p", projectID,
		"-f", composeFile,
		"exec", "-T", TSDBService,
		"pg_dump", "-U", "postgres", DefaultDB,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	pgDumpOut, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start pg_dump: %w", err)
	}

	// Write compressed output.
	f, err := os.OpenFile(outPath+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("create tmp file: %w", err)
	}

	gz := gzip.NewWriter(f)
	if _, copyErr := io.Copy(gz, pgDumpOut); copyErr != nil {
		_ = cmd.Process.Kill()
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(outPath + ".tmp")
		return "", fmt.Errorf("compress dump: %w", copyErr)
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(outPath + ".tmp")
		return "", fmt.Errorf("finalize gzip: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(outPath + ".tmp")
		return "", fmt.Errorf("close archive: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		_ = os.Remove(outPath + ".tmp")
		return "", fmt.Errorf("pg_dump failed: %w; stderr: %s", err, stderrBuf.String())
	}

	// Atomic rename.
	if err := os.Rename(outPath+".tmp", outPath); err != nil {
		_ = os.Remove(outPath + ".tmp")
		return "", fmt.Errorf("rename archive: %w", err)
	}

	// Prune old dumps.
	if err := prune(backupDir, KeepCount); err != nil {
		// Non-fatal — log but don't fail the backup.
		fmt.Fprintf(os.Stderr, "backup prune warning: %v\n", err)
	}

	return outPath, nil
}

// List returns the paths of existing backup archives for the given tenant,
// sorted oldest-first.
func (m *Manager) List(tenantID string) ([]string, error) {
	dir := filepath.Join(m.backupRoot(), tenantID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backup dir: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql.gz") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// prune deletes oldest .sql.gz files in dir until at most keep remain.
func prune(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql.gz") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files) // lexicographic == chronological for YYYYMMDD
	for len(files) > keep {
		if err := os.Remove(files[0]); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old backup %s: %w", files[0], err)
		}
		files = files[1:]
	}
	return nil
}

// SystemdTimerUnit returns the contents of a systemd .timer unit that triggers
// a daily backup for the given tenant at 02:00 UTC.
func SystemdTimerUnit(tenantID string) string {
	return fmt.Sprintf(`[Unit]
Description=controlai daily backup for %s
After=controlai.service

[Timer]
OnCalendar=*-*-* 02:00:00 UTC
Persistent=true
Unit=controlai-backup@%s.service

[Install]
WantedBy=timers.target
`, tenantID, tenantID)
}

// SystemdBackupServiceUnit returns the contents of an instantiated systemd
// service unit that runs `controlai backup run <tenant>`.
func SystemdBackupServiceUnit() string {
	return `[Unit]
Description=controlai backup for tenant %i
After=controlai.service
Requires=controlai.service

[Service]
Type=oneshot
User=controlai
ExecStart=/usr/local/bin/controlai backup run %i
StandardOutput=journal
StandardError=journal
`
}
