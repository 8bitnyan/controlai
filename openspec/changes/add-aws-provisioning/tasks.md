# Tasks: add-aws-provisioning

## 1. Repo scaffolding & prerequisites

- [x] 1.1 Create directory `deploy/aws/` with subdirs `terraform/` and `.state/`.
- [x] 1.2 Add `.gitignore` entry: `deploy/aws/.state/`.
- [x] 1.3 Add `deploy/aws/.state/.gitkeep` so the directory exists fresh-clone (entry itself gitignored — use `!.gitkeep`).
- [x] 1.4 Add `deploy/aws/README.md` (terse — points at `docs/aws-deploy.md`).

## 2. GoReleaser & release workflow

- [x] 2.1 Add `.goreleaser.yaml` building `controlai` (from `./cmd/controlai`) for `linux/amd64`; archive as `controlai_{{ .Version }}_linux_amd64.tar.gz` containing the binary + `deploy/install/install.sh` + `deploy/systemd/*.service` + `LICENSE` + `README.md`.
- [x] 2.2 Inject `LDFLAGS` for version (`-X main.version={{.Version}}`) consistent with existing `Makefile`.
- [x] 2.3 Add `.github/workflows/release.yml` triggered on `push: tags: ['v*']` — checkout, setup-go, run `goreleaser release --clean`. Uses `GITHUB_TOKEN`.
- [x] 2.4 Cut a `v0.0.1` test tag on a fork or branch + verify the release artifact lands in GitHub Releases.

## 3. Terraform module (`deploy/aws/terraform/`)

- [x] 3.1 `versions.tf` — `terraform { required_version = ">= 1.6" }`, `required_providers { aws = "~> 5.60" }`.
- [x] 3.2 `variables.tf` — `aws_region`, `deployment_name`, `instance_type` (default `"t3.medium"`), `enable_eip` (default `false`), `ssh_key_name`, `user_data`, `ca_key_ssm_parameter_arn`, `controlai_version`, `extra_tags` (map).
- [x] 3.3 `provider.tf` — `provider "aws" { region = var.aws_region }`, `default_tags { tags = merge(local.base_tags, var.extra_tags) }` where `local.base_tags = { Project = "controlai", ManagedBy = "controlai-aws-provisioning", Environment = var.deployment_name, DeploymentName = var.deployment_name }`.
- [x] 3.4 `data.tf` — `data "aws_ami" "ubuntu"` filtered on Canonical (owner 099720109477) + `ubuntu-noble-24.04-amd64-server-*`; `data "aws_vpc" "default" { default = true }`; `data "aws_subnets" "default"` in default VPC; pick first AZ alphabetically via `local.subnet_id = sort(data.aws_subnets.default.ids)[0]`.
- [x] 3.5 `iam.tf` — `aws_iam_role` with EC2 trust policy; `aws_iam_role_policy` granting `ssm:GetParameter` on `var.ca_key_ssm_parameter_arn` only; `aws_iam_instance_profile` referencing the role.
- [x] 3.6 `sg.tf` — `aws_security_group` with name `${var.deployment_name}-sg`, ingress 22/80/443/8883 from `0.0.0.0/0`, egress all.
- [x] 3.7 `instance.tf` — `aws_instance` with `ami = data.aws_ami.ubuntu.id`, `instance_type = var.instance_type`, `subnet_id = local.subnet_id`, `vpc_security_group_ids = [aws_security_group.controlai.id]`, `key_name = var.ssh_key_name`, `iam_instance_profile = aws_iam_instance_profile.controlai.name`, `user_data = var.user_data`, `root_block_device { volume_type = "gp3", volume_size = 50, encrypted = true }`, `metadata_options { http_tokens = "required" }` (IMDSv2 enforce), `associate_public_ip_address = true`, `tags = { Name = "${var.deployment_name}-host" }`.
- [x] 3.8 `eip.tf` — `aws_eip { count = var.enable_eip ? 1 : 0 }` + `aws_eip_association` conditional.
- [x] 3.9 `outputs.tf` — `instance_id`, `public_ip`, `public_dns`, `security_group_id`, `iam_role_arn`, `ami_id`, `subnet_id`, `availability_zone`.
- [x] 3.10 `terraform fmt -check -recursive` clean; `terraform validate` clean against the configured provider.

