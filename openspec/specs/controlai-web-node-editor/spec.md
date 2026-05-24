# controlai-web-node-editor Specification

## Purpose
TBD - created by archiving change add-controlai-web-pipeline-ux. Update Purpose after archive.
## Requirements
### Requirement: @xyflow/react canvas with 'use client' boundary

The node editor SHALL be implemented as a client-side React component using
`@xyflow/react` v12 behind a `'use client'` directive, wrapped by a
`<ReactFlowProvider>` in the parent server component. The canvas SHALL support
drag-and-drop node placement, edge drawing, minimap, zoom/pan controls, and keyboard
navigation. The canvas SHALL register all 6 domain node types via the `nodeTypes` prop.

#### Scenario: Canvas renders existing NodeConfig

- **WHEN** a user navigates to a SiteGroup canvas page that has an active NodeConfig
- **THEN** the canvas SHALL render all nodes and edges from the stored JSONB with correct positions
- **AND** the canvas SHALL be interactive (drag nodes, draw edges, zoom) within 2 s of page load

#### Scenario: Empty canvas for new SiteGroup

- **WHEN** a user navigates to a SiteGroup canvas page with no active NodeConfig
- **THEN** the canvas SHALL render empty with a "Drag node types from the panel to start" hint
- **AND** a node palette panel SHALL be visible on the left sidebar

### Requirement: Six domain-specific node types

The canvas SHALL provide 6 custom node types representing the controlai infrastructure
domain: `Sensor`, `Gateway`, `Broker`, `Ingest`, `TimescaleDB`, and `Monitoring`.
Each node type SHALL have a distinct visual identity (icon + color), typed port handles
with semantically named IDs, a configuration form (opened via double-click or a settings
icon), and a live status overlay (status dot + msg/sec counter). Node configuration
SHALL be persisted immediately to the draft NodeConfig on form submit.

#### Scenario: Add a Broker node

- **WHEN** a user drags a Broker node from the palette to the canvas
- **THEN** the canvas SHALL render a Broker card with one input handle (`mqtt`) and one output handle (`ingress`), a broker-kind badge (mosquitto/EMQX), a throughput badge, and a grey status dot
- **AND** double-clicking the node SHALL open a config dialog with `kind` select and `throughput` select

#### Scenario: Configure a TimescaleDB node

- **WHEN** a user opens the TimescaleDB config dialog and selects retention `7d`, then clicks Save
- **THEN** the node label SHALL update to show `Retention: 7d`
- **AND** the draft NodeConfig SHALL be marked dirty (autosave will persist within 30 s)

#### Scenario: Status dot reflects live telemetry

- **WHEN** the SSE stream delivers a message `{ nodeId: "broker-1", status: "HEALTHY", msgPerSec: 42 }`
- **THEN** the Broker node's status dot SHALL turn green and display `42 msg/s`

### Requirement: Connection validation via CONNECTION_MATRIX

The canvas SHALL enforce a typed connection matrix. `isValidConnection` SHALL prevent
any edge that is not in the allowed matrix. Invalid connection attempts SHALL be
rejected visually (edge preview drops) and SHALL display a toast notification. The
`CONNECTION_MATRIX` SHALL be a static constant importable from `connection-rules.ts`
for use in tests and the apply planner.

#### Scenario: Valid connection accepted

- **WHEN** a user draws an edge from a Broker output handle to an Ingest input handle
- **THEN** the edge SHALL be accepted and rendered on the canvas

#### Scenario: Invalid connection rejected

- **WHEN** a user attempts to draw an edge from a TimescaleDB output to a Sensor input
- **THEN** the edge SHALL be rejected (not added to the canvas)
- **AND** a toast SHALL display "Cannot connect TimescaleDB → Sensor"

### Requirement: NodeConfig persistence — JSONB save/load/version

The tRPC `nodeConfig` router SHALL persist the canvas graph as JSONB in Postgres. Each
save SHALL create a new version (incrementing integer) and mark it as inactive draft.
The active version is the one that was last successfully applied. A version list SHALL
be accessible via `nodeConfig.listVersions`. Autosave SHALL run 30 s after the last
change, saving a draft. Explicit "Activate" is only possible after a successful Apply.

#### Scenario: Autosave creates a draft version

- **WHEN** a user makes a change to the canvas and 30 s elapse without further changes
- **THEN** `nodeConfig.save` SHALL be called, inserting a new `NodeConfig` row with `isActive = false`
- **AND** the canvas toolbar SHALL display "Saved {time}"

#### Scenario: setActive only after Apply

- **WHEN** `apply.commit` completes successfully for a NodeConfig version
- **THEN** that version's `isActive` SHALL be set to `true` and all other versions' `isActive` set to `false`
- **AND** `appliedAt` and `appliedHash` SHALL be set on that version

### Requirement: Node palette sidebar — drag-and-drop node creation

The canvas SHALL provide a fixed left sidebar node palette listing all 6 domain node
types as draggable items. Each palette item SHALL display the node type icon, label,
and a one-line description. Dragging a palette item onto the canvas SHALL create a new
node of that type at the drop position using `@xyflow/react`'s `screenToFlowPosition`
helper. A "Delete selected" toolbar button and the `Delete`/`Backspace` keyboard shortcut
SHALL remove selected nodes and edges from the canvas.

#### Scenario: Drag a node type from palette to canvas

- **WHEN** a user drags the "Broker" card from the node palette and drops it on the canvas
- **THEN** a new Broker node SHALL appear at the drop position with a unique `id` (UUID) and default configuration
- **AND** the draft NodeConfig SHALL be marked dirty (autosave within 30 s)

#### Scenario: Delete selected node via keyboard

- **WHEN** a user selects a node on the canvas and presses `Delete` or `Backspace`
- **THEN** the node and all its connected edges SHALL be removed from the canvas

#### Scenario: Fit view button

- **WHEN** a user clicks the "Fit view" toolbar button
- **THEN** the canvas SHALL animate to show all nodes within the viewport with 20% padding

### Requirement: Undo/redo — 50-step history buffer

The canvas SHALL maintain a 50-step undo/redo history in a Zustand store. Undo/redo
SHALL be accessible via `Cmd/Ctrl+Z` and `Cmd/Ctrl+Shift+Z` keyboard shortcuts and via
toolbar buttons. History SHALL reset on NodeConfig load.

#### Scenario: Undo removes last node addition

- **WHEN** a user adds a node and then presses `Cmd+Z`
- **THEN** the node SHALL be removed from the canvas

#### Scenario: History limit

- **WHEN** a user makes 51 sequential changes
- **THEN** the oldest history entry SHALL be discarded
- **AND** undo SHALL work for the 50 most recent changes

