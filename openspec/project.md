# Project Context

## Purpose

`controlai` is a modular control plane that provisions and operates per-tenant
IoT data-pipeline stacks (broker → ingest → TimescaleDB) on a single EC2 host
via docker-compose. Each tenant gets a fully isolated full stack; each tenant
may operate one or more sites (physical locations / brokers) sharing the
tenant's TimescaleDB. The tool exposes the pluggable choices — broker
implementation (mosquitto / EMQX), ingest direction (uni / bi), throughput
preset, retention window — as YAML configuration that re-renders the compose
files and selectively restarts only changed containers.

Initial deployment target: single AWS EC2 (t3.medium for PoC, ≤ ~5 active
tenants). Future targets: larger EC2 instance types and eventual Kubernetes
abstraction (the renderer must keep that path open).

## Tech Stack

- Language: **Go 1.22+** (single static binary, runs as systemd daemon and CLI)
- Compose: **docker compose v2** (host-installed; controlai shells out)
- Reverse proxy / edge: **Traefik v3** (HTTP routing by domain, MQTT routing by SNI on :8883)
- Broker options: **mosquitto 2.x**, **EMQX 5.x**
- Time-series store: **TimescaleDB 2.x on PostgreSQL 16** (one container per tenant)
- Ingest service: Go binary (own subproject), MQTT subscriber → batched COPY into TimescaleDB
- Metadata: **SQLite** (`/var/lib/controlai/controlai.db`) for tenant/site registry, port pool, status
- Config: **YAML** (per-tenant `tenant.yaml`, per-site `site.yaml`)
- Templating: Go `text/template`
- CLI: cobra
- Daemon API: net/http with chi router; localhost + optional TLS
- Tests: standard `testing` + `testify`; integration tests using docker compose

## Project Conventions

### Code Style

- `gofmt` + `goimports` enforced; no exceptions.
- Package layout follows `cmd/<binary>` + `internal/<domain>` Go convention.
- Errors wrapped with `fmt.Errorf("...: %w", err)`; no naked `panic` outside `main`.
- Configuration types in `internal/config` are pure data structs with explicit
  YAML tags; no business logic.
- Reference project: `/Users/8bitnyan/Documents/ThinkTank/modules_cloud-main` —
  for style and ingest service patterns. We are NOT copying its code; we are
  re-implementing the equivalent as a per-tenant template-driven artifact.

### Architecture Patterns

```
CLI / REST API (cobra + chi)
        │
        ▼
   internal/store  ── SQLite registry (tenants, sites, ports, status)
        │
        ▼
   internal/render ── text/template → compose + deploy/** files
        │
        ▼
   internal/runner ── exec `docker compose -p <project>` selectively
        │
        ▼
   internal/recon  ── reconciler loop: desired (registry) vs actual (docker ps)
```

- **Tenant-Site hierarchy:** one tenant owns N sites; each site has its own
  broker + ingest containers; TimescaleDB is per-tenant and shared across the
  tenant's sites.
- **No host ports** for broker/HTTP services. Traefik is the only host-bound
  entrypoint (:80, :443, :8883). All tenant containers are reachable through
  Traefik routers keyed on domain (HTTP) or SNI (MQTT).
- **Compose project per tenant** (e.g. `-p tnt_abc_tsdb`, `-p tnt_abc_site_seoul`).
  Naming guarantees container/volume/network isolation between tenants and
  between sites within a tenant.
- **Renderer is k8s-aware in shape** (separate template package, no hard
  compose calls inside the renderer) so a future Helm/K8s backend can replace
  the `runner` package without touching templates.
- **Reconciler-first.** All state changes go through the SQLite desired-state
  table; the reconciler reads it and converges. CLI commands write desired
  state and optionally wait for convergence.
- **Capacity guard.** Before creating a new site/tenant, the daemon estimates
  RAM/CPU additions from a static profile table (mosquitto vs EMQX, throughput
  tier) and rejects allocations that would push usable RAM below 15% headroom.

### Testing Strategy

