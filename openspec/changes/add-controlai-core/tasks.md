## 1. Project bootstrap & shared scaffolding
- [ ] 1.1 Initialize Go module `controlai` (Go 1.22+); add `go.sum`; commit `.gitignore`, `LICENSE`, `Makefile`, `README.md` skeleton.
- [ ] 1.2 Wire base packages: `internal/config`, `internal/audit`, `internal/version`, `internal/log` (slog, JSON, level from config).
- [ ] 1.3 Add `cmd/controlai/main.go` with cobra root command + global flags (`--config`, `--data-dir`, `--socket`, `--token`).
- [ ] 1.4 Add `Makefile` targets: `build`, `test`, `lint` (`golangci-lint`), `integration` (build tag), `vet`, `tidy`.
- [ ] 1.5 Add CI placeholder (`.github/workflows/ci.yml`) running `make vet test lint`.

## 2. SQLite registry & migrations (capability: tenant-management)
- [ ] 2.1 Define schema in `migrations/sqlite/0001_init.sql`: `tenants`, `sites`, `desired_state`, `actual_state_cache`, `audit_events`, `settings`, `auth_tokens`.
- [ ] 2.2 Implement `internal/store/sqlite` with `database/sql` + `modernc.org/sqlite` driver; embed migrations; auto-migrate at daemon start.
- [ ] 2.3 Implement CRUD on `tenants` and `sites` with slug + prefix validation (`tnt_`, `ste_`).
- [ ] 2.4 Add unit tests covering CRUD, slug rules, foreign-key cascades, soft-delete on `--purge=false`.

## 3. YAML config types + schema versioning (capability: tenant-management, compose-rendering)
- [ ] 3.1 Define `internal/config/tenant.go` (`Tenant{SchemaVersion, ID, Name, Domain, Retention, Resources}`) and `internal/config/site.go` (`Site{SchemaVersion, ID, TenantID, Broker, Ingest, Throughput, PayloadCodec, AuthMode}`).
- [ ] 3.2 Implement `Validate()` for both types covering enum values, ID format, broker × throughput compatibility, mode constraints, schema-version range.
- [ ] 3.3 Implement `internal/migrate/yaml/` with `Migrator` interface + version registry; `MigrateFile()` rewrites a YAML file with backup.
- [ ] 3.4 Add `controlai migrate [--dry-run]` CLI command that walks `/var/lib/controlai/tenants/**` and applies migrations.
- [ ] 3.5 Unit tests for validation, migration, round-trip YAML.

## 4. Template-driven renderer (capability: compose-rendering, broker-modules, timescale-provisioning)
- [ ] 4.1 Implement `internal/render` with a `Renderer` that walks `templates/` (embedded via `go:embed`) and produces files under a project's directory.
- [ ] 4.2 Author `templates/shared/docker-compose.yml.tmpl` (Traefik service) and `templates/shared/traefik/{static.yml.tmpl,dynamic/}`.
- [ ] 4.3 Author `templates/tenant/tsdb/docker-compose.yml.tmpl` and `templates/tenant/tsdb/init.sql.tmpl` parameterized by retention, chunk_time_interval, RAM tier, compression.
- [ ] 4.4 Author `templates/site/mosquitto/{docker-compose.yml.tmpl,mosquitto.conf.tmpl,acl.tmpl}`.
- [ ] 4.5 Author `templates/site/emqx/{docker-compose.yml.tmpl,emqx.conf.tmpl,acl.conf.tmpl,api_keys.bootstrap.tmpl}`.
- [ ] 4.6 Author `templates/site/ingest/docker-compose.yml.tmpl` referencing the published `controlai-ingest:<version>` image.
- [ ] 4.7 Author `templates/shared/traefik/dynamic/site-mqtt.yml.tmpl` and `site-http.yml.tmpl` (TCP router + HTTPS router per site).
- [ ] 4.8 Implement deterministic content hashing per rendered file; cache in `actual_state_cache`.
- [ ] 4.9 Snapshot/golden-file tests for every template across all valid configuration permutations.
- [ ] 4.10 Validation that broker × throughput tier combinations not in the matrix produce a clear error from the renderer (e.g. mosquitto + mid + 2 replicas → reject).

