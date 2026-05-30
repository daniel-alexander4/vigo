
# Sizing a vigosrv host

You'll finish this page knowing **how many envoys one server can comfortably hold**, what happens past that line, and the operator levers (vertical tuning, horizontal federation) that move it.

**When you'd use this:** before standing up a new fleet, when planning a hardware refresh, or when an existing server starts logging `ResourceExhausted` shed counts during convergence waves.

**When you'd skip this:** small fleets — anything under a few thousand envoys runs well below the regime this page is about, with default config and any modest VM.

## The headline

**The comfortable single-host envelope is two-axis: how many envoys connect, and how often each one checks in.** Both axes feed the same underlying limit (per-connection state — gRPC HTTP/2 buffers, goroutines, GC pressure on the working-set heap). Faster check-ins drive more concurrent in-flight stream activity per connection, so the comfortable conn count drops as the interval shrinks; slower check-ins push the comfortable conn count up almost proportionally.

On this page's hardware (8 logical CPUs, 31 GiB, NVMe):

| Check-in interval | Comfortable | First cracks | Broken |
|---|---|---|---|
| **5 s** | ~25,000 envoys | between 25k and 50k *(untested in-band)* | ~50,000 connections under bursty load |
| **1 s** | ~15,000 envoys | ~20,000 envoys (first non-zero `Unavailable`) | ~25,000 envoys (291k `Unavailable`, disconnect-storm) |

The breaking shape is identical at both intervals: per-connection state crosses a threshold where transient pressure (GC pause, flow-control hiccup, scheduler latency) drops connections faster than they can be re-stabilized, and the resulting reconnect-storm degrades the steady state. CPU is **not** the limiter — peak CPU stayed under 500% (of 800% on 8 cores) in every test, including the broken ones.

**Past the comfortable envelope, the answer is federation, not bigger boxes.** Vigo's [spanner mode (ADR-026)](../adr/026-spanner-peer-equal-admissions.md) partitions the fleet by hostname pattern across peer-equal bolts; every bolt holds its own slice and they CRDT-gossip the admissions roster. That's the supported path beyond one host's comfortable envelope.

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

**Measured matrix (sustained, single-cohort steady state, default config, `--results-per-report 12` so each cycle writes a 12-row report):**

| Envoys × interval | CPU mean / peak | Goroutines (mean / peak) | RSS peak | Checkin `p50` / `p99` | Reports admitted | Transport failures | Verdict |
|---|---|---|---|---|---|---|---|
| 5,000 × 1 s | 289% / 396% | 17.1k / 17.1k | 1.97 GB | 1.5 ms / 95 ms | 280–417/s | 0 | clean, lots of headroom |
| 7,500 × 1 s | 321% / 416% | 24.1k / 24.9k | 2.41 GB | 95 ms / 759 ms | 135–334/s | 0 | clean, cap engaging |
| 10,000 × 1 s | 312% / 421% | 31.0k / 32.1k | 2.87 GB | 759 ms / 1518 ms | ~150/s | 0 | clean, cap saturated |
| 15,000 × 1 s | 308% / 455% | 37.8k / 47.1k | 3.70 GB | 759 ms / 3037 ms | ~170/s | 0 | clean, steady shedding |
| **20,000 × 1 s** | 317% / 499% | 62.1k / 62.1k | 4.77 GB | 1518 ms / 3037 ms | ~190/s | **952** ⚠ | **edge — first cracks** |
| **25,000 × 1 s** | 279% / 497% | 60 / 32k / **77k swing** | 5.77 GB | 1518 ms / 6074 ms | ~185/s | **290,808** ✗ | **broken — disconnect storm** |
| 25,000 × 5 s | 266% / 415% | 70k / 77k | 5.57 GB | 3–6 ms / 11–24 ms | 80–150/s | 0 | clean, writer-bottlenecked |
| **50,000 × 5 s (burst overlap)** | 107% mean | 27–80k swing | 6.55 GB | 95 ms / 97 s | ~70/s | **368k** ✗ | **broken — same shape** |

What "comfortable" means in this table: every request gets a fast answer or a clean shed (`ResourceExhausted` → agent backs off), CPU has headroom, RSS holds flat across the measured window, no connection churn, zero transport failures.

What "broken" means: connections drop *during* the storm (goroutine count actively decreases mid-window), `Unavailable` errors climb into the hundreds of thousands, the CPU *mean* drops while the system thrashes (less useful work gets done), and the harness records a multi-second wall-clock latency tail on whatever survives. The server doesn't crash; it just stops being a good operating point.

**Two things worth pulling out of the matrix:**

- **Connection count drives writer throughput too, not just memory.** Compare 5k × 1s vs 25k × 5s — same 5,000 checkins/s offered, same 60,000 row-INSERTs/s offered, but the lower-conn-count run admits **2–5× more reports** (280–417/s vs 80–150/s). The single SQLite writer's per-write cost is fixed; what shrinks at higher conn counts is the share of CPU it can hold against verify + interceptor work, *and* the latency it sees between syscalls as the heap working-set grows and GC pauses lengthen.
- **The cap is always doing its job — it's not the limiter.** Through 20,000 envoys × 1s the always-on `checkin.max_concurrent` (default `max(128, NumCPU*16)` = 128 on this 8-core box) sheds the excess cleanly: `ResourceExhausted` to the agent, agent backs off, no cascade. What breaks at the conn-count ceiling is the transport layer underneath the cap.

