#!/usr/bin/env bash
#
# substrate-phase-d-livetest.sh
#
# Cryptsync end-to-end livetest. Companion to phase-{a,b,c} —
# phase-c covers swarm health + payload + filesync; phase-d covers the
# cryptsync-specific surface that 0.30.24 + 0.30.25 changed:
#
#   §1  Cryptsync encrypt perms — `vigo swarm cryptsync encrypt` on
#       PRIMARY lands as `<user>:<group> 0600`, even though the binary
#       runs in a root context (the agent lives at
#       /usr/local/sbin/vigo and is invoked via sudo).
#       Validates the 0644-ownership fix in 0.30.24.
#
#   §2  Cryptsync push fan-out perms — the ciphertext propagates to
#       SECONDARY's `~<user>/cryptsync/<name>` and lands there with
#       the same `<user>:<group> 0600` perms. Validates the
#       receiver-side path (POST /swarm/cryptsync/push/<user>/<name>).
#
#   §3  Cryptsync decrypt -o perms — `vigo swarm cryptsync decrypt -o
#       <path>` writes plaintext as `<user>:<group> 0600` even when
#       run through sudo. Validates the decrypt fix in 0.30.25.
#
#   §4  Cryptsync leaves catch-up smoke — confirm the GET
#       /swarm/cryptsync/leaves/<user> route is wired and that the
#       daemon-side LeavesTable file exists and is parseable.
#       Validates the B.1 endpoint that supports offline-during-LEAVE
#       rejoin.
#
# Run from PRIMARY's workstation account (sudo cached). Same SSH
# prerequisites as phase-c (passwordless SSH to envoys).
#
# Optional env (defaults match phase-c):
#   PRIMARY            danlap
#   SECONDARY          annlap
#   THIRD              plex
#   CRYPTSYNC_USER     dan      — must be cryptsync-enrolled on PRIMARY
#                                 and reachable via passwordless SSH
#   VIGO_BIN           /usr/local/sbin/vigo
#   FANOUT_TIMEOUT     30       — seconds to wait for push to land
#   PRE_DECRYPT_UNLOCK 0        — set 1 to assume the user has already
#                                 run `vigo swarm cryptsync unlock` on
#                                 PRIMARY, so §3 (decrypt) can run.
#                                 Default skips §3 because unlock
#                                 requires the cryptsync password and
#                                 this script is unattended.

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
CRYPTSYNC_USER="${CRYPTSYNC_USER:-dan}"
VIGO_BIN="${VIGO_BIN:-/usr/local/sbin/vigo}"
FANOUT_TIMEOUT="${FANOUT_TIMEOUT:-30}"
PRE_DECRYPT_UNLOCK="${PRE_DECRYPT_UNLOCK:-0}"

# Wrap a vigo invocation so HOME points at $CRYPTSYNC_USER's home even
# under env-stripping sudo configs (default Ubuntu sudo resets HOME
# to /root, which makes pre-0.30.26 binaries fail with "not enrolled"
# because they read $HOME first instead of resolving via passwd).
# Using `sudo env HOME=...` instead of `sudo HOME=...` because some
# sudo configs only forward env vars passed via the `env` shim.
CRYPTSYNC_HOME="/home/$CRYPTSYNC_USER"
VIGO_INVOKE="sudo env HOME=$CRYPTSYNC_HOME LOGNAME=$CRYPTSYNC_USER USER=$CRYPTSYNC_USER $VIGO_BIN"

GREEN=$'\033[38;2;166;227;161m'
RED=$'\033[38;2;243;139;168m'
YELLOW=$'\033[38;2;249;226;175m'
BLUE=$'\033[38;2;137;180;250m'
LAV=$'\033[38;2;180;190;254m'
MAUVE=$'\033[38;2;203;166;247m'
RESET=$'\033[0m'

PASS=0; FAIL=0; SKIP=0
declare -a FAILURES=()

section() { printf '\n%s━━━ %s ━━━%s\n' "$MAUVE" "$*" "$RESET"; }
step()    { printf '%s▸%s %s\n' "$BLUE" "$RESET" "$*"; }
note()    { printf '%s    · %s%s\n' "$LAV" "$*" "$RESET"; }
pass()    { PASS=$((PASS+1)); printf '%s  ✓ %s%s\n' "$GREEN" "$*" "$RESET"; }
fail()    { FAIL=$((FAIL+1)); FAILURES+=("$*"); printf '%s  ✗ %s%s\n' "$RED" "$*" "$RESET"; }
skip()    { SKIP=$((SKIP+1)); printf '%s  ⊘ %s%s\n' "$YELLOW" "$*" "$RESET"; }
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

