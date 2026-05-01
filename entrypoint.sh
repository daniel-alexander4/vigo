#!/bin/sh
set -e

# Vigo server entrypoint — seeds config on first run, then starts vigosrv.
# All persistent state lives under /srv/vigo (bind-mounted from host).

DATA_DIR="/srv/vigo"
VIGO_STACKS="${DATA_DIR}/stacks"
VIGO_STOCKPILE="${VIGO_STACKS}/stockpile"
VIGO_LIVE="${VIGO_STACKS}/.live"
EXAMPLES_DIR="${VIGO_STACKS}/examples"
ANTI_EXAMPLES_DIR="${VIGO_STACKS}/anti-examples"
ENV_FILE="${DATA_DIR}/.env"
SERVER_YAML="${DATA_DIR}/server.yaml"

# Seed configs are baked into the image at /seed.
SEED_DIR="/seed"

# ---------------------------------------------------------------------------
# One-time migration to worksite/ layout (0.28.0)
# ---------------------------------------------------------------------------
# Pre-0.28 layout: /srv/vigo/stockpile/ (with nested .live/).
# Mid layout:      /srv/vigo/worksite/{stockpile, structure}/ as siblings.
# Move the whole tree if the old dir exists and the new dir doesn't.
# Refuse if BOTH exist to avoid silent merge (operator must resolve).

if [ -d "${DATA_DIR}/stockpile" ] && [ -d "${DATA_DIR}/worksite" ]; then
    echo "ERROR: both legacy /srv/vigo/stockpile and /srv/vigo/worksite exist." >&2
    echo "       Refusing to auto-migrate. Resolve manually and restart." >&2
    exit 1
fi

if [ -d "${DATA_DIR}/stockpile" ] && [ ! -d "${DATA_DIR}/worksite" ]; then
    mkdir -p "${DATA_DIR}/worksite"
    # Move nested .live/ out to structure/ first while the old tree is intact.
    if [ -d "${DATA_DIR}/stockpile/.live" ]; then
        mv "${DATA_DIR}/stockpile/.live" "${DATA_DIR}/worksite/structure"
    fi
    # Then move the rest (modules, templates, envoys.vgo, roles.vgo, etc.).
    mv "${DATA_DIR}/stockpile" "${DATA_DIR}/worksite/stockpile"
    echo "Migrated /srv/vigo/stockpile/ → /srv/vigo/worksite/stockpile/ (.live → structure/)."
fi

# ---------------------------------------------------------------------------
# One-time migration to stacks/ layout (0.33.0)
# ---------------------------------------------------------------------------
# Mid layout:  /srv/vigo/worksite/{stockpile, scaffolding, structure}/
# New layout:  /srv/vigo/stacks/{stockpile, .live, examples}/
# (scaffolding/ stays under stacks/ during phase A; phase B collapses it
# into stockpile/.)
#
# Move the whole tree if the old dir exists and the new dir doesn't.
# Also relocate the legacy /srv/vigo/example-stockpile/ → stacks/examples/.
# Refuse if BOTH exist to force operator resolution.

if [ -d "${DATA_DIR}/worksite" ] && [ -d "${VIGO_STACKS}" ]; then
    echo "ERROR: both legacy /srv/vigo/worksite and /srv/vigo/stacks exist." >&2
    echo "       Refusing to auto-migrate. Resolve manually and restart." >&2
    exit 1
fi

if [ -d "${DATA_DIR}/worksite" ] && [ ! -d "${VIGO_STACKS}" ]; then
    # Move the entire worksite tree under stacks/.
    mv "${DATA_DIR}/worksite" "${VIGO_STACKS}"
    # Promote structure/ to .live/ inside stacks (rename only).
    if [ -d "${VIGO_STACKS}/structure" ] && [ ! -d "${VIGO_LIVE}" ]; then
        mv "${VIGO_STACKS}/structure" "${VIGO_LIVE}"
    fi
    # Collapse scaffolding/* into stockpile/. After 0.33.0 there is one
    # operator-edited tree (stockpile/) — roles.vgo, envoys.vgo,
    # common.vgo, waivers.vgo, environments.vgo move alongside the
    # modules/usercrates/templates that already live in stockpile/.
    if [ -d "${VIGO_STACKS}/scaffolding" ]; then
        mkdir -p "${VIGO_STOCKPILE}"
        # Use cp -rn (no-clobber) so any existing same-named file in
        # stockpile/ is preserved; operator resolves the conflict by
        # hand. Then remove the source.
        cp -rn "${VIGO_STACKS}/scaffolding/." "${VIGO_STOCKPILE}/" 2>/dev/null || true
        rm -rf "${VIGO_STACKS}/scaffolding"
    fi
    # Drop any legacy .live/scaffolding/ left over from the prior
    # two-tree publish. Post-0.33.3 the publisher only writes to
    # .live/stockpile/; scaffolding/ here is inert disk waste.
    rm -rf "${VIGO_LIVE}/scaffolding"
    echo "Migrated /srv/vigo/worksite/ → /srv/vigo/stacks/ (structure/ → .live/, scaffolding/ → stockpile/)."
