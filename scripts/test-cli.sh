#!/usr/bin/env bash
# Automated CLI test suite for hobnobcli.
# Exercises every subcommand against a live server.
#
# Usage:
#   ./scripts/test-cli.sh [--server https://host:8443]
#
# Requires: hobnobcli in PATH (or ./bin/hobnobcli), jq, curl.
# Assumes the server is running and reachable from localhost (implicit admin).

set -uo pipefail

SERVER="${1:-https://localhost:8443}"
# Strip --server prefix if passed as flag
[[ "$SERVER" == "--server" ]] && SERVER="${2:-https://localhost:8443}"

CLI="${HOBNOBCLI:-hobnobcli}"
PASS=0
FAIL=0
SKIP=0
FAILURES=()

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

run_test() {
    local name="$1"
    shift
    local output
    if output=$("$@" 2>&1); then
        echo -e "  ${GREEN}PASS${NC}  $name"
        PASS=$((PASS + 1))
        return 0
    else
        local rc=$?
        echo -e "  ${RED}FAIL${NC}  $name (exit $rc)"
        echo "        $output" | head -3
        FAIL=$((FAIL + 1))
        FAILURES+=("$name")
        return 1
    fi
}

# Like run_test but expects nonzero exit (error handling tests).
run_test_expect_fail() {
    local name="$1"
    shift
    local output
    if output=$("$@" 2>&1); then
        echo -e "  ${RED}FAIL${NC}  $name (expected failure, got success)"
        FAIL=$((FAIL + 1))
        FAILURES+=("$name")
        return 1
    else
        echo -e "  ${GREEN}PASS${NC}  $name (correctly rejected)"
        PASS=$((PASS + 1))
        return 0
    fi
}

skip_test() {
    local name="$1"
    local reason="$2"
    echo -e "  ${YELLOW}SKIP${NC}  $name ($reason)"
    SKIP=$((SKIP + 1))
}

# Check that output contains expected string.
run_test_grep() {
    local name="$1"
    local pattern="$2"
    shift 2
    local output
    if output=$("$@" 2>&1); then
        if echo "$output" | grep -qi "$pattern"; then
            echo -e "  ${GREEN}PASS${NC}  $name"
            PASS=$((PASS + 1))
            return 0
        else
            echo -e "  ${RED}FAIL${NC}  $name (missing: $pattern)"
            echo "        $output" | head -3
            FAIL=$((FAIL + 1))
            FAILURES+=("$name")
            return 1
        fi
    else
        local rc=$?
        echo -e "  ${RED}FAIL${NC}  $name (exit $rc)"
        echo "        $output" | head -3
        FAIL=$((FAIL + 1))
        FAILURES+=("$name")
        return 1
    fi
}

echo -e "${BOLD}hobnobcli test suite${NC}"
echo "Server: $SERVER"
echo ""

# -------------------------------------------------------------------
# Preflight: verify server is reachable
# -------------------------------------------------------------------
echo -e "${BOLD}[preflight]${NC}"
if ! curl -sk "$SERVER/api/v1/doctor" -o /dev/null 2>/dev/null; then
    echo -e "  ${RED}Server unreachable at $SERVER${NC}"
    echo "  Start the server first, then re-run this script."
    exit 1
fi
echo -e "  ${GREEN}Server reachable${NC}"
echo ""

# -------------------------------------------------------------------
# 1. doctor
# -------------------------------------------------------------------
echo -e "${BOLD}[doctor]${NC}"
run_test "doctor" $CLI doctor --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 2. license
# -------------------------------------------------------------------
echo -e "${BOLD}[license]${NC}"
run_test "license status" $CLI license status --server "$SERVER"
run_test "license fingerprint" $CLI license fingerprint --ca-file /srv/hobnob/tls/ca 2>/dev/null || \
    skip_test "license fingerprint" "ca file not found"
echo ""

# -------------------------------------------------------------------
# 3. db
# -------------------------------------------------------------------
echo -e "${BOLD}[db]${NC}"
run_test "db stats" $CLI db stats --server "$SERVER"
run_test "db tables" $CLI db tables --server "$SERVER"
run_test "db integrity" $CLI db integrity --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 4. thralls
# -------------------------------------------------------------------
echo -e "${BOLD}[thralls]${NC}"
run_test "thralls list" $CLI thralls list --server "$SERVER"

