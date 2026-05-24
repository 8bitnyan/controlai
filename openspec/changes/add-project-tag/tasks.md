# Tasks: add-project-tag

## 1. SQLite migration

- [ ] 1.1 Create `internal/store/sqlite/migrations/0002_add_project_id.sql` with `ALTER TABLE tenants ADD COLUMN project_id TEXT;` and `CREATE INDEX IF NOT EXISTS idx_tenants_project_id ON tenants(project_id);`.
- [ ] 1.2 Update `internal/store/sqlite/migrate.go` to include the new migration file in the ordered migration list (if using embedded migrations via `embed.FS`).
- [ ] 1.3 Add a guard in the migration runner: check `PRAGMA table_info(tenants)` before applying the `ALTER TABLE` to prevent failure on a database that already has the column.
- [ ] 1.4 Write a migration test that applies `0001_init.sql` + `0002_add_project_id.sql` to an in-memory SQLite database and asserts both `project_id` column and index exist.

## 2. Go struct update

- [ ] 2.1 Add `ProjectID string \`json:"project_id,omitempty" db:"project_id"\`` field to the `Tenant` struct in `internal/store/tenant.go` (or wherever the struct is defined).
- [ ] 2.2 Update all SQL `SELECT` statements that retrieve tenant rows to include `project_id` in the column list.
- [ ] 2.3 Update `INSERT INTO tenants` statement to include `project_id` in both column list and values (use `$project_id` param, nullable).
- [ ] 2.4 Update `UPDATE tenants SET …` statement (if a general update path exists) to include `project_id = $project_id`.

## 3. REST handler changes

- [ ] 3.1 In `POST /v1/tenants` handler (`internal/daemon/server.go`): read `project_id` from JSON body (optional, default empty string); pass to store `CreateTenant` call.
- [ ] 3.2 In `PATCH /v1/tenants/:id` handler (or equivalent update endpoint): read `project_id` from JSON body; allow updating the tag on an existing tenant.
- [ ] 3.3 In `GET /v1/tenants` handler: read optional `?project_id=` query param; if non-empty, add `WHERE project_id = ?` to the SQL query; if `?project_id=` (empty string), return tenants where `project_id = '' OR project_id IS NULL`.
- [ ] 3.4 Ensure `GET /v1/tenants/:id` includes `project_id` in the response JSON.
- [ ] 3.5 Add input validation: `project_id` (if provided) MUST match `^[a-zA-Z0-9_-]{1,64}$` or be empty string; reject with HTTP 400 otherwise.

## 4. Audit log propagation

- [ ] 4.1 In `internal/audit/audit.go`: add `ProjectID string` field to `AuditEntry` struct.
- [ ] 4.2 In all audit-writing call sites that include a `TenantID`: look up the tenant's `project_id` from the store and set `AuditEntry.ProjectID` if non-empty.
- [ ] 4.3 Ensure audit log JSON output includes `"project_id"` field (omitempty so existing audit consumers don't break).
- [ ] 4.4 Add `project_id` column to the `audit_log` SQLite table if it is stored there (check schema; add migration step if needed).

## 5. Unit tests

- [ ] 5.1 Add table-driven test in `internal/store/tenant_test.go`: create tenant with `project_id = "proj-abc"`, retrieve, assert field matches.
- [ ] 5.2 Add test: create tenant without `project_id`, retrieve, assert field is empty string or nil.
- [ ] 5.3 Add test: `GET /v1/tenants?project_id=proj-abc` returns only tenants with that tag.
- [ ] 5.4 Add test: `GET /v1/tenants` (no filter) returns all tenants regardless of `project_id`.
- [ ] 5.5 Add test: `PATCH` updates `project_id` from "proj-abc" to "proj-xyz" correctly.
- [ ] 5.6 Add test: `project_id` with invalid characters returns HTTP 400.

## 6. Backward-compatibility test

- [ ] 6.1 Add test that opens an existing `0001_init.sql` schema database (simulate legacy installation), applies `0002_add_project_id.sql`, and verifies existing tenant rows are intact with `project_id = NULL`.
- [ ] 6.2 Verify that `GET /v1/tenants` on a database with legacy rows (NULL `project_id`) returns all tenants (the NULL rows are not excluded).
- [ ] 6.3 Verify that the JSON response for a legacy tenant does NOT include a `"project_id"` key when the value is NULL (omitempty behaviour).

## 7. Acceptance

- [ ] 7.1 Run `openspec validate add-project-tag --strict` and confirm exit 0.
- [ ] 7.2 Run all unit tests with `go test ./internal/store/... ./internal/daemon/...` and confirm no failures.
- [ ] 7.3 Run integration test against a running daemon: create tenant with `project_id`, list filtered, update tag, list again.
- [ ] 7.4 Verify audit log entry for tenant create includes `project_id` field.
