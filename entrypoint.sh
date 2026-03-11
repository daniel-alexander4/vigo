#!/bin/sh
set -e

# Hobnob server entrypoint — seeds config on first run, then starts hobd.
# All persistent state lives under /etc/hobnob-server (bind-mounted from host).

DATA_DIR="/etc/hobnob-server"
CERTS_DIR="${DATA_DIR}/certs"
THRALLS_DIR="${DATA_DIR}/thralls"
MODULES_DIR="${DATA_DIR}/modules"
ROLES_DIR="${DATA_DIR}/roles"
TEMPLATES_DIR="${DATA_DIR}/templates"
EXAMPLES_DIR="${DATA_DIR}/examples"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "$CERTS_DIR" "$THRALLS_DIR" "$MODULES_DIR" "$ROLES_DIR" "$TEMPLATES_DIR"
mkdir -p -m 700 "${DATA_DIR}/license"

# ---------------------------------------------------------------------------
# Lock down license file permissions if present
# ---------------------------------------------------------------------------

for f in "${DATA_DIR}"/license/*.lic; do
    [ -f "$f" ] && chmod 600 "$f"
done

# ---------------------------------------------------------------------------
# Generate TLS certificates if missing
# ---------------------------------------------------------------------------

if [ ! -f "${CERTS_DIR}/ca.key" ] || [ ! -f "${CERTS_DIR}/server.crt" ]; then
    echo "Generating TLS certificates..."
    openssl genrsa -out "${CERTS_DIR}/ca.key" 4096 2>/dev/null
    openssl req -new -x509 -key "${CERTS_DIR}/ca.key" -out "${CERTS_DIR}/ca.crt" -days 3650 \
        -subj "/CN=Hobnob CA" 2>/dev/null
    openssl genrsa -out "${CERTS_DIR}/server.key" 4096 2>/dev/null
    openssl req -new -key "${CERTS_DIR}/server.key" -out "${CERTS_DIR}/server.csr" \
        -subj "/CN=hobnob" \
        -addext "subjectAltName=DNS:localhost,DNS:hobnob,DNS:server,IP:127.0.0.1" 2>/dev/null
    openssl x509 -req -in "${CERTS_DIR}/server.csr" \
        -CA "${CERTS_DIR}/ca.crt" -CAkey "${CERTS_DIR}/ca.key" \
        -CAcreateserial -out "${CERTS_DIR}/server.crt" -days 365 \
        -copy_extensions copyall 2>/dev/null
    rm -f "${CERTS_DIR}/server.csr" "${CERTS_DIR}/ca.srl"
    echo "TLS certificates generated."
fi

# ---------------------------------------------------------------------------
# Write default .env if missing
# ---------------------------------------------------------------------------

if [ ! -f "$ENV_FILE" ]; then
    cat > "$ENV_FILE" <<'ENVEOF'
# =============================================================================
# Hobnob Environment Configuration
# =============================================================================
# The StaticProvider reads these values to resolve secret: references in
# server.yaml. Every secret:path/to/key maps to an env var:
#   secret:hobnob/db/dsn  ->  HOBNOB_DB_DSN
#   secret:hobnob/tls/cert -> HOBNOB_TLS_CERT
# Rule: uppercase, slashes become underscores.
#
# REQUIRED — server will not start without these:
# =============================================================================

HOBNOB_DB_DSN=hobnob.db
HOBNOB_TLS_CERT=certs/server.crt
HOBNOB_TLS_KEY=certs/server.key
HOBNOB_TLS_CA=certs/ca.crt
HOBNOB_TLS_CA_KEY=certs/ca.key

# =============================================================================
# OPTIONAL — uncomment and edit as needed:
# =============================================================================

# Server config file path (default: server.yaml)
# HOBNOB_CONFIG_FILE=server.yaml

# Config directory for modules, roles, thralls (default: . i.e. same as server dir)
# HOBNOB_NODE_CONFIGS_DIR=.

# Migrations directory (default: migrations)
# HOBNOB_MIGRATIONS_DIR=migrations

# Server version string reported to agents for self-update (default: dev)
# HOBNOB_VERSION=1.0.0

# Directory containing cross-compiled agent binaries for distribution
# Used by GET /api/v1/agent/download/{os}/{arch} (default: dist/agent)
# HOBNOB_AGENT_DIR=dist/agent

# Webhook secret for HMAC-SHA256 payload signing
# HOBNOB_WEBHOOK_SECRET=change-me-to-a-random-string

# .env file path override (default: .env)
# HOBNOB_ENV_FILE=.env

# =============================================================================
# MODULE SECRETS — uncomment when enabling the corresponding thrall entries.
# Each maps to a secret: reference in node config vars.
# =============================================================================

# HOBNOB_REDIS_PASSWORD=changeme
# HOBNOB_HAPROXY_STATS_PASSWORD=changeme
# HOBNOB_MYSQL_ROOT_PASSWORD=changeme
ENVEOF
    echo "Default .env created."
fi

# ---------------------------------------------------------------------------
# Copy server.yaml if missing
# ---------------------------------------------------------------------------

if [ ! -f "$SERVER_YAML" ]; then
    cp "${SEED_DIR}/server.yaml" "$SERVER_YAML"
    echo "Default server.yaml created."
fi

# ---------------------------------------------------------------------------
# Seed example configs into examples/ (skip files that already exist)
# ---------------------------------------------------------------------------

mkdir -p "${EXAMPLES_DIR}/modules" "${EXAMPLES_DIR}/thralls" "${EXAMPLES_DIR}/roles" "${EXAMPLES_DIR}/templates"

# Thrall mapping examples
for src in "${SEED_DIR}/node-configs/thralls/"*.hnb.example; do
    [ -f "$src" ] || continue
    dest="${EXAMPLES_DIR}/thralls/$(basename "$src")"
    [ -f "$dest" ] || cp "$src" "$dest"
done

# Module examples
for src in "${SEED_DIR}/node-configs/modules/"*.hnb.example; do
    [ -f "$src" ] || continue
    dest="${EXAMPLES_DIR}/modules/$(basename "$src")"
    [ -f "$dest" ] || cp "$src" "$dest"
done

# Role examples
for src in "${SEED_DIR}/node-configs/roles/"*.hnb.example; do
    [ -f "$src" ] || continue
    dest="${EXAMPLES_DIR}/roles/$(basename "$src")"
    [ -f "$dest" ] || cp "$src" "$dest"
done

# Template examples
if [ -d "${SEED_DIR}/node-configs/templates" ]; then
    find "${SEED_DIR}/node-configs/templates" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/node-configs/templates/}"
        dest="${EXAMPLES_DIR}/templates/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
fi

# ---------------------------------------------------------------------------
# Install CLI tools to host (bind-mounted /usr/local/bin via /host-bin)
# ---------------------------------------------------------------------------

if [ -d /host-bin ]; then
    cp /usr/local/bin/hobnobcli /host-bin/hobnobcli
    ln -sf hobnobcli /host-bin/hnbvi
    echo "Installed hobnobcli and hnbvi to host."
fi

echo "Config seeding complete."

# ---------------------------------------------------------------------------
# Export env vars and start hobd
# ---------------------------------------------------------------------------

# Source .env so hobd sees the variables.
set -a
. "$ENV_FILE"
set +a

# Run from /etc/hobnob-server so relative paths in .env and server.yaml resolve correctly.
cd "$DATA_DIR"

export HOBNOB_CONFIG_FILE="server.yaml"
export HOBNOB_NODE_CONFIGS_DIR="."
export HOBNOB_MIGRATIONS_DIR="/migrations"
export HOBNOB_AGENT_DIR="/dist/agent"

exec hobd "$@"
