# Tasks: add-controlai-web-pipeline-ux

## 1. @xyflow/react integration

- [ ] 1.1 Install `@xyflow/react` v12 in `apps/web`: `pnpm add @xyflow/react --filter apps/web`.
- [ ] 1.2 Create `apps/web/components/canvas/` directory; add `canvas.tsx` as the primary canvas component with `'use client'` directive.
- [ ] 1.3 Wrap `canvas.tsx` with `<ReactFlowProvider>` in the parent server component `apps/web/app/(app)/orgs/[orgId]/projects/[projectId]/site-groups/[siteGroupId]/page.tsx`.
- [ ] 1.4 Import `@xyflow/react/dist/style.css` in `apps/web/app/layout.tsx` (required for @xyflow default styling).
- [ ] 1.5 Create `apps/web/components/canvas/canvas-theme.css` with CSS variables for node colors, edge stroke, and selection highlight matching the app's Tailwind theme.
- [ ] 1.6 Configure `nodeTypes` prop on `<ReactFlow>` to register all 6 custom node type components (created in section 2).
- [ ] 1.7 Configure `edgeTypes` prop with a custom `ControlEdge` that draws a thick animated line when the source node is live (status = HEALTHY).
- [ ] 1.8 Add minimap (`<MiniMap>`) and controls (`<Controls>`) to the canvas.

## 2. Six node type components

- [ ] 2.1 Create `apps/web/components/canvas/nodes/sensor-node.tsx` — displays device_id, topic_prefix, qos; one output handle `data`; status dot overlay; config form via shadcn Dialog.
- [ ] 2.2 Create `apps/web/components/canvas/nodes/gateway-node.tsx` — displays gateway_id, protocol; one input handle `data`, one output handle `mqtt`; status dot overlay; config form.
- [ ] 2.3 Create `apps/web/components/canvas/nodes/broker-node.tsx` — displays broker kind (mosquitto/EMQX), throughput tier; one input handle `mqtt`, one output handle `ingress`; config form fields: kind (select), throughput (select: low/mid).
- [ ] 2.4 Create `apps/web/components/canvas/nodes/ingest-node.tsx` — displays direction (uni/bi), batch_size; input `ingress`, output `tsdb`; config form.
- [ ] 2.5 Create `apps/web/components/canvas/nodes/timescaledb-node.tsx` — displays retention preset; input `tsdb`; config form with retention select (1m/1h/1d/7d/30d).
- [ ] 2.6 Create `apps/web/components/canvas/nodes/monitoring-node.tsx` — displays subscribed metrics; input `ingress`; no output; config form with metrics checkboxes.
- [ ] 2.7 Create `apps/web/components/canvas/nodes/node-config-dialog.tsx` — generic shadcn Dialog wrapper that renders the per-node config form; calls `nodeConfig.save` tRPC mutation on submit.
- [ ] 2.8 Create `apps/web/components/canvas/nodes/status-dot.tsx` — small colored circle (green=HEALTHY, yellow=DEGRADED, red=UNREACHABLE, grey=UNKNOWN) + `{msgPerSec} msg/s` label; positioned absolute top-right of each node card.

## 3. Connection rules

- [ ] 3.1 Create `apps/web/components/canvas/connection-rules.ts` defining `CONNECTION_MATRIX` as a `Record<NodeType, NodeType[]>` constant per the design.md connection matrix.
- [ ] 3.2 Implement `isValidConnection(connection: Connection, nodes: Node[]): boolean` that looks up source node type and checks if target node type is in `CONNECTION_MATRIX[sourceType]`.
- [ ] 3.3 Pass `isValidConnection` to the `<ReactFlow>` component's `isValidConnection` prop.
- [ ] 3.4 Add a toast notification (`sonner`) when an invalid connection is attempted: "Cannot connect {sourceType} → {targetType}".
- [ ] 3.5 Write Vitest unit tests for `connection-rules.ts`: all valid matrix connections return true; all invalid pairs return false.

## 4. NodeConfig persistence

- [ ] 4.1 Finalize `NodeConfig` Prisma model in `packages/db/prisma/schema.prisma` (id, siteGroupId, version, nodes Json, edges Json, isActive, appliedAt, appliedHash, createdAt, updatedAt).
- [ ] 4.2 Run `pnpm prisma migrate dev --name add_node_config` to create the migration.
- [ ] 4.3 Create `packages/api/src/routers/nodeConfig.ts` with procedures:
  - `nodeConfig.load({ siteGroupId })` — returns the active NodeConfig (isActive=true), or null.
  - `nodeConfig.save({ siteGroupId, nodes, edges })` — upsert draft (isActive=false, version incremented).
  - `nodeConfig.listVersions({ siteGroupId })` — list version metadata (no nodes/edges).
  - `nodeConfig.setActive({ nodeConfigId })` — mark one version as active (clears others).
