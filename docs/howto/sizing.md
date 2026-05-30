
# Sizing a vigosrv host

You'll finish this page knowing **how many envoys one server can comfortably hold**, what happens past that line, and the operator levers (vertical tuning, horizontal federation) that move it.

**When you'd use this:** before standing up a new fleet, when planning a hardware refresh, or when an existing server starts logging `ResourceExhausted` shed counts during convergence waves.

**When you'd skip this:** small fleets — anything under a few thousand envoys runs well below the regime this page is about, with default config and any modest VM.

## The headline

**The comfortable single-host envelope is two-axis: how many envoys connect, and how often each one checks in.** The **connection count** sets where transport-layer instability begins — and that ceiling sits at the same place (~20,000 conns on this hardware) regardless of check-in interval. The **check-in interval** sets the offered request rate at that conn count, which determines whether crossing the ceiling stays manageable (the "edge") or escalates into a disconnect/reconnect storm ("broken").

On this page's hardware (8 logical CPUs, 31 GiB, NVMe), measured single-cohort steady state:

| Conns | 5-s check-in | 1-s check-in |
|---|---|---|
| ≤ 15,000 | comfortable (0 `Unavailable`) | comfortable (0 `Unavailable`) |
| **20,000** | edge (584 `Unavailable`) | edge (952 `Unavailable`) |
| **25,000** | **stressed (19,856 `Unavailable`)** — system holds, goroutines stable | **broken (290,808 `Unavailable`)** — disconnect/reconnect storm, goroutines swing |

Same conn count → same per-connection memory + goroutine footprint → same transport stress floor. Lower offered rate (5s vs 1s at the same conn count) just means the system can absorb more of that stress before it tips into the broken regime. **CPU is never the limiter** — peak CPU stayed under 500% (of 800% on 8 cores) in every test, including the broken ones.

Sustained-load + sudden-burst regimes break at *different* conn counts: a 5s baseline that holds steady at 25k can still break under a burst-onset event at 50k (a separate prior test ran 25k+25k overlapping cohorts at 5s and produced 368k `Unavailable`). Single-cohort numbers above are the *steady-state* envelope; plan for headroom if your fleet has correlated check-in waves (post-publish, mass re-enroll, recovery from a network partition).

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

**Measured matrix (sustained, single-cohort steady state, default config, `--results-per-report 12` so each cycle writes a 12-row report). Grouped by conn count so the same-conn-count pair is side-by-side — that's where the per-conn cost model shows up most clearly.**

| Envoys × interval | Offered ck/s | CPU mean / peak | Goroutines mean / peak | RSS peak | Checkin `p50` / `p99` | Reports admitted | `Unavailable` | Verdict |
|---|---|---|---|---|---|---|---|---|
| 5,000 × 5 s | 1,000 | 182% / 306% | 16,778 / 17,119 | 1.99 GB | 0.74 ms / 5.93 ms | 299k (≈570/s) | 0 | comfortable, cap idle |
| 5,000 × 1 s | 5,000 | 289% / 396% | 17,114 / 17,122 | 1.97 GB | 1.5 ms / 95 ms | 205k (≈340/s) | 0 | comfortable, cap engaging |
| 10,000 × 5 s | 2,000 | 243% / 449% | 31,073 / 32,115 | 2.90 GB | 0.74 ms / 759 ms | 266k | 0 | comfortable |
| 10,000 × 1 s | 10,000 | 312% / 421% | 31,037 / 32,141 | 2.87 GB | 759 ms / 1518 ms | 54k | 0 | comfortable, cap saturated |
| 15,000 × 5 s | 3,000 | 288% / 464% | 46,225 / 47,174 | 3.88 GB | 1.48 ms / 3037 ms | 216k | 0 | comfortable |
| 15,000 × 1 s | 15,000 | 308% / 455% | 37,769 / 47,121 | 3.70 GB | 759 ms / 3037 ms | 56k | 0 | comfortable, steady shedding |
| **20,000 × 5 s** | **4,000** | 307% / 478% | 60,768 / 62,126 | 4.81 GB | 2.97 ms / 6074 ms | 172k | **584** ⚠ | **edge — first cracks** |
| **20,000 × 1 s** | **20,000** | 317% / 499% | 62,118 / 62,118 | 4.77 GB | 1518 ms / 3037 ms | 62k | **952** ⚠ | **edge — first cracks** |
| **25,000 × 5 s** | **5,000** | 305% / 500% | 73,983 / 77,303 (stable) | 5.72 GB | 11.9 ms / 6074 ms | 106k | **19,856** | **stressed — holds, not broken** |
| **25,000 × 1 s** | **25,000** | 279% / 497% | 32k mean / 77k peak (*swings*) | 5.77 GB | 1518 ms / 6074 ms | 61k | **290,808** ✗ | **broken — disconnect storm** |

What the categories mean:

- **comfortable** — every request gets a fast answer or a clean shed (`ResourceExhausted` → agent backs off), CPU has headroom, RSS holds flat, no connection churn, zero transport failures.
- **edge** — first non-zero `Unavailable` (low hundreds). The conn-count pressure is starting to leak past gRPC's transport keepalive/flow-control headroom, but the system still admits and serves work at steady state.
- **stressed** — `Unavailable` in the tens of thousands. Real transport pain on some connections, but goroutine count stays *stable* (no disconnect/reconnect storm), CPU mean stays elevated (useful work is still happening), and the system holds.
- **broken** — `Unavailable` climbs into the hundreds of thousands; goroutine count *actively decreases mid-window* (connections being lost during the run, not at cleanup); CPU *mean drops* because the system is thrashing; harness records multi-second wall-clock latencies. The server doesn't crash, it just stops being a good operating point.

