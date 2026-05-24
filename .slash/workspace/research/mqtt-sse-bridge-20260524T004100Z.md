# Research: MQTT → SSE Bridge for controlai-web BFF
Date: 2026-05-24T00:41:00Z

## Summary

The BFF (`packages/mqtt-bridge`) must subscribe to per-site Mosquitto/EMQX brokers over mTLS, fan out messages to N browser clients per site via Server-Sent Events, and optionally cache the last-N messages in Redis Streams for fast widget bootstrap. This document covers all eight research areas with cited evidence, concrete code patterns, and explicit gotchas for the controlai-web context.

---

## 1. MQTT.js Client in Node.js — Current State, mTLS, Multi-Broker, Reconnect, Backpressure

### Current state (2026)

MQTT.js v5.x is the production-stable release. It was fully rewritten in TypeScript in v5.0.0 (July 2023) and requires Node.js ≥ 18. The package is `mqtt` on npm (9.1k stars, actively maintained by Matteo Collina and Daniel Lando).

- **Source**: [mqttjs/MQTT.js README](https://github.com/mqttjs/MQTT.js/blob/main/README.md) (fetched 2026-05-24)
- **Latest SHA**: `c4be5633594b5b839862440aa78125f22021f8e6`

### mTLS setup (two-way authentication)

The `mqtt.connect()` options object is passed directly to Node.js `tls.connect()`. For mTLS (mutual TLS, i.e. client cert + server CA verification):

```typescript
import mqtt from 'mqtt'
import fs from 'fs'

const client = mqtt.connect(`mqtts://${brokerHost}:8883`, {
  // mTLS: client presents its cert/key; server is verified against CA
  ca:   [fs.readFileSync('/certs/ca.crt')],
  cert: fs.readFileSync('/certs/client.crt'),
  key:  fs.readFileSync('/certs/client.key'),
  rejectUnauthorized: true,   // MUST be true in production
  // Reconnect
  reconnectPeriod: 2000,      // ms between reconnect attempts (default 1000)
  connectTimeout: 10_000,     // ms to wait for CONNACK
  keepalive: 30,              // seconds
  clean: false,               // preserve QoS 1/2 session across reconnects
  clientId: `bff-site-${siteId}`,
})
```

**Evidence** — MQTT.js test suite for secure client ([`test/node/secure_client.ts`](https://github.com/mqttjs/MQTT.js/blob/c4be5633594b5b839862440aa78125f22021f8e6/test/node/secure_client.ts#L95-L115)):
```typescript
const client = mqtt.connect({
  protocol: 'mqtts',
  port,
  ca: [fs.readFileSync(CERT)],
  rejectUnauthorized: true,
})
```

**Evidence** — EMQX official Node.js guide (two-way auth section, [emqx.com/en/blog/how-to-use-mqtt-in-nodejs](https://www.emqx.com/en/blog/how-to-use-mqtt-in-nodejs), updated June 2025):
```typescript
const client = mqtt.connect(connectUrl, {
  rejectUnauthorized: true,
  ca:   fs.readFileSync('./broker.emqx.io-ca.crt'),
  key:  fs.readFileSync('./client.key'),
  cert: fs.readFileSync('./client.crt'),
})
```

**Gotcha**: `rejectUnauthorized: false` is only acceptable in dev/test. The EMQX blog explicitly warns: "this configuration is not recommended in a production environment." For Mosquitto with self-signed certs, set the `Alt name` (SAN) in the cert to match the broker hostname, or SNI will fail.

### Multi-broker connection management (one process, 5+ brokers)

Each `mqtt.connect()` call returns an independent `MqttClient` instance. There is **no built-in multi-broker client** — you create one client per site broker and manage them in a `Map<siteId, MqttClient>`.

```typescript
// packages/mqtt-bridge/src/broker-registry.ts
import mqtt, { MqttClient } from 'mqtt'
import fs from 'fs'

interface BrokerConfig {
  host: string
  port: number
  caCert: Buffer
  clientCert: Buffer
  clientKey: Buffer
}

const clients = new Map<string, MqttClient>()

export function getOrCreateClient(siteId: string, cfg: BrokerConfig): MqttClient {
  if (clients.has(siteId)) return clients.get(siteId)!

  const client = mqtt.connect(`mqtts://${cfg.host}:${cfg.port}`, {
    ca:   [cfg.caCert],
    cert: cfg.clientCert,
    key:  cfg.clientKey,
    rejectUnauthorized: true,
    reconnectPeriod: 2000,
    connectTimeout: 10_000,
    keepalive: 30,
    clean: false,
    clientId: `bff-${siteId}-${Date.now()}`,
  })

  client.on('connect', () => console.log(`[mqtt-bridge] connected to site ${siteId}`))
  client.on('error',   (err) => console.error(`[mqtt-bridge] site ${siteId} error`, err))
  client.on('offline', () => console.warn(`[mqtt-bridge] site ${siteId} offline`))

  clients.set(siteId, client)
  return client
}

