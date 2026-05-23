# Research: EC2 Bootstrap Patterns for controlai (2025–2026)

Date: 2026-05-23

## Summary

This document covers canonical 2025–2026 patterns for bootstrapping a fresh EC2 instance via user-data / cloud-init so that on first boot the host has Docker Engine + docker compose v2, systemd, a Go binary from GitHub Releases or S3, and a systemd unit enabled and started. The recommended default AMI is **Ubuntu 24.04 LTS (Noble)**. The recommended user-data format is **`#cloud-config`** for the declarative setup phase, with a raw bash fallback for complex download logic.

---

## 1. AMI Choice: Amazon Linux 2023 vs Ubuntu 24.04 LTS vs Ubuntu 22.04 LTS

### Comparison Matrix

| Criterion | Amazon Linux 2023 (AL2023) | Ubuntu 24.04 LTS (Noble) | Ubuntu 22.04 LTS (Jammy) |
|-----------|---------------------------|--------------------------|--------------------------|
| **Docker CE support** | Via Docker's own RPM repo (no `amazon-linux-extras`; that was AL2 only) | Official Docker `apt` repo — first-class support | Official Docker `apt` repo — first-class support |
| **systemd** | Full systemd (AL2023 dropped SysV init) | Full systemd 255 | Full systemd 249 |
| **cloud-init version** | Customized cloud-init (AWS fork, well-tested on EC2) | cloud-init 24.x (upstream) | cloud-init 23.x (upstream) |
| **Package manager** | `dnf` (RPM) | `apt` (deb) | `apt` (deb) |
| **Docker install path** | `dnf config-manager --add-repo https://download.docker.com/linux/rhel/docker-ce.repo` then `dnf install docker-ce docker-compose-plugin` | `apt` repo at `download.docker.com/linux/ubuntu` | Same as 24.04 |
| **Kernel** | 6.1 LTS | 6.8 HWE | 5.15 LTS |
| **EOL** | 2028 | 2029 (standard) / 2034 (ESM) | 2027 (standard) / 2032 (ESM) |
| **AWS-native tooling** | SSM Agent pre-installed, IMDSv2 enforced by default | SSM Agent available, IMDSv2 configurable | SSM Agent available |
| **Docker community docs/examples** | Fewer Ubuntu-specific examples apply; RPM path differs | Vast majority of Docker tutorials target Ubuntu | Same as 24.04 |
| **TimescaleDB image compatibility** | No known issues | No known issues | No known issues |
| **Gotchas** | `amazon-linux-extras` does NOT exist in AL2023 (AL2 only); Docker CE repo uses RHEL path | `ufw` + Docker iptables conflict (disable ufw or use `DOCKER-USER` chain) | Same ufw caveat |

### Recommendation: **Ubuntu 24.04 LTS (Noble)**

**Rationale:**
- Docker's official `apt` repository has first-class Ubuntu Noble support (verified at [docs.docker.com/engine/install/ubuntu](https://docs.docker.com/engine/install/ubuntu/)).
- The `docker-compose-plugin` package (providing `docker compose v2`) installs cleanly alongside `docker-ce`.
- Largest community surface area for troubleshooting Docker + systemd issues.
- 2029 standard EOL is sufficient for a PoC that may grow into production.
- Ubuntu 22.04 is still valid but Noble is the current LTS; prefer it for new deployments.
- AL2023 is a good choice if you need AWS-native tooling (SSM, Nitro) as the primary concern, but the Docker install path is less documented and uses the RHEL repo.

**AWS AMI lookup (2026):**
```bash
# Get latest Ubuntu 24.04 LTS AMI for your region
aws ec2 describe-images \
  --owners 099720109477 \
  --filters "Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*" \
            "Name=state,Values=available" \
  --query 'sort_by(Images, &CreationDate)[-1].ImageId' \
  --output text
```

---

## 2. Official Docker Install Path on Ubuntu 24.04 LTS

