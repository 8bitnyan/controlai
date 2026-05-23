#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
STATE_DIR="${REPO_ROOT}/deploy/aws/.state"

AWS_REGION="${AWS_REGION:-}"
DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-controlai-poc}"
ASSUME_YES=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes) ASSUME_YES=true ;;
    *) printf 'Unknown flag: %s\n' "$1" >&2; exit 1 ;;
  esac
  shift
done

[ -n "${AWS_REGION}" ] || { printf 'AWS_REGION is required\n' >&2; exit 1; }

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
  if [ "${missing}" -ne 0 ]; then
    exit 1
  fi
}
check_prereqs

STATE_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.json"
USER_DATA_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.user-data"

[ -f "${STATE_FILE}" ] || { printf 'No state file found for deployment %s at %s\n' "${DEPLOYMENT_NAME}" "${STATE_FILE}" >&2; exit 1; }

instance_id="$(jq -r '.instance_id' "${STATE_FILE}")"
public_ip="$(jq -r '.public_ip' "${STATE_FILE}")"
ssh_key_name="$(jq -r '.ssh_key_name' "${STATE_FILE}")"
ssh_key_path_local="$(jq -r '.ssh_key_path_local' "${STATE_FILE}")"
ssh_key_created_by_up="$(jq -r '.ssh_key_created_by_up' "${STATE_FILE}")"
controlai_version="$(jq -r '.controlai_version' "${STATE_FILE}")"
ssm_parameter_name="/controlai/${DEPLOYMENT_NAME}/ca_key"

sg_id="$(aws --region "${AWS_REGION}" ec2 describe-instances --instance-ids "${instance_id}" --query 'Reservations[0].Instances[0].SecurityGroups[0].GroupId' --output text 2>/dev/null || true)"
volume_id="$(aws --region "${AWS_REGION}" ec2 describe-instances --instance-ids "${instance_id}" --query 'Reservations[0].Instances[0].BlockDeviceMappings[0].Ebs.VolumeId' --output text 2>/dev/null || true)"

printf 'About to destroy deployment:\n'
printf '  deployment_name: %s\n' "${DEPLOYMENT_NAME}"
printf '  aws_region: %s\n' "${AWS_REGION}"
printf '  instance_id: %s\n' "${instance_id}"
printf '  public_ip: %s\n' "${public_ip}"
printf '  security_group_id: %s\n' "${sg_id}"
printf '  ssm_parameter_name: %s\n' "${ssm_parameter_name}"
printf '  key_pair_name: %s\n' "${ssh_key_name}"
printf '  ebs_volume_id: %s\n' "${volume_id}"

if [ "${ASSUME_YES}" = false ]; then
  read -r -p "Type the deployment name to confirm: " confirm
  [ "${confirm}" = "${DEPLOYMENT_NAME}" ] || exit 1
fi

TF_DATA_DIR="${STATE_DIR}/.terraform" tofu -chdir="${REPO_ROOT}/deploy/aws/terraform" destroy -state="../.state/terraform.tfstate" -auto-approve -var "aws_region=${AWS_REGION}" -var "deployment_name=${DEPLOYMENT_NAME}" -var 'instance_type=t3.medium' -var 'enable_eip=false' -var "ssh_key_name=${ssh_key_name}" -var "ca_key_ssm_parameter_arn=$(jq -r '.ssm_parameter_arn' "${STATE_FILE}")" -var "controlai_version=${controlai_version}" -var "user_data=$(cat "${USER_DATA_FILE}" 2>/dev/null || true)" -var 'extra_tags={}'

aws --region "${AWS_REGION}" ssm delete-parameter --name "${ssm_parameter_name}" || true

if [ "${ssh_key_created_by_up}" = "true" ]; then
  aws --region "${AWS_REGION}" ec2 delete-key-pair --key-name "${ssh_key_name}"
  [ -n "${ssh_key_path_local}" ] && rm -f "${ssh_key_path_local}"
fi

rm -f "${STATE_FILE}" "${USER_DATA_FILE}"

printf 'Teardown complete for deployment %s in %s\n' "${DEPLOYMENT_NAME}" "${AWS_REGION}"
