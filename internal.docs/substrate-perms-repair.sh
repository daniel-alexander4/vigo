#!/usr/bin/env bash
#
# substrate-perms-repair.sh
#
# One-shot audit + repair for sigil files left in `root:root 0644`
# state by pre-0.30.24 binaries. The fixes that make this possible
# shipped across 0.30.24–0.30.28; each redeploy left some surface
# already-correct on writes that flow through the new helpers, but
# pre-existing root-owned files persist until a verb rewrites them.
#
# Two repair categories:
#
#   1. Automatic — root-owned dotfile sentinels + dirs. The audit
#      finds them; with --repair, this script chowns + chmods them
#      in place. Cheap and non-interactive.
#
#   2. Interactive — root-owned ciphertext under ~/<user>/sigil/.
#      Only `vigo sigil reencrypt` rewrites these (decrypts via the
#      session helper, re-encrypts to current recipients, writes via
#      finalize_user_ciphertext which chowns to the user). That
#      needs a live `vigo sigil unlock`, which prompts for the sigil
#      password — this script can't drive it without holding the
#      password in scope. The script prints the exact per-envoy
#      commands and stops there.
#
# Run: ./substrate-perms-repair.sh             # audit only
#      ./substrate-perms-repair.sh --repair    # automatic repair + manual hint
#
# Optional env:
#   PRIMARY    danlap
#   SECONDARY  annlap
#   THIRD      plex
#   SIGIL_USER dan
#   VIGO_BIN   /usr/local/sbin/vigo

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
SIGIL_USER="${SIGIL_USER:-dan}"
VIGO_BIN="${VIGO_BIN:-/usr/local/sbin/vigo}"

REPAIR=0
case "${1:-}" in
    --repair) REPAIR=1 ;;
    "" ) ;;
    *) echo "usage: $0 [--repair]"; exit 2 ;;
esac

GREEN=$'\033[38;2;166;227;161m'
RED=$'\033[38;2;243;139;168m'
YELLOW=$'\033[38;2;249;226;175m'
BLUE=$'\033[38;2;137;180;250m'
LAV=$'\033[38;2;180;190;254m'
MAUVE=$'\033[38;2;203;166;247m'
RESET=$'\033[0m'

section() { printf '\n%s━━━ %s ━━━%s\n' "$MAUVE" "$*" "$RESET"; }
note()    { printf '%s    · %s%s\n' "$LAV" "$*" "$RESET"; }
ok()      { printf '%s  ✓ %s%s\n' "$GREEN" "$*" "$RESET"; }
bad()     { printf '%s  ✗ %s%s\n' "$RED" "$*" "$RESET"; }
warn()    { printf '%s  ! %s%s\n' "$YELLOW" "$*" "$RESET"; }
hint()    { printf '%s  → %s%s\n' "$BLUE" "$*" "$RESET"; }

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

ALL_HOSTS=("$PRIMARY" "$SECONDARY" "$THIRD")
declare -A CIPHERTEXT_REPAIR_NEEDED
declare -A SENTINEL_REPAIR_NEEDED

# ─────────── audit ───────────

