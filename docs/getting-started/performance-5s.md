
# Performance Analysis (Theoretical): 5-Second Check-ins on 4 vCPU / 8 GB / SSD — 1,000 Nodes

> **All Vigo numbers in this document are calculated, not measured.** Vigo has no production deployment; nothing here has been proven end-to-end. The figures are derived from benchmarked per-request costs in the codebase and from the published [15-second analysis](performance-15s.md), scaled to a 5-second cadence. Treat them as projections, not results.
>
> Two assumptions are load-bearing and unverified: **(1)** the agent can actually sustain a ~5-second check-in cadence — on a non-trivial policy the agent's own convergence-cycle time (idempotency checks + `ReportResult`) is the real floor and is frequently well above 5 seconds, so "5s" here means the *configured* interval, not the *observed* one; and **(2)** server-side cost scales linearly with request rate. Where a competitor is marked **No** below, that reflects that tool's published practical floor or documented behavior — also not re-measured here, just cited.

This document analyzes Vigo server performance under 5-second check-in intervals on modest hardware, with a 1,000-node reference fleet. 5-second intervals are the practical *edge* of polling: drift corrected in roughly the time it takes to alt-tab, stale machines flagged in well under 20 seconds. Below this, adaptive streaming — not faster polling — is the right model (see [When Streaming Beats 5-Second Polling](#when-streaming-beats-5-second-polling)).

All numbers reference benchmarked values from the codebase or are derived from measured operations. See [performance-1m.md](performance-1m.md) for the per-request cost derivations; this page reuses those costs at 3× the request rate of the [15-second analysis](performance-15s.md).

See also: [1-minute intervals](performance-1m.md) | [30-second intervals](performance-30s.md) | [15-second intervals](performance-15s.md)

## Hardware Assumptions

- 4 vCPU (modern x86_64, e.g., Hetzner CPX21, DigitalOcean c-4)
- 8 GB RAM
- SSD storage (NVMe or SATA SSD, ~50k random IOPS)
- 1 Gbps network

## The Caveat That Matters Most: Configured Interval ≠ Observed Cadence

Vigo's agent runs a single loop: `CheckIn` → receive policy → apply resources idempotently → `ReportResult`. The next `CheckIn` does not fire until that loop finishes. So the *effective* check-in cadence is `max(configured_interval, convergence_cycle_time)`.

For a fleet doing 120–130 idempotency checks per cycle plus a `ReportResult`, the observed cadence at a configured 5 seconds is typically **7–80 seconds**, not 5. The server already accounts for this: the stale-detection threshold is `2.5 × max(configured_interval, observed_cycle)`, where `observed_cycle` is the agent's smoothed inter-check-in cadence reported every cycle.

So a 5-second configured interval delivers a true 5-second cadence only when the per-host policy is small enough that a full convergence cycle takes a second or two — a deliberately trivial baseline, an empty role, or a host whose modules are all no-op fast paths. A future `Heartbeat` RPC (decoupling liveness from convergence — [filed in the backlog](../../engineering/_index.md)) would let the agent emit a true 5-second tick regardless of how long convergence takes; until that lands, every "5s" responsiveness number below is the *best case*, achievable only on a light policy.

**The server-side cost numbers below are still valid** — they describe what the server does at 200 requests/sec for 1,000 nodes, however that rate is produced. They just shouldn't be read as "every node converges every 5 seconds" on a heavy policy.

## Responsiveness Profile (1,000 nodes, light policy)

| Metric | Value |
|--------|-------|
| Drift correction latency | 2.5–5 seconds (avg half-interval) — *if the agent sustains 5s* |
| Stale detection | ~12.5 seconds (2.5× the configured interval; in practice 2.5× the agent's observed cycle, which on a non-trivial policy is larger than 5s) |
| Config publish to full fleet convergence | ~5 seconds |
| New envoy visible on dashboard | ~5 seconds |
| Force-convergence effect | Within next check-in (~2.5s avg) |
| Dashboard compliance counter freshness | Real-time |

On a light policy this is the closest thing to a live mirror of the fleet. On a heavy policy, substitute the agent's actual cycle time for "5s" everywhere above.

## Request Rates

With 10% jitter (default), agents spread check-ins across a 4.5–5.5 second window:

| Fleet Size | Avg RPS | Peak RPS (burst) | Status |
|------------|---------|-------------------|--------|
| 100 | 20 | ~30 | Trivial |
| 500 | 100 | ~150 | Trivial |
| **1,000** | **200** | **~300** | **Trivial** |
| 2,000 | 400 | ~600 | Comfortable |
| 5,000 | 1,000 | ~1,500 | Tuning required |
| 7,500 | 1,500 | ~2,250 | Upgrade or spanner recommended |
| 10,000 | 2,000 | ~3,000 | Spanner strongly recommended |

At the 1,000-node reference point, 200 RPS is nothing — the server spends single-digit percentages of one core. The interest at 5s is what happens *above* 1,000 nodes: the request rate is 3× the equivalent 15-second fleet, so the headroom that felt limitless at 15s gets used up around 5,000–7,500 nodes.

## CPU Usage

Realistic per-check-in CPU cost including gRPC framing, protobuf deserialization, goroutine scheduling, ED25519 signature verify, and the TLS record layer: **200–300 µs** (no-change) and **500–800 µs** (full bundle).

| Fleet Size | Avg RPS | CPU (sustained no-change) | CPU (config change storm) |
|------------|---------|---------------------------|---------------------------|
| **1,000** | **200** | **~6% of 1 core** | **~13% of 1 core** |
| 2,000 | 400 | ~12% of 1 core | ~26% of 1 core |
| 5,000 | 1,000 | ~30% of 1 core | ~65% of 1 core |
| 7,500 | 1,500 | ~45% of 1 core | ~98% of 1 core |
| 10,000 | 2,000 | ~60% of 1 core | ~130% of 1 core (spills past one core) |

With 4 cores, the 1,000-node case is invisible — ~1.5% of total CPU sustained. The interesting line is the config-change storm at 10,000 nodes: every node converging inside one 5-second window is 2,000 full-bundle responses/sec, which is more CPU than a single core can provide for those 5 seconds (~130%). It still fits — that's only ~33% of a 4-core box — but it's the first interval where a single full-bundle storm doesn't fit on one core, and at 20,000+ nodes it would be ~260% of one core. Below ~2,000 nodes none of this is observable.

**Config change storm**: After `vigocli config publish`, all N envoys converge within one 5-second window. At 1,000 nodes that's 200 full-bundle responses/sec for ~5 seconds — CPU bumps to ~13% of one core, then drops back. Same total work as at any interval; just compressed into a 5-second window.

## Memory Usage

Memory is unchanged from longer intervals — it scales by fleet size (connection count), not check-in frequency.

| Component | 1,000 envoys | 5,000 envoys | 10,000 envoys |
|-----------|---------------|---------------|----------------|
| Go runtime + gRPC server | ~150 MiB | ~180 MiB | ~200 MiB |
| FleetIndex (state + indexes) | 1.6 MiB | 8 MiB | 16 MiB |
| Policy cache | <1 MiB | <1 MiB | <1 MiB |
| gRPC connection buffers | 64 MiB | 320 MiB | 640 MiB |
| Goroutine stacks | <1 MiB | <1 MiB | ~2 MiB |
| SQLite page cache | 100–300 MiB | 100–300 MiB | 100–500 MiB |
| **Total** | **~350 MiB** | **~850 MiB** | **~1.4 GiB** |

1,000 nodes at 5 seconds uses ~350 MiB — a rounding error on 8 GB. Even 10,000 nodes fits comfortably; memory never becomes the binding constraint before CPU does at this cadence.

## Network Bandwidth

Request sizes: ~3 KiB check-in request, ~100 bytes no-change response, ~30 KiB full bundle.

| Fleet Size | Steady-state (bidirectional) | Config change burst (5s) |
|------------|------------------------------|---------------------------|
| **1,000** | **~600 KB/s** | **~6 MB/s** |
| 5,000 | ~3 MB/s | ~30 MB/s |
| 10,000 | ~6 MB/s | ~60 MB/s |
| 15,000 | ~9 MB/s | ~90 MB/s |

At 5-second intervals, steady-state bandwidth is 3× the 15-second figure. 1,000 nodes is ~600 KB/s — under 0.5% of a 1 Gbps link. At 10,000 nodes, the config-change burst (60 MB/s for ~5 seconds) is ~half a 1 Gbps link for those 5 seconds — noticeable, and the first interval where a publish burst is genuinely visible on a 1 Gbps uplink.

**Consideration for metered or satellite links**: At 5-second intervals, 1,000 idle nodes consume roughly **1.5 GiB/month**; 10,000 nodes roughly 15 GiB/month. On a metered VPS or satellite uplink, 15-second or 30-second intervals are dramatically cheaper for little practical loss.

## SQLite Write Load

The flusher runs every 10 seconds and writes only the latest state per envoy, so it coalesces every check-in an envoy made during the window into one write.

| Fleet Size | Dirty envoys per flush | Batch transactions | SQLite write time |
|------------|-------------------------|--------------------|-------------------|
| **1,000** | **~1,000** | **2** | **~5 ms** |
| 5,000 | ~5,000 | 10 | ~25 ms |
| 10,000 | ~10,000 | 20 | ~50 ms |

Identical to every other interval. At a configured 5 seconds an envoy may check in once or twice per 10-second flush window, but the flusher still writes it once. SQLite load is a function of fleet size, not frequency.

**Traits writes**: At 5-second intervals, agents send traits 12× more often than at 1-minute intervals. The hash-dedup still catches 95%+ (traits rarely change every 5 seconds), so the actual write volume increase is negligible — unless the fleet has highly volatile traits (rapidly churning process lists, flapping interfaces), which would generate 12× more dedup misses.

## Comparison Across All Intervals

| Metric (10,000-node fleet) | 5-min | 1-min | 30-sec | 15-sec | 5-sec |
|-----------------------------|-------|-------|--------|--------|-------|
| Avg RPS | 33 | 167 | 333 | 667 | 2,000 |
| Drift correction (avg) | 2.5 min | 30s | 15s | 7s | 2.5s\* |
| Stale detection | 15 min | 3 min | 90s | 45s | ~12.5s\* |
| CPU (sustained) | ~1% | ~5% | ~10% | ~20% | ~60% |
| CPU (config storm) | ~2% | ~12% | ~24% | ~48% | ~130%† |
| Network (steady) | ~100 KB/s | ~500 KB/s | ~1 MB/s | ~2 MB/s | ~6 MB/s |
| Memory | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB | ~1.4 GiB |
| SQLite flush | ~50 ms | ~50 ms | ~50 ms | ~50 ms | ~50 ms |
| Config storm duration | ~5 min | ~60s | ~30s | ~15s | ~5s |

\* Best case — assumes the agent sustains a true 5-second cadence; on a non-trivial policy the agent's convergence-cycle time is the real floor (often 7–80s), so substitute that. † Spills past one core; with 4 cores it is still only ~33% of total CPU, for ~5 seconds.

Memory and SQLite are constant — they don't care about frequency. CPU and network scale linearly with request rate, so 5-second intervals are 3× the 15-second cost and 12× the 1-minute cost. The config storm is the same total work compressed into a 5-second window.

## Capacity Recommendations

### No Tuning: Up to ~2,000 Envoys

Default settings handle ~2,000 envoys at 5-second check-ins without configuration changes:

- ~12% of one core sustained (~3% of a 4-core box)
- ~450 MiB memory
- ~1.2 MB/s network
- ~10 ms SQLite flush every 10 seconds

The 1,000-node reference fleet sits well inside this — half the load of the ceiling.

### With Tuning: 2,000–5,000 Envoys

```yaml
checkin:
  interval: "5s"
  jitter_percent: 10
tuning:
  gogc: 200
  memory_limit: "6GiB"
  grpc_read_buffer: 16384
  grpc_write_buffer: 16384
```

Halves gRPC buffer memory (320 MiB → 160 MiB at 5,000 nodes). At 5,000 nodes the sustained cost is ~30% of one core and ~850 MiB — comfortable, but the config-storm CPU spike (~65% of one core for 5 seconds) and the network burst (~30 MB/s for 5 seconds) are now things you'd notice on a graph.

### Beyond ~5,000 Envoys

At 5-second intervals, 5,000+ envoys generates 1,000+ RPS sustained and a config-storm burst that approaches a full core. Options:

1. **Upgrade to 8 vCPU / 16 GB** — handles ~10,000 envoys at 5s with tuning.
2. **Spanner (hub-spoke)** — each spoke handles its own subset at 5s; the hub aggregates.
3. **Use 15-second intervals instead** — triples the fleet capacity on the same hardware for a 5s → 15s drift-correction trade most fleets won't notice.

For the common case — a 1,000-node fleet — none of this applies; default config is fine.

### When Streaming Beats 5-Second Polling

At 5-second polling the worst-case latency for an ad-hoc task dispatch is 5 seconds (best-case the agent's actual cycle time). If you need sub-second dispatch — incident-response automation, tight rolling deployments, live queries where milliseconds matter — switch the affected envoys to streaming rather than dropping the poll interval further. Adaptive stream promotion already does this: the server sets `stream_requested=true` when it has work, the envoy opens a stream on its next poll, and the 5-second interval becomes the worst-case promotion latency. For fleets where *every* envoy needs sub-second dispatch at all times, persistent streaming (not polling) is the model — at a cost of 64 KiB per idle stream in gRPC buffers.

In short: 5-second polling is the bottom of the polling regime. Anything that wants faster than that should be streaming.

## Bottleneck Hierarchy (5-Second Intervals)

1. **Agent convergence-cycle time** — the real floor on the effective cadence for any non-trivial policy. Until a `Heartbeat` RPC decouples liveness from convergence, a configured 5s is aspirational on heavy hosts. *This is the #1 reason "5s" may not mean 5s.*
2. **Config-storm CPU spike** — ~130% of one core for 5 seconds at 10,000 nodes (first interval where a publish burst exceeds one core). Fine on 4 cores; ~260% at 20,000+.
3. **Memory (gRPC buffers)** — 640 MiB at 10,000 connections; same as every interval. Tunable to half that.
4. **CPU (ED25519 verify + gRPC overhead)** — ~60% of one core sustained at 10,000 nodes; saturates 4 cores around 25,000–30,000 nodes.
5. **Network burst** — ~60 MB/s for 5 seconds at 10,000 nodes; ~half a 1 Gbps link, transiently.
6. **SQLite flusher** — ~50 ms at 10,000 nodes. Not a concern.

At 1,000 nodes, items 2–6 are all comfortably idle; item 1 is the only thing worth thinking about, and only if the per-host policy is large.

## When to Choose 5 Seconds vs 15 Seconds

Choose **5 seconds** when:

- A compliance SLA genuinely requires drift correction in under ~10 seconds.
- The fleet is small — roughly **≤ 2,000 nodes** on this hardware.
- The per-host policy is light enough that a full convergence cycle takes a second or two (otherwise the configured interval is fiction — see the caveat above).
- You want 5s as the *worst-case* stream-promotion latency, with streaming handling the actual fast path.

Choose **15 seconds** (or longer) when:

- 5–15 second drift correction is acceptable — which is almost always.
- The fleet is above ~2,000 nodes, or growing toward it.
- Per-host policies are non-trivial (most are), so the agent can't hit 5s anyway.
- Network is metered or bandwidth-constrained.
- You'd rather not run the server at the edge of the polling regime.

For true sub-5-second responsiveness, the answer is **streaming**, not a faster poll interval.

## Use Cases at 5-Second Convergence

At 5-second convergence Vigo is doing something no general-purpose configuration manager does — but the practical envelope is narrow (small fleets, light policies). The use cases that justify it:

- **Hard real-time compliance.** PCI-DSS / HIPAA / CIS controls on a small, security-critical fleet — firewall rules, user accounts, file permissions reverted within seconds of drift, with a continuous run-history trail. Most valuable when "within seconds" is a written requirement, not a nice-to-have.
- **Kiosk / POS / signage networks.** A few hundred locked-down, identical endpoints where drift is a security incident. Offline convergence keeps a store correct through a WAN outage; the 5-second cadence makes a tampered config self-heal almost immediately.
- **Security operations on a contained blast radius.** SSH hardening, certificate rotation, user cleanup on a set of internet-facing hosts — `when:` conditionals react to traits, query dispatch handles fleet-wide incident response, and a reverted config snaps back fast.
- **Build farms / lab environments.** ≤ 100 build agents that must stay identical; self-healing after developers break their own boxes; fleet-wide secret rotation via the watcher.

For anything larger or heavier — MSP fleets in the thousands, mixed-policy server estates, IoT at scale — [15-second](performance-15s.md) or [30-second](performance-30s.md) intervals are the right choice; the responsiveness difference is small and the headroom difference is large.

## Competitive Landscape

No existing configuration manager operates at 5-second intervals — not in practice, and in most cases not even in principle. Vigo's claim here is **theoretical and server-side-only**: the server can field the request rate; whether the agent can produce it depends on policy weight (see the caveat at the top). The honest comparison is "everything else has a documented practical floor far above 5 seconds; Vigo's floor is its own convergence cycle, which on a light policy can approach 5 seconds."

### Traditional Configuration Management

| Tool | Default interval | Practical minimum | 5-second capable? | Agent weight | Why not |
|------|------------------|-------------------|-------------------|--------------|---------|
| **Vigo** | 5 min | agent-cycle-bound (≈5s on a light policy) | **Yes — theoretical**, light policies only | ~5 MiB (Rust) | n/a — but unproven; bounded by convergence-cycle time on heavy policies |
| **Puppet** | 30 min | ~5 min | **No** | ~200 MiB (Ruby + gems) | PuppetDB (Clojure/JVM) + per-run catalog compile; the server cannot sustain sub-minute, let alone 5s |
| **Chef** | 30 min | ~5 min | **No** | ~100–200 MiB (omnibus Ruby) | `chef-client` is heavy per run; Chef Server (Erlang + PostgreSQL) is not built for sub-minute |
| **Ansible** | None (push-only) | N/A — no convergence loop | **No** | Agentless (SSH) | Push-only; one SSH session per run; AWX adds scheduling but SSH fan-out cannot approach a 5s fleet-wide cycle |
| **Salt** | 30 min+ | ~1 min | **No** | ~50–150 MiB (Python) | ZeroMQ transport is fast, but `state.apply` is expensive; ~1-minute practical floor |
| **CFEngine** | 5 min | ~1 min | **No** | ~3–10 MiB (C) | Tiny agent, but the hub's PostgreSQL-backed reporting chokes below ~1 minute |

### Event-Driven and Endpoint Platforms

| Tool | Model | 5-second capable? | General-purpose OS state? | Notes |
|------|-------|-------------------|----------------------------|-------|
| **mgmt** | Event-driven (inotify / etcd), not polling | **In principle, instant — but not in practice** | Limited | Not production-ready; no central server; tiny community; no fleet management |
| **Tanium** | P2P linear-chain endpoint platform | **Sub-15s for *queries* — but it is not a config manager** | No — query/observe, no idempotent enforcement loop | Proven at 500K+ endpoints; enterprise-only ($50–100/endpoint/yr); proprietary |
| **osquery** | Scheduled SQL queries against endpoint state | **No** | No — read-only observation, no enforcement | Often paired with a separate tool to act on findings |
| **ArgoCD / Flux** | GitOps reconciliation for Kubernetes | **No** — 3–5 minute sync | No — manages K8s resources, not OS state | Wrong layer entirely |
| **Crossplane / Terraform** | Plan/apply for cloud + infra resources | **No** — plan/apply cycles in minutes | No — not a convergence loop, not OS-level | Wrong layer entirely |

### Where Vigo Sits

| | 5-second check-ins? | General-purpose OS state | Lightweight agent | Fleet scale at this cadence |
|---|---|---|---|---|
| **Vigo** | **Yes — theoretical** (server cost modest; binding constraint is the agent's convergence-cycle time on a non-trivial policy — see disclaimer) | Yes (7 OSes) | ~5 MiB (Rust) | ~1,000–2,000 / node untuned; spanner beyond |
| **CFEngine** | **No** — ~1 min practical floor | Yes (Unix / Windows) | ~3–10 MiB (C) | 100K+ at ≥1 min |
| **Puppet** | **No** — ~5 min practical floor | Yes | ~200 MiB (Ruby) | 10K+ at ≥5 min (with PuppetDB) |
| **Chef** | **No** — ~5 min practical floor | Yes | ~100–200 MiB (Ruby) | 10K+ at ≥5 min |
| **Ansible** | **No** — push-only, no convergence loop | Yes | Agentless | bounded by SSH fan-out |
| **Salt** | **No** — ~1 min practical floor | Yes | ~50–150 MiB (Python) | 10K+ at ≥1 min |
| **Tanium** | **Partial** — sub-15s *queries*, no enforcement loop | No — queries only | Proprietary | 500K+ |
| **mgmt** | **In principle** (event-driven) — not production-ready | Limited | ~15 MiB (Go) | N/A (no central server) |
| **osquery** | **No** — read-only | No — queries only | ~15 MiB | — |
| **ArgoCD / Flux** | **No** — 3–5 min sync | No — Kubernetes / IaC | varies | — |

**The gap Vigo claims to fill:** at 5-second cadence the field is empty — nobody else does *any* OS-level state enforcement at this interval, theoretical or otherwise. CFEngine, the closest in spirit, has a ~1-minute floor; Puppet and Chef are ~5 minutes; Ansible has no loop at all; Tanium has the speed but is query-only and enterprise-priced. Vigo's distinguishing properties are real (5 MiB Rust agent, 7 OSes, gRPC/mTLS) — but the 5-second number itself is a projection: server-side it is cheap at 1,000 nodes, and the practical question is whether the per-host policy is light enough that the agent can keep pace. On a heavy policy it cannot, and that's the honest limit until a `Heartbeat` RPC separates liveness from convergence.

## The Sweet Spot at 5 Seconds

5-second check-ins make sense for a **small (≤ ~2,000-node), security-critical fleet with deliberately light per-host policy** — kiosk/POS networks, a contained set of internet-facing hosts, a build farm — where "drift reverted in seconds" is a stated requirement, and where the policy is small enough that the agent can actually hit the interval. For everything else — larger fleets, mixed or heavy policy, metered networks — [15-second](performance-15s.md) intervals deliver nearly the same responsiveness with 3× the headroom, and streaming covers the cases that need to be faster than any poll.

The spanner hub-spoke architecture extends the 5-second model to a regional topology: each spoke runs its own ≤2,000-node fleet at 5s, the hub aggregates compliance and routes admin operations. But again — these numbers are calculated, not measured. Treat the whole document as a design study, not a benchmark report.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
