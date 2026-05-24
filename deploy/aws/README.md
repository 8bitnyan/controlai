# deploy/aws — controlai EC2 provisioning

Provisions a single EC2 instance running the controlai daemon with Traefik as a TLS-terminating
reverse proxy, exposing the daemon REST API at `https://api.<DEPLOYMENT_NAME>.sslip.io`.

Full walkthrough: [`docs/aws-deploy.md`](../../docs/aws-deploy.md).

---

## Quick start

```bash
# 1. Set required environment variables (copy and edit the example).
cp deploy/aws/env.sh.example env.sh
# Edit env.sh: set AWS_REGION, ACME_EMAIL, and (optionally) DEPLOYMENT_NAME.
source env.sh

# 2. Provision.
./deploy/aws/up.sh

# 3. Create a long-lived bearer token for the controlai-web BFF.
ssh ubuntu@<public_ip>
controlai token create web-bff
# → Save the printed token; set it as CONTROLAI_BEARER_TOKEN in Vercel.

# 4. Tear down when done.
DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/down.sh
```

---

## Required environment variables

| Variable | Description |
|---|---|
| `AWS_REGION` | AWS region (e.g. `us-east-1`) |
| `ACME_EMAIL` | Email for Let's Encrypt ACME account registration |

## Optional environment variables

| Variable | Default | Description |
|---|---|---|
| `DEPLOYMENT_NAME` | `controlai-poc` | Subdomain prefix: `<name>.sslip.io` |
| `INSTANCE_TYPE` | `t3.medium` | EC2 instance type |
| `CONTROLAI_VERSION` | `latest` | controlai release tag |
| `DAEMON_TCP_PORT` | `8080` | Daemon TCP listener port (Traefik proxies here) |
| `ACME_STAGING` | `1` | `1` = LE staging (default, untrusted cert); `0` = LE production |
| `DEPLOYMENT_DOMAIN` | _(empty)_ | Override domain (replaces `<ip>.sslip.io`) |
| `ENABLE_EIP` | `false` | Allocate an Elastic IP for a stable address |
| `SSH_KEY_NAME` | _(auto-created)_ | Existing EC2 key pair name |

See [`env.sh.example`](env.sh.example) for a commented template.

---

## HTTPS API endpoint

After provisioning, the daemon REST API is available at:

```
https://api.<DEPLOYMENT_NAME>.sslip.io/
```

- **TLS** is provided by Let's Encrypt via Traefik's ACME HTTP-01 challenge.
- **Authentication** requires `Authorization: Bearer <token>` on every request.
- Port **80** must be open for the ACME HTTP-01 challenge (added automatically by Terraform).
- Port **443** must be open for HTTPS traffic (added automatically by Terraform).

### Verify the endpoint

```bash
TOKEN="$(controlai token create web-bff)"     # run on the EC2 host

# With bearer token → 200
curl -H "Authorization: Bearer $TOKEN" https://api.<DEPLOYMENT_NAME>.sslip.io/v1/health

# Without bearer token → 401
curl https://api.<DEPLOYMENT_NAME>.sslip.io/v1/health
```

---

## Bearer token management

| Action | Command (on EC2 host) |
|---|---|
| Create long-lived BFF token | `controlai token create web-bff` |
| Create transient smoke-test token | `controlai token create smoke-test` |
| List tokens | `controlai token list` |
| Revoke a token | `controlai token revoke <name>` |

> **Security note:** the daemon's bearer token is the only authentication gate on the public
> API. Store the `web-bff` token securely and set it as `CONTROLAI_BEARER_TOKEN` in the
> Vercel environment for `controlai-web`.

---

## Switching from staging to production TLS certificate

By default, `ACME_STAGING=1` is set, which uses the Let's Encrypt **staging** CA (certificate
is not browser-trusted — safe for development and testing). Once you have verified that cert
acquisition works, switch to the production CA:

```bash
# On your workstation (not the EC2 host):
DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 \
  ./deploy/aws/scripts/flip-acme-prod.sh
```

This script SSH-es to the instance, updates Traefik's `caServer`, clears the staging ACME
data, and restarts Traefik so it acquires a fresh production certificate.

---

## Integration test

Run the HTTPS API integration test against a live deployment:

```bash
# Using a pre-existing token:
DEPLOYMENT_NAME=controlai-poc BEARER_TOKEN=<token> \
  ./deploy/aws/test/test_https_api.sh

# Or let the script create + revoke a transient token automatically:
DEPLOYMENT_NAME=controlai-poc ./deploy/aws/test/test_https_api.sh

# With a staging (untrusted) cert:
DEPLOYMENT_NAME=controlai-poc ./deploy/aws/test/test_https_api.sh --allow-staging
```

Or via Make:

```bash
DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 make test-aws-https
```

---

## Traefik architecture

```
Internet
  │
  ▼ :443 (TLS — Let's Encrypt cert)
Traefik (on EC2)
  │  insecureSkipVerify=true (localhost only)
  ▼ :8080 (TLS — self-signed localhost cert)
controlai daemon TCP listener
  │ (bearer token auth enforced here)
  ▼
Handler
```

Traefik config files:
- **Static config:** `deploy/aws/traefik/traefik.yml` — uploaded by `up.sh`
- **Dynamic config template:** `deploy/aws/traefik/dynamic/api.yml.tmpl` — rendered by `up.sh` and uploaded

Traefik hot-reloads the dynamic config; `up.sh --update` can push a new dynamic config without
restarting Traefik.

---

## Security group requirements

Both ports are added automatically by the Terraform module in `deploy/aws/terraform/sg.tf`:

| Port | Protocol | Purpose |
|---|---|---|
| 22 | TCP | SSH access |
| 80 | TCP | ACME HTTP-01 challenge (Let's Encrypt) |
| 443 | TCP | HTTPS API traffic |
| 8883 | TCP | MQTT broker (tenant connections) |