- [ ] 4.4 Add `nodeConfig` router to `packages/api/src/root.ts` appRouter.
- [ ] 4.5 Define Zod schemas for `NodeData` (per node type) in `packages/shared-types/src/node-types.ts`; use discriminated union on `type` field.
- [ ] 4.6 Write Vitest tests for `nodeConfig` router: save increments version; setActive clears previous active; load returns null on empty.

## 5. Undo/redo and autosave

- [ ] 5.1 Install Zustand: `pnpm add zustand --filter apps/web`.
- [ ] 5.2 Create `apps/web/stores/canvas-store.ts` with Zustand store managing: `nodes`, `edges`, `history` (past/future stacks, max 50), `isDirty`, `lastSaved`.
- [ ] 5.3 Implement `undo()` and `redo()` actions in the store that push/pop from history stacks and update `nodes` + `edges`.
- [ ] 5.4 Wire keyboard shortcuts: `Cmd/Ctrl+Z` → undo, `Cmd/Ctrl+Shift+Z` → redo via `useHotkeys` or `useKeydown` effect in `canvas.tsx`.
- [ ] 5.5 Add undo/redo buttons to the canvas toolbar above `<ReactFlow>`.
- [ ] 5.6 Implement autosave: `useEffect` that debounces 30 s after `isDirty` becomes true, calls `nodeConfig.save` mutation, sets `isDirty = false` on success.
- [ ] 5.7 Display "Unsaved changes" / "Saved {time}" status indicator in the canvas toolbar.

## 6. Apply orchestration

- [ ] 6.1 Create `packages/api/src/routers/apply.ts` with procedures:
  - `apply.preview({ siteGroupId })` — loads active NodeConfig, fetches current daemon state, synthesizes ordered Op list per design.md algorithm, returns `Plan { planId, ops: Op[], planHash }`. Stores plan in-memory (or Redis with 10-min TTL).
  - `apply.commit({ siteGroupId, planId })` — validates planId not stale (re-computes hash), executes ops serially, polls `/v1/status` after each op with 30 s timeout, returns `Result { ops: OpResult[], success: boolean }`.
- [ ] 6.2 Define `Op` and `OpResult` types in `packages/shared-types/src/apply.ts`.
- [ ] 6.3 Implement `synthesizePlan(nodeConfig, daemonState)` pure function in `packages/api/src/lib/apply-planner.ts` per the design.md algorithm.
- [ ] 6.4 Implement `executeOp(op, instance)` in `packages/api/src/lib/apply-executor.ts` calling `daemon-client.ts`; treat daemon 409 on create as idempotent success.
- [ ] 6.5 After each successful `createSite` op, update `site.controlaiTenantId` and `site.controlaiSiteId` in Postgres.
- [ ] 6.6 Write `AuditLog` entry with `action = "apply.commit"`, `metadata = { planHash, opCount, success }` after each apply attempt.
- [ ] 6.7 Build Apply button in canvas toolbar: calls `apply.preview`, opens `ApplyModal`.
- [ ] 6.8 Build `apps/web/components/canvas/apply-modal.tsx` — shows Op list as a step-by-step checklist; "Confirm & Apply" button triggers `apply.commit`; each op shows pending/running/success/failed indicator; failure shows daemon error message.
- [ ] 6.9 Write Vitest tests for `synthesizePlan`: empty graph returns empty ops; Broker node with no daemon counterpart returns createTenant+createSite; updating retention returns updateTsdb.
- [ ] 6.10 Write Vitest tests for `apply.commit`: idempotent 409 on createTenant treated as success; timeout after 30 s returns failed op.

## 7. mqtt-bridge service scaffolding

