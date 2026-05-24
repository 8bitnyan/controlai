# Change: Add nullable project_id tag to controlai tenants

## Why

`controlai-web` owns the canonical `Project` entity (in its Postgres DB). When the web
BFF provisions a daemon Tenant for a Project's Site, the audit and log query APIs on the
daemon should be filterable by the web-side Project ID so operators can see "all daemon
activity for Project X" without fetching all tenants and filtering client-side.
The daemon does not own the Project concept — it treats `project_id` as an opaque
string tag — but it must persist and expose it for filtering. (Decision D13 from the
controlai-web interview.)

## What Changes

- `tenants` SQLite table gains a nullable `project_id TEXT` column (additive migration).
- `Tenant` Go struct gains `ProjectID string` (omitempty).
- `POST /v1/tenants` and `PATCH /v1/tenants/:id` accept optional `project_id` in the
  JSON body.
- `GET /v1/tenants` accepts optional `?project_id=` query parameter to filter results.
- Audit log entries that include a `tenant_id` also include `project_id` where set.
- Spec `tenant-management` is **MODIFIED** to document the new field and filter.

## Impact

- Affected specs: `tenant-management` (modified — additive only)
- Affected code: `internal/store/sqlite/migrations/`, `internal/store/tenant.go`,
  `internal/daemon/server.go` (tenant CRUD handlers), `internal/audit/audit.go`
- Breaking changes: **none** — `project_id` is nullable; existing tenants with NULL value
  continue to work without modification
- No reconciler change required (project_id is metadata only, not a desired-state field)
