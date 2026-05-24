#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
STATE_DIR="${REPO_ROOT}/deploy/aws/.state"

AWS_REGION="${AWS_REGION:-}"
DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-controlai-poc}"
INSTANCE_TYPE="${INSTANCE_TYPE:-t3.medium}"
CONTROLAI_VERSION="${CONTROLAI_VERSION:-latest}"
ENABLE_EIP="${ENABLE_EIP:-false}"
GITHUB_RELEASES_REPO="${GITHUB_RELEASES_REPO:-8bitnyan/controlai}"
SSH_KEY_NAME="${SSH_KEY_NAME:-}"

# HTTPS / Traefik configuration
ACME_EMAIL="${ACME_EMAIL:-}"
ACME_STAGING="${ACME_STAGING:-1}"
DAEMON_TCP_PORT="${DAEMON_TCP_PORT:-8080}"
# DEPLOYMENT_DOMAIN overrides the auto-generated sslip.io domain if set.
DEPLOYMENT_DOMAIN="${DEPLOYMENT_DOMAIN:-}"

REPLACE=false
DRY_RUN=false
ASSUME_YES=false

STATE_FILE=""
USER_DATA_FILE=""
SSH_KEY_PATH_LOCAL=""
SSH_KEY_CREATED_BY_UP=false
SSM_PARAM_NAME=""
SSM_PARAMETER_ARN=""
CA_KEY=""

log() { printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*"; }

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

on_error() {
  local exit_code=$?
  printf 'ERROR: up.sh failed at line %s (exit %s).\n' "${BASH_LINENO[0]}" "${exit_code}" >&2
  exit "${exit_code}"
}
trap on_error ERR

check_prereqs() {
  local missing=0
  check_tool() {
    local tool="$1" hint="$2"
    if ! command -v "${tool}" >/dev/null 2>&1; then
      printf 'Missing required tool: %s\n' "${tool}" >&2
      printf 'Remediation: %s\n' "${hint}" >&2
      missing=1
    fi
  }

  check_tool tofu "Install tofu: https://opentofu.org/docs/intro/install/"
  check_tool aws "Install AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
  check_tool jq "Install jq: https://jqlang.github.io/jq/download/"
  check_tool envsubst "Install envsubst (gettext): https://www.gnu.org/software/gettext/manual/html_node/envsubst-Invocation.html"
  check_tool openssl "Install OpenSSL: https://www.openssl.org/source/"
  check_tool ssh "Install OpenSSH client: https://www.openssh.com/portable.html"

  if [ "${missing}" -ne 0 ]; then
    exit 1
  fi
}

usage() {
  cat <<EOF
Usage: AWS_REGION=<region> ./deploy/aws/up.sh [--replace] [--dry-run] [--yes]
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --replace) REPLACE=true ;;
    --dry-run) DRY_RUN=true ;;
    --yes) ASSUME_YES=true ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown flag: $1" ;;
  esac
  shift
done

log "checking prerequisites"
check_prereqs
[ -n "${AWS_REGION}" ] || fail "AWS_REGION is required (example: AWS_REGION=us-east-1 ./deploy/aws/up.sh)"
[ -n "${ACME_EMAIL}" ] || fail "ACME_EMAIL is required for Let's Encrypt certificate issuance (e.g. ACME_EMAIL=you@example.com)"

# Verify a default VPC exists in the target region (D7: fail fast with a clear message if absent)
default_vpc_id="$(aws --region "${AWS_REGION}" ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text 2>/dev/null || true)"
if [ -z "${default_vpc_id}" ] || [ "${default_vpc_id}" = "None" ]; then
  printf 'ERROR: No default VPC found in region %s.\n' "${AWS_REGION}" >&2
  printf 'ERROR: Create one via: aws ec2 create-default-vpc --region %s\n' "${AWS_REGION}" >&2
  printf 'ERROR: Or supply VPC_ID and SUBNET_ID overrides in the Terraform module variables.\n' >&2
  exit 1
fi

