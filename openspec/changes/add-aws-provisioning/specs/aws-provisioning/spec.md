# Spec Delta: aws-provisioning (NEW capability)

## ADDED Requirements

### Requirement: One-command AWS provisioning entry point

controlai SHALL provide an operator-facing shell script `deploy/aws/up.sh`
that, when invoked from the repository root with valid AWS credentials and
an `AWS_REGION` environment variable, provisions a single EC2 host and
bootstraps the controlai daemon to a running, healthy state. The script
SHALL be the sole supported entry point for the AWS path; operators SHALL
NOT be expected to invoke `tofu`, `aws ec2 run-instances`, or
`controlai install` directly.

#### Scenario: Fresh deployment from a laptop

- **WHEN** an operator with configured AWS credentials runs
  `AWS_REGION=us-east-1 ./deploy/aws/up.sh` in a clean checkout
- **THEN** the script SHALL provision one EC2 instance, one security
  group, one EBS gp3 volume (≥ 50 GB), one IAM role + instance profile,
  one SSM SecureString parameter, and one EC2 key pair within ≤ 10 min
- **AND** the script SHALL print a summary block containing the public
  IP, an `ssh` command the operator can paste, a `https://<IP>.sslip.io`
  URL, and the `./deploy/aws/down.sh` teardown command
- **AND** the controlai daemon SHALL be reported `active (running)` by
  `systemctl is-active controlai` on the box at script exit

#### Scenario: Missing AWS_REGION

- **WHEN** the operator runs `./deploy/aws/up.sh` without setting
  `AWS_REGION`
- **THEN** the script SHALL exit non-zero before contacting AWS
- **AND** the script SHALL print a remediation message naming the
  missing variable and the format expected

### Requirement: Symmetric teardown removes every provisioned resource

controlai SHALL provide an operator-facing shell script
`deploy/aws/down.sh` that removes every AWS resource the paired `up.sh`
created for the same `DEPLOYMENT_NAME`, with no residue. The script
SHALL require operator confirmation by default and SHALL accept `--yes`
to bypass the prompt for CI use.

#### Scenario: Confirmed teardown

- **WHEN** the operator runs `./deploy/aws/down.sh` after a successful
  `up.sh`, types the deployment name at the prompt, and presses Enter
- **THEN** the script SHALL terminate the EC2 instance, delete the
  security group, delete the EBS volume, delete the SSM parameter,
  delete the IAM role and instance profile, and (if `up.sh` created
  the key pair) delete the EC2 key pair plus the local private-key file
- **AND** the local files `deploy/aws/.state/${DEPLOYMENT_NAME}.json`
  and `${DEPLOYMENT_NAME}.user-data` SHALL be removed
- **AND** the AWS console SHALL show no resources tagged
  `DeploymentName=${DEPLOYMENT_NAME}` in the target region

#### Scenario: Confirmation mismatch aborts

- **WHEN** the operator types anything other than the exact deployment
  name at the confirmation prompt
- **THEN** the script SHALL exit non-zero
- **AND** the script SHALL touch no AWS resources

#### Scenario: No state file present

- **WHEN** `down.sh` is invoked but no
  `deploy/aws/.state/${DEPLOYMENT_NAME}.json` exists
- **THEN** the script SHALL exit non-zero with a message explaining
  that there is no known deployment to tear down

### Requirement: Idempotent re-runs of `up.sh`

`up.sh` SHALL detect an already-provisioned deployment with the same
`DEPLOYMENT_NAME` and SHALL refuse to create duplicates. Forcing a
replacement SHALL require an explicit `--replace` flag.

#### Scenario: Re-run on an existing deployment

- **WHEN** the operator re-invokes `up.sh` with the same
  `DEPLOYMENT_NAME` while the recorded instance is `running` or
  `pending`
- **THEN** the script SHALL exit 0 without invoking `tofu apply`
- **AND** the script SHALL re-print the existing summary block

#### Scenario: Force replace

- **WHEN** the operator invokes `up.sh --replace` against an existing
  deployment
- **THEN** the script SHALL invoke `down.sh --yes` semantics internally
  and then provision a fresh deployment

### Requirement: Multiple parallel deployments per account and region

controlai SHALL support running multiple AWS deployments in the same
AWS account and region concurrently, differentiated by `DEPLOYMENT_NAME`.
Every AWS resource SHALL be named with the deployment name as a prefix,
and every resource SHALL carry the tag `DeploymentName=${DEPLOYMENT_NAME}`.

#### Scenario: Two deployments side by side

- **WHEN** the operator runs `DEPLOYMENT_NAME=demo ./up.sh` and then
  `DEPLOYMENT_NAME=ci ./up.sh` in the same account and region
- **THEN** both deployments SHALL coexist with no resource-name
  collisions
- **AND** `down.sh` against either deployment SHALL leave the other
  untouched

### Requirement: Provisioning uses OpenTofu (Terraform-compatible)