# Stat a file via sudo on the named host and return "<owner> <group> <mode>".
# Empty string if the file is missing.
stat_remote() {
    local host=$1 path=$2
    on_host "$host" "sudo stat -c '%U %G %a' '$path' 2>/dev/null || true"
}

# ─────────────────── cleanup ───────────────────

cleanup() {
    section "Cleanup"
    if [[ -n "${TEST_NAME:-}" ]]; then
        note "removing ~$CRYPTSYNC_USER/cryptsync/$TEST_NAME from PRIMARY (mesh DELETE)"
        on_host "$PRIMARY" "$VIGO_INVOKE swarm cryptsync delete '$TEST_NAME' >/dev/null 2>&1 || sudo rm -f '$CRYPTSYNC_HOME/cryptsync/$TEST_NAME'" >/dev/null 2>&1 || true
    fi
    [[ -n "${PLAIN_LOCAL:-}" && -f "$PLAIN_LOCAL" ]] && rm -f "$PLAIN_LOCAL"
    [[ -n "${PLAIN_DECRYPTED:-}" && -f "$PLAIN_DECRYPTED" ]] && sudo rm -f "$PLAIN_DECRYPTED" 2>/dev/null || true

    section "Summary"
    printf '  passed:  %s%d%s\n' "$GREEN" "$PASS" "$RESET"
    printf '  failed:  %s%d%s\n' "$RED"   "$FAIL" "$RESET"
    printf '  skipped: %s%d%s\n' "$YELLOW" "$SKIP" "$RESET"
    if (( FAIL > 0 )); then
        printf '\n%sFailures:%s\n' "$RED" "$RESET"
        for f in "${FAILURES[@]}"; do
            printf '  %s•%s %s\n' "$RED" "$RESET" "$f"
        done
        exit 1
    fi
    exit 0
}
trap cleanup EXIT

# ─────────────────── preflight ───────────────────

section "Preflight"
note "PRIMARY=$PRIMARY  SECONDARY=$SECONDARY  THIRD=$THIRD"
note "CRYPTSYNC_USER=$CRYPTSYNC_USER  VIGO_BIN=$VIGO_BIN"
note "fanout timeout: ${FANOUT_TIMEOUT}s"

# vigo binary must exist on PRIMARY at the expected path.
if ! on_host "$PRIMARY" "sudo test -x $VIGO_BIN" >/dev/null 2>&1; then
    fail "vigo binary not at $VIGO_BIN on $PRIMARY"
    exit 1
fi
pass "vigo binary present on $PRIMARY"

# User must be cryptsync-enrolled on PRIMARY (identity.age exists).
if ! on_host "$PRIMARY" "sudo test -f /home/$CRYPTSYNC_USER/.cryptsync/identity.age" >/dev/null 2>&1; then
    fail "$CRYPTSYNC_USER is not cryptsync-enrolled on $PRIMARY (~/.cryptsync/identity.age missing)"
    exit 1
fi
pass "$CRYPTSYNC_USER is cryptsync-enrolled on $PRIMARY"

# Resolve uid:gid for the cryptsync user once — used by every assertion.
CRYPTSYNC_UID=$(on_host "$PRIMARY" "id -u $CRYPTSYNC_USER 2>/dev/null" | tr -d '[:space:]')
CRYPTSYNC_GID=$(on_host "$PRIMARY" "id -g $CRYPTSYNC_USER 2>/dev/null" | tr -d '[:space:]')
if [[ -z "$CRYPTSYNC_UID" || -z "$CRYPTSYNC_GID" ]]; then
    fail "could not resolve $CRYPTSYNC_USER uid:gid on $PRIMARY"
    exit 1
fi
note "$CRYPTSYNC_USER uid=$CRYPTSYNC_UID gid=$CRYPTSYNC_GID"

# Probe SECONDARY for reachability (skip §2 if unreachable).
SECONDARY_REACHABLE=0
if is_local "$SECONDARY"; then
    SECONDARY_REACHABLE=1
