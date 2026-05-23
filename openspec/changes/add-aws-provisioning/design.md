# Design: AWS EC2 provisioning layer

## Context

`controlai` targets a single AWS EC2 host as its initial deployment surface
(see `openspec/project.md` lines 14–15: *Initial deployment target: single
AWS EC2 (t3.medium for PoC, ≤ ~5 active tenants)*). The existing on-host
installer (`deploy/install/install.sh`) assumes the host is already
provisioned with Docker + docker compose v2, the binary is already in
place, and a 64-hex CA key is already set. Operators today bridge that
gap manually through the EC2 console.

This change adds the missing **infrastructure-as-code + bootstrap** layer
in front of the on-host installer, so the entire path from "I have AWS
credentials" to "controlai daemon is running" becomes one command. The
design must be: (a) right-sized for the PoC ceiling (no Kubernetes-style
machinery), (b) symmetric (clean teardown), (c) reproducible (idempotent +
named deployments), and (d) easy to remove if/when we switch to K8s in a
future change.

Greenfield: no prior AWS/Terraform/CDK code exists in the repo. The full
landscape was surveyed before authoring (see
`.slash/workspace/research/iac-tool-comparison.md`,
`ec2-bootstrap-patterns.md`, `aws-oneshot-ux.md`).

## Goals / Non-Goals

**Goals**
- One command from the operator's laptop to a running controlai daemon on
  fresh EC2: `./deploy/aws/up.sh` (≤ 5 min p95).
- Symmetric `./deploy/aws/down.sh` that removes every AWS resource the
  paired `up.sh` created — no orphaned EIPs, SGs, key pairs, or SSM
  parameters.
- Idempotent re-runs: re-invoking `up.sh` against an existing deployment
  detects it and refuses to create duplicates (operator must `down` or
  pass `--replace`).
- Multiple parallel deployments per account/region differentiated by
  `DEPLOYMENT_NAME` (default `controlai-poc`), with that name prefixing
  every AWS resource.
- Operator inputs limited to: AWS credentials (already configured),
  `AWS_REGION` (fail-fast if unset), and optional overrides
  (`INSTANCE_TYPE`, `CONTROLAI_VERSION`, `DEPLOYMENT_NAME`).
- The bootstrap path produces an audit trail the operator can debug:
  cloud-init log on the box + `up.sh` echoing each stage with timestamps.

**Non-Goals**
- Kubernetes / EKS / ECS — explicitly out of scope.
- Multi-region or multi-AZ; single AZ in the default VPC.
- Auto-scaling, load balancers, RDS, ElastiCache.
- DNS automation (Route53). Operator gets the public IP; `sslip.io` is
  suggested for ad-hoc wildcard hostnames.
- TLS / Let's Encrypt automation for controlai endpoints. Existing core
  PKI handles MQTT-side TLS; HTTPS for the daemon API stays out.
- CloudWatch Logs / Metrics agent installation. `journalctl` on the
  instance is the supported log surface for the PoC.
- S3 backup sync (`controlai backup` writes to local
  `/var/backups/controlai/` only — S3 sync is a future change).
- Hardened production network posture (private subnet, NAT gateway,
  bastion, VPN). The default SG opens 22/80/443/8883 to 0.0.0.0/0.
- A second IaC backend (Pulumi/CDK/CloudFormation). One tool, period.

## Decisions

### D1 — IaC tool: OpenTofu 1.6+

We use OpenTofu (HashiCorp Terraform-compatible, MPL-licensed fork) as the
single provisioning tool, invoked from inside `up.sh`. **Why**:
declarative graph + free `destroy` symmetry covers our entire surface in
~80 lines of HCL; state lives in `deploy/aws/.state/terraform.tfstate`
(local backend) which is sufficient for PoC and easy to swap to S3 later;
operator installs one binary that has zero exotic prerequisites; we avoid
HashiCorp's BSL license. **Rejected alternatives**: plain bash + AWS CLI
(teardown ordering fragile; idempotency must be hand-coded); CloudFormation
(no local plan/preview; YAML inline-encoded user-data is awkward); CDK /
Pulumi (require Node/Go toolchain on the operator laptop and synthesize to
CFN anyway).

