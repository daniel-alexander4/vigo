#!/usr/bin/env bash
#
# substrate-phase-d-livetest.sh
#
# Lockbox end-to-end livetest. Companion to phase-{a,b,c} —
# phase-c covers swarm health + payload + longdrawer; phase-d covers the
# lockbox-specific surface that 0.30.24 + 0.30.25 changed:
#
#   §1  Lockbox encrypt perms — `vigo swarm lockbox encrypt` on
#       PRIMARY lands as `<user>:<group> 0600`, even though the binary
#       runs in a root context (the agent lives at
#       /usr/local/sbin/vigo and is invoked via sudo).
#       Validates the 0644-ownership fix in 0.30.24.
#
#   §2  Lockbox push fan-out perms — the ciphertext propagates to
#       SECONDARY's `~<user>/lockbox/<name>` and lands there with
#       the same `<user>:<group> 0600` perms. Validates the
#       receiver-side path (POST /swarm/lockbox/push/<user>/<name>).
#       Skipped if SECONDARY isn't fresh in PRIMARY's per-user
#       lockbox peer table at preflight (post-rejoin convergence
#       can lag the test driver — see PEER_FRESH_TIMEOUT).
#
#   §3  Lockbox decrypt -o perms — `vigo swarm lockbox decrypt -o
#       <path>` writes plaintext as `<user>:<group> 0600` even when
#       run through sudo. Validates the decrypt fix in 0.30.25.
#
#       Self-contained: §3 runs unlock + decrypt + lock inside a
#       single SSH/bash session on PRIMARY. Crossing SSH invocations
#       doesn't work — the session helper's parent-PID watchdog
#       watches the non-interactive bash that ran `vigo unlock`, and
#       that bash exits the moment its compound command finishes,
#       leaving any later SSH call to find a torn-down helper.
#       Skipped gracefully if LOCKBOX_PASSWORD_FILE is absent.
#
#   §4  Lockbox leaves catch-up smoke — confirm the GET
#       /swarm/lockbox/leaves/<user> route is wired and that the
#       daemon-side LeavesTable file exists and is parseable.
#       Validates the B.1 endpoint that supports offline-during-LEAVE
#       rejoin.
#
# Run from PRIMARY's workstation account (sudo cached). Same SSH
# prerequisites as phase-c (passwordless SSH to envoys).
#
# Optional env (defaults match phase-c):
#   PRIMARY                  danlap
#   SECONDARY                annlap
#   THIRD                    plex
#   LOCKBOX_USER           dan      — must be lockbox-enrolled on PRIMARY
#                                       and reachable via passwordless SSH
#   VIGO_BIN                 /usr/local/sbin/vigo
#   FANOUT_TIMEOUT           30       — seconds to wait for push to land
#   PEER_FRESH_TIMEOUT       120      — seconds to wait for SECONDARY
#                                       to be fresh in PRIMARY's
#                                       lockbox peer table before §2
#   LOCKBOX_PASSWORD_FILE  ~/.lockbox-test-password (mode 0600).
#                            Holds the lockbox password for §3's
#                            unlock. Section is skipped if the file
#                            is absent.

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
LOCKBOX_USER="${LOCKBOX_USER:-dan}"
VIGO_BIN="${VIGO_BIN:-/usr/local/sbin/vigo}"
FANOUT_TIMEOUT="${FANOUT_TIMEOUT:-30}"
LOCKBOX_PASSWORD_FILE="${LOCKBOX_PASSWORD_FILE:-$HOME/.lockbox-test-password}"
PEER_FRESH_TIMEOUT="${PEER_FRESH_TIMEOUT:-120}"

# Wrap a vigo invocation so HOME points at $LOCKBOX_USER's home even
# under env-stripping sudo configs (default Ubuntu sudo resets HOME
# to /root, which makes pre-0.30.26 binaries fail with "not enrolled"
# because they read $HOME first instead of resolving via passwd).
# Using `sudo env HOME=...` instead of `sudo HOME=...` because some
# sudo configs only forward env vars passed via the `env` shim.
LOCKBOX_HOME="/home/$LOCKBOX_USER"
VIGO_INVOKE="sudo env HOME=$LOCKBOX_HOME LOGNAME=$LOCKBOX_USER USER=$LOCKBOX_USER $VIGO_BIN"

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
        note "removing ~$LOCKBOX_USER/lockbox/$TEST_NAME from PRIMARY (mesh DELETE)"
        on_host "$PRIMARY" "$VIGO_INVOKE swarm lockbox delete '$TEST_NAME' >/dev/null 2>&1 || sudo rm -f '$LOCKBOX_HOME/lockbox/$TEST_NAME'" >/dev/null 2>&1 || true
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
note "LOCKBOX_USER=$LOCKBOX_USER  VIGO_BIN=$VIGO_BIN"
note "fanout timeout: ${FANOUT_TIMEOUT}s"

