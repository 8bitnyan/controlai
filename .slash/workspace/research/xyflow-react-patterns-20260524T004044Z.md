# Research: @xyflow/react v12 Patterns for controlai-web
Date: 2026-05-24T00:40:44Z

## Summary

`@xyflow/react` v12 (latest: **12.10.2**, released 2026-03-27) is a stable, MIT-licensed library with full SSR/Next.js App Router support. It is the correct choice for `controlai-web`. The core API is stable since v12.0.0 (2023); v12.10.x adds minor features (zIndexMode, experimental middleware hook, EdgeToolbar). All Pro features (undo/redo example, copy-paste example) are behind a paywall but the underlying primitives are free. No accidental Pro dependency risk if you build from the free API.

---

## Findings

### 1. @xyflow/react v12 Current State

**Latest version**: `@xyflow/react@12.10.2` — released **2026-03-27**  
**Source**: [GitHub releases](https://github.com/xyflow/xyflow/releases) (verified 2026-05-24)

**Recent notable releases** (all < 6 months old — flag ⚠️):
- `12.10.2` (2026-03-27): Allow `type` field missing in `BuiltInNode`; `snapGrid` in `screenToFlowPosition`; pass options to `useReactFlow`
- `12.10.1` (2026-02-19): Keep `onConnectEnd`/`isValidConnection` up-to-date during ongoing connection; optimize zoom performance; prevent unnecessary updates
- `12.10.0` (2025-12-04): Add `zIndexMode` prop; add `experimental_useOnNodesChangeMiddleware` hook ⚠️ (experimental)
- `12.9.0` (2025-10-20): Add `EdgeToolbar` component; prevent child nodes of different parents from overlapping

**Package name change (v11 → v12 breaking)**:
```ts
// OLD (v11)
import ReactFlow from 'reactflow';

// NEW (v12)
import { ReactFlow } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
```
Source: [Migrate to v12 guide](https://reactflow.dev/learn/troubleshooting/migrate-to-v12) (last updated 2026-04-20)

**Key v12 breaking changes relevant to controlai-web**:
1. `node.width`/`node.height` are now **inline styles** (not measured values). Measured values live in `node.measured.width`/`node.measured.height`. If loading from Postgres JSONB, strip `width`/`height` from stored nodes or use `initialWidth`/`initialHeight` for dynamic sizing.
2. `parentNode` → `parentId` (subflows)
3. `onEdgeUpdate` → `onReconnect`; `updateEdge` → `reconnectEdge`
4. `nodeInternals` → `nodeLookup` in store
5. Custom node props: `xPos`/`yPos` → `positionAbsoluteX`/`positionAbsoluteY`
6. No object mutation for node/edge updates — must spread new objects

**SSR / Next.js App Router**:
- SSR is **fully supported since v12.0.0**
- The `<ReactFlow>` component and all hooks require browser APIs (DOM measurement), so the canvas component file must be marked `'use client'`
- For SSR pre-rendering (e.g., OG images), pass explicit `width`/`height` on nodes and `handles` array for edge rendering:
  ```ts
  const nodes = [{
    id: '1', type: 'sensor',
    position: { x: 0, y: 0 },
    data: { label: 'Sensor A' },
    width: 180, height: 80,          // static SSR dimensions
    handles: [
      { type: 'source', position: Position.Bottom, x: 90, y: 80 }
    ]
  }];
  ```
- `ReactFlowProvider` accepts `initialNodes`, `initialEdges`, `initialWidth`, `initialHeight`, `fitView` for SSR hydration
- Source: [SSR guide](https://reactflow.dev/learn/advanced-use/ssr-ssg-configuration) (last updated 2026-04-20)

**`'use client'` pattern for Next.js App Router**:
```tsx
// app/canvas/page.tsx (Server Component — safe)
import { FlowCanvas } from '@/components/FlowCanvas';
export default function Page() { return <FlowCanvas />; }

// components/FlowCanvas.tsx
'use client';
import { ReactFlow, ReactFlowProvider } from '@xyflow/react';
// ... all xyflow usage here
```

---

### 2. Custom Node Types — Idiomatic Shape

**Core pattern** (from [official docs](https://reactflow.dev/learn/customization/custom-nodes), last updated 2026-04-20):

```tsx
// Define OUTSIDE component to prevent re-renders
const nodeTypes = {
  sensor: SensorNode,
  gateway: GatewayNode,
  broker: BrokerNode,
  ingest: IngestNode,
  timescaledb: TimescaleDBNode,
  monitoring: MonitoringNode,
} satisfies NodeTypes;

// TypeScript: discriminated union for typed node data
type SensorNodeData = { deviceId: string; protocol: 'mqtt' | 'coap'; sampleRateHz: number };
type GatewayNodeData = { host: string; port: number; tls: boolean };
// ...
type AppNode =
  | Node<SensorNodeData, 'sensor'>
  | Node<GatewayNodeData, 'gateway'>
  | Node<BrokerNodeData, 'broker'>
  | Node<IngestNodeData, 'ingest'>
  | Node<TimescaleDBNodeData, 'timescaledb'>
  | Node<MonitoringNodeData, 'monitoring'>;
```

**Custom node component shape**:
```tsx
import { memo } from 'react';
import { Handle, Position, NodeProps } from '@xyflow/react';

// Wrap in memo — critical for performance
const SensorNode = memo(function SensorNode({ data, selected }: NodeProps<Node<SensorNodeData, 'sensor'>>) {
  return (
    <div className={`sensor-node ${selected ? 'selected' : ''}`}>
      <Handle type="target" position={Position.Top} id="data-in" />
      <div className="node-header">
        <span className="node-icon">📡</span>
        <span>{data.deviceId}</span>
      </div>
      <div className="node-body">
        <span>{data.protocol.toUpperCase()}</span>
        <span>{data.sampleRateHz} Hz</span>
      </div>
      <Handle type="source" position={Position.Bottom} id="data-out" />
    </div>
  );
});
```

**Typed port colors** — the idiomatic pattern from production apps (Langflow, InvokeAI) is to encode port type in the handle `id` and use CSS variables keyed to data type:
```tsx
// Handle with typed port
<Handle
  type="source"
  position={Position.Right}
  id="metrics-out"
  style={{ background: 'var(--port-color-metrics)' }}
  className="handle-metrics"
/>
```

**Real-world examples from GitHub**:

1. **InvokeAI** (AI workflow editor, 27k stars) — `@xyflow/react` with typed handle validation:
   - Flow component: [`invokeai/frontend/web/src/features/nodes/components/flow/Flow.tsx`](https://github.com/invoke-ai/InvokeAI/blob/main/invokeai/frontend/web/src/features/nodes/components/flow/Flow.tsx)
   - Uses `useIsValidConnection` hook pattern, `useUpdateNodeInternals`, `useConnection`

2. **Langflow** (149k stars) — production node editor with typed colored handles:
   - Handle render: [`src/frontend/src/CustomNodes/GenericNode/components/handleRenderComponent/index.tsx`](https://github.com/langflow-ai/langflow/blob/main/src/frontend/src/CustomNodes/GenericNode/components/handleRenderComponent/index.tsx)
   - Pattern: CSS variable `hsl(var(--datatype-${colorName}))` per port type; neon glow animation on hover; `memo` + `useShallow` for Zustand selectors
   - Handle color lookup: `nodeColorsName[outputType]` → CSS variable name

3. **xyflow official validation example**:
   - [`examples/react/src/examples/Validation/index.tsx`](https://github.com/xyflow/xyflow/blob/main/examples/react/src/examples/Validation/index.tsx)
   - Shows `isValidConnection` at the `<ReactFlow>` level (preferred over per-Handle)

4. **chaiNNer** (image processing node editor):
   - [`src/renderer/components/Handle.tsx`](https://github.com/chaiNNer-org/chaiNNer/blob/main/src/renderer/components/Handle.tsx)
   - Per-handle `isValidConnection` with `Validity` type; typed `HandleType = 'input' | 'output'`

5. **xyflow Handle source** (internal):
   - [`packages/react/src/components/Handle/index.tsx`](https://github.com/xyflow/xyflow/blob/main/packages/react/src/components/Handle/index.tsx)
   - Shows how `isValidConnection` on Handle falls back to the ReactFlow-level prop

---

### 3. Connection Validation — `isValidConnection` Patterns

**Type signature** (v12):
```ts
type IsValidConnection = (connection: Connection) => boolean;
// Connection = { source: string; sourceHandle: string | null; target: string; targetHandle: string | null }
```

**Recommended placement**: on `<ReactFlow isValidConnection={...}>` (not per-Handle) — called for all connections including programmatic ones. Per-Handle `isValidConnection` is for local overrides only.

**Pattern 1 — Simple target-type check** (from xyflow official example):
```ts
const isValidConnection: IsValidConnection = (connection) =>
  connection.target === 'B';
```
Source: [xyflow/xyflow Validation example](https://github.com/xyflow/xyflow/blob/main/examples/react/src/examples/Validation/index.tsx)

**Pattern 2 — Port-type compatibility matrix** (for controlai-web):
```ts
// Define allowed connections: sourceNodeType → targetNodeType
const CONNECTION_MATRIX: Record<string, string[]> = {
  sensor:      ['gateway', 'broker'],
  gateway:     ['broker', 'ingest'],
  broker:      ['ingest'],
  ingest:      ['timescaledb', 'monitoring'],
  timescaledb: ['monitoring'],
  monitoring:  [],
};

const isValidConnection: IsValidConnection = useCallback((connection) => {
  const sourceNode = getNode(connection.source);
  const targetNode = getNode(connection.target);
  if (!sourceNode || !targetNode) return false;
  const allowed = CONNECTION_MATRIX[sourceNode.type ?? ''] ?? [];
  return allowed.includes(targetNode.type ?? '');
}, [getNode]);
```

**Pattern 3 — Handle-ID-based port type matching** (InvokeAI approach):
```ts
// Encode port type in handle ID: "data-out:metrics", "data-in:metrics"
const isValidConnection: IsValidConnection = useCallback(({ sourceHandle, targetHandle }) => {
  if (!sourceHandle || !targetHandle) return false;
  const [, sourceType] = sourceHandle.split(':');
  const [, targetType] = targetHandle.split(':');
  return sourceType === targetType || COMPATIBLE_TYPES[sourceType]?.includes(targetType);
}, []);
```

**Pattern 4 — Full graph validation** (InvokeAI `validateConnection.ts`):
- Checks: no self-connections, no duplicate edges, no cycles (`getHasCycles`), field type compatibility, single-input constraint
- Source: [`invokeai/frontend/web/src/features/nodes/store/util/validateConnection.ts`](https://github.com/invoke-ai/InvokeAI/blob/main/invokeai/frontend/web/src/features/nodes/store/util/validateConnection.ts) (434 lines, Apache-2.0)
- Key function: `validateConnection(connection, nodes, edges, templates, ignoreEdge, strict)` returns error key string or `null`

**Cycle prevention** — use `getOutgoers` from xyflow:
```ts
import { getOutgoers } from '@xyflow/react';

const isValidConnection: IsValidConnection = useCallback((connection) => {
  // ... type checks ...
  // Prevent cycles
  const hasCycle = (nodeId: string, visited = new Set<string>()): boolean => {
    if (visited.has(nodeId)) return true;
    visited.add(nodeId);
    return getOutgoers({ id: nodeId }, nodes, edges)
      .some(n => hasCycle(n.id, visited));
  };
  return !hasCycle(connection.source);
}, [nodes, edges]);
```

---

### 4. Undo/Redo — Idiomatic Store Pattern

**Official undo/redo example**: Listed at [reactflow.dev/examples/interaction/undo-redo](https://reactflow.dev/examples/interaction/undo-redo) but is a **Pro example** (requires subscription). The underlying primitives are free.

**Idiomatic free implementation** — snapshot-based with Zustand:

```ts
// store/flowStore.ts
import { create } from 'zustand';
import { applyNodeChanges, applyEdgeChanges } from '@xyflow/react';
import type { Node, Edge, OnNodesChange, OnEdgesChange } from '@xyflow/react';

type Snapshot = { nodes: Node[]; edges: Edge[] };

interface FlowStore {
  nodes: Node[];
  edges: Edge[];
  past: Snapshot[];
  future: Snapshot[];
  onNodesChange: OnNodesChange;
  onEdgesChange: OnEdgesChange;
  takeSnapshot: () => void;
  undo: () => void;
  redo: () => void;
}

export const useFlowStore = create<FlowStore>((set, get) => ({
  nodes: [],
  edges: [],
  past: [],
  future: [],

  takeSnapshot: () => {
    const { nodes, edges, past } = get();
    set({
      past: [...past.slice(-49), { nodes, edges }], // cap at 50
      future: [],
    });
  },

  onNodesChange: (changes) => {
    set({ nodes: applyNodeChanges(changes, get().nodes) });
  },

  onEdgesChange: (changes) => {
    set({ edges: applyEdgeChanges(changes, get().edges) });
  },

  undo: () => {
    const { past, nodes, edges, future } = get();
    if (!past.length) return;
    const previous = past[past.length - 1];
    set({
      nodes: previous.nodes,
      edges: previous.edges,
      past: past.slice(0, -1),
      future: [{ nodes, edges }, ...future],
    });
  },

  redo: () => {
    const { future, nodes, edges, past } = get();
    if (!future.length) return;
    const next = future[0];
    set({
      nodes: next.nodes,
      edges: next.edges,
      past: [...past, { nodes, edges }],
      future: future.slice(1),
    });
  },
}));
```

**Triggering snapshots** — call `takeSnapshot()` before mutations:
```tsx
const { takeSnapshot, onNodesChange } = useFlowStore();

const handleNodesChange: OnNodesChange = useCallback((changes) => {
  // Only snapshot on drag-stop or delete, not on drag (position changes)
  const hasSignificantChange = changes.some(
    c => c.type === 'remove' || (c.type === 'position' && c.dragging === false)
  );
  if (hasSignificantChange) takeSnapshot();
  onNodesChange(changes);
}, [takeSnapshot, onNodesChange]);
```

**Keyboard shortcuts**:
```tsx
useKeyPress(['Meta+z', 'Control+z'], undo);
useKeyPress(['Meta+Shift+z', 'Control+Shift+z'], redo);
```

**Why Zustand over `useReactFlow`**: `useReactFlow().toObject()` returns the full serialized graph (nodes + edges + viewport) but is a snapshot of the *current* state — it doesn't manage history. Zustand is the idiomatic choice because xyflow uses it internally and the `useStore`/`useStoreApi` hooks integrate cleanly.

---

### 5. Persistence — `toObject()` Shape, Restore, Version Migration

**`ReactFlowJsonObject` type** (from [API reference](https://reactflow.dev/api-reference/types/react-flow-json-object), last updated 2026-04-20):
```ts
type ReactFlowJsonObject<NodeType, EdgeType> = {
  nodes: NodeType[];
  edges: EdgeType[];
  viewport: Viewport; // { x: number; y: number; zoom: number }
};
```

**Save to Postgres JSONB**:
```ts
// In a tRPC mutation
const { getNodes, getEdges, getViewport } = useReactFlow();

const saveGraph = async () => {
  const snapshot = {
    nodes: getNodes(),
    edges: getEdges(),
    viewport: getViewport(),
    version: CURRENT_SCHEMA_VERSION, // e.g., 2
    savedAt: new Date().toISOString(),
  };
  await trpc.graph.save.mutate({ graphId, snapshot });
};
```

**Restore from JSONB**:
```ts
const { setNodes, setEdges, setViewport } = useReactFlow();

const loadGraph = (snapshot: ReactFlowJsonObject) => {
  // Strip width/height if they were stored from v11 (they'd override dynamic sizing)
  const nodes = snapshot.nodes.map(({ width, height, ...node }) => node);
  setNodes(nodes);
  setEdges(snapshot.edges);
  setViewport(snapshot.viewport);
};
```

**Version migration when node-type schemas evolve**:
```ts
type MigrationFn = (nodes: Node[], edges: Edge[]) => { nodes: Node[]; edges: Edge[] };

const MIGRATIONS: Record<number, MigrationFn> = {
  1: (nodes, edges) => ({
    // v1→v2: rename 'ingest' node data field 'endpoint' → 'url'
    nodes: nodes.map(n =>
      n.type === 'ingest'
        ? { ...n, data: { ...n.data, url: n.data.endpoint, endpoint: undefined } }
        : n
    ),
    edges,
  }),
};

function migrateGraph(snapshot: any): ReactFlowJsonObject {
  let { nodes, edges, viewport, version = 1 } = snapshot;
  while (version < CURRENT_SCHEMA_VERSION) {
    ({ nodes, edges } = MIGRATIONS[version](nodes, edges));
    version++;
  }
  return { nodes, edges, viewport };
}
```

**"Applied" snapshot pattern** — for the "applied to infrastructure" snapshot, store a separate JSONB column:
```sql
ALTER TABLE graphs ADD COLUMN applied_snapshot JSONB;
ALTER TABLE graphs ADD COLUMN applied_at TIMESTAMPTZ;
```
On apply: copy current `snapshot` → `applied_snapshot`, set `applied_at = NOW()`.

---

### 6. Performance at Scale

Source: [Performance guide](https://reactflow.dev/learn/advanced-use/performance) (last updated 2026-04-20)

**Known limits**: No official hard limit documented. Community reports:
- 100–200 nodes: smooth with default settings
- 500+ nodes: requires memoization discipline
- 1000+ nodes: requires `hidden` toggling (collapse subtrees) + simplified styles
- The [Stress test example](https://reactflow.dev/examples/nodes/stress) demonstrates 1000 nodes

**Critical rules**:

1. **Define `nodeTypes` outside the component** (prevents new object reference on every render):
   ```ts
   // ✅ OUTSIDE component
   const nodeTypes = { sensor: SensorNode, gateway: GatewayNode };
   
   // ❌ INSIDE component — recreates on every render, causes all nodes to remount
   function Canvas() {
     const nodeTypes = { sensor: SensorNode }; // BAD
   }
   ```

2. **Wrap custom nodes in `React.memo`**:
   ```ts
   const SensorNode = memo(function SensorNode({ data }: NodeProps) { ... });
   ```

3. **Memoize callbacks with `useCallback`**:
   ```ts
   const onNodeClick = useCallback((event, node) => { ... }, []);
   ```

4. **Avoid `useNodes()` / `useEdges()` in components** — these re-render on every node/edge change. Use targeted selectors instead:
   ```ts
   // ❌ Re-renders on any node change
   const nodes = useNodes();
   const selected = nodes.filter(n => n.selected);
   
   // ✅ Only re-renders when selection changes
   const selectedIds = useStore(s =>
     [...s.nodeLookup.values()]
       .filter(n => n.selected)
       .map(n => n.id)
   );
   ```

5. **Use `useNodesData(id)` for node-to-node data flow** (v12 feature):
   ```ts
   // In a downstream node, subscribe only to upstream node's data
   const upstreamData = useNodesData('sensor-1');
   ```

6. **Collapse large subtrees** using `node.hidden`:
   ```ts
   setNodes(nodes => nodes.map(n =>
     childIds.includes(n.id) ? { ...n, hidden: !n.hidden } : n
   ));
   ```

7. **Simplify styles** — avoid CSS animations, box-shadows, gradients on nodes when count > 200.

8. **`zIndexMode` prop** (added v12.10.0, ⚠️ < 6 months old): controls z-index calculation strategy for nodes/edges.

---

### 7. Real-Time Overlay on Nodes — Live Throughput/State

**The core challenge**: WebSocket/SSE telemetry arrives at high frequency; naively updating node `data` triggers full canvas re-renders.

**Pattern A — Separate telemetry store (recommended)**:
Keep telemetry in a separate Zustand store, not in `node.data`. Nodes subscribe only to their own telemetry slice:

```ts
// stores/telemetryStore.ts
interface TelemetryStore {
  metrics: Record<string, NodeMetrics>; // nodeId → metrics
  updateMetrics: (nodeId: string, metrics: NodeMetrics) => void;
}

export const useTelemetryStore = create<TelemetryStore>((set) => ({
  metrics: {},
  updateMetrics: (nodeId, metrics) =>
    set(state => ({ metrics: { ...state.metrics, [nodeId]: metrics } })),
}));

// In SensorNode component — only re-renders when THIS node's metrics change
const SensorNode = memo(function SensorNode({ id, data }: NodeProps) {
  const metrics = useTelemetryStore(
    useCallback(s => s.metrics[id], [id])
  );
  return (
    <div>
      <div className="node-body">{data.deviceId}</div>
      {metrics && (
        <div className="node-overlay">
          <StatusBadge status={metrics.status} />
          <Sparkline values={metrics.throughputHistory} />
        </div>
      )}
    </div>
  );
});
```

**Pattern B — `updateNodeData` from `useReactFlow`** (v12 feature):
```ts
const { updateNodeData } = useReactFlow();

// In WebSocket handler
ws.onmessage = (event) => {
  const { nodeId, metrics } = JSON.parse(event.data);
  updateNodeData(nodeId, { liveMetrics: metrics });
};
```
⚠️ This triggers re-render of the specific node only (not the whole canvas), but still goes through React's reconciler. For high-frequency updates (>10/s per node), Pattern A is more efficient.

**Pattern C — CSS variable injection** (zero-React-render for color changes):
```ts
// Update CSS variable directly on the node DOM element
const nodeEl = document.querySelector(`[data-id="${nodeId}"]`);
if (nodeEl) {
  (nodeEl as HTMLElement).style.setProperty('--node-status-color', statusColor);
}
```
Used by Langflow for handle glow effects. Bypasses React entirely for visual-only updates.

**Pattern D — `ViewportPortal` for overlays** (v12 feature):
Render live badges in the viewport coordinate space without being inside node components:
```tsx
<ViewportPortal>
  {Object.entries(metrics).map(([nodeId, m]) => (
    <NodeOverlay key={nodeId} nodeId={nodeId} metrics={m} />
  ))}
</ViewportPortal>
```

**Sparkline recommendation**: Use a lightweight canvas-based sparkline (e.g., `react-sparklines` or a custom `<canvas>` element) inside the node. Avoid SVG-heavy charting libraries (Recharts, Victory) inside nodes — they add significant DOM weight.

---

### 8. License + Commercial Use

**License**: **MIT** — confirmed at:
- [github.com/xyflow/xyflow/blob/main/LICENSE](https://github.com/xyflow/xyflow/blob/main/LICENSE)
- [reactflow.dev footer](https://reactflow.dev) — "MIT License"

**Commercial use**: Fully permitted under MIT. No royalties, no attribution requirement in the product (though the library shows a small "React Flow" attribution watermark by default — removable via `proOptions={{ hideAttribution: true }}` which requires a Pro subscription, OR by becoming a sponsor).

**"Pro" upsell** — what's behind the paywall:
- Pro examples (undo/redo, copy-paste, helper lines, collaborative) — **source code only**, not runtime features
- The actual undo/redo, copy-paste, etc. functionality is built from free primitives (`useReactFlow`, `useStore`, Zustand)
- **No Pro runtime dependency** — all hooks, components, and APIs in `@xyflow/react` are MIT

**Safe list** (all free, MIT):
- `ReactFlow`, `ReactFlowProvider`, `Background`, `Controls`, `MiniMap`
- All hooks: `useReactFlow`, `useNodes`, `useEdges`, `useStore`, `useNodesData`, `useHandleConnections`, `useConnection`, `useKeyPress`, etc.
- All components: `Handle`, `NodeToolbar`, `NodeResizer`, `EdgeToolbar`, `ViewportPortal`, `Panel`
- All utils: `addEdge`, `applyNodeChanges`, `applyEdgeChanges`, `getOutgoers`, `getIncomers`, `reconnectEdge`, etc.

**Do NOT accidentally depend on**:
- `proOptions` prop (only needed to hide attribution)
- Any import from `@xyflow/react/pro` (doesn't exist — Pro is just example code)

---

### 9. Alternatives Briefly Compared

| Library | Stars | License | Verdict for controlai-web |
|---------|-------|---------|--------------------------|
| **@xyflow/react** | 36.7k | MIT | ✅ Best fit — mature, TypeScript-first, SSR, active maintenance |
| **rete.js** | ~10k | MIT | ❌ v2 API is less ergonomic; smaller ecosystem; no built-in SSR |
| **drawflow** | ~4k | MIT | ❌ Vanilla JS, no React integration, no TypeScript types |
| **litegraph.js** | ~5k | MIT | ❌ Canvas-based (not DOM), harder to embed React components in nodes |
| **react-diagrams** (STORM) | ~8k | MIT | ❌ Less active, more complex API, fewer examples |

**Why @xyflow/react wins for controlai-web**:
1. **TypeScript-first** with discriminated union node types — critical for 6 typed node types
2. **SSR support** — needed for Next.js App Router
3. **Zustand integration** — already used internally, aligns with recommended state management
4. **`isValidConnection`** at the ReactFlow level — clean port-type enforcement
5. **`useNodesData` + `updateNodeData`** — built-in reactive data flow between nodes
6. **Active maintenance** — 10 releases in 2025-2026, responsive to issues
7. **Production proof** — used by InvokeAI (27k stars), Langflow (149k stars), and many others

---

## Recommendations

### For controlai-web specifically:

1. **Node type definition**: Use discriminated union `AppNode` type. Define `nodeTypes` map at module level (outside any component). Wrap each node component in `React.memo`.

2. **Port colors**: Encode port type in handle `id` (e.g., `"data-out:timeseries"`, `"data-in:timeseries"`). Use CSS variables `--port-color-timeseries`, `--port-color-control`, etc. for theming. Follow Langflow's `hsl(var(--datatype-${type}))` pattern.

3. **Connection validation**: Implement `isValidConnection` at the `<ReactFlow>` level using a `CONNECTION_MATRIX` (source node type → allowed target node types). Add cycle detection via `getOutgoers`. For handle-level port type matching, encode type in handle ID.

4. **Undo/redo**: Implement snapshot-based history in Zustand. Snapshot on `onNodeDragStop`, `onConnect`, `onNodesDelete`, `onEdgesDelete`. Cap history at 50 entries. Do NOT use the Pro example (it's paywalled source code, not a runtime dependency).

5. **Persistence**: Store `{ nodes, edges, viewport, version, savedAt }` in Postgres JSONB. On load, strip `width`/`height` from stored nodes (use `initialWidth`/`initialHeight` for dynamic sizing). Implement a `migrateGraph(snapshot)` function keyed by version number.

6. **Real-time overlay**: Keep telemetry in a **separate Zustand store** (`useTelemetryStore`). Subscribe per-node with `useCallback(s => s.metrics[id], [id])`. For high-frequency color changes (status indicators), use CSS variable injection directly on the DOM node element to bypass React reconciler.

7. **Performance**: With 6 node types and likely < 50 nodes in typical graphs, performance is not a concern. Still: always `memo` nodes, always define `nodeTypes` outside components, use `useNodesData` for inter-node data flow.

8. **SSR**: Wrap the canvas in `'use client'`. For the "applied" snapshot preview (read-only render), use `renderToStaticMarkup` with explicit `width`/`height` on nodes.

9. **`'use client'` boundary**: Place it at the canvas wrapper component level, not at the page level. This allows the page to remain a Server Component for data fetching.

---

## Sources

| Source | URL | Date | Age |
|--------|-----|------|-----|
| @xyflow/react releases | https://github.com/xyflow/xyflow/releases | 2026-05-24 | Current |
| Custom Nodes guide | https://reactflow.dev/learn/customization/custom-nodes | 2026-04-20 | < 6 months ⚠️ |
| Performance guide | https://reactflow.dev/learn/advanced-use/performance | 2026-04-20 | < 6 months ⚠️ |
| State Management guide | https://reactflow.dev/learn/advanced-use/state-management | 2026-04-20 | < 6 months ⚠️ |
| SSR guide | https://reactflow.dev/learn/advanced-use/ssr-ssg-configuration | 2026-04-20 | < 6 months ⚠️ |
| Migrate to v12 | https://reactflow.dev/learn/troubleshooting/migrate-to-v12 | 2026-04-20 | < 6 months ⚠️ |
| ReactFlowJsonObject type | https://reactflow.dev/api-reference/types/react-flow-json-object | 2026-04-20 | < 6 months ⚠️ |
| Undo/Redo example (Pro) | https://reactflow.dev/examples/interaction/undo-redo | 2026-04-20 | < 6 months ⚠️ |
| MIT License | https://github.com/xyflow/xyflow/blob/main/LICENSE | — | Stable |
| InvokeAI Flow.tsx | https://github.com/invoke-ai/InvokeAI/blob/main/invokeai/frontend/web/src/features/nodes/components/flow/Flow.tsx | 2026-05-24 | Current |
| InvokeAI validateConnection.ts | https://github.com/invoke-ai/InvokeAI/blob/main/invokeai/frontend/web/src/features/nodes/store/util/validateConnection.ts | 2026-05-24 | Current |
| InvokeAI useIsValidConnection.ts | https://github.com/invoke-ai/InvokeAI/blob/main/invokeai/frontend/web/src/features/nodes/hooks/useIsValidConnection.ts | 2026-05-24 | Current |
| Langflow handleRenderComponent | https://github.com/langflow-ai/langflow/blob/main/src/frontend/src/CustomNodes/GenericNode/components/handleRenderComponent/index.tsx | 2026-05-24 | Current |
| xyflow Validation example | https://github.com/xyflow/xyflow/blob/main/examples/react/src/examples/Validation/index.tsx | 2026-05-24 | Current |
| xyflow Handle source | https://github.com/xyflow/xyflow/blob/main/packages/react/src/components/Handle/index.tsx | 2026-05-24 | Current |
| chaiNNer Handle.tsx | https://github.com/chaiNNer-org/chaiNNer/blob/main/src/renderer/components/Handle.tsx | 2026-05-24 | Current |

⚠️ Items marked "< 6 months" are from the official docs site which was last updated 2026-04-20 — these reflect the current v12.10.x API and are authoritative.