### D2 — Wrapper UX: `up.sh` / `down.sh` over raw `tofu`

Operators run `./deploy/aws/up.sh`, never `tofu apply` directly. The
wrapper: validates prerequisites (`tofu`, `aws`, `jq`, `envsubst`), reads
env vars, generates the CA encryption key, writes it to SSM, generates the
SSH key pair locally (if needed), renders `user-data.yaml.tmpl` via
`envsubst`, calls `tofu apply -auto-approve`, captures outputs, prints the
summary block, performs smoke tests, and writes `.deploy-state.json`.
**Why**: the operator should not need to learn Terraform — they need IP +
SSH + URL. The wrapper also lets us add bash-only logic (`aws ssm
put-parameter`, smoke tests, key-pair file permissions) that doesn't belong
in HCL.

### D3 — AMI: Canonical Ubuntu 24.04 LTS (amd64)

The Terraform module looks up the latest Canonical Noble AMI via
`data "aws_ami"` filtered on `owner = 099720109477` and the
`ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*` name glob.
**Why**: Docker CE has first-class apt repo support for Noble; vast
community surface area for troubleshooting; 2029 EOL. Rejected AL2023
(Docker install path is the RHEL repo, less documented) and Ubuntu 22.04
(Noble is the current LTS, prefer it for new deployments).

### D4 — User-data: MIME multipart (`#cloud-config` + bash)

The user-data document is a MIME multipart blob: `#cloud-config` section
handles package updates, `write_files` (writes the systemd unit
expectations + `/etc/controlai/env` skeleton), and minimal `runcmd`; a
`#!/bin/bash` section handles the imperative steps (Docker apt repo
install, binary download from GitHub Releases, `./controlai install`, SSM
fetch of the CA key, `systemctl start controlai`). Variables in the
template (`${CONTROLAI_VERSION}`, `${DEPLOYMENT_NAME}`, SSM parameter name,
region) are substituted by `envsubst` before being passed to `tofu apply`
as the `user_data` variable. **Why**: hybrid maximizes both clarity
(`#cloud-config` for declarative parts) and reliability (bash with
`set -euo pipefail` for the multi-step download path). Pure cloud-config
makes the GitHub Releases download awkward; pure bash loses
`write_files`/package locking benefits.

### D5 — Binary delivery: GitHub Releases + GoReleaser pipeline

`up.sh` instructs the EC2 instance (via user-data) to download
`controlai_<version>_linux_amd64.tar.gz` from
`github.com/<org>/controlai/releases/download/<version>/`. The `<version>`
is either `latest` (resolved via the GitHub Releases API on-box) or a
caller-provided `CONTROLAI_VERSION` env var. This change therefore also
adds `.goreleaser.yaml` + `.github/workflows/release.yml` so that
`git tag v*` produces the artifact. **Why**: GitHub Releases keep the AWS
provisioning layer free of operator-side build steps; the artifact is
public, signed, and version-pinnable. Rejected S3+IAM (extra setup
burden, the binary itself is not secret) and local-build + scp (defeats
the "one command" property — operators on Windows or with no Go toolchain
fail).

### D6 — Secrets: CA encryption key via AWS SSM Parameter Store

`up.sh` generates a fresh 64-hex key with `openssl rand -hex 32`, writes
it to AWS SSM Parameter Store as `/controlai/<deployment-name>/ca_key`
(type `SecureString`, KMS default key), and never persists it to disk on
the operator laptop. The EC2 instance profile carries
`ssm:GetParameter` on exactly that parameter ARN; user-data fetches it on
first boot and writes it to `/etc/controlai/env` (mode 0640,
`root:controlai`). **Why**: keeps the key out of cloud-init logs and out
of the operator's shell history; SSM is free at the PoC scale; teardown
deletes the parameter so a re-provisioned deployment gets a fresh key.

### D7 — Networking: default VPC + new SG, public IP, no EIP

