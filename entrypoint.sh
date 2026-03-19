#!/bin/sh
set -e

# Hobnob server entrypoint — seeds config on first run, then starts hobd.
# All persistent state lives under /srv/hobnob (bind-mounted from host).

DATA_DIR="/srv/hobnob"
HNB_STAGE="${DATA_DIR}/policy/stage"
HNB_LIVE="${DATA_DIR}/policy/.live"
EXAMPLES_DIR="${DATA_DIR}/policy/examples"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "${HNB_STAGE}/modules" "${HNB_STAGE}/templates"
mkdir -p -m 750 "${HNB_LIVE}"
mkdir -p "${DATA_DIR}/db" "${DATA_DIR}/config-history" "${DATA_DIR}/push" "${DATA_DIR}/custom-traits" "${DATA_DIR}/workflows"
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
# ║  Hobnob — Optional Services                                            ║
# ║  All other configuration belongs in server.yaml.                       ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

# Uncomment to start additional containers alongside hobnob.
# Only needed for locally-hosted services. External AI providers (Claude,
# OpenAI) are configured in server.yaml and do not require a container here.
# COMPOSE_PROFILES=ollama              # local Ollama AI (free, CPU-only, ~4GB RAM)
# COMPOSE_PROFILES=postgres            # Postgres instead of SQLite
# COMPOSE_PROFILES=ollama,postgres     # both
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
# Copy docker-compose.yml if missing
# ---------------------------------------------------------------------------

if [ ! -f "${DATA_DIR}/docker-compose.yml" ]; then
    cp "${SEED_DIR}/docker-compose.yml" "${DATA_DIR}/docker-compose.yml"
    echo "Default docker-compose.yml created."
fi

# ---------------------------------------------------------------------------
# Seed starter config files (skip files that already exist)
# ---------------------------------------------------------------------------

# Starter policy config: common.hnb, roles.hnb, modules/motd.hnb
if [ -d "${SEED_DIR}/policy" ]; then
    find "${SEED_DIR}/policy" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/policy/}"
        dest="${HNB_STAGE}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
    echo "Starter config files seeded."
fi

# Push definitions: set-password, restart-service, run-command, etc.
if [ -d "${SEED_DIR}/push" ]; then
    for src in "${SEED_DIR}"/push/*.yaml; do
        [ -f "$src" ] || continue
        dest="${DATA_DIR}/push/$(basename "$src")"
        if [ ! -f "$dest" ]; then
            cp "$src" "$dest"
        fi
    done
    echo "Push definitions seeded."
fi

# ---------------------------------------------------------------------------
# Seed example configs into examples/ (skip files that already exist)
# ---------------------------------------------------------------------------

find "${SEED_DIR}/thrall-configs" -name '*.hnb.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/thrall-configs/}"
    dest="${EXAMPLES_DIR}/${rel}"
    if [ ! -f "$dest" ]; then
        mkdir -p "$(dirname "$dest")"
        cp "$src" "$dest"
    fi
done

# ---------------------------------------------------------------------------
# Seed documentation into bind-mounted volume
# ---------------------------------------------------------------------------

if [ -d "${SEED_DIR}/docs" ]; then
    mkdir -p "${DATA_DIR}/docs"
    cp -r "${SEED_DIR}/docs/." "${DATA_DIR}/docs/"
fi

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
    cp /usr/local/bin/hobnobcli /host-bin/hobnobcli
    ln -sf hobnobcli /host-bin/hnbvi
    echo "Installed hobnobcli and hnbvi to host."
fi

echo "Config seeding complete."

# ---------------------------------------------------------------------------
# Seed-only mode: populate /srv/hobnob and exit without starting the server.
# Usage: docker run --rm -v /srv/hobnob:/srv/hobnob hobnob --seed-only
# ---------------------------------------------------------------------------

if [ "${1:-}" = "--seed-only" ]; then
    echo "Seeding complete. Run 'cd /srv/hobnob && docker compose up -d' to start."
    exit 0
fi

# ---------------------------------------------------------------------------
# Start hobd
# ---------------------------------------------------------------------------

cd "$DATA_DIR"

export HOBNOB_CONFIG_FILE="server.yaml"
export HOBNOB_MIGRATIONS_DIR="/migrations"
export HOBNOB_AGENT_DIR="/dist/agent"

exec hobd "$@"
