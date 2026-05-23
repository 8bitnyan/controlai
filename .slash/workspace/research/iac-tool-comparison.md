# Research: IaC Tool Comparison for Single-EC2 PoC (2025–2026)
Date: 2026-05-23

## Summary

Six IaC options were evaluated for provisioning a single EC2 instance + security group + EBS + optional Elastic IP, bootstrapped with a Go daemon via cloud-init/user-data. The target is a tiny PoC (≤5 tenants, t3.medium) that lives next to `deploy/install/install.sh` in the controlai repo. **Terraform (or OpenTofu) is the clear winner**; plain bash + AWS CLI is the runner-up for operators who want zero tooling overhead.

---

## Dimension-by-Dimension Comparison

### 1. Plain bash + AWS CLI

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | `./up.sh` — as simple as it gets |
| **State management** | None. Script stores IDs in a local `.env` or text file; you manage it manually. |
| **Teardown** | Manual: script must call `aws ec2 terminate-instances`, `aws ec2 delete-security-group`, `aws ec2 release-address` in the right order. Easy to get wrong. |
| **Idempotency** | Not inherent. Must hand-roll checks (`aws ec2 describe-instances --filters ...`). Fragile. |
| **Prereqs** | `aws` CLI only — already required for everything else in the repo. |
| **Fit for tiny PoC** | Perfect fit for a one-shot launch; becomes painful on the second re-creation. |
| **Multi-env / re-creation** | Painful. Env vars or sed-substitution for region/AMI/name. |
| **Repo fit** | Trivially lives next to `install.sh`. |

**Verdict**: Great for a true one-shot throwaway. Breaks down the moment you need to re-create or hand off to another operator.

---

### 2. Terraform (HashiCorp) — AWS provider v5.x

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | `terraform apply -auto-approve` (or wrap in `./up.sh`). `terraform destroy` for teardown. |
| **State management** | Local `terraform.tfstate` by default; trivially moved to S3 + DynamoDB lock with a 5-line `backend` block. State is the source of truth — no manual ID tracking. |
| **Teardown** | `terraform destroy` — symmetric, complete, ordered. Handles dependency ordering automatically (EIP → instance → SG). |
| **Idempotency** | Native. Plan/apply cycle compares desired vs actual state; re-running is safe. |
| **Prereqs** | `terraform` CLI (~70 MB binary, single download). AWS credentials via env vars or `~/.aws`. No Node, no Python. |
| **Fit for tiny PoC** | Right-sized. 3–4 `.tf` files (~80 lines total) covers EC2 + SG + EBS + optional EIP. No modules needed. |
| **Multi-env / re-creation** | `terraform.tfvars` or `-var` flags for region/AMI/name. Workspaces for env isolation. |
| **Repo fit** | `infra/` or `deploy/terraform/` directory alongside `install.sh`. `.gitignore` the state file if local; commit the `.tf` files. |

**Key facts (2025)**:
- Terraform v1.12.0 released; AWS provider v5.98.0 current as of May 2026.
- `user_data` field on `aws_instance` accepts raw cloud-init script or base64.
- `aws_ebs_volume` + `aws_volume_attachment` or inline `ebs_block_device` block both work.
- `aws_eip` + `aws_eip_association` for optional Elastic IP.
- `count = var.enable_eip ? 1 : 0` pattern handles the "optional" EIP cleanly.

**Verdict**: Best balance of simplicity, safety, and operator familiarity. The de-facto standard for this use case.

---

### 3. OpenTofu (Terraform fork, CNCF)

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | Identical to Terraform: `tofu apply -auto-approve`. |
| **State management** | Identical to Terraform. Same S3 backend support. |
| **Teardown** | `tofu destroy` — identical behavior. |
| **Idempotency** | Identical. |
| **Prereqs** | `tofu` CLI (single binary, same size as terraform). Drop-in replacement. |
| **Fit for tiny PoC** | Identical to Terraform. Same HCL syntax, same AWS provider. |
| **Multi-env / re-creation** | Identical. |
| **Repo fit** | Identical. `.tf` files are 100% compatible. |

