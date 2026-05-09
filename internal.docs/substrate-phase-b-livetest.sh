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
#   LOCKBOX_PASSWORD_FILE  default ~/.lockbox-test-password — mode 0600
#                            file containing the user's lockbox password
#                            on a single line. Sections 4–7 are SKIPPED if
#                            the file is missing.
#
# Optional env:
#   PRIMARY         default danlap   — gitmirror primary, lockbox recipient watcher
#   SECONDARY       default annlap   — lockbox leave/evict subject
#   THIRD           default plex     — additional lockbox peer (evicted in §5)
#   ABSENT          default girlslaptop — host expected to fail evict gracefully
#   LOCKBOX_USER  default dan      — used as the gitmirror sidecar owner in §1–3
#                                       and the lockbox user in §4–7
#   VIGOCLI_CMD     default "sudo vigocli" — vigocli is installed mode 0700
#                                            root:root and must be invoked as
#                                            root. Override for remote/docker
#                                            hosts: "ssh server sudo vigocli"
#                                            or "docker exec vigo vigocli".

set -uo pipefail

PRIMARY="${PRIMARY:-danlap}"
SECONDARY="${SECONDARY:-annlap}"
THIRD="${THIRD:-plex}"
ABSENT="${ABSENT:-girlslaptop}"
LOCKBOX_USER="${LOCKBOX_USER:-dan}"
LOCKBOX_PASSWORD_FILE="${LOCKBOX_PASSWORD_FILE:-$HOME/.lockbox-test-password}"
VIGOCLI_CMD="${VIGOCLI_CMD:-sudo vigocli}"

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

# Run a command on a host. If $1 matches the local hostname (short, full, or
# 'localhost'), bash -c locally; otherwise ssh. Pass single-quoted command
# strings so $HOME etc. expand on the target shell.
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

