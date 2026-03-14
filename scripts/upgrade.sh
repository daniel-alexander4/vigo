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

cd "$DATA_DIR"

echo "Stopping containers..."
docker compose down --remove-orphans 2>/dev/null || true

if [ "${1:-}" = "--build" ]; then
    echo "Building image from source..."
    REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
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

# Update host CLI binaries from the new container.
echo "Updating host CLI..."
docker cp hobnob:/usr/local/bin/hobnobcli /usr/local/bin/hobnobcli
ln -sf hobnobcli /usr/local/bin/hnbvi

echo ""
docker compose ps
echo ""
echo "Upgrade complete."