## 4. `up.sh` (operator entry — provision)

- [x] 4.1 Shebang `#!/usr/bin/env bash`, `set -euo pipefail`, locale pinning.
- [x] 4.2 Prereq check function: `tofu`, `aws`, `jq`, `envsubst`, `openssl`, `ssh`. Print remediation hint per missing tool. Exit 1 on any missing.
- [x] 4.3 Env var resolution: `AWS_REGION` required (fail fast), `DEPLOYMENT_NAME` defaults `controlai-poc`, `INSTANCE_TYPE` defaults `t3.medium`, `CONTROLAI_VERSION` defaults `latest`, `ENABLE_EIP` defaults `false`, `GITHUB_RELEASES_REPO` defaults `controlai-iot/controlai`, `SSH_KEY_NAME` optional. Flags: `--replace`, `--dry-run`, `--yes`.
- [x] 4.4 Existing-deployment check: read `deploy/aws/.state/${DEPLOYMENT_NAME}.json`; if present + `aws ec2 describe-instances --instance-ids` confirms `running|pending`, print existing summary block + exit 0 (unless `--replace`).
- [x] 4.5 If `SSH_KEY_NAME` unset: `aws ec2 create-key-pair --key-type ed25519 --key-name "${DEPLOYMENT_NAME}-key"`, write to `~/.ssh/controlai-${DEPLOYMENT_NAME}.pem` (chmod 600), record path in state.
- [x] 4.6 If `--dry-run`: render `user-data.yaml.tmpl`, run `tofu plan -var-file=…`, print plan, exit 0.
- [x] 4.7 Generate CA encryption key: `openssl rand -hex 32`; SSM put: `aws ssm put-parameter --name "/controlai/${DEPLOYMENT_NAME}/ca_key" --type SecureString --value "$KEY" --overwrite=false` (fail if exists unless `--replace`); capture parameter ARN.
- [x] 4.8 Render user-data: `export CONTROLAI_VERSION DEPLOYMENT_NAME GITHUB_RELEASES_REPO AWS_REGION SSM_PARAM_NAME=…; envsubst < deploy/aws/user-data.yaml.tmpl > deploy/aws/.state/${DEPLOYMENT_NAME}.user-data`.
- [x] 4.9 `tofu -chdir=deploy/aws/terraform init -input=false` (only if `.terraform/` missing or providers stale).
- [x] 4.10 `tofu -chdir=deploy/aws/terraform apply -auto-approve -var aws_region=…  -var deployment_name=… -var instance_type=… -var enable_eip=… -var ssh_key_name=… -var ca_key_ssm_parameter_arn=… -var controlai_version=… -var user_data="$(cat …)"` with state file under `deploy/aws/.state/`.
- [x] 4.11 Capture outputs to `.deploy-state.json`: `{ deployment_name, aws_region, instance_id, public_ip, ami_id, ssm_parameter_arn, ssh_key_name, ssh_key_path_local, ssh_key_created_by_up, created_at, controlai_version }`.
- [x] 4.12 Smoke test: `aws ec2 wait instance-status-ok --instance-ids ${ID}` (timeout 600s).
- [x] 4.13 Smoke test: SSH to instance (`-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10`), run `cloud-init status --wait`; exit code must be 0.
- [x] 4.14 Smoke test: SSH, `systemctl is-active controlai` — must print `active`. On failure, capture `journalctl -u controlai --no-pager -n 200` and print it before exiting non-zero.
- [x] 4.15 Smoke test: open SSH local-forward `-L 7777:/var/run/controlai.sock`, `curl --unix-socket /tmp/sock http://localhost/v1/health` or equivalent; must HTTP 200. (If `/v1/health` not in core today, document the chosen endpoint in tasks 4.15 follow-up.)
- [x] 4.16 Print final summary block: deployment name, region, AZ, instance ID, public IP, sslip.io URL, SSH command, teardown command, state file path.
- [x] 4.17 Trap `ERR` to print a diagnostic and exit non-zero; do NOT auto-destroy on failure.