**Three things worth pulling out of the matrix:**

- **Connection count is the primary driver — for everything.** At any given conn count, 5s and 1s rows have nearly-identical goroutine count and RSS (compare the pairs at 5k, 10k, 15k, 20k). What changes with interval is offered rate, which changes `ResourceExhausted` shedding intensity and writer throughput — but not the underlying memory + transport state footprint.
- **The check-in interval determines whether the conn-count ceiling is recoverable or fatal.** Both intervals crack at the same 20k conns (584 vs 952 `Unavailable`). At 25k, the 1s axis falls into the disconnect/reconnect storm regime (290k `Unavailable`, goroutine swing) while the 5s axis stays stressed but stable (19k `Unavailable`, goroutines flat). The 5× lower offered rate gives the system enough recovery slack to absorb transport hiccups before they cascade.
- **The cap is always doing its job — it's not the limiter.** Through 20,000 envoys × 1s the always-on `checkin.max_concurrent` (default `max(128, NumCPU*16)` = 128 on this 8-core box) sheds excess cleanly: `ResourceExhausted` to the agent, agent backs off, no cascade. What breaks at the conn-count ceiling is the transport layer underneath the cap — not the cap.

## Why the conn-count ceiling sits where it does

Heap pprof on any high-conn-count run shows the same story: **~80% of `inuse_space` is gRPC per-connection buffers** (`bufWriter` + `bufio.NewReaderSize`) — about **30 KB per connection on the heap**, gRPC's default 32 KiB read + 32 KiB write buffer × every active stream. Total RSS per connection runs ~80–100 KB once you add transport goroutines and GC headroom. Per the matrix above, that's linear and predictable up to the edge.

At the comfortable point (≤15k conns) this is manageable: 1.5–3.7 GB of heap, ~17k–47k goroutines, GC sawtooth the runtime handles without pauses long enough to matter. **At 20k conns the floor of transport stress crosses what gRPC's keepalive + flow-control headroom can absorb** — some connections start failing transiently with `Unavailable`. At 25k the floor is higher still, and what determines whether the system holds or breaks is the *rate at which work is hitting the same connections*: 1s check-ins at 25k give the disconnect/reconnect storm enough fuel to self-sustain; 5s check-ins at 25k leave enough slack between events that the transport layer recovers each cycle.

The 290,808-vs-19,856 gap at 25k between the two intervals isn't a different *kind* of failure — it's the same underlying pressure either escalating into self-reinforcing churn or staying in a tolerable stressed equilibrium, depending on how much offered work compounds on the already-stressed connections.

**The mental model: vigosrv's ceiling is per-connection state, set by connection count alone.** Check-in interval is what determines whether crossing the ceiling produces edge / stressed / broken behavior. The 0.69.19 always-on CheckIn admission cap and the policy-bundle cache stopped the *request-rate* CPU cascade we used to have. What's left is connection-count cost — linear with envoy count, indifferent to interval until the system tips into self-sustaining churn.

## Tunables that move the line

These all live in `server.yaml` (see [server-yaml.md](../reference/server-yaml.md)) and trade one resource for another. None of them turn a 50k host into a comfortable 50k host on their own — they're for nudging a host that's at the edge.

- **`grpc.max_concurrent_streams`** (default `5000`). Per-connection HTTP/2 stream cap. Doesn't change envoy count, but bounds the per-conn stream-state footprint when an envoy is misbehaving.
- **`grpc.gogc`** (default `100`). Lower (e.g. `50`) shortens GC sawtooth (smaller RSS peaks, more CPU on GC); higher (e.g. `200`) trades the other way. At 50k the heap working set is large enough that GC frequency matters.
- **`grpc.memory_limit`** (Go's `GOMEMLIMIT`). Set this to leave headroom under the container limit; Go's GC will work harder to stay below it instead of OOMing the container.
- **`checkin.interval`** (the *agent-side* poll cadence, set in the agent config or fleet defaults). A 5-second interval at 25k envoys offers ~5,000 check-ins/s; a 30-second interval offers ~830. The relationship is linear — longer intervals push the comfortable envoy count up almost proportionally.
- **`checkin.max_concurrent`** (default `0` → CPU-scaled, always on). Don't disable this. See [Sizing `max_concurrent`](../reference/server-yaml.md#sizing-max_concurrent) in the config reference.

## When to federate (and how)

Federate via spanner when **any** of these is true for the planned fleet:

1. Envoy count on one server-host would exceed the conn-count ceiling (~15k comfortable, edge at 20k regardless of interval). The interval determines whether running *above* the ceiling is "stressed" or "broken," but the ceiling itself is conn-count-driven.
2. Connection memory at the chosen scale would push the host past ~80% of available RAM (rough rule: `envoy_count × 0.08 GB + 1 GB base`).
3. Correlated check-in events (post-publish waves, mass re-enroll, network-partition recovery) could plausibly push the fleet past the ceiling even if steady-state would be fine — the burst-onset regime breaks at lower conn counts than steady state (an earlier test ran 25k + 25k overlapping cohorts at 5s and broke at the cohort-B release point even though sustained 25k × 5s holds).
4. Fault tolerance matters and you can't lose the one server-host without losing the fleet.

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
