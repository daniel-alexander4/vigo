<!--
Copyright (c) 2026 Alexander4, LLC
SPDX-License-Identifier: LicenseRef-Proprietary
-->

# ADR-033: Conditional traits transmission — vitals/inventory split

**Status:** Accepted — implemented (first cut) 2026-06-04, dormant until the
fleet is rebuilt/redeployed. **Spike (2026-06-04) refined the decision from delta
encoding to a stable/volatile split (the agent already maintains the
classification); see Spike Findings.** Proto + server + agent shipped (`82a24b5e`
/ `4df7ca36` / `c850416b`); the inventory cache is bounded to the live fleet via
`FleetIndex` lifecycle eviction (0.74.12); and both the unary and stream check-in
paths split (stream-path split shipped 0.74.13 — the gate moved behind interior
mutability so the `&self` stream builder shares it); and `cert_lifecycle` no
longer emits `days_remaining` (0.74.14 — derived server-side from `not_after` via
`server/certexpiry`, so the collector is deterministic and stops flapping the
inventory hash daily). The FleetIndex-partition optimization is deferred
(network-bound server, idle CPU — see deferred.md); a separate array-ordering
determinism audit is tracked in pending.
**Date:** 2026-06-04

## Follow-on (0.76.20 / 0.76.21): server-side per-tick CPU

The original "network-bound, not CPU-bound" reading (and the "idle CPU"
rationale above) was measured on a **toy-blob** fleet (synthetic ~400 B traits).
Re-measured off-box with **realistic 150 KB inventory** at 1 s cadence, the
server *was* CPU-bound: the steady `no_change` check-in cost ~4.8 ms/tick (~90%
of 8 cores at 1.5k envoys), dominated by the per-tick shallow `json.Unmarshal`
of the full ~155 KB merged blob (~48%) and `traitsBatch.Mark`'s `sha256` of that
blob (~36%) — both run *before* the no_change short-circuit to feed work that
only needs the ~5 KB vitals (swarm ingest) or a stable inventory field
(os/arch). Two fixes finish ADR-033's split on the server side:

- **0.76.20** — drop the per-tick full parse: parse only the ~5 KB vitals;
  derive + cache os/arch once per inventory change on `inventoryEntry`; the
  validity gate moves into `reconstructTraits` (resend on malformed). So the
  ~150 KB inventory is never re-scanned on a vitals-only tick.
  (`server/grpc/checkin_traits_split.go`, `agentstream.go`.)
- **0.76.21** — `traitsBatch.Mark` dedups on a cheap change-key
  (`inventory_hash` + `sha256(vitals)`) + lazy blob materialization, not a
  `sha256` of the full ~155 KB blob every tick. (`server/db/traitsbatcher.go`.)

Measured net: **4.82 → 1.72 ms/checkin (−64%), ~1,250 → ~3,500 safe envoys @1s**
on 8 vCPU. The remaining per-tick cost is the snapshot-flush churn that changing
vitals drive (Mark fires every tick) — tracked as "option C" (snapshot on
inventory change only, a UX-affecting follow-on) in `pending.md`; the capacity
table lives in `docs/internal.docs/numbers-of-record.md`.

## Context

The agent attaches the full `traits_json` to **every** `CheckIn` — there is no
change-gate (`agent/src/checkin/client.rs:385-399`; `collect_traits` →
`serde_json::to_vec` → sent each cycle). Measured against the real home-lab
fleet, that payload is large:

| Envoy | traits raw |
|---|---|
| danlap | 219 KB |
| annlap | 293 KB |
| girlslaptop | 332 KB |

A 2026-06-04 load investigation established the server is **network-bound, not
CPU-bound**: under a 13.6k-envoy run vigosrv served check-ins with 6 of 8 cores
idle while the link saturated; on loopback (no wifi) it served 8k check-ins/s
cleanly. So the *cost of traits that matters is the wire cost.* Re-sending
~220 KB every check-in, where almost nothing has changed, is the dominant
upstream load — a 60k fleet at a 30s cadence pushes ~3.5 Gbps of raw traits.

The obvious fix — "hash the payload, send only when it changes" — is **dead on
arrival.** The traits carry *live* runtime values: `uptime.seconds`, `time`,
`loadavg`, `memory.{free,percent}`, `swap.used`. They change every collection,
so the whole-payload hash changes every check-in and the gate never fires.
Critically, that volatile tail is **~582 bytes — 0.3% of the 219 KB.** A tiny,
ever-changing tail is dragging ~213 KB of stable inventory across the wire every
second.

