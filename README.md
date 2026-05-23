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

## Operator Runbook

### Install

```bash
# 1. Build or download the binary
make build

# 2. Run the one-shot installer (as root)
sudo ./controlai install

# 3. Set the CA master key in /etc/controlai/env
sudo bash -c 'echo "CONTROLAI_CA_KEY_ENCRYPTION_KEY=$(openssl rand -hex 32)" >> /etc/controlai/env'

# 4. Start the daemon
sudo systemctl start controlai
sudo systemctl status controlai

# 5. Initialize the shared Traefik infrastructure
controlai shared init --domain iot.example.com

# View daemon logs
journalctl -u controlai -f
```

### First Tenant

```bash
# Create a tenant (capacity check runs automatically)
controlai tenant create acme-corp --domain acme.iot.example.com --retention 7d

# Verify TSDB is running
docker ps | grep tnt_acme-corp
```

### First Site

```bash
# Add a site under the tenant
controlai site create tnt_acme-corp seoul \
  --broker mosquitto --throughput low --direction uni --codec cbor

# Apply desired state and wait for convergence (up to 120 s)
controlai site apply tnt_acme-corp ste_seoul

# Check site status
controlai site list tnt_acme-corp
```

### Retention Change

Retention is set at tenant creation time. To change retention for an existing
tenant (deferred full reconciler support; manual procedure):

```bash
# 1. Edit the tenant YAML directly
sudo nano /var/lib/controlai/tenants/tnt_acme-corp/tenant.yaml
# Change the `retention` field (1m|1h|1d|7d|30d)

# 2. Re-apply the tenant TSDB project
controlai apply tnt_acme-corp-tsdb

# 3. Connect to TSDB and update the retention policy manually
docker exec -it tnt_acme-corp-tsdb-1 psql -U postgres controlai \
  -c "SELECT remove_retention_policy('telemetry');" \
  -c "SELECT add_retention_policy('telemetry', INTERVAL '30 days');"
```

### Broker Swap (mosquitto → EMQX)

```bash
# 1. Stop the site
controlai site stop tnt_acme-corp ste_seoul

# 2. Edit site.yaml to change broker.kind
sudo nano /var/lib/controlai/tenants/tnt_acme-corp/sites/ste_seoul/site.yaml
# Set broker.kind: emqx, throughput: low (or mid), and re-check the codec.

# 3. Validate
controlai migrate --dry-run

# 4. Apply and wait
controlai site apply tnt_acme-corp ste_seoul
```

### Capacity Check

```bash
# Show current allocation and headroom
controlai capacity

# What-if: would adding a new emqx-mid-uni site fit?
# (capacity endpoint supports ?plan= query string)
curl --unix-socket /var/run/controlai.sock \
  'http://controlai/v1/capacity?plan=emqx:mid:uni'
```

### Backup and Restore

```bash
# Run an immediate backup
controlai backup run tnt_acme-corp
# → /var/backups/controlai/tnt_acme-corp/YYYYMMDD.sql.gz

# List backups
controlai backup ls tnt_acme-corp

# Restore (manual)
docker exec -i tnt_acme-corp-tsdb-1 psql -U postgres controlai \
  < <(gunzip -c /var/backups/controlai/tnt_acme-corp/20260101.sql.gz)
```

### Uninstall

```bash
# 1. Stop all containers managed by controlai
controlai tenant list | awk '{print $1}' | while read id; do
  controlai tenant rm "$id" --purge
done

# 2. Stop and disable the daemon
sudo systemctl stop controlai
sudo systemctl disable controlai

# 3. Remove systemd units
sudo rm /etc/systemd/system/controlai*.service /etc/systemd/system/controlai*.timer 2>/dev/null || true
sudo systemctl daemon-reload

# 4. Remove data (WARNING: irreversible)
sudo rm -rf /var/lib/controlai /var/backups/controlai /etc/controlai

# 5. Remove binary
sudo rm /usr/local/bin/controlai
```

## Commands

| Command | Description |
|---------|-------------|
| `controlai daemon start` | Start the control plane daemon |
| `controlai install` | Install systemd unit, user, and directories |
| `controlai tenant create <slug>` | Create a tenant |
| `controlai tenant list` | List tenants |
| `controlai tenant rm <id> [--purge]` | Remove a tenant |
| `controlai site create <tenant> <slug>` | Add a site |
| `controlai site list <tenant>` | List sites |
| `controlai site stop <tenant> <site>` | Stop a site |
| `controlai site start <tenant> <site>` | Start a site |
| `controlai site apply <tenant> <site>` | Apply and wait for convergence |
| `controlai capacity` | Check capacity / what-if |
| `controlai backup run <tenant>` | Run an immediate pg_dump backup |
| `controlai backup ls <tenant>` | List existing backup archives |
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
