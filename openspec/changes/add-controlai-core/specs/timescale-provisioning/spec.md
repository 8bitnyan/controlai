## ADDED Requirements

### Requirement: Per-tenant TimescaleDB container with low-RAM profile

controlai SHALL provision exactly one TimescaleDB 2.x (PostgreSQL 16)
container per tenant. The rendered `postgresql.conf` SHALL apply a
low-RAM profile derived from `timescaledb-tune`'s formulas
(`shared_buffers=64 MB`, `effective_cache_size=192 MB`,
`maintenance_work_mem=32 MB`, `work_mem=2 MB`, `max_connections=10`,
`wal_buffers=4 MB`, `synchronous_commit=off`,
`timescaledb.telemetry_level=off`, `max_background_workers=2`,
parallel query disabled) targeted at a ~256 MB container memory limit
appropriate for the t3.medium PoC ceiling.

#### Scenario: Tenant TSDB applies low-RAM defaults
- **WHEN** a new tenant is created
- **THEN** the rendered TSDB compose service SHALL set `mem_limit=256m` and SHALL mount a `postgresql.conf` matching the documented low-RAM values

### Requirement: Telemetry hypertable with site-aware indexing

controlai SHALL create exactly one `telemetry` hypertable per tenant
with columns `(time timestamptz NOT NULL, site_id text NOT NULL,
device_id text NOT NULL, metric text NOT NULL, payload jsonb,
raw bytea)`, an index `(site_id, time DESC)`, and a chunk interval
selected by the renderer based on the chosen retention preset.

#### Scenario: Chunk interval scales with retention
- **WHEN** retention is `1d`, the rendered init.sql SHALL declare `chunk_time_interval => INTERVAL '15 minutes'`
- **WHEN** retention is `7d`, the rendered init.sql SHALL declare `chunk_time_interval => INTERVAL '1 hour'`
- **WHEN** retention is `30d`, the rendered init.sql SHALL declare `chunk_time_interval => INTERVAL '6 hours'`

### Requirement: Configurable retention policy per tenant

The tenant's retention SHALL be one of `1m`, `1h`, `1d`, `7d`, `30d`.
The renderer SHALL emit a single `add_retention_policy('telemetry',
INTERVAL '<value>')` call against the tenant's hypertable, with
`schedule_interval` set to at least the chosen chunk interval.

#### Scenario: Retention change re-renders init.sql and re-applies policy
- **WHEN** an operator changes a tenant's retention from `7d` to `1d`
- **THEN** controlai SHALL re-render the tenant's init.sql, execute an idempotent migration script inside the running TSDB container that drops the old retention policy and adds the new one, and the change SHALL be reflected in `SELECT * FROM timescaledb_information.policies` within 60 s

### Requirement: Compression policy when retention permits

When the chosen retention is `7d` or `30d`, the renderer SHALL emit
`add_compression_policy('telemetry', INTERVAL '7 days')`. For retentions
shorter than `7d`, compression SHALL be off.

#### Scenario: 30d retention enables compression
- **WHEN** a tenant is provisioned with `retention=30d`
- **THEN** the rendered init.sql SHALL contain both an `add_retention_policy` and an `add_compression_policy` call

#### Scenario: 1h retention disables compression
- **WHEN** a tenant is provisioned with `retention=1h`
- **THEN** the rendered init.sql SHALL NOT contain `add_compression_policy`

### Requirement: TSDB credentials managed by controlai

controlai SHALL generate per-tenant database credentials (one
superuser used by init scripts and one constrained role used by the
ingest service), store them in SQLite, and inject them into compose env
files; credentials SHALL NEVER be checked into the repo or rendered to
operator-readable plaintext files.

#### Scenario: Ingest role has minimum privileges
- **WHEN** controlai bootstraps a tenant TSDB
- **THEN** the ingest role SHALL have INSERT permission on `telemetry` only and NO other privileges; the superuser role SHALL be used solely for migrations and policy management