mkdir -p "${STATE_DIR}"
STATE_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.json"
USER_DATA_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.user-data"
SSM_PARAM_NAME="/controlai/${DEPLOYMENT_NAME}/ca_key"

if [ -f "${STATE_FILE}" ] && [ "${REPLACE}" = false ]; then
  existing_instance_id="$(jq -r '.instance_id // empty' "${STATE_FILE}")"
  if [ -n "${existing_instance_id}" ]; then
    status="$(aws --region "${AWS_REGION}" ec2 describe-instances --instance-ids "${existing_instance_id}" --query 'Reservations[0].Instances[0].State.Name' --output text 2>/dev/null || true)"
    if [ "${status}" = "running" ] || [ "${status}" = "pending" ]; then
      printf 'Deployment already exists (%s).\n' "${DEPLOYMENT_NAME}"
      jq -r '"deployment_name=\(.deployment_name)\naws_region=\(.aws_region)\ninstance_id=\(.instance_id)\npublic_ip=\(.public_ip)\nssh_key_name=\(.ssh_key_name)\nssh_key_path_local=\(.ssh_key_path_local)\nstate_file='"${STATE_FILE}"'"' "${STATE_FILE}"
      exit 0
    fi
  fi
fi

if [ "${REPLACE}" = true ] && [ -f "${STATE_FILE}" ]; then
  log "--replace specified; tearing down existing deployment first"
  if [ "${ASSUME_YES}" = true ]; then
    DEPLOYMENT_NAME="${DEPLOYMENT_NAME}" AWS_REGION="${AWS_REGION}" "${REPO_ROOT}/deploy/aws/down.sh" --yes
  else
    DEPLOYMENT_NAME="${DEPLOYMENT_NAME}" AWS_REGION="${AWS_REGION}" "${REPO_ROOT}/deploy/aws/down.sh"
  fi
fi

if [ -z "${SSH_KEY_NAME}" ]; then
  SSH_KEY_NAME="${DEPLOYMENT_NAME}-key"
  SSH_KEY_PATH_LOCAL="${HOME}/.ssh/controlai-${DEPLOYMENT_NAME}.pem"
  if [ "${DRY_RUN}" = true ]; then
    log "[dry-run] would create EC2 key pair ${SSH_KEY_NAME} (skipped; no AWS calls in dry-run)"
  elif [ ! -f "${SSH_KEY_PATH_LOCAL}" ]; then
    log "creating EC2 key pair ${SSH_KEY_NAME}"
    umask 077
    aws --region "${AWS_REGION}" ec2 create-key-pair --key-type ed25519 --key-name "${SSH_KEY_NAME}" --query 'KeyMaterial' --output text >"${SSH_KEY_PATH_LOCAL}"
    chmod 600 "${SSH_KEY_PATH_LOCAL}"
    SSH_KEY_CREATED_BY_UP=true
  fi
elif [ "${DRY_RUN}" = false ]; then
  aws --region "${AWS_REGION}" ec2 describe-key-pairs --key-names "${SSH_KEY_NAME}" >/dev/null
fi

if [ "${DRY_RUN}" = false ]; then
  CA_KEY="$(openssl rand -hex 32)"
  if [ "${REPLACE}" = true ]; then
    aws --region "${AWS_REGION}" ssm put-parameter --name "${SSM_PARAM_NAME}" --type SecureString --value "${CA_KEY}" --overwrite >/dev/null
  else
    aws --region "${AWS_REGION}" ssm put-parameter --name "${SSM_PARAM_NAME}" --type SecureString --value "${CA_KEY}" --no-overwrite >/dev/null
  fi
  SSM_PARAMETER_ARN="$(aws --region "${AWS_REGION}" ssm get-parameter --name "${SSM_PARAM_NAME}" --query 'Parameter.ARN' --output text)"
else
  SSM_PARAMETER_ARN="arn:aws:ssm:${AWS_REGION}:000000000000:parameter${SSM_PARAM_NAME}"
fi