Terraform launches the instance in the account's default VPC + default
subnet (first AZ alphabetically for determinism) with `associate_public_ip_address = true`.
A new security group is created per deployment with ingress
`22/80/443/8883` from `0.0.0.0/0` and egress `0.0.0.0/0`. No Elastic IP
is allocated by default — operators who restart the instance accept a new
public IP. **Why**: matches the PoC posture; default VPC eliminates VPC
plumbing; SG open to the world is operator-friendly for demos.
Restricting `:22` to the operator's `curl ifconfig.me` IP was considered
and rejected for v1 (added a confusing failure mode when the operator
moves networks). Elastic IP support is added later as
`ENABLE_EIP=true` toggle (HCL `count = var.enable_eip ? 1 : 0`).

### D8 — State: local `deploy/aws/.state/`, gitignored

OpenTofu state lives at `deploy/aws/.state/terraform.tfstate`. A
companion `deploy/aws/.state/<deployment-name>.json` records the
`up.sh`-rendered facts (instance ID, public IP, SSH key path, SSM
parameter name, AMI ID, AWS region, timestamps) so `down.sh` can act
without re-deriving them. Both are gitignored. **Why**: simplest backend;
operators rarely share PoC deployments. S3 remote backend is a 5-line
addition when it becomes necessary.

### D9 — SSH key: auto-generate, store under `~/.ssh/`

If `SSH_KEY_NAME` is unset, `up.sh` generates an Ed25519 key pair via
`aws ec2 create-key-pair --key-type ed25519`, writes the private material
to `~/.ssh/controlai-<deployment-name>.pem` (mode 0600), and registers
the key in AWS. If `SSH_KEY_NAME` is set, it must reference an existing
key pair already in the target region; `up.sh` does not touch it.
`down.sh` only deletes the key pair when `up.sh` originally created it
(tracked in `.deploy-state.json`).

### D10 — Idempotency strategy: state file + Terraform graph

`up.sh` reads `.deploy-state.json` first. If `instance_id` is present
and `aws ec2 describe-instances` confirms it is `running` / `pending`,
the script exits with the existing summary block — no Terraform run. To
force replacement the operator passes `--replace` (calls `tofu destroy`
then `apply`). **Why**: Terraform alone is idempotent at the resource
level, but operators who lose track of their state file expect a clear
"this deployment already exists" message rather than a silent no-op.

### D11 — Smoke test: cloud-init wait + daemon health probe

After `tofu apply`, `up.sh` runs:
1. `aws ec2 wait instance-status-ok` (≤ 10 min).
2. SSH to the box, `cloud-init status --wait` (exit 0 required).
3. SSH, `systemctl is-active controlai` (must print `active`).
4. SSH tunnel `-L 7777:/var/run/controlai.sock` + `curl --unix-socket http://localhost/v1/health` (or equivalent endpoint provided by core); must return HTTP 200.

If any step fails, `up.sh` prints `journalctl -u controlai --no-pager -n 100`
captured over SSH, then exits non-zero. The instance is **not**
auto-destroyed — operator can debug or `down.sh` manually.

### D12 — Multi-deployment naming: prefix every resource with `DEPLOYMENT_NAME`

Every AWS resource gets `Name = ${DEPLOYMENT_NAME}-<role>` and
`tags = { Project = "controlai", ManagedBy = "controlai-aws-provisioning",
Environment = ${DEPLOYMENT_NAME}, DeploymentName = ${DEPLOYMENT_NAME} }`.
The SSM parameter path embeds the deployment name. The SSH key file path
embeds it. The state file under `.state/` embeds it. This lets one
operator run `DEPLOYMENT_NAME=demo ./up.sh` and `DEPLOYMENT_NAME=ci ./up.sh`
side by side in the same account.

### D13 — `down.sh`: confirmation prompt + symmetric teardown

`down.sh` reads `.deploy-state.json`, prints the resources it is about to
delete (instance ID, SG, SSM parameter path, key pair name, EBS volume),
asks `Type the deployment name to confirm:`, then calls `tofu destroy`,
deletes the SSM parameter, deletes the local SSH key file (only if `up.sh`
created it), and removes `.deploy-state.json`. `--yes` flag bypasses the
prompt for CI. **Why**: cost-blast-radius safety net; the failure mode of
an operator running `down.sh` in the wrong terminal must be loud.

