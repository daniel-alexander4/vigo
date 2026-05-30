
# Sizing a vigosrv host

You'll finish this page knowing **how many envoys one server can comfortably hold**, what happens past that line, and the operator levers (vertical tuning, horizontal federation) that move it.

**When you'd use this:** before standing up a new fleet, when planning a hardware refresh, or when an existing server starts logging `ResourceExhausted` shed counts during convergence waves.

**When you'd skip this:** small fleets — anything under a few thousand envoys runs well below the regime this page is about, with default config and any modest VM.

## The headline

**One vigosrv host comfortably holds ~25,000 envoys at a 5-second check-in interval.** Past ~50,000 connections the gRPC transport layer becomes unstable under bursty load (connections drop, clients reconnect-storm, the request mix degrades into a long-latency tail) — not because CPU runs out, but because the per-connection memory + goroutine state crosses a regime where any transient pressure (GC pause, transport flow-control hiccup, kernel scheduler latency) tips connections off the cliff faster than they can be re-stabilized.

**Past ~25k envoys on one host, the answer is federation, not bigger boxes.** Vigo's [spanner mode (ADR-026)](../adr/026-spanner-peer-equal-admissions.md) partitions the fleet by hostname pattern across peer-equal bolts; every bolt holds its own slice and they CRDT-gossip the admissions roster. That's the supported path beyond one host's comfortable envelope.

## What "comfortable" looks like, on what hardware

The numbers below are measured live, not extrapolated. The harness is [`test/burstwave/`](../../test/burstwave/) (a coordinated read-path + write-path burst harness that exercises the real agent cycle). The capture wrapper is [`test/burstwave/capture.sh`](../../test/burstwave/capture.sh) (CPU pprof + goroutine + RSS + FD telemetry alongside the harness).

**Server-host hardware used for the numbers below:**

| | |
|---|---|
| CPU | Intel i5-1155G7 — 4 cores, 8 logical CPUs (SMT-2), 2.5 GHz base / 4.5 GHz boost |
| Memory | 31 GiB DDR4 |
| Storage | NVMe SSD (Crucial P3), ext4, no separate /srv mount |
| OS / kernel | Linux Mint 22, kernel 6.8 |
| Container | Docker, no CPU or memory limit on the `vigo` container |
| Vigo version | 0.69.19 |

**25,000 envoys at 5-second check-in (sustained, single-cohort steady state):**

| | |
|---|---|
| Check-in `p50` / `p99` | 3–6 ms / 11–24 ms |
| Check-ins served / sec | ~4,800 |
| Check-ins shed / sec (`ResourceExhausted`) | ~4,500 (the always-on `checkin.max_concurrent` cap doing its job; agents back off and retry) |
| ReportResult admitted / sec | ~150 |
| Server CPU (8-core total = 800%) | ~270% mean, ~415% peak |
| Server RSS | ~5.6 GB peak |
| Goroutines | ~77,000 (transport + per-conn handlers — sustained, not growing) |
| Open FDs | ~28,000 |
| Crashes / fatal errors | 0 |

That's the **comfortable steady state**: every request gets a fast answer or a clean shed; CPU has headroom; RSS holds flat; no connection churn. A 25k-envoy fleet at 5-second check-in on this hardware is a healthy operating point.

## Why ~50k connections is a different regime

Doubling cohort A with an overlapping 25k cohort B (so the server holds 50,000 concurrent envoys) does **not** crash the server — but the steady state collapses. Under burstwave 25k+25k:

- Server CPU drops to ~110% mean (lower than 25k alone), with peaks ~315% during the cohort-B burst transition.
- Server RSS climbs to ~6.5 GB.
- Goroutines briefly hit ~80k, then **the count actively drops mid-storm to 27–38k** — connections are being lost during the run, not at cleanup.
- The harness records ~368k `Unavailable` errors (transport failures), 154k `DeadlineExceeded`, and a wall-clock latency tail in the 97–194 *second* range on the connections that survive.