export CONTROLAI_VERSION DEPLOYMENT_NAME GITHUB_RELEASES_REPO AWS_REGION SSM_PARAM_NAME DAEMON_TCP_PORT
# Only substitute the known provisioning variables; all other shell variables in the
# bash section of user-data.yaml.tmpl (e.g. $KEY, $VERSION) must pass through unchanged.
# shellcheck disable=SC2016  # single-quotes intentional: envsubst parses this list itself, no shell expansion wanted
envsubst '$CONTROLAI_VERSION $DEPLOYMENT_NAME $GITHUB_RELEASES_REPO $AWS_REGION $SSM_PARAM_NAME $DAEMON_TCP_PORT' \
  <"${REPO_ROOT}/deploy/aws/user-data.yaml.tmpl" >"${USER_DATA_FILE}"

TF_DATA_DIR="${STATE_DIR}/.terraform" tofu -chdir="${REPO_ROOT}/deploy/aws/terraform" init -input=false

if [ "${DRY_RUN}" = true ]; then
  TF_DATA_DIR="${STATE_DIR}/.terraform" tofu -chdir="${REPO_ROOT}/deploy/aws/terraform" plan -state="../.state/terraform.tfstate" -var "aws_region=${AWS_REGION}" -var "deployment_name=${DEPLOYMENT_NAME}" -var "instance_type=${INSTANCE_TYPE}" -var "enable_eip=${ENABLE_EIP}" -var "ssh_key_name=${SSH_KEY_NAME}" -var "ca_key_ssm_parameter_arn=${SSM_PARAMETER_ARN}" -var "controlai_version=${CONTROLAI_VERSION}" -var "user_data=$(cat "${USER_DATA_FILE}")" -var 'extra_tags={}'
  exit 0
fi

TF_DATA_DIR="${STATE_DIR}/.terraform" tofu -chdir="${REPO_ROOT}/deploy/aws/terraform" apply -state="../.state/terraform.tfstate" -auto-approve -var "aws_region=${AWS_REGION}" -var "deployment_name=${DEPLOYMENT_NAME}" -var "instance_type=${INSTANCE_TYPE}" -var "enable_eip=${ENABLE_EIP}" -var "ssh_key_name=${SSH_KEY_NAME}" -var "ca_key_ssm_parameter_arn=${SSM_PARAMETER_ARN}" -var "controlai_version=${CONTROLAI_VERSION}" -var "user_data=$(cat "${USER_DATA_FILE}")" -var 'extra_tags={}'

tofu_output() {
  TF_DATA_DIR="${STATE_DIR}/.terraform" tofu -chdir="${REPO_ROOT}/deploy/aws/terraform" output -state="../.state/terraform.tfstate" -raw "$1"
}
instance_id="$(tofu_output instance_id)"
public_ip="$(tofu_output public_ip)"
ami_id="$(tofu_output ami_id)"
az="$(tofu_output availability_zone)"

jq -n --arg deployment_name "${DEPLOYMENT_NAME}" --arg aws_region "${AWS_REGION}" --arg instance_id "${instance_id}" --arg public_ip "${public_ip}" --arg ami_id "${ami_id}" --arg ssm_parameter_arn "${SSM_PARAMETER_ARN}" --arg ssh_key_name "${SSH_KEY_NAME}" --arg ssh_key_path_local "${SSH_KEY_PATH_LOCAL}" --arg created_at "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" --arg controlai_version "${CONTROLAI_VERSION}" --argjson ssh_key_created_by_up "${SSH_KEY_CREATED_BY_UP}" '{deployment_name:$deployment_name,aws_region:$aws_region,instance_id:$instance_id,public_ip:$public_ip,ami_id:$ami_id,ssm_parameter_arn:$ssm_parameter_arn,ssh_key_name:$ssh_key_name,ssh_key_path_local:$ssh_key_path_local,ssh_key_created_by_up:$ssh_key_created_by_up,created_at:$created_at,controlai_version:$controlai_version}' >"${STATE_FILE}"

