# Research: Traefik v3 MQTT/S SNI Routing on TCP :8883
Date: 2026-05-22

## Summary

Traefik v3 supports multi-tenant MQTT(S) routing on a single port (8883) via TCP routers with `HostSNI` rules. The broker terminates TLS (passthrough mode) or Traefik terminates it (Traefik-TLS mode). For the controlai daemon pattern of writing YAML files to `traefik/dynamic/`, the **file provider with `watch: true` and `directory:`** is the correct fit. mTLS client-cert auth works cleanly only in passthrough mode; Traefik-TLS mode breaks it unless Traefik is configured to forward the client cert (which it cannot do for TCP).

---

## 1. Traefik v3 TCP Entrypoint for :8883

**Static config** (`traefik.yml` or CLI flags — never hot-reloaded):

```yaml
# traefik/traefik.yml  (static config)
entryPoints:
  mqtts:
    address: ":8883"          # TCP, no /udp suffix → TCP by default

providers:
  file:
    directory: "/etc/traefik/dynamic"
    watch: true               # inotify-based hot reload

api:
  dashboard: true
  insecure: true              # dev only; bind to 127.0.0.1 in prod
```

Key points:
- No `http:` block under the entrypoint — this is a raw TCP entrypoint.
- `watch: true` is the default for the file provider but explicit is safer.
- Static config changes (entrypoint address, provider list) **require a Traefik restart**. Dynamic config (routers, services) hot-reloads without restart.

**Source**: [Traefik v3 EntryPoints reference](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/) · [File provider reference](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/)

---

## 2. Per-Tenant TCP Router Keyed on HostSNI

Each tenant/site gets one file in `traefik/dynamic/`. The daemon writes (or overwrites) these files; Traefik picks up changes within ~2 s (default `providersThrottleDuration: 2s`).

### Passthrough mode (broker holds the cert)

```yaml
# traefik/dynamic/tenant-x-site-a.yml
tcp:
  routers:
    mqtt-tenantX-siteA:
      entryPoints:
        - "mqtts"
      rule: "HostSNI(`siteA.tenantX.controlai.local`)"
      service: "broker-tenantX-siteA"
      tls:
        passthrough: true     # Traefik peeks at SNI, then forwards raw TLS bytes

  services:
    broker-tenantX-siteA:
      loadBalancer:
        servers:
          - address: "emqx-tenantX-siteA:8883"   # docker service name:port
        healthCheck:
          interval: "10s"
          timeout: "3s"
          # no send/expect → TCP connect-only probe
```

### Traefik-TLS termination mode (Traefik holds the cert)

```yaml
# traefik/dynamic/tenant-x-site-a.yml
tcp:
  routers:
    mqtt-tenantX-siteA:
      entryPoints:
        - "mqtts"
      rule: "HostSNI(`siteA.tenantX.controlai.local`)"
      service: "broker-tenantX-siteA"
      tls:
        passthrough: false
        # certResolver: "myresolver"   # or supply certs via tls.certificates below

  services:
    broker-tenantX-siteA:
      loadBalancer:
        servers:
          - address: "emqx-tenantX-siteA:1883"   # cleartext to broker

tls:
  certificates:
    - certFile: "/certs/siteA.tenantX.crt"
      keyFile:  "/certs/siteA.tenantX.key"
```

**Critical rule**: `HostSNI` only works on TLS connections (the SNI extension is part of the TLS ClientHello). For non-TLS TCP catch-all, use `HostSNI(`*`)`.

