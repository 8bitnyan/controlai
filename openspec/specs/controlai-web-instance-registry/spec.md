# controlai-web-instance-registry Specification

## Purpose
TBD - created by archiving change add-controlai-web-skeleton. Update Purpose after archive.
## Requirements
### Requirement: ControlaiInstance entity with encrypted bearer token

`controlai-web` SHALL define a `ControlaiInstance` entity scoped to an `Organization`.
Each instance represents a running controlai daemon at a known HTTPS base URL. The
bearer token used to authenticate BFF requests to the daemon SHALL be stored encrypted
at rest using AES-256-GCM with a server-side key (`INSTANCE_TOKEN_KEY` env var). The
plaintext token SHALL never be stored in Postgres or logged. Only OWNER and ADMIN may
register, update, or delete instances; MEMBER may list instances (without token) and
view status.

#### Scenario: Register an instance

- **WHEN** an OWNER calls `instance.register({ orgId, name, baseURL, bearerToken })`
- **THEN** the BFF SHALL call `GET <baseURL>/v1/health` with `Authorization: Bearer <bearerToken>` (10 s timeout) to validate connectivity
- **AND** if the health check returns HTTP 200, the instance SHALL be inserted with `bearerTokenEnc = encrypt(bearerToken)` and `status = HEALTHY`
- **AND** if the health check fails, the API SHALL return an error: "Cannot reach daemon at <baseURL>" and NOT insert the instance

#### Scenario: Token encryption at rest

- **WHEN** an instance is registered
- **THEN** the `bearerTokenEnc` column in Postgres SHALL contain the AES-256-GCM ciphertext, NOT the plaintext token
- **AND** `SELECT * FROM "ControlaiInstance"` in Postgres Studio SHALL NOT reveal the plaintext token

#### Scenario: Missing encryption key at startup

- **WHEN** `INSTANCE_TOKEN_KEY` is absent from the environment at app startup
- **THEN** the process SHALL exit with a non-zero code and print: "INSTANCE_TOKEN_KEY is required"

#### Scenario: Test connection on existing instance

- **WHEN** an ADMIN calls `instance.testConnection({ instanceId })`
- **THEN** the BFF SHALL decrypt the token, call `GET <baseURL>/v1/health` with a 10 s timeout, and return `{ status: 'HEALTHY' | 'UNREACHABLE', version?, capacityUsedMB?, capacityAllowedMB? }`

#### Scenario: MEMBER cannot register or delete instance

- **WHEN** a user with role MEMBER calls `instance.register` or `instance.delete`
- **THEN** the API SHALL return `TRPCError({ code: 'FORBIDDEN' })`

### Requirement: Instance health polling via Vercel Cron

The BFF SHALL poll every registered `ControlaiInstance`'s `/v1/health` endpoint
approximately every 60 seconds using Vercel Cron (schedule `* * * * *`). Each poll
SHALL update `status`, `lastSeenAt`, `version`, `capacityUsedMB`, and `capacityAllowedMB`
in the `ControlaiInstance` row. An instance that fails 3 consecutive polls SHALL have its
`status` set to `UNREACHABLE`. An instance that recovers after being `UNREACHABLE` SHALL
have its `status` reset to `HEALTHY`.

#### Scenario: Healthy poll updates lastSeenAt

- **WHEN** the Vercel Cron job runs and `GET /v1/health` returns HTTP 200 with `{"status":"healthy","version":"0.0.3","capacity":{"used_mb":512,"allowed_mb":3276}}`
- **THEN** the `ControlaiInstance` row SHALL be updated with `status = HEALTHY`, `lastSeenAt = now()`, `version = "0.0.3"`, `capacityUsedMB = 512`, `capacityAllowedMB = 3276`

#### Scenario: Unreachable after 3 failures

- **WHEN** 3 consecutive Vercel Cron polls for an instance result in a network error or non-200 response
- **THEN** the `ControlaiInstance.status` SHALL be set to `UNREACHABLE`
- **AND** the instance card in the UI SHALL display an "Unreachable" badge

#### Scenario: Recovery resets status

- **WHEN** a previously `UNREACHABLE` instance returns HTTP 200 on a poll
- **THEN** `status` SHALL be reset to `HEALTHY` and `lastSeenAt` updated

#### Scenario: Cron secured against public invocation

- **WHEN** a request reaches `GET /api/cron/instance-health` without the correct `CRON_SECRET` header
- **THEN** the handler SHALL return HTTP 401 and NOT poll any instances

### Requirement: Manual bearer token rotation

Operators SHALL be able to update the bearer token for a registered instance via the UI
without re-registering the instance. The old token SHALL be replaced in storage; the UI
SHALL confirm the new token is valid before saving. The old token MUST be revoked on the
daemon side separately using `controlai token revoke <name>`.

#### Scenario: Token update succeeds

- **WHEN** an OWNER submits a new bearer token for an existing instance via `instance.update({ instanceId, bearerToken })`
- **THEN** the BFF SHALL test the new token against `GET /v1/health`, and if HTTP 200, store `encryptToken(newToken)` in `bearerTokenEnc`

#### Scenario: Invalid new token rejected

- **WHEN** an OWNER submits a bearer token that does not authenticate against the daemon (daemon returns 401)
- **THEN** the update SHALL fail with an error: "New token rejected by daemon (401)"
- **AND** the existing `bearerTokenEnc` SHALL NOT be overwritten

#### Scenario: Delete instance blocked when projects depend on it

- **WHEN** an OWNER calls `instance.delete({ instanceId })` and one or more Projects reference this instance
- **THEN** the API SHALL return an error listing the project names that depend on the instance
- **AND** the instance SHALL NOT be deleted

