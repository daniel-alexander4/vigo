#!/usr/bin/env bash
# Run all Go and Rust unit tests with a summary.
#
# Usage:
#   ./scripts/test-unit.sh              # Go + Rust
#   ./scripts/test-unit.sh --go         # Go only
#   ./scripts/test-unit.sh --rust       # Rust only
#   ./scripts/test-unit.sh --windows    # Include Windows-only Rust tests (requires cross)

set -uo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

RUN_GO=true
RUN_RUST=true
RUN_WINDOWS=false

for arg in "$@"; do
    case "$arg" in
        --go)      RUN_RUST=false ;;
        --rust)    RUN_GO=false ;;
        --windows) RUN_WINDOWS=true ;;
    esac
done

GO_PASS=0
GO_FAIL=0
GO_TOTAL=0
RUST_PASS=0
RUST_FAIL=0
RUST_TOTAL=0
WIN_PASS=0
WIN_FAIL=0
WIN_TOTAL=0
FAILURES=()

echo -e "${BOLD}hobnob unit test suite${NC}"
echo ""

# -------------------------------------------------------------------
# Go tests
# -------------------------------------------------------------------
if $RUN_GO; then
    echo -e "${BOLD}[go tests]${NC}"
    GO_OUTPUT=$(go test ./... -count=1 -race 2>&1)
    GO_EXIT=$?

    while IFS= read -r line; do
        if [[ "$line" =~ ^ok ]]; then
            pkg=$(echo "$line" | awk '{print $2}')
            dur=$(echo "$line" | awk '{print $3}')
            echo -e "  ${GREEN}PASS${NC}  $pkg $dur"
            GO_PASS=$((GO_PASS + 1))
        elif [[ "$line" =~ ^FAIL ]]; then
            pkg=$(echo "$line" | awk '{print $2}')
            echo -e "  ${RED}FAIL${NC}  $pkg"
            GO_FAIL=$((GO_FAIL + 1))
            FAILURES+=("go: $pkg")
        fi
    done <<< "$GO_OUTPUT"

    GO_TOTAL=$((GO_PASS + GO_FAIL))

    if [[ $GO_FAIL -gt 0 ]]; then
        echo ""
        echo -e "${RED}Go test output:${NC}"
        echo "$GO_OUTPUT" | grep -A5 "^---  FAIL" | head -30
    fi

    # Count individual test functions.
    GO_TEST_COUNT=$(echo "$GO_OUTPUT" | grep -cE '^\s*(--- PASS|--- FAIL|=== RUN)' || true)
    echo ""
    echo -e "  Go: ${GREEN}$GO_PASS${NC} packages pass, ${RED}$GO_FAIL${NC} fail ($GO_TEST_COUNT test functions)"
    echo ""
fi

# -------------------------------------------------------------------
# Rust tests
# -------------------------------------------------------------------
if $RUN_RUST; then
    echo -e "${BOLD}[rust tests]${NC}"
    pushd agent-rs > /dev/null 2>&1 || { echo "  agent-rs/ not found"; exit 1; }

    RUST_OUTPUT=$(cargo test --lib 2>&1)
    RUST_EXIT=$?

    # Parse Rust test output.
    RUST_RESULT=$(echo "$RUST_OUTPUT" | grep "^test result:" | tail -1)
    if [[ -n "$RUST_RESULT" ]]; then
        RUST_PASSED=$(echo "$RUST_RESULT" | grep -oP '\d+ passed' | grep -oP '\d+')
        RUST_FAILED=$(echo "$RUST_RESULT" | grep -oP '\d+ failed' | grep -oP '\d+')
        RUST_IGNORED=$(echo "$RUST_RESULT" | grep -oP '\d+ ignored' | grep -oP '\d+')
        RUST_PASS=${RUST_PASSED:-0}
        RUST_FAIL=${RUST_FAILED:-0}
        RUST_TOTAL=$((RUST_PASS + RUST_FAIL))

        if [[ $RUST_FAIL -eq 0 ]]; then
            echo -e "  ${GREEN}PASS${NC}  $RUST_PASS tests passed, $RUST_IGNORED ignored"
        else
            echo -e "  ${RED}FAIL${NC}  $RUST_PASS passed, $RUST_FAIL failed"
            FAILURES+=("rust: $RUST_FAIL tests")
            echo ""
            echo "$RUST_OUTPUT" | grep -E "^test .* FAILED|^failures:" -A5 | head -20
        fi
    else
        echo -e "  ${RED}FAIL${NC}  cargo test failed to produce results"
        RUST_FAIL=1
        FAILURES+=("rust: compilation or test failure")
        echo "$RUST_OUTPUT" | tail -10
    fi

    popd > /dev/null 2>&1
    echo ""
fi

# -------------------------------------------------------------------
# Windows Rust tests (cross-compile check only)
# -------------------------------------------------------------------
if $RUN_WINDOWS; then
    echo -e "${BOLD}[windows rust tests]${NC}"
    pushd agent-rs > /dev/null 2>&1

    # Check if the Windows target can compile (tests can't run on Linux).
    WIN_OUTPUT=$(cargo check --target x86_64-pc-windows-msvc 2>&1)
    WIN_EXIT=$?

    if [[ $WIN_EXIT -eq 0 ]]; then
        echo -e "  ${GREEN}PASS${NC}  Windows cross-compile check"
        WIN_PASS=1
    else
        echo -e "  ${RED}FAIL${NC}  Windows cross-compile check"
        WIN_FAIL=1
        FAILURES+=("windows: cross-compile failed")
        echo "$WIN_OUTPUT" | tail -10
    fi

    WIN_TOTAL=$((WIN_PASS + WIN_FAIL))
    popd > /dev/null 2>&1
    echo ""
fi

# -------------------------------------------------------------------
# Summary
# -------------------------------------------------------------------
TOTAL_PASS=$((GO_PASS + RUST_PASS + WIN_PASS))
TOTAL_FAIL=$((GO_FAIL + RUST_FAIL + WIN_FAIL))

echo -e "${BOLD}========================================${NC}"
if $RUN_GO; then
    echo -e "  Go:     ${GREEN}$GO_PASS${NC} packages pass, ${RED}$GO_FAIL${NC} fail"
fi
if $RUN_RUST; then
    echo -e "  Rust:   ${GREEN}$RUST_PASS${NC} tests pass, ${RED}$RUST_FAIL${NC} fail"
fi
if $RUN_WINDOWS; then
    echo -e "  Windows: ${GREEN}$WIN_PASS${NC} pass, ${RED}$WIN_FAIL${NC} fail"
fi
echo -e "${BOLD}========================================${NC}"

if [[ ${#FAILURES[@]} -gt 0 ]]; then
    echo ""
    echo -e "${RED}Failures:${NC}"
    for f in "${FAILURES[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