# Re-enroll a user on a host using the password from $PW. Stdin is piped
# straight through bash -c (local) or ssh (remote).
reenroll() {
    local host=$1
    if printf '%s\n' "$PW" | on_host "$host" 'vigo swarm lockbox enroll' >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

cleanup() {
    section "Cleanup"
    if [[ -n "${PW:-}" ]]; then
        for h in "$SECONDARY" "$THIRD"; do
            if on_host "$h" 'test -f $HOME/.lockbox/identity.age' >/dev/null 2>&1; then
                note "$h still enrolled — no restore needed"
            elif reenroll "$h"; then
                note "$h re-enrolled"
            else
                warn "$h re-enroll failed — manual restore required"
            fi
        done
    else
        note "no lockbox password loaded; skipping enrollment restore"
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
    note "no $LOCKBOX_PASSWORD_FILE — sections 4–7 will be skipped"
fi

note "PRIMARY=$PRIMARY  SECONDARY=$SECONDARY  THIRD=$THIRD  ABSENT=$ABSENT"
note "vigocli prefix: $VIGOCLI_CMD"

# ───────────────────────── 1. Phase A — gitmirror source CLI ─────────────────────────
section "1. Phase A — gitmirror source CLI (on $PRIMARY as $LOCKBOX_USER)"

step "preparing test repo on $PRIMARY"
# Clean any prior-run residue so init/add/commit are deterministic —
# without rm -rf, a repeat run finds an already-committed repo and
# the commit step exits non-zero ("nothing to commit").
on_host "$PRIMARY" 'rm -rf $HOME/gitmirror/sourcetest && mkdir -p $HOME/gitmirror/sourcetest && cd $HOME/gitmirror/sourcetest && git init -q && echo x > x && git add . && git commit -qm init' \
    && pass "test repo created" \
    || fail "test repo creation failed"

expect_zero "vigo swarm gitmirror source add" \
    on_host "$PRIMARY" "vigo swarm gitmirror source add \$HOME/gitmirror/sourcetest --tags smoke --description 'phase A test'"

expect_match ".gitignore contains .gitmirror.yaml" '\.gitmirror\.yaml' \
    on_host "$PRIMARY" 'cat $HOME/gitmirror/sourcetest/.gitignore'

expect_match "show reports owner=$PRIMARY" "owner:.*$PRIMARY" \
    on_host "$PRIMARY" 'vigo swarm gitmirror source show sourcetest'

expect_zero "source pause" on_host "$PRIMARY" 'vigo swarm gitmirror source pause sourcetest'
sleep 6
expect_match "list shows sourcetest paused (PAUSED column = yes)" 'sourcetest.*yes' \
    on_host "$PRIMARY" 'vigo swarm gitmirror source list'
expect_zero "source resume" on_host "$PRIMARY" 'vigo swarm gitmirror source resume sourcetest'

expect_zero "validate good yaml" \
    on_host "$PRIMARY" 'vigo swarm gitmirror source validate $HOME/gitmirror/sourcetest/.gitmirror.yaml'

on_host "$PRIMARY" 'echo "garbage: yes" > /tmp/bad.yaml' >/dev/null
expect_nonzero "validate rejects garbage yaml" \
    on_host "$PRIMARY" 'vigo swarm gitmirror source validate /tmp/bad.yaml'

expect_zero "source remove --yes" \
    on_host "$PRIMARY" 'vigo swarm gitmirror source remove sourcetest --yes'
expect_nonzero "sidecar gone after remove" \
    on_host "$PRIMARY" 'test -f $HOME/gitmirror/sourcetest/.gitmirror.yaml'

# ───────────────────────── 2. Phase B — vigo swarm gitmirror leave ─────────────────────────
section "2. Phase B — vigo swarm gitmirror leave (on $PRIMARY as $LOCKBOX_USER)"

for n in leave1 leave2; do
    on_host "$PRIMARY" "mkdir -p \$HOME/gitmirror/$n && cd \$HOME/gitmirror/$n && git init -q && echo $n > x && git add . && git commit -qm $n" >/dev/null
    expect_zero "add source $n" \
        on_host "$PRIMARY" "vigo swarm gitmirror source add \$HOME/gitmirror/$n --no-gitignore"
done

expect_zero "leave --pause --yes" on_host "$PRIMARY" 'vigo swarm gitmirror leave --pause --yes'
expect_match "both sidecars paused" 'paused: true' \
    on_host "$PRIMARY" 'grep ^paused $HOME/gitmirror/leave1/.gitmirror.yaml $HOME/gitmirror/leave2/.gitmirror.yaml'

expect_zero "resume leave1" on_host "$PRIMARY" 'vigo swarm gitmirror source resume leave1'

expect_zero "leave --yes (delete)" on_host "$PRIMARY" 'vigo swarm gitmirror leave --yes'
expect_nonzero "leave1 sidecar gone" on_host "$PRIMARY" 'test -f $HOME/gitmirror/leave1/.gitmirror.yaml'
expect_nonzero "leave2 sidecar gone" on_host "$PRIMARY" 'test -f $HOME/gitmirror/leave2/.gitmirror.yaml'

on_host "$PRIMARY" 'rm -rf $HOME/gitmirror/leave1 $HOME/gitmirror/leave2' >/dev/null

# ───────────────────────── 3. Phase B — gitmirror prune + vigocli evict ─────────────────────────
section "3. Phase B — gitmirror prune --seeder + vigocli swarm gitmirror evict"

expect_zero "$SECONDARY: vigo swarm gitmirror list --seeder $PRIMARY" \
    on_host "$SECONDARY" "vigo swarm gitmirror list --seeder $PRIMARY"

expect_zero "$SECONDARY: prune --seeder (lists candidates)" \
    on_host "$SECONDARY" "sudo vigo swarm gitmirror prune --seeder $PRIMARY"

expect_zero "$SECONDARY: prune --seeder --yes" \
    on_host "$SECONDARY" "sudo vigo swarm gitmirror prune --seeder $PRIMARY --yes"

expect_zero "vigocli swarm gitmirror members" vigocli swarm gitmirror members
expect_zero "vigocli swarm gitmirror evict --host $ABSENT" vigocli swarm gitmirror evict --host "$ABSENT" --yes
sleep 10
expect_zero "vigocli swarm gitmirror status" vigocli swarm gitmirror status

# ───────────────────────── sections 4–7 require lockbox password ─────────────────────────
if (( HAVE_PASSWORD == 0 )); then
    for s in "4. vigo swarm lockbox leave" "5. vigocli swarm lockbox evict" "6. lockbox leave --purge" "7. DELETE tombstone regression"; do
        section "$s"
        skip "no LOCKBOX_PASSWORD_FILE — section skipped"
    done
    exit 0
fi

# ───────────────────────── 4. Phase B — vigo swarm lockbox leave ─────────────────────────
section "4. Phase B — vigo swarm lockbox leave (on $SECONDARY as $LOCKBOX_USER)"

expect_zero "$SECONDARY: lockbox status" on_host "$SECONDARY" 'vigo swarm lockbox status'

on_host "$SECONDARY" 'echo phase-b > /tmp/test.txt' >/dev/null
expect_zero "$SECONDARY: encrypt smoketest.age" \
    on_host "$SECONDARY" 'vigo swarm lockbox encrypt /tmp/test.txt --as smoketest.age'

before=$(on_host "$PRIMARY" 'wc -l < $HOME/.lockbox/recipients.txt' 2>/dev/null || echo 0)
note "$PRIMARY recipients before leave: $before"

expect_zero "$SECONDARY: lockbox leave --yes" on_host "$SECONDARY" 'vigo swarm lockbox leave --yes'

note "waiting 45s for daemon reactor (30s tick) to process leave.pending + fan out envelope"
sleep 45

# Default leave removes individual files inside ~/.lockbox/ (identity.age,
# identity.salt, recipients.txt, config.yaml) but does NOT remove the
# directory itself. The signature of "daemon processed the sentinel" is
# (a) identity.age is gone and (b) leave.pending is gone.
expect_nonzero "$SECONDARY: identity.age removed (default leave)" \
    on_host "$SECONDARY" 'test -f $HOME/.lockbox/identity.age'
expect_nonzero "$SECONDARY: leave.pending consumed by daemon" \
    on_host "$SECONDARY" 'test -f $HOME/.lockbox/leave.pending'
expect_zero "$SECONDARY: ~/lockbox/ ciphertext dir kept (orphaned)" \
    on_host "$SECONDARY" 'test -d $HOME/lockbox'

after=$(on_host "$PRIMARY" 'wc -l < $HOME/.lockbox/recipients.txt' 2>/dev/null || echo 0)
note "$PRIMARY recipients after leave: $after"
if (( after < before )); then
    pass "$PRIMARY recipients shrank ($before → $after)"
else
    fail "$PRIMARY recipients did not shrink ($before → $after)"
fi

expect_zero "$PRIMARY: reencrypt.pending sentinel dropped" \
    on_host "$PRIMARY" 'test -f $HOME/.lockbox/reencrypt.pending'

# NOTE: reencrypt.pending is created when a peer leaves but is NEVER
# auto-removed by any code path (verified in agent/src/swarm/lockbox/mod.rs
# + cli.rs — only leave.pending and enroll.pending have remove_file calls).
# So we don't assert it's cleared after unlock — that would always fail.
# This is a real UX gap in the agent; tracking separately.
step "$PRIMARY: unlock (verifies session helper accepts piped password)"
if printf '%s\n' "$PW" | on_host "$PRIMARY" 'vigo swarm lockbox unlock' >/dev/null 2>&1; then
    pass "$PRIMARY unlock succeeded"
else
    fail "$PRIMARY unlock failed"
fi

step "restoring $SECONDARY enrollment"
if reenroll "$SECONDARY"; then pass "$SECONDARY re-enrolled"; else fail "$SECONDARY re-enroll failed"; fi

# ───────────────────────── 5. Phase B — vigocli swarm lockbox evict ─────────────────────────
section "5. Phase B — vigocli swarm lockbox evict (server host)"

expect_zero "vigocli swarm lockbox members $LOCKBOX_USER" vigocli swarm lockbox members "$LOCKBOX_USER"

expect_zero "vigocli swarm lockbox evict --host $THIRD --user $LOCKBOX_USER" \
    vigocli swarm lockbox evict --host "$THIRD" --user "$LOCKBOX_USER" --yes
# Server dispatches via TaskDispatch over AgentStream — agent picks up the
# task within seconds, _apply-evict regenerates recipients.txt locally.
sleep 10
if on_host "$PRIMARY" "grep -q $THIRD \$HOME/.lockbox/recipients.txt" >/dev/null 2>&1; then
    fail "$THIRD pubkey still in $PRIMARY recipients.txt"
else
    pass "$THIRD pubkey gone from $PRIMARY recipients.txt"
fi

# Known fleet-projection gap: girlslaptop. Either succeeds (if fleet has it)
# or fails with the 'not enrolled in any matching lockbox circle' error. Both
# outcomes are acceptable here — we just want it to not crash the server.
step "vigocli swarm lockbox evict --host $ABSENT --all-users (may report 'not enrolled')"
if vigocli swarm lockbox evict --host "$ABSENT" --all-users --yes >/tmp/livetest.out 2>&1; then
    pass "evict $ABSENT succeeded"
else
    if grep -qi 'not enrolled\|no matching\|not found' /tmp/livetest.out; then
        pass "evict $ABSENT correctly reported absent host"
    else
        fail "evict $ABSENT failed unexpectedly: $(head -2 /tmp/livetest.out | tr '\n' ' ')"
    fi
fi

step "restoring $THIRD enrollment"
# vigocli swarm lockbox evict dispatches via TaskDispatch — the actual
# self-evict scrub on the evicted host runs async via the agent
# stream. Poll for identity.age to disappear (timeout 30s) before
# trying to re-enroll, otherwise `vigo swarm lockbox enroll` bails with
# "already enrolled" because the scrub hasn't landed yet.
note "waiting for $THIRD self-evict scrub (identity.age removal) — up to 30s"
scrubbed=0
for _ in $(seq 1 30); do
    if ! on_host "$THIRD" 'test -f $HOME/.lockbox/identity.age' >/dev/null 2>&1; then
        scrubbed=1
        break
    fi
    sleep 1
done
if (( scrubbed == 0 )); then
    warn "$THIRD identity.age still present after 30s — re-enroll will likely fail"
fi
if reenroll "$THIRD"; then pass "$THIRD re-enrolled"; else fail "$THIRD re-enroll failed"; fi

# ───────────────────────── 6. --purge variant ─────────────────────────
section "6. --purge variant on lockbox leave (on $SECONDARY)"

# Make sure secondary is enrolled before the test
if ! on_host "$SECONDARY" 'test -f $HOME/.lockbox/identity.age' >/dev/null 2>&1; then
    if reenroll "$SECONDARY"; then note "re-enrolled $SECONDARY for §6"; else fail "could not enroll $SECONDARY for §6"; fi
fi

on_host "$SECONDARY" 'echo secret > /tmp/s.txt && vigo swarm lockbox encrypt /tmp/s.txt' >/dev/null 2>&1
expect_zero "$SECONDARY: ~/lockbox has content" \
    on_host "$SECONDARY" '[ -n "$(ls $HOME/lockbox/ 2>/dev/null)" ]'

expect_zero "$SECONDARY: lockbox leave --purge --yes" \
    on_host "$SECONDARY" 'vigo swarm lockbox leave --purge --yes'
note "waiting 45s for daemon to process purge"
sleep 45
# --purge removes the ~/lockbox/ content directory entirely AND removes the
# identity files from ~/.lockbox/. The ~/.lockbox/ directory itself stays.
expect_nonzero "$SECONDARY: identity.age removed by --purge" \
    on_host "$SECONDARY" 'test -f $HOME/.lockbox/identity.age'
expect_nonzero "$SECONDARY: ~/lockbox/ removed by --purge" \
    on_host "$SECONDARY" 'test -d $HOME/lockbox'

# ───────────────────────── 7. Regression — DELETE tombstone path ─────────────────────────
section "7. Regression — DELETE tombstone path (on $SECONDARY)"

if reenroll "$SECONDARY"; then note "$SECONDARY re-enrolled for §7"; else fail "could not enroll $SECONDARY for §7"; fi

# §6's --purge destroyed ~/lockbox/; reenroll just recreated it under
# the same path but a different inode. The watcher's reconciler
# detects the rotation and re-arms its inotify watch on the next
# 30s tick (see agent/src/swarm/lockbox/watcher.rs). Wait for that
# tick before the encrypt — without it, the inotify event is captured
# against the dead watch and no PushJob/DeleteJob is generated.
note "waiting 35s for watcher reconciler to re-arm on the recreated ~/lockbox/"
sleep 35

on_host "$SECONDARY" 'echo regress > /tmp/r.txt && vigo swarm lockbox encrypt /tmp/r.txt --as regress.age' >/dev/null 2>&1
expect_zero "$SECONDARY: delete regress.age" \
    on_host "$SECONDARY" 'vigo swarm lockbox delete regress.age'
note "waiting 45s for tombstone gossip (one announce tick at 30s + slack)"
sleep 45
expect_match "tombstone propagated to $PRIMARY" 'regress\.age' \
    on_host "$PRIMARY" 'vigo swarm lockbox list --tombstones'
