
# Sizing a vigosrv host

You'll finish this page knowing **how many envoys one server can comfortably hold**, what happens past that line, and the operator levers (vertical headroom, horizontal federation) that move it.

**When you'd use this:** before standing up a new fleet, when planning a hardware refresh, or when an existing server's memory use climbs under convergence waves.

**When you'd skip this:** small fleets — anything under a few thousand envoys runs well below the regime this page is about, with default config and any modest VM.

> All figures derive from the measurement registry [`numbers-of-record.md`](../../internal.docs/numbers-of-record.md) (the single source of truth). The **8 vCPU / 32 GB** row is the measured anchor; the rest scale from it.

## The headline

**Two ceilings — memory, and at fast cadence, CPU — and which one binds depends entirely on your check-in cadence.**

- At **normal cadences (30s–5m)** the server is **memory-bound**. Budget **~1 GB of server RAM per 1,000 envoys** and you're sized correctly. This is the binding limit for most fleets.
- At **aggressive cadence (≤ 5s)** the per-check-in **CPU** to verify and process each contact binds first. Budget **~1 vCPU per 1,000 envoys at a 1s cadence** (×N for an N-second cadence — at 5s it's ~1 vCPU per 5,000).
- The **crossover is ≈ 4–6s**: faster → size for CPU; slower → size for memory.

## The sizing table

Max envoys per box. Built **conservatively** — the CPU column uses the heavier unary-poll cost (~0.80 ms/check-in) and the RAM column the heavier held-stream footprint (~623 KB/envoy), so you're safe whichever path your agents use. Headroom is baked in: CPU sized to 75 % of cores, RAM to ~60 % of physical.

| Server (vCPU / RAM) | Aggressive **≤ 5s** (CPU-bound, @1s) | Normal **30s – 5m** (RAM-bound) | Basis |
|---|---|---|---|
| 4 vCPU / 16 GB | ~3,700 | **~15,000** ⚠️ stay well under | measured (brownout) |
| **8 vCPU / 32 GB** | **~7,500** | **~30,000** | **measured anchor** |
| 16 vCPU / 64 GB | ~15,000 | ~60,000 | calculated |
| 32 vCPU / 128 GB | ~30,000 | ~123,000 | calculated |
| 32 vCPU / 256 GB | ~30,000 | ~245,000 | calculated (RAM unverified) |

**How to use it:**

1. Pick your cadence. Most fleets run 30s–5m → read the **RAM-bound** column.
2. Find your envoy count → choose the smallest box whose number exceeds it.
3. Running ≤ 5s? Check the CPU column too and take the **smaller** of the two.

**Example:** 25,000 envoys at 60s → memory-bound → **8 vCPU / 32 GB** (30k headroom). The *same* fleet at a 1s cadence is CPU-bound at ~7,500 on that box, so you'd step up to **32 vCPU / 128 GB**.

## Rules of thumb (the fast way)

- **RAM: ~1 GB of server RAM per 1,000 envoys** (~623 KB/envoy live heap + negligible base, kept under ~60 % of physical RAM). This is usually the binding limit.
- **CPU only matters at fast cadence:** ~1 vCPU per 1,000 envoys at 1s; ~1 vCPU per 5,000 at 5s; at 30s+ CPU is a non-issue (it scales with cadence).
- **Crossover ≈ 4–6s.** At 30s / 5m you are always RAM-bound — size by RAM.

## Caveats — read these before you trust a row

- **⚠️ Small boxes (≤ 16 GB) degrade, they don't crash.** As a 4/16 box approaches its RAM limit it enters a **GC brownout** — under the auto-`GOMEMLIMIT` default it spends nearly all CPU on garbage collection to honor the soft limit, the web UI / API / metrics go unresponsive for ~1.5m, and the box never reaches a clean OOM-kill. The useful ceiling on small boxes is *brownout onset*, well below a naive linear figure — **stay well under the number and don't run small boxes near the line.** (Bigger boxes with more cores reach a clean shed/OOM instead; the GC/CPU coupling that causes the brownout eases above ~6–8 cores.)
- **Measured at ~150 KB inventory per envoy.** Package-heavy fleets (~280 KB — many installed packages, CVEs, certs) run ~20 % heavier on RAM → knock ~15–20 % off the RAM-bound counts.
- **The 128–256 GB rows are calculated, not measured** — the at-scale (250k) RAM verification is pending. The 8/32 row is the measured anchor everything scales from.
- **Held-stream fleets** (the default — every online envoy holds a persistent AgentStream) are actually a bit *lighter* than this conservative table on CPU (~0.56 ms vs 0.80 ms/check-in) and the table already uses their heavier RAM figure — so you have margin, not a deficit.

## Why the ceiling is per-envoy memory

Each connected envoy costs **~623 KB of live server heap**: the in-memory FleetIndex entry (~275 KB — the flattened, indexed trait tree), the cached full inventory it last reported (~150 KB at a realistic 150 KB payload), and the held AgentStream connection (~200 KB — gRPC read/write buffers, three goroutines and their stacks, HTTP/2 stream and flow-control state, GC headroom). A non-held unary poller is ~554 KB — it drops ~69 KB by not holding the bidi stream. The cost is linear and predictable, which is why sizing is a single multiply: double the host RAM, double the envoy ceiling.

## Tunables that move the line

These live in `server.yaml` (see [server-yaml.md](../../reference/server-yaml.md)) and trade one resource for another. None turns a small host into a comfortable large one on its own — they nudge a host that's near its line.

- **`checkin.max_connections`** (default `0` → auto-derived from host RAM; `-1` → uncapped). Caps concurrently-held agent streams, refusing the excess with `ResourceExhausted` (the agent backs off and retries) so the server sheds instead of climbing toward the wall. **Note:** the auto-derived cap is built from a connection-buffer estimate and is *optimistic* against the ~623 KB/envoy inventory-aware cost — on a memory-bound box it can sit *above* the real safe ceiling, so don't treat the cap alone as your limit; size by the table above and set an explicit number if needed. Logged at startup as `held-stream admission cap auto-derived from host memory`.
- **`GOMEMLIMIT`** (Go's memory soft-limit). **Auto-derived to ~80 % of the cgroup-aware memory budget** — the ~20 % headroom is a transient GC-overshoot safety margin. It trades CPU for survival (and on small boxes that trade becomes the brownout described above), but it does **not** refuse connections — pair it with `checkin.max_connections` for an actual ceiling. No `server.yaml` key: set the standard `GOMEMLIMIT` env var (in docker-compose/systemd, alongside the container's memory limit), or `GOMEMLIMIT=off` for no limit.
- **`grpc.gogc`** (default `100`; the load-test image runs `200`). Higher reduces GC CPU at the cost of a larger heap working set; lower shrinks RSS peaks at the cost of more GC CPU.
- **`checkin.interval`** (the *agent-side* poll cadence). Connection count, not request rate, sets the memory ceiling — so a longer interval does **not** let you hold more envoys; it only lowers offered request rate (CPU and write-shed intensity). It is, however, exactly the lever that moves you from the CPU-bound column to the RAM-bound one.
- **`checkin.max_concurrent`** (default `0` → CPU-scaled, always on). Don't disable it. See [Sizing `max_concurrent`](../../reference/server-yaml.md#sizing-max_concurrent).

## When to federate (and how)

Federate via spanner when **any** of these is true for the planned fleet:

1. Envoy count on one host would exceed the safe ceiling for its RAM (the table above).
2. Correlated check-in events (post-publish waves, mass re-enroll, network-partition recovery) could push concurrent connections past the safe ceiling even if steady state would fit. These spikes land on memory directly — size for the peak, not the average.
3. Fault tolerance matters and you can't lose the one host without losing the fleet.

The spanner setup is in [Set up spanner](set-up-spanner.md); the rationale is ADR-026. Operator-visible cost: a few more moving parts (peer-equal bolts, hostname-pattern partitioning, multicast Tier-1 + REST Tier-2 admissions gossip) in exchange for linear scale-out and single-host-failure survival.

## How these numbers were measured

The figures are measured off-box on GCP (server VM + separate harness VM(s), so the offered load is real and not competing with the server for CPU), using [`test/fakefleet/`](../../../test/fakefleet/) to enroll synthetic envoys with a realistic 150 KB split-traits inventory and hold check-ins at the target cadence. Per-check-in CPU is `Δ process_cpu_seconds_total ÷ Δ check-in rate` over a settled 60s window (using the harness's own reported rate); per-envoy RAM is the settled **forced-GC live heap** (`pprof heap?gc=1`) ÷ enrolled count. The full method, run matrix, and per-figure provenance live in the registry: [`numbers-of-record.md`](../../internal.docs/numbers-of-record.md) and [`loadtest-per-cadence-runbook.md`](../../internal.docs/loadtest-per-cadence-runbook.md).

---
*Verified on Vigo 0.76.35 · 2026-06-24. Anchor: 8 vCPU / 32 GB measured; larger rows calculated.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
