# vigo-resultstorm

A load harness for the server's **run-result ingestion path** — the
write-heavy half of the agent lifecycle that `test/loadtest` and
`test/burst` never touch.

## What it measures, and why it's different

`loadtest` and `burst` exercise `CheckIn`: a read-mostly RPC that
short-circuits on an unchanged state fingerprint. Real agents also call
`ReportResult` after every convergence, and *that* is where the writes
happen. Per report the server:

1. `CreateRun` — one `runs` row,
2. `BatchCreateRunResults` — **M** `run_results` rows (M = `--results-per-report`),
3. `FinishRun` — updates the run row,
4. updates the in-memory fleet index + recent-runs ring,
5. dispatches integration events (`run.*`, and `drift.detected` when any
   result changed),
6. `InsertFileSnapshot` for changed `file` results carrying
   `previous_content` (only when `--snapshot-bytes > 0`).

SQLite is a single writer. This harness exists to find the wall that
imposes: **rows/sec is the headline number**, and the symptom to watch
is `ReportResult` p99 climbing as the `run_results` table grows during
the run. Mutex/block wait in `server.csv` is the contention fingerprint
of write serialization.

The synthetic envoys never `CheckIn`. Enrollment records the ed25519
pubkey; `ReportResult` needs only that pubkey + a valid signature (the
server generates its own `run_id` and ignores the agent's, using the
agent's value purely as the signed nonce). So the tool enrolls, then
goes straight to the write storm.

## Prerequisites

The bootstrap trusted-enrollment config must token-free-enroll the
hostnames this tool generates (`storm-NNNNN.test` by default). Enrollment
passes when **(a)** a pattern glob-matches the hostname *and* **(b)** the
driver's source IP falls in that pattern's `cidrs` (the CIDR is checked
against the connecting peer's address — `RemoteAddr` — not the
`ip_address` in the request body).

A permissive config like the default already covers this — no change
needed if you drive from the server box or the LAN:

```yaml
trusted_enrollment:
  - pattern: "*"
    cidrs: ["127.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8"]
```

`pattern: "*"` matches `storm-*.test` (matching is `filepath.Match`; `*`
spans any non-`/` run, dots included), and loopback/LAN source IPs land
in those `cidrs`. If you instead keep a tight allow-list, add a pattern
that matches the prefix and a `cidrs` entry for the host you drive from:

```yaml
trusted_enrollment:
  - pattern: "storm-*.test"
    cidrs: ["127.0.0.0/8"]   # widen to the driver's subnet if not loopback
```

`cidrs` is a list (omit it to allow any source IP for that pattern).
After editing, `vigocli config publish` and let the server reload.

Server-side sampling reads pprof inside the container via `docker exec`
— the container needs the pprof listener on `127.0.0.1:6060` (the burst
harness assumes the same). Pass `--server-container ""` to disable
sampling if you're driving a non-container server.

## Usage

```bash
go run ./test/resultstorm \
  --server 127.0.0.1:1530 \
  --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 2000 \
  --interval 5s \
  --results-per-report 12 \
  --warmup 30 --duration 300 \
  --output ./run-$(date +%s)
```

That offers `count / interval` reports/s and `M ×` that in rows/s — here
≈ 400 reports/s and ≈ 4 800 `run_results` INSERTs/s. To push the writer
harder, lower `--interval`, raise `--count`, or raise
`--results-per-report` (more rows per report concentrates write pressure
without more connections).

### Key knobs

| Flag | Default | Effect |
|---|---|---|
| `--count` | 1000 | simulated envoys (one persistent gRPC conn each) |
| `--interval` | 5s | per-envoy report cadence; storm rate = count/interval |
| `--results-per-report` | 12 | `run_result` rows per report |
| `--changed-ratio` | 0.15 | fraction `changed=true` → drift events + `DriftDetected{hostname,module}` metric (high-cardinality, on purpose) + snapshot eligibility |
| `--error-ratio` | 0.02 | fraction carrying `error_message` → `failure` run status |
| `--snapshot-bytes` | 0 | >0 makes changed `file` results carry that many bytes of `previous_content`, exercising the heavier `InsertFileSnapshot` path |
| `--rpc-timeout` | 30s | per-report deadline; a write queued behind the writer past this surfaces as `DeadlineExceeded` |
| `--warmup` / `--duration` | 30 / 300 | discarded warmup, then the measured window |
| `--start-at` | — | RFC3339 barrier; pass the same value to two driver hosts to align phase clocks (drive from a separate box so the driver isn't stealing the server's cores) |

## Output

Console prints a per-second line (reports/s, rows/s, p50/p99/max,
failures by class) and a measured-window summary. With `--output`:

- `manifest.json` — every knob + server.yaml SHA + vigosrv version (admin token redacted)
- `results.csv` — per-second time series (`reports, rows, p50…max, fail_{internal,exhausted,other}`)
- `server.csv` — server RSS / heap / goroutines / mutex-wait / block-wait deltas

Read the run by asking: did rows/s hold flat, or did p99 climb as the
table grew? Did failures appear as `Internal` (write failed/timed out)
or `ResourceExhausted` (load-shedder)? Is `mutex_wait_ms` rising in step
with offered rows/s?

## Cleanup

Enrolled envoys are deleted at the end (`--skip-cleanup` to keep them).
The `runs` / `run_results` rows written during the test persist in the
DB — this is intentional load, but on a long-lived DB you may want to
prune `storm-*` runs afterward.
