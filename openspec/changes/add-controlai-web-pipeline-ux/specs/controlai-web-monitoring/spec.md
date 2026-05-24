## ADDED Requirements

### Requirement: mqtt-bridge — standalone SSE telemetry service

`controlai-web` SHALL include a separate `apps/mqtt-bridge` service (Node.js + Hono +
mqtt.js v5) deployed on Fly.io (Phase 1) or an EC2 sidecar (Phase 2). The service SHALL
maintain one MQTT subscriber per site with mTLS using the site's client certificate
(stored in Postgres by the Apply provisioning step), fan out incoming MQTT messages to
all connected SSE clients for that site via an in-process `EventEmitter`, and write each
message to an Upstash Redis Stream (`XADD <siteId>:<topic> MAXLEN ~ 1000 * payload <json>`).

#### Scenario: SSE stream delivers live messages

- **WHEN** a browser connects to `GET /sites/:siteId/stream?token=<valid-jwt>` and the daemon's broker publishes a message on topic `sensors/device-1/data`
- **THEN** the SSE stream SHALL deliver an event with `data: { nodeId, siteId, topic, payload, timestamp }` within 500 ms of the broker publish
- **AND** the event SHALL be formatted as `data: <json>\n\n` per SSE protocol

#### Scenario: Multiple SSE clients for same site

- **WHEN** 5 browser tabs connect to the same `siteId` stream
- **THEN** the mqtt-bridge SHALL maintain ONE MQTT subscriber for that site
- **AND** each of the 5 SSE responses SHALL receive each message (fan out via EventEmitter)

#### Scenario: Last-Event-ID replay on reconnect

- **WHEN** a browser SSE connection drops and reconnects with `Last-Event-ID: <seq>`
- **THEN** the mqtt-bridge SHALL send all Redis Stream messages with ID > `Last-Event-ID` before resuming live fan out

### Requirement: JWT authentication for SSE connections

The mqtt-bridge SHALL authenticate every SSE connection via a short-lived HS256 JWT
passed as `?token=<jwt>` in the URL. The JWT SHALL be issued by the Vercel BFF tRPC
`stream.token` endpoint, signed with the `STREAM_JWT_SECRET` shared secret, and carry
claims `{ siteId, userId, exp }` with a 5-minute TTL. Connections without a valid,
non-expired JWT SHALL receive HTTP 401. The browser SHALL refresh the token 30 s before
expiry.

#### Scenario: Valid JWT accepted

- **WHEN** a browser connects with a valid, non-expired HS256 JWT containing the correct `siteId`
- **THEN** the mqtt-bridge SHALL upgrade to SSE and begin streaming

#### Scenario: Missing JWT rejected

- **WHEN** a request arrives at `/sites/:siteId/stream` without a `?token=` parameter
- **THEN** the mqtt-bridge SHALL return HTTP 401 and close the connection

#### Scenario: Expired JWT rejected

- **WHEN** a browser connects with a JWT whose `exp` has passed
- **THEN** the mqtt-bridge SHALL return HTTP 401
- **AND** the browser SHALL request a new token from `stream.token` tRPC and retry

#### Scenario: siteId mismatch in JWT rejected

- **WHEN** a browser presents a JWT with `siteId = "site-A"` but connects to `/sites/site-B/stream`
- **THEN** the mqtt-bridge SHALL return HTTP 403

### Requirement: MQTT subscriber lifecycle — per-site with backoff

The mqtt-bridge SHALL create a mqtt.js client for each unique `siteId` on first
subscriber connect. The client SHALL subscribe to `#` (all topics) for that site's
broker using mTLS (CA cert, client cert, and private key loaded from Postgres). On
broker disconnect, the client SHALL reconnect with exponential backoff starting at 1 s,
doubling per attempt up to 30 s. If no SSE clients are connected for a site for 60 s,
the MQTT client for that site SHALL be closed to conserve connections.

#### Scenario: MQTT client created on first SSE subscriber

- **WHEN** the first browser connects to a site's SSE stream
- **THEN** the mqtt-bridge SHALL create a mqtt.js client for that site's broker and subscribe to `#`

#### Scenario: MQTT client closed after idle timeout

- **WHEN** all SSE clients for a site disconnect and 60 s elapse
- **THEN** the mqtt-bridge SHALL close the mqtt.js client for that site

#### Scenario: Reconnect with backoff on broker disconnect

- **WHEN** the broker drops the MQTT connection
- **THEN** the mqtt-bridge SHALL attempt to reconnect after 1 s, then 2 s, 4 s, 8 s, ..., capping at 30 s
- **AND** SSE clients SHALL receive a `retry: <ms>` hint in the SSE stream to delay reconnect

### Requirement: Redis Stream message buffering

The mqtt-bridge SHALL write every received MQTT message to an Upstash Redis Stream keyed
by `<siteId>:<topic>` with `XADD MAXLEN ~ 1000`. This allows the dashboard to
bootstrap its "last N messages" widget from Redis without waiting for live MQTT data.

#### Scenario: Message written to Redis Stream

- **WHEN** the mqtt-bridge receives an MQTT message on topic `sensors/device-1/data` for `site-A`
- **THEN** the mqtt-bridge SHALL execute `XADD site-A:sensors/device-1/data MAXLEN ~ 1000 * payload <json> timestamp <iso>` on Upstash Redis

#### Scenario: Stream capped at 1000 messages

- **WHEN** a Redis Stream for `site-A:sensors/device-1/data` already contains 1000 entries and a new message arrives
- **THEN** Redis SHALL automatically evict the oldest entry (approximate trimming with `~` flag)
- **AND** the stream SHALL contain at most ~1000 entries after the write
