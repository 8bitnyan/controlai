## 1. Project bootstrap & shared scaffolding
- [x] 1.1 Initialize Go module `controlai` (Go 1.22+); add `go.sum`; commit `.gitignore`, `LICENSE`, `Makefile`, `README.md` skeleton.
- [x] 1.2 Wire base packages: `internal/config`, `internal/audit`, `internal/version`, `internal/log` (slog, JSON, level from config).
- [x] 1.3 Add `cmd/controlai/main.go` with cobra root command + global flags (`--config`, `--data-dir`, `--socket`, `--token`).
- [x] 1.4 Add `Makefile` targets: `build`, `test`, `lint` (`golangci-lint`), `integration` (build tag), `vet`, `tidy`.
- [x] 1.5 Add CI placeholder (`.github/workflows/ci.yml`) running `make vet test lint`.

## 2. SQLite registry & migrations (capability: tenant-management)
- [x] 2.1 Define schema in `migrations/sqlite/0001_init.sql`: `tenants`, `sites`, `desired_state`, `actual_state_cache`, `audit_events`, `settings`, `auth_tokens`, `cas`, `certs`, `emqx_api_keys`, `tsdb_credentials`.
- [x] 2.2 Implement `internal/store/sqlite` with `database/sql` + `modernc.org/sqlite` driver; embed migrations; auto-migrate at daemon start.
- [x] 2.3 Implement CRUD on `tenants` and `sites` with slug + prefix validation (`tnt_`, `ste_`).
- [x] 2.4 Add unit tests covering CRUD, slug rules, foreign-key cascades, soft-delete on `--purge=false`, CA/cert round-trip, token create/revoke.

## 3. YAML config types + schema versioning (capability: tenant-management, compose-rendering)
- [x] 3.1 Define `internal/config/tenant.go` and `internal/config/site.go` with all required fields.
- [x] 3.2 Implement `Validate()` for both types covering enum values, ID format, broker × throughput compatibility, high-tier rejection, schema-version range.
- [x] 3.3 Implement `internal/migrate/yaml/` with `Migrator` interface + version registry; `MigrateFile()` rewrites a YAML file with backup.
- [x] 3.4 Add `controlai migrate [--dry-run]` CLI command that walks `/var/lib/controlai/tenants/**` and applies migrations.
- [x] 3.5 Unit tests for validation, migration, round-trip YAML (config_test.go).

## 4. Template-driven renderer (capability: compose-rendering, broker-modules, timescale-provisioning)
- [x] 4.1 Implement `internal/render` with a `Renderer` that walks `templates/` (embedded via `go:embed`) and produces files under a project's directory. Empty templates are skipped.
- [x] 4.2 Author `templates/shared/docker-compose.yml.tmpl` (Traefik service), `templates/shared/traefik/static.yml.tmpl`, and `templates/shared/traefik/dynamic/`.
- [x] 4.3 Author `templates/tenant/tsdb/docker-compose.yml.tmpl`, `init.sql.tmpl`, `postgresql.conf.tmpl` parameterized by retention, chunk_time_interval, compression.
- [x] 4.4 Author `templates/site/mosquitto/docker-compose.yml.tmpl` (merged broker+ingest), `deploy/mosquitto.conf.tmpl`, `deploy/acl.conf.tmpl`.
- [x] 4.5 Author `templates/site/emqx/docker-compose.yml.tmpl` (merged broker+ingest), `deploy/emqx.conf.tmpl`, `deploy/acl.conf.tmpl`, `deploy/api_keys.bootstrap.tmpl`.
- [x] 4.6 Ingest services merged into broker docker-compose templates (one compose file per site, no separate ingest compose).
- [x] 4.7 Author `templates/shared/traefik/dynamic/site-mqtt.yml.tmpl` (TCP SNI router per site); `RenderTraefikDynamicForSite` names files per-site to avoid conflicts.
- [x] 4.8 Implement deterministic SHA-256 content hashing per rendered file; `actual_state_cache` table stores hashes.
- [x] 4.9 Unit tests for render package: idempotency, single compose per site, deploy/ path placement, TSDB creds injection, per-site Traefik naming (render_test.go).
- [x] 4.10 Validation that broker × throughput combinations not in the matrix produce a clear error (mosquitto+mid → rejected at config.Validate() and handler level).

