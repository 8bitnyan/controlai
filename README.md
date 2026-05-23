# controlai

Multi-tenant IoT data-pipeline control plane for a single EC2 host.

## Overview

controlai provisions and operates per-tenant IoT data pipelines
(gateway → MQTT broker → ingest → TimescaleDB) through declarative YAML
configuration. It targets a t3.medium PoC ceiling of ~5 active tenants.

## Architecture

```
[Gateway] --mTLS--> [:8883 Traefik SNI] --> [broker container] --> [ingest container] --> [TimescaleDB]
                                                                                             |
                                                                          [controlai daemon] -+
```

- **Per-tenant**: one TimescaleDB container, isolated network
- **Per-site**: one broker (mosquitto or EMQX) + one or more ingest containers
- **Shared**: one Traefik v3 on `:80/:443/:8883`
- **controlai daemon**: REST API on unix socket + optional TCP+TLS; reconciler loop

## Quick Start

```bash
# Install
sudo make build
sudo ./controlai install

# Start daemon (dev mode, no CA key required)
CONTROLAI_CA_KEY_ENCRYPTION_KEY=$(openssl rand -hex 32) controlai daemon start

# Create a tenant
controlai tenant create acme-corp --domain acme.example.com --retention 7d

# Add a site
controlai site create tnt_acme-corp seoul \
  --broker mosquitto --throughput low --direction uni --codec cbor

# Check capacity
controlai capacity

# Apply desired state
controlai site apply tnt_acme-corp ste_seoul
```

## Commands

| Command | Description |
|---------|-------------|
| `controlai daemon start` | Start the control plane daemon |
| `controlai tenant create <slug>` | Create a tenant |
| `controlai tenant list` | List tenants |
| `controlai tenant rm <id> [--purge]` | Remove a tenant |
| `controlai site create <tenant> <slug>` | Add a site |
| `controlai site list <tenant>` | List sites |
| `controlai site stop <tenant> <site>` | Stop a site |
| `controlai site start <tenant> <site>` | Start a site |
| `controlai site apply <tenant> <site>` | Apply and wait for convergence |
| `controlai capacity [--plan ...]` | Check capacity / what-if |
| `controlai migrate [--dry-run]` | Migrate YAML configs |
| `controlai pki ca create --site <t/s>` | Create site CA |
| `controlai pki cert issue --site <t/s> --gateway <name>` | Issue gateway cert |
| `controlai token create <name>` | Create bearer token |
| `controlai token revoke <id>` | Revoke bearer token |
| `controlai version` | Print version |

## Broker Capability Matrix

| Combination | Supported |
|-------------|-----------|
| mosquitto + low + uni | ✅ |
| mosquitto + low + bi | ✅ |
| mosquitto + mid | ❌ (no MQTT5 shared subscriptions) |
| emqx + low + uni/bi | ✅ |
| emqx + mid + uni/bi | ✅ (2 ingest replicas, shared subscription) |
| any + high | ❌ (requires t3.large+) |

## Build

```bash
make build        # controlai binary
make build-ingest # controlai-ingest binary (for site containers)
make test         # unit tests
make lint         # golangci-lint
make integration  # integration tests (requires docker)
```

## Data Layout

```
/var/lib/controlai/
  controlai.db                # SQLite registry
  shared/
    docker-compose.yml        # Traefik service
    traefik/static.yml
    traefik/dynamic/          # per-site route files (atomically written)
  tenants/
    <tenant_id>/
      tenant.yaml
      tsdb/docker-compose.yml
      tsdb/init.sql
      sites/
        <site_id>/
          site.yaml
          docker-compose.yml
          deploy/             # broker configs, certs
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `CONTROLAI_CA_KEY_ENCRYPTION_KEY` | Yes (production) | 32-byte hex master key for CA key wrapping |

## License

Apache 2.0
