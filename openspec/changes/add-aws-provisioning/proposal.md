# Change: Add one-command AWS EC2 provisioning for controlai core

## Why

`controlai` ships with an on-host installer (`deploy/install/install.sh`) and
two systemd units, but it assumes the operator has **already** stood up an
EC2 instance with Docker + docker compose v2 installed, decided on
networking, generated a 64-hex CA encryption key, and copied the binary onto
the box. Nothing in the repo helps with that pre-step.

The result today is a manual 12-step runbook (launch EC2 in console, pick
AMI, attach SG, SSH in, install Docker, install compose, scp binary, run
`install`, generate CA key, edit `/etc/controlai/env`, `systemctl start`,
`controlai shared init`). That friction blocks both demos and CI smoke
tests, and is exactly where new operators fail.

This change adds a **single command from the operator's laptop** that
provisions a fresh EC2 host, bootstraps controlai, and hands back an IP +
SSH command in under ~5 minutes — plus a symmetric `down.sh` that tears
everything down. It is **strictly a packaging / deployment layer**: it adds
no new control-plane behavior and modifies no existing capability.

## What Changes

- **New capability `aws-provisioning`** (additive). Operator-facing
  contract: `./deploy/aws/up.sh` provisions and bootstraps, `./deploy/aws/down.sh`
  tears down, both idempotent.
- **New directory `deploy/aws/`** containing:
  - `up.sh`, `down.sh` — operator entry points (POSIX bash).
  - `terraform/*.tf` — OpenTofu/Terraform modules (single EC2 + SG + EBS +
    IAM instance profile + SSM Parameter for CA key + key pair).
  - `user-data.yaml.tmpl` — cloud-init MIME multipart template
    (`#cloud-config` packages/files + `#!/bin/bash` script).
  - `.state/` — gitignored local state (tofu state + `.deploy-state.json`).
- **New release pipeline** under `.github/workflows/release.yml` + a
  `.goreleaser.yaml` that publishes `controlai_<version>_linux_amd64.tar.gz`
  to GitHub Releases on `git tag v*`. The `up.sh` flow depends on this
  artifact existing.
- **New doc `docs/aws-deploy.md`** — operator runbook for the AWS path
  (prereqs, command surface, troubleshooting, teardown, cost notes).
- **README.md update** — short section pointing at the AWS quickstart.
- **`.gitignore` update** — exclude `deploy/aws/.state/`.

This change does **NOT** modify: `cmd/controlai`, `internal/**`,
`services/ingest`, `deploy/install/install.sh`, `deploy/systemd/*`, or any
existing OpenSpec capability. The on-host install path remains the
canonical one; the AWS layer wraps it.

## Impact

- **Affected specs:** `aws-provisioning` (new capability; ADDED only).
  No MODIFIED/REMOVED on any existing capability.
- **Affected code:** new files only — `deploy/aws/**`, `.github/workflows/release.yml`,
  `.goreleaser.yaml`, `docs/aws-deploy.md`. README and .gitignore minor
  updates.
- **New operator prereqs (documented):** AWS CLI v2 + credentials,
  OpenTofu ≥ 1.6 (`tofu` binary), bash 4+, `envsubst`, `jq`.
- **New AWS resources created per deployment:** 1 × EC2 instance (t3.medium
  default), 1 × security group, 1 × EBS gp3 50 GB, 1 × EC2 key pair, 1 ×
  IAM role + instance profile (scope: `ssm:GetParameter` only), 1 × SSM
  SecureString parameter (CA encryption key).
- **Estimated runtime cost (us-east-1, t3.medium, gp3 50 GB, no EIP):**
  ~$35/month if left running; $0 once `down.sh` is invoked.
- **Out of scope (explicitly):** Kubernetes / EKS, multi-region, ASG, ALB,
  RDS, Route53/TLS automation, CloudWatch Logs integration, S3 backup
  sync. Each is a separate future change.
