# Design: controlai MVP

## Context

controlai is a single-host (initially AWS EC2 t3.medium PoC) control plane
that provisions and operates many small per-tenant IoT data pipelines.

Source of validated patterns: `/Users/8bitnyan/Documents/ThinkTank/modules_cloud-main`.
That project ships a working single-tenant Go stack:

- `cmd/ingestor/main.go:23-85` — MQTT subscribe → CBOR decode → single-row
  pgx INSERT, ~10 s context timeout per message.
- `internal/mqtt/client.go:46-203` — `autopaho.ConnectionManager`, mTLS via
  CA + client cert/key, QoS-bytes subscribe, dispatch callback.
- `internal/codec/cbor.go:24-142` — `fxamacker/cbor` decoding, payload
  struct + loose-map fallback + downlink command codec.
- `internal/store/telemetry.go:11-27` — single-row `pgxpool.Exec` with
  `$4::jsonb` cast, no batching, no COPY.
- `migrations/0001_init.up.sql:46-54` — `telemetry(time, gateway_id,
  msg_type, payload jsonb, raw bytea)` hypertable with chunk 7d. No
  retention / compression / continuous aggregates.
- `deploy/emqx/emqx.conf:10-103` — EMQX 5.7 mTLS listener on :8883,
  `peer_cert_as_username=cn`, `peer_cert_as_clientid=md5`, file ACL +
  HTTP authn callback.
- `internal/pki/{ca,issue,server_cert,crypto}.go` — RSA-2048 self-signed CA
  (AES-256-GCM key wrap), leaf issuance with CN=GroupID, server cert with
  ServerAuth+ClientAuth EKU.
- `internal/broker/files.go:31-238` — `EnsureEMQXTrustFiles()` rotation
  triggers: missing / CA-mismatch / ≤30 d expiry / SAN drift.
- `internal/emqx/{client,banned,listener}.go` — REST API client with Basic
  Auth, `POST /api/v5/banned`, `POST /api/v5/listeners/ssl:default/restart`.

The reference is **not** a library dependency. controlai re-implements the
relevant pieces as parameterized templates and a smaller ingest binary, and
adds the multi-tenant control plane the reference lacks.

## Goals / Non-Goals

**Goals.**

- One operator can stand up and tear down many isolated tenant stacks on a
  single host via declarative YAML.
- Changing a single YAML field reliably restarts only the affected
  container(s) without recreating dependencies.
- Adding a new tenant or site is admission-controlled by static capacity
  prediction; surprise OOMs are not possible from controlai admissions.
- The REST API contract is stable enough that a later web GUI / SaaS
  control panel speaks the same protocol the CLI uses today.
- One Go binary embeds CLI, REST daemon, ingest service launcher, and
  reconciler. (The ingest *runtime* is a separate `services/ingest`
  binary that ships inside every site container.)

**Non-Goals.**

- Multi-host orchestration. Swarm and Kubernetes are explicitly deferred.
- Web UI. The REST contract is the deliverable; UI is a follow-up.
- High-throughput tier (≥3000 msg/s). Documented as requiring t3.large+.
- Buying into a third-party orchestrator (Coolify / Dokploy / CapRover).
  We borrow patterns from them but stay in-house.

## Decisions

### D1. Tenant-Site hierarchy, TSDB per tenant

A tenant owns N sites. Each site has its own broker + ingest containers.
TimescaleDB is **per tenant** and shared across that tenant's sites. This
addresses the user's "한 유저가 여러 현장을 통합" requirement (unified queries
across sites within one tenant) while preserving cross-tenant isolation.

Alternative considered: TSDB per site. Rejected — duplicating Postgres for
every site burns RAM (~350 MB each) without any cross-tenant safety benefit
the per-tenant TSDB doesn't already provide.

### D2. Traefik SNI passthrough on :8883 + dynamic file provider

Port-per-broker exposure scales poorly past a handful of sites. Traefik v3
TCP routers with `HostSNI(...)` on a single :8883 entrypoint route MQTT
connections by ServerName to the correct upstream broker container. TLS is
**passthrough** because TCP routers cannot forward client certificates to
the backend — mTLS termination must happen at the broker for cert-CN-based
auth to work (validated in research note `traefik-mqtt-sni-routing.md`).

Configuration source: Traefik file provider with `watch: true` pointed at
`shared/traefik/dynamic/`. The controlai daemon writes YAML files
atomically (tmp + `os.Rename`) to avoid fsnotify misses.

Alternative considered: port-per-tenant offsets. Rejected — coordination
nightmare past 20 sites and not friendly to firewall / load-balancer rules.

### D3. Renderer is template-only; runner is the only docker-compose caller

`internal/render` produces files. `internal/runner` shells out to
`docker compose -p <project> -f <file> up -d --no-deps <services>`. The
renderer never invokes docker. This keeps a future K8s backend a drop-in
swap of the runner package.