controlai SHALL implement the AWS resource graph via OpenTofu ≥ 1.6
under `deploy/aws/terraform/`. The Terraform configuration SHALL pin
provider versions, SHALL pass `terraform fmt -check -recursive` and
`terraform validate`, and SHALL be invoked exclusively through the
wrapper scripts (operators SHALL NOT need to learn Terraform).

#### Scenario: OpenTofu absent

- **WHEN** the operator runs `up.sh` and `tofu` is not on PATH
- **THEN** the script SHALL exit non-zero before contacting AWS
- **AND** the script SHALL print a remediation hint pointing at the
  OpenTofu installation page

#### Scenario: Configuration formatting

- **WHEN** CI runs `terraform fmt -check -recursive deploy/aws/terraform/`
- **THEN** the command SHALL exit 0

### Requirement: Default networking — default VPC, new SG, public IP, no EIP

`up.sh` SHALL place the EC2 instance in the account's default VPC and
the first (alphabetically) default subnet. A fresh security group SHALL
be created per deployment with ingress on TCP 22, 80, 443, and 8883
from `0.0.0.0/0`. No Elastic IP SHALL be allocated unless
`ENABLE_EIP=true` is set.

#### Scenario: No default VPC

- **WHEN** the target region has no default VPC
- **THEN** `up.sh` SHALL exit non-zero with a message explaining how
  to either create a default VPC or supply `VPC_ID` / `SUBNET_ID`
  overrides

#### Scenario: Elastic IP opt-in

- **WHEN** the operator sets `ENABLE_EIP=true` and runs `up.sh`
- **THEN** an `aws_eip` SHALL be allocated and associated with the
  instance
- **AND** the summary block SHALL print the allocated EIP as the public
  IP

### Requirement: CA encryption key stored in SSM Parameter Store

The required 64-hex `CONTROLAI_CA_KEY_ENCRYPTION_KEY` SHALL be generated
locally by `up.sh` and SHALL be persisted to AWS SSM Parameter Store as
a `SecureString` at path `/controlai/${DEPLOYMENT_NAME}/ca_key`. The
EC2 instance profile SHALL grant `ssm:GetParameter` on exactly that
parameter ARN — no broader IAM scope. The user-data bootstrap SHALL
fetch the key from SSM on first boot and SHALL write it to
`/etc/controlai/env` with mode 0640 and ownership `root:controlai`.

#### Scenario: Key generation and persistence

- **WHEN** `up.sh` generates the CA key
- **THEN** the key SHALL be generated via `openssl rand -hex 32`
- **AND** the key SHALL be written to SSM with type `SecureString`
- **AND** the key SHALL NOT be written to any file on the operator's
  laptop
- **AND** the key SHALL NOT appear in cloud-init logs on the EC2 host

#### Scenario: IAM scope check

- **WHEN** the EC2 instance attempts `ssm:GetParameter` against any
  parameter other than `/controlai/${DEPLOYMENT_NAME}/ca_key`
- **THEN** AWS IAM SHALL deny the request

### Requirement: Binary delivery via GitHub Releases

The user-data bootstrap SHALL fetch the `controlai` binary from the
GitHub Releases of the repository identified by `GITHUB_RELEASES_REPO`
(default `controlai-iot/controlai`). The version SHALL be either
`latest` (resolved at boot via the GitHub Releases REST API) or pinned
via `CONTROLAI_VERSION`. The release artifact SHALL be named
`controlai_<version>_linux_amd64.tar.gz` and SHALL contain the
binary plus `install.sh` + systemd units + `LICENSE`. The repository
SHALL publish these artifacts via a GoReleaser workflow triggered on
tags matching `v*`.

#### Scenario: Latest version on fresh boot

- **WHEN** `up.sh` is invoked with `CONTROLAI_VERSION` unset
- **THEN** the user-data script on the host SHALL resolve `latest`
  via `GET https://api.github.com/repos/${GITHUB_RELEASES_REPO}/releases/latest`
- **AND** the host SHALL download and install that release's
  `linux_amd64` archive

#### Scenario: Pinned version

- **WHEN** `up.sh` is invoked with `CONTROLAI_VERSION=v1.4.2`
- **THEN** the user-data script SHALL fetch exactly
  `controlai_1.4.2_linux_amd64.tar.gz` from
  `github.com/${GITHUB_RELEASES_REPO}/releases/download/v1.4.2/`
- **AND** `controlai version` on the box SHALL print `1.4.2`

#### Scenario: Release workflow on tag push

- **WHEN** a tag matching `v*` is pushed to the repository
- **THEN** `.github/workflows/release.yml` SHALL run GoReleaser
  against the tag
- **AND** the resulting `controlai_<version>_linux_amd64.tar.gz`
  SHALL be attached to the corresponding GitHub Release

### Requirement: Bootstrap base image is Ubuntu 24.04 LTS

The Terraform module SHALL look up the latest Canonical Ubuntu 24.04
LTS (Noble) amd64 AMI in the target region at apply time via an
`aws_ami` data source filtered on owner `099720109477` and the name
glob `ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*`.

#### Scenario: AMI resolution

