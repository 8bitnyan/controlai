## ADDED Requirements

### Requirement: Single Go ingest binary configured per site via environment

controlai SHALL ship one ingest binary (`services/ingest`) built once
and run as a docker container in every site, configured exclusively by
environment variables and a mounted snapshot of the site's
`site.yaml`. Per-site differences SHALL never require rebuilding the
image.

#### Scenario: Same image runs two distinct sites
- **WHEN** controlai provisions two sites with different broker kinds, throughput tiers, and payload codecs
- **THEN** both site ingest containers SHALL run the same image tag, differing only in environment variables and mounts

### Requirement: Configurable payload codec

The ingest binary SHALL support three payload codec modes selected per
site by `site.yaml.payload_codec`: `cbor`, `json`, and `raw_passthrough`.
The codec SHALL decide what is written to the `payload jsonb` column
versus the `raw bytea` column.

#### Scenario: CBOR payload decoded to JSONB
- **WHEN** `payload_codec=cbor` and a gateway publishes a valid CBOR map
- **THEN** the ingest SHALL decode the CBOR into a `map[string]any`, write the result to `payload jsonb`, and also retain the original bytes in `raw bytea`

#### Scenario: Raw passthrough never decodes
- **WHEN** `payload_codec=raw_passthrough`
- **THEN** the ingest SHALL write the MQTT payload bytes verbatim to `raw bytea`, leave `payload` NULL, and SHALL NOT attempt any decoding

#### Scenario: Decode failure is non-fatal
- **WHEN** `payload_codec=cbor` or `json` and a message fails to decode
- **THEN** the ingest SHALL record `payload=NULL` with the raw bytes preserved in `raw`, emit a metric counter increment, and continue processing subsequent messages

### Requirement: Batched multi-row INSERT into TimescaleDB

The ingest SHALL accumulate messages in an in-memory ring buffer and
flush to TimescaleDB via `jackc/pgx/v5` multi-row INSERT (one statement
covering up to the configured batch size). Flush SHALL trigger on either
batch size reached or flush-interval elapsed, whichever comes first.

#### Scenario: Mid throughput tier batches 1000 with 500 ms flush
- **WHEN** a site is provisioned with `throughput=mid`
- **THEN** the ingest container's `INGEST_BATCH_SIZE` SHALL be 1000 and `INGEST_FLUSH_INTERVAL_MS` SHALL be 500

#### Scenario: Idle ingest still flushes the partial batch
- **WHEN** the ring buffer holds < batch-size messages and the flush interval elapses
- **THEN** the ingest SHALL execute a multi-row INSERT for whatever rows are present

### Requirement: Shared subscription on EMQX only

The ingest SHALL subscribe using `$share/<site_id>/<filter>` when
`broker.kind=emqx` and the rendered replica count is greater than one,
so that EMQX distributes messages across replicas. The ingest SHALL
subscribe to the bare filter when `broker.kind=mosquitto`, and the
renderer SHALL never produce a multi-replica plan for mosquitto.

#### Scenario: EMQX mid tier uses shared subscription
- **WHEN** a site is `broker.kind=emqx` with `throughput=mid` (2 replicas)
- **THEN** each ingest replica SHALL subscribe to `$share/ste_<id>/<tenant_id>/<site_id>/+/+` and EMQX SHALL deliver each message to exactly one replica

### Requirement: Bi-mode downlink path

When `site.yaml.ingest.direction=bi`, the ingest container SHALL expose
an internal HTTP endpoint reachable from the controlai daemon that
accepts `{topic, payload, qos}` and publishes back to the broker on the
configured downlink topic. When `direction=uni`, this endpoint SHALL be
absent.

#### Scenario: Daemon forwards a downlink command
- **WHEN** an authenticated REST client calls `POST /v1/tenants/tnt_acme-corp/sites/ste_seoul/publish {"topic":"tnt_acme-corp/ste_seoul/device-1/cmd","payload":"reboot","qos":1}` and the site is `direction=bi`
- **THEN** the daemon SHALL forward the request to the site's ingest, the ingest SHALL publish to the broker with QoS 1, and the REST call SHALL return HTTP 202 within 1 s under nominal conditions

#### Scenario: Downlink to uni-mode site rejected
- **WHEN** the same publish request targets a site with `direction=uni`
- **THEN** the daemon SHALL refuse with HTTP 409 stating the site is uni-direction

### Requirement: Graceful shutdown drains the buffer

On SIGTERM the ingest SHALL stop accepting new MQTT messages, flush the
current ring buffer to TimescaleDB, then close the pgx pool, all within
10 s.

#### Scenario: Clean shutdown loses no buffered rows
- **WHEN** the ingest is sent SIGTERM with N < batch-size rows buffered and a healthy TSDB connection
- **THEN** all N rows SHALL be persisted before the process exits, and the exit SHALL occur within 10 s
