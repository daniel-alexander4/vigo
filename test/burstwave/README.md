# vigo-burstwave

A load harness for **two overlapping read+write bursts** — it tests what happens
when a second convergence wave hits the server before the first drains.

## What it does

Each synthetic envoy runs the real agent cycle every `--interval`: **CheckIn**
(read — pull policy) then **ReportResult** (write — persist run results). So a
burst drives both the SplitDB read pool and the single-writer pool, not just
the write path that `test/resultstorm` exercises.

Two cohorts are enrolled up front:

- **Cohort A** (`--count`) storms from t0 — `--warmup` discarded, then a
  `--duration` measured window.
- **Cohort B** (`--second-count`, default = `--count`) is released when A
  reaches **`--second-burst-at`** of its measured window (`0.8` = 80%),
  overlapping A's tail and roughly **doubling the offered load** for the
  overlap. B then fires for its own `--duration`.

Both cohorts must be enrolled before either fires — enrolling tens of thousands
of envoys takes minutes, so the second "burst" couldn't overlap if it enrolled
on demand. Peak concurrent connections ≈ `count + second_count`.

## What it measures

The per-second `results.csv` **splits read from write** and marks how many
cohorts are firing, so the headline question is answerable directly:

```
elapsed_sec, active_cohorts, checkin_ok, checkin_p50_ms, checkin_p99_ms,
             report_ok, report_p50_ms, report_p99_ms, report_shed, other_fail
```

When `active_cohorts` flips 1 → 2 (the second burst joining), do **reads stay
fast** (`checkin_p50/p99`) while **writes shed harder** (`report_shed` rises,
`report_p99` climbs)? That's the read/write-split behavior under doubled write
load. `server.csv` samples RSS / goroutines / mutex-wait throughout to capture
the overlap spike.

**Caveat:** `CheckIn` is read-*mostly* but largely served from the in-memory
FleetIndex (signature verify + bundle build + a *batched* `last_seen` write),
so `checkin_*` latency reflects the check-in path, not pure read-pool
contention. The write path (`ReportResult` → single writer + admission
semaphore) is where the overlap actually bites.

## Prerequisites

Same as `test/resultstorm`: the bootstrap `trusted_enrollment` must token-free
enroll the generated hostnames (`wave-a-NNNNN.test`, `wave-b-NNNNN.test`) — a
permissive `pattern: "*"` from loopback/LAN covers it. Server pprof on
`127.0.0.1:6060` for the `--server-container` sampling (pass `""` to disable).

## Usage

```bash
go run ./test/burstwave \
  --server 127.0.0.1:1530 --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 25000 --interval 5s \
  --warmup 30 --duration 300 --second-burst-at 0.8 \
  --output ./run-$(date +%s)
```

That enrolls **50,000** envoys (A=25k + B=25k); at the overlap, ~10k checkins/s
+ ~10k reports/s are offered against the single writer's few-hundred/s ceiling,
so expect heavy `report_shed` (`ResourceExhausted`) in the overlap while reads
stay cheap. Set `--second-count` to make B a different size, or `--count`
smaller for a lighter run. Enrolled envoys are deleted at the end
(`--skip-cleanup` to keep them).