## Why the connection-count ceiling is the limiter at any interval

Both broken rows above (`25k × 1s` and `50k × 5s overlap`) show the same signature: goroutines that had been sustained suddenly start dropping mid-storm, `Unavailable` errors jump from zero to six-figures, CPU mean *decreases* because connections aren't there to drive work, and the harness records seconds-long wall-clock latencies on the survivors. Heap pprof on the broken runs tells you why: **~80% of `inuse_space` is gRPC per-connection buffers** (`bufWriter` + `bufio.NewReaderSize`), about **30 KB per connection on the heap** — gRPC's default 32 KiB read + 32 KiB write buffer × every active stream. Total RSS per connection runs ~80–100 KB once you add transport goroutines and GC headroom.

At the comfortable point (15k × 1s or 25k × 5s) this is annoying but manageable: 1.5–2.5 GB of heap, 4–6 GB RSS, a GC sawtooth the runtime handles without pauses long enough to matter. At the broken point (25k × 1s or 50k × 5s) it crowds Go's working-set + scheduler + HTTP/2 server state hard enough that any transient pressure (a GC pause running long, a flow-control window stalling, a kernel scheduler delay) drops connections before they can be re-stabilized. Once connections start dropping the clients reconnect-storm, which adds new transport-establish work on top of the already-pressured server, and the system is stuck in the degraded regime for the rest of the window.

**The mental model: vigosrv's ceiling is per-connection state — driven by connection count *and* per-connection in-flight stream activity (which is set by check-in interval).** The 0.69.19 always-on CheckIn admission cap and the policy-bundle cache stopped the *request-rate* CPU cascade we used to have. What's left is connection-count cost, and that's linear with envoy count and amplified by faster check-ins.

## Tunables that move the line

These all live in `server.yaml` (see [server-yaml.md](../reference/server-yaml.md)) and trade one resource for another. None of them turn a 50k host into a comfortable 50k host on their own — they're for nudging a host that's at the edge.

- **`grpc.max_concurrent_streams`** (default `5000`). Per-connection HTTP/2 stream cap. Doesn't change envoy count, but bounds the per-conn stream-state footprint when an envoy is misbehaving.
- **`grpc.gogc`** (default `100`). Lower (e.g. `50`) shortens GC sawtooth (smaller RSS peaks, more CPU on GC); higher (e.g. `200`) trades the other way. At 50k the heap working set is large enough that GC frequency matters.
- **`grpc.memory_limit`** (Go's `GOMEMLIMIT`). Set this to leave headroom under the container limit; Go's GC will work harder to stay below it instead of OOMing the container.
- **`checkin.interval`** (the *agent-side* poll cadence, set in the agent config or fleet defaults). A 5-second interval at 25k envoys offers ~5,000 check-ins/s; a 30-second interval offers ~830. The relationship is linear — longer intervals push the comfortable envoy count up almost proportionally.
- **`checkin.max_concurrent`** (default `0` → CPU-scaled, always on). Don't disable this. See [Sizing `max_concurrent`](../reference/server-yaml.md#sizing-max_concurrent) in the config reference.

## When to federate (and how)

Federate via spanner when **any** of these is true for the planned fleet:

1. Envoy count on one server-host would exceed the comfortable envelope for your check-in interval (~15k at 1s, ~25k at 5s; scale roughly linearly with interval — a 30s interval comfortably holds several × more envoys than a 1s interval at the same conn-state pressure).
2. Connection memory at the chosen scale would push the host past ~80% of available RAM (rough rule: `envoy_count × 0.08 GB + 1 GB base`).
3. Fault tolerance matters and you can't lose the one server-host without losing the fleet.

The spanner setup is documented in [Set up spanner](set-up-spanner.md) and the architectural rationale is in [ADR-026](../adr/026-spanner-peer-equal-admissions.md). Operator-visible cost: a small handful of additional moving parts (peer-equal bolts, hostname-pattern partitioning, multicast Tier-1 + REST Tier-2 admissions gossip) in exchange for linear scale-out and survival of any single-host failure.

## Re-running the measurements yourself

The capture wrapper is reusable on any deployment:

```bash
# build the harness once
go build -o /tmp/burstwave ./test/burstwave

# run it under the capture wrapper — same flags as burstwave, plus --output.
# --second-count 1 + --second-burst-at 1.0 keeps cohort B effectively disabled
# (one envoy released at end of A's window), so this is a clean single-cohort
# steady-state measurement. Drop those two flags to test the overlap regime.
test/burstwave/capture.sh --output /tmp/sizing-run -- \
  --server 127.0.0.1:1530 --api https://127.0.0.1:8443 \
  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)" \
  --count 15000 --second-count 1 --interval 1s --results-per-report 12 \
  --warmup 30 --duration 300 --second-burst-at 1.0 \
  --server-container vigo
```

The wrapper produces `cpu.pprof`, `goroutines-peak.txt`, `heap-peak.pprof`, `resources.csv` (goroutines + RSS + FDs every 5s), `stats.csv` (docker stats every 1s), and `run.log` (the harness's per-second results) — enough to characterize a host without a second rerun. Analyze the CPU profile with `go tool pprof -http=:8080 /tmp/sizing-run/cpu.pprof`.

---
*Verified on Vigo 0.69.19 · 2026-05-29.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
