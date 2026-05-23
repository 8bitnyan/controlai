-- controlai SQLite registry schema v1
-- All IDs use kebab-prefixed slugs: tnt_<slug>, ste_<slug>.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

-- Tenants table
CREATE TABLE IF NOT EXISTS tenants (
    id           TEXT    PRIMARY KEY,               -- tnt_<slug>
    name         TEXT    NOT NULL DEFAULT '',
    domain       TEXT    NOT NULL,
    retention    TEXT    NOT NULL DEFAULT '7d',
    schema_version INTEGER NOT NULL DEFAULT 1,
    status       TEXT    NOT NULL DEFAULT 'active', -- active | orphaned
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Sites table
CREATE TABLE IF NOT EXISTS sites (
    id             TEXT    PRIMARY KEY,             -- ste_<slug>
    tenant_id      TEXT    NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    broker_kind    TEXT    NOT NULL,                -- mosquitto | emqx
    throughput     TEXT    NOT NULL DEFAULT 'low',  -- low | mid
    direction      TEXT    NOT NULL DEFAULT 'uni',  -- uni | bi
    payload_codec  TEXT    NOT NULL DEFAULT 'cbor', -- cbor | json | raw_passthrough
    leaf_ttl_days  INTEGER NOT NULL DEFAULT 365,
    schema_version INTEGER NOT NULL DEFAULT 1,
    status         TEXT    NOT NULL DEFAULT 'active', -- active | stopped | orphaned
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_sites_tenant ON sites(tenant_id);

-- Desired state: per compose-project desired state (running | stopped | removed)
CREATE TABLE IF NOT EXISTS desired_state (
    project_id   TEXT    PRIMARY KEY,  -- compose project name
    tenant_id    TEXT,
    site_id      TEXT,
    state        TEXT    NOT NULL DEFAULT 'running',  -- running | stopped | removed
    config_hash  TEXT    NOT NULL DEFAULT '',         -- SHA-256 of rendered files
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Actual state cache: per rendered file content-addressed hash
CREATE TABLE IF NOT EXISTS actual_state_cache (
    project_id   TEXT    NOT NULL,
    file_path    TEXT    NOT NULL,         -- repo-relative rendered path
    content_hash TEXT    NOT NULL,         -- SHA-256 hex
    updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (project_id, file_path)
);

-- Audit events
CREATE TABLE IF NOT EXISTS audit_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,           -- e.g. "reconciler.success"
    tenant_id    TEXT    NOT NULL DEFAULT '',
    site_id      TEXT    NOT NULL DEFAULT '',
    actor_ip     TEXT    NOT NULL DEFAULT '',
    detail       TEXT    NOT NULL DEFAULT '', -- JSON or free-form
    success      INTEGER NOT NULL DEFAULT 1, -- 1 = true, 0 = false
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_audit_kind       ON audit_events(kind);
CREATE INDEX IF NOT EXISTS idx_audit_tenant     ON audit_events(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_events(created_at);

-- Settings: key-value for daemon configuration stored at runtime
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Auth tokens for the optional TCP+TLS transport
CREATE TABLE IF NOT EXISTS auth_tokens (
    id           TEXT    PRIMARY KEY,               -- random UUID
    display_name TEXT    NOT NULL,
    hash         TEXT    NOT NULL UNIQUE,            -- SHA-256 of the raw token, hex
    prefix       TEXT    NOT NULL,                  -- first 8 chars for display
    revoked      INTEGER NOT NULL DEFAULT 0,        -- 0 = active, 1 = revoked
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    revoked_at   DATETIME
);

-- PKI: per-site Certificate Authorities
CREATE TABLE IF NOT EXISTS cas (
    site_id         TEXT    PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    cert_pem        TEXT    NOT NULL,  -- PEM-encoded CA cert
    key_enc         BLOB    NOT NULL,  -- AES-256-GCM encrypted CA private key
    key_nonce       BLOB    NOT NULL,  -- 12-byte GCM nonce
    fingerprint     TEXT    NOT NULL,  -- SHA-256 hex of DER cert
    not_before      DATETIME NOT NULL,
    not_after       DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- PKI: issued certificates
CREATE TABLE IF NOT EXISTS certs (
    id              TEXT    PRIMARY KEY,          -- UUID
    site_id         TEXT    NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL,             -- gateway | server | ingestor
    common_name     TEXT    NOT NULL,             -- slugified CN
    cert_pem        TEXT    NOT NULL,
    fingerprint     TEXT    NOT NULL UNIQUE,      -- SHA-256 hex
    not_before      DATETIME NOT NULL,
    not_after       DATETIME NOT NULL,
    revoked         INTEGER NOT NULL DEFAULT 0,
    revoked_at      DATETIME,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_certs_site    ON certs(site_id);
CREATE INDEX IF NOT EXISTS idx_certs_expiry  ON certs(not_after, revoked);

-- EMQX per-site REST API keys
CREATE TABLE IF NOT EXISTS emqx_api_keys (
    site_id     TEXT    PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    key_id      TEXT    NOT NULL,
    key_secret  TEXT    NOT NULL,  -- plaintext (AES-GCM-wrapped storage is future work)
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- TSDB credentials per tenant
CREATE TABLE IF NOT EXISTS tsdb_credentials (
    tenant_id      TEXT    PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    superuser_name TEXT    NOT NULL DEFAULT 'controlai_admin',
    superuser_pass TEXT    NOT NULL,  -- randomly generated, never logged
    ingest_name    TEXT    NOT NULL DEFAULT 'controlai_ingest',
    ingest_pass    TEXT    NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