# Capture first thrall hostname for subsequent tests.
FIRST_THRALL=$($CLI thralls list --server "$SERVER" 2>/dev/null | awk 'NR==2{print $2}' || true)
if [[ -n "$FIRST_THRALL" && "$FIRST_THRALL" != "ID" ]]; then
    run_test "thralls show" $CLI thralls show "$FIRST_THRALL" --server "$SERVER"
    run_test "thralls traits" $CLI thralls traits "$FIRST_THRALL" --server "$SERVER"
    run_test "thralls preview" $CLI thralls preview "$FIRST_THRALL" --server "$SERVER"

    # Tag and untag.
    run_test "thralls tag" $CLI thralls tag "$FIRST_THRALL" "test-tag" --server "$SERVER"
    run_test "thralls untag" $CLI thralls untag "$FIRST_THRALL" "test-tag" --server "$SERVER"

    # Packages.
    run_test "packages list" $CLI packages list --thrall "$FIRST_THRALL" --server "$SERVER" || true
else
    skip_test "thralls show/traits/preview/tag" "no thralls enrolled"
fi
echo ""

# -------------------------------------------------------------------
# 5. runs
# -------------------------------------------------------------------
echo -e "${BOLD}[runs]${NC}"
run_test "runs list" $CLI runs list --server "$SERVER"

FIRST_RUN=$($CLI runs list --server "$SERVER" 2>/dev/null | awk 'NR==2{print $1}' || true)
if [[ -n "$FIRST_RUN" && "$FIRST_RUN" != "ID" ]]; then
    run_test "runs show" $CLI runs show "$FIRST_RUN" --server "$SERVER"
else
    skip_test "runs show" "no runs found"
fi
echo ""

# -------------------------------------------------------------------
# 6. approved + tokens
# -------------------------------------------------------------------
echo -e "${BOLD}[approved + tokens]${NC}"
run_test "approved list" $CLI approved list --server "$SERVER"

# Generate a token, add an approved pattern, then clean up.
TOKEN=$($CLI tokens generate --server "$SERVER" 2>/dev/null | grep -oP 'Token:\s+\K\S+' || true)
if [[ -n "$TOKEN" ]]; then
    run_test "tokens generate" true  # Already succeeded above.
    PATTERN_ID=""
    ADD_OUT=$($CLI approved add "test-cli-*.example.com" --token "$TOKEN" --server "$SERVER" 2>&1 || true)
    if echo "$ADD_OUT" | grep -qi "added\|created\|success\|id"; then
        echo -e "  ${GREEN}PASS${NC}  approved add"
        PASS=$((PASS + 1))
        PATTERN_ID=$(echo "$ADD_OUT" | grep -oP '[0-9a-f-]{36}' | head -1 || true)
    else
        echo -e "  ${RED}FAIL${NC}  approved add"
        echo "        $ADD_OUT" | head -3
        FAIL=$((FAIL + 1))
        FAILURES+=("approved add")
    fi

    # Revoke the pattern we just created.
    if [[ -n "$PATTERN_ID" ]]; then
        run_test "approved revoke" bash -c "echo y | $CLI approved revoke '$PATTERN_ID' --server '$SERVER'"
    fi
else
    skip_test "tokens generate + approved add/revoke" "token generation failed"
fi
echo ""

# -------------------------------------------------------------------
# 7. config
# -------------------------------------------------------------------
echo -e "${BOLD}[config]${NC}"
run_test "config history" $CLI config history --server "$SERVER"
run_test "config publish --dry-run" $CLI config publish --dry-run --server "$SERVER"

if [[ -n "$FIRST_THRALL" && "$FIRST_THRALL" != "ID" ]]; then
    run_test "config trace" $CLI config trace "$FIRST_THRALL" --server "$SERVER"
fi
echo ""

# -------------------------------------------------------------------
# 8. inventory
# -------------------------------------------------------------------
echo -e "${BOLD}[inventory]${NC}"
run_test "inventory" $CLI inventory --server "$SERVER"
run_test "inventory --where" $CLI inventory --where "os.family=linux" --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 9. push (ad-hoc command dispatch)
# -------------------------------------------------------------------
echo -e "${BOLD}[push]${NC}"
run_test "push list" $CLI push list --server "$SERVER"

