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

export CONTROLAI_VERSION DEPLOYMENT_NAME GITHUB_RELEASES_REPO AWS_REGION SSM_PARAM_NAME
# Only substitute the five known provisioning variables; all other shell variables in the
# bash section of user-data.yaml.tmpl (e.g. $KEY, $VERSION) must pass through unchanged.
# shellcheck disable=SC2016  # single-quotes intentional: envsubst parses this list itself, no shell expansion wanted
envsubst '$CONTROLAI_VERSION $DEPLOYMENT_NAME $GITHUB_RELEASES_REPO $AWS_REGION $SSM_PARAM_NAME' \
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

ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" cloud-init status --wait
if [ "$(ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" systemctl is-active controlai || true)" != "active" ]; then
  ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" journalctl -u controlai --no-pager -n 200 || true
  fail "controlai service is not active"
fi

# Probe strategy: try /v1/health first; if 404, fallback to bootstrap log + active systemd.
if ! ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" 'code=$(sudo curl -sS -o /dev/null -w "%{http_code}" --unix-socket /run/controlai/controlai.sock http://localhost/v1/health || true); if [ "$code" = "200" ]; then exit 0; fi; if [ "$code" = "404" ]; then grep -q "\[controlai-bootstrap\] complete" /var/log/cloud-init-output.log && [ "$(systemctl is-active controlai)" = "active" ]; else exit 1; fi'; then
  ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" journalctl -u controlai --no-pager -n 200 || true
  fail "controlai health smoke test failed"
fi

printf 'Deployment ready:\n'
printf '  deployment_name: %s\n' "${DEPLOYMENT_NAME}"
printf '  aws_region: %s\n' "${AWS_REGION}"
printf '  availability_zone: %s\n' "${az}"
printf '  instance_id: %s\n' "${instance_id}"
printf '  public_ip: %s\n' "${public_ip}"
printf '  url: https://%s.sslip.io\n' "${public_ip}"
if [ -n "${SSH_KEY_PATH_LOCAL}" ]; then
  printf '  ssh: ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null -i %q ubuntu@%s\n' "${SSH_KEY_PATH_LOCAL}" "${public_ip}"
fi
printf '  teardown: DEPLOYMENT_NAME=%s AWS_REGION=%s ./deploy/aws/down.sh\n' "${DEPLOYMENT_NAME}" "${AWS_REGION}"
printf '  state_file: %s\n' "${STATE_FILE}"