- [ ] 7.1 Create `apps/mqtt-bridge/` directory with `package.json` (`name: @controlai-web/mqtt-bridge`), `tsconfig.json`, `src/index.ts`.
- [ ] 7.2 Install Hono, mqtt.js, ioredis: `pnpm add hono mqtt ioredis --filter @controlai-web/mqtt-bridge`.
- [ ] 7.3 Create `apps/mqtt-bridge/src/server.ts` — Hono app with `GET /health` (200 OK), `GET /sites/:siteId/stream` (SSE endpoint).
- [ ] 7.4 Create `apps/mqtt-bridge/src/jwt.ts` — `verifyJWT(token, secret): { siteId, userId }` using `jose` (HS256 verify); throw on invalid or expired.
- [ ] 7.5 Create `apps/mqtt-bridge/src/broker-registry.ts` — reads `ControlaiInstance` and `Site` from Postgres (Prisma, read replica URL from `DATABASE_URL_REPLICA` or same URL); provides `getBrokerConfig(siteId): BrokerConfig | null`.
- [ ] 7.6 Create `apps/mqtt-bridge/src/mqtt-manager.ts` — maintains a `Map<siteId, mqtt.MqttClient>`; on first SSE subscriber for a site, creates an mqtt.js client with mTLS (cert from Postgres `Site.mqttCert`); on last subscriber disconnect, closes client after 60 s idle timeout; implements exponential backoff 1s→30s on broker reconnect.
- [ ] 7.7 Create `apps/mqtt-bridge/src/sse-fanout.ts` — `EventEmitter`-based fanout: `subscribe(siteId, handler)` / `unsubscribe(siteId, handler)` / `emit(siteId, message)`.
- [ ] 7.8 Create `apps/mqtt-bridge/src/redis-writer.ts` — `writeMessage(siteId, topic, payload)` calls `XADD <siteId>:<topic> MAXLEN ~ 1000 * payload <json>` via ioredis.
- [ ] 7.9 Create `apps/mqtt-bridge/Dockerfile` (multi-stage: `node:20-alpine` builder → minimal runner; non-root user).
- [ ] 7.10 Create `apps/mqtt-bridge/fly.toml` for Fly.io deployment (`app = "controlai-mqtt-bridge"`, region `nrt`, 1 shared-cpu-1x 256MB machine, port 8080 HTTP).
- [ ] 7.11 Create `apps/mqtt-bridge/.env.example` documenting: `DATABASE_URL`, `STREAM_JWT_SECRET`, `UPSTASH_REDIS_URL`, `UPSTASH_REDIS_TOKEN`.

## 8. Stream JWT tRPC endpoint

- [ ] 8.1 Install `jose` in `packages/api`: `pnpm add jose --filter @controlai-web/api`.
- [ ] 8.2 Create `packages/api/src/routers/stream.ts` with procedure `stream.token({ siteId })`:
  - Verify caller has read access to `siteId` (orgProcedure chain)
  - Mint HS256 JWT `{ siteId, userId, iat, exp: now + 300 }` signed with `STREAM_JWT_SECRET`
  - Return `{ token, expiresAt, streamUrl: \`${STREAM_SERVICE_URL}/sites/${siteId}/stream\` }`
- [ ] 8.3 Add `stream` router to `appRouter` in `packages/api/src/root.ts`.
- [ ] 8.4 Create `apps/web/hooks/use-site-stream.ts` — React hook that: calls `stream.token` on mount, creates `EventSource(url + '?token=...')`, parses messages into `{ nodeId, status, msgPerSec, timestamp }`, refreshes token 30 s before expiry via `setTimeout`, handles `error` event with exponential reconnect.
- [ ] 8.5 Validate JWT secret is set at startup in `packages/api/src/routers/stream.ts`; throw startup error if missing.

## 9. Live telemetry overlays on nodes

- [ ] 9.1 Create `apps/web/hooks/use-node-telemetry.ts` — subscribes to the SiteGroup's single `EventSource` (via `use-site-stream.ts`), filters messages for a specific `nodeId`, returns `{ status, msgPerSec }`.
- [ ] 9.2 Wire `use-node-telemetry` into each of the 6 node components (section 2); pass `status` and `msgPerSec` to `StatusDot` component.
- [ ] 9.3 Throttle status updates in the Zustand canvas store to max 2 Hz to prevent canvas thrashing on high-throughput sites.
- [ ] 9.4 Add a `status` field to `NodeData` base type (`UNKNOWN | HEALTHY | DEGRADED | UNREACHABLE`); default `UNKNOWN` before first SSE message.
- [ ] 9.5 Display a "Live" badge in the canvas toolbar when the SSE connection is active; "Disconnected" with reconnect button when the EventSource is in error state.

## 10. echarts dashboard