- Unit tests on `internal/render`, `internal/config`, `internal/store` with
  table-driven cases.
- Golden-file tests for rendered compose / mosquitto.conf / emqx.conf /
  init.sql output (snapshot diff on schema changes).
- Integration tests behind a `// +build integration` tag that actually run
  `docker compose up` against a mocked gateway, assert message flow into
  TimescaleDB. Requires Docker on the test host.
- Capacity-guard tests use injected fake `/proc/meminfo` and synthetic
  registry snapshots.

### Git Workflow

- Single trunk (`main`). Feature work via short-lived branches `feat/...`,
  `fix/...`, `refactor/...`.
- Conventional commit subject lines (`feat:`, `fix:`, `chore:`, `docs:`,
  `refactor:`, `test:`).
- OpenSpec changes land alongside code; archive after deployment per
  `openspec/AGENTS.md` Stage 3.

## Domain Context

- **Tenant** = a customer / organization that owns one or more sites and one
  per-tenant TimescaleDB.
- **Site** = a physical location (factory, building, field deployment) with
  its own broker and ingest containers. A site is the unit of broker-protocol
  / implementation / throughput configuration.
- **Gateway** = the customer's hardware device that connects to a site's
  broker over MQTT(S). Out of scope for controlai itself — controlai only
  provisions the receiving side.
- **Uni mode ingest** = MQTT subscribe → TimescaleDB write only.
- **Bi mode ingest** = adds a downlink path: REST API on the daemon → MQTT
  publish back to the gateway on per-site command topics.
- **Throughput tier** = a named preset that maps to ingest replica count,
  batch size, flush interval, and MQTT QoS choices. Initial tiers: `low`
  (≤100 msg/s), `mid` (≤1000 msg/s). High tier (≥3000 msg/s) is documented as
  "requires dedicated EC2; not supported on t3.medium PoC".
- **Retention** = TimescaleDB `add_retention_policy` on the per-tenant
  hypertable; controlai exposes presets `1m`, `1h`, `1d`, `7d`, `30d`.

## Important Constraints

- **Single-host MVP.** No multi-host orchestration; no Swarm/K8s. Future-ready,
  not future-implemented.
- **t3.medium PoC ceiling.** 4 GiB RAM, 2 vCPU burstable. Capacity guard hard
  limit derived from per-component RSS budgets (see `openspec/changes/.../design.md`).
- **No host-port expose** other than Traefik. Tenant brokers/ingests are
  Traefik upstreams only.
- **Wildcard DNS required** for SNI routing in production; `/etc/hosts`
  acceptable for local development against `*.controlai.local`.
- **PoC ≤ 5 active tenants on t3.medium.** Beyond that, instance must be
  upgraded; controlai refuses new tenants past the capacity threshold.
- **OpenSpec is the source of truth** for feature scope. All non-trivial
  changes go through a change proposal before implementation.

## External Dependencies

- **docker engine + docker compose v2** — required on the host. controlai
  shells out; it does not bundle docker.
- **Traefik v3 image** (`traefik:v3.x`) — pulled at `controlai shared init`.
- **TimescaleDB image** (`timescale/timescaledb:2.x-pg16`) — pulled per tenant.
- **mosquitto image** (`eclipse-mosquitto:2.x`) — pulled per site.
- **EMQX image** (`emqx/emqx:5.x`) — pulled per site.
- **No external SaaS** dependencies in MVP. ACME / Let's Encrypt for cert
  issuance in Traefik is optional and behind a feature flag.

## Reference Project

`/Users/8bitnyan/Documents/ThinkTank/modules_cloud-main` is a single-tenant Go
implementation of essentially the same data path (EMQX → Go ingestor →
TimescaleDB → REST API → React web). It is the source of:

- Ingest service patterns (MQTT subscribe, batched insert, codec layer).
- TimescaleDB init / hypertable / migration patterns.
- EMQX ACL conventions for per-group topic isolation.

It is NOT used as a library. controlai re-implements the relevant pieces as
parameterized templates and a smaller ingest binary.