### Rejected alternatives

- **Whole-payload hash gate** (B in the discussion thread) — dead, per above.
- **gRPC compression** — ~5× on all traffic for a near-trivial change, but
  measured ~1.0 ms server decompress per 219 KB check-in (~1 core per 1,000
  check-ins/s); it trades the network bound for a CPU bound at high rates and
  still re-sends the redundant bulk. Kept as a smaller, orthogonal follow-on
  (it can layer on top of a delta), not the answer.
- **Collector inventory/vitals split** — tag the ~5 volatile collectors
  (`Uptime`/`LoadAvg`/`Memory`/`Swap`/`Time`, `CollectorKind` in
  `agent/src/traits/mod.rs:222`) as always-send, hash-gate the rest. ~200×, but
  brittle: mixed collectors (`filesystem`/`mountpoints` free-space) carry
  volatile fields that poison "inventory," and it requires correctly
  classifying every collector's volatility forever — a misclassified volatile
  field silently disables the gain.

## Spike findings (2026-06-04)

The spike measured the real danlap payload (218,731 B) against the **classification
the agent already maintains** for collection cost — `CachedCollectors` in
`agent/src/traits/cached.rs` splits collectors into `volatile_collectors()` (run
every check-in), `stable_collectors()` (every `trait_refresh_cycles`, default
**10**), and `periodic_collectors()` (~1 hour):

- **Volatile set — 5,219 B (2%)**: `time`, `uptime`, `loadavg`, `mountpoints`,
  `filesystem`, `users`, `memory`, `swap`, `ports`, and the swarm subsystems
  (`swarm`, `swarm_seeds`, `gitback`, `curator`, `poolq`, `puddle`, `longdrawer`,
  `lockbox`), `agent_self`, `host_self`, `host_containers`, `battery`.
- **Stable + periodic inventory — 213,512 B (97%)**: `packages` (80 KB),
  `cert_lifecycle` (63 KB), `security_scan`, `kernel_module_attestation`, `dmi`,
  `kernel`, etc. — refreshed only every 10 cycles.

Two consequences invert the original decision:

1. The per-collector volatility classification this feature needs **already
   exists** — the split is not the brittle, hand-maintained tagging the Context
   feared.
2. The **swarm** collectors are *volatile* (sent every check-in already), so the
   swarm ingest heartbeat is untouched by reducing inventory traffic — the
   freshness-from-liveness decouple is **not** a prerequisite.

## Decision

Split the wire payload into **vitals** (the volatile collectors, ~5 KB, sent every
check-in) and **inventory** (stable + periodic collectors, ~213 KB, sent only when
it changes). This rides the agent's existing `volatile_collectors()` /
`stable_collectors()` / `periodic_collectors()` classification — no new tagging to
maintain, and **no diff/patch machinery**.

### Wire shape

`CheckInRequest`:
- `bytes vitals_json` — the volatile collectors' output, sent every check-in.
- `bytes inventory_json` — the stable+periodic output, sent **only when its
  content hash changes** (or on a server resend request).
- `string inventory_hash` — hash of the inventory the agent currently holds, sent
  every check-in so the server detects desync.

`CheckInResponse` gains `bool resend_inventory` (modeled on `stream_requested` /
`force_update`).

### Agent

`CachedCollectors` already serializes volatile separately from stable/periodic.
The agent hashes the stable+periodic group, sends `vitals_json` every cycle, and
sends `inventory_json` only when that hash differs from what it last sent (or
`resend_inventory` was set). Requirement: stable collectors must be
**deterministic** (sorted output, no per-refresh timestamps) so an unchanged
inventory re-serializes byte-identically and the hash gate holds.

### Server

Caches the inventory per envoy (rides the already-cached `FleetIndex` flattened
form — see Resolved Questions). Each check-in it merges fresh vitals + cached
inventory into the full traits and **processes exactly as today** — swarm ingest,
flatten, `when:`/lookups, arch. On an `inventory_hash` mismatch / cache miss it
sets `resend_inventory = true` and uses the inventory it has until the agent
resends.

### Delta encoding — considered, not chosen

Delta (JSON Merge Patch of the full traits against a server-held baseline) was the
original decision. It is marginally more robust — content-driven, so it tolerates
a non-deterministic stable collector by sending just the noisy field rather than
re-sending 213 KB. But it needs diff/patch machinery and a full per-envoy
baseline, and the spike showed the agent already maintains the volatile/stable
split — making the split simpler for the same ~98% cut. Delta stays the fallback
if stable collectors carry unavoidable per-refresh noise that erodes the gate.

