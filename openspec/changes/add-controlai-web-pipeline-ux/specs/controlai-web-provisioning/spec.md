## ADDED Requirements

### Requirement: Apply dry-run preview — Plan synthesis

The BFF SHALL expose a `apply.preview({ siteGroupId })` tRPC procedure that loads the
active NodeConfig, fetches the current daemon state via `GET /v1/tenants` and site-level
endpoints, diffs the desired graph against current state, and returns an ordered
`Plan { planId, ops: Op[], planHash }`. The plan represents the minimal set of daemon
API calls needed to converge the daemon's state to match the graph. The plan SHALL be
displayed to the operator in the Apply modal before any mutation is executed.

#### Scenario: Preview shows required ops for new site

- **WHEN** a user clicks "Apply" on a canvas with a Broker node that has no daemon counterpart
- **THEN** `apply.preview` SHALL return a Plan containing at minimum `createTenant` and `createSite` ops
- **AND** the Apply modal SHALL list these ops with human-readable descriptions before the user confirms

#### Scenario: Preview shows empty plan when daemon matches graph

- **WHEN** the daemon state already matches the active NodeConfig
- **THEN** `apply.preview` SHALL return an empty `ops` list
- **AND** the Apply modal SHALL display "Nothing to apply — daemon is already up to date"

#### Scenario: Preview is non-destructive

- **WHEN** `apply.preview` is called
- **THEN** no daemon API calls SHALL be made and no Postgres mutations SHALL occur
- **AND** calling preview multiple times SHALL return the same plan (given unchanged NodeConfig and daemon state)

### Requirement: Apply commit — serial execution with reconciler polling

`apply.commit({ siteGroupId, planId })` SHALL validate the `planId` has not been
superseded (re-compute plan hash; reject if stale), then execute each `Op` sequentially
against the daemon REST API using the instance's decrypted bearer token. After each
mutating op, the BFF SHALL poll the daemon's `/v1/status` endpoint for up to 30 s to
confirm the reconciler has converged before proceeding to the next op. If any op fails,
the commit SHALL stop at the failed step and return a `Result` with `success: false` and
per-step outcomes.

#### Scenario: Successful serial apply

- **WHEN** the user confirms Apply on a 3-op plan (createTenant → createSite → issueCert)
- **THEN** the BFF SHALL execute all 3 ops in order, polling reconciler status between each
- **AND** the Apply modal SHALL show each op transitioning from "pending" → "running" → "success"
- **AND** `site.controlaiTenantId` and `site.controlaiSiteId` SHALL be updated in Postgres after `createSite` succeeds

#### Scenario: Apply stops at failure

- **WHEN** the third op of a 5-op plan returns a non-200 response from the daemon
- **THEN** the BFF SHALL NOT execute ops 4 and 5
- **AND** the Apply modal SHALL show op 3 as "failed" with the daemon's error message
- **AND** `Result.success` SHALL be `false`

#### Scenario: Idempotent retry on 409

- **WHEN** a `createTenant` op returns HTTP 409 (tenant already exists on daemon)
- **THEN** the BFF SHALL treat this as a success (idempotent create)
- **AND** apply SHALL proceed to the next op

#### Scenario: Stale planId rejected

- **WHEN** the NodeConfig changes between `apply.preview` and `apply.commit`
- **THEN** `apply.commit` SHALL detect the plan hash mismatch and return an error: "Graph changed since preview — re-run preview"

### Requirement: Apply AuditLog entry

Every `apply.commit` call SHALL write an `AuditLog` entry regardless of success or
failure. The metadata SHALL include: `planHash`, `opCount`, `successCount`, `success`,
and a list of failed op types.

#### Scenario: Audit entry on successful apply

- **WHEN** `apply.commit` completes with all ops successful
- **THEN** an `AuditLog` row SHALL be written with `action = "apply.commit"`, `metadata.success = true`

#### Scenario: Audit entry on partial failure

- **WHEN** `apply.commit` fails at op 2 of 4
- **THEN** an `AuditLog` row SHALL be written with `action = "apply.commit"`, `metadata.success = false`, `metadata.failedAt = 2`

### Requirement: Daemon API mapping from graph nodes

The BFF SHALL map NodeConfig nodes and edges to daemon API calls according to the
following rules: each Broker node maps to one daemon `Site` (and its parent `Tenant` if
not yet created); each Ingest node maps to the `ingest` config of its downstream Broker's
Site; each TimescaleDB node maps to the `tsdb` config of the parent Tenant; Sensor,
Gateway, and Monitoring nodes do NOT map to daemon resources (they are UI-only or
mqtt-bridge config).

#### Scenario: Broker node maps to daemon Site

- **WHEN** the plan includes a Broker node of kind `mosquitto` with throughput `low`
- **THEN** the `createSite` op request body SHALL include `{"broker":{"kind":"mosquitto"},"ingest":{"direction":"uni"},"throughput":"low"}`

#### Scenario: TimescaleDB retention maps to daemon config

- **WHEN** the plan includes a TimescaleDB node with retention `7d`
- **THEN** the `updateTsdb` op SHALL call `PATCH /v1/tenants/:id/tsdb {"retention":"7d"}`

### Requirement: Apply run history — last-apply status in canvas toolbar

The BFF SHALL persist each `apply.commit` call as an `ApplyRun` record (id,
siteGroupId, planHash, success, opCount, failedAt, createdAt, resultJson). A tRPC
`apply.status({ siteGroupId })` procedure SHALL return the most recent `ApplyRun` for a
SiteGroup. The canvas toolbar SHALL display "Last applied: {relative time}" (green) or
"Last apply failed" (red, with a re-run button) based on this record.

#### Scenario: Canvas toolbar shows last apply time

- **WHEN** a user views a SiteGroup canvas after a successful Apply
- **THEN** the canvas toolbar SHALL display "Last applied: {time}" in green text
- **AND** clicking it SHALL navigate to the most recent ApplyRun detail modal

#### Scenario: Canvas toolbar shows failure state

- **WHEN** the most recent `ApplyRun` for a SiteGroup has `success = false`
- **THEN** the canvas toolbar SHALL display "Last apply failed" in red text with a "Re-run" button
- **AND** clicking "Re-run" SHALL call `apply.preview` and open the Apply modal pre-filled with the failed plan

### Requirement: Apply error surfacing — daemon error detail in modal

When an `apply.commit` op fails, the Apply modal SHALL display the daemon's full HTTP
response body (truncated to 2 KB) in a monospace block alongside the failing op's human-
readable description. Operators SHALL NOT need to SSH to the EC2 host to diagnose a
failed apply step.

#### Scenario: Daemon error body displayed in modal

- **WHEN** a `createSite` op returns HTTP 500 with body `{"error":"capacity guard rejected: 15% headroom minimum not met"}`
- **THEN** the Apply modal SHALL display that error message under the failed step
- **AND** a "Re-run failed ops" button SHALL be visible in the modal footer
