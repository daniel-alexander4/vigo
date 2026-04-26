#!/usr/bin/env bash
#
# substrate-phase-c-livetest.sh
#
# Companion to substrate-phase-b-livetest.sh, covering the parts of
# the swarm substrate that phase-b doesn't touch:
#
#   §1  Swarm health        — read-only assertions: each envoy's
#                              peers.json knows the others; manifest.json
#                              is non-empty and roughly consistent across
#                              the fleet.
#   §2  Lode end-to-end     — distribute a small payload, poll status
#                              until every envoy is complete/cached/source,
#                              verify the file lands at the documented
#                              materialization paths, revoke, verify
#                              eviction.
#   §3  Echo end-to-end     — drop a file in PRIMARY's ~/echo/, verify
#                              it propagates to SECONDARY, modify on
#                              SECONDARY, verify the update reflects on
#                              PRIMARY, delete on PRIMARY, verify removal.
#
# Run from the workstation. Same SSH/sudo prerequisites as phase-b
# (passwordless SSH to envoys, sudo cached on the workstation for the
# vigocli prefix).
#
# Optional env:
#   PRIMARY       default danlap   — workstation; must run vigocli locally
#   SECONDARY     default annlap
#   THIRD         default plex
#   ECHO_USER     default dan      — user account for echo files (must
#                                     have ~/echo/ in their home)
#   VIGOCLI_CMD   default "sudo vigocli" — vigocli is mode 0700 root
#                                          by design. See feedback memory.
#   LODE_TIMEOUT  default 90       — seconds to wait for distribution
#                                     to reach all envoys
#   ECHO_TIMEOUT  default 30       — seconds to wait for an echo sync
#                                     event to propagate

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
ECHO_USER="${ECHO_USER:-dan}"
VIGOCLI_CMD="${VIGOCLI_CMD:-sudo vigocli}"
LODE_TIMEOUT="${LODE_TIMEOUT:-90}"
ECHO_TIMEOUT="${ECHO_TIMEOUT:-30}"

# Catppuccin Mocha — same palette as phase-b for visual continuity.
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

vigocli() { $VIGOCLI_CMD "$@"; }

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

# Run a command as $ECHO_USER on a host. Local: su -; remote: ssh as
# user. Echo files must be owned by the user, not root, so we can't
# use the default on_host (which runs as the invoking user — usually
# fine since the invoker IS the echo user, but this is explicit).
on_host_as_user() {
    local host=$1 user=$2; shift 2
    if is_local "$host"; then
        if [[ "$(id -un)" == "$user" ]]; then
            bash -c "$*"
        else
            sudo -u "$user" -- bash -c "$*"
        fi
    else
        ssh "$user@$host" "$*"
    fi
}

# ───────────────── lode helpers ─────────────────

# Poll vigocli lode status until every TEST envoy (PRIMARY/SECONDARY/
# THIRD that's reachable) shows complete / cached / source on its
# per-envoy line. The fleet may include other envoys (girlslaptop,
# raspberrypi, etc.) that aren't under test and may be perpetually
# pending — checking the summary "downloading + pending == 0" would
# wait for those too and time out.
#
# Per-envoy line shape (cli/lode.go renders):
#     "  ✓ <hostname>                    complete"
#     "  ✓ <hostname>                    cached"
#     "  ★ <hostname>                    source"
# Returns 0 on success, 1 on timeout.
wait_lode_complete() {
    local sha=$1 timeout=$2
    local elapsed=0
    while (( elapsed < timeout )); do
        local out
        out=$(vigocli lode status "$sha" 2>&1 || true)
        local all_done=1
        for h in "${ALL_HOSTS[@]}"; do
            (( HOST_REACHABLE[$h] == 0 )) && continue
            # Match the envoy's line and capture its status word.
            local line
            line=$(grep -E "^[[:space:]]+[^[:space:]]+[[:space:]]+$h[[:space:]]+" <<<"$out" | head -1)
            if [[ -z "$line" ]]; then
                all_done=0
                break
            fi
            local status
            status=$(awk '{print $NF}' <<<"$line")
            case "$status" in
                complete|cached|source) ;;
                *) all_done=0; break ;;
            esac
        done
        if (( all_done == 1 )); then
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    return 1
}

