#!/bin/sh
set -e

# Vigo server entrypoint — seeds config on first run, then starts vigosrv.
# All persistent state lives under /srv/vigo (bind-mounted from host).

DATA_DIR="/srv/vigo"
VIGO_STACKS="${DATA_DIR}/stacks"             # operator config tree (the gitback "stacks" project)
VIGO_LIVE="${DATA_DIR}/.live"                # validated output the server reads
EXAMPLES_DIR="${DATA_DIR}/stacks-examples"   # read-only reference templates
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "${VIGO_STACKS}/configcrates" "${VIGO_STACKS}/templates" "${VIGO_STACKS}/tasks" "${VIGO_STACKS}/workflows" "${VIGO_STACKS}/usercrates"
mkdir -p -m 750 "${VIGO_LIVE}"
mkdir -p "${DATA_DIR}/db" "${DATA_DIR}/config-history" "${DATA_DIR}/custom-traits"
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
# Refresh docker-compose.yml from seed (always)
# ---------------------------------------------------------------------------
#
# Unconditional overwrite — this file ships with image-registry paths,
# service definitions, and compose-schema constructs that change with
# the vigosrv release. A stale copy from a prior version (e.g. referencing
# ghcr.io before the AR migration) prevents `docker compose up -d` from
# pulling the right image entirely.
#
# Operator customization must happen via /srv/vigo/.env (COMPOSE_PROFILES),
# not by editing this file. If someone does hand-edit, the cmp check
# below surfaces a one-line log so the refresh isn't silent.

if ! cmp -s "${SEED_DIR}/docker-compose.yml" "${DATA_DIR}/docker-compose.yml" 2>/dev/null; then
    if [ -f "${DATA_DIR}/docker-compose.yml" ]; then
        echo "docker-compose.yml differed from seed — refreshing."
    else
        echo "Default docker-compose.yml created."
    fi
    cp "${SEED_DIR}/docker-compose.yml" "${DATA_DIR}/docker-compose.yml"
fi

# ---------------------------------------------------------------------------
# Seed starter config files (skip files that already exist)
# Seed layout (per repo): /seed/stacks/ mirrors the operator config tree
# (configcrates/, usercrates/, templates/, tasks/, workflows/, roles.vgo …)
# and lands directly in /srv/vigo/stacks/.
# ---------------------------------------------------------------------------

if [ -d "${SEED_DIR}/stacks" ]; then
    find "${SEED_DIR}/stacks" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/stacks/}"
        dest="${VIGO_STACKS}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
fi
echo "Starter config files seeded."

# Initial publish: sync stacks/ into .live/ on first run so the server has
# config available at startup. Subsequent changes go through vigocli config
# publish. roles.vgo is the canonical file every published .live/ carries,
# so its absence is a reliable "this tree is uninitialized" marker; the
# trailing /. on the source copies stacks's CONTENTS into .live/ flat.
if [ ! -f "${VIGO_LIVE}/roles.vgo" ] && [ -f "${VIGO_STACKS}/roles.vgo" ]; then
    mkdir -p "${VIGO_LIVE}"
    cp -r "${VIGO_STACKS}"/. "${VIGO_LIVE}/"
    chmod -R u=rwX,g=rX,o= "${VIGO_LIVE}"
    echo "Initial config published to .live/."
fi

# ---------------------------------------------------------------------------
# Rebuild stacks-examples/ from the image seed
# ---------------------------------------------------------------------------
#
# A read-only reference tree — operators copy *from* it via
# `vigocli config examples copy`, never edit it in place. Rebuilding from
# /seed/ on every startup guarantees the tree matches the running image.
# Anything custom an operator dropped here is removed; this is by design —
# the tree is not for operator content.

rm -rf "${EXAMPLES_DIR}"
mkdir -p "${EXAMPLES_DIR}"

# The seed tree (example-configs/stacks/) mirrors the operator stacks/
# layout one-to-one, so `vigocli config examples copy <name>` writes
# destinations relative to stacks/ directly.
find "${SEED_DIR}/example-configs/stacks" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/example-configs/stacks/}"
    # Skip compliance configcrates — gated behind paid license.
    case "$rel" in
        configcrates/compliance/*) continue ;;
    esac
    dest="${EXAMPLES_DIR}/${rel}"
    mkdir -p "$(dirname "$dest")"
    cp "$src" "$dest"
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
    cp /usr/local/bin/vigocli /host-bin/vigocli
    chown root:root /host-bin/vigocli
    chmod 0700 /host-bin/vigocli
    echo "Installed vigocli to host (root-only, 0700)."
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
