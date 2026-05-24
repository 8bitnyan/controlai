# controlai-web-domain Specification

## Purpose
TBD - created by archiving change add-controlai-web-skeleton. Update Purpose after archive.
## Requirements
### Requirement: Organization entity and membership model

`controlai-web` SHALL define an `Organization` entity as the top-level multi-tenant
boundary. Each `Organization` SHALL have a unique URL-safe slug, a human-readable name,
and timestamps. Users belong to organizations via `OrganizationMember` with roles:
`OWNER`, `ADMIN`, or `MEMBER`. An org SHALL have at most one OWNER at a time; the OWNER
role may be transferred but not deleted directly. A user may belong to multiple orgs.

#### Scenario: Create organization

- **WHEN** an authenticated user calls `org.create({ name, slug })`
- **THEN** an `Organization` row SHALL be created and the user added as OWNER via `OrganizationMember`

#### Scenario: Org slug globally unique

- **WHEN** two users attempt to create orgs with the same slug
- **THEN** the second attempt SHALL fail with HTTP 409

#### Scenario: Delete org blocked with active projects

- **WHEN** an OWNER calls `org.delete` on an org that still has Projects
- **THEN** the API SHALL return an error: "Cannot delete org with active projects"

### Requirement: Project entity scoped to an Organization and ControlaiInstance

Each `Project` SHALL belong to one `Organization` and SHALL require a `controlaiInstanceId`
pointing to a `ControlaiInstance` in the same org. The project represents a logical
grouping of SiteGroups that share the same daemon instance. Only org OWNER and ADMIN may
create, update, or delete projects; MEMBERs may read (D30).

#### Scenario: Create project

- **WHEN** an OWNER or ADMIN calls `project.create({ orgId, instanceId, name })`
- **THEN** a `Project` row SHALL be created with `orgId` and `instanceId` set
- **AND** the `instanceId` SHALL reference an existing `ControlaiInstance` in the same org

#### Scenario: Instance in different org blocked

- **WHEN** a user attempts to create a project with an `instanceId` belonging to a different org
- **THEN** the API SHALL return `TRPCError({ code: 'FORBIDDEN' })`

#### Scenario: MEMBER cannot create project

- **WHEN** a user with role MEMBER calls `project.create`
- **THEN** the API SHALL return `TRPCError({ code: 'FORBIDDEN' })`

### Requirement: SiteGroup entity — logical site grouping

A `SiteGroup` SHALL represent a logical physical location (factory, building, field
deployment) within a Project. A SiteGroup groups one or more Sites that share the same
geographical location but may use different broker protocols. Only OWNER and ADMIN may
create or delete SiteGroups; MEMBER may list and read.

#### Scenario: Create site group

- **WHEN** an OWNER calls `siteGroup.create({ projectId, name })`
- **THEN** a `SiteGroup` row SHALL be created under the specified project

#### Scenario: SiteGroup listing scoped to project

- **WHEN** any org member calls `siteGroup.list({ projectId })`
- **THEN** only SiteGroups belonging to that project SHALL be returned
- **AND** SiteGroups from other projects or orgs SHALL NOT appear

### Requirement: Site entity — daemon Tenant+Site mapping

A `Site` SHALL be the leaf entity in the hierarchy, representing one physical broker
deployment (one daemon `Tenant` + one daemon `Site`). A Site belongs to a SiteGroup. The
fields `controlaiTenantId` and `controlaiSiteId` SHALL be null until the "Apply"
operation in `add-controlai-web-pipeline-ux` provisions the tenant and site on the
daemon. The broker kind (mosquitto/EMQX), ingest direction (uni/bi), and throughput tier
SHALL be stored on the Site for display in the node editor.

#### Scenario: Create site with pending daemon mapping

- **WHEN** an OWNER creates a Site via `site.create({ siteGroupId, name })`
- **THEN** a `Site` row SHALL be created with `controlaiTenantId = null` and `controlaiSiteId = null`
- **AND** the site SHALL appear in the node editor canvas as "not provisioned"

#### Scenario: Daemon mapping populated after Apply

- **WHEN** the Apply operation in `add-controlai-web-pipeline-ux` successfully provisions a daemon Tenant+Site
- **THEN** `site.controlaiTenantId` and `site.controlaiSiteId` SHALL be updated
- **AND** the site SHALL appear in the node editor canvas as "provisioned"

#### Scenario: Delete site blocked if provisioned

- **WHEN** an OWNER calls `site.delete` on a Site that has `controlaiTenantId` set
- **THEN** the API SHALL return an error: "Site is provisioned — delete via Apply or manually decommission the daemon tenant first"

### Requirement: AuditLog — write path for v1

`controlai-web` SHALL write an `AuditLog` entry for every mutating tRPC procedure call.
Each entry SHALL record: `orgId`, `userId` (nullable for system actions), `action`
(string, e.g. `org.create`, `instance.register`), `targetId`, `targetType`, and optional
JSON `metadata`. In v1, the audit log has no UI — it is visible via Prisma Studio. The
write path SHALL NOT block the primary mutation; failures SHALL be logged but swallowed.

#### Scenario: Audit entry written on org create

- **WHEN** a user calls `org.create`
- **THEN** an `AuditLog` row SHALL be inserted with `action = "org.create"`, `targetId = <orgId>`, `userId = <userId>`

#### Scenario: Audit write failure does not block mutation

- **WHEN** the audit write throws (e.g. DB timeout)
- **THEN** the primary mutation (org create) SHALL still complete successfully
- **AND** the error SHALL be logged to the server console

#### Scenario: Audit log scoped to org

- **WHEN** an ADMIN calls `audit.list({ orgId })`
- **THEN** only audit entries for that org SHALL be returned
- **AND** entries from other orgs SHALL NOT appear

### Requirement: Domain CRUD UI pages — Org, Project, SiteGroup, Site

`controlai-web` SHALL provide Next.js App Router pages for all CRUD operations in the
domain hierarchy: Org settings (rename, members, invitations, danger zone), Project list
and creation, SiteGroup detail and Site list, and Site create/edit. All delete actions
SHALL present a confirmation dialog naming the resource before executing. Breadcrumb
navigation SHALL reflect the current hierarchy path on all (app) pages.

#### Scenario: Create a project from the project list page

- **WHEN** an OWNER or ADMIN clicks "New Project" on the project list page and fills in a name and selects an instance
- **THEN** a shadcn Dialog SHALL open with the create form; submitting SHALL call `project.create` and the new project card SHALL appear in the list without a full page reload

#### Scenario: Delete a SiteGroup with confirmation

- **WHEN** an OWNER clicks "Delete" on a SiteGroup card
- **THEN** a confirmation dialog SHALL appear with the SiteGroup name and "This action cannot be undone"
- **AND** only after confirming SHALL `siteGroup.delete` be called

#### Scenario: Site form persists broker config

- **WHEN** an OWNER fills in the Site create/edit form (broker kind, throughput, ingest direction, retention) and submits
- **THEN** the `Site` row SHALL be created or updated with all supplied fields persisted
- **AND** the site SHALL appear in the SiteGroup's site list with the correct broker kind badge

