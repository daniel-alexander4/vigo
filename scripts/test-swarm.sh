#!/usr/bin/env bash
# Swarm P2P integration test suite.
# Validates the full swarm pipeline: firewall, multicast, peer discovery,
# manifest gossip, and chunk transfer between danlap (seeder) and remote envoys.
#
# Usage:
#   ./scripts/test-swarm.sh [envoy]
#
# Default envoy: girlslaptop. Requires SSH + sudo access to the envoy.
# Assumes vigosrv is running in Docker on danlap and the envoy agent is running.

set -uo pipefail

ENVOY="${1:-girlslaptop}"
CLI="${VIGOCLI:-vigocli}"
PASS=0
FAIL=0
SKIP=0
FAILURES=()

# Colors -- Catppuccin Mocha palette (24-bit RGB)
RED='\033[38;2;243;139;168m'     # #f38ba8
GREEN='\033[38;2;166;227;161m'   # #a6e3a1
YELLOW='\033[38;2;249;226;175m'  # #f9e2af
BLUE='\033[38;2;137;180;250m'    # #89b4fa
BOLD='\033[1m'
NC='\033[0m'

pass() {
    echo -e "  ${GREEN}PASS${NC}  $1"
    PASS=$((PASS + 1))
}

fail() {
    echo -e "  ${RED}FAIL${NC}  $1"
    [ -n "${2:-}" ] && echo "        $2" | head -3
    FAIL=$((FAIL + 1))
    FAILURES+=("$1")
}

skip() {
    echo -e "  ${YELLOW}SKIP${NC}  $1 (${2:-skipped})"
    SKIP=$((SKIP + 1))
}

ssh_envoy() {
    # -T disables PTY; 2>/dev/null drops SSH banner/MOTD from stderr
    ssh -T -o ConnectTimeout=5 -o LogLevel=ERROR "$ENVOY" "$@" 2>/dev/null
}

# Like ssh_envoy but preserves stderr (for diagnostics)
ssh_envoy_verbose() {
    ssh -T -o ConnectTimeout=5 -o LogLevel=ERROR "$ENVOY" "$@" 2>&1
}

# -------------------------------------------------------------------
echo -e "${BOLD}Swarm P2P Integration Tests${NC}"
echo -e "${BLUE}Seeder:${NC} danlap (localhost)"
echo -e "${BLUE}Envoy:${NC}  $ENVOY"
echo ""
# -------------------------------------------------------------------

# -------------------------------------------------------------------
echo -e "${BOLD}[connectivity]${NC}"
# -------------------------------------------------------------------

# SSH access to envoy
if ssh -T -o ConnectTimeout=5 -o LogLevel=ERROR "$ENVOY" "true" >/dev/null 2>/dev/null; then
    pass "ssh to $ENVOY"
else
    fail "ssh to $ENVOY" "cannot connect -- remaining tests will fail"
fi

# Envoy agent running
ENVOY_PID=$(ssh_envoy "pgrep -x vigo" | head -1)
if [[ -n "$ENVOY_PID" ]]; then
    pass "agent running on $ENVOY (pid $ENVOY_PID)"
else
    fail "agent running on $ENVOY" "vigo process not found"
fi

# Agent version
ENVOY_VERSION=$(ssh_envoy "sudo journalctl -u vigo-envoy _PID=$ENVOY_PID 2>/dev/null | grep 'starting daemon loop' | tail -1 | grep -oP 'version=\"\K[^\"]+'" 2>/dev/null)
if [[ -n "$ENVOY_VERSION" ]]; then
    LOCAL_VERSION=$(cat VERSION 2>/dev/null || echo "unknown")
    if [[ "$ENVOY_VERSION" == "$LOCAL_VERSION" ]]; then
        pass "agent version matches ($ENVOY_VERSION)"
    else
        fail "agent version mismatch" "envoy=$ENVOY_VERSION server=$LOCAL_VERSION"
    fi
else
    skip "agent version check" "could not determine version"
fi

