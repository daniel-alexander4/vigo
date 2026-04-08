#!/usr/bin/env bash
# Automated CLI test suite for vigocli.
# Exercises every subcommand against a live server.
#
# Usage:
#   ./scripts/test-cli.sh [--server https://host:8443]
#
# Requires: vigocli in PATH (or ./bin/vigocli), jq, curl.
# Assumes the server is running and reachable from localhost (implicit admin).

set -uo pipefail

SERVER="${1:-https://localhost:8443}"
# Strip --server prefix if passed as flag
[[ "$SERVER" == "--server" ]] && SERVER="${2:-https://localhost:8443}"

CLI="${VIGOCLI:-vigocli}"
PASS=0
FAIL=0
SKIP=0
FAILURES=()

# Colors — Catppuccin Mocha palette (24-bit RGB)
RED='\033[38;2;243;139;168m'     # #f38ba8
GREEN='\033[38;2;166;227;161m'   # #a6e3a1
YELLOW='\033[38;2;249;226;175m'  # #f9e2af
BOLD='\033[1m'
NC='\033[0m'

run_test() {
    local name="$1"
    shift
    echo -ne "  ${YELLOW} RUN${NC}  $name\r"
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
    echo -ne "  ${YELLOW} RUN${NC}  $name\r"
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
    echo -ne "  ${YELLOW} RUN${NC}  $name\r"
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

echo -e "${BOLD}vigocli test suite${NC}"
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
# 2. audit
# -------------------------------------------------------------------
echo -e "${BOLD}[audit]${NC}"
run_test "audit list" $CLI audit list --server "$SERVER"
run_test "audit verify" $CLI audit verify --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 3. license
# -------------------------------------------------------------------
echo -e "${BOLD}[license]${NC}"
run_test "license status" $CLI license status --server "$SERVER"
run_test "license fingerprint" $CLI license fingerprint --ca-file /srv/vigo/tls/ca 2>/dev/null || \
    skip_test "license fingerprint" "ca file not found"
echo ""

# -------------------------------------------------------------------
# 3. db
# -------------------------------------------------------------------
echo -e "${BOLD}[db]${NC}"
run_test "db stats" $CLI db stats --server "$SERVER"
run_test "db tables" $CLI db tables --server "$SERVER"
run_test "db integrity" $CLI db integrity --server "$SERVER"
run_test "db checkpoint" $CLI db checkpoint --server "$SERVER"
run_test "db vacuum" $CLI db vacuum --force --server "$SERVER"
run_test "db query" $CLI db query "SELECT count(*) FROM envoys" --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 4. envoys
# -------------------------------------------------------------------
echo -e "${BOLD}[envoys]${NC}"
run_test "envoys list" $CLI envoys list --server "$SERVER"

# Capture first envoy hostname for subsequent tests.
FIRST_ENVOY=$($CLI envoys list --server "$SERVER" 2>/dev/null | awk 'NR==2{print $2}' || true)
if [[ -n "$FIRST_ENVOY" && "$FIRST_ENVOY" != "ID" ]]; then
    run_test "envoys show" $CLI envoys show "$FIRST_ENVOY" --server "$SERVER"
    run_test "envoys traits" $CLI envoys traits "$FIRST_ENVOY" --server "$SERVER"
    run_test "envoys preview" $CLI envoys preview "$FIRST_ENVOY" --server "$SERVER"

    # Tag and untag.
    run_test "envoys tag" $CLI envoys tag "$FIRST_ENVOY" "test-tag" --server "$SERVER"
    run_test "envoys untag" $CLI envoys untag "$FIRST_ENVOY" "test-tag" --server "$SERVER"

    # Packages.
    run_test "packages list" $CLI envoys packages list --envoy "$FIRST_ENVOY" --server "$SERVER" || true

    # Maintenance: set and immediately clear.
    run_test "envoys maintenance set" $CLI envoys maintenance "$FIRST_ENVOY" --until "2099-01-01T00:00:00Z" --server "$SERVER"
    run_test "envoys maintenance clear" $CLI envoys maintenance "$FIRST_ENVOY" --clear --server "$SERVER"

    # Force push — triggers immediate check-in via the force-update endpoint.
    run_test "envoys push" $CLI envoys push "$FIRST_ENVOY" --server "$SERVER"
else
    skip_test "envoys show/traits/preview/tag/maintenance" "no envoys enrolled"
fi

# Purge (dry-run — safe, no deletions).
run_test "envoys purge --dry-run" $CLI envoys purge --dry-run --server "$SERVER"
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
if [[ $(id -u) -eq 0 ]]; then
    run_test "config publish --dry-run" $CLI config publish --dry-run --server "$SERVER"
else
    skip_test "config publish --dry-run" "requires root (normalizes stage permissions)"
fi

if [[ -n "$FIRST_ENVOY" && "$FIRST_ENVOY" != "ID" ]]; then
    run_test "config trace" $CLI config trace "$FIRST_ENVOY" --server "$SERVER"

    # Config trace should include all 7 sections when data exists.
    TRACE_OUT=$($CLI config trace "$FIRST_ENVOY" --server "$SERVER" 2>&1 || true)
    run_test_grep "config trace: Path Inheritance" "Path Inheritance" echo "$TRACE_OUT"
    run_test_grep "config trace: Module Inheritance" "Module Inheritance" echo "$TRACE_OUT"
    run_test_grep "config trace: Variable Inheritance" "Variable" echo "$TRACE_OUT"

    # Test --module filter.
    FIRST_MODULE=$($CLI config trace "$FIRST_ENVOY" --server "$SERVER" 2>/dev/null | grep -oP 'modules:\s*\K\S+' | head -1 || true)
    if [[ -n "$FIRST_MODULE" ]]; then
        run_test "config trace --module" $CLI config trace "$FIRST_ENVOY" --module "$FIRST_MODULE" --server "$SERVER"
    fi
fi
run_test "config tree" $CLI config tree --server "$SERVER"
run_test "config pending" $CLI config pending --server "$SERVER"
run_test "config search --module" $CLI config search --module "backups" --server "$SERVER" || true

# History show: grab first publish ID.
FIRST_PUBLISH=$($CLI config history --server "$SERVER" 2>/dev/null | awk 'NR==2{print $1}' || true)
if [[ -n "$FIRST_PUBLISH" && "$FIRST_PUBLISH" =~ ^[0-9]+$ ]]; then
    run_test "config history show" $CLI config history show "$FIRST_PUBLISH" --server "$SERVER"
else
    skip_test "config history show" "no publishes found"
fi

# Config rollback (list versions, don't actually rollback).
run_test "config rollback list" $CLI config rollback --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 8. config freeze / unfreeze
# -------------------------------------------------------------------
echo -e "${BOLD}[config freeze]${NC}"
run_test "config freeze" bash -c "echo y | $CLI config freeze --until 1s --server '$SERVER'"
run_test "config unfreeze" $CLI config unfreeze --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 9. inventory
# -------------------------------------------------------------------
echo -e "${BOLD}[inventory]${NC}"
run_test "inventory" $CLI inventory --server "$SERVER"
run_test "inventory --where" $CLI inventory --where "os.family=linux" --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 9. task (ad-hoc command dispatch)
# -------------------------------------------------------------------
echo -e "${BOLD}[task]${NC}"
run_test "task list" $CLI task list --server "$SERVER"

if [[ -n "$FIRST_ENVOY" && "$FIRST_ENVOY" != "ID" ]]; then
    # Dispatch a harmless command to one envoy.
    TASK_OUT=$($CLI task run "echo vigo-cli-test" --target "$FIRST_ENVOY" --timeout 15 --server "$SERVER" 2>&1 || true)
    if echo "$TASK_OUT" | grep -qiE "dispatched|task_id|run_id|complete"; then
        echo -e "  ${GREEN}PASS${NC}  task run (echo)"
        PASS=$((PASS + 1))
        TASK_ID=$(echo "$TASK_OUT" | grep -oP '[0-9a-f-]{36}' | head -1 || true)
        if [[ -n "$TASK_ID" ]]; then
            sleep 3
            run_test "task status" $CLI task status "$TASK_ID" --server "$SERVER"
        fi
    else
        echo -e "  ${RED}FAIL${NC}  task run (echo)"
        echo "        $TASK_OUT" | head -3
        FAIL=$((FAIL + 1))
        FAILURES+=("task run")
    fi
else
    skip_test "task run" "no envoys enrolled"
fi
echo ""

# -------------------------------------------------------------------
# 10. packages
# -------------------------------------------------------------------
echo -e "${BOLD}[packages]${NC}"
run_test "packages drift" $CLI envoys packages drift --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 11. query
# -------------------------------------------------------------------
echo -e "${BOLD}[query]${NC}"
if [[ -n "$FIRST_ENVOY" && "$FIRST_ENVOY" != "ID" ]]; then
    run_test "query" $CLI query "os.hostname" --target "$FIRST_ENVOY" --timeout 10 --server "$SERVER" || true
else
    skip_test "query" "no envoys enrolled"
fi
echo ""

# -------------------------------------------------------------------
# 11. export
# -------------------------------------------------------------------
echo -e "${BOLD}[export]${NC}"
if $CLI export siem --server "$SERVER" >/dev/null 2>&1; then
    run_test "export siem" $CLI export siem --server "$SERVER"
    run_test "export cmdb" $CLI export cmdb --server "$SERVER"
    run_test "export oscal" $CLI export oscal --server "$SERVER"
else
    skip_test "export siem/cmdb/oscal" "not enabled in server.yaml"
fi
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
run_test "auth emergency-access" $CLI auth emergency-access --reason "CLI test suite" --server "$SERVER"

# API token lifecycle: create → list → revoke.
TOKEN_OUT=$($CLI auth create-token --server "$SERVER" 2>&1 || true)
API_TOKEN_ID=$(echo "$TOKEN_OUT" | grep -oP '[0-9a-f-]{36}' | head -1 || true)
if [[ -n "$API_TOKEN_ID" ]]; then
    echo -e "  ${GREEN}PASS${NC}  auth create-token"
    PASS=$((PASS + 1))
    run_test "auth list-tokens" $CLI auth list-tokens --server "$SERVER"
    run_test "auth revoke-token" bash -c "echo y | $CLI auth revoke-token '$API_TOKEN_ID' --server '$SERVER'"
else
    # create-token may need auth mode enabled.
    skip_test "auth create-token/list-tokens/revoke-token" "auth not enabled or token creation failed"
fi
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
run_test "spanner spokes" $CLI spanner spokes --server "$SERVER" || true

# add-spoke and remove-spoke require hub mode — test error handling on standalone.
SPANNER_ROLE=$($CLI spanner status --server "$SERVER" 2>/dev/null | grep -oP 'Role:\s*\K\S+' || true)
if [[ "$SPANNER_ROLE" == "hub" ]]; then
    run_test_expect_fail "spanner add-spoke (no args)" $CLI spanner add-spoke --server "$SERVER"
    run_test_expect_fail "spanner remove-spoke (no args)" $CLI spanner remove-spoke --server "$SERVER"
else
    run_test_expect_fail "spanner add-spoke (not hub)" $CLI spanner add-spoke "test-spoke" --addr "localhost:1531" --patterns "*.test" --server "$SERVER"
fi
echo ""

# -------------------------------------------------------------------
# 17. workflow
# -------------------------------------------------------------------
echo -e "${BOLD}[workflow]${NC}"
run_test "workflow list" $CLI workflow list --server "$SERVER"
# Workflow status/abort with bad ID (error handling).
run_test_expect_fail "workflow status bad-id" $CLI workflow status "nonexistent-wf-000" --server "$SERVER"
run_test_expect_fail "workflow abort bad-id" bash -c "echo y | $CLI workflow abort 'nonexistent-wf-000' --server '$SERVER'"
echo ""

# -------------------------------------------------------------------
# 18. backup
# -------------------------------------------------------------------
echo -e "${BOLD}[backup]${NC}"
BACKUP_FILE=$(mktemp /tmp/vigo-test-backup-XXXXXX.tar.gz)
if [[ $(id -u) -eq 0 ]]; then
    run_test "backup create" $CLI backup create "$BACKUP_FILE"
    if [[ -f "$BACKUP_FILE" ]]; then
        run_test "backup inspect" $CLI backup inspect "$BACKUP_FILE"
        run_test "backup restore --dry-run" $CLI backup restore "$BACKUP_FILE" --dry-run

        # Verify SHA-256 sidecar file was created.
        if [[ -f "${BACKUP_FILE}.sha256" ]]; then
            echo -e "  ${GREEN}PASS${NC}  backup sha256 sidecar exists"
            PASS=$((PASS + 1))
        else
            echo -e "  ${RED}FAIL${NC}  backup sha256 sidecar missing"
            FAIL=$((FAIL + 1))
            FAILURES+=("backup sha256 sidecar")
        fi
    fi
else
    skip_test "backup create" "requires root (reads server.yaml and secrets)"
fi
rm -f "$BACKUP_FILE" "${BACKUP_FILE}.sha256"
echo ""

# -------------------------------------------------------------------
# 19. test (config validation)
# -------------------------------------------------------------------
echo -e "${BOLD}[test]${NC}"
if [[ -d "/srv/vigo/stockpile" ]]; then
    run_test "test" $CLI test /srv/vigo/stockpile --server "$SERVER"
elif [[ -d "examples" ]]; then
    run_test "test" $CLI test examples --server "$SERVER"
else
    skip_test "test" "no config dir found"
fi
echo ""

# -------------------------------------------------------------------
# 20. server (peer replication)
# -------------------------------------------------------------------
echo -e "${BOLD}[server]${NC}"
if $CLI server status --server "$SERVER" >/dev/null 2>&1; then
    echo -e "  ${GREEN}PASS${NC}  server status"
    PASS=$((PASS + 1))
else
    skip_test "server status" "peer replication not configured"
fi

# Generate TLS certs to a temp dir.
CERT_TMPDIR=$(mktemp -d)
run_test "secrets generate-certs" $CLI secrets generate-certs --tls-dir "$CERT_TMPDIR" --force
rm -rf "$CERT_TMPDIR"
echo ""

# -------------------------------------------------------------------
# 21. validate
# -------------------------------------------------------------------
echo -e "${BOLD}[validate]${NC}"
if [[ -d "/srv/vigo/stockpile/modules" ]]; then
    VGO_FILE=$(find /srv/vigo/stockpile/modules -name '*.vgo' ! -name '*.example' 2>/dev/null | head -1)
    if [[ -n "$VGO_FILE" ]]; then
        run_test "config validate" $CLI config validate "$VGO_FILE"
    else
        skip_test "config validate" "no .vgo files found in stage"
    fi
elif [[ -d "example-configs/stockpile/modules" ]]; then
    VGO_FILE=$(find example-configs/stockpile/modules -name '*.vgo' ! -name '*.example' 2>/dev/null | head -1)
    if [[ -n "$VGO_FILE" ]]; then
        run_test "config validate" $CLI config validate "$VGO_FILE"
    else
        skip_test "config validate" "no .vgo files found"
    fi
else
    skip_test "config validate" "no config dir found"
fi
echo ""

# -------------------------------------------------------------------
# 23. env
# -------------------------------------------------------------------
echo -e "${BOLD}[env]${NC}"
# env promote requires valid module + tiers; test that it rejects bad input.
run_test_expect_fail "env promote (no args)" $CLI env promote --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 24. ask (AI assistant)
# -------------------------------------------------------------------
echo -e "${BOLD}[ask]${NC}"
if $CLI ask "How many envoys are enrolled?" --server "$SERVER" >/dev/null 2>&1; then
    run_test "ask" $CLI ask "How many envoys are enrolled?" --server "$SERVER"
else
    skip_test "ask" "AI not enabled in server.yaml"
fi
echo ""

# -------------------------------------------------------------------
# 25. rollback
# -------------------------------------------------------------------
echo -e "${BOLD}[rollback]${NC}"
# Rollback requires a envoy + run ID with file snapshots; test error handling.
run_test_expect_fail "rollback (no args)" $CLI rollback --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 27. report (compliance reports)
# -------------------------------------------------------------------
echo -e "${BOLD}[report]${NC}"
run_test "report compliance (JSON)" $CLI report compliance --server "$SERVER"
run_test "report compliance (HTML)" $CLI report compliance --format html --server "$SERVER"
run_test "report hitrust (JSON)" $CLI report hitrust --server "$SERVER"
run_test "report hitrust (HTML)" $CLI report hitrust --format html --server "$SERVER"
run_test "report soc2 (JSON)" $CLI report soc2 --server "$SERVER"
run_test "report soc2 (HTML)" $CLI report soc2 --format html --server "$SERVER"
run_test "report pci (JSON)" $CLI report pci --server "$SERVER"
run_test "report pci (HTML)" $CLI report pci --format html --server "$SERVER"
run_test "report cis (JSON)" $CLI report cis --server "$SERVER"
run_test "report cis (HTML)" $CLI report cis --format html --server "$SERVER"
run_test "report nist (JSON)" $CLI report nist --server "$SERVER"
run_test "report nist (HTML)" $CLI report nist --format html --server "$SERVER"
run_test "report iso27001 (JSON)" $CLI report iso27001 --server "$SERVER"
run_test "report iso27001 (HTML)" $CLI report iso27001 --format html --server "$SERVER"
run_test "report gdpr (JSON)" $CLI report gdpr --server "$SERVER"
run_test "report gdpr (HTML)" $CLI report gdpr --format html --server "$SERVER"
run_test "report nerccip (JSON)" $CLI report nerccip --server "$SERVER"
run_test "report nerccip (HTML)" $CLI report nerccip --format html --server "$SERVER"
run_test "report cyberessentials (JSON)" $CLI report cyberessentials --server "$SERVER"
run_test "report cyberessentials (HTML)" $CLI report cyberessentials --format html --server "$SERVER"
run_test "report iec62443 (JSON)" $CLI report iec62443 --server "$SERVER"
run_test "report iec62443 (HTML)" $CLI report iec62443 --format html --server "$SERVER"
run_test "report executive (JSON)" $CLI report executive --server "$SERVER"
run_test "report executive (HTML)" $CLI report executive --format html --server "$SERVER"

# Test --output flag with a temp file.
REPORT_TMPFILE=$(mktemp /tmp/vigo-report-XXXXXX.json)
run_test "report compliance --output" $CLI report compliance --output "$REPORT_TMPFILE" --server "$SERVER"
if [[ -s "$REPORT_TMPFILE" ]]; then
    echo -e "  ${GREEN}PASS${NC}  report output file is non-empty"
    PASS=$((PASS + 1))
else
    echo -e "  ${RED}FAIL${NC}  report output file is empty"
    FAIL=$((FAIL + 1))
    FAILURES+=("report output file check")
fi
rm -f "$REPORT_TMPFILE"
echo ""

# -------------------------------------------------------------------
# 28. remediation
# -------------------------------------------------------------------
echo -e "${BOLD}[remediation]${NC}"
run_test "remediation status" $CLI remediation status --server "$SERVER"
run_test "remediation list" $CLI remediation list --server "$SERVER"
run_test "remediation run --dry-run" $CLI remediation run --dry-run --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 29. swarm
# -------------------------------------------------------------------
echo -e "${BOLD}[swarm]${NC}"
run_test "swarm list" $CLI swarm list --server "$SERVER"
run_test "swarm list --source seed" $CLI swarm list --source seed --server "$SERVER"
run_test "swarm list --status complete" $CLI swarm list --status complete --server "$SERVER"
run_test "swarm list --retired" $CLI swarm list --retired --server "$SERVER"
run_test "swarm broken" $CLI swarm broken --server "$SERVER"

# Distribute a small test blob, check status, then delete.
SWARM_TMPFILE=$(mktemp /tmp/vigo-swarm-test-XXXXXX.txt)
echo "vigo-cli-test-blob" > "$SWARM_TMPFILE"
SWARM_OUT=$($CLI swarm distribute "$SWARM_TMPFILE" --target "*" --server "$SERVER" 2>&1 || true)
SWARM_SHA=$(echo "$SWARM_OUT" | grep -oP '[0-9a-f]{64}' | head -1 || true)
if [[ -n "$SWARM_SHA" ]]; then
    echo -e "  ${GREEN}PASS${NC}  swarm distribute"
    PASS=$((PASS + 1))
    run_test "swarm status" $CLI swarm status "$SWARM_SHA" --server "$SERVER"
    run_test "swarm delete" bash -c "echo y | $CLI swarm delete '$SWARM_SHA' --server '$SERVER'"
else
    echo -e "  ${RED}FAIL${NC}  swarm distribute"
    echo "        $SWARM_OUT" | head -3
    FAIL=$((FAIL + 1))
    FAILURES+=("swarm distribute")
    skip_test "swarm status" "no blob to check"
    skip_test "swarm delete" "no blob to delete"
fi
rm -f "$SWARM_TMPFILE"
echo ""

# -------------------------------------------------------------------
# 30. risk + cve
# -------------------------------------------------------------------
echo -e "${BOLD}[risk + cve]${NC}"
run_test "risk fleet" $CLI risk --server "$SERVER"
if [[ -n "$FIRST_ENVOY" && "$FIRST_ENVOY" != "ID" ]]; then
    run_test "risk envoy" $CLI risk "$FIRST_ENVOY" --server "$SERVER"
else
    skip_test "risk envoy" "no envoys enrolled"
fi
run_test "cve (bad ID)" $CLI cve "CVE-0000-0000" --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 31. traits
# -------------------------------------------------------------------
echo -e "${BOLD}[traits]${NC}"
run_test "traits search cpu" $CLI traits search cpu --server "$SERVER"
run_test "traits search cloud" $CLI traits search cloud --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 32. sandgorgon
# -------------------------------------------------------------------
echo -e "${BOLD}[sandgorgon]${NC}"
if $CLI sandgorgon status --server "$SERVER" >/dev/null 2>&1; then
    run_test "sandgorgon status" $CLI sandgorgon status --server "$SERVER"
    run_test "sandgorgon events" $CLI sandgorgon events --server "$SERVER"
    run_test "sandgorgon certificates" $CLI sandgorgon certificates --server "$SERVER"
    run_test "sandgorgon images" $CLI sandgorgon images --server "$SERVER"
    run_test_expect_fail "sandgorgon commission (no args)" $CLI sandgorgon commission --server "$SERVER"
    run_test_expect_fail "sandgorgon decommission (no args)" $CLI sandgorgon decommission --server "$SERVER"
else
    skip_test "sandgorgon" "sandgorgon not configured"
fi
echo ""

# -------------------------------------------------------------------
# 33. scrier
# -------------------------------------------------------------------
echo -e "${BOLD}[scrier]${NC}"
if $CLI scrier users list --server "$SERVER" >/dev/null 2>&1; then
    run_test "scrier users list" $CLI scrier users list --server "$SERVER"
else
    skip_test "scrier users list" "scrier not enabled"
fi
echo ""

# -------------------------------------------------------------------
# 34. config extras
# -------------------------------------------------------------------
echo -e "${BOLD}[config extras]${NC}"
run_test "config examples list" $CLI config examples list || true
run_test "config dedup" $CLI config dedup --server "$SERVER" || true
run_test "config server.yaml list" $CLI config server.yaml list --server "$SERVER" || true
run_test "config server.yaml full" $CLI config server.yaml full --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 35. runs extras
# -------------------------------------------------------------------
echo -e "${BOLD}[runs extras]${NC}"
run_test "runs drift" $CLI runs drift --server "$SERVER" || true
echo ""

# -------------------------------------------------------------------
# 36. task extras
# -------------------------------------------------------------------
echo -e "${BOLD}[task extras]${NC}"
run_test_expect_fail "task cancel (bad ID)" $CLI task cancel "nonexistent-task-000" --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 37. additional reports
# -------------------------------------------------------------------
echo -e "${BOLD}[additional reports]${NC}"
run_test "report sox (JSON)" $CLI report sox --server "$SERVER"
run_test "report finra (JSON)" $CLI report finra --server "$SERVER"
run_test "report mifid2 (JSON)" $CLI report mifid2 --server "$SERVER"
run_test "report ccss (JSON)" $CLI report ccss --server "$SERVER"
run_test "report fda21cfr11 (JSON)" $CLI report fda21cfr11 --server "$SERVER"
run_test "report gaming (JSON)" $CLI report gaming --server "$SERVER"
run_test "report ciswindows (JSON)" $CLI report ciswindows --server "$SERVER"
run_test "report abacyber (JSON)" $CLI report abacyber --server "$SERVER"
run_test "report nydfscyber (JSON)" $CLI report nydfscyber --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 38. envoy extras
# -------------------------------------------------------------------
echo -e "${BOLD}[envoy extras]${NC}"
# revert requires --run or --file; test with bad run ID to verify error handling.
run_test_expect_fail "envoys revert (no flags)" $CLI envoys revert "nonexistent-000" --server "$SERVER"
run_test_expect_fail "envoys rebootstrap (bad ID)" $CLI envoys rebootstrap "nonexistent-000" --server "$SERVER"
echo ""

# -------------------------------------------------------------------
# 39. Error handling
# -------------------------------------------------------------------
echo -e "${BOLD}[error handling]${NC}"
run_test_expect_fail "envoys show bad-id" $CLI envoys show "nonexistent-id-000" --server "$SERVER"
run_test_expect_fail "runs show bad-id" $CLI runs show "nonexistent-id-000" --server "$SERVER"
run_test_expect_fail "task run missing --target" $CLI task run "echo hi" --server "$SERVER"
run_test_expect_fail "config approve bad-id" $CLI config approve 999999 --server "$SERVER"
run_test_expect_fail "config reject bad-id" $CLI config reject 999999 --server "$SERVER"
run_test_expect_fail "validate nonexistent" $CLI config validate "/tmp/nonexistent-file.vgo"
run_test_expect_fail "envoys revoke bad-id" bash -c "echo y | $CLI envoys revoke 'nonexistent-000' --server '$SERVER'"
run_test_expect_fail "envoys delete bad-id" bash -c "echo y | $CLI envoys delete 'nonexistent-000' --server '$SERVER'"
run_test_expect_fail "spanner reassign bad-id" bash -c "echo y | $CLI spanner reassign 'nonexistent-000' --from 'a' --to 'b' --server '$SERVER'"
run_test "db reset (denied)" bash -c "echo n | $CLI db reset --server '$SERVER'"
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
