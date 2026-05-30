#!/usr/bin/env bash
# capture.sh — burstwave + server-side telemetry, one coordinated rerun.
#
# Wraps /tmp/burstwave with parallel docker-stats / goroutine-RSS-FD /
# pprof captures so the 2coh degradation can be classified (server-CPU-
# bound vs harness-client-bound vs goroutine/memory blowup) from one
# artifact set instead of needing a second rerun. The pprof CPU profile
# is centered on B's release moment (parsed from the harness's "second
# burst at +Ns" line) so we catch the burst transition.
#
# Usage:
#   test/burstwave/capture.sh --output <dir> [-- <burstwave args>...]
# Env:
#   BURSTWAVE  path to harness binary (default /tmp/burstwave)
#   CONTAINER  vigosrv container name (default vigo)
#   PPROF_DUR  CPU profile duration in seconds (default 60)
#   PPROF_LEAD seconds before B's release to start the profile (default 10)

set -euo pipefail

BURSTWAVE="${BURSTWAVE:-/tmp/burstwave}"
CONTAINER="${CONTAINER:-vigo}"
PPROF_DUR="${PPROF_DUR:-60}"
PPROF_LEAD="${PPROF_LEAD:-10}"

OUTDIR=""
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTDIR="$2"; shift 2 ;;
    --) shift; ARGS+=("$@"); break ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
[[ -z "$OUTDIR" ]] && { echo "usage: $0 --output <dir> [-- <burstwave args>]" >&2; exit 2; }
[[ -x "$BURSTWAVE" ]] || { echo "burstwave binary not found at $BURSTWAVE" >&2; exit 2; }
mkdir -p "$OUTDIR"

# Stamp run context so artifacts are self-describing.
{
  echo "captured_at: $(date -Iseconds)"
  echo "container:   $CONTAINER"
  echo "burstwave:   $BURSTWAVE"
  echo "pprof_dur:   ${PPROF_DUR}s   pprof_lead: ${PPROF_LEAD}s"
  echo "args:        ${ARGS[*]}"
  echo "server_ver:  $(docker exec "$CONTAINER" vigosrv --version 2>/dev/null || echo unknown)"
} > "$OUTDIR/manifest-capture.txt"

# Launch the harness, redirecting stdout/stderr so we can tail it.
"$BURSTWAVE" "${ARGS[@]}" --output "$OUTDIR" > "$OUTDIR/run.log" 2>&1 &
BW_PID=$!
trap 'kill $BW_PID 2>/dev/null || true; wait 2>/dev/null || true' EXIT

# docker stats every 1s — Name,CPU%,Mem,NetIO,Block,PIDs prefixed with epoch ts.
{
  echo "ts,name,cpu_pct,mem_usage,net_io,block_io,pids"
  while kill -0 "$BW_PID" 2>/dev/null; do
    docker stats --no-stream --format \
      '{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.NetIO}},{{.BlockIO}},{{.PIDs}}' \
      "$CONTAINER" 2>/dev/null | sed "s/^/$(date +%s),/"
    sleep 1
  done
} > "$OUTDIR/stats.csv" &
STATS_PID=$!

# goroutine count + RSS + open FDs every 5s.
{
  echo "ts,goroutines,rss_kb,open_fds"
  while kill -0 "$BW_PID" 2>/dev/null; do
    g=$(docker exec "$CONTAINER" sh -c \
      'wget -qO- --timeout=2 "http://127.0.0.1:6060/debug/pprof/goroutine?debug=1" 2>/dev/null | head -1 | grep -oE "[0-9]+"' 2>/dev/null || true)
    r=$(docker exec "$CONTAINER" sh -c \
      'awk "/VmRSS/{print \$2}" /proc/$(pgrep -n vigosrv)/status 2>/dev/null' 2>/dev/null || true)
    f=$(docker exec "$CONTAINER" sh -c \
      'ls /proc/$(pgrep -n vigosrv)/fd 2>/dev/null | wc -l' 2>/dev/null || true)
    echo "$(date +%s),${g:-},${r:-},${f:-}"
    sleep 5
  done
} > "$OUTDIR/resources.csv" &
RES_PID=$!

# pprof at B's release. Parse bStartSec from the harness banner so we
# track whatever --second-burst-at the operator passed (not hardcoded).
{
  for _ in $(seq 1 1800); do
    if grep -qE 'second burst at \+[0-9]+s' "$OUTDIR/run.log" 2>/dev/null; then break; fi
    sleep 1
  done
  storm_started=$(date +%s)
  bsec=$(grep -oE 'second burst at \+[0-9]+s' "$OUTDIR/run.log" | head -1 | grep -oE '[0-9]+')
  : "${bsec:=270}"
  delay=$((bsec - PPROF_LEAD))
  echo "[capture] storm started at $(date -Iseconds), B-release at +${bsec}s; sleeping ${delay}s then capturing ${PPROF_DUR}s CPU profile" >&2
  sleep "$delay"
  docker exec "$CONTAINER" sh -c \
    "wget -qO- --timeout=$((PPROF_DUR + 15)) 'http://127.0.0.1:6060/debug/pprof/profile?seconds=${PPROF_DUR}'" \
    > "$OUTDIR/cpu.pprof" 2>/dev/null
  # Peak-window goroutine dump (debug=2 = full stacks) and heap snapshot.
  docker exec "$CONTAINER" sh -c \
    'wget -qO- --timeout=10 "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2"' \
    > "$OUTDIR/goroutines-peak.txt" 2>/dev/null
  docker exec "$CONTAINER" sh -c \
    'wget -qO- --timeout=10 "http://127.0.0.1:6060/debug/pprof/heap"' \
    > "$OUTDIR/heap-peak.pprof" 2>/dev/null
  echo "[capture] pprof window done at $(date -Iseconds)" >&2
} &
PPROF_PID=$!

wait "$BW_PID"; BW_EXIT=$?
trap - EXIT
wait "$STATS_PID" "$RES_PID" "$PPROF_PID" 2>/dev/null || true

echo
echo "[capture] burstwave exit=$BW_EXIT"
echo "[capture] artifacts:"
ls -la "$OUTDIR"
exit "$BW_EXIT"