- **WHEN** `tofu plan` executes
- **THEN** the resolved AMI ID SHALL belong to Canonical (owner
  `099720109477`) and the AMI name SHALL match the Noble 24.04 amd64
  pattern

### Requirement: SSH key pair auto-generated when absent

When `SSH_KEY_NAME` is unset, `up.sh` SHALL generate a fresh Ed25519
EC2 key pair named `${DEPLOYMENT_NAME}-key`, SHALL write the private
material to `~/.ssh/controlai-${DEPLOYMENT_NAME}.pem` with mode 0600,
and SHALL record `ssh_key_created_by_up=true` in the state file.
When `SSH_KEY_NAME` is set, `up.sh` SHALL verify the named key pair
exists in the target region and SHALL NOT modify it.

#### Scenario: Auto-generation

- **WHEN** `up.sh` runs without `SSH_KEY_NAME` set
- **THEN** `~/.ssh/controlai-${DEPLOYMENT_NAME}.pem` SHALL be created
  with mode 0600
- **AND** the AWS EC2 key pair `${DEPLOYMENT_NAME}-key` SHALL be
  created with key type `ed25519`

#### Scenario: Bring-your-own key

- **WHEN** `up.sh` runs with `SSH_KEY_NAME=existing-key`
- **THEN** no new key pair SHALL be created
- **AND** the EC2 instance SHALL be launched with
  `key_name = "existing-key"`
- **AND** `down.sh` against this deployment SHALL NOT delete that key
  pair

### Requirement: Local state tracking under `deploy/aws/.state/`

`up.sh` SHALL persist deployment facts to
`deploy/aws/.state/${DEPLOYMENT_NAME}.json` after a successful run.
OpenTofu state SHALL live in the same `.state/` directory. The
directory SHALL be gitignored. The state file SHALL include at minimum:
deployment name, AWS region, instance ID, public IP, AMI ID, SSM
parameter ARN, SSH key name, SSH key local path, `ssh_key_created_by_up`
boolean, controlai version installed, and an ISO-8601 created-at
timestamp.

#### Scenario: State file after fresh apply

- **WHEN** `up.sh` completes a fresh deployment
- **THEN** `deploy/aws/.state/${DEPLOYMENT_NAME}.json` SHALL exist
- **AND** the file SHALL contain all minimum required fields named
  above

### Requirement: Resource tagging convention

Every AWS resource created by this change SHALL carry the tags
`Project=controlai`, `ManagedBy=controlai-aws-provisioning`,
`Environment=${DEPLOYMENT_NAME}`, and `DeploymentName=${DEPLOYMENT_NAME}`.
Additional tags MAY be merged via a `TAGS` env var or `extra_tags`
Terraform variable.

#### Scenario: Default tags applied

- **WHEN** any taggable AWS resource is created by the Terraform
  module
- **THEN** the resource SHALL carry the four default tags above
- **AND** the values SHALL be derived from the active `DEPLOYMENT_NAME`

### Requirement: Smoke test before reporting success

`up.sh` SHALL NOT report success until all four checks pass: (a) EC2
`instance-status-ok`, (b) `cloud-init status --wait` exits 0 on the
host, (c) `systemctl is-active controlai` on the host prints `active`,
and (d) the controlai daemon's unix-socket API responds to a health
request through an SSH local-forward tunnel. On any failure, `up.sh`
SHALL capture the last 200 lines of `journalctl -u controlai` over SSH
and print them, then SHALL exit non-zero without auto-destroying the
instance.

#### Scenario: All smoke tests pass

- **WHEN** all four smoke-test conditions succeed
- **THEN** `up.sh` SHALL print the summary block and exit 0

#### Scenario: Daemon failed to start

- **WHEN** `systemctl is-active controlai` does not print `active`
  within the smoke-test budget
- **THEN** `up.sh` SHALL print the captured journalctl tail and exit
  non-zero
- **AND** the EC2 instance SHALL remain running for operator
  inspection

### Requirement: Daemon API not exposed to the public network

The created security group SHALL NOT open the controlai daemon's
optional TCP+TLS port to `0.0.0.0/0`. Operators SHALL access the
daemon's REST API only via SSH local-forward over the unix socket at
`/var/run/controlai.sock`. The summary block printed by `up.sh`
SHALL include the recommended forwarding command.

#### Scenario: Default SG does not expose daemon

- **WHEN** the security group is created by Terraform
- **THEN** no ingress rule SHALL open any port other than 22, 80,
  443, and 8883

### Requirement: Operator runbook published at `docs/aws-deploy.md`

A documentation page at `docs/aws-deploy.md` SHALL describe the
operator prerequisites, the env var reference table, the quickstart
command, common troubleshooting cases (default VPC missing, vCPU
quota, GH rate limit, SSM permission denied, smoke-test failure),
cost notes, and the teardown command.

#### Scenario: Doc exists with required sections

- **WHEN** an operator opens `docs/aws-deploy.md`
- **THEN** the doc SHALL contain sections titled at least
  "Prerequisites", "Quickstart", "Environment variables",
  "Troubleshooting", "Cost", and "Teardown"
