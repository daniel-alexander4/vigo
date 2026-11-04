#!/usr/bin/env bash
# Stage a Docker build context for Dockerfile.server.
#
# Copies the repo into a self-contained context directory, skipping host
# build outputs and other paths the repo's .dockerignore excludes.
#
# Usage:
#   scripts/stage-build-context.sh <repo-dir> <dest-dir>
#
# The caller owns mktemp and cleanup (trap).

set -euo pipefail

if [ $# -ne 2 ]; then
    echo "usage: $0 <repo-dir> <dest-dir>" >&2
    exit 2
fi

REPO_DIR="$1"
DEST_DIR="$2"

[ -d "$REPO_DIR" ] || { echo "repo dir not found: $REPO_DIR" >&2; exit 1; }
[ -d "$DEST_DIR" ] || { echo "dest dir not found: $DEST_DIR" >&2; exit 1; }

# Mirror the repo's .dockerignore exclusions: skip the Rust target dirs
# (54+ GiB), host build outputs, git history, TLS certs, private keys (the
# signing key travels only as a BuildKit secret), IDE state, Claude scratch,
# and test/coverage artifacts.
rsync -a \
    --exclude='agent/target/' \
    --exclude='agent/.build/' \
    --exclude='bin/' \
    --exclude='dist/server/' \
    --exclude='dist/cli/' \
    --exclude='.git/' \
    --exclude='certs/' \
    --exclude='signing.key' \
    --exclude='*.key' \
    --exclude='.vscode/' \
    --exclude='.idea/' \
    --exclude='*.out' \
    --exclude='coverage.html' \
    --exclude='full.docker.reset.sh' \
    --exclude='.claude/' \
    "$REPO_DIR/" "$DEST_DIR/"