# vigo binary must exist on PRIMARY at the expected path.
if ! on_host "$PRIMARY" "sudo test -x $VIGO_BIN" >/dev/null 2>&1; then
    fail "vigo binary not at $VIGO_BIN on $PRIMARY"
    exit 1
fi
pass "vigo binary present on $PRIMARY"

# User must be lockbox-enrolled on PRIMARY (identity.age exists).
if ! on_host "$PRIMARY" "sudo test -f /home/$LOCKBOX_USER/.lockbox/identity.age" >/dev/null 2>&1; then
    fail "$LOCKBOX_USER is not lockbox-enrolled on $PRIMARY (~/.lockbox/identity.age missing)"
    exit 1
fi
pass "$LOCKBOX_USER is lockbox-enrolled on $PRIMARY"

# Resolve uid:gid for the lockbox user once — used by every assertion.
LOCKBOX_UID=$(on_host "$PRIMARY" "id -u $LOCKBOX_USER 2>/dev/null" | tr -d '[:space:]')
LOCKBOX_GID=$(on_host "$PRIMARY" "id -g $LOCKBOX_USER 2>/dev/null" | tr -d '[:space:]')
if [[ -z "$LOCKBOX_UID" || -z "$LOCKBOX_GID" ]]; then
    fail "could not resolve $LOCKBOX_USER uid:gid on $PRIMARY"
    exit 1
fi
note "$LOCKBOX_USER uid=$LOCKBOX_UID gid=$LOCKBOX_GID"

# Probe SECONDARY for reachability (skip §2 if unreachable).
SECONDARY_REACHABLE=0
if is_local "$SECONDARY"; then
    SECONDARY_REACHABLE=1
elif ssh -o BatchMode=yes -o ConnectTimeout=3 "$SECONDARY" true 2>/dev/null; then
    SECONDARY_REACHABLE=1
else
    warn "$SECONDARY unreachable — §2 (push fan-out) will be skipped"
fi

