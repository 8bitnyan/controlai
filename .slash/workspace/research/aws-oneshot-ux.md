# Research: One-Command EC2 Deploy UX — Design North Star
Date: 2026-05-23

## Summary

Surveyed 9 open-source self-hosted projects (Coolify, Dokku, CapRover, Plausible CE, Supabase self-hosted, Umami, PocketBase, k3s, Tailscale on EC2, n8n) for their "one-command deploy to a single host" UX. The dominant pattern is `curl | bash` for on-host bootstrapping, but **none of them provision the EC2 instance itself** — that gap is exactly where `deploy/aws/` can differentiate. The best-in-class UX combines a single local command that provisions infra + bootstraps the service, then prints a clean summary block.

---

## Findings

### 1. The One Command

| Project | Command | Mechanism |
|---|---|---|
| **Coolify** | `curl -fsSL https://cdn.coollabs.io/coolify/install.sh \| sudo bash` | curl-pipe-bash; installs Docker + Compose stack |
| **Dokku** | `wget … bootstrap.sh && sudo DOKKU_TAG=v0.38.5 bash bootstrap.sh` | wget + explicit version var |
| **CapRover** | `docker run -p 80:80 -p 443:443 … caprover/caprover` | single `docker run`; then `caprover serversetup` CLI |
| **Plausible CE** | `git clone … && echo BASE_URL=… >> .env && docker compose up -d` | 4-step manual; no single command |
| **Supabase self-hosted** | `git clone … && sh utils/generate-keys.sh && docker compose up -d` | 5-step manual; key generation script |
| **Umami** | `docker-compose up -d` | assumes Docker already present |
| **k3s** | `curl -sfL https://get.k3s.io \| sh -` | curl-pipe-sh; gold standard for simplicity |
| **Tailscale on EC2** | 10-step manual (Console + SSH) | no automation; manual VPC wizard |
| **n8n** | `docker compose up -d` | assumes server + Docker present |

**Pattern**: The cleanest UX is k3s-style `curl | sh` for on-host setup. **No project automates the EC2 provisioning step itself from the operator's laptop.** That is the gap.

The closest analogue for full-stack provisioning is Coolify's env-var-prefixed install:
```bash
env ROOT_USERNAME=admin ROOT_USER_PASSWORD=secret \
  bash -c 'curl -fsSL https://cdn.coollabs.io/coolify/install.sh | bash'
```

### 2. AWS Credentials

