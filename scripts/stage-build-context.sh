#!/usr/bin/env bash
# Stage a Docker build context for Dockerfile.server.
#
# vigo's go.mod has `replace git.alexander4.io/tessera => ../tessera`, but
# Docker build contexts can't reach outside their root. This helper builds a
# self-contained context: the repo at the root, tessera copied into
# `.build-deps/tessera/`, and the staged go.mod's replace directive rewritten
# to point at the in-context path. The real repo's go.mod is untouched.
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

TESSERA_DIR="$(dirname "$REPO_DIR")/tessera"
if [ ! -d "$TESSERA_DIR" ]; then
    echo "ERROR: sibling tessera repo not found at $TESSERA_DIR" >&2
    echo "       vigo's go.mod requires it (replace => ../tessera)." >&2
    echo "       Clone it next to vigo: cd $(dirname "$REPO_DIR") && git clone <tessera-url>" >&2
    exit 1
fi

# Mirror the repo's .dockerignore exclusions: skip the Rust target dirs
# (54+ GiB), host build outputs, git history, TLS certs, IDE state,
# Claude scratch, and test/coverage artifacts.
rsync -a \
    --exclude='agent/target/' \
    --exclude='agent/.build/' \
    --exclude='bin/' \
    --exclude='dist/server/' \
    --exclude='dist/cli/' \
    --exclude='.git/' \
    --exclude='certs/' \
    --exclude='.vscode/' \
    --exclude='.idea/' \
    --exclude='*.out' \
    --exclude='coverage.html' \
    --exclude='full.docker.reset.sh' \
    --exclude='.claude/' \
    "$REPO_DIR/" "$DEST_DIR/"

mkdir -p "$DEST_DIR/.build-deps"
rsync -a --exclude='.git/' --exclude='bin/' \
    "$TESSERA_DIR/" "$DEST_DIR/.build-deps/tessera/"

# Rewrite the replace directive to target the in-context path so
# `go mod download` inside the builder resolves tessera without
# needing to reach outside the Docker context.
sed -i 's|=> \.\./tessera|=> ./.build-deps/tessera|' "$DEST_DIR/go.mod"
