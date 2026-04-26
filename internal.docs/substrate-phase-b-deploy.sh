#!/usr/bin/env bash
#
# substrate-phase-b-deploy.sh
#
# Section 0 of the substrate Phase A + B live test plan. Cross-compiles the
# agent and deploys to every envoy in $ENVOYS, then verifies each envoy
# reports the expected version.
#
# Run from the repo root on the workstation that has the source tree. Assumes
# passwordless SSH + sudo on every target host.
#
# Env (all optional):
#   REPO_ROOT  default: /home/dan/cairn/vigo
#   ENVOYS     space-separated host list. default: "danlap annlap plex"

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-/home/dan/cairn/vigo}"
read -ra ENVOYS <<<"${ENVOYS:-danlap annlap plex}"

GREEN=$'\033[38;2;166;227;161m'
RED=$'\033[38;2;243;139;168m'
YELLOW=$'\033[38;2;249;226;175m'
BLUE=$'\033[38;2;137;180;250m'
LAV=$'\033[38;2;180;190;254m'
RESET=$'\033[0m'

log()  { printf '%s==>%s %s\n' "$BLUE" "$RESET" "$*"; }
ok()   { printf '%s  ✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%s  !%s %s\n' "$YELLOW" "$RESET" "$*"; }
err()  { printf '%s  ✗%s %s\n' "$RED" "$RESET" "$*" >&2; }
note() { printf '%s    %s%s\n' "$LAV" "$*" "$RESET"; }

cd "$REPO_ROOT"
EXPECTED_VERSION=$(cat VERSION)

log "cross-compiling agent ($EXPECTED_VERSION)"
make dist-agent
ok "cross-compile complete"

for h in "${ENVOYS[@]}"; do
    log "deploying to $h"
    arch=$(ssh "$h" 'uname -m')
    case "$arch" in
        x86_64|amd64)  bin="dist/agent/linux/amd64/vigo" ;;
        aarch64|arm64) bin="dist/agent/linux/arm64/vigo" ;;
        *) err "unknown arch on $h: $arch"; exit 1 ;;
    esac
    note "arch=$arch  binary=$bin"
    scp -q "$bin" "$h:/tmp/vigo"
    ssh "$h" 'sudo install -m 0755 /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy'
    reported=$(ssh "$h" 'vigo --version' 2>&1 | awk '{print $NF}')
    if [[ "$reported" == "$EXPECTED_VERSION" ]]; then
        ok "$h running $reported"
    else
        err "$h reports '$reported', expected '$EXPECTED_VERSION'"
        exit 1
    fi
done

log "deploy complete — all envoys on $EXPECTED_VERSION"