Concurrency: per-project `sync.Mutex` + global `semaphore.Weighted(15)` to
cap simultaneous compose invocations and prevent docker socket pressure.
This matches the pattern documented in
`go-docker-compose-orchestration.md`. We do **not** import
`github.com/docker/compose/v5` — that adds ~50 MB to the binary and pulls
in significant transitive deps. We do import the Docker engine SDK
(`github.com/docker/docker/client`) for the reconciler's **read** path
(`ContainerList` filtered by `com.docker.compose.project`, plus
`ContainerInspect` for `State.Health.Status`).

### D4. SQLite registry, file-tree config

State of record:

```
/var/lib/controlai/
  controlai.db                # SQLite: tenants, sites, audit_events, settings
  shared/
    docker-compose.yml      # rendered Traefik service
    traefik/
      static.yml
      dynamic/              # per-site / per-tenant YAML, atomically written
  tenants/
    <tenant_id>/
      tenant.yaml           # human-edited, schema_version pinned
      tsdb/
        docker-compose.yml
        init.sql
        data/               # PG data volume mount
      sites/
        <site_id>/
          site.yaml         # human-edited, schema_version pinned
          docker-compose.yml
          deploy/           # mosquitto.conf | emqx.conf, acl.conf, certs
          data/             # broker persistence volume mount
```

SQLite is for fast queries (status, capacity admission, audit), not the
source of truth for desired state. The YAML files are. The reconciler
reads YAML + SQLite and writes rendered artifacts + actual-state cache.

### D5. Ingest binary architecture

One Go binary in `services/ingest/`. It is the runtime inside every
site's `ingest` container. controlai builds this once (or pulls a tagged
image from a registry); per-site differences are driven by env vars and a
mounted `site.yaml` snapshot.

Patterns adapted from the reference:

- MQTT: `eclipse/paho.golang/autopaho`, mTLS via mounted CA + client cert.
- Codec: per-site `payload_codec` field selects `cbor` (using
  `fxamacker/cbor`), `json`, or `raw_passthrough` (stored as `bytea`).
- Storage: `jackc/pgx/v5` with multi-row INSERT batching. The reference's
  single-row `Exec` per message is **not** copied — at the documented
  mid tier (1000 msg/s) it would consume too much CPU. Batch parameters:
  - low: batch=200, flush=1000 ms
  - mid: batch=1000, flush=500 ms
- Bi mode: daemon REST `POST /v1/tenants/<t>/sites/<s>/publish
  {topic, payload, qos}` forwards into the ingest's per-site command
  channel (controlai-daemon → site ingest over an internal unix socket
  inside the docker network, or via direct MQTT publish from the daemon
  using its own ingestor client cert — to be decided in implementation,
  with the simpler "daemon publishes directly" path preferred for MVP).

### D6. Throughput tiers map to a fixed table

`low` and `mid` only in MVP. `high` is reserved for future EC2-larger
deployments and is **rejected at validation** on t3.medium.

| tier | batch | flush  | replicas (EMQX) | replicas (mosquitto) |
| ---- | ----- | ------ | --------------- | -------------------- |
| low  | 200   | 1000ms | 1               | 1                    |
| mid  | 1000  | 500ms  | 2               | 1                    |
| high | 2000  | 200ms  | 4               | rejected             |

Mosquitto is capped at 1 replica because mosquitto 2.x has no MQTT5
shared-subscription support; a second replica would double-receive every
message. EMQX uses `$share/<site>/<topic>` for the second replica.

### D7. Retention is per tenant

One `add_retention_policy` per tenant's `telemetry` hypertable. Allowed
values: `1m`, `1h`, `1d`, `7d`, `30d`. The compression policy is fixed at
`add_compression_policy(..., INTERVAL '7 days')` when retention ≥ 7 d, off
otherwise. Source: `timescaledb-low-ram-tuning.md`.

### D8. Daemon API: two transports, one schema

Default transport: **unix socket** at `/var/run/controlai.sock`, mode 0660,
owner `controlai:controlai`. The CLI uses this.

Optional transport: **TCP + TLS** bound to a configured port, behind a
bearer token, for remote operators or a future GUI. Both transports
expose the same `chi`-routed HTTP handlers.

Token format: opaque random 32-byte base64 stored under
`/var/lib/controlai/tokens/` with restricted permissions. CLI also accepts
a token to handle the case where it talks to a remote daemon.

### D9. PKI module: per-site CA, AES-GCM key wrap

Per-site CA. CN-of-leaf == device identifier (operator-provided gateway
name, slugified, ≤63 chars). Leaf TTL: 365 d default, configurable per
site. Server cert TTL: 10 y (fixed). The reference's `internal/pki`
patterns are re-implemented; we do not copy code.

Master key for AES-GCM CA-key wrap: read from
`CONTROLAI_CA_KEY_ENCRYPTION_KEY` env var. Plaintext fallback only when
`controlai_dev_mode=true` in config; the daemon refuses to start in
production mode without the env var.

### D10. Capacity-guard math

