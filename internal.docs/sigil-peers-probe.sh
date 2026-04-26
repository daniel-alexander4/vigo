#!/usr/bin/env bash
#
# sigil-peers-probe.sh
#
# Follow-up to sigil-tombstone-probe.sh. The first probe established that
# propagation is broken end-to-end (push fan-out + tombstone gossip both
# fail) for the FRESH state, while EARLIER content (smoketest.age,
# vigo-lic-backup.* tombstone) successfully propagated. The hypothesis is
# that re-enrollment of $SECONDARY in §7 of the livetest left peer-state
# inconsistent between the two sides.
#
# This probe dumps the relevant on-disk state on BOTH hosts so we can see:
#   • peers.json     — does each side know about the other? what pubkey?
#   • leaves.json    — has anyone been LEFT and never restored?
#   • recipients.txt — how many recipients on each side, what hostnames?
#   • identity.age   — does each side have a current local identity?
#   • announce traffic — recent journalctl evidence of sigil announces.
#
# Read-only. Doesn't write or modify anything.
#
# Run from the workstation. Same env vars as the other livetest scripts.

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
SIGIL_USER="${SIGIL_USER:-dan}"

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

dump_host() {
    local host=$1
    section "Host: $host"

    local state_dir="/var/lib/vigo/sigil/$SIGIL_USER"
    local home_dir="\$HOME/.sigil"

    step "$host: $state_dir/peers.json"
    local peers
    peers=$(on_host "$host" "sudo cat $state_dir/peers.json 2>&1" || true)
    if grep -qiE 'no such|cannot access' <<<"$peers"; then
        bad "peers.json missing"
    else
        # Pretty-print and show pubkey + last_seen per hostname
        printf '%s\n' "$peers" | while IFS= read -r line; do note "$line"; done
    fi

    step "$host: $state_dir/leaves.json"
    local leaves
    leaves=$(on_host "$host" "sudo cat $state_dir/leaves.json 2>&1" || true)
    if grep -qiE 'no such|cannot access' <<<"$leaves"; then
        note "(no leaves.json — never had a LEAVE recorded)"
    else
        printf '%s\n' "$leaves" | while IFS= read -r line; do note "$line"; done
    fi

    step "$host: $home_dir/recipients.txt (line count + hostnames if listed)"
    local rcpt
    rcpt=$(on_host "$host" "cat $home_dir/recipients.txt 2>&1" || true)
    local count
    count=$(grep -cE '^age1' <<<"$rcpt" 2>/dev/null || echo 0)
    note "recipient lines: $count"
    # recipients.txt usually has comments naming each hostname above its key
    printf '%s\n' "$rcpt" | head -40 | while IFS= read -r line; do note "$line"; done

    step "$host: $home_dir/identity.age (existence + age)"
    on_host "$host" "ls -la $home_dir/identity.age 2>&1" | while IFS= read -r line; do note "$line"; done

    step "$host: recent sigil announce log lines (last 10 min)"
    local logs
    logs=$(on_host "$host" "sudo journalctl -u vigo-envoy --since '10 min ago' --no-pager 2>&1 | grep -iE 'sigil.*announce|sigil.*peer|sigil.*pubkey rotation|sigil.*push|sigil.*tombstone' | tail -25" || true)
    if [[ -z "$logs" ]] || grep -qiE '^--' <<<"$logs"; then
        note "(no matching log lines in last 10 min)"
    else
        printf '%s\n' "$logs" | while IFS= read -r line; do note "$line"; done
    fi
}

dump_host "$PRIMARY"
dump_host "$SECONDARY"

section "Cross-check"

# Pull each side's local pubkey identity (the public part of identity.age
# unwrapped) by reading /var/lib/vigo/sigil/<user>/local_pubkey or whatever
# the daemon caches. We cheat: the recipient line for the local envoy is
# the first/only line on a fresh enrollment.
get_self_pubkey() {
    local host=$1
    on_host "$host" "head -1 \$HOME/.sigil/recipients.txt 2>/dev/null | grep -oE 'age1[a-z0-9]+'" 2>&1 | tr -d '\r\n'
}

P_PUB=$(get_self_pubkey "$PRIMARY")
S_PUB=$(get_self_pubkey "$SECONDARY")
note "$PRIMARY local pubkey:  ${P_PUB:-<missing>}"
note "$SECONDARY local pubkey: ${S_PUB:-<missing>}"

# Does PRIMARY's peers.json contain SECONDARY's current pubkey?
step "does $PRIMARY's peers.json contain $SECONDARY's CURRENT pubkey?"
if [[ -n "$S_PUB" ]]; then
    if on_host "$PRIMARY" "sudo grep -q '$S_PUB' /var/lib/vigo/sigil/$SIGIL_USER/peers.json 2>/dev/null"; then
        ok "yes — $PRIMARY knows $SECONDARY's current pubkey"
    else
        bad "NO — $PRIMARY's peers.json does not contain $SECONDARY's current pubkey"
        note "this is the smoking gun: announce isn't reaching $PRIMARY,"
        note "or $PRIMARY's reactor isn't upserting the rotated key."
    fi
else
    warn "could not extract $SECONDARY local pubkey — skipping check"
fi

# And the symmetric check
step "does $SECONDARY's peers.json contain $PRIMARY's CURRENT pubkey?"
if [[ -n "$P_PUB" ]]; then
    if on_host "$SECONDARY" "sudo grep -q '$P_PUB' /var/lib/vigo/sigil/$SIGIL_USER/peers.json 2>/dev/null"; then
        ok "yes — $SECONDARY knows $PRIMARY's current pubkey"
    else
        bad "NO — $SECONDARY's peers.json does not contain $PRIMARY's current pubkey"
        note "this would explain 'Encrypted to 1 recipient(s)' — $SECONDARY's"
        note "recipient regeneration only includes peers in peers.json."
    fi
else
    warn "could not extract $PRIMARY local pubkey — skipping check"
fi
