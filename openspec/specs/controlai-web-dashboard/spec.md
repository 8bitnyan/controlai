# controlai-web-dashboard Specification

## Purpose
TBD - created by archiving change add-controlai-web-pipeline-ux. Update Purpose after archive.
## Requirements
### Requirement: Per-SiteGroup dashboard with react-grid-layout

`controlai-web` SHALL render a per-SiteGroup dashboard page using `react-grid-layout`
(Responsive variant, 12 columns on large screens, 6 on medium) at the route
`/orgs/:orgId/projects/:projectId/site-groups/:siteGroupId/dashboard`. Widget layout
SHALL be persisted to the `Dashboard` Prisma model and auto-saved on grid layout change.
The grid SHALL provide 4 built-in widget types: `msg-rate-chart`, `status-board`,
`last-n-messages`, and `capacity-gauge`. Users SHALL be able to add, resize, move, and
remove widgets within the grid.

#### Scenario: Dashboard persists layout

- **WHEN** a user repositions a widget on the dashboard grid
- **THEN** the new layout SHALL be sent to `dashboard.save` tRPC mutation within 2 s
- **AND** on page reload, the dashboard SHALL render widgets in the saved positions

#### Scenario: Add a widget

- **WHEN** a user clicks "Add widget" and selects "Message Rate Chart"
- **THEN** a new `msg-rate-chart` widget SHALL appear on the grid at a default position
- **AND** the layout SHALL be saved automatically

#### Scenario: Dashboard accessible to MEMBER

- **WHEN** an org MEMBER navigates to a SiteGroup dashboard
- **THEN** the dashboard SHALL render with all widgets in read-only mode (no layout editing)
- **AND** live telemetry data SHALL still stream via SSE

### Requirement: Message rate chart — echarts line chart bound to SSE

The `msg-rate-chart` widget SHALL render an `echarts-for-react` line chart displaying
messages-per-second over time for the selected SiteGroup's nodes. Data SHALL be sourced
from the per-SiteGroup `EventSource` maintained by `use-site-stream.ts`. The chart SHALL
update in real time (max 2 Hz refresh) as SSE messages arrive. A time-window selector
(Last 1h / 6h / 24h / 7d) SHALL be available in the widget header; for windows > 1h,
data SHALL be fetched from `telemetry.range` tRPC (TimescaleDB backfill, if available).

#### Scenario: Real-time chart updates

- **WHEN** the SSE stream delivers a message with `msgPerSec: 42` for node `broker-1`
- **THEN** the message rate chart SHALL update its `broker-1` series with the new data point within 500 ms

#### Scenario: Time window switches to backfill

- **WHEN** a user selects the "Last 7d" time window
- **THEN** the widget SHALL call `telemetry.range({ siteId, start: now-7d, end: now })` and render the returned rows in echarts

### Requirement: Status board — echarts status grid

The `status-board` widget SHALL render a grid of status indicators for all nodes in the
SiteGroup's active NodeConfig. Each cell SHALL show the node type icon, node name, and a
colored status indicator (green=HEALTHY, yellow=DEGRADED, red=UNREACHABLE, grey=UNKNOWN).
Status SHALL update via the SiteGroup SSE stream.

#### Scenario: Status board reflects live status

- **WHEN** the SSE stream delivers `{ nodeId: "broker-1", status: "DEGRADED" }` for a SiteGroup
- **THEN** the `broker-1` cell in the status board SHALL show a yellow indicator within 500 ms

#### Scenario: Status board renders all NodeConfig nodes

- **WHEN** the active NodeConfig contains 5 nodes (Sensor, Gateway, Broker, Ingest, TimescaleDB)
- **THEN** the status board SHALL render 5 cells, one per node, in a grid layout

### Requirement: Last-N-messages table — Redis-backed telemetry

The `last-n-messages` widget SHALL display the last N (default: 50, max: 200) MQTT
messages received for the SiteGroup, sourced from `telemetry.recent` tRPC which reads
from Upstash Redis Streams. The table SHALL show columns: timestamp, topic, and a
truncated payload preview. The widget SHALL poll `telemetry.recent` every 10 s for fresh
data while the dashboard tab is visible.

#### Scenario: Widget shows last 50 messages on load

- **WHEN** a user opens the dashboard and the `last-n-messages` widget is present
- **THEN** the widget SHALL call `telemetry.recent({ siteId, n: 50 })` and display up to 50 rows

#### Scenario: Widget refreshes on poll interval

- **WHEN** 10 s elapse after the last `telemetry.recent` call
- **THEN** the widget SHALL re-fetch and update the table with any new messages
- **AND** the newest message SHALL appear at the top of the table

### Requirement: Capacity gauge — echarts gauge bound to instance health

The `capacity-gauge` widget SHALL render an echarts gauge showing the controlai daemon
instance's capacity utilization (`capacityUsedMB / capacityAllowedMB * 100`), read from
the `ControlaiInstance` Postgres row (updated by the health cron). The gauge SHALL use
color thresholds: green (0–60%), yellow (60–85%), red (85–100%). A link to the instance
detail page SHALL be available in the widget header.

#### Scenario: Capacity gauge reflects latest health poll

- **WHEN** the Vercel Cron instance health poll updates `capacityUsedMB = 2048`, `capacityAllowedMB = 3276`
- **THEN** the capacity gauge widget SHALL show approximately 62.5% utilization with a yellow color

#### Scenario: Unknown capacity when instance unreachable

- **WHEN** the `ControlaiInstance.status` is `UNREACHABLE`
- **THEN** the capacity gauge SHALL show a grey "Unreachable" overlay instead of a numeric value

