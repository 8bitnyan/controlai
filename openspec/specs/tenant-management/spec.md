# tenant-management Specification

## Purpose
TBD - created by archiving change add-controlai-core. Update Purpose after archive.
## Requirements
### Requirement: Tenant entity with slug ID and per-tenant TimescaleDB

controlai SHALL define a `Tenant` entity uniquely identified by an
operator-supplied kebab-case slug automatically prefixed with `tnt_`
(e.g. `tnt_acme-corp`). Each tenant SHALL own exactly one TimescaleDB
container shared across all of that tenant's sites, and SHALL be isolated
from other tenants at the container, volume, and network level.

#### Scenario: Create a tenant
- **WHEN** an operator submits `POST /v1/tenants {"slug": "acme-corp"}` with the daemon healthy
- **THEN** controlai SHALL allocate `tnt_acme-corp` as the tenant ID, create `/var/lib/controlai/tenants/tnt_acme-corp/` with `tenant.yaml` (`schema_version: 1`) and an empty `sites/` directory, insert a row into the SQLite `tenants` table, render the per-tenant TimescaleDB compose project, and start the TSDB container — converging within 60 s

#### Scenario: Reject duplicate slug
- **WHEN** an operator submits a tenant create request with a slug already used
- **THEN** controlai SHALL refuse the request with HTTP 409 and leave no on-disk artifacts behind

#### Scenario: Reject invalid slug
- **WHEN** the supplied slug does not match `^[a-z][a-z0-9-]{0,40}$`
- **THEN** controlai SHALL refuse with HTTP 400 stating the allowed character class

### Requirement: Site entity scoped to a tenant

controlai SHALL define a `Site` entity uniquely identified within its
parent tenant by a kebab-case slug automatically prefixed with `ste_`,
fully qualified as `<tenant_id>/<site_id>`. Each site SHALL own its own
broker container and ingest container(s) that write to the parent
tenant's TimescaleDB.

#### Scenario: Add a site under an existing tenant
- **WHEN** an operator submits `POST /v1/tenants/tnt_acme-corp/sites {"slug":"seoul","broker":{"kind":"mosquitto"},"ingest":{"direction":"uni"},"throughput":"low"}`
- **THEN** controlai SHALL allocate `ste_seoul`, create `/var/lib/controlai/tenants/tnt_acme-corp/sites/ste_seoul/site.yaml`, render the site compose project, write the corresponding Traefik dynamic route file, start the broker and ingest containers, and converge within 60 s

#### Scenario: Reject site creation when tenant absent
- **WHEN** the tenant referenced by the site request does not exist
- **THEN** controlai SHALL refuse with HTTP 404 and leave no on-disk artifacts behind

### Requirement: Lifecycle commands

controlai SHALL expose `start`, `stop`, `apply`, `rm`, `migrate`, and
`backup` lifecycle commands on both tenant and site granularities.
Deletion SHALL be conservative: `rm` without `--purge` SHALL remove
containers but preserve volumes and YAML; `rm --purge` SHALL additionally
remove volumes and the on-disk directory.

#### Scenario: Stop and start a site
- **WHEN** an operator runs `controlai site stop tnt_acme-corp ste_seoul`
- **THEN** controlai SHALL set `desired_state=stopped`, the reconciler SHALL bring the site's containers down within 30 s, and `controlai site start tnt_acme-corp ste_seoul` SHALL bring them back to running within 60 s

#### Scenario: Conservative delete preserves data
- **WHEN** an operator runs `controlai tenant rm tnt_acme-corp` without `--purge`
- **THEN** controlai SHALL remove the TSDB container and all site containers but SHALL preserve `/var/lib/controlai/tenants/tnt_acme-corp/` and all Docker volumes; the tenant SHALL appear as `orphaned` in `controlai status` until the operator either restores it or runs `rm --purge`

#### Scenario: Purge delete removes data
- **WHEN** an operator runs `controlai tenant rm tnt_acme-corp --purge` and confirms the prompt
- **THEN** controlai SHALL stop all related containers, remove all related volumes, delete `/var/lib/controlai/tenants/tnt_acme-corp/`, delete the tenant's backups under `/var/backups/controlai/tnt_acme-corp/`, and remove the tenant row from SQLite

### Requirement: YAML schema versioning and migration

`tenant.yaml` and `site.yaml` SHALL carry a `schema_version` integer
field. controlai SHALL refuse to start its daemon when any registry YAML
file declares a schema version newer than the binary supports. controlai
SHALL provide a `controlai migrate` command that walks the registry and
applies registered version-to-version transformations with backups.

#### Scenario: Daemon refuses unknown schema version
- **WHEN** any tenant or site YAML declares `schema_version` greater than the binary's supported maximum
- **THEN** the daemon SHALL log the offending file and exit with a non-zero code before opening listeners

#### Scenario: Migrate applies pending transformations
- **WHEN** an operator runs `controlai migrate` against a registry where some files are at schema_version=1 and the binary supports up to version=2
- **THEN** controlai SHALL write a `.bak` for each affected file and rewrite each one to `schema_version: 2`, emitting an `audit_event(kind=migrate.apply)` per file