export function destroyClient(siteId: string) {
  clients.get(siteId)?.endAsync()
  clients.delete(siteId)
}
```

**Source**: MQTT.js README — "Can I connect to multiple brokers with a single MQTT.js client? No, each MQTT.js client can only connect to one broker at a time. If you want to connect to multiple brokers, you need to create multiple MQTT.js client instances." ([emqx.com/en/blog/mqtt-js-tutorial](https://www.emqx.com/en/blog/mqtt-js-tutorial), June 2024)

### Reconnect / backoff

MQTT.js has **built-in linear reconnect** via `reconnectPeriod` (default 1000 ms). There is no built-in exponential backoff. For production, implement it yourself by listening to `reconnect` events and dynamically adjusting `client.options.reconnectPeriod`:

```typescript
let backoff = 1000
client.on('reconnect', () => {
  backoff = Math.min(backoff * 2, 30_000)
  client.options.reconnectPeriod = backoff
})
client.on('connect', () => { backoff = 1000 }) // reset on success
```

**Source**: MQTT.js README — "Enabling Reconnection with `reconnectPeriod` option" section. Also: `reconnectOnConnackError: true` (added in v5) enables reconnect even when the broker rejects with a CONNACK error code.

### Message backpressure

MQTT.js exposes `client.handleMessage(packet, callback)` for backpressure-aware processing. The callback **must always be called** or the client hangs:

```typescript
client.handleMessage = (packet, callback) => {
  // process synchronously or await async work, then:
  processMessage(packet).then(() => callback()).catch(callback)
}
```

For high-rate telemetry (10–100 msg/sec × ~1 KB), the in-process EventEmitter fan-out (§3) is fast enough. If SSE clients are slow, use a bounded queue (e.g. `p-queue` with concurrency=1) per site to avoid unbounded memory growth.

**Source**: MQTT.js README — `mqtt.Client#handleMessage(packet, callback)` — "Handle messages with backpressure support, one at a time. Override at will, but **always call `callback`**, or the client will hang."

---

## 2. SSE in Next.js 16 (App Router)

### Idiomatic route handler

Next.js 16 App Router route handlers use the Web `Response` + `ReadableStream` API. The route must be in the **Node.js runtime** (not Edge) because MQTT.js uses Node.js TLS APIs.

**File**: `apps/web/src/app/api/sites/[siteId]/stream/route.ts`

```typescript
import type { NextRequest } from 'next/server'
import { cookies } from 'next/headers'
import { streamManager } from '@/lib/stream-manager'
import { validateSiteAccess } from '@/lib/auth'

// MUST be Node.js runtime — MQTT.js uses Node TLS
export const runtime = 'nodejs'
// Disable static caching for streaming routes
export const dynamic = 'force-dynamic'

export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ siteId: string }> },
) {
  const { siteId } = await params

  // Auth: read session cookie (works natively with SSE — see §6)
  const cookieStore = await cookies()
  const session = cookieStore.get('session')
  if (!session || !(await validateSiteAccess(session.value, siteId))) {
    return new Response('Unauthorized', { status: 401 })
  }

  // Last-Event-ID for reconnect replay
  const lastEventId = request.headers.get('last-event-id') ?? undefined

  const encoder = new TextEncoder()
  let controller: ReadableStreamDefaultController

  const stream = new ReadableStream({
    start(ctrl) {
      controller = ctrl

      // Register this client with the fan-out manager
      streamManager.addStream(siteId, ctrl)

      // Replay missed messages if client reconnects with Last-Event-ID
      if (lastEventId) {
        streamManager.replayFrom(siteId, lastEventId, ctrl)
      }

      // Keepalive comment every 25s (browsers time out SSE at ~30s idle)
      const keepalive = setInterval(() => {
        try {
          ctrl.enqueue(encoder.encode(': keepalive\n\n'))
        } catch {
          clearInterval(keepalive)
        }
      }, 25_000)

      // Detect client disconnect via AbortSignal
      request.signal.addEventListener('abort', () => {
        clearInterval(keepalive)
        streamManager.removeStream(siteId, ctrl)
        try { ctrl.close() } catch {}
      }, { once: true })
    },
  })

  return new Response(stream, {
    headers: {
      'Content-Type':  'text/event-stream',
      'Cache-Control': 'no-cache, no-transform',
      'Connection':    'keep-alive',
      'X-Accel-Buffering': 'no',  // Disable nginx/Traefik buffering
    },
  })
}
```