# `aws ec2 wait instance-status-ok` has an intermittent XML parsing bug in some
# botocore versions; poll directly instead. Timeout: 60 attempts × 10s = 10 min.
log "waiting for instance status-ok (manual poll)"
for _i in $(seq 1 60); do
  st="$(aws --region "${AWS_REGION}" ec2 describe-instance-status --instance-ids "${instance_id}" --query 'InstanceStatuses[0].[InstanceStatus.Status,SystemStatus.Status]' --output text 2>/dev/null || echo "pending pending")"
  case "${st}" in
    "ok"*"ok") log "instance status-ok after ~$((_i * 10))s"; break ;;
  esac
  if [ "${_i}" = 60 ]; then fail "instance did not reach status-ok within 10 min: ${st}"; fi
  sleep 10
done

ssh_opts=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null)
[ -n "${SSH_KEY_PATH_LOCAL}" ] && ssh_opts+=(-i "${SSH_KEY_PATH_LOCAL}")

# Wait for the bootstrap script to finish; don't use `cloud-init status` exit
# code — it returns non-zero for recoverable warnings (e.g. write_files chown
# referencing a user created later in the same boot). The bootstrap-complete
# log line + systemctl is-active are the truth.
log "waiting for cloud-init bootstrap log line"
for _i in $(seq 1 30); do
  if ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" "sudo grep -q '\[controlai-bootstrap\] complete' /var/log/cloud-init-output.log" 2>/dev/null; then
    log "bootstrap complete after ~$((_i * 10))s"
    break
  fi
  if [ "${_i}" = 30 ]; then fail "cloud-init bootstrap did not complete within 5 min"; fi
  sleep 10
done

if [ "$(ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" systemctl is-active controlai || true)" != "active" ]; then
  ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" journalctl -u controlai --no-pager -n 200 || true
  fail "controlai service is not active"
fi

# Probe strategy: try /v1/health first; if 404, fallback to bootstrap log + active systemd.
if ! ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" 'code=$(sudo curl -sS -o /dev/null -w "%{http_code}" --unix-socket /run/controlai/controlai.sock http://localhost/v1/health || true); if [ "$code" = "200" ]; then exit 0; fi; if [ "$code" = "404" ]; then grep -q "\[controlai-bootstrap\] complete" /var/log/cloud-init-output.log && [ "$(systemctl is-active controlai)" = "active" ]; else exit 1; fi'; then
  ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" journalctl -u controlai --no-pager -n 200 || true
  fail "controlai health smoke test failed"
fi

# ── Security group port assertion ─────────────────────────────────────────────
# Ports 80 and 443 must be open for ACME HTTP-01 challenge and HTTPS API traffic.
# The Terraform module already opens these ports; this step provides an explicit
# smoke-test assertion so misconfigured custom SG setups are caught early.
sg_id="$(aws --region "${AWS_REGION}" ec2 describe-instances \
  --instance-ids "${instance_id}" \
  --query 'Reservations[0].Instances[0].SecurityGroups[0].GroupId' \
  --output text 2>/dev/null || true)"

check_sg_port() {
  local port="$1"
  if [ -z "${sg_id}" ] || [ "${sg_id}" = "None" ]; then
    log "WARN: could not determine security group ID — skipping port ${port} assertion"
    return
  fi
  local open
  open="$(aws --region "${AWS_REGION}" ec2 describe-security-group-rules \
    --filters "Name=group-id,Values=${sg_id}" \
    --query "SecurityGroupRules[?IpProtocol=='tcp' && FromPort<=${port} && ToPort>=${port} && CidrIpv4=='0.0.0.0/0'].SecurityGroupRuleId" \
    --output text 2>/dev/null || true)"
  if [ -z "${open}" ]; then
    log "Port ${port} not open in SG ${sg_id} — adding ingress rule"
    aws --region "${AWS_REGION}" ec2 authorize-security-group-ingress \
      --group-id "${sg_id}" \
      --protocol tcp \
      --port "${port}" \
      --cidr 0.0.0.0/0 >/dev/null
    log "Port ${port} ingress rule added"
  else
    log "Port ${port} is open in SG ${sg_id} — OK"
  fi
}

log "asserting security group ports 80 and 443 are open"
check_sg_port 80
check_sg_port 443