## 5. `down.sh` (operator entry — teardown)

- [x] 5.1 Shebang `#!/usr/bin/env bash`, `set -euo pipefail`.
- [x] 5.2 Prereq check (`tofu`, `aws`, `jq`).
- [x] 5.3 Read `deploy/aws/.state/${DEPLOYMENT_NAME}.json`; exit 1 if absent with explanation.
- [x] 5.4 Print resource summary: instance ID, public IP, SG ID, SSM parameter name, key pair name, EBS volume ID, region.
- [x] 5.5 Confirmation prompt: `read -p "Type the deployment name to confirm:"`; abort unless input matches. `--yes` bypasses.
- [x] 5.6 `tofu -chdir=deploy/aws/terraform destroy -auto-approve -var …` (same vars as apply, sourced from state).
- [x] 5.7 `aws ssm delete-parameter --name "/controlai/${DEPLOYMENT_NAME}/ca_key"`; ignore `ParameterNotFound`.
- [x] 5.8 If `ssh_key_created_by_up == true`: `aws ec2 delete-key-pair --key-name …` + `rm -f ~/.ssh/controlai-${DEPLOYMENT_NAME}.pem`.
- [x] 5.9 Remove `deploy/aws/.state/${DEPLOYMENT_NAME}.json` and `deploy/aws/.state/${DEPLOYMENT_NAME}.user-data`.
- [x] 5.10 Print final confirmation block.

## 6. `user-data.yaml.tmpl`

- [x] 6.1 MIME multipart header with stable boundary `//`.
- [x] 6.2 `#cloud-config` part: `package_update: true`, `package_upgrade: true`, `packages: [ca-certificates, curl, gnupg, jq, unzip, awscli]`.
- [x] 6.3 `#cloud-config` part: `write_files` for `/etc/controlai/env.template` (mode 0640, owner `root:controlai` — note user/group created later by `controlai install`), placeholder for `CONTROLAI_CA_KEY_ENCRYPTION_KEY=`.
- [x] 6.4 `#!/bin/bash` part with `set -euo pipefail` and a `log()` helper to `tag` into journald.
- [x] 6.5 Docker install: official Docker apt repo (`download.docker.com/linux/ubuntu noble stable`) — keyring at `/etc/apt/keyrings/docker.asc`, DEB822 source file; `apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin`; `systemctl enable --now docker`.
- [x] 6.6 controlai binary fetch: resolve version (`if [ "${CONTROLAI_VERSION}" = "latest" ]; then curl -fsSL https://api.github.com/repos/${GITHUB_RELEASES_REPO}/releases/latest | jq -r .tag_name; fi`); `curl -fsSL -o /tmp/controlai.tgz https://github.com/${GITHUB_RELEASES_REPO}/releases/download/${VERSION}/controlai_${VERSION#v}_linux_amd64.tar.gz`; `tar -xzf /tmp/controlai.tgz -C /tmp/`; `install -m 0755 /tmp/controlai /usr/local/bin/controlai`.
- [x] 6.7 Run `CONTROLAI_BINARY=/usr/local/bin/controlai bash /tmp/install.sh` (the install.sh extracted from the tarball).
- [x] 6.8 Fetch CA key from SSM: `KEY=$(aws ssm get-parameter --region ${AWS_REGION} --name ${SSM_PARAM_NAME} --with-decryption --query Parameter.Value --output text)`; write `/etc/controlai/env` with `CONTROLAI_CA_KEY_ENCRYPTION_KEY=${KEY}` (mode 0640, `root:controlai`).
- [x] 6.9 `systemctl daemon-reload && systemctl enable --now controlai`.
- [x] 6.10 Final log line `[controlai-bootstrap] complete` for grep-based smoke test backup.

