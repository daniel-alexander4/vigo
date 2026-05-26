# vigo-burst — config-publish burst load test

A measurement-discipline load test focused on the **mass-config-publish
burst** scenario — what happens to a healthy fleet when an operator runs
`vigocli config publish` and every envoy's next CheckIn pulls a fresh
bundle within one interval window.

The simple `test/loadtest/` tool keeps existing — it stays the right
shape for "can the server do N CheckIns/sec at all." This one is for
deeper questions: where does the burst latency curve, when does the
server return to baseline, what bottleneck pops next at higher fleet
sizes.

## What's different from the simple loadtest

Per the QA review of the existing harness, this tool fixes the
measurement defects that make the simple tool's headline numbers
narrower than they look:

- **HDR-style bucketed histogram for latency** — lock-free per-bucket
  atomic counters; eliminates the mutex-and-slice-append in the per-call
  hot path that distorted client-side latency in the simple tool.
- **Per-second time series of percentiles + throughput** — so the burst
  spike + recovery curve is visible, not averaged into an 8-minute mean.
- **Warmup discard window** — first N seconds excluded from the
  reported "baseline" percentiles.
- **Failure breakdown by gRPC code** — `DeadlineExceeded` vs
  `Unavailable` vs `ResourceExhausted` rather than a single failure
  count.
- **Server-side resource sampling** — RSS, goroutine count, mutex wait,
  CPU profile durations pulled at fixed cadence from the loopback pprof
  endpoint during the burst.
- **Multi-trial replication** — `--trials N` runs the burst pattern N
  times and reports mean ± stdev of the headline metrics.
- **Realistic state-fingerprint cycling** — envoys remember the
  fingerprint the server returned and echo it back, so steady-state
  CheckIns see "no change" (the cheap path); the burst is the only
  thing that flips them onto the bundle-rebuild path.
- **Documented run config** — every run emits a `manifest.json`
  capturing hardware, OS, server version, full CLI flags, and the
  server.yaml hash so reproducing the trial doesn't require ESP.

## What it doesn't fix (yet)

- **Driver colocation** — the loopback driver still runs on the server
  host, sharing CPU. A wired-LAN third driver is the right fix and
  needs hardware that doesn't exist here.
- **CheckIn-only** — no `ReportResult` / `ReportTraits` / `Wake` calls
  yet. That's the next pending realism lever.

## Run

```bash
go run ./test/burst \
  --server 127.0.0.1:1530 \
  --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 10000 \
  --interval 5s \
  --warmup 60s \
  --baseline 60s \
  --burst-settle 180s \
  --cooldown 30s \
  --trials 3 \
  --output ./out/burst-$(date +%Y%m%d-%H%M%S)
```

The default phasing is:

1. **Enroll** (serial; not measured)
2. **Warmup** (60 s; not measured — cold connections + cold cache)
3. **Baseline** (60 s; measured — steady-state CheckIn cycle, no-change responses)
4. **Burst trigger** — REST `POST /api/v1/config/reload` with admin
   token bumps the server's config version + secrets_epoch. Every
   envoy's next CheckIn sees a stale `state_fingerprint` and gets a
   fresh bundle.
5. **Burst-settle** (180 s; measured per-second) — captures the spike
   and the recovery curve.
6. **Cooldown** (30 s; measured) — confirms steady-state returned.

## Output

Per trial, `--output <dir>` gets:

- `manifest.json` — hardware, OS, server version, full CLI args, server.yaml SHA256, trial timestamps
- `summary.json` — headline metrics (baseline + peak + settle-time + failure breakdown)
- `latency.csv` — per-second p50/p95/p99/p99.9/throughput/fail-count
- `server.csv` — per-sample RSS/goroutine_count/heap_mb/cpu_pct from pprof
- `console.txt` — human-readable run log

After all trials, an `aggregate.json` reports mean ± stdev of each
headline metric so a single number isn't quoted without its confidence
band.

## SLA gate

`--sla-baseline-p99 1s --sla-settle-time 30s --sla-failure-rate 0.001`
causes the binary to exit non-zero if the measured run violates any
named target. Useful for CI gating.