## Consequences / tradeoffs

- **~98% steady-state wire reduction** (measured: ≈5 KB vitals vs ≈219 KB today;
  the 213 KB inventory ships only on an actual change), directly on the measured
  bottleneck. The server also merges vitals onto cached inventory instead of
  re-parsing ~213 KB every cycle — a secondary, non-bottleneck CPU win.
- **Self-healing on desync.** Any `inventory_hash` mismatch — agent restart,
  server restart, a dropped check-in — triggers a full inventory resend on the
  next cycle; there is no wedged state and no manual recovery.
- **Backward compatible by construction.** An agent that still sends full
  `traits_json` processes exactly as today, so mixed fleets work during the
  rollout. Per the Linux-first cadence, Windows/BSD agents keep sending full
  until ported.
- **Cross-cutting.** Proto + Rust agent + server. The agent holds only the
  last-sent inventory hash (32 bytes), not a full snapshot copy.
- **Orthogonal to compression and confidentiality.** gRPC compression can layer
  on the (already much smaller) vitals if wanted; this changes *what* is sent, not
  how it is encrypted (mTLS) or framed.

## Resolved by the spike

1. **Which collectors churn per cycle → the volatile set, ~5 KB (2%).** Measured
   above, and already enumerated by `volatile_collectors()` — nothing to
   re-discover. The stable+periodic inventory (~213 KB, 97%) refreshes every 10
   cycles and ships only on change.
2. **Baseline storage → ride the `FleetIndex` flattened form; no second
   structured baseline.** The only per-check-in consumer of *structured* traits is
   swarm ingest, and the swarm sub-trees are *volatile* — sent every cycle — so
   the server always has them fresh. Everything else (`when:`/lookups, UI)
   consumes the **flattened** traits the `FleetIndex` already caches; the
   inventory-hash gate refreshes that flattened cache only when inventory changes.
   No ~13 GB structured-baseline copy is needed. (envoy_traits history stores
   inventory on change; volatile vitals are live-only, not historized — consistent
   with their nature as runtime metrics.)
3. **Granularity → whole-collector split, not field-level.** The vitals set is
   only ~5 KB; field-level diffing inside it buys little and adds machinery. The
   volatile collectors ship whole every cycle.
4. **Sync robustness → resend-on-mismatch, same shape unary or streamed.** The
   `inventory_hash` rides every check-in (unary and AgentStream-multiplexed
   alike); any mismatch sets `resend_inventory` and the agent resends next cycle.
   First contact and either-side restart are just the no-cached-inventory case →
   resend. No wedged state.

**Residual concern.** Stable collectors must serialize **deterministically** (no
per-refresh timestamps, sorted maps) or the inventory hash flaps every 10 cycles
and re-ships 213 KB needlessly. Slice 2 audits the stable set for non-determinism;
`security_scan` / `cert_lifecycle` are the likeliest offenders (scan timestamps).
If one is irreducibly noisy, exclude its noisy field from the hashed inventory, or
fall back to delta for that robustness.

## Implementation notes

- **Slice 1 (done 2026-06-04)** — measurement spike + this ADR + `_index.md`
  entry. Confirmed the win (~98%), inverted the design to the split, and resolved
  the storage shape (ride FleetIndex flattened).
- **Slice 2** — audit the stable collectors for serialization non-determinism
  (the residual concern); proto fields (`vitals_json`, `inventory_json`,
  `inventory_hash`, `resend_inventory`) + regenerate; agent: serialize the
  volatile vs stable+periodic groups separately (reusing `CachedCollectors`), hash
  the inventory, gate `inventory_json` on hash change, track last-sent hash +
  resend flag.
- **Slice 3** — server: cache inventory per envoy onto the FleetIndex flattened
  form, merge vitals each check-in, `resend_inventory` on hash mismatch /
  cache-miss; reconstruction feeds the existing CheckIn path unchanged.
- **Slice 4** — tests (vitals-only round-trip, inventory-change resend,
  desync/restart recovery, mixed full-vs-split fleet) + a before/after wire-size
  measurement on the live home-lab agents (the synthetic harness can't show it —
  fakefleet traits are ~400 B).

Cross-references: the gRPC-response-compression and swarm-freshness-decouple
follow-ons (`pending.md`); ADR-028 set the precedent of evolving the
`CheckInRequest` body for cross-cutting agent↔server concerns.