**Key differentiators vs Terraform (2025–2026)**:
- OpenTofu 1.12.0 released; fully open-source (MPL-2.0 → BSL concern eliminated).
- Adds features Terraform hasn't: native state encryption, `tofu test` improvements, provider-defined functions.
- AWS provider is shared — same `hashicorp/aws` provider works with both.
- No BSL licensing concern (relevant if controlai ever redistributes tooling).
- Slightly smaller community than Terraform but growing fast under CNCF umbrella.

**Verdict**: Functionally identical to Terraform for this use case. Prefer OpenTofu if open-source licensing matters or if you want to avoid HashiCorp's BSL. Either is fine.

---

### 4. AWS CloudFormation (YAML template)

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | `aws cloudformation deploy --template-file stack.yaml --stack-name controlai` — one command, wrappable. |
| **State management** | AWS-managed, zero local state. Stack state lives in CloudFormation service. |
| **Teardown** | `aws cloudformation delete-stack --stack-name controlai` — symmetric. |
| **Idempotency** | Native. CloudFormation tracks stack state server-side. |
| **Prereqs** | `aws` CLI only — no extra binary. |
| **Fit for tiny PoC** | Over-engineered for a PoC. YAML template for EC2+SG+EBS+EIP is ~120–150 lines of verbose YAML. `UserData` must be base64-encoded inline (`Fn::Base64`). Debugging failures requires reading CloudFormation events. |
| **Multi-env / re-creation** | Parameters + `--parameter-overrides` flags. Works but verbose. |
| **Repo fit** | Single YAML file lives anywhere in the repo. |

**Pain points**:
- CloudFormation rollback behavior on partial failures can leave resources in a stuck state requiring manual cleanup.
- No local plan/preview — you deploy and watch events.
- `UserData` must be base64-encoded in the template (or use `Fn::Base64` wrapper), making cloud-init scripts awkward to read/edit.
- Stack update behavior for EC2 instances is complex: many property changes force instance replacement.

**Verdict**: Zero extra tooling is appealing, but the verbosity, lack of local preview, and awkward UserData handling make it worse than Terraform for this use case. Better suited for teams already deep in AWS-native tooling.

---

### 5. AWS CDK (TypeScript or Go)

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | `cdk deploy` — but requires `cdk bootstrap` first (one-time per account/region). |
| **State management** | CloudFormation-backed (same as CFN above). CDK synthesizes a CFN template. |
| **Teardown** | `cdk destroy` — symmetric. |
| **Idempotency** | Inherited from CloudFormation. |
| **Prereqs** | Node.js + npm (for TypeScript CDK) OR Go (for Go CDK). Plus `aws-cdk` CLI (`npm install -g aws-cdk`). Heavy prereq chain. |
| **Fit for tiny PoC** | Massively over-engineered. CDK shines for complex multi-service architectures. For a single EC2 instance, you're writing a full TypeScript/Go project with `package.json`/`go.mod`, a `cdk.json`, and a `lib/` directory just to wrap ~5 CloudFormation resources. |
| **Multi-env / re-creation** | CDK environments (account+region) and context variables. Powerful but complex. |
| **Repo fit** | Requires its own subdirectory with a full project structure. Awkward next to `install.sh`. |

**Verdict**: Wrong tool for this job. CDK's abstraction value emerges at 10+ resources or when you need L2/L3 constructs (ALB, ECS, RDS). For a single EC2 PoC it adds 3 layers of indirection (Go/TS → CDK → CFN → AWS API) with no benefit.

---

### 6. Pulumi (Go SDK)

| Dimension | Assessment |
|-----------|------------|
| **Single-command UX** | `pulumi up` — interactive by default; `pulumi up --yes` for non-interactive. |
| **State management** | Pulumi Cloud (SaaS) by default — requires account/login. Can use S3 backend (`pulumi login s3://bucket`) for self-hosted state. More friction than Terraform's local-first default. |
| **Teardown** | `pulumi destroy` — symmetric. |
| **Idempotency** | Native. State-driven like Terraform. |
| **Prereqs** | `pulumi` CLI + Go (already in use for the daemon). Pulumi Cloud account or S3 bucket for state. |
| **Fit for tiny PoC** | Moderate fit. Writing infra in Go is appealing since the team already knows Go. But the state backend friction and Pulumi Cloud dependency add overhead for a PoC. |
| **Multi-env / re-creation** | Pulumi stacks (one per env). Clean model. |
| **Repo fit** | `infra/` Go module alongside the daemon. Shares Go toolchain. |

