#!/usr/bin/env bash
#
# sigil-tombstone-probe.sh
#
# Single-purpose diagnostic for substrate Phase B failure 3:
#   "DELETE tombstone never reached danlap"
#
# Disambiguates between:
#   (A) tombstone propagation is broken — fan-out, announce, or merge
#   (B) tombstone propagation works, but `vigo sigil list --tombstones`
#       renders an empty result misleadingly (the empty-state path at
#       agent/src/sigil/cli.rs:639-640 hardcodes "(no files in <dir>)"
#       regardless of the --tombstones flag)
#
# How: encrypt a uniquely-named file on $SECONDARY, delete it, wait for the
# 30s announce tick + slack, then probe $PRIMARY three independent ways:
#   1. Raw on-disk: /var/lib/vigo/sigil/<user>/tombstones.json (sudo cat)
#   2. CLI: vigo sigil list --tombstones
#   3. CLI: vigo sigil list  (no flag — should also include tombstoned rows)
#
# Run from the workstation. Same env vars as substrate-phase-b-livetest.sh.
#
# Required:
#   SIGIL_PASSWORD_FILE  default ~/.sigil-test-password — enrollment password
#
# Optional:
#   PRIMARY     default danlap   — the receiver under inspection
#   SECONDARY   default annlap   — the deleter / tombstone source
#   SIGIL_USER  default dan
#   WAIT_SECS   default 60       — seconds to wait after delete

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
SIGIL_USER="${SIGIL_USER:-dan}"
SIGIL_PASSWORD_FILE="${SIGIL_PASSWORD_FILE:-$HOME/.sigil-test-password}"
WAIT_SECS="${WAIT_SECS:-60}"

GREEN=$'\033[38;2;166;227;161m'
RED=$'\033[38;2;243;139;168m'
YELLOW=$'\033[38;2;249;226;175m'
BLUE=$'\033[38;2;137;180;250m'
LAV=$'\033[38;2;180;190;254m'
MAUVE=$'\033[38;2;203;166;247m'
RESET=$'\033[0m'

section() { printf '\n%s━━━ %s ━━━%s\n' "$MAUVE" "$*" "$RESET"; }
step()    { printf '%s▸%s %s\n' "$BLUE" "$RESET" "$*"; }
note()    { printf '%s    · %s%s\n' "$LAV" "$*" "$RESET"; }
ok()      { printf '%s  ✓ %s%s\n' "$GREEN" "$*" "$RESET"; }
bad()     { printf '%s  ✗ %s%s\n' "$RED" "$*" "$RESET"; }
warn()    { printf '%s  ! %s%s\n' "$YELLOW" "$*" "$RESET"; }

LOCAL_SHORT=$(hostname -s)
LOCAL_FULL=$(hostname)
is_local() {
    [[ "$1" == "$LOCAL_SHORT" || "$1" == "$LOCAL_FULL" || "$1" == "localhost" ]]
}
on_host() {
    local host=$1; shift
    if is_local "$host"; then
        bash -c "$*"
    else
        ssh "$host" "$*"
    fi
}

[[ -r "$SIGIL_PASSWORD_FILE" ]] || { bad "missing $SIGIL_PASSWORD_FILE"; exit 1; }
PW=$(< "$SIGIL_PASSWORD_FILE")

# Unique probe filename so we can distinguish this run's tombstone from any
# residue of prior runs.
PROBE_NAME="probe-$(date +%s).age"

section "Setup"
note "PRIMARY=$PRIMARY  SECONDARY=$SECONDARY  SIGIL_USER=$SIGIL_USER"
note "probe filename: $PROBE_NAME"
note "wait after delete: ${WAIT_SECS}s"

step "$SECONDARY: ensuring sigil enrollment"
if printf '%s\n' "$PW" | on_host "$SECONDARY" 'vigo sigil enroll' >/dev/null 2>&1; then
    ok "$SECONDARY enrolled (or already enrolled)"
else
    warn "$SECONDARY enroll returned non-zero — proceeding anyway (may already be enrolled)"
fi

step "$SECONDARY: unlocking session"
if printf '%s\n' "$PW" | on_host "$SECONDARY" 'vigo sigil unlock' >/dev/null 2>&1; then
    ok "$SECONDARY unlocked"
else
    bad "$SECONDARY unlock failed — cannot encrypt; aborting"
    exit 1
fi

section "Stage 1 — encrypt + delete on $SECONDARY"

step "encrypting $PROBE_NAME"
if on_host "$SECONDARY" "echo probe-content > /tmp/$PROBE_NAME.plain && vigo sigil encrypt /tmp/$PROBE_NAME.plain --as $PROBE_NAME && rm -f /tmp/$PROBE_NAME.plain"; then
    ok "encrypted ~/sigil/$PROBE_NAME on $SECONDARY"
else
    bad "encrypt failed on $SECONDARY"
    exit 1
fi

step "verifying ciphertext exists on $SECONDARY"
if on_host "$SECONDARY" "test -f \$HOME/sigil/$PROBE_NAME"; then
    ok "ciphertext present"
else
    bad "ciphertext missing immediately after encrypt — abort"
    exit 1
fi

# Brief pause so the on-disk fan-out has a chance to push to PRIMARY (push
# happens via inotify watcher, normally <1s).
sleep 3