## 5. PKI module (capability: pki-management)
- [ ] 5.1 Implement `internal/pki/ca.go`: RSA-2048 self-signed CA, AES-256-GCM wrap with master key from `CONTROLAI_CA_KEY_ENCRYPTION_KEY`.
- [ ] 5.2 Implement `internal/pki/leaf.go`: leaf cert issuance (ClientAuth EKU), CN slug.
- [ ] 5.3 Implement `internal/pki/server.go`: ServerAuth+ClientAuth EKU, SANs from configured host list.
- [ ] 5.4 Implement `internal/pki/store.go`: persist CA + cert metadata in SQLite (`cas`, `certs` tables); private keys stored only in the encrypted CA row; leaf private keys are returned once and not persisted.
- [ ] 5.5 Implement `EnsureTrustFiles()` triggered renewal (missing / CA-mismatch / ≤30 d expiry / SAN-drift).
- [ ] 5.6 CLI: `controlai pki ca create`, `controlai pki cert issue --site <id> --gateway <name>`, `controlai pki cert revoke <fingerprint>`.
- [ ] 5.7 Unit tests covering rotation logic, AES-GCM round-trip, revocation propagation.

## 6. Ingest service binary (capability: ingest-service)
- [ ] 6.1 Scaffold `services/ingest/main.go` with config struct loaded from env + mounted `site.yaml`.
- [ ] 6.2 Implement MQTT subscribe via `eclipse/paho.golang/autopaho` with mTLS (CA + client cert/key from mounted paths).
- [ ] 6.3 Implement payload-codec dispatcher (`cbor` / `json` / `raw_passthrough`); CBOR via `fxamacker/cbor`.
- [ ] 6.4 Implement batched writer: ring buffer + flush by size or by timer; multi-row INSERT into `telemetry` via `jackc/pgx/v5`; configurable batch size + flush interval.
- [ ] 6.5 Implement bi-mode downlink path: HTTP listener on a known internal port; daemon forwards `POST /v1/tenants/<t>/sites/<s>/publish` to it; ingest publishes back to MQTT on the configured downlink topic.
- [ ] 6.6 Implement MQTT5 shared subscription opt-in for EMQX (`$share/<site>/<filter>`); auto-disabled for mosquitto.
- [ ] 6.7 Implement graceful shutdown: SIGTERM → stop MQTT → drain buffer → close pgx pool, 10 s budget.
- [ ] 6.8 Dockerfile for `controlai-ingest` image (multi-stage, distroless base).
- [ ] 6.9 Unit tests for codec, batcher, downlink HTTP-to-MQTT path; integration test under `// +build integration` against a real mosquitto + Postgres in compose.

## 7. Runner & reconciler (capability: reconciler)
- [ ] 7.1 Implement `internal/runner/compose.go`: shell out to `docker compose -p <project> -f <file>` with `up`, `down`, `restart`, `ps`. Capture stdout/stderr separately; parse `ps --format json` as NDJSON.
- [ ] 7.2 Implement `internal/runner/docker.go`: wrap Docker engine SDK (`github.com/docker/docker/client`) for `ContainerList` filtered by `com.docker.compose.project`, `ContainerInspect` for `Health.Status`.
- [ ] 7.3 Implement per-project `sync.Mutex` map + global `semaphore.Weighted(15)`; all runner calls go through these.
- [ ] 7.4 Implement `internal/recon/reconciler.go`: 30 s tick loop, computes desired vs actual, drives runner, exponential backoff on failure (30 s → 1 m → 5 m → 30 m), emits audit events.
- [ ] 7.5 Implement `controlai apply <selector>` blocking command that writes desired-state row and polls convergence with timeout.
- [ ] 7.6 Unit tests for backoff state machine; integration tests for full provision → apply → modify → restart cycle.

## 8. Edge routing (capability: edge-routing)
- [ ] 8.1 Implement `controlai shared init` command: render shared compose + Traefik static config, start container, verify :443 + :8883 reachable.
- [ ] 8.2 Implement dynamic-config writer with atomic-rename pattern (write to `*.tmp` + `os.Rename`); ensure permissions match Traefik's expected user.
- [ ] 8.3 Wire reconciler to maintain `shared/traefik/dynamic/` files in lockstep with `sites` table.
- [ ] 8.4 Add ACME flag (`shared.traefik.acme = true|false`) — when true, render `certResolvers` in Traefik static config.
- [ ] 8.5 Integration test: bring up shared + 2 sites on `*.controlai.local` via `/etc/hosts`, assert MQTT SNI routing to the correct upstream.