Static profile table at `internal/capacity/profile.go`, regenerated by a
benchmark script (`scripts/measure-rss.sh`) but checked into source.

Per-host fixed overhead: 530 MB (Docker engine + Traefik + daemon + OS).

Per-tenant: 350 MB (Postgres with 64 MB shared_buffers preset).

Per-site by `(broker.kind, throughput.tier, ingest.direction)`:

|              | low mosquitto uni | mid mosquitto uni | low EMQX uni | mid EMQX uni | bi adds |
| ------------ | ----------------- | ----------------- | ------------ | ------------ | ------- |
| broker (MB)  | 20                | 25                | 400          | 450          | —       |
| ingest (MB)  | 60                | 90                | 60           | 120          | +20     |

Admission rule (with `usable = /proc/meminfo.MemTotal - 530`):

```
sum(per_tenant_rss for all tenants_after) +
sum(per_site_rss   for all sites_after  ) <= usable * 0.85
```

A new tenant or site is refused if it would push the projected total over
85 % of usable. `controlai capacity` returns a structured what-if.

### D11. Reconciler

Loop frequency 30 s. Per-iteration:

1. Read `tenants` + `sites` + `desired_state` from SQLite.
2. `ContainerList` filtered by `com.docker.compose.project` (Docker SDK).
3. For each project that differs from desired (missing, mismatched
   config-hash, unhealthy):
   - acquire per-project mutex,
   - render (if hash drift),
   - run `docker compose ... up -d --no-deps <services>` via runner,
   - on error: exponential backoff (30 s → 1 m → 5 m → 30 m cap),
   - emit `audit_event(kind=reconciler.*)`.

A `controlai apply` command is a thin wrapper that writes a desired-state
row and then blocks on convergence by polling SQLite for up to N seconds.

### D12. Payload codec is per-site

`site.yaml.payload_codec` ∈ `cbor | json | raw_passthrough`. Validation
enforces a matching column type expectation in TSDB:

- `cbor` and `json` → `payload jsonb` populated (raw stored as `bytea`).
- `raw_passthrough` → `payload` is `NULL`, `raw bytea` populated. Indexed
  on `(site_id, time DESC)` only.

### D13. MQTT topic contract

Fixed pattern: `<tenant_id>/<site_id>/<device_id>/<metric>`.
Allowed character class: `[a-z0-9-]`. controlai authn webhook on EMQX
returns an inline ACL restricting publish/subscribe to
`<tenant_id>/<site_id>/+/+`. Mosquitto uses a generated file ACL with
equivalent semantics.

The reference's Sparkplug-B-ish `modules/{group}/{type}/{node}/{device}`
pattern is **not** adopted — over-fitted to that project's domain.

### D14. Schema versioning + migrate

`tenant.yaml` and `site.yaml` carry `schema_version: <int>`. controlai
ships an `internal/migrate/yaml/` package with version-to-version
transformers. `controlai migrate` walks the file tree, applies pending
migrations, writes back. Refuses to start a daemon against a registry
whose YAML schema is newer than the binary.

## Risks / Trade-offs

| Risk                                              | Mitigation                                                                                                          |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Capacity profile drifts from real RSS             | `scripts/measure-rss.sh` benchmark + assertion test in CI that the checked-in table matches a measured baseline ±15 %. |
| Traefik file-provider misses an event             | Atomic rename + retry loop in dynamic-config writer; integration test that asserts route appears within 5 s.        |
| docker compose v2 schema drift                    | Pin in `templates/` to compose spec `3.9`; integration test that lints rendered output with `docker compose config`.|
| TSDB OOM under burst                              | Compose `deploy.resources.limits.memory` enforced; ingest backs off with circuit breaker on Postgres connection errors. |
| Reconciler races with manual `docker compose down` | Reconciler treats absence as drift; admin command `controlai pause <id>` writes a desired=stopped row.              |
| EMQX REST API auth required                       | Renderer writes `api_keys.bootstrap` from a daemon-generated key; key stored in SQLite, fetched by reconciler.      |
| mTLS clock skew on gateways                       | Document NTP requirement; leaf cert NotBefore set to (now − 5 min).                                                 |

## Migration Plan

This is a greenfield change; there is no controlai data to migrate.

For the YAML schema itself: every `tenant.yaml` and `site.yaml` written by
controlai includes `schema_version: 1`. Future changes that bump the schema
version provide a migration in `internal/migrate/yaml/`; rollback is
"write the old schema by hand and restart daemon".

## Open Questions

- Bi-mode downlink internal transport (daemon → ingest publish path):
  daemon-publishes-directly vs internal unix socket inside the docker
  network. Resolve in implementation task 6.4.
- Multi-replica EMQX ingest: whether `$share/<site>/<topic>` needs a
  per-site EMQX shared-subscription group name vs the default. Resolve in
  implementation task 4.3.
- Whether Let's Encrypt should be on by default in production mode or
  remain a feature flag. Currently flagged; revisit after first production
  deployment.
