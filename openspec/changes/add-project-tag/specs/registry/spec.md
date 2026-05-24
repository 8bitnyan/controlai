## MODIFIED Requirements

### Requirement: Tenant entity with slug ID and per-tenant TimescaleDB

controlai SHALL define a `Tenant` entity uniquely identified by an
operator-supplied kebab-case slug automatically prefixed with `tnt_`
(e.g. `tnt_acme-corp`). Each tenant SHALL own exactly one TimescaleDB
container shared across all of that tenant's sites, and SHALL be isolated
from other tenants at the container, volume, and network level.

Each tenant MAY carry an optional opaque `project_id` string tag (up to 64
alphanumeric, underscore, or hyphen characters) that associates the daemon
tenant with a Project entity in an external control plane (e.g. controlai-web).
The daemon treats `project_id` as metadata only and SHALL NOT enforce referential
integrity against any external system. Tenants without a `project_id` SHALL
continue to function normally.

#### Scenario: Create a tenant

- **WHEN** an operator submits `POST /v1/tenants {"slug": "acme-corp"}` with the daemon healthy
- **THEN** controlai SHALL allocate `tnt_acme-corp` as the tenant ID, create `/var/lib/controlai/tenants/tnt_acme-corp/` with `tenant.yaml` (`schema_version: 1`) and an empty `sites/` directory, insert a row into the SQLite `tenants` table, render the per-tenant TimescaleDB compose project, and start the TSDB container — converging within 60 s

#### Scenario: Create a tenant with project_id

- **WHEN** an operator submits `POST /v1/tenants {"slug": "acme-corp", "project_id": "proj-abc123"}` with the daemon healthy
- **THEN** controlai SHALL allocate the tenant as above AND persist `project_id = "proj-abc123"` in the `tenants` table
- **AND** the response JSON SHALL include `"project_id": "proj-abc123"`

#### Scenario: Update project_id on existing tenant

- **WHEN** an operator submits `PATCH /v1/tenants/tnt_acme-corp {"project_id": "proj-xyz456"}`
- **THEN** controlai SHALL update the `project_id` column in the `tenants` table
- **AND** the response JSON SHALL reflect the new `project_id`

#### Scenario: Reject invalid project_id

- **WHEN** the supplied `project_id` contains characters outside `[a-zA-Z0-9_-]` or exceeds 64 characters
- **THEN** controlai SHALL refuse with HTTP 400 stating the allowed character class and length limit

#### Scenario: Reject duplicate slug

- **WHEN** an operator submits a tenant create request with a slug already used
- **THEN** controlai SHALL refuse the request with HTTP 409 and leave no on-disk artifacts behind

#### Scenario: Reject invalid slug

- **WHEN** the supplied slug does not match `^[a-z][a-z0-9-]{0,40}$`
- **THEN** controlai SHALL refuse with HTTP 400 stating the allowed character class

## ADDED Requirements

### Requirement: Project-scoped tenant list filter

`GET /v1/tenants` SHALL accept an optional `?project_id=` query parameter.
When provided with a non-empty value, the daemon SHALL return only tenants
whose `project_id` matches exactly. When provided with an empty value
(`?project_id=`), the daemon SHALL return tenants where `project_id` is
NULL or empty string (untagged tenants). When omitted entirely, the daemon
SHALL return all tenants regardless of `project_id`.

#### Scenario: Filter tenants by project_id

- **WHEN** an operator calls `GET /v1/tenants?project_id=proj-abc123`
- **THEN** the daemon SHALL return only tenants with `project_id = "proj-abc123"`
- **AND** tenants with a different `project_id` or NULL `project_id` SHALL NOT appear in the response

#### Scenario: List untagged tenants

- **WHEN** an operator calls `GET /v1/tenants?project_id=` (empty value)
- **THEN** the daemon SHALL return tenants where `project_id IS NULL OR project_id = ''`
- **AND** tenants with a non-empty `project_id` SHALL NOT appear in the response

#### Scenario: Unfiltered list returns all tenants

- **WHEN** an operator calls `GET /v1/tenants` without a `project_id` parameter
- **THEN** the daemon SHALL return all tenants regardless of their `project_id` value

### Requirement: project_id propagation in audit log

Audit log entries that include a `tenant_id` SHALL also include the tenant's
`project_id` when set, so that external consumers can filter audit events by
the web-side Project without a separate tenant lookup.

#### Scenario: Audit entry includes project_id

- **WHEN** an audit event is written for an action on a tenant that has `project_id = "proj-abc123"`
- **THEN** the audit log entry SHALL include `"project_id": "proj-abc123"` in its JSON payload

#### Scenario: Audit entry omits project_id when unset

- **WHEN** an audit event is written for an action on a tenant that has no `project_id`
- **THEN** the audit log entry SHALL omit the `project_id` field (omitempty) rather than including a null or empty value

#### Scenario: Backward-compatible audit consumers

- **WHEN** an audit consumer that does not know about `project_id` reads an audit entry from a tenant with `project_id` set
- **THEN** the consumer SHALL be able to parse the entry without error (the field is additive in JSON and ignored by older consumers)