## 9. Daemon API (capability: daemon-api)
- [ ] 9.1 Implement `internal/daemon/server.go` with chi router; mount handlers under `/v1/`.
- [ ] 9.2 Implement unix-socket listener at `/var/run/controlai.sock`, mode 0660; CLI talks to it by default.
- [ ] 9.3 Implement optional TCP+TLS listener; cert paths and listen-addr in daemon config.
- [ ] 9.4 Implement bearer-token middleware: tokens stored in `auth_tokens` table; `controlai token create|list|revoke` CLI.
- [ ] 9.5 Implement REST handlers:
  - `GET    /v1/health`
  - `GET    /v1/capacity`
  - `POST   /v1/tenants` / `GET /v1/tenants` / `GET /v1/tenants/{id}` / `PATCH /v1/tenants/{id}` / `DELETE /v1/tenants/{id}?purge=true|false`
  - `POST   /v1/tenants/{tid}/sites` / `GET .../sites` / `GET .../sites/{sid}` / `PATCH .../sites/{sid}` / `DELETE .../sites/{sid}?purge=...`
  - `POST   /v1/tenants/{tid}/sites/{sid}/publish` (bi-mode downlink)
  - `GET    /v1/tenants/{tid}/sites/{sid}/logs?service=<name>&tail=N`
  - `POST   /v1/apply/{selector}`
- [ ] 9.6 Wire all CLI subcommands to call the REST API (CLI never touches SQLite / docker directly).
- [ ] 9.7 OpenAPI 3.1 spec checked into `docs/api/openapi.yaml`; generated handler stubs verified to compile.
- [ ] 9.8 Unit tests per handler + integration test that runs the daemon and exercises every endpoint end-to-end.

## 10. Capacity guard (capability: capacity-guard)
- [ ] 10.1 Author `internal/capacity/profile.go` with the static RSS profile table from design D10.
- [ ] 10.2 Implement `Predict(plan)` that totals existing tenants/sites + the requested delta.
- [ ] 10.3 Wire into admission path: every `POST /v1/tenants` and `POST /v1/sites` calls `Predict` and rejects with HTTP 409 if over 85 % of `usable`.
- [ ] 10.4 Add `controlai capacity [--plan tenant=...,site=...]` CLI for what-if queries.
- [ ] 10.5 Add `scripts/measure-rss.sh` that boots one of each profile combination and emits an updated table; CI test asserts checked-in table matches ±15 %.

## 11. Backup (capability: tenant-management)
- [ ] 11.1 Implement `internal/backup/pgdump.go`: per-tenant `pg_dump` invoked via `docker compose exec` against the tenant TSDB container.
- [ ] 11.2 Implement daily systemd timer template (rendered into `/etc/systemd/system/controlai-backup-<tenant>.{service,timer}`) when tenant is created.
- [ ] 11.3 Output compressed dumps to `/var/backups/controlai/<tenant>/<YYYYMMDD>.sql.gz`; keep last 7 by default.
- [ ] 11.4 CLI: `controlai backup run <tenant>` and `controlai backup ls <tenant>`.

## 12. Systemd integration + production hardening
- [ ] 12.1 Author `deploy/systemd/controlai.service` (Type=notify, sd_notify integration, restart=on-failure).
- [ ] 12.2 Implement `controlai install` command that writes the unit file, creates the `controlai` system user, sets file perms on `/var/lib/controlai`.
- [ ] 12.3 Author `deploy/install.sh` for one-shot operator install.
- [ ] 12.4 README runbook: install, first tenant, first site, retention change, broker swap, capacity check, uninstall.

## 13. Verification
- [ ] 13.1 End-to-end integration test that:
  - boots shared infra,
  - creates 2 tenants (one mosquitto/low/uni, one EMQX/mid/bi),
  - publishes test telemetry through both,
  - verifies rows in respective TSDB containers,
  - verifies bi-mode downlink reaches the gateway,
  - swaps tenant-A broker from mosquitto to EMQX and reasserts data flow,
  - changes tenant-B retention from 7d to 1d and verifies policy applied.
- [ ] 13.2 Run `openspec validate add-controlai-core --strict` and resolve any issues.
- [ ] 13.3 Verify capacity guard refuses a third tenant on a synthetic 4 GiB host.
- [ ] 13.4 Verify reconciler converges after manual `docker compose down` of a site (auto-restart within 30 s).
- [ ] 13.5 Manual cleanup: `controlai tenant rm --purge` removes containers + volumes + backups; daemon-side data is gone.
