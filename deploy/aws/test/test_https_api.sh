#!/usr/bin/env bash
# test_https_api.sh — Integration test for the controlai daemon HTTPS API endpoint.
#
# Asserts:
#   1. HTTPS endpoint is reachable at api.<DEPLOYMENT_NAME>.sslip.io
#   2. TLS certificate is valid (Let's Encrypt — not expired, not self-signed staging unless --allow-staging)
#   3. GET /v1/health with a valid bearer token returns HTTP 200
#   4. GET /v1/health without a bearer token returns HTTP 401
#
# Usage:
#   AWS_REGION=<region> DEPLOYMENT_NAME=<name> BEARER_TOKEN=<token> \
#     ./deploy/aws/test/test_https_api.sh [--allow-staging]
#
# If BEARER_TOKEN is not set, the script SSH-es to the host to create and then revoke
# a transient smoke-test token automatically.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
STATE_DIR="${REPO_ROOT}/deploy/aws/.state"

DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-controlai-poc}"
BEARER_TOKEN="${BEARER_TOKEN:-}"
ALLOW_STAGING=false

log()  { printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --allow-staging) ALLOW_STAGING=true ;;
    *) fail "unknown flag: $1" ;;
  esac
  shift
done

STATE_FILE="${STATE_DIR}/${DEPLOYMENT_NAME}.json"
[ -f "${STATE_FILE}" ] || fail "State file not found: ${STATE_FILE}"

public_ip="$(jq -r '.public_ip // empty' "${STATE_FILE}")"
ssh_key_path="$(jq -r '.ssh_key_path_local // empty' "${STATE_FILE}")"
[ -n "${public_ip}" ] || fail "Could not read public_ip from state file"

API_BASE="https://api.${DEPLOYMENT_NAME}.sslip.io"
HEALTH_URL="${API_BASE}/v1/health"

ssh_opts=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o UserKnownHostsFile=/dev/null)
[ -n "${ssh_key_path}" ] && ssh_opts+=(-i "${ssh_key_path}")

# ─── Token management ────────────────────────────────────────────────────────

TOKEN_WAS_CREATED=false
if [ -z "${BEARER_TOKEN}" ]; then
  log "No BEARER_TOKEN set — creating transient smoke-test token via SSH"
  BEARER_TOKEN="$(ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" \
    "sudo -u controlai controlai token create smoke-test 2>/dev/null || controlai token create smoke-test" 2>/dev/null | tail -1)"
  TOKEN_WAS_CREATED=true
fi

cleanup() {
  if [ "${TOKEN_WAS_CREATED}" = true ]; then
    log "Revoking transient smoke-test token"
    ssh "${ssh_opts[@]}" ubuntu@"${public_ip}" \
      "sudo -u controlai controlai token revoke smoke-test 2>/dev/null || controlai token revoke smoke-test" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ─── TLS / reachability check ────────────────────────────────────────────────

log "Test 1: HTTPS endpoint reachable with valid TLS certificate"
curl_tls_opts=(--max-time 30 --silent --output /dev/null --write-out '%{http_code}')
if [ "${ALLOW_STAGING}" = true ]; then
  curl_tls_opts+=(--insecure)
  log "  (--allow-staging: skipping cert trust validation)"
fi

http_code="$(curl "${curl_tls_opts[@]}" "${HEALTH_URL}" 2>/dev/null || echo "000")"
if [ "${http_code}" = "401" ] || [ "${http_code}" = "200" ]; then
  pass "HTTPS endpoint reachable (HTTP ${http_code} — Traefik is up and forwarding)"
elif [ "${http_code}" = "000" ]; then
  fail "HTTPS endpoint not reachable — curl returned no response (connection refused or TLS error). Check Traefik status and SG rules."
else
  fail "Unexpected HTTP ${http_code} from ${HEALTH_URL} (expected 200 or 401)"
fi

# ─── Authenticated request ────────────────────────────────────────────────────

log "Test 2: GET /v1/health with bearer token returns HTTP 200"
auth_curl_opts=(--max-time 30 --silent --write-out '%{http_code}' --output /tmp/controlai_health_body.json -H "Authorization: Bearer ${BEARER_TOKEN}")
[ "${ALLOW_STAGING}" = true ] && auth_curl_opts+=(--insecure)

http_code="$(curl "${auth_curl_opts[@]}" "${HEALTH_URL}" 2>/dev/null || echo "000")"
if [ "${http_code}" = "200" ]; then
  response_body="$(cat /tmp/controlai_health_body.json 2>/dev/null || echo "")"
  pass "Authenticated /v1/health → HTTP 200"
  log "  Response body: ${response_body}"
elif [ "${http_code}" = "000" ]; then
  fail "No response from ${HEALTH_URL} with bearer token"
else
  response_body="$(cat /tmp/controlai_health_body.json 2>/dev/null || echo "")"
  fail "Authenticated /v1/health → HTTP ${http_code} (expected 200). Body: ${response_body}"
fi

# ─── Unauthenticated request ─────────────────────────────────────────────────

log "Test 3: GET /v1/health without bearer token returns HTTP 401"
noauth_curl_opts=(--max-time 30 --silent --write-out '%{http_code}' --output /tmp/controlai_noauth_body.json)
[ "${ALLOW_STAGING}" = true ] && noauth_curl_opts+=(--insecure)

http_code="$(curl "${noauth_curl_opts[@]}" "${HEALTH_URL}" 2>/dev/null || echo "000")"
if [ "${http_code}" = "401" ]; then
  noauth_body="$(cat /tmp/controlai_noauth_body.json 2>/dev/null || echo "")"
  pass "Unauthenticated /v1/health → HTTP 401 (bearer token correctly required)"
  log "  Error body: ${noauth_body}"
else
  fail "Unauthenticated /v1/health → HTTP ${http_code} (expected 401)"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────

printf '\n'
printf '========================================\n'
printf ' API endpoint: HEALTHY\n'
printf ' URL: %s\n' "${HEALTH_URL}"
printf '========================================\n'