# ── Traefik configuration upload and start ────────────────────────────────────
# Compute the ACME CA server URL based on ACME_STAGING flag.
if [ "${ACME_STAGING}" = "1" ] || [ "${ACME_STAGING}" = "true" ]; then
  ACME_CA_SERVER="https://acme-staging-v02.api.letsencrypt.org/directory"
else
  ACME_CA_SERVER="https://acme-v02.api.letsencrypt.org/directory"
fi

# Determine API domain (sslip.io or custom override).
if [ -n "${DEPLOYMENT_DOMAIN}" ]; then
  API_DOMAIN="${DEPLOYMENT_DOMAIN}"
else
  API_DOMAIN="api.${DEPLOYMENT_NAME}.sslip.io"
fi

log "rendering Traefik static config (ACME_STAGING=${ACME_STAGING})"
TRAEFIK_STATIC_RENDERED="${STATE_DIR}/${DEPLOYMENT_NAME}.traefik.yml"
export ACME_EMAIL ACME_CA_SERVER
# shellcheck disable=SC2016
envsubst '$ACME_EMAIL $ACME_CA_SERVER' \
  <"${REPO_ROOT}/deploy/aws/traefik/traefik.yml" >"${TRAEFIK_STATIC_RENDERED}"

log "rendering Traefik dynamic API route config"
TRAEFIK_DYNAMIC_RENDERED="${STATE_DIR}/${DEPLOYMENT_NAME}.api.yml"
API_HOST="${API_DOMAIN}"
export DEPLOYMENT_NAME DAEMON_TCP_PORT API_HOST
# shellcheck disable=SC2016
envsubst '$DEPLOYMENT_NAME $DAEMON_TCP_PORT $API_HOST' \
  <"${REPO_ROOT}/deploy/aws/traefik/dynamic/api.yml.tmpl" >"${TRAEFIK_DYNAMIC_RENDERED}"

log "uploading Traefik config files to ${public_ip}"
scp "${ssh_opts[@]}" "${TRAEFIK_STATIC_RENDERED}" ubuntu@"${public_ip}":/tmp/traefik.yml
scp "${ssh_opts[@]}" "${TRAEFIK_DYNAMIC_RENDERED}" ubuntu@"${public_ip}":/tmp/api.yml

ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" bash <<'EOF_TRAEFIK_INSTALL'
set -euo pipefail
sudo install -m 0644 /tmp/traefik.yml /etc/traefik/traefik.yml
sudo install -m 0644 /tmp/api.yml /etc/traefik/dynamic/api.yml
sudo systemctl enable traefik
sudo systemctl restart traefik
# Wait for Traefik to be active.
for _i in $(seq 1 10); do
  if sudo systemctl is-active --quiet traefik; then
    echo "Traefik is active"
    break
  fi
  if [ "${_i}" = 10 ]; then
    sudo journalctl -u traefik --no-pager -n 50 >&2
    echo "ERROR: Traefik failed to start" >&2
    exit 1
  fi
  sleep 2
done
EOF_TRAEFIK_INSTALL

# Print ACME staging warning banner if applicable.
if [ "${ACME_STAGING}" = "1" ] || [ "${ACME_STAGING}" = "true" ]; then
  printf '\n'
  printf '╔══════════════════════════════════════════════════════════════╗\n'
  printf '║  ⚠  ACME STAGING MODE — certificate is NOT browser-trusted  ║\n'
  printf '║                                                              ║\n'
  printf '║  The Let'\''s Encrypt staging CA was used (ACME_STAGING=1).   ║\n'
  printf '║  To switch to a production certificate after validation:     ║\n'
  printf '║    DEPLOYMENT_NAME=%s AWS_REGION=%s \\\n' "${DEPLOYMENT_NAME}" "${AWS_REGION}"
  printf '║    ./deploy/aws/scripts/flip-acme-prod.sh                   ║\n'
  printf '╚══════════════════════════════════════════════════════════════╝\n'
  printf '\n'
fi