## 5. PKI module (capability: pki-management)
- [x] 5.1 Implement `internal/pki/ca.go`: RSA-2048 self-signed CA, AES-256-GCM wrap with master key from `CONTROLAI_CA_KEY_ENCRYPTION_KEY`; devMode fallback.
- [x] 5.2 Implement `internal/pki/leaf.go`: leaf cert issuance (ClientAuth EKU), CN slug, clock-skew tolerance (-5 min NotBefore).
- [x] 5.3 Implement `internal/pki/server.go`: ServerAuth+ClientAuth EKU, SANs from configured host list, 10y TTL.
- [x] 5.4 PKI persistence: `StoreCACert`, `GetCACert`, `StoreCert`, `GetCertByFingerprint`, `ListCertsBySite`, `RevokeCert`, `ListExpiringCerts` methods in `store/sqlite`.
- [x] 5.5 Implement `NeedsRotation()` in `internal/pki/ensure.go`: triggers for missing / CA-mismatch / ≤30 d expiry / SAN-drift.
- [x] 5.6 CLI: `controlai pki ca create`, `controlai pki cert issue`, `controlai pki cert revoke` (REST API backed).
- [x] 5.7 Unit tests: CA generate/decrypt, leaf issuance & verify, server cert & verify, NeedsRotation, AES-GCM round-trip (pki_test.go).

## 6. Ingest service binary (capability: ingest-service)
- [x] 6.1 Scaffold `services/ingest/main.go` with config struct loaded from env + mounted `site.yaml`.
- [x] 6.2 Implement MQTT subscribe via `eclipse/paho.golang/autopaho` with mTLS (CA + client cert/key from mounted paths).
- [x] 6.3 Implement payload-codec dispatcher (`cbor` / `json` / `raw_passthrough`); CBOR via `fxamacker/cbor`; decode failure is non-fatal.
- [x] 6.4 Implement batched writer: in-memory ring buffer + flush by size or by timer; multi-row INSERT into `telemetry` via `jackc/pgx/v5`; low=200/1000ms, mid=1000/500ms.
- [x] 6.5 Implement bi-mode downlink path: daemon publishes directly to MQTT via `mqttPublish()` helper with site ingestor cert + SNI routing through Traefik :8883.
- [x] 6.6 Implement MQTT5 shared subscription opt-in for EMQX (`$share/<site>/<filter>`); bare topic for mosquitto.
- [x] 6.7 Implement graceful shutdown: SIGTERM → disconnect MQTT → drain ring buffer → close pgx pool, 10 s budget.
- [ ] 6.8 Dockerfile for `controlai-ingest` image (multi-stage, distroless base).
- [ ] 6.9 Unit tests for codec, batcher, downlink MQTT path; integration test under `// +build integration`.

## 7. Runner & reconciler (capability: reconciler)
- [x] 7.1 Implement `internal/runner/compose.go`: shell out to `docker compose -p <project> -f <file>` with `up`, `down`, `restart`, `ps`; capture stdout/stderr; parse `ps --format json` as NDJSON.
- [x] 7.2 Implement `internal/runner/docker.go`: wrap Docker engine SDK for `ContainerList` filtered by `com.docker.compose.project`, `ContainerInspect` for `Health.Status`.
- [x] 7.3 Implement per-project `sync.Mutex` map + global `semaphore.Weighted(15)` in runner/compose.go; all compose calls go through these.
- [x] 7.4 Implement `internal/recon/reconciler.go`: 30 s tick loop, desired vs actual, drives runner, exponential backoff (30 s → 1 m → 5 m → 30 m), emits audit events.
- [x] 7.5 `controlai apply <selector>` CLI command writes desired-state row and triggers reconciler tick via REST API.
- [ ] 7.6 Unit tests for backoff state machine; integration tests for provision → apply → modify → restart cycle.

## 8. Edge routing (capability: edge-routing)
- [ ] 8.1 Implement `controlai shared init` command: render shared compose + Traefik static config, start container, verify :443 + :8883 reachable.
- [x] 8.2 Implement dynamic-config writer with atomic-rename pattern (`*.tmp` + `os.Rename`) in `render.WriteResults`; permissions set to 0640.
- [ ] 8.3 Wire reconciler to maintain `shared/traefik/dynamic/` files in lockstep with `sites` table.
- [x] 8.4 ACME flag: `shared.traefik.acme=true` renders `certResolvers` in Traefik static config template.
- [ ] 8.5 Integration test: bring up shared + 2 sites on `*.controlai.local`, assert MQTT SNI routing.