# Wait until every TEST envoy's per-envoy line shows "revoked".
# Same scope-to-test-envoys logic as wait_lode_complete — fleet
# envoys not under test may remain in revoking state indefinitely
# if they're offline.
wait_lode_revoked() {
    local sha=$1 timeout=$2
    local elapsed=0
    while (( elapsed < timeout )); do
        local out
        out=$(vigocli lode status "$sha" 2>&1 || true)
        local all_revoked=1
        for h in "${ALL_HOSTS[@]}"; do
            (( HOST_REACHABLE[$h] == 0 )) && continue
            local line
            line=$(grep -E "^[[:space:]]+[^[:space:]]+[[:space:]]+$h[[:space:]]+" <<<"$out" | head -1)
            if [[ -z "$line" ]]; then
                # Hostname disappeared from the listing — counts as
                # post-revoke (server stopped tracking).
                continue
            fi
            local status
            status=$(awk '{print $NF}' <<<"$line")
            if [[ "$status" != "revoked" ]]; then
                all_revoked=0
                break
            fi
        done
        if (( all_revoked == 1 )); then
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    return 1
}

# ───────────────── cleanup ─────────────────

cleanup() {
    section "Cleanup"

    # Remove leftover lode test payloads (best-effort — names match a
    # phase-c-lode-* prefix so we don't touch other operator payloads).
    if [[ -n "${LODE_SHA:-}" ]]; then
        if vigocli lode status "$LODE_SHA" >/dev/null 2>&1; then
            note "revoking leftover lode payload $LODE_SHA"
            vigocli lode revoke "$LODE_SHA" >/dev/null 2>&1 || true
        fi
    fi

    # Remove the workstation-side test payload file we generated.
    [[ -n "${LODE_FILE:-}" && -f "$LODE_FILE" ]] && rm -f "$LODE_FILE"

    # Echo cleanup: remove the test file from every reachable envoy
    # if it somehow survived. Safe — name is unique to this run.
    if [[ -n "${ECHO_NAME:-}" ]]; then
        for h in "${ALL_HOSTS[@]}"; do
            (( ${HOST_REACHABLE[$h]:-0} == 0 )) && continue
            on_host_as_user "$h" "$ECHO_USER" "rm -f \$HOME/echo/$ECHO_NAME" >/dev/null 2>&1 || true
        done
    fi

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

# ───────────────────────── preflight ─────────────────────────
section "Preflight"
note "PRIMARY=$PRIMARY  SECONDARY=$SECONDARY  THIRD=$THIRD"
note "ECHO_USER=$ECHO_USER  vigocli prefix: $VIGOCLI_CMD"
note "lode timeout: ${LODE_TIMEOUT}s   echo timeout: ${ECHO_TIMEOUT}s"

# Sanity: vigocli reachable.
if ! vigocli --help >/dev/null 2>&1; then
    fail "vigocli not callable as '$VIGOCLI_CMD' — check sudo cache or VIGOCLI_CMD env"
    exit 1
fi
pass "vigocli reachable"

# Resolve fleet hostnames to IPs (used for swarm-peers.json IP matching
# in §1 — the swarm peer table is keyed by IP:port, not hostname) and
# probe each host for reachability so we can SKIP rather than FAIL on
# offline envoys.
declare -A HOST_IP        # primary IPv4 for human-readable logging
declare -A HOST_IPV4_LIST # space-separated list of every IPv4 the host has
declare -A HOST_REACHABLE
ALL_HOSTS=("$PRIMARY" "$SECONDARY" "$THIRD")
for h in "${ALL_HOSTS[@]}"; do
    # Use ahostsv4 — `getent hosts` happily returns IPv6 (which the
    # swarm peer table never records, since peer probes go to LAN
    # IPv4 + port 1531). ahostsv4 also expands all addresses so a
    # multi-homed host's other interfaces still match.
    ipv4_list=$(getent ahostsv4 "$h" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')
    HOST_IPV4_LIST[$h]="$ipv4_list"
    HOST_IP[$h]=$(awk '{print $1}' <<<"$ipv4_list")
    # Reachability: is_local hosts are always reachable; remote hosts
    # get a 3-second ssh probe (BatchMode disables password prompts so
    # a stale known_hosts entry can't hang the test).
    if is_local "$h"; then
        HOST_REACHABLE[$h]=1
        note "$h: local (ip=${ip:-unresolved}) — reachable"
    elif ssh -o BatchMode=yes -o ConnectTimeout=3 "$h" true 2>/dev/null; then
        HOST_REACHABLE[$h]=1
        note "$h: remote (ip=${ip:-unresolved}) — reachable"
    else
        HOST_REACHABLE[$h]=0
        warn "$h: UNREACHABLE — assertions for this host will be skipped"
    fi
done

# How many envoys are participating? Sections that depend on cross-host
# propagation degrade gracefully when fewer than the expected count
# are reachable.
REACHABLE_COUNT=0
for h in "${ALL_HOSTS[@]}"; do
    (( HOST_REACHABLE[$h] == 1 )) && REACHABLE_COUNT=$((REACHABLE_COUNT + 1))
done
if (( REACHABLE_COUNT < 2 )); then
    fail "fewer than 2 reachable envoys ($REACHABLE_COUNT) — propagation tests cannot run"
    exit 1
fi

# ───────────────────────── 1. Swarm health (read-only) ─────────────────────────
section "1. Swarm health — peer table + manifest gossip"

# Swarm peers.json is a blob-holder index: a JSON array of records
# of shape {sha256, address, chunk_count, source, age_secs} — keyed
# by IP:port, NOT hostname. So we assert that each REACHABLE peer's
# IP appears in every other reachable envoy's peers.json (i.e. the
# fleet has successfully cross-probed each other for blobs).
for h in "${ALL_HOSTS[@]}"; do
    if (( HOST_REACHABLE[$h] == 0 )); then
        skip "$h: swarm peers.json (host unreachable)"
        continue
    fi
    step "$h: swarm peers.json"
    out=$(on_host "$h" 'sudo cat /var/lib/vigo/swarm/peers.json 2>/dev/null' || true)
    if [[ -z "$out" ]]; then
        fail "$h: peers.json missing or unreadable"
        continue
    fi
    for other in "${ALL_HOSTS[@]}"; do
        [[ "$other" == "$h" ]] && continue
        if (( HOST_REACHABLE[$other] == 0 )); then
            skip "$h: peers.json should include $other (skipped — $other unreachable, expected absent)"
            continue
        fi
        other_ips="${HOST_IPV4_LIST[$other]}"
        if [[ -z "$other_ips" ]]; then
            warn "$h: cannot verify $other in peers.json — $other has no resolved IPv4"
            continue
        fi
        # Match any IPv4 the host has — multi-homed hosts may show up
        # under whichever interface answered the probe from $h.
        matched_ip=""
        for ip in $other_ips; do
            if grep -q "\"address\":\"$ip:" <<<"$out"; then
                matched_ip="$ip"
                break
            fi
        done
        if [[ -n "$matched_ip" ]]; then
            pass "$h: peers.json knows $other ($matched_ip)"
        else
            fail "$h: peers.json missing $other (tried: $other_ips)"
        fi
    done
done

# Manifest gossip: each envoy's manifest.json should be non-empty and
# the entry-count should agree across the fleet within a small delta
# (gossip is eventually-consistent — a small skew during a window
# where one envoy hasn't picked up a recent change is normal).
declare -A MANIFEST_COUNTS
for h in "${ALL_HOSTS[@]}"; do
    if (( HOST_REACHABLE[$h] == 0 )); then
        skip "$h: manifest entry count (host unreachable)"
        continue
    fi
    step "$h: swarm manifest entry count"
    cnt=$(on_host "$h" "sudo grep -oE '\"sha256\"' /var/lib/vigo/swarm/manifest.json 2>/dev/null | wc -l" || echo 0)
    cnt=$(tr -d '[:space:]' <<<"$cnt")
    if [[ "$cnt" =~ ^[0-9]+$ ]] && (( cnt > 0 )); then
        pass "$h: manifest has $cnt entries"
        MANIFEST_COUNTS[$h]=$cnt
    else
        fail "$h: manifest has 0 entries (gossip not running, or freshly-wiped state)"
    fi
done

# Cross-fleet manifest delta — only meaningful when ≥2 reachable
# envoys reported a count.
if (( ${#MANIFEST_COUNTS[@]} >= 2 )); then
    max=0; min=999999
    for c in "${MANIFEST_COUNTS[@]}"; do
        (( c > max )) && max=$c
        (( c < min )) && min=$c
    done
    delta=$((max - min))
    if (( delta <= 2 )); then
        pass "manifest entry counts agree across reachable fleet (min=$min max=$max delta=$delta)"
    else
        fail "manifest entry counts diverge across reachable fleet (min=$min max=$max delta=$delta)"
    fi
fi

# ───────────────────────── 2. Lode end-to-end ─────────────────────────
section "2. Lode — distribute, verify fan-out, revoke"

# Keep the label ≤27 chars — `vigocli lode list` truncates anything
# longer to first 24 chars + "...", which would defeat the
# substring grep below.
LODE_LABEL="pc-lode-$(date +%s)"
LODE_FILE="/tmp/${LODE_LABEL}.bin"
step "generating 32 KiB random payload at $LODE_FILE"
if head -c 32768 /dev/urandom > "$LODE_FILE"; then
    LODE_LOCAL_SHA=$(sha256sum "$LODE_FILE" | awk '{print $1}')
    pass "payload created (sha256=${LODE_LOCAL_SHA:0:12}...)"
else
    fail "could not create test payload"
    exit 1
fi

step "vigocli lode distribute --target '*' --label $LODE_LABEL"
distribute_out=$(vigocli lode distribute "$LODE_FILE" --target '*' --label "$LODE_LABEL" 2>&1)
if [[ $? -ne 0 ]]; then
    fail "distribute failed: $(head -2 <<<"$distribute_out" | tr '\n' ' ')"
    exit 1
fi
pass "distribute returned 0"

# Pull the SHA back from the CLI output ("  SHA256: <hex>"). Compare
# with the local hash to sanity-check the upload.
LODE_SHA=$(grep -oE 'SHA256: [0-9a-f]{64}' <<<"$distribute_out" | head -1 | awk '{print $2}')
if [[ -z "$LODE_SHA" ]]; then
    fail "could not extract SHA256 from distribute output"
    exit 1
fi
if [[ "$LODE_SHA" != "$LODE_LOCAL_SHA" ]]; then
    fail "server-reported SHA $LODE_SHA != local $LODE_LOCAL_SHA"
else
    pass "server SHA matches local hash"
fi
note "tracking sha=${LODE_SHA:0:16}..."

step "polling lode status until every envoy is complete/cached/source (timeout ${LODE_TIMEOUT}s)"
if wait_lode_complete "$LODE_SHA" "$LODE_TIMEOUT"; then
    pass "all envoys reported complete within ${LODE_TIMEOUT}s"
else
    fail "lode distribution did not complete within ${LODE_TIMEOUT}s"
    note "current status:"
    vigocli lode status "$LODE_SHA" 2>&1 | head -20 | while IFS= read -r line; do note "  $line"; done
fi

# Verify materialization on each envoy. Lode writes the canonical
# blob to /var/lib/vigo/swarm/.blobs/<sha256> and a friendly symlink
# to /var/lib/vigo/swarm/files/<label>. Check both. Unreachable hosts
# are skipped (they couldn't have received the payload).
for h in "${ALL_HOSTS[@]}"; do
    if (( HOST_REACHABLE[$h] == 0 )); then
        skip "$h: blob materialization (host unreachable)"
        continue
    fi
    step "$h: blob materialized"
    if on_host "$h" "sudo test -f /var/lib/vigo/swarm/.blobs/$LODE_SHA"; then
        pass "$h: /var/lib/vigo/swarm/.blobs/${LODE_SHA:0:12}... present"
    else
        fail "$h: blob missing from .blobs/"
    fi
    if on_host "$h" "sudo test -e /var/lib/vigo/swarm/files/$LODE_LABEL"; then
        pass "$h: /var/lib/vigo/swarm/files/$LODE_LABEL symlink present"
    else
        fail "$h: friendly-name symlink missing under files/"
    fi
done

step "vigocli lode list shows the payload"
if vigocli lode list 2>&1 | grep -q "$LODE_LABEL"; then
    pass "list output contains $LODE_LABEL"
else
    fail "list output does not contain $LODE_LABEL"
fi

step "vigocli lode revoke $LODE_SHA"
if vigocli lode revoke "$LODE_SHA" >/dev/null 2>&1; then
    pass "revoke returned 0"
else
    fail "revoke command failed"
fi

step "polling lode status until every envoy is revoked (timeout ${LODE_TIMEOUT}s)"
if wait_lode_revoked "$LODE_SHA" "$LODE_TIMEOUT"; then
    pass "all envoys reported revoked within ${LODE_TIMEOUT}s"
else
    fail "lode revocation did not complete within ${LODE_TIMEOUT}s"
fi

# Verify the friendly symlink is gone (the blob may linger in
# .blobs/ briefly during graveyard sweep, so we don't assert on
# .blobs/ removal — the symlink removal is the operator-visible
# contract).
for h in "${ALL_HOSTS[@]}"; do
    if (( HOST_REACHABLE[$h] == 0 )); then
        skip "$h: friendly symlink removed post-revoke (host unreachable)"
        continue
    fi
    step "$h: friendly symlink removed post-revoke"
    if on_host "$h" "sudo test -e /var/lib/vigo/swarm/files/$LODE_LABEL"; then
        fail "$h: files/$LODE_LABEL still present after revoke"
    else
        pass "$h: files/$LODE_LABEL gone"
    fi
done

# Mark LODE_SHA empty so cleanup doesn't try to revoke an
# already-revoked payload (which would just return 'already revoked'
# but produces a noisier cleanup log).
LODE_SHA=""

# ───────────────────────── 3. Echo end-to-end ─────────────────────────
section "3. Echo — drop file, sync, modify, propagate back, delete"

# Echo round-trip needs PRIMARY and SECONDARY both reachable. Skip
# the whole section if not — we'd just be timing out on every assertion.
if (( HOST_REACHABLE[$PRIMARY] == 0 || HOST_REACHABLE[$SECONDARY] == 0 )); then
    skip "echo round-trip needs both $PRIMARY and $SECONDARY reachable"
else
# Pre-flight: every reachable envoy must have ~$ECHO_USER/echo/. The
# agent auto-creates it when the user is enrolled in echo, but on
# first boot it might not exist. mkdir is idempotent.
for h in "${ALL_HOSTS[@]}"; do
    (( HOST_REACHABLE[$h] == 0 )) && continue
    on_host_as_user "$h" "$ECHO_USER" 'mkdir -p $HOME/echo' >/dev/null 2>&1 || true
done

ECHO_NAME="phase-c-echo-$$-$(date +%s).txt"
ECHO_BODY_1="hello from $PRIMARY at $(date +%s)"
ECHO_BODY_2="updated by $SECONDARY at $(date +%s)+1"

step "$PRIMARY: write $ECHO_NAME with body 1"
if on_host_as_user "$PRIMARY" "$ECHO_USER" "printf '%s\n' '$ECHO_BODY_1' > \$HOME/echo/$ECHO_NAME"; then
    pass "wrote ~/echo/$ECHO_NAME on $PRIMARY"
else
    fail "could not write echo file on $PRIMARY"
    exit 1
fi

step "polling for $ECHO_NAME on $SECONDARY (timeout ${ECHO_TIMEOUT}s)"
elapsed=0
synced=0
while (( elapsed < ECHO_TIMEOUT )); do
    if on_host_as_user "$SECONDARY" "$ECHO_USER" "test -f \$HOME/echo/$ECHO_NAME" >/dev/null 2>&1; then
        synced=1
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done
if (( synced == 1 )); then
    pass "$ECHO_NAME appeared on $SECONDARY in ${elapsed}s"
else
    fail "$ECHO_NAME did not appear on $SECONDARY within ${ECHO_TIMEOUT}s"
fi

step "verify content on $SECONDARY matches body 1"
remote_body=$(on_host_as_user "$SECONDARY" "$ECHO_USER" "cat \$HOME/echo/$ECHO_NAME 2>/dev/null" | tr -d '\n')
if [[ "$remote_body" == "$ECHO_BODY_1" ]]; then
    pass "content matches"
else
    fail "content mismatch — got '$remote_body', expected '$ECHO_BODY_1'"
fi

step "$SECONDARY: overwrite $ECHO_NAME with body 2"
if on_host_as_user "$SECONDARY" "$ECHO_USER" "printf '%s\n' '$ECHO_BODY_2' > \$HOME/echo/$ECHO_NAME"; then
    pass "wrote update on $SECONDARY"
else
    fail "could not write update on $SECONDARY"
fi

step "polling for body-2 update on $PRIMARY (timeout ${ECHO_TIMEOUT}s)"
elapsed=0
updated=0
while (( elapsed < ECHO_TIMEOUT )); do
    primary_body=$(on_host_as_user "$PRIMARY" "$ECHO_USER" "cat \$HOME/echo/$ECHO_NAME 2>/dev/null" | tr -d '\n')
    if [[ "$primary_body" == "$ECHO_BODY_2" ]]; then
        updated=1
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done
if (( updated == 1 )); then
    pass "body-2 reflected on $PRIMARY in ${elapsed}s"
else
    fail "body-2 did not reach $PRIMARY within ${ECHO_TIMEOUT}s"
fi

step "$PRIMARY: delete $ECHO_NAME"
if on_host_as_user "$PRIMARY" "$ECHO_USER" "rm -f \$HOME/echo/$ECHO_NAME"; then
    pass "removed ~/echo/$ECHO_NAME on $PRIMARY"
else
    fail "could not delete echo file on $PRIMARY"
fi

step "polling for deletion to reach $SECONDARY (timeout ${ECHO_TIMEOUT}s)"
elapsed=0
deleted=0
while (( elapsed < ECHO_TIMEOUT )); do
    if ! on_host_as_user "$SECONDARY" "$ECHO_USER" "test -f \$HOME/echo/$ECHO_NAME" >/dev/null 2>&1; then
        deleted=1
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done
if (( deleted == 1 )); then
    pass "$ECHO_NAME removed from $SECONDARY in ${elapsed}s"
else
    fail "$ECHO_NAME still present on $SECONDARY after ${ECHO_TIMEOUT}s"
fi
fi  # close: if echo round-trip prerequisites met