- [ ] 10.1 Install dashboard dependencies: `pnpm add echarts echarts-for-react react-grid-layout --filter apps/web`.
- [ ] 10.2 Install `@types/react-grid-layout`: `pnpm add -D @types/react-grid-layout --filter apps/web`.
- [ ] 10.3 Create dashboard page at `apps/web/app/(app)/orgs/[orgId]/projects/[projectId]/site-groups/[siteGroupId]/dashboard/page.tsx`.
- [ ] 10.4 Create `apps/web/components/dashboard/dashboard-grid.tsx` — `react-grid-layout` Responsive grid with `cols={{ lg: 12, md: 6 }}`, `rowHeight = 60`, drag handle on widget header, layout persisted to `Dashboard` Prisma model.
- [ ] 10.5 Create widget components:
  - `apps/web/components/dashboard/widgets/msg-rate-chart.tsx` — echarts line chart of msg/sec over time, bound to SSE data.
  - `apps/web/components/dashboard/widgets/status-board.tsx` — echarts custom status grid showing all nodes in a SiteGroup.
  - `apps/web/components/dashboard/widgets/last-n-messages.tsx` — table of last N messages, reads from `telemetry.recent` tRPC.
  - `apps/web/components/dashboard/widgets/capacity-gauge.tsx` — echarts gauge of daemon instance capacity.
- [ ] 10.6 Create tRPC `telemetry.recent` procedure in `packages/api/src/routers/telemetry.ts`: reads `XRANGE <siteId>:<topic> - + COUNT <n>` from Upstash Redis; returns `{ messages: { timestamp, topic, payload }[] }`.
- [ ] 10.7 Create tRPC `telemetry.range` procedure for TimescaleDB backfill: calls `daemonClient` to `GET /v1/tenants/:id/tsdb/query` with `start`, `end`, `topic` params; returns raw rows. (Placeholder for future daemon tsdb query endpoint.)
- [ ] 10.8 Finalize `Dashboard` Prisma model (id, siteGroupId, layout Json, createdAt, updatedAt); run `pnpm prisma migrate dev --name add_dashboard`.
- [ ] 10.9 Create `packages/api/src/routers/dashboard.ts` with procedures `dashboard.load` and `dashboard.save` (upsert layout JSON for siteGroupId).
- [ ] 10.10 Add time-window selector component to dashboard page header (Last 1h / 6h / 24h / 7d); pass selected window to `telemetry.range`.

## 11. Node palette sidebar

- [ ] 11.1 Create `apps/web/components/canvas/node-palette.tsx` — fixed left sidebar listing the 6 node types as draggable cards (icon + label + brief description); uses `@xyflow/react` `useDnD` hook / `onDragStart` handler to set `transferData`.
- [ ] 11.2 Handle `onDrop` on the `<ReactFlow>` component in `canvas.tsx` to compute the drop position via `screenToFlowPosition` and insert a new node of the dragged type at that position.
- [ ] 11.3 Assign a unique `id` to each dropped node using `crypto.randomUUID()`.
- [ ] 11.4 Add a "Delete selected" toolbar button (trash icon) that calls `deleteElements({ nodes: selectedNodes })` from `useReactFlow()`; also wire the `Delete`/`Backspace` keyboard shortcut.
- [ ] 11.5 Add a "Fit view" toolbar button that calls `reactFlowInstance.fitView({ padding: 0.2 })`.

## 12. Apply modal — error recovery and re-run

- [ ] 12.1 In `apply-modal.tsx`, add a "Re-run failed ops" button that appears when `Result.success = false`; clicking it re-runs `apply.preview` and then opens a fresh confirmation prompt.
- [ ] 12.2 In `apply-modal.tsx`, display the daemon error body (JSON) in a `<pre>` block for each failed op so operators can diagnose without leaving the UI.
- [ ] 12.3 In `apply-executor.ts`, capture and surface the daemon's HTTP response body (up to 2 KB) in the `OpResult.errorDetail` field on failure.
- [ ] 12.4 Add a `apply.status({ siteGroupId })` tRPC procedure that returns the most recent apply result for a site group (stored in a lightweight `ApplyRun` Prisma model: `id, siteGroupId, planHash, success, opCount, failedAt, createdAt, resultJson`); shown in the canvas toolbar as "Last applied: {time}" or "Last apply failed".
- [ ] 12.5 Add `ApplyRun` Prisma model to `packages/db/prisma/schema.prisma`; run `pnpm prisma migrate dev --name add_apply_run`.

## 13. mqtt-bridge — Redis replay and Phase 2 EC2 sidecar

