## ADDED Requirements

### Requirement: REST API exposed over unix socket and optional TLS TCP

controlai SHALL expose a single REST API surface under `/v1/` reachable
via two transports: a unix domain socket at `/var/run/controlai.sock`
(mode 0660, owner `controlai:controlai`) used by the CLI by default,
and an optional TCP+TLS listener bound to a configurable address that a
remote operator or future web GUI consumes. Both transports SHALL serve
identical handlers.

#### Scenario: CLI uses unix socket by default
- **WHEN** the CLI is run without `--remote`
- **THEN** the CLI SHALL connect over the unix socket and SHALL never require a network token

#### Scenario: Remote GUI uses the same routes
- **WHEN** a client connects over the TCP+TLS listener with a valid bearer token to `GET /v1/tenants`
- **THEN** the daemon SHALL respond with the identical JSON payload it would have returned to a CLI request

### Requirement: Bearer-token authentication on TCP transport

The TCP+TLS transport SHALL require `Authorization: Bearer <token>` on
every request. Tokens SHALL be stored in the `auth_tokens` SQLite table
with a SHA-256 hash, never in plaintext, and SHALL be creatable, listable
(by display name and prefix), and revocable via CLI.

#### Scenario: Missing token rejected
- **WHEN** a request to the TCP listener arrives without an `Authorization` header
- **THEN** the daemon SHALL respond HTTP 401 with `WWW-Authenticate: Bearer`

#### Scenario: Token revocation takes effect immediately
- **WHEN** an operator runs `controlai token revoke <id>`
- **THEN** subsequent requests using that token SHALL fail HTTP 401 within 1 s

### Requirement: Stable REST contract documented as OpenAPI 3.1

The full REST contract SHALL be specified in `docs/api/openapi.yaml`
covering all `/v1/` endpoints. The contract SHALL be the source of
truth that the CLI and any future GUI build against.

#### Scenario: OpenAPI spec is self-consistent
- **WHEN** `openapi-cli validate docs/api/openapi.yaml` runs in CI
- **THEN** it SHALL exit zero

### Requirement: Core endpoint set

controlai SHALL implement at minimum:
- `GET /v1/health`
- `GET /v1/capacity`
- `POST | GET | PATCH | DELETE /v1/tenants[/{id}]`
- `POST | GET | PATCH | DELETE /v1/tenants/{tid}/sites[/{sid}]`
- `POST /v1/tenants/{tid}/sites/{sid}/publish` (bi-mode downlink)
- `GET /v1/tenants/{tid}/sites/{sid}/logs?service=<name>&tail=N`
- `POST /v1/apply/{selector}` (blocking convergence wait)

#### Scenario: Health endpoint reports component status
- **WHEN** any client calls `GET /v1/health`
- **THEN** the response SHALL include the daemon version, the docker engine reachability, the SQLite registry health, and the reconciler last-tick timestamp

### Requirement: CLI never bypasses the API

The `controlai` CLI SHALL invoke its functionality exclusively through
the daemon REST API. The CLI SHALL NOT directly read or write SQLite,
the file tree under `/var/lib/controlai/`, or invoke `docker compose`.

#### Scenario: Daemon down implies CLI cannot operate
- **WHEN** the daemon is stopped
- **THEN** every CLI subcommand other than `controlai version`, `controlai install`, and `controlai daemon start` SHALL fail with a clear "daemon unavailable" error