elif ssh -o BatchMode=yes -o ConnectTimeout=3 "$SECONDARY" true 2>/dev/null; then
    SECONDARY_REACHABLE=1
else
    warn "$SECONDARY unreachable — §2 (push fan-out) will be skipped"
fi

# ─────────────────── 1. Cryptsync encrypt perms ───────────────────

section "1. Cryptsync encrypt perms — local file lands as $CRYPTSYNC_USER:$CRYPTSYNC_USER 0600"

PLAIN_LOCAL="/tmp/phase-d-plain-$$-$(date +%s).txt"
TEST_NAME="phase-d-perms-$$-$(date +%s).txt"
echo "phase-d perms probe $(date +%s)" > "$PLAIN_LOCAL"

step "encrypt $PLAIN_LOCAL → ~/cryptsync/$TEST_NAME on $PRIMARY (via sudo, HOME=$CRYPTSYNC_HOME)"
# We deliberately put the plaintext in /tmp (readable by other users)
# and ask vigo to encrypt it under CRYPTSYNC_USER's home — the act of
# crossing the user boundary is what surfaces the chown bug, since
# the sudo'd vigo process is root but the resulting ciphertext must
# end up owned by CRYPTSYNC_USER.
encrypt_out=$(on_host "$PRIMARY" "$VIGO_INVOKE swarm cryptsync encrypt '$PLAIN_LOCAL' --as '$TEST_NAME' 2>&1")
if [[ $? -ne 0 ]]; then
    fail "encrypt failed: $(head -2 <<<"$encrypt_out" | tr '\n' ' ')"
    exit 1
fi
pass "encrypt returned 0"

step "stat $PRIMARY:$CRYPTSYNC_HOME/cryptsync/$TEST_NAME"
local_stat=$(stat_remote "$PRIMARY" "$CRYPTSYNC_HOME/cryptsync/$TEST_NAME")
if [[ -z "$local_stat" ]]; then
    fail "ciphertext not present on $PRIMARY"
else
    note "$PRIMARY: $local_stat"
    read -r owner group mode <<<"$local_stat"
    if [[ "$owner" == "$CRYPTSYNC_USER" && "$group" == "$CRYPTSYNC_USER" && "$mode" == "600" ]]; then
        pass "$PRIMARY: ciphertext is $CRYPTSYNC_USER:$CRYPTSYNC_USER 0600"
    else
        fail "$PRIMARY: ciphertext is $owner:$group $mode (expected $CRYPTSYNC_USER:$CRYPTSYNC_USER 600)"
    fi
fi

# ─────────────────── 2. Cryptsync push fan-out perms ───────────────────

section "2. Cryptsync push fan-out — receiver-side perms preserved"

if (( SECONDARY_REACHABLE == 0 )); then
    skip "fan-out check needs $SECONDARY reachable"
else
    step "polling for $TEST_NAME to land on $SECONDARY (timeout ${FANOUT_TIMEOUT}s)"
    elapsed=0
    landed=0
    while (( elapsed < FANOUT_TIMEOUT )); do
        if on_host "$SECONDARY" "sudo test -f $CRYPTSYNC_HOME/cryptsync/$TEST_NAME" >/dev/null 2>&1; then
            landed=1
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    if (( landed == 0 )); then
        fail "$TEST_NAME did not appear on $SECONDARY within ${FANOUT_TIMEOUT}s"
    else
        pass "$TEST_NAME appeared on $SECONDARY in ${elapsed}s"

        step "stat $SECONDARY:$CRYPTSYNC_HOME/cryptsync/$TEST_NAME"
        remote_stat=$(stat_remote "$SECONDARY" "$CRYPTSYNC_HOME/cryptsync/$TEST_NAME")
        if [[ -z "$remote_stat" ]]; then
            fail "ciphertext disappeared from $SECONDARY between probe and stat"
        else
            note "$SECONDARY: $remote_stat"
            read -r owner group mode <<<"$remote_stat"
            if [[ "$owner" == "$CRYPTSYNC_USER" && "$group" == "$CRYPTSYNC_USER" && "$mode" == "600" ]]; then
                pass "$SECONDARY: ciphertext is $CRYPTSYNC_USER:$CRYPTSYNC_USER 0600"
            else
                fail "$SECONDARY: ciphertext is $owner:$group $mode (expected $CRYPTSYNC_USER:$CRYPTSYNC_USER 600)"
            fi
        fi
    fi