step "checking ciphertext fanned out to $PRIMARY (pre-delete sanity)"
if on_host "$PRIMARY" "test -f \$HOME/sigil/$PROBE_NAME"; then
    ok "$PRIMARY received $PROBE_NAME via push (fan-out works)"
    PRE_DELETE_PUSH=1
else
    warn "$PRIMARY did NOT receive $PROBE_NAME — push fan-out may be broken too"
    PRE_DELETE_PUSH=0
fi

step "deleting $PROBE_NAME on $SECONDARY"
if on_host "$SECONDARY" "vigo sigil delete $PROBE_NAME"; then
    ok "delete returned 0"
else
    bad "delete failed on $SECONDARY"
    exit 1
fi

step "waiting ${WAIT_SECS}s for tombstone announce tick + propagation"
sleep "$WAIT_SECS"

section "Stage 2 — three independent probes on $PRIMARY"

# Probe 1: raw on-disk tombstones.json
step "probe 1: raw /var/lib/vigo/sigil/$SIGIL_USER/tombstones.json (sudo cat)"
TS_PATH="/var/lib/vigo/sigil/$SIGIL_USER/tombstones.json"
RAW=$(on_host "$PRIMARY" "sudo cat $TS_PATH 2>&1" || true)
if grep -q "$PROBE_NAME" <<<"$RAW"; then
    ok "probe 1 PASS — $PROBE_NAME present in $PRIMARY's tombstones.json"
    PROBE1=PASS
elif grep -qiE "no such|cannot access|not found" <<<"$RAW"; then
    bad "probe 1 FAIL — tombstones.json does not exist on $PRIMARY"
    note "$RAW"
    PROBE1=NO_FILE
else
    bad "probe 1 FAIL — $PROBE_NAME NOT in tombstones.json"
    note "file contents (first 1KB):"
    note "$(head -c 1024 <<<"$RAW")"
    PROBE1=ABSENT
fi

# Probe 2: vigo sigil list --tombstones (as user)
step "probe 2: vigo sigil list --tombstones (as $SIGIL_USER on $PRIMARY)"
P2=$(on_host "$PRIMARY" 'vigo sigil list --tombstones' 2>&1 || true)
if grep -q "$PROBE_NAME" <<<"$P2"; then
    ok "probe 2 PASS — $PROBE_NAME visible in CLI tombstone list"
    PROBE2=PASS
else
    bad "probe 2 FAIL — $PROBE_NAME NOT in 'sigil list --tombstones' output"
    note "output:"
    while IFS= read -r line; do note "  $line"; done <<<"$P2"
    PROBE2=ABSENT
fi

# Probe 3: vigo sigil list (no flag)
step "probe 3: vigo sigil list (no flag — unified live + tombstone view)"
P3=$(on_host "$PRIMARY" 'vigo sigil list' 2>&1 || true)
if grep -q "$PROBE_NAME" <<<"$P3"; then
    ok "probe 3 PASS — $PROBE_NAME visible in default list output"
    PROBE3=PASS
else
    bad "probe 3 FAIL — $PROBE_NAME NOT in default list output"
    note "output:"
    while IFS= read -r line; do note "  $line"; done <<<"$P3"
    PROBE3=ABSENT
fi

section "Diagnosis"

note "pre-delete push fan-out:  $([[ "$PRE_DELETE_PUSH" == 1 ]] && echo OK || echo BROKEN)"
note "probe 1 (raw json):       $PROBE1"
note "probe 2 (--tombstones):   $PROBE2"
note "probe 3 (default list):   $PROBE3"

case "$PROBE1:$PROBE2:$PROBE3" in
    PASS:PASS:PASS)
        ok "tombstone propagation AND rendering both work — failure 3 was a transient timing miss"
        ;;
    PASS:ABSENT:ABSENT)
        warn "DIAGNOSIS: propagation works; CLI rendering is broken"
        note "tombstone is on disk at $TS_PATH but neither CLI form surfaces it."
        note "look at agent/src/sigil/cli.rs around line 594-640 — table-build + empty-state."
        ;;
    PASS:PASS:ABSENT)
        warn "DIAGNOSIS: --tombstones works, default list omits tombstones"
        note "look at the merge in agent/src/sigil/cli.rs:594-598."
        ;;
    PASS:ABSENT:PASS)
        warn "DIAGNOSIS: --tombstones flag is broken specifically"
        note "look at the tombstones_only branch in agent/src/sigil/cli.rs:559."
        ;;
    ABSENT:*|NO_FILE:*)
        warn "DIAGNOSIS: propagation is broken (rendering is moot)"
        note "tombstone never reached $PRIMARY's disk."
        note "check on $SECONDARY: sudo cat /var/lib/vigo/sigil/$SIGIL_USER/tombstones.json"
        note "  — is the tombstone there at all? (delete-side write)"
        note "check daemon logs on both: sudo journalctl -u vigo-envoy -n 100 | grep -iE 'tombstone|announce|sigil'"
        note "check announce reception: does $PRIMARY have $SECONDARY in its peers.json?"
        ;;
    *)
        warn "unexpected combination — review output above"
        ;;
esac

section "Cleanup"
step "removing probe artifact on $SECONDARY (best effort)"
on_host "$SECONDARY" "rm -f \$HOME/sigil/$PROBE_NAME /tmp/$PROBE_NAME.plain" 2>/dev/null || true
ok "done"
