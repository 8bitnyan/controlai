## ADDED Requirements

### Requirement: Deterministic template-driven file rendering

controlai SHALL render all docker-compose files and supporting
configuration (broker configs, ACL files, init.sql, Traefik dynamic
routes) from Go `text/template` sources embedded in the controlai binary
via `go:embed`. The rendering process SHALL be a pure function of the
input config and SHALL produce byte-identical output across runs when the
input is unchanged.

#### Scenario: Idempotent rendering
- **WHEN** controlai renders the same `tenant.yaml` and `site.yaml` twice in succession
- **THEN** every output file SHALL be byte-identical between the two runs

#### Scenario: Embedded templates need no filesystem
- **WHEN** controlai is run on a host without the source repository present
- **THEN** rendering SHALL succeed using only the binary's embedded templates

### Requirement: Content-addressed change detection

controlai SHALL compute a SHA-256 hash per rendered file and cache hashes
in an `actual_state_cache` table. The reconciler SHALL restart only the
services whose rendered config hash differs from the cached hash since
the last successful apply.

#### Scenario: Unrelated edit triggers no restart
- **WHEN** an operator changes a tenant's `retention` field but leaves the broker section unchanged
- **THEN** controlai SHALL re-render the TSDB init.sql, restart only the TSDB container, and leave all site broker and ingest containers running untouched

#### Scenario: Broker swap restarts only that site
- **WHEN** an operator changes one site's `broker.kind` from `mosquitto` to `emqx`
- **THEN** controlai SHALL restart only that site's broker container plus its Traefik dynamic route, leaving every other site and the TSDB untouched

### Requirement: Render produces docker-compose v3.9 compliant output

Every rendered `docker-compose.yml` SHALL conform to the docker-compose
spec version 3.9 and SHALL pass `docker compose config` without error.

#### Scenario: Lint passes on rendered output
- **WHEN** controlai renders a tenant or site project
- **THEN** running `docker compose -f <rendered> config` SHALL exit zero

### Requirement: Rejection of unsupported configuration permutations

The renderer SHALL refuse to produce output for configurations the
broker-modules capability matrix does not support, and SHALL surface a
clear validation error before any file is written to disk.

#### Scenario: Mosquitto with mid-tier multi-replica ingest rejected
- **WHEN** a site declares `broker.kind=mosquitto` with `throughput=mid` and the resulting plan would require 2 ingest replicas (mosquitto lacks shared subscriptions)
- **THEN** controlai SHALL reject the configuration at validation time with a message identifying the offending field and the supported alternative (`broker.kind=emqx` or `throughput=low`)
