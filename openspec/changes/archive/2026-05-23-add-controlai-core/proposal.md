# Change: Introduce controlai MVP — modular, multi-tenant IoT data-pipeline control plane on a single host

## Why

We need a tool that lets us provision and operate per-tenant IoT data
pipelines (gateway → MQTT broker → ingest → TimescaleDB) on a single EC2 host
through declarative YAML configuration. The reference single-tenant Go
project at `/Users/8bitnyan/Documents/ThinkTank/modules_cloud-main` proves the
data path but hard-codes one tenant, one broker (EMQX), one TSDB, and
host-port exposure — none of which scale to a multi-tenant SaaS model.

controlai replaces that pattern with:

- **Per-tenant full-stack isolation** (broker + ingest containers per site,
  one TimescaleDB per tenant covering all the tenant's sites).
- **Modular swap points**: broker implementation (`mosquitto` ↔ `EMQX`),
  ingest direction (uni / bi), throughput preset (low / mid), retention
  window (1m / 1h / 1d / 7d / 30d), telemetry payload format (cbor / json /
  raw_passthrough).
- **Traefik-fronted edge** with MQTT SNI passthrough on a single :8883 (no
  host-port exposure per broker, unbounded site count).
- **Declarative config + diff-driven apply** — change a YAML field, re-render
  templates, restart only the containers whose contents actually differ.
- **Capacity guard** that refuses new tenants/sites that would push usable
  RAM below a 15 % headroom on the host (PoC ceiling: ~5 active tenants on
  t3.medium).
- **CLI + REST API + unix-socket and TLS-TCP transports** so the same
  control plane that operators drive from a CLI today is consumed unchanged
  by a future web dashboard / SaaS GUI.

## What Changes

This change scaffolds the entire MVP across ten capabilities. All ADDED.

- **tenant-management** — `Tenant` and `Site` entities, registry, lifecycle
  (create / start / stop / rm / migrate), kebab-slug ID convention.
- **compose-rendering** — `text/template`-based renderer that produces
  `docker-compose.yml` + `deploy/**` config files per project (tenant TSDB,
  per-site broker + ingest, shared Traefik). Schema-version aware.
- **broker-modules** — pluggable broker renderers: `mosquitto 2.x` (file
  ACL) and `EMQX 5.x` (HOCON + ACL + REST API). Per-broker capability matrix
  enforced at validation time.
- **ingest-service** — single Go binary in `services/ingest/` that:
  subscribes to MQTT (autopaho), decodes per configured payload codec,
  batches into TimescaleDB via pgx multi-row INSERT, optionally exposes a
  per-site downlink publish endpoint for bi mode. Throughput tier maps to
  batch size, flush interval, replica count, MQTT5 shared subscription on
  EMQX.
- **timescale-provisioning** — per-tenant TimescaleDB 2.x container
  template, hypertable + retention + compression policies driven by
  `tenant.yaml`, low-RAM-tuned `postgresql.conf` profile, init.sql
  generator.
- **edge-routing** — shared Traefik v3 service in `shared/`, file provider
  with `watch: true`, dynamic YAML written atomically by controlai per
  site/tenant. MQTT SNI passthrough on :8883, HTTPS on :443.
- **pki-management** — per-site self-signed CA (AES-256-GCM wrapped),
  leaf-cert issuance for gateways, ingestor client cert, server cert for
  broker. Renewal triggers: missing / expired (≤30 days) / CA-mismatch /
  SAN-drift.
- **daemon-api** — chi-based REST API + cobra CLI. Two transports: unix
  socket (default) and optional TCP + TLS (for remote GUI). Bearer token
  auth, audit log.
- **capacity-guard** — static RSS profile table per
  `broker.kind × throughput.tier × ingest.direction`, 15 % headroom,
  refuses new tenant/site when overflow predicted. `controlai capacity`
  command for what-if queries.
- **reconciler** — background loop in the daemon: reads SQLite desired
  state, polls docker via SDK with `com.docker.compose.project` label
  filter, drives convergence via `docker compose -p ... up -d --no-deps`
  with per-project mutex + global semaphore of 15. 30 s base period,
  exponential backoff on failure, audit-event emission.

## Impact

- **Affected specs**: 10 new capabilities created (listed above).
- **Affected code**: Greenfield repo. Top-level Go module `controlai` with
  layout:
  - `cmd/controlai/` — CLI + daemon entrypoint
  - `internal/` — `config`, `store`, `render`, `runner`, `recon`,
    `daemon`, `pki`, `capacity`, `audit`
  - `services/ingest/` — separate `main` for the ingest binary that ships
    inside every site container
  - `templates/` — all `text/template` sources for compose + broker
    configs + TSDB init.sql + Traefik dynamic config
  - `migrations/` — SQLite migrations for the controlai registry + TSDB
    hypertable migrations rendered into per-tenant init.sql
- **External dependencies**: docker engine + `docker compose v2` on host,
  Traefik v3 image, TimescaleDB image, mosquitto image, EMQX image. No SaaS
  dependencies in MVP.
- **Out of scope for this change** (deferred to follow-up changes):
  - Web dashboard UI (REST contract is stable; UI is separate work).
  - High-throughput tier (≥3000 msg/s) — documented as t3.large+ only.
  - Kubernetes / Swarm backend. The renderer is structured to allow a
    future K8s runner package without touching templates.
  - M2M / HTTP / Kafka broker plugins. The broker-modules capability
    defines the plugin interface so they can be added as separate changes.
  - Multi-host orchestration. The daemon is single-host.
  - Continuous-aggregate / TSDB compression scheduling beyond the default
    `INTERVAL '7 days'` compression policy preset.
