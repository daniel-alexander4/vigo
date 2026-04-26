#!/usr/bin/env bash
#
# substrate-phase-b-livetest.sh
#
# Sections 1–7 of the substrate Phase A + B live test plan. Run-all-and-report
# mode: every assertion is independent, no halt on failure, summary printed at
# end. Exit code is non-zero iff any assertion failed.
#
# Run from the workstation. The script SSHes to envoys and (optionally) to a
# vigocli host. Assumes passwordless SSH + sudo on every target host.
#
# Required for sections 4–7:
#   SIGIL_PASSWORD_FILE  default ~/.sigil-test-password — mode 0600 file
#                        containing the user's sigil password on a single line.
#                        Sections 4–7 are SKIPPED if the file is missing.
#
# Optional env:
#   PRIMARY        default danlap   — cairn primary, sigil recipient watcher
#   SECONDARY      default annlap   — sigil leave/evict subject
#   THIRD          default plex     — additional sigil peer (evicted in §5)
#   ABSENT         default girlslaptop — host expected to fail evict gracefully
#   SIGIL_USER     default dan
#   VIGOCLI_CMD    default vigocli  — full prefix; e.g. "ssh server vigocli"
#                                       or "docker exec vigo vigocli"

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
ABSENT="${ABSENT:-girlslaptop}"
SIGIL_USER="${SIGIL_USER:-dan}"
SIGIL_PASSWORD_FILE="${SIGIL_PASSWORD_FILE:-$HOME/.sigil-test-password}"
VIGOCLI_CMD="${VIGOCLI_CMD:-vigocli}"

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
pass()    { PASS=$((PASS+1));   printf '%s  ✓ %s%s\n' "$GREEN" "$*" "$RESET"; }
fail()    { FAIL=$((FAIL+1));   FAILURES+=("$*"); printf '%s  ✗ %s%s\n' "$RED" "$*" "$RESET"; }
skip()    { SKIP=$((SKIP+1));   printf '%s  ⊘ %s%s\n' "$YELLOW" "$*" "$RESET"; }
warn()    { printf '%s  ! %s%s\n' "$YELLOW" "$*" "$RESET"; }

# Run a vigocli command via the configured prefix (which may be ssh/docker).
vigocli() { $VIGOCLI_CMD "$@"; }

# Assertion: command exits 0.
expect_zero() {
    local desc=$1; shift
    if "$@" >/tmp/livetest.out 2>&1; then
        pass "$desc"
    else
        local rc=$?
        fail "$desc (exit $rc) — $(head -2 /tmp/livetest.out | tr '\n' ' ')"
    fi
}

# Assertion: command exits non-zero.
expect_nonzero() {
    local desc=$1; shift
    if "$@" >/tmp/livetest.out 2>&1; then
        fail "$desc — expected non-zero, got 0"
    else
        pass "$desc"
    fi
}

# Assertion: command output (stdout+stderr) matches a basic-regex pattern.
expect_match() {
    local desc=$1 pattern=$2; shift 2
    local out
    out=$("$@" 2>&1) || true
    if grep -qE "$pattern" <<<"$out"; then
        pass "$desc"
    else
        fail "$desc — pattern '$pattern' not in output: $(head -2 <<<"$out" | tr '\n' ' ')"
    fi
}

# Run a command on a remote host via ssh. The remote shell will expand
# anything not pre-quoted locally — pass single-quoted strings to defer
# expansion to the remote.
on_host() { ssh "$1" "$2"; }