**Evidence** — Next.js 16 official docs on streaming ([nextjs.org/docs/app/api-reference/file-conventions/route](https://nextjs.org/docs/app/building-your-application/routing/route-handlers), fetched 2026-05-24):
```typescript
export const runtime = 'nodejs'
// ReadableStream with pull-based iterator
function iteratorToStream(iterator: any) {
  return new ReadableStream({
    async pull(controller) {
      const { value, done } = await iterator.next()
      if (done) { controller.close() } else { controller.enqueue(value) }
    },
  })
}
```

**Evidence** — `request.signal.addEventListener('abort', ...)` for disconnect detection ([claude-code-best/claude-code `sse-writer.ts`](https://github.com/claude-code-best/claude-code/blob/main/packages/remote-control-server/src/transport/sse-writer.ts#L11-L20)):
```typescript
const stream = new ReadableStream({
  start(controller) {
    const encoder = new TextEncoder()
    c.req.raw.signal.addEventListener('abort', () => {
      controller.close()
    })
  }
})
```

### SSE wire format

```
id: <eventId>\n
event: telemetry\n
data: {"siteId":"s1","topic":"sensors/temp","payload":"..."}\n
\n
```

- `id:` field sets the `Last-Event-ID` the browser will send on reconnect.
- `: keepalive` (comment line) prevents proxy/browser timeout without triggering a `message` event.
- `retry: 3000` can be sent once to tell the browser to wait 3s before reconnecting.

### Edge runtime vs Node runtime

| | Edge Runtime | Node Runtime |
|---|---|---|
| MQTT.js | ❌ No TLS/TCP | ✅ Full support |
| `fs` module | ❌ | ✅ |
| Long-lived connections | ⚠️ 30s Vercel limit | ✅ Unlimited |
| Cold start | ~0ms | ~100ms |

**Verdict**: Use `export const runtime = 'nodejs'` for the SSE stream route. The Edge runtime cannot hold long-lived TCP connections to MQTT brokers.

**Source**: Next.js docs — "Segment Config Options: `runtime`" — `'nodejs'` is the default for App Router route handlers.

### `X-Accel-Buffering: no`

Critical for Traefik/nginx reverse proxies. Without it, the proxy buffers the SSE stream and the browser receives events in batches. Set this header on the response.

**Evidence**: OpenCTI production SSE handler ([OpenCTI-Platform/opencti `httpChatbotProxy.ts`](https://github.com/OpenCTI-Platform/opencti/blob/master/opencti-platform/opencti-graphql/src/http/httpChatbotProxy.ts#L153-L162)):
```typescript
res.setHeader('Content-Type', 'text/event-stream')
res.setHeader('Cache-Control', 'no-cache, no-transform')
res.setHeader('Connection', 'keep-alive')
res.setHeader('X-Accel-Buffering', 'no')
res.setHeader('Transfer-Encoding', 'chunked')
```

---

## 3. Fan-Out Pattern: Single MQTT Subscriber → N Browser Clients

### The pattern

Use a **singleton `StreamManager`** (module-level, stable across HMR via `globalThis`) that holds a `Map<siteId, Set<ReadableStreamDefaultController>>`. The MQTT `message` event calls `streamManager.publish(siteId, event)` which iterates the set and enqueues to each controller.

**Evidence** — Production SSE fan-out implementation ([opactorai/Claudable `stream.ts`](https://github.com/opactorai/Claudable/blob/main/lib/services/stream.ts)):

```typescript
export class StreamManager {
  private streams: Map<string, Set<ReadableStreamDefaultController>>

  public addStream(projectId: string, controller: ReadableStreamDefaultController): void {
    if (!this.streams.has(projectId)) {
      this.streams.set(projectId, new Set())
    }
    this.streams.get(projectId)!.add(controller)
  }

  public removeStream(projectId: string, controller: ReadableStreamDefaultController): void {
    const projectStreams = this.streams.get(projectId)
    if (projectStreams) {
      projectStreams.delete(controller)
      if (projectStreams.size === 0) {
        this.streams.delete(projectId)
      }
    }
  }

  public publish(projectId: string, event: RealtimeEvent): void {
    const projectStreams = this.streams.get(projectId)
    if (!projectStreams || projectStreams.size === 0) return

    const message = `data: ${JSON.stringify(event)}\n\n`
    const encoder = new TextEncoder()
    const encodedMessage = encoder.encode(message)

    const deadControllers: ReadableStreamDefaultController[] = []
    projectStreams.forEach((controller) => {
      try {
        controller.enqueue(encodedMessage)
      } catch (error) {
        deadControllers.push(controller)  // client disconnected
      }
    })
    // Remove dead connections after iteration
    deadControllers.forEach((controller) => {
      this.removeStream(projectId, controller)
    })
  }
}

// Stable singleton across HMR
const g = globalThis as unknown as { __stream_mgr__?: StreamManager }
export const streamManager: StreamManager =
  g.__stream_mgr__ ?? (g.__stream_mgr__ = StreamManager.getInstance())
```

**Source**: [github.com/opactorai/Claudable/blob/main/lib/services/stream.ts](https://github.com/opactorai/Claudable/blob/main/lib/services/stream.ts) (MIT license, 4k stars)

### Wiring MQTT → StreamManager

```typescript
// packages/mqtt-bridge/src/site-bridge.ts
import { getOrCreateClient } from './broker-registry'
import { streamManager } from './stream-manager'

export function activateSiteBridge(siteId: string, cfg: BrokerConfig) {
  const client = getOrCreateClient(siteId, cfg)

  client.on('connect', () => {
    // Subscribe to all topics for this site
    client.subscribe(`sites/${siteId}/#`, { qos: 1 })
  })

  client.on('message', (topic, payload) => {
    const event = {
      siteId,
      topic,
      payload: payload.toString(),
      ts: Date.now(),
    }
    // Fan out to all SSE clients for this site
    streamManager.publish(siteId, event)
    // Also write to Redis cache (§5)
    redisCache.append(siteId, topic, event)
  })
}
```

### Why not re-subscribe MQTT per browser client?

Each MQTT subscription creates a new TCP connection to the broker. At 100 browser clients × 5 sites = 500 MQTT connections. Mosquitto's default `max_connections` is 1000 but each connection consumes ~50 KB RAM. The single-subscriber fan-out pattern uses exactly 1 MQTT connection per site regardless of browser client count.

### In-process EventEmitter alternative

For simpler cases, Node.js `EventEmitter` works as the fan-out bus:

```typescript
import { EventEmitter } from 'events'
const bus = new EventEmitter()
bus.setMaxListeners(1000) // raise from default 10

// MQTT message handler
client.on('message', (topic, payload) => {
  bus.emit(`site:${siteId}`, { topic, payload: payload.toString() })
})

// SSE route registers listener
const listener = (event: any) => {
  try { controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`)) }
  catch { bus.off(`site:${siteId}`, listener) }
}
bus.on(`site:${siteId}`, listener)
request.signal.addEventListener('abort', () => {
  bus.off(`site:${siteId}`, listener)
}, { once: true })
```

**Recommendation**: Use the `StreamManager` (Map of Sets) pattern over EventEmitter for controlai-web — it's more explicit, easier to count active clients, and avoids the `MaxListenersExceededWarning`.

---

## 4. Horizontal Scale: Who Owns the MQTT Subscription?

### Phase 1 (single BFF instance)

No coordination needed. One BFF process owns all MQTT subscriptions. The `StreamManager` singleton lives in-process.

### Phase 2 (2+ BFF instances) — what we're deferring

**Problem**: If BFF-A and BFF-B both subscribe to `sites/s1/#`, both receive every message. Browser clients connected to BFF-A get messages from BFF-A's MQTT subscription; clients on BFF-B get messages from BFF-B's subscription. This is actually fine for telemetry fan-out — **both instances independently fan out to their own connected clients**. No coordination needed for the fan-out itself.

**Problem that does need coordination**: If you want to avoid duplicate MQTT subscriptions (e.g. to reduce broker load), you need a leader election per site. Options:

1. **Redis-coordinated leader election** (recommended for Phase 2):
   - Use `SET site:{siteId}:leader {instanceId} NX EX 30` (Redis `SET NX` with TTL).
   - The instance that wins the lock subscribes to MQTT and publishes to a Redis Pub/Sub channel `site:{siteId}:events`.
   - All BFF instances subscribe to the Redis channel and fan out to their local SSE clients.
   - On leader failure, the lock expires and another instance takes over.

2. **Sticky routing** (simpler but less resilient):
   - Route `/api/sites/:id/stream` to a consistent BFF instance via Traefik's `sticky` cookie or consistent hashing.
   - Each instance only handles its assigned sites.
   - Failure requires session migration.

3. **Accept duplicate subscriptions** (simplest, works for ≤5 sites):
   - Each BFF instance subscribes to all sites.
   - Broker receives N subscriptions per site (N = BFF instances).
   - At 10–100 msg/sec × 1 KB × 2 instances = 200 KB/s broker egress — negligible.

**Recommendation for controlai-web Phase 2**: Accept duplicate subscriptions initially (option 3). Implement Redis pub/sub coordination only if broker load becomes a concern. The t3.medium PoC ceiling (≤5 active tenants) makes this a non-issue for Phase 1.

**Source**: Redis `SET NX EX` pattern is the standard distributed lock primitive. See [redis.io/docs/latest/commands/set/](https://redis.io/docs/latest/commands/set/) — `SET key value NX EX seconds`.

---

## 5. Last-N-Messages Cache: Redis Streams vs Lists vs Sorted Sets

### Redis Streams (XADD / XREVRANGE) — **Recommended**

Redis Streams are purpose-built for append-only time-series with capped length. The `XADD MAXLEN ~ N` command appends a message and trims the stream to approximately N entries in O(1).

```typescript
// Write (on every MQTT message)
await redis.xadd(
  `stream:site:${siteId}:${topic}`,
  'MAXLEN', '~', '100',   // keep ~100 entries, approximate trim
  '*',                     // auto-generate ID (timestamp-based)
  'payload', JSON.stringify(event),
  'topic', topic,
)

// Read on widget bootstrap (last 100 messages, newest first)
const entries = await redis.xrevrange(
  `stream:site:${siteId}:${topic}`,
  '+', '-',
  'COUNT', 100,
)
// entries: [['1716500000000-0', ['payload', '...', 'topic', '...']], ...]
```

**Source**: Redis XADD docs ([redis.io/docs/latest/commands/xadd/](https://redis.io/docs/latest/commands/xadd/), fetched 2026-05-24):
> "MAXLEN: Evicts entries as long as the stream's length exceeds the specified threshold. `~`: Approximate trimming — more efficient, may leave slightly more entries than the threshold."
> "O(1) when adding a new entry, O(N) when trimming where N being the number of entries evicted."

**Why `~` (approximate) over `=` (exact)?**: Exact trimming requires a full scan of the macro-node boundary on every write. Approximate trimming is amortized O(1) and is the recommended production pattern.

### Redis Lists (LPUSH / LTRIM / LRANGE)

```typescript
// Write
await redis.lpush(`list:site:${siteId}:${topic}`, JSON.stringify(event))
await redis.ltrim(`list:site:${siteId}:${topic}`, 0, 99)  // keep last 100

// Read
const entries = await redis.lrange(`list:site:${siteId}:${topic}`, 0, 99)
```

**Downside**: Two commands per write (LPUSH + LTRIM) vs one (XADD MAXLEN). No built-in timestamp IDs. Cannot use `Last-Event-ID` for replay without storing IDs separately.

### Redis Sorted Sets (ZADD / ZREVRANGEBYSCORE)

```typescript
// Write (score = timestamp)
await redis.zadd(`zset:site:${siteId}:${topic}`, Date.now(), JSON.stringify(event))
// Trim to last 100
await redis.zremrangebyrank(`zset:site:${siteId}:${topic}`, 0, -101)

// Read since lastEventId (timestamp-based replay)
const entries = await redis.zrangebyscore(
  `zset:site:${siteId}:${topic}`,
  lastEventId, '+inf',
)
```

**Advantage over Streams**: Easier range queries by timestamp. **Disadvantage**: Two commands per write; score collisions if two messages arrive in the same millisecond.

### Comparison table

| | Redis Streams | Redis Lists | Redis Sorted Sets |
|---|---|---|---|
| Write cost | O(1) single command | O(1) + O(N) trim | O(log N) + O(N) trim |
| Read last N | XREVRANGE O(N) | LRANGE O(N) | ZREVRANGE O(N) |
| Timestamp IDs | ✅ built-in | ❌ manual | ✅ via score |
| Last-Event-ID replay | ✅ native | ❌ | ✅ via score |
| Memory per entry | ~50 bytes overhead | ~20 bytes | ~50 bytes |
| **Verdict** | **✅ Best** | ⚠️ Simple but limited | ⚠️ Good for time-range queries |

### TimescaleDB read-back vs Redis cache trade-off

| | Redis Streams cache | TimescaleDB direct |
|---|---|---|
| Bootstrap latency | ~1ms | ~50–200ms (SQL query) |
| Data freshness | Last N messages only | Full history |
| Memory cost | ~100 msgs × 1 KB × N sites = ~500 KB for 5 sites | 0 (disk) |
| Complexity | Redis dependency | Already present |
| **Recommendation** | Use for widget bootstrap (last 100 msgs) | Use for historical charts (>100 msgs) |

**Recommendation**: Use Redis Streams for the "last 100 messages" bootstrap cache. Fall back to TimescaleDB for historical queries (e.g. "last 24 hours"). This matches the sdi_oc pattern where the BFF caches recent telemetry in memory/Redis and queries TimescaleDB for historical data.

---

## 6. Auth on the SSE Channel

### Cookie-based session works natively with SSE

The browser's `EventSource` API automatically sends cookies with every SSE request (same-origin or with `withCredentials: true` for cross-origin). This is unlike WebSocket or custom `Authorization` headers, which `EventSource` does not support.

```typescript
// Browser
const es = new EventSource(`/api/sites/${siteId}/stream`, {
  withCredentials: true,  // send cookies cross-origin
})
```

```typescript
// BFF route handler — read session cookie
import { cookies } from 'next/headers'

export async function GET(request: NextRequest, ...) {
  const cookieStore = await cookies()
  const sessionToken = cookieStore.get('better-auth.session_token')?.value
  if (!sessionToken) return new Response('Unauthorized', { status: 401 })

  const session = await validateSession(sessionToken)
  if (!session) return new Response('Unauthorized', { status: 401 })

  // Check ABAC: does this user have access to this site?
  const { siteId } = await params
  if (!await userCanAccessSite(session.userId, siteId)) {
    return new Response('Forbidden', { status: 403 })
  }
  // ... proceed with SSE stream
}
```

### Gotchas

1. **Cookie `SameSite=Lax` vs `Strict`**: `SameSite=Strict` blocks cookies on cross-origin SSE requests even with `withCredentials: true`. Use `SameSite=Lax` or `SameSite=None; Secure` for cross-origin deployments.

2. **Session expiry during long-lived SSE**: The SSE connection can outlive the session cookie. The server should check session validity periodically (e.g. every 5 minutes) and close the stream with a `401` event if the session expires:
   ```typescript
   const sessionCheck = setInterval(async () => {
     const valid = await validateSession(sessionToken)
     if (!valid) {
       controller.enqueue(encoder.encode('event: auth-expired\ndata: {}\n\n'))
       controller.close()
       clearInterval(sessionCheck)
     }
   }, 5 * 60_000)
   request.signal.addEventListener('abort', () => clearInterval(sessionCheck))
   ```

3. **CSRF**: SSE is a GET request. GET requests with cookies are not CSRF-vulnerable (no state mutation). No CSRF token needed for the SSE endpoint.

4. **`Authorization` header is NOT supported by `EventSource`**: This is a known limitation. If you need token-based auth (e.g. for mobile clients), use a short-lived query-param token (`?token=...`) or switch to WebSocket. For the BFF-to-browser path, cookie sessions are the correct choice.

**Source**: EMQX blog — "Can I implement two-way authentication connections in the browser? No, it is not possible to specify a client certificate using JavaScript code when establishing a connection in a browser." ([emqx.com/en/blog/mqtt-js-tutorial](https://www.emqx.com/en/blog/mqtt-js-tutorial)) — confirms that browser-side mTLS is impossible, reinforcing the BFF-as-MQTT-subscriber pattern.

---

## 7. Production Examples: MQTT→Browser Bridges

### Example 1: opactorai/Claudable (SSE fan-out, 4k stars, MIT)

**What it does**: Real-time project event streaming from a backend to N browser clients via SSE. Uses the exact `Map<projectId, Set<ReadableStreamDefaultController>>` pattern.

**What it got right**:
- Singleton `StreamManager` stable across Next.js HMR via `globalThis`
- Dead controller cleanup after failed `enqueue()`
- `closeProjectStreams()` for graceful shutdown

**What it got wrong / missing**:
- No `Last-Event-ID` replay support
- No keepalive heartbeat
- No auth check in the SSE route

**Source**: [github.com/opactorai/Claudable/blob/main/lib/services/stream.ts](https://github.com/opactorai/Claudable/blob/main/lib/services/stream.ts)

### Example 2: OpenCTI Platform (production SSE, AGPL-3.0)

**What it does**: Streams threat intelligence events to browser clients. Uses Node.js `http.ServerResponse` (Pages Router style) but the SSE header pattern is identical.

**What it got right**:
- `X-Accel-Buffering: no` for nginx/Traefik
- `Cache-Control: no-cache, no-transform`
- Error SSE events (sends errors as SSE data instead of crashing)

**What it got wrong**:
- Uses Pages Router `res.setHeader()` instead of App Router `Response` headers
- No `Last-Event-ID` support

**Source**: [github.com/OpenCTI-Platform/opencti/blob/master/opencti-platform/opencti-graphql/src/http/httpChatbotProxy.ts#L153](https://github.com/OpenCTI-Platform/opencti/blob/master/opencti-platform/opencti-graphql/src/http/httpChatbotProxy.ts#L153)

### Example 3: EventSource/eventsource test server (reference implementation)

The official `eventsource` npm package's test server shows the canonical SSE wire format including `Last-Event-ID` replay:

```typescript
async function writeCounter(req: IncomingMessage, res: ServerResponse) {
  res.writeHead(200, {
    'Content-Type': 'text/event-stream',
    'Cache-Control': 'no-cache',
    Connection: 'keep-alive',
  })
  // Read Last-Event-ID from request header
  const lastId = req.headers['last-event-id']
  // ... replay from lastId
}
```

**Source**: [github.com/EventSource/eventsource/blob/main/test/server.ts#L113](https://github.com/EventSource/eventsource/blob/main/test/server.ts#L113)

### Example 4: EMQX Node.js integration (official, June 2025)

The EMQX blog's Express.js + MQTT middleware pattern shows the correct approach for integrating MQTT into a Node.js web framework:

```typescript
const mqttClient = mqtt.connect('mqtt://localhost:1883')
app.use(function (req, res, next) {
  req.mqttSubscribe = function (topic, callback) {
    mqttClient.subscribe(topic)
    mqttClient.on('message', function (t, m) {
      if (t === topic) { callback(m.toString()) }
    })
  }
  next()
})
```

**What it got wrong**: Registers a new `message` listener per HTTP request — this leaks listeners. The correct pattern is a single `message` listener on the MQTT client that dispatches to the `StreamManager`.

**Source**: [emqx.com/en/blog/how-to-use-mqtt-in-nodejs](https://www.emqx.com/en/blog/how-to-use-mqtt-in-nodejs) (June 2025)

---

## 8. Alternative Transports: Why SSE Wins

### Native MQTT-over-WSS in browser (mqtt.js)

```typescript
// Browser-side MQTT over WebSocket
import mqtt from 'mqtt'
const client = mqtt.connect('wss://broker.example.com:8084/mqtt')
```

**Why it loses for controlai-web**:
1. **mTLS impossible in browser**: Browsers cannot present client certificates via JavaScript. The BFF-as-MQTT-subscriber pattern is the only way to use mTLS with Mosquitto/EMQX.
2. **Bundle size**: Adding `mqtt` to the browser bundle adds ~100 KB (minified). SSE uses the native `EventSource` API (0 KB).
3. **N broker connections**: Each browser tab would open its own MQTT connection to the broker. At 10 users × 5 sites = 50 broker connections. The BFF fan-out uses 1 connection per site.
4. **Auth complexity**: MQTT username/password would need to be exposed to the browser. Cookie-based auth is cleaner.

**Source**: EMQX blog — "Can I implement two-way authentication connections in the browser? No." ([emqx.com/en/blog/mqtt-js-tutorial](https://www.emqx.com/en/blog/mqtt-js-tutorial))

### WebSocket (native or socket.io)

**Pros**: Bidirectional, lower latency than SSE for high-frequency updates.
**Cons for controlai-web**:
- `EventSource` auto-reconnects with `Last-Event-ID`; WebSocket requires manual reconnect logic.
- WebSocket does not send cookies by default in cross-origin scenarios (requires `withCredentials` on the `WebSocket` constructor, which is not universally supported).
- Next.js App Router has no built-in WebSocket support — requires a separate WebSocket server or a library like `socket.io` with its own process.
- Overkill: telemetry is server→browser only (unidirectional). SSE is designed for this.

### GraphQL Subscriptions

**Pros**: Type-safe, integrates with tRPC-like patterns.
**Cons**:
- Requires a WebSocket transport (same issues as above).
- Adds `graphql-ws` or `subscriptions-transport-ws` dependency.
- tRPC v11 has experimental subscription support but it's not production-stable for high-rate streams.

### SSE wins because

| Criterion | SSE | WebSocket | MQTT-over-WSS | GraphQL Sub |
|---|---|---|---|---|
| Browser native | ✅ `EventSource` | ✅ | ✅ (with mqtt.js) | ❌ needs lib |
| Auto-reconnect | ✅ built-in | ❌ manual | ❌ manual | ❌ manual |
| Cookie auth | ✅ automatic | ⚠️ needs `withCredentials` | ❌ | ⚠️ |
| mTLS to broker | ✅ (BFF handles) | ✅ (BFF handles) | ❌ browser can't | ✅ (BFF handles) |
| Bundle size | 0 KB | 0 KB | ~100 KB | ~50 KB |
| Next.js App Router | ✅ native | ❌ needs extra server | ❌ | ❌ |
| Unidirectional | ✅ (correct for telemetry) | ⚠️ overkill | ⚠️ overkill | ⚠️ overkill |

---

## Recommendations for controlai-web `packages/mqtt-bridge`

### Architecture

```
Browser (EventSource)
  ↕ SSE (text/event-stream, cookie auth)
Next.js App Router route: /api/sites/[siteId]/stream/route.ts
  ↕ in-process StreamManager (Map<siteId, Set<Controller>>)
mqtt-bridge singleton (one MqttClient per site)
  ↕ mqtts:// mTLS (ca + cert + key)
Mosquitto / EMQX per site
```

### Key implementation decisions

| Decision | Recommendation | Rationale |
|---|---|---|
| MQTT client | `mqtt` v5.x, one client per site | Only option for multi-broker |
| mTLS | `ca`, `cert`, `key` + `rejectUnauthorized: true` | Production security |
| Reconnect | `reconnectPeriod: 2000` + exponential backoff listener | Avoid thundering herd |
| Fan-out | `StreamManager` singleton via `globalThis` | Stable across HMR |
| SSE runtime | `export const runtime = 'nodejs'` | MQTT.js needs Node TLS |
| Disconnect detection | `request.signal.addEventListener('abort', ...)` | Web standard |
| Keepalive | `: keepalive\n\n` every 25s | Prevent proxy timeout |
| Auth | `cookies()` from `next/headers` + session validation | Native SSE cookie support |
| Cache | Redis Streams `XADD MAXLEN ~ 100` | O(1) write, native IDs |
| Bootstrap | `XREVRANGE stream:site:{id}:{topic} + - COUNT 100` | Last 100 msgs in ~1ms |
| Horizontal scale | Defer to Phase 2; accept duplicate subscriptions initially | PoC ceiling ≤5 sites |

### File layout for `packages/mqtt-bridge`

```
packages/mqtt-bridge/
├── src/
│   ├── broker-registry.ts    # Map<siteId, MqttClient>, getOrCreate, destroy
│   ├── stream-manager.ts     # Map<siteId, Set<Controller>>, publish, add, remove
│   ├── site-bridge.ts        # wires broker-registry → stream-manager + redis cache
│   ├── redis-cache.ts        # XADD / XREVRANGE helpers
│   └── index.ts              # exports activateSiteBridge, streamManager, redisCache
└── package.json
```

---

## Sources Index

| # | Source | URL | Date |
|---|---|---|---|
| 1 | MQTT.js README (official) | https://github.com/mqttjs/MQTT.js/blob/main/README.md | 2026-05-24 |
| 2 | MQTT.js secure_client.ts test | https://github.com/mqttjs/MQTT.js/blob/c4be5633594b5b839862440aa78125f22021f8e6/test/node/secure_client.ts | 2026-05-24 |
| 3 | EMQX: MQTT.js Tutorial | https://www.emqx.com/en/blog/mqtt-js-tutorial | Jun 2024 |
| 4 | EMQX: MQTT with Node.js | https://www.emqx.com/en/blog/how-to-use-mqtt-in-nodejs | Jun 2025 |
| 5 | Next.js 16 Route Handlers docs | https://nextjs.org/docs/app/api-reference/file-conventions/route | 2026-05-19 |
| 6 | opactorai/Claudable StreamManager | https://github.com/opactorai/Claudable/blob/main/lib/services/stream.ts | 2026-05-24 |
| 7 | EventSource/eventsource test server | https://github.com/EventSource/eventsource/blob/main/test/server.ts | 2026-05-24 |
| 8 | OpenCTI SSE proxy handler | https://github.com/OpenCTI-Platform/opencti/blob/master/opencti-platform/opencti-graphql/src/http/httpChatbotProxy.ts | 2026-05-24 |
| 9 | claude-code SSE writer | https://github.com/claude-code-best/claude-code/blob/main/packages/remote-control-server/src/transport/sse-writer.ts | 2026-05-24 |
| 10 | Redis XADD docs | https://redis.io/docs/latest/commands/xadd/ | 2026-05-24 |
