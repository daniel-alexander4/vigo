#!/bin/sh
set -e

# Vigo server entrypoint — seeds config on first run, then starts vigosrv.
# All persistent state lives under /srv/vigo (bind-mounted from host).

DATA_DIR="/srv/vigo"
VIGO_WORKSITE="${DATA_DIR}/worksite"
VIGO_STOCKPILE="${VIGO_WORKSITE}/stockpile"
VIGO_STRUCTURE="${VIGO_WORKSITE}/structure"
EXAMPLES_DIR="${DATA_DIR}/example-stockpile"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# One-time migration to worksite/ layout (0.28.0)
# ---------------------------------------------------------------------------
# Pre-0.28 layout: /srv/vigo/stockpile/ (with nested .live/).
# New layout:      /srv/vigo/worksite/{stockpile, structure}/ as siblings.
# Move the whole tree if the old dir exists and the new dir doesn't.
# Refuse if BOTH exist to avoid silent merge (operator must resolve).

if [ -d "${DATA_DIR}/stockpile" ] && [ -d "${VIGO_WORKSITE}" ]; then
    echo "ERROR: both legacy /srv/vigo/stockpile and new /srv/vigo/worksite exist." >&2
    echo "       Refusing to auto-migrate. Resolve manually and restart." >&2
    exit 1
fi

if [ -d "${DATA_DIR}/stockpile" ] && [ ! -d "${VIGO_WORKSITE}" ]; then
    mkdir -p "${VIGO_WORKSITE}"
    # Move nested .live/ out to structure/ first while the old tree is intact.
    if [ -d "${DATA_DIR}/stockpile/.live" ]; then
        mv "${DATA_DIR}/stockpile/.live" "${VIGO_STRUCTURE}"
    fi
    # Then move the rest (modules, templates, envoys.vgo, roles.vgo, etc.).
    mv "${DATA_DIR}/stockpile" "${VIGO_STOCKPILE}"
    echo "Migrated /srv/vigo/stockpile/ → /srv/vigo/worksite/stockpile/ (.live → structure/)."
fi

# ---------------------------------------------------------------------------
# Phase 2 migration: flat stockpile → stockpile + scaffolding split (0.29.0)
# ---------------------------------------------------------------------------
# Pre-0.29 layout: worksite/stockpile/ flat (modules/, envoys.vgo, roles.vgo,
#                  common.vgo, compliance.vgo holding waivers, etc.)
# New layout:      worksite/stockpile/ holds module library (stones/,
#                  usercrates/, templates/, compliance.vgo claims, tasks/,
#                  workflows/) and worksite/scaffolding/ holds envoy rules
#                  (envoys.vgo, roles.vgo, common.vgo, waivers.vgo,
#                  environments.vgo, customer subdirs).
#
# Detected by `worksite/stockpile/modules/` existing (old flat layout).
# Safe to run once — after the split, modules/ is gone from stockpile/.

if [ -d "${VIGO_STOCKPILE}/modules" ] && [ ! -d "${VIGO_STOCKPILE}/stones" ]; then
    mkdir -p "${VIGO_WORKSITE}/scaffolding"

    # Rename the module library: modules/ → stones/
    mv "${VIGO_STOCKPILE}/modules" "${VIGO_STOCKPILE}/stones"

    # Files that live directly in stockpile/ and belong in scaffolding/.
    for f in envoys.vgo roles.vgo common.vgo environments.vgo; do
        if [ -f "${VIGO_STOCKPILE}/${f}" ]; then
            mv "${VIGO_STOCKPILE}/${f}" "${VIGO_WORKSITE}/scaffolding/${f}"
        fi
    done

    # compliance.vgo containing waivers: becomes scaffolding/waivers.vgo.
    # We key on the `waivers:` block appearing in the file — if present,
    # the file is a waiver file and moves; if only `compliance:`, it's
    # a claim file and stays.
    if [ -f "${VIGO_STOCKPILE}/compliance.vgo" ]; then
        if grep -q '^waivers:' "${VIGO_STOCKPILE}/compliance.vgo"; then
            mv "${VIGO_STOCKPILE}/compliance.vgo" "${VIGO_WORKSITE}/scaffolding/waivers.vgo"
        fi
        # Otherwise leave in stockpile/ as the claim file.
    fi

    # Customer/site subdirs (anything other than the module-library subdirs)
    # move under scaffolding/. A subdir is "library" if it matches one of
    # stones, usercrates, templates, tasks, workflows — otherwise it's an
    # envoy-tree subdir with its own matchfile/common.vgo and moves.
    for sub in "${VIGO_STOCKPILE}"/*/; do
        [ -d "$sub" ] || continue
        name=$(basename "$sub")
        case "$name" in
            stones|usercrates|templates|tasks|workflows) continue ;;
        esac
        mv "$sub" "${VIGO_WORKSITE}/scaffolding/${name}"
    done

    echo "Migrated worksite/stockpile/ flat layout → stockpile/ + scaffolding/ (modules→stones, waivers.vgo split)."
fi

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "${VIGO_STOCKPILE}/stones" "${VIGO_STOCKPILE}/templates" "${VIGO_STOCKPILE}/tasks" "${VIGO_STOCKPILE}/workflows" "${VIGO_STOCKPILE}/usercrates"
mkdir -p "${VIGO_WORKSITE}/scaffolding"
mkdir -p -m 750 "${VIGO_STRUCTURE}"
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
# The seed layout mirrors the on-disk layout: /seed/worksite/{stockpile,
# scaffolding}/ → /srv/vigo/worksite/{stockpile,scaffolding}/. Everything
# gets published to structure/ together via vigocli config publish.
# ---------------------------------------------------------------------------

if [ -d "${SEED_DIR}/worksite" ]; then
    find "${SEED_DIR}/worksite" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/worksite/}"
        dest="${VIGO_WORKSITE}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
    echo "Starter config files seeded."
fi

# Initial publish: sync both stockpile/ and scaffolding/ into structure/
# on first run so the server has config available at startup. Subsequent
# changes go through vigocli config publish.
if [ ! -d "${VIGO_STRUCTURE}/stockpile" ] && [ ! -d "${VIGO_STRUCTURE}/scaffolding" ]; then
    for subtree in stockpile scaffolding; do
        if [ -d "${VIGO_WORKSITE}/${subtree}" ]; then
            cp -r "${VIGO_WORKSITE}/${subtree}" "${VIGO_STRUCTURE}/"
        fi
    done
    chmod -R u=rwX,g=rX,o= "${VIGO_STRUCTURE}"
    echo "Initial config published to structure/."
fi

# ---------------------------------------------------------------------------
# Seed example configs into examples/ (skip files that already exist)
# ---------------------------------------------------------------------------

# Seed worksite examples (stockpile + scaffolding)
find "${SEED_DIR}/example-configs/worksite" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/example-configs/worksite/}"
    # Skip compliance stones and roles — gated behind paid license.
    case "$rel" in
        stockpile/stones/compliance/*|scaffolding/compliance-roles.vgo.example) continue ;;
    esac
    dest="${EXAMPLES_DIR}/worksite/${rel}"
    if [ ! -f "$dest" ]; then
        mkdir -p "$(dirname "$dest")"
        cp "$src" "$dest"
    fi
done

# Seed retraction examples
find "${SEED_DIR}/example-configs/retractions" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/example-configs/retractions/}"
    # Skip compliance retractions — gated behind paid license.
    case "$rel" in
        modules/compliance/*) continue ;;
    esac
    dest="${EXAMPLES_DIR}/retractions/${rel}"
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
