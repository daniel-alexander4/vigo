#!/bin/bash
# Run vigosrv natively (outside Docker) for fast iteration.
# Stop the Docker container first: docker compose down
# To go back to Docker: make up

set -e

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
VIGO_DIR="/srv/vigo"

cd "${VIGO_DIR}"
exec sudo \
    VIGO_DOCS_DIR="${REPO_DIR}/docs" \
    VIGO_AGENT_DIR="${REPO_DIR}/dist/agent" \
    "${REPO_DIR}/bin/vigosrv" "$@"