**Key consideration**: Pulumi's Go SDK is genuinely good — `aws.ec2.NewInstance`, `aws.ec2.NewSecurityGroup`, `aws.ec2.NewEip` are idiomatic Go. The main friction is state: the default Pulumi Cloud backend requires an account and internet access during `pulumi up`. Self-hosting state on S3 works but requires explicit setup.

**Verdict**: Attractive if the team wants infra-as-Go-code and is willing to manage state. Overkill for a PoC; better suited if the project grows to need programmatic resource generation (e.g., per-tenant infra loops).

---

## Summary Scorecard

| Tool | UX | State | Teardown | Idempotency | Prereqs | PoC Fit | Repo Fit | **Score** |
|------|----|----|----|----|----|----|----|----|
| Bash + AWS CLI | ✅ | ❌ | ⚠️ | ❌ | ✅✅ | ✅ | ✅✅ | 5/10 |
| **Terraform** | ✅ | ✅ | ✅✅ | ✅✅ | ✅ | ✅✅ | ✅ | **9/10** |
| **OpenTofu** | ✅ | ✅ | ✅✅ | ✅✅ | ✅ | ✅✅ | ✅ | **9/10** |
| CloudFormation | ✅ | ✅✅ | ✅ | ✅ | ✅✅ | ⚠️ | ✅ | 6/10 |
| AWS CDK | ⚠️ | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | 4/10 |
| Pulumi (Go) | ✅ | ⚠️ | ✅ | ✅ | ⚠️ | ⚠️ | ✅ | 6/10 |

---

## Recommendation

### 🥇 Winner: OpenTofu (or Terraform — they are interchangeable for this use case)

**Rationale:**
1. **Right-sized**: 3–4 HCL files (~80 lines) covers EC2 + SG + EBS + optional EIP with `count`-based conditionals. No boilerplate beyond what the problem requires.
2. **Best teardown story**: `tofu destroy` handles dependency ordering (EIP → instance → SG) automatically and completely — critical for a PoC that will be torn down and re-created repeatedly.
3. **Local-first state**: `terraform.tfstate` works out of the box; upgrading to S3 backend is a 5-line change when the team is ready.
4. **Zero exotic prereqs**: one binary download, same AWS credentials already in use. No Node, no Python, no Pulumi Cloud account.
5. **Repo-native**: `deploy/terraform/` or `infra/` directory sits cleanly next to `deploy/install/install.sh`. The `.tf` files are readable by any engineer without knowing Go or TypeScript.
6. **Prefer OpenTofu** over Terraform if open-source licensing (BSL) is a concern, or if you want to avoid HashiCorp vendor lock-in. The HCL files are 100% compatible — switching is a binary swap.

### 🥈 Runner-up: Plain bash + AWS CLI

**Rationale:**
If the operator constraint is "zero new tools, ever" and the PoC is truly a one-shot launch that will never be re-created, a well-written `up.sh` + `down.sh` pair (with `aws ec2 describe-instances` guards for idempotency) is perfectly valid. It requires only the `aws` CLI already present in the repo. The cost is manual state tracking (store instance ID / SG ID in a `.env` file) and a fragile teardown script. Acceptable for a single operator who owns the lifecycle end-to-end; not acceptable once a second person needs to run it.

---

## Sources

- Terraform AWS Get Started tutorial: https://developer.hashicorp.com/terraform/tutorials/aws-get-started/aws-build (accessed 2026-05-23)
- Terraform AWS provider v5.98.0: https://registry.terraform.io/providers/hashicorp/aws/latest
- OpenTofu 1.12.0 release: https://opentofu.org/blog/opentofu-1-12-0/
- OpenTofu intro: https://opentofu.org/docs/intro/
- AWS CloudFormation EC2::Instance reference: https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-ec2-instance.html
- Pulumi AWS get started: https://www.pulumi.com/docs/iac/get-started/aws/
- AWS CDK v2 getting started: https://docs.aws.amazon.com/cdk/v2/guide/getting_started.html