# -------------------------------------------------------------------
echo -e "${BOLD}[firewall]${NC}"
# -------------------------------------------------------------------

# TCP 1531
if ssh_envoy "sudo ufw status" | grep -q "1531/tcp.*ALLOW"; then
    pass "ufw allows TCP 1531"
else
    fail "ufw allows TCP 1531" "swarm peer server port blocked"
fi

# UDP 1531
if ssh_envoy "sudo ufw status" | grep -q "1531/udp.*ALLOW"; then
    pass "ufw allows UDP 1531"
else
    fail "ufw allows UDP 1531" "swarm multicast discovery port blocked"
fi

# -------------------------------------------------------------------
echo -e "${BOLD}[swarm subsystem]${NC}"
# -------------------------------------------------------------------

# Peer server listening
if ssh_envoy "sudo ss -tlnp | grep -q ':1531'"; then
    pass "peer server listening on TCP 1531"
else
    fail "peer server listening on TCP 1531"
fi

# Multicast socket bound
if ssh_envoy "sudo ss -ulnp | grep -q ':1531'"; then
    pass "multicast socket bound on UDP 1531"
else
    fail "multicast socket bound on UDP 1531"
fi

# Swarm manager started
if ssh_envoy "sudo journalctl -u vigo-envoy _PID=$ENVOY_PID 2>/dev/null | grep -q 'swarm manager started'"; then
    pass "swarm manager started"
else
    fail "swarm manager started"
fi

# Multicast listener started
if ssh_envoy "sudo journalctl -u vigo-envoy _PID=$ENVOY_PID 2>/dev/null | grep -q 'multicast listener started'"; then
    pass "multicast listener started"
else
    fail "multicast listener started"
fi

# -------------------------------------------------------------------
echo -e "${BOLD}[peer discovery]${NC}"
# -------------------------------------------------------------------

# PeerMap has entries
PEER_COUNT=$(ssh_envoy "cat /var/lib/vigo/swarm/peers.json 2>/dev/null | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null" || echo "0")
if [[ "$PEER_COUNT" -gt 0 ]]; then
    pass "peer map has $PEER_COUNT entries"
else
    fail "peer map is empty" "no peers discovered via multicast"
fi

# PeerMap contains danlap (192.168.1.2)
if ssh_envoy "cat /var/lib/vigo/swarm/peers.json 2>/dev/null" | grep -q "192.168.1.2"; then
    pass "peer map includes danlap (192.168.1.2)"
else
    fail "peer map includes danlap" "seeder not discovered"
fi

# -------------------------------------------------------------------
echo -e "${BOLD}[manifest]${NC}"
# -------------------------------------------------------------------

# Manifest loaded
MANIFEST_ENTRIES=$(ssh_envoy "cat /var/lib/vigo/swarm/manifest.json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get(\"entries\",{})))' 2>/dev/null" || echo "0")
if [[ "$MANIFEST_ENTRIES" -gt 0 ]]; then
    pass "manifest has $MANIFEST_ENTRIES entries"
else
    fail "manifest is empty" "no entries from server gossip"
fi

# Manifest version > 0
MANIFEST_VERSION=$(ssh_envoy "cat /var/lib/vigo/swarm/manifest.json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"version\",0))' 2>/dev/null" || echo "0")
if [[ "$MANIFEST_VERSION" -gt 0 ]]; then
    pass "manifest version $MANIFEST_VERSION"
else
    fail "manifest version is 0"
fi

# -------------------------------------------------------------------
echo -e "${BOLD}[distribute + download]${NC}"
# -------------------------------------------------------------------

# Create a small test blob
TMPFILE=$(mktemp /tmp/vigo-swarm-test-XXXXXX.bin)
dd if=/dev/urandom of="$TMPFILE" bs=1024 count=64 2>/dev/null
EXPECTED_SIZE=65536

DIST_OUT=$($CLI swarm distribute "$TMPFILE" --target "-rasp*" --label "swarm-test-$(date +%s)" 2>&1 || true)
TEST_SHA=$(echo "$DIST_OUT" | grep -oP '[0-9a-f]{64}' | head -1 || true)