**Source:** [docs.docker.com/engine/install/ubuntu/](https://docs.docker.com/engine/install/ubuntu/) (verified May 2026)

The official path is the **Docker `apt` repository** — not the distro's `docker.io` package (which lags behind) and not a convenience script (not recommended for production).

### Step-by-step (suitable for embedding in user-data)

```bash
# 1. Remove any conflicting distro packages
apt-get remove -y docker.io docker-compose docker-compose-v2 docker-doc podman-docker containerd runc 2>/dev/null || true

# 2. Install prerequisites
apt-get update -y
apt-get install -y ca-certificates curl

# 3. Add Docker's official GPG key
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc

# 4. Add Docker apt repository (DEB822 format, current as of 2026)
tee /etc/apt/sources.list.d/docker.sources <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
EOF

# 5. Install Docker CE + compose plugin
apt-get update -y
apt-get install -y \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin

# 6. Enable and start Docker
systemctl enable --now docker

# 7. Verify
docker --version
docker compose version
```

**Key packages:**
- `docker-ce` — Docker Engine daemon
- `docker-compose-plugin` — provides `docker compose` (v2) as a CLI plugin; invoked as `docker compose` (space, not hyphen)
- `docker-buildx-plugin` — BuildKit frontend (needed for multi-platform builds)

**Do NOT use:**
- `docker.io` — Ubuntu's own package, typically 1–2 major versions behind
- `docker-compose` (standalone binary) — v1, deprecated and removed from Docker Hub
- `amazon-linux-extras install docker` — AL2 only, does not exist on AL2023

---

## 3. `#cloud-config` vs Raw Bash User-Data

### Summary

| Aspect | `#cloud-config` | Raw `#!/bin/bash` |
|--------|-----------------|-------------------|
| **Declarative** | Yes — YAML modules for packages, files, users, systemd | No — imperative |
| **Error handling** | Module-level; failures logged to `/var/log/cloud-init-output.log`; partial success possible | Script exits on first `set -e` failure; full control |
| **Ordering** | Fixed module order (network → packages → write_files → runcmd) | Fully explicit |
| **Idempotency** | Modules are generally idempotent (e.g., `packages:` won't reinstall) | Must implement yourself |
| **Debugging** | `cloud-init analyze show`, `cloud-init status --long` | `journalctl -u cloud-init` |
| **16 KB limit** | Applies to both | Applies to both |
| **Systemd unit creation** | `write_files:` + `runcmd: [systemctl enable ...]` | `cat > /etc/systemd/system/...` + `systemctl enable` |
| **Binary download** | `runcmd:` (awkward for multi-step logic) | Natural bash flow |

### Recommendation: **Hybrid — `#cloud-config` for structure, `runcmd` for imperative steps**

For the controlai use case (Docker install + binary download + systemd unit), the best pattern is:

1. Use `#cloud-config` for:
   - `package_update: true` / `package_upgrade: true`
   - `packages:` list (ca-certificates, curl, etc.)
   - `write_files:` to drop the systemd unit file
   - `runcmd:` for the Docker repo setup, Docker install, binary download, and `systemctl enable`

2. Alternatively, use a **MIME multipart** document to combine `#cloud-config` (for packages/files) with a `#!/bin/bash` script (for complex download logic). This is the AWS-documented pattern for combining both.

**Why not pure bash?**
- `write_files:` in cloud-config is cleaner for embedding multi-line unit files (avoids heredoc quoting issues).
- `packages:` handles apt locking and retries automatically.
- cloud-init logs are structured and queryable.

**Why not pure cloud-config?**
- `runcmd:` runs commands sequentially but has no native retry logic.
- Downloading a binary from GitHub Releases (with version pinning, checksum verification) is cleaner in bash.

### MIME Multipart Pattern (AWS-documented)

```
Content-Type: multipart/mixed; boundary="//"
MIME-Version: 1.0

--//
Content-Type: text/cloud-config; charset="us-ascii"
MIME-Version: 1.0
Content-Transfer-Encoding: 7bit
Content-Disposition: attachment; filename="cloud-config.txt"

#cloud-config
package_update: true
packages:
  - ca-certificates
  - curl
  - unzip

write_files:
  - path: /etc/systemd/system/controlai.service
    permissions: '0644'
    owner: root:root
    content: |
      [Unit]
      Description=controlai daemon
      After=network-online.target docker.service cloud-init.target
      Wants=network-online.target
      Requires=docker.service

      [Service]
      Type=simple
      EnvironmentFile=/etc/controlai/env
      ExecStart=/usr/local/bin/controlai daemon start
      Restart=on-failure
      RestartSec=5s
      StandardOutput=journal
      StandardError=journal
      SyslogIdentifier=controlai

      [Install]
      WantedBy=multi-user.target

--//
Content-Type: text/x-shellscript; charset="us-ascii"
MIME-Version: 1.0
Content-Transfer-Encoding: 7bit
Content-Disposition: attachment; filename="setup.sh"

#!/bin/bash
set -euo pipefail
# ... Docker install + binary download steps here
--//--
```

---

## 4. Waiting for cloud-init Completion and Surfacing Failures

### `cloud-init status --wait`

**Source:** [docs.cloud-init.io/en/latest/howto/wait_for_cloud_init.html](https://docs.cloud-init.io/en/latest/howto/wait_for_cloud_init.html)

```bash
# Block until cloud-init finishes (exit code 0 = success, 1 = error)
cloud-init status --wait

# Check final status
cloud-init status --long
```

**Exit codes:**
- `0` — done, no errors
- `1` — done, with errors
- `2` — running (only if `--wait` is not used)

### Surfacing Failures to the Operator

#### Option A: CloudWatch Logs Agent (recommended for production)

Install the CloudWatch agent in user-data and stream `/var/log/cloud-init-output.log` and `journald` to CloudWatch:

```bash
# Install CloudWatch agent (Ubuntu)
curl -fsSL https://s3.amazonaws.com/amazoncloudwatch-agent/ubuntu/amd64/latest/amazon-cloudwatch-agent.deb \
  -o /tmp/cwa.deb
dpkg -i /tmp/cwa.deb

# Minimal config to stream cloud-init log
cat > /opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json <<'EOF'
{
  "logs": {
    "logs_collected": {
      "files": {
        "collect_list": [
          {
            "file_path": "/var/log/cloud-init-output.log",
            "log_group_name": "/ec2/controlai/cloud-init",
            "log_stream_name": "{instance_id}"
          },
          {
            "file_path": "/var/log/syslog",
            "log_group_name": "/ec2/controlai/syslog",
            "log_stream_name": "{instance_id}"
          }
        ]
      }
    }
  }
}
EOF

systemctl enable --now amazon-cloudwatch-agent
```

**IAM requirement:** The instance profile must have `logs:CreateLogGroup`, `logs:CreateLogStream`, `logs:PutLogEvents`.

#### Option B: journalctl (SSH-based debugging)

```bash
# After SSH into the instance:
journalctl -u cloud-init --no-pager
journalctl -u cloud-init-local --no-pager
cat /var/log/cloud-init-output.log

# Check if cloud-init finished successfully
cloud-init status --long
```

#### Option C: EC2 Instance Connect + CloudWatch (PoC pattern)

For a PoC, the simplest approach is:
1. Check `/var/log/cloud-init-output.log` via SSH after launch.
2. Use `cloud-init status --wait` in a post-launch health check script.
3. Use AWS EC2 console → "Get system log" to see early boot output.

#### Systemd Unit Dependency Pattern

Per [docs.cloud-init.io](https://docs.cloud-init.io/en/latest/howto/wait_for_cloud_init.html), services that must start after cloud-init completes should declare:

```ini
[Unit]
After=cloud-init.target multi-user.target
```

This ensures the service only starts after cloud-init's `final` stage completes.

---

## 5. Shipping a Single Binary onto the Box

### Option A: curl from GitHub Releases (simplest for PoC)

```bash
# Pattern: download latest release binary
BINARY_VERSION="v1.2.3"  # or use GitHub API to get latest
BINARY_URL="https://github.com/your-org/controlai/releases/download/${BINARY_VERSION}/controlai_linux_amd64.tar.gz"

curl -fsSL "${BINARY_URL}" -o /tmp/controlai.tar.gz
tar -xzf /tmp/controlai.tar.gz -C /tmp/
install -m 0755 /tmp/controlai /usr/local/bin/controlai
rm -f /tmp/controlai.tar.gz /tmp/controlai

# Verify
controlai version
```

**To get the latest release tag dynamically:**
```bash
LATEST=$(curl -fsSL https://api.github.com/repos/your-org/controlai/releases/latest \
  | grep '"tag_name"' | cut -d'"' -f4)
```

**Pros:** No AWS dependencies, works from any network.  
**Cons:** Requires public GitHub repo or a GitHub token for private repos; rate-limited without auth.

**For private repos**, pass a GitHub token via SSM Parameter Store:
```bash
TOKEN=$(aws ssm get-parameter --name /controlai/github-token --with-decryption --query Parameter.Value --output text)
curl -fsSL -H "Authorization: token ${TOKEN}" \
  "https://api.github.com/repos/your-org/controlai/releases/assets/${ASSET_ID}" \
  -H "Accept: application/octet-stream" \
  -o /usr/local/bin/controlai
```

### Option B: S3 + IAM Instance Profile (recommended for production)

**Setup:**
1. Upload binary to S3: `aws s3 cp ./controlai s3://your-bucket/releases/controlai_v1.2.3_linux_amd64`
2. Attach an IAM instance profile with `s3:GetObject` on the bucket.

**In user-data:**
```bash
# Instance profile provides credentials automatically — no keys needed
aws s3 cp s3://your-bucket/releases/controlai_v1.2.3_linux_amd64 /usr/local/bin/controlai
chmod 0755 /usr/local/bin/controlai
```

**Pros:** No public internet dependency; IAM-controlled access; fast (S3 is in-region); works with private binaries.  
**Cons:** Requires S3 bucket + IAM role setup; version must be baked into user-data or fetched from SSM.

**Version pinning via SSM:**
```bash
VERSION=$(aws ssm get-parameter --name /controlai/current-version --query Parameter.Value --output text)
aws s3 cp "s3://your-bucket/releases/controlai_${VERSION}_linux_amd64" /usr/local/bin/controlai
chmod 0755 /usr/local/bin/controlai
```

### Option C: scp After Instance is Up (manual / CI pattern)

```bash
# Wait for instance to be ready
aws ec2 wait instance-status-ok --instance-ids i-xxxx

# Get public IP
PUBLIC_IP=$(aws ec2 describe-instances --instance-ids i-xxxx \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)

# Copy binary
scp -i ~/.ssh/controlai.pem ./controlai ubuntu@${PUBLIC_IP}:/tmp/
ssh -i ~/.ssh/controlai.pem ubuntu@${PUBLIC_IP} \
  'sudo install -m 0755 /tmp/controlai /usr/local/bin/controlai'
```

**Pros:** Simple for one-off deployments; no S3 or GitHub dependency.  
**Cons:** Requires SSH access; not suitable for auto-scaling or fully automated bootstrap; race condition between instance ready and cloud-init completion.

### Recommendation for controlai PoC

Use **Option B (S3 + IAM instance profile)** for the binary. It's the most reliable, doesn't depend on GitHub rate limits or public internet for the binary, and the IAM pattern is already needed for CloudWatch logs. Use **Option A (GitHub Releases)** only if you want zero AWS infrastructure for the binary itself.

---

## 6. SSH Key Handling

### `aws ec2 create-key-pair` (create new)

```bash
# Create a new key pair and save the private key
aws ec2 create-key-pair \
  --key-name controlai-poc \
  --key-type ed25519 \
  --query 'KeyMaterial' \
  --output text > ~/.ssh/controlai-poc.pem
chmod 600 ~/.ssh/controlai-poc.pem
```

**Pros:** Simple; key is managed by AWS (public key stored in EC2).  
**Cons:** Private key is only returned once at creation; if lost, must create a new key pair.

### Reuse Existing Key Pair

```bash
# Import an existing public key
aws ec2 import-key-pair \
  --key-name controlai-poc \
  --public-key-material fileb://~/.ssh/id_ed25519.pub
```

**Pros:** Use your existing SSH key; no new private key to manage.  
**Cons:** Must have the public key available.

### Recommendation

For a PoC, **reuse an existing key pair** via `import-key-pair` if you already have an SSH key. This avoids managing a new private key file. For production or team environments, use AWS Systems Manager Session Manager (SSM) instead of SSH — it requires no key pair and no port 22 open.

**SSM Session Manager pattern (no SSH key needed):**
```bash
# Connect without SSH
aws ssm start-session --target i-xxxx
```
Requires: SSM Agent (pre-installed on Ubuntu 24.04 via snap or apt), IAM role with `ssm:StartSession`.

---

## 7. Security Group Ports for controlai

Based on `openspec/project.md`: Traefik is the only host-bound entrypoint on `:80`, `:443`, `:8883`. SSH on `:22` for operator access.

```bash
# Create security group
SG_ID=$(aws ec2 create-security-group \
  --group-name controlai-poc-sg \
  --description "controlai PoC security group" \
  --vpc-id vpc-xxxx \
  --query GroupId --output text)

# SSH (restrict to your IP in production)
aws ec2 authorize-security-group-ingress \
  --group-id $SG_ID \
  --protocol tcp --port 22 --cidr 0.0.0.0/0

# HTTP (Traefik, for ACME challenges and HTTP→HTTPS redirect)
aws ec2 authorize-security-group-ingress \
  --group-id $SG_ID \
  --protocol tcp --port 80 --cidr 0.0.0.0/0

# HTTPS (Traefik, for tenant HTTP APIs)
aws ec2 authorize-security-group-ingress \
  --group-id $SG_ID \
  --protocol tcp --port 443 --cidr 0.0.0.0/0

# MQTTS (Traefik SNI routing for MQTT brokers)
aws ec2 authorize-security-group-ingress \
  --group-id $SG_ID \
  --protocol tcp --port 8883 --cidr 0.0.0.0/0
```

**Notes:**
- Port 22 should be restricted to operator IPs in production (`--cidr YOUR_IP/32`).
- No other ports need to be open: tenant broker/ingest containers are Traefik upstreams only (no host port exposure per project spec).
- The controlai daemon API is on a Unix socket (`/var/run/controlai.sock`) — no host port needed.
- If using SSM Session Manager instead of SSH, port 22 can be removed entirely.

---

## 8. EBS Sizing for ~5 Tenants of TimescaleDB (7d Retention, Low Throughput)

**Recommendation:** A **30 GiB gp3 root volume** is sufficient for a PoC with 5 tenants at 7-day retention and low throughput (≤100 msg/s per site); TimescaleDB compression (auto-enabled by controlai for 7d retention) typically achieves 10–20× compression on time-series IoT data, keeping per-tenant storage well under 2 GiB for this workload.

---

## 9. Elastic IP — Recommend Yes/No for PoC

**Recommendation: Yes, allocate an Elastic IP for the PoC.**

**Rationale:**
- The PoC requires wildcard DNS (`*.iot.example.com → EC2 IP`) for Traefik SNI routing. If the instance is stopped/started, the public IP changes, breaking DNS.
- An Elastic IP costs nothing while associated with a running instance; only charged when unassociated.
- Eliminates the need to update DNS records after every stop/start cycle during development.

```bash
# Allocate EIP
ALLOC_ID=$(aws ec2 allocate-address --domain vpc --query AllocationId --output text)

# Associate with instance
aws ec2 associate-address \
  --instance-id i-xxxx \
  --allocation-id $ALLOC_ID
```

---

## Appendix: Complete `#cloud-config` Skeleton for controlai Bootstrap

This is a reference skeleton — not the final script — showing how the patterns above compose together.

```yaml
#cloud-config
# Ubuntu 24.04 LTS (Noble) — controlai bootstrap skeleton

package_update: true
package_upgrade: true

packages:
  - ca-certificates
  - curl
  - unzip
  - awscli          # for S3 binary download (or install via snap)

# Drop the systemd unit before runcmd runs
write_files:
  - path: /etc/systemd/system/controlai.service
    permissions: '0644'
    owner: root:root
    content: |
      [Unit]
      Description=controlai IoT control plane daemon
      Documentation=https://github.com/your-org/controlai
      After=network-online.target docker.service cloud-init.target
      Wants=network-online.target
      Requires=docker.service

      [Service]
      Type=simple
      User=root
      EnvironmentFile=-/etc/controlai/env
      ExecStart=/usr/local/bin/controlai daemon start
      Restart=on-failure
      RestartSec=5s
      StandardOutput=journal
      StandardError=journal
      SyslogIdentifier=controlai

      [Install]
      WantedBy=multi-user.target

  - path: /etc/controlai/env
    permissions: '0600'
    owner: root:root
    content: |
      # Populated by bootstrap; override as needed
      CONTROLAI_CA_KEY_ENCRYPTION_KEY=REPLACE_ME

runcmd:
  # --- Docker CE install (official apt repo) ---
  - apt-get remove -y docker.io docker-compose docker-compose-v2 podman-docker containerd runc || true
  - install -m 0755 -d /etc/apt/keyrings
  - curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  - chmod a+r /etc/apt/keyrings/docker.asc
  - |
    tee /etc/apt/sources.list.d/docker.sources <<'EOF'
    Types: deb
    URIs: https://download.docker.com/linux/ubuntu
    Suites: noble
    Components: stable
    Architectures: amd64
    Signed-By: /etc/apt/keyrings/docker.asc
    EOF
  - apt-get update -y
  - apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  - systemctl enable --now docker

  # --- Download controlai binary from S3 (IAM instance profile provides creds) ---
  - aws s3 cp s3://YOUR_BUCKET/releases/controlai_REPLACE_VERSION_linux_amd64 /usr/local/bin/controlai
  - chmod 0755 /usr/local/bin/controlai

  # --- Create required directories ---
  - mkdir -p /var/lib/controlai /var/backups/controlai /etc/controlai

  # --- Enable and start controlai ---
  - systemctl daemon-reload
  - systemctl enable controlai
  - systemctl start controlai

  # --- Verify ---
  - docker --version
  - docker compose version
  - controlai version

# Optional: output all runcmd to cloud-init log
output:
  all: '| tee -a /var/log/cloud-init-output.log'
```

---

## Key References

- Docker CE install on Ubuntu: https://docs.docker.com/engine/install/ubuntu/
- Docker CE install on RHEL (AL2023 path): https://docs.docker.com/engine/install/rhel/
- cloud-init examples: https://docs.cloud-init.io/en/latest/reference/examples.html
- cloud-init wait: https://docs.cloud-init.io/en/latest/howto/wait_for_cloud_init.html
- AL2023 cloud-init: https://docs.aws.amazon.com/linux/al2023/ug/cloud-init.html
- AWS EC2 user-data: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/user-data.html
- cloud-init systemd integration: `After=cloud-init.target multi-user.target` in unit `[Unit]` section