### D14 — Forward-compatibility: K8s isolation hook

The wrapper scripts and Terraform module deliberately do **not** import
anything from `internal/**`. The contract between this change and the
core is exactly the on-host artifact set produced by `controlai install`
+ a 64-hex env var. A future change introducing a Kubernetes backend can
replace `deploy/aws/` wholesale without touching this change's surface.
No "K8s-shaped abstractions" are pre-built here — that would be
speculative generality.

## Risks / Trade-offs

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| Default SG (`0.0.0.0/0` on 22) on a long-lived box gets brute-forced | Medium | High | docs/aws-deploy.md flags this; D7 toggle for IP-restricted SSH in a follow-up change; SSH key is Ed25519 by default |
| Operator loses `.state/` and reruns `up.sh` → second instance created | Low | Medium | `.deploy-state.json` is the primary guard; Terraform state is the secondary; resource Name tag includes `DEPLOYMENT_NAME` so duplicates show up in console immediately |
| GitHub Releases rate-limits public download from the EC2 box | Very low | Low | User-data uses unauthenticated GH API for `latest` (60 req/h/IP — well under budget for one boot); `CONTROLAI_VERSION` override skips the API call |
| SSM SecureString deleted out-of-band → next boot fails to fetch CA key | Low | Medium | `down.sh` is the only intentional deleter; user-data exits with a clear error referencing the parameter ARN; doc covers manual restore |
| Operator's `aws` CLI uses a different region than `AWS_REGION` env | Medium | Low | `up.sh` exports `AWS_REGION` into the AWS CLI environment for every call; fails fast on unset |
| OpenTofu version drift breaks the module | Low | Low | `terraform { required_version = ">= 1.6" }`; pinned provider versions; `up.sh` checks `tofu version` |
| Default VPC has been deleted in the account (common in enterprise tenants) | Medium | High | `up.sh` runs `aws ec2 describe-vpcs --filters Name=isDefault,Values=true`; if empty, exits with explicit "create a default VPC or set `VPC_ID`/`SUBNET_ID`" message |
| Instance type quota (`vCPUs for Running On-Demand Standard instances`) hits 0 in fresh accounts | Medium | Medium | Surface AWS API error verbatim with a hint pointing at the Service Quotas console |
| Cloud-init failure leaves a half-bootstrapped box | Low | Medium | D11 smoke test catches and reports; instance is not auto-destroyed so operator can debug |

## Migration Plan

This is a greenfield additive change. No data migration; no breaking
behavior. Sequence:

1. Land Terraform module, wrapper scripts, user-data template, docs.
2. Land `.goreleaser.yaml` + release workflow. Cut `v0.0.1` (or
   whichever the first release is) so `up.sh latest` resolves to a real
   artifact.
3. Operator-side prerequisites are documented in `docs/aws-deploy.md`.
4. README.md gets a new "Quickstart: deploy to AWS EC2" section pointing
   at the AWS doc.

Rollback: this change is a directory + a workflow file; deleting the
directory restores the previous behavior. No core code paths change, so
there is no rollback risk inside the daemon.

## Open Questions

1. Should the GoReleaser config publish `linux/arm64` too, or only `linux/amd64`?
   Today the AWS module hard-codes `t3.medium` (amd64). Adding `t4g.medium`
   support is a follow-up; for v1 amd64-only is correct. → **Decision: amd64 only
   for v1.** Documented; arm64 enabled when an `INSTANCE_FAMILY=t4g` override
   ships.
2. Do we want a `--dry-run` flag on `up.sh` that just runs `tofu plan`?
   → **Decision: yes**, cheap, useful for CI; included in tasks 4.6.
3. Where does the `<org>` GitHub path actually point to? The repo today
   has placeholder `your-org` strings. → **Decision:** parametrize via
   `GITHUB_RELEASES_REPO` env var (default `controlai-iot/controlai`,
   override per fork); the user-data template substitutes it. Documented
   in the doc.
