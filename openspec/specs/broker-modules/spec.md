# broker-modules Specification

## Purpose
TBD - created by archiving change add-controlai-core. Update Purpose after archive.
## Requirements
### Requirement: Pluggable broker implementations behind a uniform site contract

controlai SHALL support `mosquitto` (2.x) and `emqx` (5.x) as broker
implementations selectable per site via `site.yaml.broker.kind`. Each
implementation SHALL deliver the same observable contract to gateways and
the ingest service: mTLS on the routed SNI hostname over Traefik's :8883
entrypoint, topic ACL restricting traffic to
`<tenant_id>/<site_id>/+/+`, and a stable internal DNS name reachable by
the site's ingest container.

#### Scenario: Mosquitto and EMQX accept the same gateway publish
- **WHEN** the same gateway connects with the same client certificate and publishes the same MQTT message on `tnt_acme-corp/ste_seoul/device-1/temperature` to either a mosquitto-backed or EMQX-backed site
- **THEN** the ingest container SHALL receive the publish and persist exactly one telemetry row in either case

### Requirement: Broker capability matrix enforced at validation

controlai SHALL maintain a capability matrix that declares which
`(broker.kind, throughput.tier, ingest.direction)` combinations are
valid. Combinations outside the matrix SHALL be rejected with HTTP 400
before any artifact is written.

#### Scenario: Mosquitto with mid + 2 replicas rejected
- **WHEN** a site requests `broker.kind=mosquitto` and `throughput=mid` (which the mid profile maps to 2 ingest replicas)
- **THEN** controlai SHALL reject the request because mosquitto 2.x does not implement MQTT5 shared subscriptions and 2 ingest replicas would double-receive every message

### Requirement: EMQX REST API key managed by controlai

When `broker.kind=emqx`, controlai SHALL generate a per-site EMQX REST
API key, render it into the site's `api_keys.bootstrap` mount, store the
key in SQLite, and use it for `POST /api/v5/banned` and
`POST /api/v5/listeners/ssl:default/restart` operations.

#### Scenario: EMQX listener restart after cert rotation
- **WHEN** the PKI module rotates a site's server certificate
- **THEN** controlai SHALL call `POST /api/v5/listeners/ssl:default/restart` on that site's EMQX REST API and an `audit_event(kind=broker.listener_restart)` SHALL be recorded with success or failure

### Requirement: Mosquitto file ACL generated from site config

When `broker.kind=mosquitto`, controlai SHALL render a file ACL that
allows the configured ingestor identity to subscribe to
`<tenant_id>/<site_id>/+/+` and denies all other access, and SHALL
configure mosquitto to require `verify_peer` with the site's CA as the
trust anchor.

#### Scenario: Cross-site publish rejected
- **WHEN** a gateway authenticated for `tnt_acme-corp/ste_seoul` publishes on `tnt_acme-corp/ste_busan/device-1/temperature`
- **THEN** the mosquitto broker SHALL refuse the publish based on the rendered ACL