if [[ -n "$FIRST_THRALL" && "$FIRST_THRALL" != "ID" ]]; then
    # Push a harmless command to one thrall.
    PUSH_OUT=$($CLI push run "echo hobnob-cli-test" --target "$FIRST_THRALL" --timeout 15 --server "$SERVER" 2>&1 || true)
    if echo "$PUSH_OUT" | grep -qiE "dispatched|push_id|run_id|complete"; then
        echo -e "  ${GREEN}PASS${NC}  push run (echo)"
        PASS=$((PASS + 1))
        PUSH_ID=$(echo "$PUSH_OUT" | grep -oP '[0-9a-f-]{36}' | head -1 || true)
        if [[ -n "$PUSH_ID" ]]; then
            sleep 3
            run_test "push status" $CLI push status "$PUSH_ID" --server "$SERVER"
        fi
    else
        echo -e "  ${RED}FAIL${NC}  push run (echo)"
        echo "        $PUSH_OUT" | head -3
        FAIL=$((FAIL + 1))
        FAILURES+=("push run")
    fi
else
    skip_test "push run" "no thralls enrolled"
fi
echo ""

# -------------------------------------------------------------------
# 10. query
# -------------------------------------------------------------------
echo -e "${BOLD}[query]${NC}"
if [[ -n "$FIRST_THRALL" && "$FIRST_THRALL" != "ID" ]]; then
    run_test "query" $CLI query "os.hostname" --target "$FIRST_THRALL" --timeout 10 --server "$SERVER" || true
else
    skip_test "query" "no thralls enrolled"
fi
echo ""

# -------------------------------------------------------------------
# 11. export
# -------------------------------------------------------------------
echo -e "${BOLD}[export]${NC}"
run_test "export siem" $CLI export siem --server "$SERVER" || skip_test "export siem" "not enabled in server.yaml"
run_test "export cmdb" $CLI export cmdb --server "$SERVER" || skip_test "export cmdb" "not enabled in server.yaml"
run_test "export oscal" $CLI export oscal --server "$SERVER" || skip_test "export oscal" "not enabled in server.yaml"
echo ""

# -------------------------------------------------------------------
# 12. webusers
# -------------------------------------------------------------------
echo -e "${BOLD}[webusers]${NC}"
run_test "webusers list" $CLI webusers list --server "$SERVER"

# Create → verify → delete a test user.
CREATE_OUT=$($CLI webusers create --username "cli-test-user" --role viewer --server "$SERVER" 2>&1 || true)
if echo "$CREATE_OUT" | grep -qiE "created|success|cli-test-user"; then
    echo -e "  ${GREEN}PASS${NC}  webusers create"
    PASS=$((PASS + 1))
    run_test "webusers delete" bash -c "echo y | $CLI webusers delete --username 'cli-test-user' --server '$SERVER'"
else
    echo -e "  ${RED}FAIL${NC}  webusers create"
    echo "        $CREATE_OUT" | head -3
    FAIL=$((FAIL + 1))
    FAILURES+=("webusers create")
    # Try cleanup anyway.
    $CLI webusers delete --username "cli-test-user" --server "$SERVER" 2>/dev/null || true
fi
echo ""

# -------------------------------------------------------------------
# 13. auth
# -------------------------------------------------------------------
echo -e "${BOLD}[auth]${NC}"
run_test "auth status" $CLI auth status --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 14. webhooks
# -------------------------------------------------------------------
echo -e "${BOLD}[webhooks]${NC}"
run_test "webhooks list" $CLI webhooks list --server "$SERVER"

# Create → list → delete.
WH_OUT=$($CLI webhooks add "https://httpbin.org/post" --secret "test-secret-123" --events "run.complete" --server "$SERVER" 2>&1 || true)
WH_ID=$(echo "$WH_OUT" | grep -oP '[0-9a-f-]{36}' | head -1 || true)
if [[ -n "$WH_ID" ]]; then
    echo -e "  ${GREEN}PASS${NC}  webhooks add"
    PASS=$((PASS + 1))
    run_test "webhooks delete" bash -c "echo y | $CLI webhooks delete '$WH_ID' --server '$SERVER'"