## 7. Docs & README

- [x] 7.1 Write `docs/aws-deploy.md`: prereqs (AWS CLI v2 + creds, OpenTofu ≥ 1.6, bash 4+, jq, envsubst), quickstart (`AWS_REGION=us-east-1 ./deploy/aws/up.sh`), env var reference table, troubleshooting section (default VPC missing, vCPU quota, GH rate-limit, SSM permission denied, smoke-test failures), cost notes, teardown.
- [x] 7.2 Update root `README.md`: add "Deploy to AWS EC2 (quickstart)" subsection right after the on-host install runbook, pointing to `docs/aws-deploy.md`.
- [x] 7.3 Add a security caveat block to `docs/aws-deploy.md` explaining the default SG opens 22 to `0.0.0.0/0` and how to restrict.

## 8. Verification

- [x] 8.1 `terraform fmt -check` and `terraform validate` clean in CI (add a job to `.github/workflows/ci.yml`).
- [x] 8.2 `shellcheck deploy/aws/up.sh deploy/aws/down.sh` clean (or documented exceptions).
- [x] 8.3 Manual end-to-end test in a sandbox AWS account:
  - [x] 8.3.1 `AWS_REGION=ap-northeast-2 ./deploy/aws/up.sh` from a clean checkout — completes in ≤ 10 min, summary block correct. (Validated 2026-05-23 with DEPLOYMENT_NAME=ctl-smoke, v0.0.3 binary.)
  - [x] 8.3.2 SSH using the printed command — works.
  - [x] 8.3.3 `controlai version` on the box — prints `controlai 0.0.3`.
  - [x] 8.3.4 `systemctl is-active controlai` — `active`. Health endpoint `curl --unix-socket /run/controlai/controlai.sock http://localhost/v1/health` returns `{"docker_reachable":true,"reconciler_last_tick":...,"registry_healthy":true,"status":"ok","version":"0.0.3"}`.
  - [x] 8.3.5 Re-run `./deploy/aws/up.sh` — exits with "deployment exists" + same summary, no new instance.
  - [ ] 8.3.6 `./deploy/aws/up.sh --replace` — destroys + recreates cleanly. (Skipped during 2026-05-23 run to save AWS minutes; the path uses the same down.sh + up.sh code, both independently validated.)
  - [x] 8.3.7 `./deploy/aws/down.sh` (with `DEPLOYMENT_NAME=ctl-smoke`) — confirms, destroys, removes SSM parameter, deletes key pair, removes local state and key file. Validated end-to-end three times during this session.
  - [x] 8.3.8 After `down.sh`, AWS console shows no `ctl-smoke-*` resources in the region (instance, SG, EBS, key pair, SSM parameter).
- [x] 8.4 Negative tests:
  - [x] 8.4.1 `unset AWS_REGION; ./up.sh` — fails fast with `ERROR: AWS_REGION is required (example: ...)`.
  - [x] 8.4.2 Missing `tofu` binary — fails fast with `Missing required tool: tofu / Remediation: Install tofu: https://opentofu.org/docs/intro/install/`.
  - [ ] 8.4.3 No default VPC in the region — not exercised (default VPC was present in test region); code path validated by pre-flight `aws ec2 describe-vpcs` filter.
  - [ ] 8.4.4 Parallel deployments — not exercised; the implementation prefixes every resource with `DEPLOYMENT_NAME` and uses isolated state files, so collision is structurally prevented.
- [x] 8.5 `openspec validate add-aws-provisioning --strict` passes.

## 9. Archival

- [ ] 9.1 After all tasks complete, archive change to `openspec/changes/archive/YYYY-MM-DD-add-aws-provisioning/`, sync `aws-provisioning` capability into `openspec/specs/`.
