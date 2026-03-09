#!/bin/sh
set -e

# Hobnob server entrypoint — seeds config on first run, then starts hobd.
# All persistent state lives under /etc/hobnob (bind-mounted from host).

DATA_DIR="/etc/hobnob"
SERVER_CONFIGS_DIR="${DATA_DIR}/server-configs"
CERTS_DIR="${SERVER_CONFIGS_DIR}/certs"
NODE_CONFIGS_DIR="${DATA_DIR}/node-configs"
MODULES_DIR="${NODE_CONFIGS_DIR}/modules"
ROLES_DIR="${NODE_CONFIGS_DIR}/roles"
TEMPLATES_DIR="${NODE_CONFIGS_DIR}/templates"
ENV_FILE="${SERVER_CONFIGS_DIR}/.env"
SERVER_YAML="${SERVER_CONFIGS_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "$SERVER_CONFIGS_DIR" "$CERTS_DIR" "$MODULES_DIR" "$ROLES_DIR" "$TEMPLATES_DIR"

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
# server-configs/server.yaml. Every secret:path/to/key maps to an env var:
#   secret:hobnob/db/dsn  ->  HOBNOB_DB_DSN
#   secret:hobnob/tls/cert -> HOBNOB_TLS_CERT
# Rule: uppercase, slashes become underscores.
#
# REQUIRED — server will not start without these:
# =============================================================================

HOBNOB_DB_DSN=postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable
HOBNOB_TLS_CERT=server-configs/certs/server.crt
HOBNOB_TLS_KEY=server-configs/certs/server.key
HOBNOB_TLS_CA=server-configs/certs/ca.crt
HOBNOB_TLS_CA_KEY=server-configs/certs/ca.key

# =============================================================================
# OPTIONAL — uncomment and edit as needed:
# =============================================================================

# Server config file path (default: server-configs/server.yaml)
# HOBNOB_CONFIG_FILE=server-configs/server.yaml

# Node configs directory (default: node-configs)
# HOBNOB_NODE_CONFIGS_DIR=node-configs

# Migrations directory (default: migrations)
# HOBNOB_MIGRATIONS_DIR=migrations

# Server version string reported to agents for self-update (default: dev)
# HOBNOB_VERSION=1.0.0

# Directory containing cross-compiled agent binaries for distribution
# Used by GET /api/v1/agent/download/{os}/{arch} (default: dist/agent)
# HOBNOB_AGENT_DIR=dist/agent

# Webhook secret for HMAC-SHA256 payload signing
# HOBNOB_WEBHOOK_SECRET=change-me-to-a-random-string

# .env file path override (default: server-configs/.env)
# HOBNOB_ENV_FILE=server-configs/.env

# =============================================================================
# MODULE SECRETS — uncomment when enabling the corresponding node entries.
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
# Seed example configs (skip files that already exist)
# ---------------------------------------------------------------------------

# clients.hob.example
if [ ! -f "${NODE_CONFIGS_DIR}/clients.hob.example" ]; then
    cp "${SEED_DIR}/node-configs/clients.hob.example" "${NODE_CONFIGS_DIR}/"
fi

# Module examples
for src in "${SEED_DIR}/node-configs/modules/"*.hob.example; do
    [ -f "$src" ] || continue
    dest="${MODULES_DIR}/$(basename "$src")"
    [ -f "$dest" ] || cp "$src" "$dest"
done

# Role examples
for src in "${SEED_DIR}/node-configs/roles/"*.hob.example; do
    [ -f "$src" ] || continue
    dest="${ROLES_DIR}/$(basename "$src")"
    [ -f "$dest" ] || cp "$src" "$dest"
done

# Template files
if [ -d "${SEED_DIR}/node-configs/templates" ]; then
    find "${SEED_DIR}/node-configs/templates" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/node-configs/templates/}"
        dest="${TEMPLATES_DIR}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
fi

echo "Config seeding complete."

# ---------------------------------------------------------------------------
# Export env vars and start hobd
# ---------------------------------------------------------------------------

# Source .env so hobd sees the variables.
set -a
. "$ENV_FILE"
set +a

# Run from /etc/hobnob so relative paths in .env and server.yaml resolve correctly.
cd "$DATA_DIR"

export HOBNOB_CONFIG_FILE="server-configs/server.yaml"
export HOBNOB_NODE_CONFIGS_DIR="node-configs"
export HOBNOB_MIGRATIONS_DIR="/migrations"

exec hobd "$@"