## 9. Daemon API (capability: daemon-api)
- [x] 9.1 Implement `internal/daemon/server.go` with chi router; mount handlers under `/v1/` with request timeout and recovery middleware.
- [x] 9.2 Implement unix-socket listener at `/var/run/controlai.sock`, mode 0660; CLI connects by default without token.
- [x] 9.3 Implement optional TCP+TLS listener; cert paths and listen-addr in daemon config; bearer-token required.
- [x] 9.4 Implement bearer-token middleware: SHA-256 hashed in `auth_tokens` table; `controlai token create|list|revoke` CLI.
- [x] 9.5 Implement all REST handlers:
  - `GET    /v1/health` — version, docker reachability, registry health, reconciler last tick
  - `GET    /v1/capacity` — projected RSS breakdown + admissible flag
  - `POST   /v1/tenants` — slug validation, admission check, create
  - `GET    /v1/tenants`, `GET /v1/tenants/{id}`, `DELETE /v1/tenants/{id}[?purge]`
  - `PATCH  /v1/tenants/{id}` — returns 501 (pending retention-change reconciler)
  - `POST   /v1/tenants/{tid}/sites` — capability matrix + admission check
  - `GET    /v1/tenants/{tid}/sites`, `GET .../sites/{sid}`, `DELETE .../sites/{sid}[?purge]`
  - `PATCH  /v1/tenants/{tid}/sites/{sid}` — returns 501 (pending)
  - `POST   /v1/tenants/{tid}/sites/{sid}/publish` — bi-mode downlink via direct MQTT publish
  - `GET    /v1/tenants/{tid}/sites/{sid}/logs` — shells to `docker compose logs --tail N [service]`
  - `POST   /v1/apply/{selector}` — write desired state, trigger reconciler tick
- [x] 9.6 All CLI subcommands talk exclusively to the daemon via REST API (`apiGet`, `apiPost`, `apiDelete`).
- [x] 9.7 OpenAPI 3.1 spec in `docs/api/openapi.yaml` covering all `/v1/` endpoints.
- [ ] 9.8 Unit tests per handler; integration test exercising every endpoint against a live daemon.

## 10. Capacity guard (capability: capacity-guard)
- [x] 10.1 Author `internal/capacity/profile.go` with static RSS profile table from design D10.
- [x] 10.2 Implement `Predict(plan, memKB)` that sums tenant TSDB + site RSS against 85 % of usable RAM.
- [x] 10.3 Wire into admission path: `POST /v1/tenants` and `POST /v1/tenants/{tid}/sites` call `admissionCheck`, reject HTTP 409 on overflow.
- [x] 10.4 `controlai capacity` CLI returns projected breakdown; `GET /v1/capacity` with what-if support.
- [ ] 10.5 Add `scripts/measure-rss.sh`; CI test asserts checked-in table matches measured baseline ±15 %.

## 11. Backup (capability: tenant-management)
- [ ] 11.1 Implement `internal/backup/pgdump.go`: per-tenant `pg_dump` via `docker compose exec`.
- [ ] 11.2 Implement daily systemd timer template rendered on tenant create.
- [ ] 11.3 Output compressed dumps to `/var/backups/controlai/<tenant>/<YYYYMMDD>.sql.gz`; keep last 7.
- [ ] 11.4 CLI: `controlai backup run <tenant>` and `controlai backup ls <tenant>`.

## 12. Systemd integration + production hardening
- [ ] 12.1 Author `deploy/systemd/controlai.service` (Type=notify, restart=on-failure).
- [ ] 12.2 Implement `controlai install` command: unit file, `controlai` system user, file perms.
- [ ] 12.3 Author `deploy/install.sh` for one-shot operator install.
- [ ] 12.4 README runbook: install, first tenant, first site, retention change, broker swap, capacity check, uninstall.

## 13. Verification
- [ ] 13.1 End-to-end integration test (mosquitto/low/uni + EMQX/mid/bi, telemetry rows, bi-mode downlink, broker swap, retention change).
- [ ] 13.2 Run `openspec validate add-controlai-core --strict` and resolve issues.
- [ ] 13.3 Verify capacity guard refuses a third tenant on a synthetic 4 GiB host.
- [ ] 13.4 Verify reconciler converges after manual `docker compose down` within 30 s.
- [ ] 13.5 Manual cleanup: `controlai tenant rm --purge` removes containers + volumes.
