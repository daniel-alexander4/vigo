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
LOCAL_SHORT=$(hostname -s)
LOCAL_FULL=$(hostname)

is_local() {
    [[ "$1" == "$LOCAL_SHORT" || "$1" == "$LOCAL_FULL" || "$1" == "localhost" ]]
}

log "cross-compiling agent ($EXPECTED_VERSION)"
make dist-agent
ok "cross-compile complete"

# Pre-authenticate sudo once so the in-loop sudo calls don't re-prompt and
# can't silently die on a swallowed password prompt.
log "pre-authenticating sudo (one prompt, cached for the rest of the run)"
sudo -v

for h in "${ENVOYS[@]}"; do
    log "deploying to $h"
    if is_local "$h"; then
        arch=$(uname -m)
        note "$h is local — skipping ssh/scp"
    else
        arch=$(ssh "$h" 'uname -m')
    fi
    case "$arch" in
        x86_64|amd64)  bin="dist/agent/linux/amd64/vigo" ;;
        aarch64|arm64) bin="dist/agent/linux/arm64/vigo" ;;
        *) err "unknown arch on $h: $arch"; exit 1 ;;
    esac
    note "arch=$arch  binary=$bin"
    if is_local "$h"; then
        note "installing $bin → /usr/local/sbin/vigo"
        if ! sudo install -m 0755 "$bin" /usr/local/sbin/vigo; then
            err "install failed (exit $?)"
            exit 1
        fi
        note "restarting vigo-envoy"
        if ! sudo systemctl restart vigo-envoy; then
            err "systemctl restart failed — last log lines:"
            sudo journalctl -u vigo-envoy -n 20 --no-pager >&2 || true
            exit 1
        fi
        note "querying version"
        if ! version_output=$(vigo version 2>&1); then
            err "vigo version failed: $version_output"
            exit 1
        fi
        reported=$(awk '{print $NF}' <<<"$version_output")
    else
        note "scp $bin → $h:/tmp/vigo"
        if ! scp -q "$bin" "$h:/tmp/vigo"; then
            err "scp to $h failed (exit $?)"
            exit 1
        fi
        note "remote install + restart on $h"
        if ! ssh "$h" 'sudo install -m 0755 /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy'; then
            err "remote install/restart on $h failed (exit $?)"
            ssh "$h" 'sudo journalctl -u vigo-envoy -n 20 --no-pager' >&2 || true
            exit 1
        fi
        if ! version_output=$(ssh "$h" 'vigo version' 2>&1); then
            err "remote vigo version on $h failed: $version_output"
            exit 1
        fi
        reported=$(awk '{print $NF}' <<<"$version_output")
    fi
    if [[ "$reported" == "$EXPECTED_VERSION" ]]; then
        ok "$h running $reported"
    else
        err "$h reports '$reported', expected '$EXPECTED_VERSION'"
        exit 1
    fi
done

log "deploy complete — all envoys on $EXPECTED_VERSION"