- [ ] 13.1 Implement `Last-Event-ID` replay in `apps/mqtt-bridge/src/server.ts`: parse `Last-Event-ID` header from SSE request; if present, call `XRANGE <siteId>:<topic> <lastEventId> + COUNT 100` for each subscribed topic and emit those messages before switching to live fan-out.
- [ ] 13.2 Set the SSE `id:` field on each event to the Redis Stream entry ID so that clients can provide an accurate `Last-Event-ID` on reconnect.
- [ ] 13.3 Create `apps/mqtt-bridge/src/health.ts` — `GET /health` returns `{ status: "ok", activeSites: N, totalSubscribers: M }` where N is the number of sites with active MQTT clients and M is the count of connected SSE clients.
- [ ] 13.4 Create `deploy/aws/docker-compose.mqtt-bridge.yml.tmpl` Go template for Phase 2 deployment of `mqtt-bridge` as a sidecar on the EC2 host: service `mqtt-bridge`, image `ghcr.io/8bitnyan/controlai-web/mqtt-bridge:latest`, ports `8080:8080`, env vars `DATABASE_URL`, `STREAM_JWT_SECRET`, `UPSTASH_REDIS_URL`, `UPSTASH_REDIS_TOKEN`.
- [ ] 13.5 Document Phase 2 deployment in `apps/mqtt-bridge/README.md`: build the Docker image, push to GHCR, SSH to EC2 host, `docker compose -f docker-compose.mqtt-bridge.yml up -d`; update Traefik dynamic config to route `stream.<deployment>.sslip.io → mqtt-bridge:8080`.

## 14. Dashboard — accessibility and widget management

- [ ] 14.1 Add keyboard navigation to `dashboard-grid.tsx`: Tab through widgets; Enter to focus the drag handle; Space/arrow keys to move the focused widget within the grid (react-grid-layout supports programmatic layout update).
- [ ] 14.2 Create `apps/web/components/dashboard/add-widget-dialog.tsx` — shadcn Dialog listing the 4 widget types with descriptions; selecting one appends a new widget entry to the dashboard layout and calls `dashboard.save`.
- [ ] 14.3 Create `apps/web/components/dashboard/widget-wrapper.tsx` — common wrapper around each widget that provides: drag handle (gripper icon), resize handle (corner icon), widget title, and a `⋮` menu with "Remove widget" option (calls `dashboard.save` with that widget removed from layout).
- [ ] 14.4 Add `aria-label` attributes and `role="region"` to each widget in `widget-wrapper.tsx` so screen readers can navigate the dashboard.
- [ ] 14.5 Add empty-state placeholder to `dashboard-grid.tsx`: when the `Dashboard` record has no widgets, render a centred "No widgets yet — click Add widget" card.

## 15. Acceptance

- [ ] 15.1 Run `openspec validate add-controlai-web-pipeline-ux --strict` and confirm exit 0.
- [ ] 15.2 Run `pnpm turbo run lint typecheck` — confirm zero errors.
- [ ] 15.3 Run `pnpm turbo run test` — confirm all Vitest tests pass (connection-rules, nodeConfig router, apply.commit, synthesizePlan).
- [ ] 15.4 Write Playwright E2E test `apps/web/e2e/pipeline-apply.spec.ts`:
  - Sign in → navigate to a SiteGroup canvas
  - Add Sensor → Gateway → Broker → Ingest → TimescaleDB nodes
  - Connect them per the connection matrix
  - Click Apply → see preview modal with Op list
  - Confirm Apply → assert success step-by-step
  - Navigate to dashboard → assert capacity gauge and status board render
- [ ] 15.5 Write Playwright E2E test `apps/web/e2e/canvas-undo-redo.spec.ts`:
  - Add a node → undo → assert node gone → redo → assert node present
- [ ] 15.6 Write Playwright E2E test `apps/web/e2e/invalid-connection.spec.ts`:
  - Attempt to connect TimescaleDB → Sensor → assert toast "Cannot connect TimescaleDB → Sensor"
- [ ] 15.7 Deploy `apps/mqtt-bridge` to Fly.io: `fly deploy --app controlai-mqtt-bridge` from `apps/mqtt-bridge/`.
- [ ] 15.8 Set `STREAM_SERVICE_URL` and `STREAM_JWT_SECRET` in Vercel env; confirm `stream.token` tRPC endpoint returns a valid JWT.
- [ ] 15.9 Connect a live controlai daemon site; assert SSE telemetry appears on canvas nodes as status dot + msg/sec.
- [ ] 15.10 Assert dashboard `last-n-messages` widget reads from Redis and displays ≥1 message row after SSE data flows.
- [ ] 15.11 Write Playwright E2E test `apps/web/e2e/node-palette-drag.spec.ts`: drag Broker node from palette → drop on canvas → assert Broker card rendered with config dialog openable.
- [ ] 15.12 Write Playwright E2E test `apps/web/e2e/apply-rerun.spec.ts`: simulate apply failure by mocking daemon 500 → assert "Re-run failed ops" button appears → click → confirm new plan modal opens.
