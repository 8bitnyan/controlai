# Change: controlai-web — node editor, provisioning, monitoring, and dashboard

## Why

With the skeleton (`add-controlai-web-skeleton`) providing auth, domain model, and
instance registry, `controlai-web` needs its primary user-facing value: a drag-and-drop
node editor where operators design IoT pipeline topologies (Sensor → Gateway → Broker →
Ingest → TimescaleDB → Monitoring), apply them to a controlai daemon instance to
provision the actual Docker containers, and watch the live telemetry in a dashboard. This
is the feature the user explicitly asked for: "account and project and thus site based
sensor-gateway-broker-ingest-timescale configuration provisioning and monitoring using a
node editor." (Decision D2: full CRUD + Apply; D7: 6 custom node types; D8: live status
dots + msg/sec; D23: BFF-orchestrated serial apply.)

## What Changes

- **4 new capability specs** added to `openspec/changes/add-controlai-web-pipeline-ux/specs/`:
  - `controlai-web-node-editor` — @xyflow/react canvas, 6 node types, connection matrix, persistence, undo/redo
  - `controlai-web-provisioning` — dry-run preview → confirm → serial apply with reconciler polling
  - `controlai-web-monitoring` — mqtt-bridge (Hono + mqtt.js), SSE fanout, JWT auth, Redis Streams
  - `controlai-web-dashboard` — react-grid-layout, echarts widgets, telemetry binding
- No existing controlai or controlai-web specs are modified; all 4 are NET-NEW.
- **Depends on**: `add-controlai-web-skeleton` MUST be archived first (shell, auth, domain, instance-registry must exist).
- **Cross-repo dependency**: `add-daemon-https-via-traefik` must be applied to controlai before any daemon calls can succeed from the BFF.

## Impact

- Affected specs: none existing (all 4 are ADDED as new capabilities)
- Affected code: `apps/web` (canvas pages, dashboard pages), `apps/mqtt-bridge` (new Hono service), `packages/api` (new routers: nodeConfig, apply, stream, telemetry), `packages/db` (NodeConfig model + Dashboard model finalized)
- Breaking changes: **none** — builds on top of skeleton; no existing routes modified
- New infrastructure: Fly.io deployment for `apps/mqtt-bridge` (Phase 1); Upstash Redis for message buffering