All surveyed projects assume the server already exists. For the few that touch AWS (Tailscale, Supabase recommending SES), the pattern is:
- **Env vars** (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_DEFAULT_REGION`) — most common for scripted deploys
- **`~/.aws/credentials`** — assumed present when using AWS CLI interactively
- **IAM Instance Profile** — recommended for production (Tailscale docs explicitly say "use IAM permissions, not root user")
- **IAM Identity Center / SSO** — not mentioned by any project; too complex for self-hosted audience

**Best practice observed**: Check `AWS_PROFILE` → fall back to `~/.aws/credentials` → fall back to instance metadata. Never prompt for raw keys.

### 3. Region, Instance Size, SSH Key, Domain, TLS

| Concern | Common Pattern |
|---|---|
| **Region** | Env var `AWS_DEFAULT_REGION` or `AWS_REGION`; default to `us-east-1` if unset |
| **Instance size** | Hardcoded default (Tailscale: `t2.micro`; Supabase: 4 GB RAM / 2 vCPU minimum); override via env var |
| **SSH key** | Assumed pre-existing key pair name passed as env var or arg; Tailscale docs say "make note of key pair name" |
| **Domain** | Optional; if omitted, use raw IP or `sslip.io` trick (Dokku: `dokku domains:set-global 10.0.0.2.sslip.io`) |
| **TLS** | Auto Let's Encrypt when domain + ports 80/443 are set (Plausible CE, Coolify via Traefik, CapRover); skipped for IP-only |

**sslip.io trick** (Dokku, CapRover): append `.sslip.io` to the public IP to get a wildcard-resolvable hostname with no DNS setup — enables HTTPS without a real domain.

### 4. Teardown

| Project | Teardown |
|---|---|
| Coolify | Documented uninstall page; no single command |
| Supabase | `docker compose down -v && rm -rf volumes/` |
| Plausible CE | `docker compose down -v` |
| k3s | `k3s-uninstall.sh` (installed automatically by bootstrap) |
| CapRover | No documented teardown |
| Tailscale | Manual: terminate EC2 instance + delete VPC in Console |

**Pattern**: Docker-based projects use `docker compose down -v`. k3s installs a companion uninstall script. **No project automates cloud resource teardown** (security groups, EIP, key pairs). This is a footgun.

### 5. Operator Output at the End

Best-in-class projects print a summary block. Examples:

**Coolify** (after install):
```
Coolify is ready!
  Dashboard: http://<IP>:8000
  Default password: (set during install)
```

**k3s** (after install):
```
[INFO] systemd: Starting k3s
Node token: /var/lib/rancher/k3s/server/node-token
```

**CapRover** (after `caprover serversetup`):
```
CapRover is available at: https://captain.something.mydomain.com
```

**Tailscale** (after `tailscale ip -4`): just the IP — no summary block.

**Pattern**: Print IP, URL, copy-paste SSH command, and any generated secrets. Projects that omit this force the operator to dig through logs.

### 6. Common Pitfalls / Footguns

1. **`curl | bash` trust**: Operator pipes an unreviewed script as root. Mitigation: pin to a versioned URL or SHA; show the script URL prominently.
2. **Hardcoded `us-east-1`**: AMI IDs are region-specific; scripts that hardcode an AMI ID silently fail in other regions.
3. **SSH key not pre-existing**: Scripts that assume `~/.ssh/id_rsa` exists break on fresh CI machines. Must either generate a key or accept a key path.
4. **Port 22 left open**: Tailscale docs explicitly add a step to close SSH after Tailscale is up. Most projects skip this.
5. **No teardown = orphaned resources**: EIPs, security groups, and key pairs accumulate cost and attack surface. Projects that don't provide `./down.sh` leave operators stranded.
6. **Secrets in shell history**: `env SECRET=foo bash -c '...'` leaks to `~/.bash_history`. Better: read from a `.env` file or prompt with `read -s`.
7. **Domain required for TLS, but not validated early**: Plausible CE and Supabase will start without TLS if `BASE_URL` is wrong; operator discovers this only after 10 minutes of pulling images.
8. **`docker compose` vs `docker-compose`**: Supabase docs note CRLF line-ending issues on Windows clones; scripts must normalize.
9. **Instance profile vs static keys**: Scripts that require `AWS_ACCESS_KEY_ID` break in environments using IAM roles (CI, EC2 itself).
10. **No idempotency**: Re-running the deploy script creates a second instance instead of updating the first.

---

## Design North Star for `deploy/aws/`

**The one command** (from operator's laptop, not the server):
```bash
./deploy/aws/up.sh
```
Or with overrides:
```bash
AWS_REGION=eu-west-1 INSTANCE_TYPE=t3.medium DOMAIN=controlai.example.com ./deploy/aws/up.sh
```

**Credential resolution order** (never prompt for raw keys):
1. `AWS_PROFILE` env var → named profile in `~/.aws/credentials`
2. `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` env vars
3. EC2 instance metadata (if running inside AWS)
4. Fail fast with a clear error message pointing to `aws configure`

**Inputs with sane defaults**:
| Input | Default | Override |
|---|---|---|
| Region | `us-east-1` | `AWS_REGION` |
| Instance type | `t3.small` | `INSTANCE_TYPE` |
| SSH key | auto-generate + save to `~/.ssh/controlai_deploy.pem` | `SSH_KEY_NAME` (existing key pair) |
| Domain | none (use `<IP>.sslip.io`) | `DOMAIN` |
| TLS | auto via sslip.io or Let's Encrypt if domain set | implicit |

**End-of-run summary block** (non-negotiable):
```
============================================================
  controlai deployed successfully
  Instance:  i-0abc123def456  (t3.small, us-east-1a)
  Public IP: 54.12.34.56
  URL:       https://54.12.34.56.sslip.io
  SSH:       ssh -i ~/.ssh/controlai_deploy.pem ubuntu@54.12.34.56
  Teardown:  ./deploy/aws/down.sh
============================================================
```

**Teardown** (`./deploy/aws/down.sh`): terminate instance, release EIP, delete security group, optionally delete key pair. Prompt for confirmation. Print cost estimate saved.

**Idempotency**: store instance ID in `.deploy-state.json`; re-running `up.sh` detects existing instance and skips creation (or offers `--replace`).

**Security defaults**: security group allows 443 inbound only; SSH (22) open only during bootstrap, then closed. Recommend Tailscale or SSM Session Manager for ongoing access.