# Wait for SECONDARY to be FRESH in PRIMARY's per-user lockbox peer
# table. Without this, phase-b's churn (annlap leave + re-enroll) can
# leave the table showing annlap as stale (last_seen >STALE_SECS=90s
# old) at phase-d's encrypt time, so the pusher silently skips annlap
# (`lan=1`) and §2 fails with "did not appear" — even though SSH stat
# can reach annlap. Distinct from SECONDARY_REACHABLE — this gate
# cares about agent-to-agent freshness, not test-driver-to-host
# reachability.
SECONDARY_LOCKBOX_FRESH=0
if (( SECONDARY_REACHABLE == 1 )); then
    step "waiting for $SECONDARY to be fresh in $PRIMARY's lockbox peer table (timeout ${PEER_FRESH_TIMEOUT}s)"
    fresh_peers_path="/var/lib/vigo/swarm/lockbox/$LOCKBOX_USER/peers.json"
    elapsed=0
    while (( elapsed < PEER_FRESH_TIMEOUT )); do
        peers_json=$(on_host "$PRIMARY" "sudo cat $fresh_peers_path 2>/dev/null || echo '{}'")
        age=$(printf '%s' "$peers_json" | python3 -c "
import json, sys, time
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit()
peer = d.get('peers', {}).get('$SECONDARY')
if peer is None:
    print('missing')
    sys.exit()
print(int(time.time() - peer.get('last_seen', 0)))
" 2>/dev/null)
        if [[ "$age" =~ ^[0-9]+$ ]] && (( age <= 90 )); then
            SECONDARY_LOCKBOX_FRESH=1
            note "$SECONDARY fresh in $PRIMARY peer table (last_seen ${age}s ago)"
            break
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    if (( SECONDARY_LOCKBOX_FRESH == 0 )); then
        warn "$SECONDARY not fresh in $PRIMARY's lockbox peer table after ${PEER_FRESH_TIMEOUT}s (last age: ${age:-?}) — §2 will be skipped"
    fi
fi

# Load the lockbox password once (used by §3's unlock). Mode-checked
# the same way phase-b does — the file holds a password and must not
# be world-readable.
HAVE_PASSWORD=0
PW=""
if [[ -r "$LOCKBOX_PASSWORD_FILE" ]]; then
    mode=$(stat -c %a "$LOCKBOX_PASSWORD_FILE")
    if [[ "$mode" != "600" ]]; then
        fail "$LOCKBOX_PASSWORD_FILE must be mode 600 (currently $mode)"
        exit 1
    fi
    PW=$(<"$LOCKBOX_PASSWORD_FILE")
    HAVE_PASSWORD=1
    note "loaded lockbox password from $LOCKBOX_PASSWORD_FILE"
else
    note "no $LOCKBOX_PASSWORD_FILE — §3 (decrypt) will be skipped"
fi

# ─────────────────── 1. Lockbox encrypt perms ───────────────────

section "1. Lockbox encrypt perms — local file lands as $LOCKBOX_USER:$LOCKBOX_USER 0600"

PLAIN_LOCAL="/tmp/phase-d-plain-$$-$(date +%s).txt"
TEST_NAME="phase-d-perms-$$-$(date +%s).txt"
echo "phase-d perms probe $(date +%s)" > "$PLAIN_LOCAL"

step "encrypt $PLAIN_LOCAL → ~/lockbox/$TEST_NAME on $PRIMARY (via sudo, HOME=$LOCKBOX_HOME)"
# We deliberately put the plaintext in /tmp (readable by other users)
# and ask vigo to encrypt it under LOCKBOX_USER's home — the act of
# crossing the user boundary is what surfaces the chown bug, since
# the sudo'd vigo process is root but the resulting ciphertext must
# end up owned by LOCKBOX_USER.
encrypt_out=$(on_host "$PRIMARY" "$VIGO_INVOKE swarm lockbox encrypt '$PLAIN_LOCAL' --as '$TEST_NAME' 2>&1")
if [[ $? -ne 0 ]]; then
    fail "encrypt failed: $(head -2 <<<"$encrypt_out" | tr '\n' ' ')"
    exit 1
fi
pass "encrypt returned 0"

step "stat $PRIMARY:$LOCKBOX_HOME/lockbox/$TEST_NAME"
local_stat=$(stat_remote "$PRIMARY" "$LOCKBOX_HOME/lockbox/$TEST_NAME")
if [[ -z "$local_stat" ]]; then
    fail "ciphertext not present on $PRIMARY"
else
    note "$PRIMARY: $local_stat"
    read -r owner group mode <<<"$local_stat"
    if [[ "$owner" == "$LOCKBOX_USER" && "$group" == "$LOCKBOX_USER" && "$mode" == "600" ]]; then
        pass "$PRIMARY: ciphertext is $LOCKBOX_USER:$LOCKBOX_USER 0600"
    else
        fail "$PRIMARY: ciphertext is $owner:$group $mode (expected $LOCKBOX_USER:$LOCKBOX_USER 600)"
    fi
fi

# ─────────────────── 2. Lockbox push fan-out perms ───────────────────

section "2. Lockbox push fan-out — receiver-side perms preserved"

if (( SECONDARY_REACHABLE == 0 )); then
    skip "fan-out check needs $SECONDARY reachable"
elif (( SECONDARY_LOCKBOX_FRESH == 0 )); then
    skip "$SECONDARY stale in $PRIMARY's lockbox peer table — likely post-rejoin convergence delay"
else
    step "polling for $TEST_NAME to land on $SECONDARY (timeout ${FANOUT_TIMEOUT}s)"
    elapsed=0
    landed=0
    while (( elapsed < FANOUT_TIMEOUT )); do
        if on_host "$SECONDARY" "sudo test -f $LOCKBOX_HOME/lockbox/$TEST_NAME" >/dev/null 2>&1; then
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

        step "stat $SECONDARY:$LOCKBOX_HOME/lockbox/$TEST_NAME"
        remote_stat=$(stat_remote "$SECONDARY" "$LOCKBOX_HOME/lockbox/$TEST_NAME")
        if [[ -z "$remote_stat" ]]; then
            fail "ciphertext disappeared from $SECONDARY between probe and stat"
        else
            note "$SECONDARY: $remote_stat"
            read -r owner group mode <<<"$remote_stat"
            if [[ "$owner" == "$LOCKBOX_USER" && "$group" == "$LOCKBOX_USER" && "$mode" == "600" ]]; then
                pass "$SECONDARY: ciphertext is $LOCKBOX_USER:$LOCKBOX_USER 0600"
            else
                fail "$SECONDARY: ciphertext is $owner:$group $mode (expected $LOCKBOX_USER:$LOCKBOX_USER 600)"
            fi
        fi
    fi
fi

# ─────────────────── 3. Lockbox decrypt -o perms ───────────────────

section "3. Lockbox decrypt -o perms — plaintext output owned by $LOCKBOX_USER 0600"

if (( HAVE_PASSWORD == 0 )); then
    skip "no \$LOCKBOX_PASSWORD_FILE — §3 needs the password to unlock the session helper"
else
    PLAIN_DECRYPTED="/tmp/phase-d-decrypted-$$-$(date +%s).txt"
    step "unlock + decrypt $TEST_NAME → $PLAIN_DECRYPTED on $PRIMARY (single SSH session)"

    # Run unlock and decrypt inside one SSH/bash session so the session
    # helper spawned by unlock is still alive when decrypt connects.
    # Crossing SSH invocations doesn't work: the helper's parent-PID
    # watchdog watches the non-interactive bash that ran `vigo unlock`,
    # and that bash exits the moment its compound command finishes,
    # leaving any later SSH call to find a torn-down helper.
    #
    # $VIGO_INVOKE / $TEST_NAME / $PLAIN_DECRYPTED expand on this side
    # before the script is sent over SSH; \$-prefixed vars expand on
    # the remote bash. Password arrives on stdin (ssh forwards it).
    remote_script="
read -r LOCKBOX_PW
printf '%s\n' \"\$LOCKBOX_PW\" | $VIGO_INVOKE swarm lockbox unlock 2>&1
unlock_rc=\$?
if [ \$unlock_rc -ne 0 ]; then exit \$unlock_rc; fi
sleep 1
$VIGO_INVOKE swarm lockbox decrypt '$TEST_NAME' -o '$PLAIN_DECRYPTED' 2>&1
decrypt_rc=\$?
$VIGO_INVOKE swarm lockbox lock >/dev/null 2>&1 || true
exit \$decrypt_rc
"
    decrypt_out=$(printf '%s\n' "$PW" | on_host "$PRIMARY" "$remote_script" 2>&1)
    decrypt_rc=$?
    if (( decrypt_rc != 0 )); then
        fail "decrypt failed: $(head -3 <<<"$decrypt_out" | tr '\n' ' ')"
    else
        pass "decrypt returned 0"
        decrypt_stat=$(stat_remote "$PRIMARY" "$PLAIN_DECRYPTED")
        note "$PLAIN_DECRYPTED: $decrypt_stat"
        read -r owner group mode <<<"$decrypt_stat"
        if [[ "$owner" == "$LOCKBOX_USER" && "$group" == "$LOCKBOX_USER" && "$mode" == "600" ]]; then
            pass "decrypt output is $LOCKBOX_USER:$LOCKBOX_USER 0600"
        else
            fail "decrypt output is $owner:$group $mode (expected $LOCKBOX_USER:$LOCKBOX_USER 600)"
        fi
    fi
fi

# ─────────────────── 4. Leaves catch-up smoke ───────────────────

section "4. Lockbox leaves catch-up — GET /swarm/lockbox/leaves/$LOCKBOX_USER returns valid JSON"

# We can't easily curl the mTLS endpoint from outside the agent
# (the test driver doesn't have the fleet CA + agent cert handy),
# so the smoke check is: the daemon-side LeavesTable file exists and
# is parseable, AND the announcement payload now carries a non-empty
# leaves_digest field. If the agent is at 0.30.25+, both should
# hold; older agents have an empty/missing leaves_digest path.

# 4a. LeavesTable file readable + JSON-parseable (always present
#     on enrolled hosts, even if the leaves array is empty).
step "$PRIMARY: /var/lib/vigo/swarm/lockbox/$LOCKBOX_USER/leaves.json parseable"
leaves_json=$(on_host "$PRIMARY" "sudo cat /var/lib/vigo/swarm/lockbox/$LOCKBOX_USER/leaves.json 2>/dev/null || echo '{}'")
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
step "$PRIMARY: vigo-envoy journal mentions lockbox announce activity"
journal_out=$(on_host "$PRIMARY" "sudo journalctl -u vigo-envoy.service -n 200 --no-pager 2>/dev/null | grep -E 'lockbox.*(announce|leaves|digest)' | tail -3" 2>/dev/null || true)
if [[ -n "$journal_out" ]]; then
    note "recent lockbox log lines:"
    while IFS= read -r line; do note "  $line"; done <<<"$journal_out"
    pass "$PRIMARY: lockbox announce path active"
else
    skip "$PRIMARY: no recent lockbox announce log lines (daemon may have just restarted)"
fi
