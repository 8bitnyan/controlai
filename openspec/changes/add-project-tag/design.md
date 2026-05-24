# Design: add-project-tag

## Context

controlai's domain hierarchy today is `Tenant → Site`. The upcoming `controlai-web`
control plane adds `User → Org → ControlaiInstance → Project → SiteGroup → Site`,
where each `controlai-web Site` maps to a daemon `Tenant + Site` pair. When the BFF
provisions or queries the daemon, it needs to tag each daemon Tenant with the
controlai-web Project ID so audit and list queries can be scoped to a project without
fetching all tenants.

The daemon is intentionally kept simple: it treats `project_id` as an opaque string.
controlai-web is the authoritative source for the Project entity; the daemon is a dumb
consumer that stores and exposes the tag.

## Goals / Non-Goals

- **Goals**
  - Add `project_id TEXT` nullable column to `tenants` table via additive SQLite migration
  - Accept `project_id` on tenant create and update
  - Filter `GET /v1/tenants` by `?project_id=`
  - Include `project_id` in audit log entries where the tenant has one set
- **Non-Goals**
  - Daemon-side Projects table — controlai-web owns the canonical Project entity
  - FK constraints from `project_id` to a daemon-side table (opaque string only)
  - Per-project capacity limits (out of scope; capacity guard is per-tenant, not per-project)
  - Cascading deletes when a Project is removed from controlai-web (controlai-web BFF
    is responsible for deleting its tenants before deleting a project)

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| D13 | Opaque string tag | controlai-web owns the canonical Project entity; daemon is dumb consumer. Using an FK would create an unwanted dependency on controlai-web's data model inside the daemon. |
| — | Nullable column | Backward-compatible: existing tenants continue to work with `project_id = NULL`. No data migration needed. |
| — | No reconciler change | `project_id` is metadata only. The reconciler operates on desired infrastructure state; project membership is not part of that. |
| — | Migration file `0002_add_project_id.sql` | Follows the existing migration naming convention (`0001_init.sql` from add-controlai-core). |
| — | `?project_id=` query param | Simplest API extension; consistent with existing filter patterns in `GET /v1/tenants`. |

### Alternatives Considered

- **Add a `Projects` table to the daemon** — Rejected. Adds schema complexity and
  a sync problem (controlai-web is the system of record; keeping daemon-side Projects
  in sync would require webhooks or polling).
- **Store project_id only in controlai-web Postgres** — Rejected. The daemon's audit
  log and list API wouldn't be filterable by project from outside controlai-web.
- **Embed project_id in tenant slug prefix** — Rejected. The slug is already used as a
  container prefix (`tnt_<slug>`); changing slugs for existing tenants is destructive.

## Migration Plan

1. Add `migrations/0002_add_project_id.sql`:
   ```sql
   ALTER TABLE tenants ADD COLUMN project_id TEXT;
   CREATE INDEX idx_tenants_project_id ON tenants(project_id);
   ```
2. controlai daemon runs migrations on startup via `internal/store/sqlite/migrate.go`;
   the `ALTER TABLE … ADD COLUMN` is a no-op if the column already exists (SQLite
   behaviour; guard with `PRAGMA table_info`).
3. Existing tenants gain `project_id = NULL`; all queries and handlers treat NULL as
   "no project" and return the tenant in unfiltered queries.
4. Rollback: drop the column and index (requires SQLite `CREATE TABLE … AS SELECT` dance
   for column removal — document as manual step; automatic rollback not supported).

## Risks / Trade-offs

- **Stale project_id** — If a controlai-web Project is deleted without cleaning up its
  daemon tenants, tenants keep their `project_id` tag pointing to a non-existent project.
  Mitigated: controlai-web BFF MUST delete daemon tenants before deleting a project
  (enforced in `add-controlai-web-skeleton` tasks).
- **No FK constraint** — Daemon cannot validate that the project_id exists in
  controlai-web. Accepted risk (D13).

## Open Questions

- Should `GET /v1/tenants?project_id=` also include tenants where `project_id IS NULL`
  when `project_id=` is an empty string? Current answer: empty string filter returns
  tenants with `project_id = ''` OR `project_id IS NULL` (treat both as "untagged").