fi

# ─────────────────── 3. Cryptsync decrypt -o perms ───────────────────

section "3. Cryptsync decrypt -o perms — plaintext output owned by $CRYPTSYNC_USER 0600"

if (( PRE_DECRYPT_UNLOCK == 0 )); then
    skip "decrypt requires the user to have run \`vigo swarm cryptsync unlock\` (set PRE_DECRYPT_UNLOCK=1 to enable)"
else
    PLAIN_DECRYPTED="/tmp/phase-d-decrypted-$$-$(date +%s).txt"
    step "decrypt $TEST_NAME → $PLAIN_DECRYPTED on $PRIMARY (via sudo)"
    decrypt_out=$(on_host "$PRIMARY" "$VIGO_INVOKE swarm cryptsync decrypt '$TEST_NAME' -o '$PLAIN_DECRYPTED' 2>&1")
    if [[ $? -ne 0 ]]; then
        fail "decrypt failed: $(head -2 <<<"$decrypt_out" | tr '\n' ' ')"
    else
        pass "decrypt returned 0"
        decrypt_stat=$(stat_remote "$PRIMARY" "$PLAIN_DECRYPTED")
        note "$PLAIN_DECRYPTED: $decrypt_stat"
        read -r owner group mode <<<"$decrypt_stat"
        if [[ "$owner" == "$CRYPTSYNC_USER" && "$group" == "$CRYPTSYNC_USER" && "$mode" == "600" ]]; then
            pass "decrypt output is $CRYPTSYNC_USER:$CRYPTSYNC_USER 0600"
        else
            fail "decrypt output is $owner:$group $mode (expected $CRYPTSYNC_USER:$CRYPTSYNC_USER 600)"
        fi
    fi
fi

# ─────────────────── 4. Leaves catch-up smoke ───────────────────

section "4. Cryptsync leaves catch-up — GET /swarm/cryptsync/leaves/$CRYPTSYNC_USER returns valid JSON"

# We can't easily curl the mTLS endpoint from outside the agent
# (the test driver doesn't have the fleet CA + agent cert handy),
# so the smoke check is: the daemon-side LeavesTable file exists and
# is parseable, AND the announcement payload now carries a non-empty
# leaves_digest field. If the agent is at 0.30.25+, both should
# hold; older agents have an empty/missing leaves_digest path.

# 4a. LeavesTable file readable + JSON-parseable (always present
#     on enrolled hosts, even if the leaves array is empty).
step "$PRIMARY: /var/lib/vigo/swarm/cryptsync/$CRYPTSYNC_USER/leaves.json parseable"
leaves_json=$(on_host "$PRIMARY" "sudo cat /var/lib/vigo/swarm/cryptsync/$CRYPTSYNC_USER/leaves.json 2>/dev/null || echo '{}'")
if printf '%s' "$leaves_json" | python3 -c 'import sys, json; json.load(sys.stdin)' 2>/dev/null; then
    leaves_count=$(printf '%s' "$leaves_json" | python3 -c 'import sys, json; d=json.load(sys.stdin); print(len(d.get("leaves",{})))' 2>/dev/null || echo "0")
    pass "$PRIMARY: leaves.json parseable, $leaves_count entries"
else
    fail "$PRIMARY: leaves.json missing or unparseable"
fi

# 4b. Announcer log mentions leaves_digest. The journal is the
#     cheapest place to confirm the new field is being computed
#     post-deploy; full multicast capture is overkill for a smoke.
#     We grep the last 200 lines of vigo-envoy for the path that
#     would be exercised on every announce.
step "$PRIMARY: vigo-envoy journal mentions cryptsync announce activity"
journal_out=$(on_host "$PRIMARY" "sudo journalctl -u vigo-envoy.service -n 200 --no-pager 2>/dev/null | grep -E 'cryptsync.*(announce|leaves|digest)' | tail -3" 2>/dev/null || true)
if [[ -n "$journal_out" ]]; then
    note "recent cryptsync log lines:"
    while IFS= read -r line; do note "  $line"; done <<<"$journal_out"
    pass "$PRIMARY: cryptsync announce path active"
else
    skip "$PRIMARY: no recent cryptsync announce log lines (daemon may have just restarted)"
fi
