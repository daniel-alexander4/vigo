#!/bin/sh
set -eu

# Hobnob Agent Bootstrap Script
# ==============================
# Self-contained script that installs the hobnob agent on a new node,
# bootstraps it with the server (generates keys, CSR, receives signed certs),
# and starts the agent as a systemd service.
#
# Usage:
#   curl -sSf http://hobd.example.com:8181/bootstrap | sh
#
#   # or with a token for non-trusted enrollment:
#   curl -sSf "http://hobd.example.com:8181/bootstrap?token=TOKEN" | sh
#
#   # or run directly with environment variables:
#   HOBNOB_SERVER=http://hobd.example.com:8181 sh bootstrap.sh
#   HOBNOB_SERVER=http://hobd.example.com:8181 HOBNOB_TOKEN=abc123 sh bootstrap.sh

# Configuration (override with environment variables).
SERVER_ADDR="${HOBNOB_SERVER:-}"
TOKEN="${HOBNOB_TOKEN:-}"
INSTALL_DIR="${HOBNOB_INSTALL_DIR:-/usr/local/bin}"
DATA_DIR="${HOBNOB_DATA_DIR:-/etc/hobnob-node}"
GRPC_PORT="${HOBNOB_GRPC_PORT:-1530}"

# ---- Helpers ----

die() {
    echo "ERROR: $*" >&2
    exit 1
}

need_cmd() {
    command -v "$1" > /dev/null 2>&1 || die "required command not found: $1"
}

# ---- Preflight checks ----

[ "$(id -u)" -eq 0 ] || die "this script must be run as root"

need_cmd curl
need_cmd uname

if [ -z "$SERVER_ADDR" ]; then
    die "HOBNOB_SERVER is not set. Usage: HOBNOB_SERVER=http://hobd:8181 sh bootstrap.sh"
fi

# ---- Detect platform ----

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)         ARCH="amd64" ;;
    aarch64|arm64)  ARCH="arm64" ;;
    *)              die "unsupported architecture: $ARCH" ;;
esac

case "$OS" in
    linux|darwin) ;;
    *)            die "unsupported OS: $OS" ;;
esac

echo "Platform:  ${OS}/${ARCH}"
echo "Server:    ${SERVER_ADDR}"
echo "Data dir:  ${DATA_DIR}"
echo ""

# ---- Create directories ----

mkdir -p "$DATA_DIR"

# ---- Download agent binary ----

echo "Downloading hobnob agent binary..."
DOWNLOAD_URL="${SERVER_ADDR}/api/v1/agent/download/${OS}/${ARCH}"
HTTP_CODE=$(curl -sSf -w '%{http_code}' -o "${INSTALL_DIR}/hobnob" "$DOWNLOAD_URL" 2>/dev/null) || true

if [ ! -f "${INSTALL_DIR}/hobnob" ] || [ "$HTTP_CODE" != "200" ]; then
    die "failed to download agent binary from ${DOWNLOAD_URL} (HTTP ${HTTP_CODE})"
fi
chmod +x "${INSTALL_DIR}/hobnob"
echo "Installed: ${INSTALL_DIR}/hobnob"

# ---- Bootstrap agent (generates keys, CSR, registers, receives certs) ----

echo "Bootstrapping agent..."
BOOTSTRAP_CMD="${INSTALL_DIR}/hobnob bootstrap --server ${SERVER_ADDR} --data-dir ${DATA_DIR}"
if [ -n "$TOKEN" ]; then
    BOOTSTRAP_CMD="${BOOTSTRAP_CMD} --token ${TOKEN}"
fi

eval "$BOOTSTRAP_CMD" || die "bootstrap failed"

# ---- Extract gRPC host ----

GRPC_HOST="$(echo "$SERVER_ADDR" | sed 's|^https\?://||' | sed 's|:[0-9]*$||')"

# ---- Install systemd service ----

if command -v systemctl > /dev/null 2>&1; then
    cat > /etc/systemd/system/hobnob-node.service << EOF
[Unit]
Description=Hobnob Configuration Management Agent
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/hobnob --server ${GRPC_HOST}:${GRPC_PORT} --data-dir ${DATA_DIR}
Restart=always
RestartSec=10
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable hobnob-node
    systemctl start hobnob-node
    echo ""
    echo "Hobnob agent service installed and started."
    echo "  systemctl status hobnob-node    # check status"
    echo "  journalctl -u hobnob-node -f    # follow logs"
else
    echo ""
    echo "systemd not found. Start the agent manually:"
    echo "  ${INSTALL_DIR}/hobnob --server ${GRPC_HOST}:${GRPC_PORT} --data-dir ${DATA_DIR}"
fi

echo ""
echo "Bootstrap complete."
