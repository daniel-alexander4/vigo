#!/usr/bin/env bash
#
# substrate-livetest-all.sh
#
# Run every substrate live-test phase in sequence and aggregate
# pass/fail counts. Each phase script is independent; a failure in
# one phase does NOT halt subsequent phases. Exit code is non-zero
# iff any phase reported failures.
#
# Phase scope (see substrate-livetests.md for detail):
#
#   phase-b  — gitmirror source CLI + leave/evict (gitmirror +
#              cryptsync). Sections 4–7 require
#              CRYPTSYNC_PASSWORD_FILE; skipped gracefully if absent.
#   phase-c  — swarm health (peers + manifest gossip), payload
#              distribute/revoke, filesync round-trip.
#   phase-d  — cryptsync encrypt/push/decrypt perms, leaves catch-up
#              smoke. Section 3 (decrypt) requires
#              CRYPTSYNC_PASSWORD_FILE; skipped gracefully if absent.
#
# All env vars accepted by the underlying scripts (PRIMARY,
# SECONDARY, THIRD, FILESYNC_USER, CRYPTSYNC_USER, VIGO_BIN, etc.)
# pass through transparently — set them before invoking this wrapper.
#
# Run: ./substrate-livetest-all.sh
#      CRYPTSYNC_PASSWORD_FILE=~/.pw ./substrate-livetest-all.sh

set -uo pipefail

GREEN=$'\033[38;2;166;227;161m'
RED=$'\033[38;2;243;139;168m'
YELLOW=$'\033[38;2;249;226;175m'
BLUE=$'\033[38;2;137;180;250m'
LAV=$'\033[38;2;180;190;254m'
MAUVE=$'\033[38;2;203;166;247m'
RESET=$'\033[0m'

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PHASES=(
    "phase-b:Gitmirror source CLI + leave/evict (gitmirror + cryptsync)"
    "phase-c:Swarm health, payload, filesync"
    "phase-d:Cryptsync encrypt/push/decrypt + leaves catch-up"
)

declare -A PHASE_RC
declare -A PHASE_DURATION

START_TIME=$(date +%s)

for entry in "${PHASES[@]}"; do
    phase="${entry%%:*}"
    label="${entry#*:}"
    script="$SCRIPT_DIR/substrate-${phase}-livetest.sh"

    printf '\n%s═══════════════════════════════════════════════════════════════════%s\n' "$MAUVE" "$RESET"
    printf '%s  %s — %s%s\n' "$MAUVE" "$phase" "$label" "$RESET"
    printf '%s═══════════════════════════════════════════════════════════════════%s\n' "$MAUVE" "$RESET"

    if [[ ! -x "$script" ]]; then
        printf '%s  ✗ script missing or not executable: %s%s\n' "$RED" "$script" "$RESET"
        PHASE_RC[$phase]=127
        PHASE_DURATION[$phase]=0
        continue
    fi

    phase_start=$(date +%s)
    "$script"
    rc=$?
    phase_end=$(date +%s)

    PHASE_RC[$phase]=$rc
    PHASE_DURATION[$phase]=$((phase_end - phase_start))
done

END_TIME=$(date +%s)
TOTAL_DURATION=$((END_TIME - START_TIME))

# ─────────── aggregate summary ───────────

printf '\n%s═══════════════════════════════════════════════════════════════════%s\n' "$MAUVE" "$RESET"
printf '%s  Substrate live-test sweep — summary%s\n' "$MAUVE" "$RESET"
printf '%s═══════════════════════════════════════════════════════════════════%s\n' "$MAUVE" "$RESET"

OVERALL_PASS=0
OVERALL_FAIL=0
for entry in "${PHASES[@]}"; do
    phase="${entry%%:*}"
    label="${entry#*:}"
    rc=${PHASE_RC[$phase]:-127}
    dur=${PHASE_DURATION[$phase]:-0}
    if (( rc == 0 )); then
        printf '%s  ✓%s %-8s %s(%ds)%s — %s\n' "$GREEN" "$RESET" "$phase" "$LAV" "$dur" "$RESET" "$label"
        OVERALL_PASS=$((OVERALL_PASS + 1))
    else
        printf '%s  ✗%s %-8s %s(%ds, exit=%d)%s — %s\n' "$RED" "$RESET" "$phase" "$LAV" "$dur" "$rc" "$RESET" "$label"
        OVERALL_FAIL=$((OVERALL_FAIL + 1))
    fi
done

printf '\n  total runtime: %ds\n' "$TOTAL_DURATION"
printf '  phases passed: %s%d%s\n' "$GREEN" "$OVERALL_PASS" "$RESET"
printf '  phases failed: %s%d%s\n' "$RED" "$OVERALL_FAIL" "$RESET"

if (( OVERALL_FAIL > 0 )); then
    exit 1
fi
exit 0