**Priority**: Default priority = rule string length. All `HostSNI(`siteA.tenantX.controlai.local`)` rules are the same length if the FQDN is the same length — add explicit `priority:` if two rules could collide (they won't if FQDNs differ).

**Source**: [TCP Router reference](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/routing/router/) · [TCP Rules & Priority](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/routing/rules-and-priority/) · [TCP TLS reference](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/tls/)

---

## 3. Dynamic Config Sourcing Options

| Provider | Hot-reload | Daemon writes files? | Notes |
|---|---|---|---|
| **File (directory + watch)** | ✅ ~2 s via inotify | ✅ daemon writes YAML | **Best fit** for controlai pattern |
| Docker labels | ✅ on container events | ❌ labels on containers | Requires daemon to manage container lifecycle |
| HTTP provider | ✅ poll interval | ❌ daemon must serve HTTP | Extra complexity; good for distributed setups |

### Recommendation: File provider with `directory:`

```yaml
# static config
providers:
  file:
    directory: "/etc/traefik/dynamic"   # mount traefik/dynamic/ here
    watch: true
```

- Daemon writes `traefik/dynamic/<tenant>-<site>.yml` atomically (write to `.tmp`, then `mv`).
- Traefik uses [fsnotify](https://github.com/fsnotify/fsnotify) — **atomic rename triggers the event correctly**; in-place overwrite may miss events on some kernels.
- `providersThrottleDuration: 2s` (default) debounces rapid writes; tune down to `500ms` if faster convergence is needed.
- **Docker volume mount caveat**: if the host file is renamed (not overwritten), the bind-mount link breaks and fsnotify stops working. Mount the **parent directory**, not individual files.

```yaml
# docker-compose.yml (Traefik service)
volumes:
  - ./traefik/dynamic:/etc/traefik/dynamic:ro   # mount directory, not files
  - ./traefik/traefik.yml:/etc/traefik/traefik.yml:ro
```

**Source**: [File provider docs — Limitations section](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/)

---

## 4. TLS Termination Strategies

### Strategy A: TLS Passthrough (broker terminates)

```
Client ──MQTTS──► Traefik :8883 ──(raw TLS bytes)──► Broker :8883
                  [peeks SNI only]                    [terminates TLS]
```

| | |
|---|---|
| **Pros** | Broker sees real client cert (mTLS works end-to-end). No cert management in Traefik. Traefik is transparent — broker controls cipher suites, TLS version, CA chain. |
| **Cons** | Traefik cannot inspect or modify the payload. No Traefik-level rate limiting or middleware on the TLS layer. Broker must have its own cert per SNI name (or a wildcard). |

### Strategy B: Traefik TLS Termination + cleartext to broker

```
Client ──MQTTS──► Traefik :8883 ──(cleartext MQTT)──► Broker :1883
                  [terminates TLS]
```

| | |
|---|---|
| **Pros** | Traefik manages certs centrally. Broker config is simpler (no TLS). Can apply TCP middlewares (IPAllowList, InFlightConn). |
| **Cons** | **mTLS breaks**: Traefik terminates the client cert but cannot forward it to the broker over a raw TCP connection. The broker never sees the client cert. Traefik's `PassTLSClientCert` middleware is HTTP-only (adds an HTTP header) — not available for TCP routers. |

### Verdict for mTLS use case

**Use passthrough**. If you need mTLS client cert auth at the broker, passthrough is the only viable option. Traefik-TLS termination is only appropriate when the broker does not need to authenticate clients by certificate.

---

## 5. Broker Configs for SNI Passthrough

### EMQX 5.x (minimal SSL listener)

EMQX must listen on 8883 with its own cert. Each tenant instance gets its own container with its own cert matching the SNI name.

```hocon
# emqx.conf  (HOCON format, EMQX 5.x)
listeners.ssl.default {
  bind = "0.0.0.0:8883"
  ssl_options {
    certfile  = "/certs/siteA.tenantX.crt"
    keyfile   = "/certs/siteA.tenantX.key"
    cacertfile = "/certs/ca.crt"          # for mTLS
    verify    = verify_peer               # require client cert
    fail_if_no_peer_cert = true
  }
}
```

In docker-compose, the container's internal port is 8883; Traefik routes to `emqx-tenantX-siteA:8883` (no host port mapping needed — all on the same Docker network).

```yaml
# docker-compose.yml (broker fragment)
  emqx-tenantX-siteA:
    image: emqx/emqx:5.8
    environment:
      - EMQX_NODE__NAME=emqx@emqx-tenantX-siteA
    volumes:
      - ./certs/siteA.tenantX:/certs:ro
      - ./emqx/emqx.conf:/opt/emqx/etc/emqx.conf:ro
    networks:
      - traefik-net
    # NO ports: mapping — Traefik routes internally
```

### Mosquitto 2.x (minimal TLS config)

```conf
# mosquitto/siteA-tenantX/mosquitto.conf
listener 8883
protocol mqtt

cafile   /certs/ca.crt
certfile /certs/siteA.tenantX.crt
keyfile  /certs/siteA.tenantX.key
require_certificate true    # mTLS: require client cert
use_identity_as_username true
```

```yaml
# docker-compose.yml (broker fragment)
  mosquitto-tenantX-siteA:
    image: eclipse-mosquitto:2.0
    volumes:
      - ./mosquitto/siteA-tenantX/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro
      - ./certs/siteA.tenantX:/certs:ro
    networks:
      - traefik-net
```

---

## 6. mTLS with SNI Passthrough

### What works

- Client presents a cert during TLS handshake → broker validates against its CA.
- Broker enforces `require_certificate true` (mosquitto) or `verify = verify_peer` (EMQX).
- Traefik is transparent — it only reads the SNI from the ClientHello (first ~100 bytes) and then forwards the raw TCP stream unchanged.
- The full TLS handshake (including client cert exchange) happens between client and broker.

### What breaks / gotchas

| Issue | Detail |
|---|---|
| **Traefik-TLS + mTLS** | Impossible for TCP. `PassTLSClientCert` is HTTP-only. Broker never sees client cert. |
| **SNI absent** | If the MQTT client does not set SNI (old clients, some embedded SDKs), `HostSNI` matching fails. Traefik falls through to a catch-all `HostSNI(`*`)` router if one exists, or drops the connection. Always test with `openssl s_client -servername <fqdn> -connect host:8883`. |
| **Wildcard certs** | A wildcard cert `*.tenantX.controlai.local` on the broker works fine with passthrough — the broker presents it and the client validates it. Traefik only needs to match the SNI string, not validate the cert. |
| **Cert rotation** | In passthrough mode, cert rotation is entirely the broker's concern. EMQX 5.x supports hot cert reload via its API. Mosquitto requires a `SIGHUP`. |
| **Client cert revocation** | CRL/OCSP checking is the broker's responsibility in passthrough mode. |

---

## 7. Limits and Known Issues

### Traefik TCP/SNI routing maturity

- TCP routing with `HostSNI` has been stable since Traefik v2.0 (2019). v3 added wildcard SNI (`*.example.com`) support in the v3 rule syntax (default in v3).
- No known MQTT-specific bugs. Traefik treats MQTT as opaque TCP bytes after SNI peek.
- **ALPN**: MQTT over TLS does not use ALPN by default. If your clients set ALPN to `mqtt`, you can add `&& ALPN(`mqtt`)` to the rule for extra specificity, but it is not required.

### Hot-reload behavior

- Dynamic config (routers, services) reloads without dropping existing connections.
- **Existing connections are not re-routed** on reload — they stay on the old backend until they disconnect.
- New connections after reload use the new config immediately.
- Static config (entrypoints, provider list) requires a Traefik restart; existing connections are dropped during restart.
- `providersThrottleDuration` (default 2 s) means rapid file writes are debounced — the last write within the window wins.

### Connection limits

- No per-router connection limit in Traefik TCP by default.
- Use the `InFlightConn` TCP middleware to cap concurrent connections per router:

```yaml
# traefik/dynamic/middlewares.yml
tcp:
  middlewares:
    limit-per-tenant:
      inFlightConn:
        amount: 1000
```

Then reference in the router: `middlewares: ["limit-per-tenant@file"]`.

- Traefik itself has no hard cap on total TCP connections; it is bounded by OS file descriptor limits (`ulimit -n`). For high-connection MQTT workloads, set `LimitNOFILE=1048576` in the systemd unit or `ulimits` in docker-compose.

### File provider fsnotify limitations

- On Docker Desktop (macOS/Windows), inotify events from bind mounts may be delayed or missed. Use polling as a fallback by setting `TRAEFIK_PROVIDERS_FILE_WATCH=true` and ensuring the directory (not file) is mounted.
- NFS/CIFS mounts do not support inotify — avoid for the dynamic config directory.

---

## 8. Health Check / Readiness Probing

Traefik v3 TCP services support a native health check that removes unhealthy backends from rotation.

### TCP connect-only probe (minimal)

```yaml
tcp:
  services:
    broker-tenantX-siteA:
      loadBalancer:
        servers:
          - address: "emqx-tenantX-siteA:8883"
        healthCheck:
          interval: "10s"
          timeout:  "3s"
          # no send/expect → TCP connect probe only
```

Traefik opens a TCP connection to the backend. If it connects, the server is healthy. This works for MQTTS because a successful TCP connect (even before TLS handshake) indicates the broker is up.

### TCP payload probe (MQTT CONNECT ping)

For a deeper probe, send a minimal MQTT CONNECT packet and expect a CONNACK. However, this is complex to encode as raw bytes and is rarely worth it for a health check. The TCP connect probe is sufficient for most cases.

```yaml
healthCheck:
  send:   "\x10\x0d\x00\x04MQTT\x04\x00\x00\x3c\x00\x00"  # MQTT CONNECT (v3.1.1, no auth)
  expect: "\x20\x02\x00\x00"                                 # CONNACK success
  interval: "15s"
  timeout:  "5s"
```

> **Warning**: The raw MQTT CONNECT bytes above are for cleartext MQTT (port 1883). For MQTTS (8883), the TLS handshake happens first — Traefik's TCP health check does not do TLS, so it will fail against a TLS-only listener. Use the TCP connect-only probe (no `send`/`expect`) for MQTTS backends.

### Sidecar / external probe pattern

For MQTTS health checks with actual TLS validation, run a sidecar health-check container (e.g., a small script using `mosquitto_pub --cafile ... --cert ... --key ...`) and use Docker healthcheck to mark the container unhealthy. Traefik's Docker provider will then remove it from rotation automatically.

**Source**: [TCP Service health check reference](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/service/#health-check)

---

## 9. Complete docker-compose Example (2 tenants, 2 sites)

```yaml
# docker-compose.yml
version: "3.9"

networks:
  traefik-net:
    driver: bridge

services:
  traefik:
    image: traefik:v3.3
    ports:
      - "8883:8883"           # MQTTS — single external port
      - "8080:8080"           # dashboard (dev only)
    volumes:
      - ./traefik/traefik.yml:/etc/traefik/traefik.yml:ro
      - ./traefik/dynamic:/etc/traefik/dynamic:ro   # daemon writes here
      - /var/run/docker.sock:/var/run/docker.sock:ro  # optional: docker provider
    networks:
      - traefik-net
    ulimits:
      nofile:
        soft: 1048576
        hard: 1048576

  # Tenant X, Site A — EMQX
  emqx-tenantX-siteA:
    image: emqx/emqx:5.8
    environment:
      EMQX_NODE__NAME: "emqx@emqx-tenantX-siteA"
    volumes:
      - ./certs/siteA.tenantX:/certs:ro
      - ./emqx/emqx.conf:/opt/emqx/etc/emqx.conf:ro
    networks:
      - traefik-net
    healthcheck:
      test: ["CMD", "emqx", "ping"]
      interval: 10s
      timeout: 5s
      retries: 3

  # Tenant Y, Site B — Mosquitto
  mosquitto-tenantY-siteB:
    image: eclipse-mosquitto:2.0
    volumes:
      - ./mosquitto/siteB-tenantY/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro
      - ./certs/siteB.tenantY:/certs:ro
    networks:
      - traefik-net
    healthcheck:
      test: ["CMD-SHELL", "mosquitto_pub -h localhost -p 8883 --cafile /certs/ca.crt --cert /certs/client.crt --key /certs/client.key -t health -m ping -q 0 || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 3
```

```yaml
# traefik/dynamic/tenantX-siteA.yml  (written by controlai daemon)
tcp:
  routers:
    mqtt-tenantX-siteA:
      entryPoints: ["mqtts"]
      rule: "HostSNI(`siteA.tenantX.controlai.local`)"
      service: "broker-tenantX-siteA"
      tls:
        passthrough: true

  services:
    broker-tenantX-siteA:
      loadBalancer:
        servers:
          - address: "emqx-tenantX-siteA:8883"
        healthCheck:
          interval: "10s"
          timeout: "3s"
```

```yaml
# traefik/dynamic/tenantY-siteB.yml  (written by controlai daemon)
tcp:
  routers:
    mqtt-tenantY-siteB:
      entryPoints: ["mqtts"]
      rule: "HostSNI(`siteB.tenantY.controlai.local`)"
      service: "broker-tenantY-siteB"
      tls:
        passthrough: true

  services:
    broker-tenantY-siteB:
      loadBalancer:
        servers:
          - address: "mosquitto-tenantY-siteB:8883"
        healthCheck:
          interval: "10s"
          timeout: "3s"
```

---

## Citations

### Official Traefik v3 Docs
- [TCP Router reference](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/routing/router/)
- [TCP Rules & Priority (HostSNI, ALPN, ClientIP)](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/routing/rules-and-priority/)
- [TCP TLS configuration (passthrough, certResolver)](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/tls/)
- [TCP Service & health check](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/service/)
- [File provider (directory, watch, limitations)](https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/)
- [EntryPoints reference](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/)
- [TCP InFlightConn middleware](https://doc.traefik.io/traefik/reference/routing-configuration/tcp/middlewares/inflightconn/)

### Real-World Example Repos
1. **traefik/traefik** — integration tests for TCP SNI routing:
   `https://github.com/traefik/traefik/tree/master/integration/testdata/rawdata-tcp`
   Contains golden-file fixtures for TCP router + HostSNI + passthrough configurations used in CI.

2. **emqx/emqx-docker** — official EMQX Docker examples including SSL listener config:
   `https://github.com/emqx/emqx-docker`
   Shows `EMQX_LISTENERS__SSL__DEFAULT__*` env var overrides for containerized deployments.

3. **eclipse/mosquitto** — official Mosquitto Docker image with TLS config examples:
   `https://github.com/eclipse/mosquitto/tree/master/docker`
   The `mosquitto.conf` examples in `docker/` show `listener`, `cafile`, `certfile`, `keyfile`, `require_certificate` for mTLS.

---

## Quick Decision Matrix

| Need | Choice |
|---|---|
| mTLS client cert auth at broker | Passthrough (`tls.passthrough: true`) |
| Traefik manages certs, broker is simple | Traefik-TLS (`tls.passthrough: false`) + cleartext broker |
| Daemon writes routing config | File provider, `directory:`, `watch: true` |
| Cap connections per tenant | `InFlightConn` TCP middleware |
| Health check for MQTTS backend | TCP connect-only probe (no `send`/`expect`) |
| New tenant provisioning | Daemon writes new YAML file → Traefik picks up in ~2 s |
| Remove tenant | Daemon deletes YAML file → router removed, existing connections drain |