for h in "${ALL_HOSTS[@]}"; do
    section "$h"
    if ! on_host "$h" "true" >/dev/null 2>&1; then
        warn "unreachable, skipping"
        continue
    fi

    # List every file under ~/.sigil/ and ~/sigil/ that's owned by root.
    # The expected end-state after 0.30.24+ deploy is: every file is
    # owned by SIGIL_USER. Pre-existing files predate the fix.
    bad_files=$(on_host "$h" "sudo find /home/$SIGIL_USER/.sigil /home/$SIGIL_USER/sigil -maxdepth 1 -type f \\( -uid 0 -o -gid 0 \\) -printf '%M %u %g %p\n' 2>/dev/null")
    bad_dirs=$(on_host "$h" "sudo find /home/$SIGIL_USER/.sigil /home/$SIGIL_USER/sigil -maxdepth 0 -type d \\( -uid 0 -o -gid 0 \\) -printf '%M %u %g %p\n' 2>/dev/null")

    if [[ -z "$bad_files$bad_dirs" ]]; then
        ok "all files + dirs already owned by $SIGIL_USER"
        continue
    fi

    if [[ -n "$bad_dirs" ]]; then
        echo "$bad_dirs" | while IFS= read -r line; do
            bad "$line"
        done
    fi
    if [[ -n "$bad_files" ]]; then
        echo "$bad_files" | while IFS= read -r line; do
            bad "$line"
        done
    fi

    # Categorize: sentinels + dirs are auto-repairable; ciphertext
    # under ~/sigil/ needs reencrypt.
    cipher_count=$(echo "$bad_files" | grep -c "/sigil/" || true)
    sentinel_count=$(echo "$bad_files" | grep -c "/.sigil/" || true)
    dir_count=$(echo "$bad_dirs" | grep -c "." || true)
    if (( cipher_count > 0 )); then
        CIPHERTEXT_REPAIR_NEEDED[$h]=$cipher_count
    fi
    if (( sentinel_count + dir_count > 0 )); then
        SENTINEL_REPAIR_NEEDED[$h]=$((sentinel_count + dir_count))
    fi
done

# ─────────── automatic repair (no password) ───────────

if (( REPAIR == 1 )); then
    section "Automatic repair (sentinels + dirs)"
    if (( ${#SENTINEL_REPAIR_NEEDED[@]} == 0 )); then
        ok "no sentinels or dirs need repair"
    else
        for h in "${!SENTINEL_REPAIR_NEEDED[@]}"; do
            note "$h: chown ~/.sigil/ + ~/sigil/ tree to $SIGIL_USER"
            on_host "$h" "sudo chown -R $SIGIL_USER:$SIGIL_USER /home/$SIGIL_USER/.sigil; sudo chown $SIGIL_USER:$SIGIL_USER /home/$SIGIL_USER/sigil 2>/dev/null; sudo chmod 0600 /home/$SIGIL_USER/.sigil/reencrypt.pending 2>/dev/null"
            ok "$h: chowned"
        done
    fi
fi

# ─────────── manual repair (interactive password) ───────────

if (( ${#CIPHERTEXT_REPAIR_NEEDED[@]} > 0 )); then
    section "Manual repair required (interactive password)"
    note "These envoys have root-owned ciphertext under ~$SIGIL_USER/sigil/."
    note "Run on EACH envoy listed (the password prompt is local)."
    note "The env wrapper preserves HOME so verbs work on pre-0.30.26 binaries."
    echo
    for h in "${!CIPHERTEXT_REPAIR_NEEDED[@]}"; do
        cnt=${CIPHERTEXT_REPAIR_NEEDED[$h]}
        echo "$h ($cnt files):"
        if is_local "$h"; then
            hint "sudo env HOME=/home/$SIGIL_USER LOGNAME=$SIGIL_USER USER=$SIGIL_USER $VIGO_BIN sigil unlock"
            hint "sudo env HOME=/home/$SIGIL_USER LOGNAME=$SIGIL_USER USER=$SIGIL_USER $VIGO_BIN sigil reencrypt"
        else
            hint "ssh -t $h 'sudo env HOME=/home/$SIGIL_USER LOGNAME=$SIGIL_USER USER=$SIGIL_USER $VIGO_BIN sigil unlock && sudo env HOME=/home/$SIGIL_USER LOGNAME=$SIGIL_USER USER=$SIGIL_USER $VIGO_BIN sigil reencrypt'"
        fi
        echo
    done
    note "After running on each envoy, re-run this script (no flags) to confirm."
fi

if (( REPAIR == 0 && (${#SENTINEL_REPAIR_NEEDED[@]} + ${#CIPHERTEXT_REPAIR_NEEDED[@]}) == 0 )); then
    section "Result"
    ok "no repair needed across the fleet"
elif (( REPAIR == 0 )); then
    section "Result"
    note "audit-only run. Re-run with --repair to fix sentinels + dirs automatically."
fi