if [[ -n "$TEST_SHA" ]]; then
    pass "distribute test blob (${TEST_SHA:0:12})"
else
    fail "distribute test blob" "$DIST_OUT"
    rm -f "$TMPFILE"
    # Can't continue without a blob
    skip "status shows envoy" "no blob"
    skip "envoy downloads blob" "no blob"
    skip "cleanup test blob" "no blob"

    echo ""
    echo -e "${BOLD}Results:${NC} ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}, ${YELLOW}$SKIP skipped${NC}"
    [[ ${#FAILURES[@]} -gt 0 ]] && echo -e "${RED}Failures:${NC} ${FAILURES[*]}"
    rm -f "$TMPFILE"
    exit $([[ $FAIL -eq 0 ]] && echo 0 || echo 1)
fi

# Status should show the envoy as pending
STATUS_OUT=$($CLI swarm status "$TEST_SHA" 2>&1 || true)
if echo "$STATUS_OUT" | grep -qi "$ENVOY"; then
    pass "status shows $ENVOY"
else
    fail "status shows $ENVOY" "$STATUS_OUT"
fi

# Wait for envoy to download (up to 60 seconds)
echo -ne "  ${YELLOW}WAIT${NC}  waiting for $ENVOY to download (up to 60s)...\r"
DOWNLOADED=false
for i in $(seq 1 12); do
    sleep 5
    if ssh_envoy "test -f /var/lib/vigo/swarm/.blobs/$TEST_SHA" 2>/dev/null; then
        DOWNLOADED=true
        break
    fi
done

if $DOWNLOADED; then
    pass "envoy downloaded blob within ${i}0s"

    # Verify the blob appeared in status as complete
    STATUS_OUT=$($CLI swarm status "$TEST_SHA" 2>&1 || true)
    if echo "$STATUS_OUT" | grep -qi "complete"; then
        pass "status shows download complete"
    else
        fail "status shows download complete" "$STATUS_OUT"
    fi
else
    fail "envoy downloaded blob" "not in .blobs/ after 60s"

    # Dump diagnostics
    echo -e "  ${YELLOW}DIAG${NC}  swarm logs on $ENVOY:"
    ssh_envoy_verbose "sudo journalctl -u vigo-envoy _PID=$ENVOY_PID --since '2 minutes ago' 2>/dev/null | grep -i swarm | tail -10" | sed 's/^/        /'
    echo -e "  ${YELLOW}DIAG${NC}  peers.json (danlap entries):"
    ssh_envoy "cat /var/lib/vigo/swarm/peers.json 2>/dev/null" | python3 -c "import json,sys; [print(f'        {e[\"sha256\"][:12]} → {e[\"address\"]} ({e[\"chunk_count\"]} chunks, {e[\"source\"]})') for e in json.load(sys.stdin) if '192.168.1.2' in e['address']]" 2>/dev/null
    echo -e "  ${YELLOW}DIAG${NC}  connections to port 1531:"
    ssh_envoy "ss -tnp 2>/dev/null | grep 1531" | sed 's/^/        /'
fi

# Cleanup
echo -ne "  ${YELLOW} RUN${NC}  cleanup test blob\r"
CLEANUP_OUT=$(echo y | $CLI swarm delete "$TEST_SHA" 2>&1 || true)
if echo "$CLEANUP_OUT" | grep -qi "deleted"; then
    pass "cleanup test blob"
else
    fail "cleanup test blob" "$CLEANUP_OUT"
fi
rm -f "$TMPFILE"

# -------------------------------------------------------------------
echo -e "${BOLD}[multi-chunk download]${NC}"
# -------------------------------------------------------------------

# 30 MiB blob = 30 chunks. Tests the scheduler's concurrent producer/consumer
# pattern that previously deadlocked on >24 chunks (channel buffer=16, semaphore=8).
TMPFILE_BIG=$(mktemp /tmp/vigo-swarm-bigtest-XXXXXX.bin)
dd if=/dev/urandom of="$TMPFILE_BIG" bs=1M count=30 2>/dev/null

DIST_OUT=$($CLI swarm distribute "$TMPFILE_BIG" --target "-rasp*" --label "swarm-bigtest-$(date +%s)" 2>&1 || true)
BIG_SHA=$(echo "$DIST_OUT" | grep -oP '[0-9a-f]{64}' | head -1 || true)

if [[ -n "$BIG_SHA" ]]; then
    pass "distribute 30 MiB blob (${BIG_SHA:0:12})"
else
    fail "distribute 30 MiB blob" "$DIST_OUT"
    rm -f "$TMPFILE_BIG"
    skip "multi-chunk download" "no blob"
    skip "cleanup multi-chunk blob" "no blob"

    echo ""
    echo -e "${BOLD}Results:${NC} ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}, ${YELLOW}$SKIP skipped${NC}"
    [[ ${#FAILURES[@]} -gt 0 ]] && echo -e "${RED}Failures:${NC} ${FAILURES[*]}"
    rm -f "$TMPFILE_BIG"
    exit $([[ $FAIL -eq 0 ]] && echo 0 || echo 1)
fi

# Wait for download (up to 120 seconds — 30 chunks over WiFi)
echo -ne "  ${YELLOW}WAIT${NC}  waiting for $ENVOY to download 30 MiB (up to 120s)...\r"
DOWNLOADED=false
for i in $(seq 1 24); do
    sleep 5
    if ssh_envoy "test -f /var/lib/vigo/swarm/.blobs/$BIG_SHA" 2>/dev/null; then
        DOWNLOADED=true
        break
    fi
done

if $DOWNLOADED; then
    ELAPSED=$((i * 5))
    pass "multi-chunk download complete (${ELAPSED}s)"

    # Verify no CLOSE-WAIT connections lingering
    CLOSE_WAIT=$(ssh_envoy "ss -tnp 2>/dev/null | grep 1531 | grep -c CLOSE-WAIT || true" 2>/dev/null | tail -1)
    if [[ "$CLOSE_WAIT" -eq 0 ]]; then
        pass "no CLOSE-WAIT connections after download"
    else
        fail "CLOSE-WAIT connections after download" "$CLOSE_WAIT connections lingering"
    fi
else
    fail "multi-chunk download" "30 MiB blob not in .blobs/ after 120s"

    echo -e "  ${YELLOW}DIAG${NC}  swarm logs on $ENVOY:"
    ENVOY_PID=$(ssh_envoy "pgrep -x vigo" | head -1)
    ssh_envoy_verbose "sudo journalctl -u vigo-envoy _PID=$ENVOY_PID --since '3 minutes ago' 2>/dev/null | grep -i swarm | tail -10" | sed 's/^/        /'
    echo -e "  ${YELLOW}DIAG${NC}  partial chunks:"
    ssh_envoy "ls /var/lib/vigo/swarm/.blobs/${BIG_SHA}.partial/ 2>/dev/null | wc -l" | sed 's/^/        /'
    echo -e "  ${YELLOW}DIAG${NC}  connections:"
    ssh_envoy "ss -tnp 2>/dev/null | grep 1531" | sed 's/^/        /'
fi

echo -ne "  ${YELLOW} RUN${NC}  cleanup multi-chunk blob\r"
CLEANUP_OUT=$(echo y | $CLI swarm delete "$BIG_SHA" 2>&1 || true)
if echo "$CLEANUP_OUT" | grep -qi "deleted"; then
    pass "cleanup multi-chunk blob"
else
    fail "cleanup multi-chunk blob" "$CLEANUP_OUT"
fi
rm -f "$TMPFILE_BIG"

# -------------------------------------------------------------------
echo ""
echo -e "${BOLD}Results:${NC} ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}, ${YELLOW}$SKIP skipped${NC}"
[[ ${#FAILURES[@]} -gt 0 ]] && echo -e "${RED}Failures:${NC} ${FAILURES[*]}"
exit $([[ $FAIL -eq 0 ]] && echo 0 || echo 1)
