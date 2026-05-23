# AWS EC2 Provisioning Guide

This guide walks you through provisioning a controlai daemon on AWS EC2 using the automated deployment scripts.

## Prerequisites

Ensure you have the following tools installed and configured:

| Tool | Version | Installation | Notes |
|------|---------|--------------|-------|
| **AWS CLI** | v2 | [aws.amazon.com/cli](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) | Must be configured with credentials (`aws configure`) |
| **OpenTofu** | ≥ 1.6 | [opentofu.org/docs/intro/install](https://opentofu.org/docs/intro/install/) | Terraform-compatible fork; critical for infrastructure provisioning |
| **bash** | ≥ 4.0 | Built-in on macOS 3.2+ / Linux | Check with `bash --version` |
| **jq** | ≥ 1.6 | macOS: `brew install jq`; Ubuntu: `sudo apt-get install jq` | JSON query tool for state parsing |
| **envsubst** | Latest | macOS: `brew install gettext`; Ubuntu: `sudo apt-get install gettext` | Variable substitution for cloud-init templates |
| **openssl** | Latest | Built-in on macOS / Linux | Used for generating the CA encryption key |
| **ssh** | Latest | Built-in on macOS / Linux (OpenSSH) | For post-deployment connectivity and health checks |

### AWS Credentials

You must have valid AWS credentials configured locally. If you haven't done so:

```bash
aws configure
# Follow the prompts to enter your AWS Access Key ID and Secret Access Key.
# You can skip default region and output format (both overridable per command).
```

Verify your credentials are working:

```bash
aws sts get-caller-identity
# Should print your AWS account ID, User ARN, etc.
```

### AWS Permissions Required

The AWS credentials (IAM user or role) must have permissions for:

- EC2: `CreateInstance`, `CreateSecurityGroup`, `CreateKeyPair`, `CreateTags`, `DescribeInstances`, `DescribeAMIs`, `DescribeVpcs`, `DescribeSubnets`, `DescribeSecurityGroups`, `DescribeKeyPairs`, `DescribeVolumes`, `TerminateInstances`, `DeleteSecurityGroup`, `DeleteKeyPair`, `ModifyInstanceAttribute`
- IAM: `CreateRole`, `PutRolePolicy`, `CreateInstanceProfile`, `AddRoleToInstanceProfile`, `DeleteRolePolicy`, `DeleteInstanceProfile`, `RemoveRoleFromInstanceProfile`, `DeleteRole`
- SSM: `PutParameter`, `GetParameter`, `DeleteParameter`, `GetParameters`
- EIP (if `ENABLE_EIP=true`): `AllocateAddress`, `AssociateAddress`, `ReleaseAddress`

Most AWS managed policies `PowerUserAccess` or `IAMFullAccess` cover this. Start with a test account if unsure.

## Quickstart

From the repository root (where `deploy/aws/up.sh` exists), run:

```bash
AWS_REGION=us-east-1 ./deploy/aws/up.sh
```

Expected output (abbreviated):

```
[2026-05-23T14:32:01Z] checking prerequisites
[2026-05-23T14:32:02Z] creating EC2 key pair controlai-poc-key
[2026-05-23T14:32:03Z] generating CA encryption key
[2026-05-23T14:32:04Z] rendering user-data template
[2026-05-23T14:32:05Z] running tofu init
[2026-05-23T14:32:15Z] running tofu apply
...
[2026-05-23T14:37:30Z] smoke test: waiting for instance status OK
[2026-05-23T14:37:45Z] smoke test: cloud-init complete
[2026-05-23T14:37:46Z] smoke test: daemon active (running)
[2026-05-23T14:37:47Z] smoke test: daemon health check OK

Deployment ready:
  deployment_name: controlai-poc
  aws_region: us-east-1
  availability_zone: us-east-1a
  instance_id: i-0f1c2a3b4c5d6e7f8
  public_ip: 192.0.2.123
  url: https://192.0.2.123.sslip.io
  ssh: ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -i ~/.ssh/controlai-poc.pem ubuntu@192.0.2.123
  teardown: DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/down.sh
  state_file: deploy/aws/.state/controlai-poc.json
```

Your deployment is now ready. Use the printed `ssh` command to connect and verify:

```bash
ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -i ~/.ssh/controlai-poc.pem ubuntu@192.0.2.123

# On the box:
controlai version
systemctl status controlai
```

## Environment Variables Reference

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `AWS_REGION` | **Yes** | — | AWS region to deploy into (e.g., `us-east-1`, `eu-west-1`). **Fail-fast if unset.** |
| `DEPLOYMENT_NAME` | No | `controlai-poc` | Name prefix for all AWS resources. Enables multiple deployments per account/region (e.g., `DEPLOYMENT_NAME=demo`, `DEPLOYMENT_NAME=ci`). Used in resource names, tags, and state files. |
| `INSTANCE_TYPE` | No | `t3.medium` | EC2 instance type. For PoC (~5 tenants): use `t3.medium`. For higher load: `t3.large`, `t3.xlarge`, etc. (See cost notes below.) |
| `CONTROLAI_VERSION` | No | `latest` | Version to install. Options: `latest` (resolves at boot via GitHub API) or pinned (e.g., `v0.0.1`, `v1.2.3`). Must exist as a release on `GITHUB_RELEASES_REPO`. |
| `ENABLE_EIP` | No | `false` | Set to `true` to allocate an Elastic IP (static public IP). Useful if you need the IP to persist across instance restarts. **Cost: ~$3.25/month if unused.** |
| `GITHUB_RELEASES_REPO` | No | `8bitnyan/controlai` | GitHub repository (in format `org/repo`) from which to fetch the controlai binary. Override if deploying from a fork. |
| `SSH_KEY_NAME` | No | Auto-generated | Use an existing EC2 key pair by name. If unset, `up.sh` generates `${DEPLOYMENT_NAME}-key` and stores the private key at `~/.ssh/controlai-${DEPLOYMENT_NAME}.pem`. |

## Command-Line Flags

| Flag | Effect |
|------|--------|
| `--dry-run` | Run `tofu plan` (preview changes) without provisioning. Exit 0 after printing the plan. Useful for validating templates and cost estimates before committing. |
| `--replace` | If a deployment already exists, tear it down first (calls `down.sh --yes`), then provision fresh. Use carefully; no confirmation prompt. |
| `--yes` | Accept all prompts automatically. Used with `--replace` in CI environments. |

## What Gets Created

When you run `./deploy/aws/up.sh`, the following AWS resources are created:

| Resource | Type | Count | Notes |
|----------|------|-------|-------|
| EC2 Instance | `t3.medium` (default) | 1 | Ubuntu 24.04 LTS; runs the controlai daemon in Docker containers. |
| Security Group | Custom | 1 | Opens ports 22 (SSH), 80/443 (HTTP/HTTPS), 8883 (MQTT TLS) to `0.0.0.0/0`. See **Security Caveat** below. |
| EBS Volume | gp3, 50 GB (default) | 1 | Root volume; encrypted by default. Attached to the EC2 instance. |
| EC2 Key Pair | Ed25519 | 1 | Auto-generated (unless `SSH_KEY_NAME` provided). Private key saved locally. |
| IAM Role | — | 1 | Scoped to `ssm:GetParameter` on the CA key SSM parameter only. |
| IAM Instance Profile | — | 1 | Attached to the EC2 instance; grants the IAM role. |
| SSM Parameter | SecureString | 1 | Stores the CA encryption key at `/controlai/${DEPLOYMENT_NAME}/ca_key`. Encrypted with the AWS KMS default key. |

### Cost Estimate

**Baseline (us-east-1, t3.medium, 50 GB gp3, no EIP, 24/7):**
- EC2 instance: ~$30/month
- EBS gp3 storage: ~$4/month
- **Total: ~$34/month**

Once you run `./deploy/aws/down.sh`, all costs cease.

**Cost reductions:**
- Stop the instance (not delete): no EC2 charge, keep the EBS volume (~$0.80/month).
- Reduce instance type: `t3.small` (~$8/month), `t3.nano` (~$4/month). Verify tenants fit before downsizing.
- Delete EBS volume: save the storage cost.

**Cost additions:**
- Enable EIP: +~$3.25/month (only while associated).
- Larger instance: `t3.large` (~$60/month), `t3.xlarge` (~$120/month).

Check the [AWS EC2 Pricing page](https://aws.amazon.com/ec2/pricing/on-demand/) for your region.

## Connecting to Your Deployment

### SSH Access

After a successful `up.sh`, use the printed SSH command:

```bash
ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -i ~/.ssh/controlai-poc.pem ubuntu@192.0.2.123
```

(Replace the IP with the one printed in the summary block.)

The private key is stored at `~/.ssh/controlai-${DEPLOYMENT_NAME}.pem` (e.g., `~/.ssh/controlai-poc.pem`) with mode `0600`.

### Web Access (Traefik UI / MQTT over TLS)

**MQTT (port 8883):**
```bash
# From controlai CLI (local machine)
controlai pki cert issue --site tnt_acme-corp/ste_seoul --gateway my-device
# Gateways connect with mTLS to 192.0.2.123:8883
```

**HTTP/HTTPS (ports 80/443):**
The Traefik reverse proxy routes requests. For a quick test:
```bash
# HTTPS (self-signed, ignore warnings for PoC):
curl -k https://192.0.2.123.sslip.io/v1/health
# or use the sslip.io wildcard:
curl -k https://demo-tenant.192.0.2.123.sslip.io/

# HTTP (will redirect to HTTPS):
curl http://192.0.2.123/
```

### Accessing the Daemon REST API

The controlai daemon listens on a **unix socket** (`/run/controlai/controlai.sock`) and is **not exposed to the public network**. To access it:

```bash
# From your laptop, open an SSH tunnel:
ssh -i ~/.ssh/controlai-poc.pem -L 7777:/run/controlai/controlai.sock ubuntu@192.0.2.123

# In a second terminal, curl through the tunnel:
curl --unix-socket /tmp/controlai.sock http://localhost/v1/health
```

Or use the controlai CLI from the box itself:

```bash
ssh -i ~/.ssh/controlai-poc.pem ubuntu@192.0.2.123
# On the box:
controlai tenant list
controlai site list tnt_acme-corp
```

## Teardown

To destroy the deployment and **stop all AWS charges**:

```bash
DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/down.sh
```

The script will print the resources being destroyed and ask for confirmation:

```
About to destroy deployment:
  deployment_name: controlai-poc
  aws_region: us-east-1
  instance_id: i-0f1c2a3b4c5d6e7f8
  public_ip: 192.0.2.123
  security_group_id: sg-0a1b2c3d4e5f6g7h8
  ssm_parameter_name: /controlai/controlai-poc/ca_key
  key_pair_name: controlai-poc-key
  ebs_volume_id: vol-0a1b2c3d4e5f6g7h8

Type the deployment name to confirm:
```

Type `controlai-poc` and press Enter to confirm. The script will:

1. Terminate the EC2 instance.
2. Delete the security group.
3. Delete the EBS volume.
4. Delete the SSM parameter.
5. Delete the IAM role and instance profile.
6. Delete the EC2 key pair (if `up.sh` created it).
7. Delete the local private key file.
8. Remove `deploy/aws/.state/${DEPLOYMENT_NAME}.json` and related state files.

After `down.sh` completes, the AWS console should show no resources tagged with `DeploymentName=controlai-poc` in the region.

**For CI/automation**, use the `--yes` flag to skip the confirmation prompt:

```bash
DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/down.sh --yes
```

## Troubleshooting

### No Default VPC

**Error:** `ERROR: No default VPC found. Create one or supply VPC_ID/SUBNET_ID overrides.`

**Cause:** Many AWS accounts (especially enterprise tenants) have the default VPC deleted for security.

**Solution:**
1. Create a default VPC in the AWS console: EC2 → VPC → Actions → Create default VPC.
2. Or set `VPC_ID` and `SUBNET_ID` as Terraform overrides (future enhancement).

### vCPU Service Quota Exceeded

**Error:** `An error occurred (InsufficientInstanceCapacity) ... Cannot launch instance t3.medium ...` or quota exceeded.

**Cause:** New AWS accounts have a vCPU limit of 0–2 for `t3` instances. A `t3.medium` requires 1 vCPU.

**Solution:**
1. Go to the [Service Quotas console](https://console.aws.amazon.com/servicequotas/).
2. Search for "Running On-Demand Standard instances" (or `t3` directly).
3. Request a quota increase (usually approved in minutes).
4. Or downsize to `t3.nano` (0.5 vCPU) temporarily: `INSTANCE_TYPE=t3.nano ./up.sh`

### GitHub API Rate Limit

**Error:** `curl: (22) The requested URL returned error: 403 Forbidden` during binary download; `message: "API rate limit exceeded"`

**Cause:** If multiple deployments resolve `latest` from the public GitHub API, you may hit the 60 req/hour limit per IP.

**Solution:**
1. Use a pinned `CONTROLAI_VERSION`: `CONTROLAI_VERSION=v0.0.1 ./up.sh` (skips the API call).
2. Wait an hour and retry.
3. Set up GitHub authentication in the cloud-init script (future enhancement).

### SSM Parameter Permission Denied

**Error:** `An error occurred (AccessDenied) ... is not authorized to perform: ssm:GetParameter ...`

**Cause:** The EC2 instance profile's IAM role is missing `ssm:GetParameter` permission, or the role was manually deleted.

**Solution:**
1. Verify the IAM role exists: `aws iam get-role --role-name controlai-poc-role`.
2. Check its policy grants `ssm:GetParameter` on `/controlai/controlai-poc/ca_key`.
3. Re-run `./deploy/aws/down.sh --yes` and `./deploy/aws/up.sh` to recreate the role.

### Cloud-init Failure / Smoke Test Failed

**Error:** `ERROR: controlai health smoke test failed` or `controlai service is not active`

**Cause:** The cloud-init bootstrap script failed partway through (Docker install, binary download, controlai install, or service startup).

**Solution:**
1. SSH into the box and check the cloud-init logs:
   ```bash
   journalctl -u cloud-init -n 100
   sudo tail -f /var/log/cloud-init-output.log
   ```
2. Check the controlai service logs:
   ```bash
   journalctl -u controlai -n 200
   ```
3. Common issues:
   - **Docker not running:** `systemctl restart docker && systemctl restart controlai`
   - **Binary download failed:** Check GitHub API quota or network (`curl -I https://github.com`).
   - **Wrong CA key:** Check SSM parameter exists: `aws ssm get-parameter --name /controlai/controlai-poc/ca_key --region us-east-1`.
4. Logs are captured in `up.sh` output; review them for the root cause.
5. If unresolvable, destroy and re-provision: `./deploy/aws/down.sh --yes && ./deploy/aws/up.sh`

### Re-run Says Deployment Exists

**Message:** `Deployment already exists (controlai-poc). ...`

**Cause:** `up.sh` detected an existing deployment in the state file and running on AWS.

**Solution:**
1. **If you want to keep it:** Just SSH in and continue work.
2. **If you want a fresh deployment:** Pass the `--replace` flag:
   ```bash
   DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/up.sh --replace
   ```
   This destroys the old deployment and provisions a new one.
3. **If you want to delete it:** Use `down.sh`:
   ```bash
   DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/down.sh --yes
   ```

### Wrong AWS Region

**Error:** Instance launches in a region you didn't expect, or resources created in the wrong region.

**Cause:** `AWS_REGION` env var was not set, or your AWS CLI default region is different.

**Solution:**
1. Always explicitly set `AWS_REGION`:
   ```bash
   AWS_REGION=us-east-1 ./deploy/aws/up.sh
   ```
   Do **not** rely on `aws configure` default region.
2. Verify the correct region was used:
   ```bash
   grep aws_region deploy/aws/.state/controlai-poc.json
   ```
3. If you misprovisioned, tear down and re-run in the correct region:
   ```bash
   # In the wrong region:
   DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-west-2 ./deploy/aws/down.sh --yes
   
   # In the right region:
   DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/up.sh
   ```

## Security Caveat

### Default Security Group Opens SSH to the World

By default, the security group created by `up.sh` opens **TCP port 22 (SSH)** to `0.0.0.0/0` (the entire internet). This is intentional for PoC demo convenience, but **is not suitable for production**. Port 80, 443, and 8883 are also open to the world.

**Risk:** Brute-force SSH attacks (though Ed25519 keys are cryptographically strong and slow to attack compared to weak passwords).

**Mitigation:**

1. **Restrict SSH to your IP (temporary):**
   ```bash
   # Find your public IP:
   curl -s https://ifconfig.me
   # E.g., 203.0.113.42
   
   # Manually edit deploy/aws/terraform/sg.tf:
   # Change the SSH ingress rule to:
   # cidr_blocks = ["203.0.113.42/32"]  # Your IP only
   
   # Then re-provision:
   DEPLOYMENT_NAME=controlai-poc AWS_REGION=us-east-1 ./deploy/aws/up.sh --replace
   ```

2. **Use an AWS Systems Manager Session (future enhancement):**
   Instead of opening SSH, access the instance via AWS Systems Manager Session Manager (requires additional IAM permissions).

3. **Deploy behind a bastion or VPN (future):**
   Place the instance in a private subnet and access it via a bastion host or VPN.

For a production deployment, coordinate with your security team to:
- Restrict SSH to known IPs.
- Enable VPC Flow Logs to monitor inbound connections.
- Use AWS Systems Manager Session Manager instead of SSH.
- Run security group audits with AWS Config.

## Additional Resources

- **Design Decisions:** See `openspec/changes/add-aws-provisioning/design.md` for why OpenTofu was chosen, why SSM stores the CA key, and other architecture decisions.
- **OpenTofu Docs:** [opentofu.org/docs](https://opentofu.org/docs/)
- **AWS EC2 Pricing:** [aws.amazon.com/ec2/pricing](https://aws.amazon.com/ec2/pricing/on-demand/)
- **controlai CLI:** After SSH, run `controlai --help` for daemon and tenant management.
- **GitHub Releases Workflow:** See `.goreleaser.yaml` and `.github/workflows/release.yml` for how releases are cut.

## FAQ

**Q: Can I resize the instance after provisioning?**
A: Yes. SSH in, edit `deploy/aws/terraform/terraform.tfvars` (or re-run with `INSTANCE_TYPE=t3.large`), then `tofu apply`. The instance will be replaced.

**Q: Can I change the region after provisioning?**
A: No. Tear down (`down.sh`) and re-provision in the new region.

**Q: What if I accidentally delete the state file?**
A: The instance still exists in AWS. You can manually delete it in the EC2 console, or create a new `deploy/aws/.state/${DEPLOYMENT_NAME}.json` with the instance ID and re-run `down.sh`.

**Q: How do I upgrade controlai after deployment?**
A: SSH in and pull the latest release:
```bash
ssh ubuntu@<IP>
cd /opt/controlai
sudo systemctl stop controlai
curl -fsSL -o /tmp/controlai.tgz https://github.com/8bitnyan/controlai/releases/download/v0.0.2/controlai_0.0.2_linux_amd64.tar.gz
sudo tar -xzf /tmp/controlai.tgz -C /tmp/
sudo install -m 0755 /tmp/controlai /usr/local/bin/controlai
sudo systemctl start controlai
```

**Q: Is there a way to automate multiple deployments?**
A: Yes, use the `--yes` flag and set `DEPLOYMENT_NAME`:
```bash
for region in us-east-1 eu-west-1 ap-southeast-1; do
  AWS_REGION=$region DEPLOYMENT_NAME=controlai-$region ./deploy/aws/up.sh --yes
done
```

## Support

For issues or questions:
1. Review the **Troubleshooting** section above.
2. Check `journalctl` and `cloud-init-output.log` on the instance.
3. Review `openspec/changes/add-aws-provisioning/design.md` for architecture context.
4. Open an issue on the [GitHub repository](https://github.com/8bitnyan/controlai).
