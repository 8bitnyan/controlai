#!/usr/bin/env bash
# controlai one-shot operator install script.
# Run as root on the target EC2 host.
#
# Usage:
#   curl -fsSL https://example.com/install.sh | sudo bash
#   # or
#   sudo bash install.sh
#
# Environment variables:
#   CONTROLAI_VERSION   — release tag to download (default: latest)
#   CONTROLAI_BINARY    — path to a local binary (skips download)
#   CONTROLAI_DATA_DIR  — data root (default: /var/lib/controlai)

set -euo pipefail

CONTROLAI_BINARY="${CONTROLAI_BINARY:-}"
CONTROLAI_DATA_DIR="${CONTROLAI_DATA_DIR:-/var/lib/controlai}"
INSTALL_DIR="/usr/local/bin"
SYSTEMD_DIR="/etc/systemd/system"
ENV_DIR="/etc/controlai"
BACKUP_DIR="/var/backups/controlai"

# ── helpers ───────────────────────────────────────────────────────────────────

info()  { echo "[controlai-install] INFO  $*"; }
warn()  { echo "[controlai-install] WARN  $*" >&2; }
error() { echo "[controlai-install] ERROR $*" >&2; exit 1; }

require_root() {
  [[ "$(id -u)" -eq 0 ]] || error "This script must be run as root."
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || error "Required command not found: $1"
}

# ── preflight ─────────────────────────────────────────────────────────────────

require_root
require_cmd docker
require_cmd systemctl

# ── install binary ────────────────────────────────────────────────────────────

if [[ -n "${CONTROLAI_BINARY}" ]]; then
  info "Installing local binary: ${CONTROLAI_BINARY}"
  install -m 0755 -o root -g root "${CONTROLAI_BINARY}" "${INSTALL_DIR}/controlai"
else
  CONTROLAI_VERSION="${CONTROLAI_VERSION:-latest}"
  info "Downloading controlai ${CONTROLAI_VERSION} ..."
  warn "Automated binary download not configured in this build."
  warn "Set CONTROLAI_BINARY=/path/to/controlai and re-run."
  error "No binary source configured."
fi

# ── system user ───────────────────────────────────────────────────────────────

info "Creating controlai system user and group..."
groupadd --system controlai 2>/dev/null || true
useradd  --system --no-create-home \
         --gid controlai \
         --shell /usr/sbin/nologin \
         --comment "controlai daemon" \
         controlai 2>/dev/null || true

# Add controlai user to docker group so it can reach the socket.
usermod -aG docker controlai 2>/dev/null || true

# ── directories ───────────────────────────────────────────────────────────────

info "Creating data directories..."
for d in \
    "${CONTROLAI_DATA_DIR}" \
    "${CONTROLAI_DATA_DIR}/tenants" \
    "${CONTROLAI_DATA_DIR}/shared" \
    "${BACKUP_DIR}" \
    "${ENV_DIR}"; do
  mkdir -p "$d"
done

chown -R controlai:controlai "${CONTROLAI_DATA_DIR}"
chmod  0750 "${CONTROLAI_DATA_DIR}"
chown -R controlai:controlai "${BACKUP_DIR}"
chmod  0750 "${BACKUP_DIR}"

# Only root reads /etc/controlai/env (contains the master key env var).
chown root:controlai "${ENV_DIR}"
chmod 0750 "${ENV_DIR}"

# ── env file ─────────────────────────────────────────────────────────────────

ENV_FILE="${ENV_DIR}/env"
if [[ ! -f "${ENV_FILE}" ]]; then
  info "Writing template env file: ${ENV_FILE}"
  cat > "${ENV_FILE}" <<'EOF'
# controlai daemon environment variables.
# Uncomment and fill in the master CA key encryption key (required in production).
# CONTROLAI_CA_KEY_ENCRYPTION_KEY=<64-hex-chars>
EOF
  chmod 0640 "${ENV_FILE}"
  chown root:controlai "${ENV_FILE}"
fi

# ── systemd units ─────────────────────────────────────────────────────────────

info "Installing systemd unit files..."
# The service unit is installed by the binary's `controlai install` command;
# this script also ships the unit files directly for operator convenience.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYSTEMD_SRC="$(dirname "${SCRIPT_DIR}")/systemd"

if [[ -d "${SYSTEMD_SRC}" ]]; then
  for unit_file in "${SYSTEMD_SRC}"/*.service "${SYSTEMD_SRC}"/*.timer; do
    [[ -f "${unit_file}" ]] || continue
    cp "${unit_file}" "${SYSTEMD_DIR}/"
    info "  installed $(basename "${unit_file}")"
  done
else
  # Fallback: let the binary write the unit files.
  "${INSTALL_DIR}/controlai" install || true
fi

info "Reloading systemd and enabling controlai.service..."
systemctl daemon-reload
systemctl enable controlai.service

# ── done ─────────────────────────────────────────────────────────────────────

info ""
info "controlai installed successfully."
info ""
info "Next steps:"
info "  1. Set CONTROLAI_CA_KEY_ENCRYPTION_KEY in ${ENV_FILE} (64 hex chars)"
info "  2. Start the daemon:    systemctl start controlai"
info "  3. Verify it's running: systemctl status controlai"
info "  4. Initialize shared Traefik infra: controlai shared init --domain your.domain.tld"
info "  5. Create your first tenant: controlai tenant create acme-corp --domain acme.your.domain.tld"
info ""
info "  View logs: journalctl -u controlai -f"
