#!/bin/sh
set -e

# Hobnob server entrypoint — seeds config on first run, then starts hobd.
# All persistent state lives under /data (mounted as a Docker volume).

DATA_DIR="/data"
CERTS_DIR="${DATA_DIR}/certs"
NODE_CONFIGS_DIR="${DATA_DIR}/node-configs"
MODULES_DIR="${NODE_CONFIGS_DIR}/modules"
ROLES_DIR="${NODE_CONFIGS_DIR}/roles"
TEMPLATES_DIR="${NODE_CONFIGS_DIR}/templates"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "$CERTS_DIR" "$MODULES_DIR" "$ROLES_DIR" "$TEMPLATES_DIR"

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
# Hobnob Environment — resolve secret: references in server.yaml.
# Rule: secret:hobnob/db/dsn -> HOBNOB_DB_DSN (uppercase, slashes to underscores).

HOBNOB_DB_DSN=postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable
HOBNOB_TLS_CERT=certs/server.crt
HOBNOB_TLS_KEY=certs/server.key
HOBNOB_TLS_CA=certs/ca.crt
HOBNOB_TLS_CA_KEY=certs/ca.key
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

# Run from /data so relative paths in .env (e.g. certs/server.crt) resolve correctly.
cd "$DATA_DIR"

export HOBNOB_CONFIG_FILE="server.yaml"
export HOBNOB_NODE_CONFIGS_DIR="node-configs"

exec hobd "$@"