Where the heap sits at 50k tells the story: **~80% of `inuse_space` is gRPC per-connection buffers** (`bufWriter` + `bufio.NewReaderSize`), about **30 KB per connection** — gRPC's default 32 KiB read + 32 KiB write buffer × every active stream. That's not a leak; it's a linear per-connection memory cost the server can't avoid without bigger-hammer tuning (see below). At 25k it's manageable. At 50k it crowds against everything else (Go heap working-set, GC pacing, scheduler runqueues), and the resulting transient pressure is what drops connections.

**The mental model: vigosrv's ceiling is per-connection state, not request rate.** The 0.69.19 always-on CheckIn admission cap and the policy-bundle cache stopped the *request-rate* cascade we used to have. What's left is connection-count cost, and that's linear with envoy count.

## Tunables that move the line

These all live in `server.yaml` (see [server-yaml.md](../reference/server-yaml.md)) and trade one resource for another. None of them turn a 50k host into a comfortable 50k host on their own — they're for nudging a host that's at the edge.

- **`grpc.max_concurrent_streams`** (default `5000`). Per-connection HTTP/2 stream cap. Doesn't change envoy count, but bounds the per-conn stream-state footprint when an envoy is misbehaving.
- **`grpc.gogc`** (default `100`). Lower (e.g. `50`) shortens GC sawtooth (smaller RSS peaks, more CPU on GC); higher (e.g. `200`) trades the other way. At 50k the heap working set is large enough that GC frequency matters.
- **`grpc.memory_limit`** (Go's `GOMEMLIMIT`). Set this to leave headroom under the container limit; Go's GC will work harder to stay below it instead of OOMing the container.
- **`checkin.interval`** (the *agent-side* poll cadence, set in the agent config or fleet defaults). A 5-second interval at 25k envoys offers ~5,000 check-ins/s; a 30-second interval offers ~830. The relationship is linear — longer intervals push the comfortable envoy count up almost proportionally.
- **`checkin.max_concurrent`** (default `0` → CPU-scaled, always on). Don't disable this. See [Sizing `max_concurrent`](../reference/server-yaml.md#sizing-max_concurrent) in the config reference.

## When to federate (and how)

Federate via spanner when **any** of these is true for the planned fleet:

1. Envoy count on one server-host would exceed ~25k at your chosen check-in interval.
2. Connection memory at the chosen scale would push the host past ~80% of available RAM (rough rule: `envoy_count × 0.030 GB + 1 GB base`).
3. Fault tolerance matters and you can't lose the one server-host without losing the fleet.

The spanner setup is documented in [Set up spanner](set-up-spanner.md) and the architectural rationale is in [ADR-026](../adr/026-spanner-peer-equal-admissions.md). Operator-visible cost: a small handful of additional moving parts (peer-equal bolts, hostname-pattern partitioning, multicast Tier-1 + REST Tier-2 admissions gossip) in exchange for linear scale-out and survival of any single-host failure.

## Re-running the measurements yourself

The capture wrapper is reusable on any deployment:

```bash
# build the harness once
go build -o /tmp/burstwave ./test/burstwave

# run it under the capture wrapper — same flags as burstwave, plus --output
test/burstwave/capture.sh --output /tmp/sizing-run -- \
  --server 127.0.0.1:1530 --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 25000 --interval 5s --results-per-report 12 \
  --warmup 30 --duration 300 \
  --server-container vigo
```

The wrapper produces `cpu.pprof`, `goroutines-peak.txt`, `heap-peak.pprof`, `resources.csv` (goroutines + RSS + FDs every 5s), `stats.csv` (docker stats every 1s), and `run.log` (the harness's per-second results) — enough to characterize a host without a second rerun. Analyze the CPU profile with `go tool pprof -http=:8080 /tmp/sizing-run/cpu.pprof`.

---
*Verified on Vigo 0.69.19 · 2026-05-29.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