# Re-enroll a user on a host using the password from $PW.
reenroll() {
    local host=$1
    if printf '%s\n' "$PW" | ssh "$host" 'vigo sigil enroll' >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

cleanup() {
    section "Cleanup"
    if [[ -n "${PW:-}" ]]; then
        for h in "$SECONDARY" "$THIRD"; do
            if ssh "$h" 'test -f $HOME/.sigil/identity.age' >/dev/null 2>&1; then
                note "$h still enrolled — no restore needed"
            elif reenroll "$h"; then
                note "$h re-enrolled"
            else
                warn "$h re-enroll failed — manual restore required"
            fi
        done
    else
        note "no sigil password loaded; skipping enrollment restore"
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
HAVE_PASSWORD=0
PW=""
if [[ -r "$SIGIL_PASSWORD_FILE" ]]; then
    mode=$(stat -c %a "$SIGIL_PASSWORD_FILE")
    if [[ "$mode" != "600" ]]; then
        fail "$SIGIL_PASSWORD_FILE must be mode 600 (currently $mode)"
        exit 1
    fi
    PW=$(<"$SIGIL_PASSWORD_FILE")
    HAVE_PASSWORD=1
    note "loaded sigil password from $SIGIL_PASSWORD_FILE"
else
    note "no $SIGIL_PASSWORD_FILE — sections 4–7 will be skipped"
fi

note "PRIMARY=$PRIMARY  SECONDARY=$SECONDARY  THIRD=$THIRD  ABSENT=$ABSENT"
note "vigocli prefix: $VIGOCLI_CMD"

# ───────────────────────── 1. Phase A — cairn source CLI ─────────────────────────
section "1. Phase A — cairn source CLI (on $PRIMARY as $SIGIL_USER)"

step "preparing test repo on $PRIMARY"
on_host "$PRIMARY" 'mkdir -p $HOME/cairn/sourcetest && cd $HOME/cairn/sourcetest && git init -q && echo x > x && git add . && git commit -qm init' \
    && pass "test repo created" \
    || fail "test repo creation failed"

expect_zero "vigo cairn source add" \
    on_host "$PRIMARY" "vigo cairn source add \$HOME/cairn/sourcetest --tags smoke --description 'phase A test'"

expect_match ".gitignore contains .cairn.yaml" '\.cairn\.yaml' \
    on_host "$PRIMARY" 'cat $HOME/cairn/sourcetest/.gitignore'

expect_match "show reports owner=$PRIMARY" "$PRIMARY" \
    on_host "$PRIMARY" 'vigo cairn source show sourcetest'

expect_zero "source pause" on_host "$PRIMARY" 'vigo cairn source pause sourcetest'
sleep 6
expect_match "list shows sourcetest paused" 'sourcetest.*y(es)?' \
    on_host "$PRIMARY" 'vigo cairn source list'
expect_zero "source resume" on_host "$PRIMARY" 'vigo cairn source resume sourcetest'

expect_zero "validate good yaml" \
    on_host "$PRIMARY" 'vigo cairn source validate $HOME/cairn/sourcetest/.cairn.yaml'

on_host "$PRIMARY" 'echo "garbage: yes" > /tmp/bad.yaml' >/dev/null
expect_nonzero "validate rejects garbage yaml" \
    on_host "$PRIMARY" 'vigo cairn source validate /tmp/bad.yaml'

expect_zero "source remove --yes" \
    on_host "$PRIMARY" 'vigo cairn source remove sourcetest --yes'
expect_nonzero "sidecar gone after remove" \
    on_host "$PRIMARY" 'test -f $HOME/cairn/sourcetest/.cairn.yaml'

# ───────────────────────── 2. Phase B — vigo cairn leave ─────────────────────────
section "2. Phase B — vigo cairn leave (on $PRIMARY as $SIGIL_USER)"

for n in leave1 leave2; do
    on_host "$PRIMARY" "mkdir -p \$HOME/cairn/$n && cd \$HOME/cairn/$n && git init -q && echo $n > x && git add . && git commit -qm $n" >/dev/null
    expect_zero "add source $n" \
        on_host "$PRIMARY" "vigo cairn source add \$HOME/cairn/$n --no-gitignore"
done

expect_zero "leave --pause --yes" on_host "$PRIMARY" 'vigo cairn leave --pause --yes'
expect_match "both sidecars paused" 'paused: true' \
    on_host "$PRIMARY" 'grep ^paused $HOME/cairn/leave1/.cairn.yaml $HOME/cairn/leave2/.cairn.yaml'

expect_zero "resume leave1" on_host "$PRIMARY" 'vigo cairn source resume leave1'

expect_zero "leave --yes (delete)" on_host "$PRIMARY" 'vigo cairn leave --yes'
expect_nonzero "leave1 sidecar gone" on_host "$PRIMARY" 'test -f $HOME/cairn/leave1/.cairn.yaml'
expect_nonzero "leave2 sidecar gone" on_host "$PRIMARY" 'test -f $HOME/cairn/leave2/.cairn.yaml'

on_host "$PRIMARY" 'rm -rf $HOME/cairn/leave1 $HOME/cairn/leave2' >/dev/null

# ───────────────────────── 3. Phase B — cairn prune + vigocli evict ─────────────────────────
section "3. Phase B — cairn prune --seeder + vigocli cairn evict"

expect_zero "$SECONDARY: vigo cairn list --seeder $PRIMARY" \
    on_host "$SECONDARY" "vigo cairn list --seeder $PRIMARY"

expect_zero "$SECONDARY: prune --seeder (lists candidates)" \
    on_host "$SECONDARY" "sudo vigo cairn prune --seeder $PRIMARY"

expect_zero "$SECONDARY: prune --seeder --yes" \
    on_host "$SECONDARY" "sudo vigo cairn prune --seeder $PRIMARY --yes"

expect_zero "vigocli cairn members" vigocli cairn members
expect_zero "vigocli cairn evict --host $ABSENT" vigocli cairn evict --host "$ABSENT" --yes
sleep 10
expect_zero "vigocli cairn status" vigocli cairn status

# ───────────────────────── sections 4–7 require sigil password ─────────────────────────
if (( HAVE_PASSWORD == 0 )); then
    for s in "4. vigo sigil leave" "5. vigocli sigil evict" "6. sigil leave --purge" "7. DELETE tombstone regression"; do
        section "$s"
        skip "no SIGIL_PASSWORD_FILE — section skipped"
    done
    exit 0
fi

# ───────────────────────── 4. Phase B — vigo sigil leave ─────────────────────────
section "4. Phase B — vigo sigil leave (on $SECONDARY as $SIGIL_USER)"

expect_zero "$SECONDARY: sigil status" on_host "$SECONDARY" 'vigo sigil status'

on_host "$SECONDARY" 'echo phase-b > /tmp/test.txt' >/dev/null
expect_zero "$SECONDARY: encrypt smoketest.age" \
    on_host "$SECONDARY" 'vigo sigil encrypt /tmp/test.txt --as smoketest.age'

before=$(on_host "$PRIMARY" 'wc -l < $HOME/.sigil/recipients.txt' 2>/dev/null || echo 0)
note "$PRIMARY recipients before leave: $before"

expect_zero "$SECONDARY: sigil leave --yes" on_host "$SECONDARY" 'vigo sigil leave --yes'
# leave.pending is checked immediately (before the daemon picks it up)
expect_zero "leave.pending sentinel exists immediately" \
    on_host "$SECONDARY" 'test -f $HOME/.sigil/leave.pending'

note "waiting 35s for daemon to process leave + fan out LEAVE envelope"
sleep 35

expect_nonzero "$SECONDARY: ~/.sigil/ scrubbed (default leave)" \
    on_host "$SECONDARY" 'test -d $HOME/.sigil'
expect_zero "$SECONDARY: ~/sigil/ ciphertext kept (orphaned)" \
    on_host "$SECONDARY" 'test -d $HOME/sigil'

after=$(on_host "$PRIMARY" 'wc -l < $HOME/.sigil/recipients.txt' 2>/dev/null || echo 0)
note "$PRIMARY recipients after leave: $after"
if (( after < before )); then
    pass "$PRIMARY recipients shrank ($before → $after)"
else
    fail "$PRIMARY recipients did not shrink ($before → $after)"
fi

expect_zero "$PRIMARY: reencrypt.pending sentinel" \
    on_host "$PRIMARY" 'test -f $HOME/.sigil/reencrypt.pending'

step "$PRIMARY: unlock + drain reencrypt sentinel"
if printf '%s\n' "$PW" | ssh "$PRIMARY" 'vigo sigil unlock' >/dev/null 2>&1; then
    pass "$PRIMARY unlock succeeded"
else
    fail "$PRIMARY unlock failed"
fi
sleep 5
expect_nonzero "$PRIMARY: reencrypt.pending cleared after unlock" \
    on_host "$PRIMARY" 'test -f $HOME/.sigil/reencrypt.pending'

step "restoring $SECONDARY enrollment"
if reenroll "$SECONDARY"; then pass "$SECONDARY re-enrolled"; else fail "$SECONDARY re-enroll failed"; fi

# ───────────────────────── 5. Phase B — vigocli sigil evict ─────────────────────────
section "5. Phase B — vigocli sigil evict (server host)"

expect_zero "vigocli sigil members $SIGIL_USER" vigocli sigil members "$SIGIL_USER"

expect_zero "vigocli sigil evict --host $THIRD --user $SIGIL_USER" \
    vigocli sigil evict --host "$THIRD" --user "$SIGIL_USER" --yes
sleep 5
if on_host "$PRIMARY" "grep -q $THIRD \$HOME/.sigil/recipients.txt" >/dev/null 2>&1; then
    fail "$THIRD pubkey still in $PRIMARY recipients.txt"
else
    pass "$THIRD pubkey gone from $PRIMARY recipients.txt"
fi

# Known fleet-projection gap: girlslaptop. Either succeeds (if fleet has it)
# or fails with the 'not enrolled in any matching sigil circle' error. Both
# outcomes are acceptable here — we just want it to not crash the server.
step "vigocli sigil evict --host $ABSENT --all-users (may report 'not enrolled')"
if vigocli sigil evict --host "$ABSENT" --all-users --yes >/tmp/livetest.out 2>&1; then
    pass "evict $ABSENT succeeded"
else
    if grep -qi 'not enrolled\|no matching\|not found' /tmp/livetest.out; then
        pass "evict $ABSENT correctly reported absent host"
    else
        fail "evict $ABSENT failed unexpectedly: $(head -2 /tmp/livetest.out | tr '\n' ' ')"
    fi
fi

step "restoring $THIRD enrollment"
if reenroll "$THIRD"; then pass "$THIRD re-enrolled"; else fail "$THIRD re-enroll failed"; fi

# ───────────────────────── 6. --purge variant ─────────────────────────
section "6. --purge variant on sigil leave (on $SECONDARY)"

# Make sure secondary is enrolled before the test
if ! on_host "$SECONDARY" 'test -f $HOME/.sigil/identity.age' >/dev/null 2>&1; then
    if reenroll "$SECONDARY"; then note "re-enrolled $SECONDARY for §6"; else fail "could not enroll $SECONDARY for §6"; fi
fi

on_host "$SECONDARY" 'echo secret > /tmp/s.txt && vigo sigil encrypt /tmp/s.txt' >/dev/null 2>&1
expect_zero "$SECONDARY: ~/sigil has content" \
    on_host "$SECONDARY" '[ -n "$(ls $HOME/sigil/ 2>/dev/null)" ]'

expect_zero "$SECONDARY: sigil leave --purge --yes" \
    on_host "$SECONDARY" 'vigo sigil leave --purge --yes'
note "waiting 35s for daemon to process purge"
sleep 35
expect_nonzero "$SECONDARY: ~/.sigil/ gone after --purge" \
    on_host "$SECONDARY" 'test -d $HOME/.sigil'
expect_nonzero "$SECONDARY: ~/sigil/ gone after --purge" \
    on_host "$SECONDARY" 'test -d $HOME/sigil'

# ───────────────────────── 7. Regression — DELETE tombstone path ─────────────────────────
section "7. Regression — DELETE tombstone path (on $SECONDARY)"

if reenroll "$SECONDARY"; then note "$SECONDARY re-enrolled for §7"; else fail "could not enroll $SECONDARY for §7"; fi

on_host "$SECONDARY" 'echo regress > /tmp/r.txt && vigo sigil encrypt /tmp/r.txt --as regress.age' >/dev/null 2>&1
expect_zero "$SECONDARY: delete regress.age" \
    on_host "$SECONDARY" 'vigo sigil delete regress.age'
note "waiting 35s for tombstone fan-out"
sleep 35
expect_match "tombstone propagated to $PRIMARY" 'regress\.age' \
    on_host "$PRIMARY" 'vigo sigil list --tombstones'
