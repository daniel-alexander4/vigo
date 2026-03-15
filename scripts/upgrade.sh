#!/usr/bin/env bash
set -euo pipefail

# Upgrade the Hobnob server to the latest container image.
# Preserves: server.yaml, .env, db/, secrets/, tls/, license/, hnb/, config-history/
# Replaces:  container image, docs/, examples/, docker-compose.yml (if unchanged)
#
# Usage:
#   sudo scripts/upgrade.sh              # pull latest and restart
#   sudo scripts/upgrade.sh --build      # build from source and restart (dev)

DATA_DIR="/srv/hobnob"
IMAGE="ghcr.io/daniel-alexander4/hobnob:latest"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo $0" >&2
    exit 1
fi

# Resolve repo path before cd (needed for --build).
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

cd "$DATA_DIR"

echo "Stopping containers..."
docker compose down --remove-orphans 2>/dev/null || true

if [ "${1:-}" = "--build" ]; then
    echo "Building image from source..."
    docker build -f "$REPO_DIR/Dockerfile.server" -t "$IMAGE" "$REPO_DIR"
else
    echo "Pulling latest image..."
    docker compose pull hobnob
fi

# Re-seed docs and examples from the new image (non-destructive to user files).
echo "Updating docs and examples..."
docker run --rm -v "$DATA_DIR:$DATA_DIR" "$IMAGE" --seed-only 2>/dev/null || true

echo "Starting containers..."
docker compose up -d

echo ""
echo "Waiting for server..."
timeout 60 docker compose logs -f hobnob 2>&1 | grep -m1 "server ready" || true

# Update host binaries from the new container.
echo "Updating host binaries..."
docker cp hobnob:/usr/local/bin/hobnobcli /usr/local/bin/hobnobcli
ln -sf hobnobcli /usr/local/bin/hnbvi
docker cp hobnob:/dist/agent/linux/amd64/hobnob /usr/local/bin/hobnob
systemctl restart hobnob-thrall 2>/dev/null || true

echo ""
docker compose ps

# ── Freshness check ────────────────────────────────────────────────────
# Compare binaries and key files between the container and the host/repo
# to flag anything that didn't get updated.
echo ""
echo "Checking freshness..."

STALE=0

# Compare host CLI against container CLI.
if [ -f /usr/local/bin/hobnobcli ]; then
    HOST_MD5=$(md5sum /usr/local/bin/hobnobcli 2>/dev/null | awk '{print $1}')
    CTR_MD5=$(docker exec hobnob md5sum /usr/local/bin/hobnobcli 2>/dev/null | awk '{print $1}')
    if [ "$HOST_MD5" != "$CTR_MD5" ]; then
        echo "  STALE  /usr/local/bin/hobnobcli (host differs from container)"
        STALE=1
    else
        echo "  OK     /usr/local/bin/hobnobcli"
    fi
fi

# Compare container binaries against repo source (--build mode only).
if [ "${1:-}" = "--build" ] && [ -d "$REPO_DIR" ]; then
    # Check server binary build time vs latest source modification.
    NEWEST_GO=$(find "$REPO_DIR/server" "$REPO_DIR/ui" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_GO=${NEWEST_GO%.*}
    CTR_HOBD=$(docker exec hobnob stat -c '%Y' /usr/local/bin/hobd 2>/dev/null || echo 0)
    if [ -n "$NEWEST_GO" ] && [ "$NEWEST_GO" -gt "$CTR_HOBD" ]; then
        echo "  STALE  hobd in container (source is newer than binary)"
        STALE=1
    else
        echo "  OK     hobd"
    fi

    # Check CLI binary.
    NEWEST_CLI=$(find "$REPO_DIR/cli" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_CLI=${NEWEST_CLI%.*}
    CTR_CLI=$(docker exec hobnob stat -c '%Y' /usr/local/bin/hobnobcli 2>/dev/null || echo 0)
    if [ -n "$NEWEST_CLI" ] && [ "$NEWEST_CLI" -gt "$CTR_CLI" ]; then
        echo "  STALE  hobnobcli in container (source is newer than binary)"
        STALE=1
    else
        echo "  OK     hobnobcli"
    fi

    # Check agent binary.
    NEWEST_RS=$(find "$REPO_DIR/agent-rs/src" -name '*.rs' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_RS=${NEWEST_RS%.*}
    CTR_AGENT=$(docker exec hobnob stat -c '%Y' /dist/agent/linux/amd64/hobnob 2>/dev/null || echo 0)
    if [ -n "$NEWEST_RS" ] && [ "$NEWEST_RS" -gt "$CTR_AGENT" ]; then
        echo "  STALE  agent in container (source is newer than binary)"
        STALE=1
    else
        echo "  OK     agent"
    fi

    # Check entrypoint.sh.
    REPO_EP_MD5=$(md5sum "$REPO_DIR/entrypoint.sh" 2>/dev/null | awk '{print $1}')
    CTR_EP_MD5=$(docker exec hobnob md5sum /entrypoint.sh 2>/dev/null | awk '{print $1}')
    if [ "$REPO_EP_MD5" != "$CTR_EP_MD5" ]; then
        echo "  STALE  entrypoint.sh (repo differs from container)"
        STALE=1
    else
        echo "  OK     entrypoint.sh"
    fi

    # Check Dockerfile itself (was the image built from the current Dockerfile?).
    DOCKERFILE_TIME=$(stat -c '%Y' "$REPO_DIR/Dockerfile.server" 2>/dev/null || echo 0)
    IMAGE_TIME=$(docker inspect --format='{{.Created}}' "$IMAGE" 2>/dev/null | xargs -I{} date -d {} +%s 2>/dev/null || echo 0)
    if [ "$DOCKERFILE_TIME" -gt "$IMAGE_TIME" ]; then
        echo "  STALE  Dockerfile.server (modified after image was built)"
        STALE=1
    else
        echo "  OK     Dockerfile.server"
    fi
fi

# Check docker-compose.yml.
if [ -f "$DATA_DIR/docker-compose.yml" ] && [ -f "$REPO_DIR/docker-compose.yml" ]; then
    DATA_DC_MD5=$(md5sum "$DATA_DIR/docker-compose.yml" 2>/dev/null | awk '{print $1}')
    REPO_DC_MD5=$(md5sum "$REPO_DIR/docker-compose.yml" 2>/dev/null | awk '{print $1}')
    if [ "$DATA_DC_MD5" != "$REPO_DC_MD5" ]; then
        echo "  STALE  docker-compose.yml ($DATA_DIR differs from repo)"
        STALE=1
    else
        echo "  OK     docker-compose.yml"
    fi
fi

echo ""
if [ "$STALE" -eq 0 ]; then
    echo "All files are fresh."
else
    echo "WARNING: Stale files detected. Re-run with --build to update."
fi

echo ""
echo "Upgrade complete."