fi

# Drop the legacy /srv/vigo/example-stockpile/ entirely — it's
# superseded by stacks/examples/, which is rebuilt from /seed/ below
# on every startup. Same for examples/retractions/ → anti-examples/:
# previous in-place migrations were silently lossy on directory
# conflicts (`mv -t target/ ... 2>/dev/null`) and accumulated four
# overlaid layouts on volumes that survived multiple bumps. The
# nuke-and-rebuild step below makes those migrations unnecessary.
rm -rf "${DATA_DIR}/example-stockpile"

# ---------------------------------------------------------------------------
# Create directory structure
# ---------------------------------------------------------------------------

mkdir -p "${VIGO_STOCKPILE}/modules" "${VIGO_STOCKPILE}/templates" "${VIGO_STOCKPILE}/tasks" "${VIGO_STOCKPILE}/workflows" "${VIGO_STOCKPILE}/usercrates"
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
# Seed layout (per repo): /seed/worksite/stockpile/ and /seed/worksite/
# scaffolding/. Both contribute to the single /srv/vigo/stacks/stockpile/
# tree at runtime — scaffolding/ contents are merged in alongside
# modules/usercrates/templates as part of the post-0.33.0 collapse.
# ---------------------------------------------------------------------------

if [ -d "${SEED_DIR}/worksite/stockpile" ]; then
    find "${SEED_DIR}/worksite/stockpile" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/worksite/stockpile/}"
        dest="${VIGO_STOCKPILE}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
fi
if [ -d "${SEED_DIR}/worksite/scaffolding" ]; then
    find "${SEED_DIR}/worksite/scaffolding" -type f | while IFS= read -r src; do
        rel="${src#${SEED_DIR}/worksite/scaffolding/}"
        dest="${VIGO_STOCKPILE}/${rel}"
        if [ ! -f "$dest" ]; then
            mkdir -p "$(dirname "$dest")"
            cp "$src" "$dest"
        fi
    done
fi
echo "Starter config files seeded."

# Initial publish: sync stockpile/ into .live/ on first run so the
# server has config available at startup. Subsequent changes go through
# vigocli config publish.
if [ ! -d "${VIGO_LIVE}/stockpile" ] && [ -d "${VIGO_STOCKPILE}" ]; then
    cp -r "${VIGO_STOCKPILE}" "${VIGO_LIVE}/stockpile"
    chmod -R u=rwX,g=rX,o= "${VIGO_LIVE}"
    echo "Initial config published to .live/."
fi

# ---------------------------------------------------------------------------
# Rebuild stacks/examples/ and stacks/anti-examples/ from the image seed
# ---------------------------------------------------------------------------
#
# Both trees are read-only reference templates — operators copy *from*
# them via `vigocli config examples copy` / `config anti-examples copy`,
# never edit them in place. Rebuilding from /seed/ on every startup
# guarantees the tree matches the running image and prevents the
# overlaid-layouts accumulation that prior in-place migrations produced
# (each rename of the on-disk substrate vocabulary historically left
# behind a layer that the next rename had to step over).
#
# Anything custom that an operator dropped under examples/ or
# anti-examples/ will be removed; this is by design — those trees are
# not for operator content.

rm -rf "${EXAMPLES_DIR}" "${ANTI_EXAMPLES_DIR}"
mkdir -p "${EXAMPLES_DIR}" "${ANTI_EXAMPLES_DIR}"

# Seed examples — flatten the legacy worksite/{stockpile,scaffolding}/
# split in the seed source into a single examples/ tree so paths match
# the post-0.33.0 stockpile layout. `vigocli config examples copy
# <name>` then writes destinations relative to stockpile/.
find "${SEED_DIR}/example-configs/worksite" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/example-configs/worksite/}"
    # Skip compliance modules and roles — gated behind paid license.
    case "$rel" in
        stockpile/modules/compliance/*|scaffolding/compliance-roles.vgo.example) continue ;;
    esac
    # Strip the stockpile/ or scaffolding/ prefix so the example file
    # tree mirrors the runtime stockpile/ layout one-to-one.
    case "$rel" in
        stockpile/*)   rel="${rel#stockpile/}" ;;
        scaffolding/*) rel="${rel#scaffolding/}" ;;
    esac
    dest="${EXAMPLES_DIR}/${rel}"
    mkdir -p "$(dirname "$dest")"
    cp "$src" "$dest"
done

# Seed retract templates — sibling to examples/. The mirror layout
# pairs each module example with its retract:
#   examples/modules/<cat>/<name>.vgo.example
#   anti-examples/modules/<cat>/<name>-retract.vgo.example
find "${SEED_DIR}/example-configs/retractions" -name '*.vgo.example' -type f | while IFS= read -r src; do
    rel="${src#${SEED_DIR}/example-configs/retractions/}"
    # Skip compliance retractions — gated behind paid license.
    case "$rel" in
        modules/compliance/*) continue ;;
    esac
    dest="${ANTI_EXAMPLES_DIR}/${rel}"
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