# ── Poll HTTPS endpoint (60-second timeout) ───────────────────────────────────
log "polling https://${API_DOMAIN}/v1/health for up to 60 seconds (cert acquisition)..."
HTTPS_HEALTHY=false
deadline=$(( $(date +%s) + 60 ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  http_code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 \
    "https://${API_DOMAIN}/v1/health" 2>/dev/null || echo "000")"
  if [ "${http_code}" = "401" ] || [ "${http_code}" = "200" ]; then
    HTTPS_HEALTHY=true
    log "HTTPS endpoint reachable (HTTP ${http_code}) — Traefik+cert are up"
    break
  fi
  sleep 5
done
if [ "${HTTPS_HEALTHY}" = false ]; then
  log "WARN: HTTPS endpoint not yet reachable after 60s — cert may still be pending."
  log "WARN: Check Traefik logs: ssh ubuntu@${public_ip} sudo journalctl -u traefik --no-pager -n 100"
fi

# ── Bearer-token end-to-end smoke test ───────────────────────────────────────
if [ "${HTTPS_HEALTHY}" = true ]; then
  log "running bearer-token smoke test against https://${API_DOMAIN}"

  # Create a transient smoke-test token on the host.
  SMOKE_TOKEN="$(ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" \
    "sudo -u controlai controlai token create smoke-test 2>/dev/null || controlai token create smoke-test" \
    2>/dev/null | tail -1 || true)"

  if [ -n "${SMOKE_TOKEN}" ]; then
    # Call HTTPS API with valid token — expect 200.
    auth_code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 30 \
      -H "Authorization: Bearer ${SMOKE_TOKEN}" \
      "https://${API_DOMAIN}/v1/health" 2>/dev/null || echo "000")"

    # Call HTTPS API without token — expect 401.
    noauth_code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 30 \
      "https://${API_DOMAIN}/v1/health" 2>/dev/null || echo "000")"

    # Revoke the transient token.
    ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" \
      "sudo -u controlai controlai token revoke smoke-test 2>/dev/null || controlai token revoke smoke-test" \
      2>/dev/null || true

    if [ "${auth_code}" = "200" ] && [ "${noauth_code}" = "401" ]; then
      log "API endpoint: HEALTHY (authenticated=200, unauthenticated=401)"
    elif [ "${auth_code}" = "200" ]; then
      log "WARN: authenticated request returned 200 but unauthenticated returned ${noauth_code} (expected 401)"
    else
      log "WARN: smoke test: authenticated returned ${auth_code} (expected 200), unauthenticated returned ${noauth_code}"
    fi
  else
    log "WARN: could not create smoke-test token — skipping bearer-token validation"
    log "WARN: Run manually: controlai token create smoke-test && curl -H 'Authorization: Bearer <token>' https://${API_DOMAIN}/v1/health"
  fi
fi

printf 'Deployment ready:\n'
printf '  deployment_name: %s\n' "${DEPLOYMENT_NAME}"
printf '  aws_region: %s\n' "${AWS_REGION}"
printf '  availability_zone: %s\n' "${az}"
printf '  instance_id: %s\n' "${instance_id}"
printf '  public_ip: %s\n' "${public_ip}"
printf '  url: https://%s.sslip.io\n' "${public_ip}"
printf '  api_url: https://%s\n' "${API_DOMAIN}"
if [ -n "${SSH_KEY_PATH_LOCAL}" ]; then
  printf '  ssh: ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -i %q ubuntu@%s\n' "${SSH_KEY_PATH_LOCAL}" "${public_ip}"
fi
printf '  teardown: DEPLOYMENT_NAME=%s AWS_REGION=%s ./deploy/aws/down.sh\n' "${DEPLOYMENT_NAME}" "${AWS_REGION}"
printf '  state_file: %s\n' "${STATE_FILE}"
printf '\n'
printf '  Next step: create a long-lived bearer token for the controlai-web BFF:\n'
printf '    ssh ubuntu@%s\n' "${public_ip}"
printf '    controlai token create web-bff\n'
printf '  Save the token and set it as CONTROLAI_BEARER_TOKEN in your Vercel environment.\n'
