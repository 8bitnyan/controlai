// Package sqlite provides the SQLite-backed registry store for controlai.
// It embeds the migration SQL and auto-migrates on Open().
package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"controlai/internal/audit"
	_ "modernc.org/sqlite"
)

//go:embed migrations/0001_init.sql
var initSQL string

// Store is the SQLite-backed store for controlai.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs pending migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite WAL supports 1 writer
	if _, err := db.ExecContext(context.Background(), initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying *sql.DB for callers that need raw access.
func (s *Store) DB() *sql.DB { return s.db }

// ─── Tenants ────────────────────────────────────────────────────────────────

// TenantRow is the flat SQLite representation of a tenant.
type TenantRow struct {
	ID            string
	Name          string
	Domain        string
	Retention     string
	SchemaVersion int
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateTenant inserts a new tenant, returning ErrDuplicate if the ID already exists.
func (s *Store) CreateTenant(ctx context.Context, t TenantRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenants (id, name, domain, retention, schema_version, status)
		 VALUES (?, ?, ?, ?, ?, 'active')`,
		t.ID, t.Name, t.Domain, t.Retention, t.SchemaVersion)
	if err != nil {
		if isSQLiteDuplicate(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("create tenant: %w", err)
	}
	return nil
}

// GetTenant retrieves a tenant by ID.
func (s *Store) GetTenant(ctx context.Context, id string) (*TenantRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, domain, retention, schema_version, status, created_at, updated_at
		 FROM tenants WHERE id = ?`, id)
	t := &TenantRow{}
	err := row.Scan(&t.ID, &t.Name, &t.Domain, &t.Retention, &t.SchemaVersion, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return t, nil
}

// ListTenants returns all tenants.
func (s *Store) ListTenants(ctx context.Context) ([]TenantRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, domain, retention, schema_version, status, created_at, updated_at
		 FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Domain, &t.Retention, &t.SchemaVersion, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTenant marks a tenant as orphaned (soft delete). Pass purge=true to hard-delete.
func (s *Store) DeleteTenant(ctx context.Context, id string, purge bool) error {
	if purge {
		_, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenants SET status='orphaned', updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, id)
	return err
}

// UpdateTenantStatus updates the status field.
func (s *Store) UpdateTenantStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenants SET status=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, id)
	return err
}

// ─── Sites ───────────────────────────────────────────────────────────────────

// SiteRow is the flat SQLite representation of a site.
type SiteRow struct {
	ID            string
	TenantID      string
	BrokerKind    string
	Throughput    string
	Direction     string
	PayloadCodec  string
	LeafTTLDays   int
	SchemaVersion int
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateSite inserts a new site row.
func (s *Store) CreateSite(ctx context.Context, site SiteRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sites (id, tenant_id, broker_kind, throughput, direction, payload_codec, leaf_ttl_days, schema_version, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active')`,
		site.ID, site.TenantID, site.BrokerKind, site.Throughput, site.Direction,
		site.PayloadCodec, site.LeafTTLDays, site.SchemaVersion)
	if err != nil {
		if isSQLiteDuplicate(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("create site: %w", err)
	}
	return nil
}

// GetSite retrieves a site by ID.
func (s *Store) GetSite(ctx context.Context, id string) (*SiteRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, broker_kind, throughput, direction, payload_codec, leaf_ttl_days, schema_version, status, created_at, updated_at
		 FROM sites WHERE id = ?`, id)
	site := &SiteRow{}
	err := row.Scan(&site.ID, &site.TenantID, &site.BrokerKind, &site.Throughput, &site.Direction,
		&site.PayloadCodec, &site.LeafTTLDays, &site.SchemaVersion, &site.Status, &site.CreatedAt, &site.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get site: %w", err)
	}
	return site, nil
}

// ListSitesByTenant returns all sites for a tenant.
func (s *Store) ListSitesByTenant(ctx context.Context, tenantID string) ([]SiteRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, broker_kind, throughput, direction, payload_codec, leaf_ttl_days, schema_version, status, created_at, updated_at
		 FROM sites WHERE tenant_id = ? ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteRow
	for rows.Next() {
		var site SiteRow
		if err := rows.Scan(&site.ID, &site.TenantID, &site.BrokerKind, &site.Throughput, &site.Direction,
			&site.PayloadCodec, &site.LeafTTLDays, &site.SchemaVersion, &site.Status, &site.CreatedAt, &site.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// ListAllSites returns all sites across all tenants.
func (s *Store) ListAllSites(ctx context.Context) ([]SiteRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, broker_kind, throughput, direction, payload_codec, leaf_ttl_days, schema_version, status, created_at, updated_at
		 FROM sites ORDER BY tenant_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteRow
	for rows.Next() {
		var site SiteRow
		if err := rows.Scan(&site.ID, &site.TenantID, &site.BrokerKind, &site.Throughput, &site.Direction,
			&site.PayloadCodec, &site.LeafTTLDays, &site.SchemaVersion, &site.Status, &site.CreatedAt, &site.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// DeleteSite marks a site as orphaned or hard-deletes it.
func (s *Store) DeleteSite(ctx context.Context, id string, purge bool) error {
	if purge {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE sites SET status='orphaned', updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, id)
	return err
}

// UpdateSiteStatus updates the status field.
func (s *Store) UpdateSiteStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sites SET status=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, id)
	return err
}

// ─── Desired state ────────────────────────────────────────────────────────────

// DesiredStateRow holds the reconciler target for a compose project.
type DesiredStateRow struct {
	ProjectID  string
	TenantID   string
	SiteID     string
	State      string // running | stopped | removed
	ConfigHash string
	UpdatedAt  time.Time
}

// UpsertDesiredState writes or updates the desired state for a project.
func (s *Store) UpsertDesiredState(ctx context.Context, d DesiredStateRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO desired_state (project_id, tenant_id, site_id, state, config_hash, updated_at)
		 VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 ON CONFLICT(project_id) DO UPDATE SET state=excluded.state, config_hash=excluded.config_hash,
		   updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		d.ProjectID, d.TenantID, d.SiteID, d.State, d.ConfigHash)
	return err
}

// GetDesiredState returns the desired state for a project.
func (s *Store) GetDesiredState(ctx context.Context, projectID string) (*DesiredStateRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT project_id, tenant_id, site_id, state, config_hash, updated_at FROM desired_state WHERE project_id=?`,
		projectID)
	d := &DesiredStateRow{}
	err := row.Scan(&d.ProjectID, &d.TenantID, &d.SiteID, &d.State, &d.ConfigHash, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// ListDesiredStates returns all desired state rows.
func (s *Store) ListDesiredStates(ctx context.Context) ([]DesiredStateRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, tenant_id, site_id, state, config_hash, updated_at FROM desired_state ORDER BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DesiredStateRow
	for rows.Next() {
		var d DesiredStateRow
		if err := rows.Scan(&d.ProjectID, &d.TenantID, &d.SiteID, &d.State, &d.ConfigHash, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ─── Actual state cache ───────────────────────────────────────────────────────

// UpsertFileHash stores or updates the content hash for a rendered file.
func (s *Store) UpsertFileHash(ctx context.Context, projectID, filePath, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO actual_state_cache (project_id, file_path, content_hash, updated_at)
		 VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		 ON CONFLICT(project_id, file_path) DO UPDATE SET content_hash=excluded.content_hash, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		projectID, filePath, hash)
	return err
}

// GetFileHash retrieves the cached hash for a rendered file.
func (s *Store) GetFileHash(ctx context.Context, projectID, filePath string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT content_hash FROM actual_state_cache WHERE project_id=? AND file_path=?`,
		projectID, filePath).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// ─── Audit events ─────────────────────────────────────────────────────────────

// Emit implements audit.Emitter.
func (s *Store) Emit(ctx context.Context, ev audit.Event) error {
	detail := ev.Detail
	if detail == "" {
		detail = "{}"
	}
	success := 1
	if !ev.Success {
		success = 0
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_events (kind, tenant_id, site_id, actor_ip, detail, success)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		string(ev.Kind), ev.TenantID, ev.SiteID, ev.ActorIP, detail, success)
	return err
}

// ListAuditEvents returns the most recent N audit events, optionally filtered by kind prefix.
func (s *Store) ListAuditEvents(ctx context.Context, kindPrefix string, limit int) ([]audit.Event, error) {
	q := `SELECT id, kind, tenant_id, site_id, actor_ip, detail, success, created_at FROM audit_events`
	args := []any{}
	if kindPrefix != "" {
		q += ` WHERE kind LIKE ?`
		args = append(args, kindPrefix+"%")
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.Event
	for rows.Next() {
		var ev audit.Event
		var successInt int
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.TenantID, &ev.SiteID, &ev.ActorIP, &ev.Detail, &successInt, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Success = successInt == 1
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ─── Auth tokens ──────────────────────────────────────────────────────────────

// TokenRow holds a bearer token row from auth_tokens.
type TokenRow struct {
	ID          string
	DisplayName string
	Hash        string // SHA-256 hex of the raw token
	Prefix      string // first 8 chars of the raw token for display
	Revoked     bool
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

// CreateToken generates a new 32-byte random token, stores its SHA-256 hash,
// and returns the raw token string exactly once.
func (s *Store) CreateToken(ctx context.Context, displayName string) (rawToken string, row TokenRow, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", TokenRow{}, fmt.Errorf("generate token: %w", err)
	}
	raw := hex.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(h[:])
	prefix := raw[:8]
	id := mustNewUUID()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO auth_tokens (id, display_name, hash, prefix) VALUES (?, ?, ?, ?)`,
		id, displayName, hash, prefix)
	if err != nil {
		return "", TokenRow{}, fmt.Errorf("store token: %w", err)
	}
	return raw, TokenRow{ID: id, DisplayName: displayName, Hash: hash, Prefix: prefix}, nil
}

// LookupToken returns the token row whose SHA-256 hash matches rawToken. Returns ErrNotFound if absent or revoked.
func (s *Store) LookupToken(ctx context.Context, rawToken string) (*TokenRow, error) {
	h := sha256.Sum256([]byte(rawToken))
	hash := hex.EncodeToString(h[:])
	row := &TokenRow{}
	var revokedInt int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, display_name, hash, prefix, revoked, created_at FROM auth_tokens WHERE hash=?`, hash).
		Scan(&row.ID, &row.DisplayName, &row.Hash, &row.Prefix, &revokedInt, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row.Revoked = revokedInt == 1
	if row.Revoked {
		return nil, ErrNotFound
	}
	return row, nil
}

// ListTokens returns all non-revoked tokens.
func (s *Store) ListTokens(ctx context.Context) ([]TokenRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, display_name, hash, prefix, revoked, created_at FROM auth_tokens WHERE revoked=0 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var t TokenRow
		var revokedInt int
		if err := rows.Scan(&t.ID, &t.DisplayName, &t.Hash, &t.Prefix, &revokedInt, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Revoked = revokedInt == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken marks a token revoked by ID.
func (s *Store) RevokeToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_tokens SET revoked=1, revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND revoked=0`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── TSDB credentials ────────────────────────────────────────────────────────

// TSDBCreds holds the per-tenant database credentials.
type TSDBCreds struct {
	TenantID      string
	SuperuserName string
	SuperuserPass string
	IngestName    string
	IngestPass    string
}

// UpsertTSDBCreds stores or updates per-tenant TSDB credentials.
func (s *Store) UpsertTSDBCreds(ctx context.Context, c TSDBCreds) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tsdb_credentials (tenant_id, superuser_name, superuser_pass, ingest_name, ingest_pass)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET superuser_pass=excluded.superuser_pass, ingest_pass=excluded.ingest_pass`,
		c.TenantID, c.SuperuserName, c.SuperuserPass, c.IngestName, c.IngestPass)
	return err
}

// GetTSDBCreds retrieves per-tenant TSDB credentials.
func (s *Store) GetTSDBCreds(ctx context.Context, tenantID string) (*TSDBCreds, error) {
	c := &TSDBCreds{}
	err := s.db.QueryRowContext(ctx,
		`SELECT tenant_id, superuser_name, superuser_pass, ingest_name, ingest_pass FROM tsdb_credentials WHERE tenant_id=?`,
		tenantID).Scan(&c.TenantID, &c.SuperuserName, &c.SuperuserPass, &c.IngestName, &c.IngestPass)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ─── Settings ─────────────────────────────────────────────────────────────────

// GetSetting returns a setting value.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return val, err
}

// SetSetting upserts a setting value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		key, value)
	return err
}

// ─── EMQX API keys ────────────────────────────────────────────────────────────

// EMQXAPIKey holds the EMQX REST API key for a site.
type EMQXAPIKey struct {
	SiteID    string
	KeyID     string
	KeySecret string
}

// UpsertEMQXKey stores or updates the EMQX API key for a site.
func (s *Store) UpsertEMQXKey(ctx context.Context, key EMQXAPIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO emqx_api_keys (site_id, key_id, key_secret) VALUES (?, ?, ?)
		 ON CONFLICT(site_id) DO UPDATE SET key_id=excluded.key_id, key_secret=excluded.key_secret`,
		key.SiteID, key.KeyID, key.KeySecret)
	return err
}

// GetEMQXKey retrieves the EMQX API key for a site.
func (s *Store) GetEMQXKey(ctx context.Context, siteID string) (*EMQXAPIKey, error) {
	k := &EMQXAPIKey{}
	err := s.db.QueryRowContext(ctx,
		`SELECT site_id, key_id, key_secret FROM emqx_api_keys WHERE site_id=?`, siteID).
		Scan(&k.SiteID, &k.KeyID, &k.KeySecret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

var (
	ErrDuplicate = errors.New("already exists")
	ErrNotFound  = errors.New("not found")
)

func isSQLiteDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// mustNewUUID generates a UUID v4-like string from crypto/rand.
func mustNewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}


