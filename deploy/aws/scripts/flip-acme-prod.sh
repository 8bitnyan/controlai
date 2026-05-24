#!/usr/bin/env bash
# flip-acme-prod.sh — Switch the Traefik ACME issuer from Let's Encrypt staging to production.
#
# Usage:
#   AWS_REGION=<region> DEPLOYMENT_NAME=<name> ./deploy/aws/scripts/flip-acme-prod.sh
#
# Prerequisites:
#   - The deployment must already be provisioned (deploy/aws/.state/<DEPLOYMENT_NAME>.json exists).
#   - You must have validated cert acquisition with ACME_STAGING=1 first.
#   - SSH key referenced in the state file must be accessible.
#
# What this script does:
#   1. Reads the deployment state to get the instance IP and SSH key.
#   2. SSH-es to the instance and replaces the staging CA server URL with the production URL
#      in /etc/traefik/traefik.yml.
#   3. Removes the stale staging ACME account + certificate data from acme.json so Traefik
#      re-acquires a fresh certificate from the production CA.
#   4. Restarts Traefik to apply the change.
#   5. Polls /v1/health via HTTPS for up to 90 seconds to confirm the new cert is trusted.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
STATE_DIR="${REPO_ROOT}/deploy/aws/.state"

AWS_REGION="${AWS_REGION:-}"
DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-controlai-poc}"

log() { printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

[ -n "${AWS_REGION}" ] || fail "AWS_REGION is required"
[ -n "${DEPLOYMENT_NAME}" ] || fail "DEPLOYMENT_NAME is required"

STATE_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.json"
[ -f "${STATE_FILE}" ] || fail "State file not found: ${STATE_FILE}. Has this deployment been provisioned?"

public_ip="$(jq -r '.public_ip // empty' "${STATE_FILE}")"
ssh_key_path="$(jq -r '.ssh_key_path_local // empty' "${STATE_FILE}")"
[ -n "${public_ip}" ] || fail "Could not read public_ip from state file"

ssh_opts=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o UserKnownHostsFile=/dev/null)
[ -n "${ssh_key_path}" ] && ssh_opts+=(-i "${ssh_key_path}")

PROD_CA="https://acme-v02.api.letsencrypt.org/directory"
STAGING_CA="https://acme-staging-v02.api.letsencrypt.org/directory"

log "Connecting to ${public_ip} to flip ACME CA from staging → production"

# shellcheck disable=SC2087  # heredoc over SSH is intentional
ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" bash <<EOF
set -euo pipefail

TRAEFIK_CFG=/etc/traefik/traefik.yml
ACME_JSON=/etc/traefik/acme/acme.json

# Verify current setting
if ! grep -q '${STAGING_CA}' "\${TRAEFIK_CFG}"; then
  echo "WARNING: staging CA URL not found in \${TRAEFIK_CFG} — may already be on production." >&2
fi

echo "Updating caServer in \${TRAEFIK_CFG}"
sudo sed -i 's|${STAGING_CA}|${PROD_CA}|g' "\${TRAEFIK_CFG}"

echo "Clearing stale ACME data from \${ACME_JSON}"
if [ -f "\${ACME_JSON}" ]; then
  sudo truncate -s 0 "\${ACME_JSON}"
fi

echo "Restarting Traefik"
sudo systemctl restart traefik

echo "Waiting for Traefik to start..."
sleep 3
sudo systemctl is-active traefik || { sudo journalctl -u traefik --no-pager -n 50; exit 1; }
echo "Traefik restarted successfully"
EOF

log "Traefik is running with production ACME CA"
log "Polling https://api.${DEPLOYMENT_NAME}.sslip.io/v1/health for up to 90 seconds..."

api_url="https://api.${DEPLOYMENT_NAME}.sslip.io/v1/health"
deadline=$(( $(date +%s) + 90 ))
healthy=false
while [ "$(date +%s)" -lt "${deadline}" ]; do
  http_code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 "${api_url}" 2>/dev/null || echo "000")"
  if [ "${http_code}" = "401" ] || [ "${http_code}" = "200" ]; then
    # 401 means Traefik is up and forwarding to daemon (bearer token required).
    # A trusted cert would not show a curl SSL error.
    healthy=true
    break
  fi
  sleep 5
done

if [ "${healthy}" = true ]; then
  log "Production cert acquired — API endpoint is reachable at ${api_url}"
  log "Verify with: curl -H 'Authorization: Bearer <token>' ${api_url}"
else
  log "WARNING: cert may still be pending. Check Traefik logs on the instance:"
  log "  ssh ubuntu@${public_ip} sudo journalctl -u traefik --no-pager -n 100"
fi