else
    echo -e "  ${RED}FAIL${NC}  webhooks add"
    echo "        $WH_OUT" | head -3
    FAIL=$((FAIL + 1))
    FAILURES+=("webhooks add")
fi
echo ""

# -------------------------------------------------------------------
# 15. secrets (local operations — no server needed)
# -------------------------------------------------------------------
echo -e "${BOLD}[secrets]${NC}"
TMPDIR=$(mktemp -d)
KEYFILE="$TMPDIR/master.key"
SECRETS_DIR="$TMPDIR/secrets"
mkdir -p "$SECRETS_DIR"

run_test "secrets init" $CLI secrets init --key-file "$KEYFILE"

if [[ -f "$KEYFILE" ]]; then
    run_test "secrets set" $CLI secrets set "test/key" "hello-world" --key-file "$KEYFILE" --secrets-dir "$SECRETS_DIR"
    run_test_grep "secrets reveal" "hello-world" $CLI secrets reveal "test/key" --key-file "$KEYFILE" --secrets-dir "$SECRETS_DIR"
    run_test "secrets list" $CLI secrets list --key-file "$KEYFILE" --secrets-dir "$SECRETS_DIR"
    run_test "secrets delete" bash -c "echo y | $CLI secrets delete 'test/key' --key-file '$KEYFILE' --secrets-dir '$SECRETS_DIR'"
fi
rm -rf "$TMPDIR"
echo ""

# -------------------------------------------------------------------
# 16. spanner
# -------------------------------------------------------------------
echo -e "${BOLD}[spanner]${NC}"
run_test "spanner status" $CLI spanner status --server "$SERVER" || \
    skip_test "spanner status" "spanner not configured"
echo ""

# -------------------------------------------------------------------
# 17. workflow
# -------------------------------------------------------------------
echo -e "${BOLD}[workflow]${NC}"
run_test "workflow list" $CLI workflow list --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 18. backup
# -------------------------------------------------------------------
echo -e "${BOLD}[backup]${NC}"
BACKUP_FILE=$(mktemp /tmp/hobnob-test-backup-XXXXXX.tar.gz)
run_test "backup create" $CLI backup create "$BACKUP_FILE" --server "$SERVER" || \
    skip_test "backup create" "backup endpoint not available"
rm -f "$BACKUP_FILE"
echo ""

# -------------------------------------------------------------------
# 19. test (config validation)
# -------------------------------------------------------------------
echo -e "${BOLD}[test]${NC}"
if [[ -d "/srv/hobnob/policy/stage" ]]; then
    run_test "test" $CLI test /srv/hobnob/policy/stage --server "$SERVER"
elif [[ -d "thrall-configs" ]]; then
    run_test "test" $CLI test thrall-configs --server "$SERVER"
else
    skip_test "test" "no config dir found"
fi
echo ""

# -------------------------------------------------------------------
# 20. server (peer replication)
# -------------------------------------------------------------------
echo -e "${BOLD}[server]${NC}"
run_test "server status" $CLI server status --server "$SERVER" || \
    skip_test "server status" "peer replication not configured"
echo ""

# -------------------------------------------------------------------
# 21. Error handling
# -------------------------------------------------------------------
echo -e "${BOLD}[error handling]${NC}"
run_test_expect_fail "thralls show bad-id" $CLI thralls show "nonexistent-id-000" --server "$SERVER"
run_test_expect_fail "runs show bad-id" $CLI runs show "nonexistent-id-000" --server "$SERVER"
run_test_expect_fail "push run missing --target" $CLI push run "echo hi" --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# Summary
# -------------------------------------------------------------------
echo ""
echo -e "${BOLD}========================================${NC}"
TOTAL=$((PASS + FAIL + SKIP))
echo -e "  Total: $TOTAL  ${GREEN}Pass: $PASS${NC}  ${RED}Fail: $FAIL${NC}  ${YELLOW}Skip: $SKIP${NC}"
echo -e "${BOLD}========================================${NC}"

if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo ""
    echo -e "${RED}Failed tests:${NC}"
    for f in "${FAILURES[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
