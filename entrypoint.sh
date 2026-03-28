#!/bin/sh
set -e

# Vigo server entrypoint — seeds config on first run, then starts vigosrv.
# All persistent state lives under /srv/vigo (bind-mounted from host).

DATA_DIR="/srv/vigo"
VIGO_STAGE="${DATA_DIR}/stack/stage"
VIGO_LIVE="${DATA_DIR}/stack/.live"
EXAMPLES_DIR="${DATA_DIR}/stack/examples"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "${VIGO_STAGE}/modules" "${VIGO_STAGE}/templates"
mkdir -p -m 750 "${VIGO_LIVE}"
mkdir -p "${DATA_DIR}/db" "${DATA_DIR}/config-history" "${DATA_DIR}/stack/tasks" "${DATA_DIR}/custom-traits" "${DATA_DIR}/stack/workflows"
mkdir -p -m 700 "${DATA_DIR}/license" "${DATA_DIR}/secrets" "${DATA_DIR}/tls"

# ---------------------------------------------------------------------------
# Lock down license file permissions if present
# ---------------------------------------------------------------------------

for f in "${DATA_DIR}"/license/*.lic; do
    [ -f "$f" ] && chmod 600 "$f"
done

# ---------------------------------------------------------------------------
# Write default .env if missing
# ---------------------------------------------------------------------------

if [ ! -f "$ENV_FILE" ]; then
    cat > "$ENV_FILE" <<'ENVEOF'
# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║  Vigo — Optional Services                                            ║
# ║  All other configuration belongs in server.yaml.                       ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

# Uncomment to start additional containers alongside vigo.
# Only needed for locally-hosted services. External AI providers (Claude,
# OpenAI) are configured in server.yaml and do not require a container here.
# COMPOSE_PROFILES=ollama              # local Ollama AI (free, CPU-only, ~4GB RAM)
# COMPOSE_PROFILES=postgres            # Postgres instead of SQLite
# COMPOSE_PROFILES=ollama,postgres     # both
ENVEOF
    echo "Default .env created."
fi

# ---------------------------------------------------------------------------
# Copy license and commercial terms (always overwrite — keep current)
# ---------------------------------------------------------------------------

cp "${SEED_DIR}/LICENSE" "${DATA_DIR}/LICENSE"
cp "${SEED_DIR}/COMMERCIAL-TERMS.md" "${DATA_DIR}/COMMERCIAL-TERMS.md"

# ---------------------------------------------------------------------------
# Copy server.yaml if missing
# ---------------------------------------------------------------------------

if [ ! -f "$SERVER_YAML" ]; then
    cp "${SEED_DIR}/server.yaml" "$SERVER_YAML"
    echo "Default server.yaml created."
fi

# ---------------------------------------------------------------------------
# Copy docker-compose.yml if missing
# ---------------------------------------------------------------------------

if [ ! -f "${DATA_DIR}/docker-compose.yml" ]; then
    cp "${SEED_DIR}/docker-compose.yml" "${DATA_DIR}/docker-compose.yml"
    echo "Default docker-compose.yml created."
fi

# ---------------------------------------------------------------------------
# Seed starter config files (skip files that already exist)
# ---------------------------------------------------------------------------

# Starter stack config: common.vgo, roles.vgo, modules/motd.vgo
if [ -d "${SEED_DIR}/stack/stage" ]; then
    find "${SEED_DIR}/stack/stage" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/stack/stage/}"
        dest="${VIGO_STAGE}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
    echo "Starter config files seeded."
fi

# Task definitions: set-password, restart-service, run-command, etc.
if [ -d "${SEED_DIR}/stack/tasks" ]; then
    for src in "${SEED_DIR}"/stack/tasks/*.vgo; do
        [ -f "$src" ] || continue
        dest="${DATA_DIR}/stack/tasks/$(basename "$src")"
        if [ ! -f "$dest" ]; then
            cp "$src" "$dest"
        fi
    done
    echo "Task definitions seeded."
fi

# ---------------------------------------------------------------------------
# Seed example configs into examples/ (skip files that already exist)
# ---------------------------------------------------------------------------

find "${SEED_DIR}/envoy-configs" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/envoy-configs/}"
    # Skip compliance modules and roles — gated behind paid license.
    case "$rel" in
        modules/compliance/*|compliance-roles.vgo.example) continue ;;
    esac
    dest="${EXAMPLES_DIR}/${rel}"
    if [ ! -f "$dest" ]; then
        mkdir -p "$(dirname "$dest")"
        cp "$src" "$dest"
    fi
done

# Documentation stays inside the container at /seed/docs/ (not seeded to
# the bind mount). The UI handler reads from /seed/docs/ directly.
# This prevents docs from being extracted via the host filesystem.

# ---------------------------------------------------------------------------
# Seed CLI test script into bind-mounted volume
# ---------------------------------------------------------------------------

if [ -d "${SEED_DIR}/scripts" ]; then
    mkdir -p "${DATA_DIR}/scripts"
    cp -r "${SEED_DIR}/scripts/." "${DATA_DIR}/scripts/"
    chmod +x "${DATA_DIR}"/scripts/*.sh 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Install CLI tools to host (bind-mounted /usr/local/bin via /host-bin)
# ---------------------------------------------------------------------------

if [ -d /host-bin ]; then
    cp /usr/local/bin/vigogo /host-bin/vigogo
    echo "Installed vigogo to host."
fi

echo "Config seeding complete."

# ---------------------------------------------------------------------------
# Seed-only mode: populate /srv/vigo and exit without starting the server.
# Usage: docker run --rm -v /srv/vigo:/srv/vigo vigo --seed-only
# ---------------------------------------------------------------------------

if [ "${1:-}" = "--seed-only" ]; then
    echo "Seeding complete. Run 'cd /srv/vigo && docker compose up -d' to start."
    exit 0
fi

# ---------------------------------------------------------------------------
# Start vigosrv
# ---------------------------------------------------------------------------

cd "$DATA_DIR"

export VIGO_CONFIG_FILE="server.yaml"
export VIGO_MIGRATIONS_DIR="/migrations"
export VIGO_AGENT_DIR="/dist/agent"

exec vigosrv "$@"
