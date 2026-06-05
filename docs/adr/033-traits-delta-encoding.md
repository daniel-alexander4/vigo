<!--
Copyright (c) 2026 Alexander4, LLC
SPDX-License-Identifier: LicenseRef-Proprietary
-->

# ADR-033: Conditional traits transmission via delta encoding

**Status:** Proposed 2026-06-04. Design captured; gated on a measurement spike (see Open Questions) before implementation.
**Date:** 2026-06-04

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

## Decision

Adopt **delta encoding.** The agent sends only the portion of its traits that
changed relative to the last full snapshot the server holds; the server
reconstructs the full traits by applying the delta to a cached baseline. The
delta is **content-driven** — whatever changed (the volatile tail on a steady
check-in, a package install on a real change) is exactly what crosses the wire —
so there is **no per-field volatility classification** to maintain and new or
mixed collectors are handled automatically.

### Wire shape

`CheckInRequest` gains:
- `bytes traits_delta` — the JSON Merge Patch (RFC 7386) of changed top-level
  subtrees relative to the baseline.
- `string traits_base_hash` — hash of the baseline the agent diffed against, so
  the server can detect desync.

The existing `traits_json` carries the **full** snapshot, sent on first contact,
on a base-hash mismatch, or when the server requests a resend. `CheckInResponse`
gains `bool resend_traits` (modeled on the existing server-directive flags
`stream_requested` / `force_update`).

### Agent

Retains its last-sent traits. Each cycle it computes the merge patch (changed
top-level subtrees) against the current collection and sends `traits_delta` +
`traits_base_hash`. With no prior baseline, or when the previous response set
`resend_traits`, it sends the full `traits_json` and records the new baseline.

### Server

Holds a per-envoy baseline. On a delta check-in it verifies
`traits_base_hash == cached baseline hash`, applies the patch to reconstruct the
full traits, and then **processes exactly as today** — swarm ingest, flatten
into `FleetIndex`, arch lookup, `when:`/lookups bundle building. On a mismatch
or cache miss it returns `resend_traits = true` and uses whatever baseline it
has until the agent resends full.

### Why this avoids the swarm-freshness rework

Because the server reconstructs the **full** traits every check-in, the
`PeerTracker` swarm ingest still runs every check-in with the complete blob set,
so the per-blob `LastSeen` heartbeat and eviction-aging are unchanged — no
freshness-from-liveness decouple is required on the reconstruction path. (That
decouple, tracked separately, becomes relevant only if a future optimization
chooses to *skip* ingest on swarm-unchanged deltas.)

## Consequences / tradeoffs

- **~50× steady-state wire reduction** (≈220 KB → a few-KB patch of the volatile
  subtrees), directly on the measured bottleneck; larger at aggressive cadences.
  The server also applies a small patch instead of re-parsing ~220 KB — a
  secondary, non-bottleneck CPU win.
- **Self-healing on desync.** Any base-hash mismatch — agent restart, server
  restart, a dropped check-in — triggers a full resend on the next cycle; there
  is no wedged state and no manual recovery.
- **Backward compatible by construction.** An agent that sends full `traits_json`
  with no delta processes exactly as today, so mixed fleets work during the
  rollout. Per the Linux-first cadence, Windows/BSD agents keep sending full
  until ported.
- **Cross-cutting.** Proto + Rust agent + server. The agent must retain its
  last-sent snapshot (one extra in-memory copy per agent — cheap, single node).
- **Orthogonal to compression and confidentiality.** gRPC compression can layer
  on the (already much smaller) delta if wanted; this changes *what* is sent, not
  how it is encrypted (mTLS) or framed.

## Open Questions

The spike (Slice 1) must answer these before committing to implementation —
each can shrink the win or change the storage shape:

1. **Which collectors actually churn per cycle?** The ~50× estimate assumes
   volatile changes stay confined to small subtrees
   (`memory`/`loadavg`/`uptime`/`time`/`swap`/fs-usage). If `network`,
   `listeners`, `docker`, or `processes` also change every cycle (unmeasured),
   the delta is larger and the win shrinks. Instrument consecutive-snapshot
   diffs on the real fleet first.
2. **Baseline storage.** Can the server reconstruct everything it needs —
   especially the `swarm`/`swarm_seeds` sub-trees for ingest — by applying the
   delta to the **already-cached `FleetIndex` flattened form**, or does it need a
   second full structured baseline per envoy (~213 KB × N ≈ 13 GB at 60k)? The
   flattened form may be lossy for sub-tree reconstruction.
3. **Patch granularity.** Top-level-subtree merge patch (simple, ~50×) vs
   field-level JSON Patch / RFC 6902 (smaller deltas, more machinery). Start with
   merge patch unless (1) shows the volatile subtrees are themselves large.
4. **Base-hash sync robustness.** Confirm the resend-on-mismatch loop converges
   under dropped / out-of-order check-ins and either-side restarts without a
   stuck state, and that `AgentStream`-multiplexed check-ins carry the delta the
   same way as unary ones.

## Implementation notes

Deferred until the spike resolves the Open Questions. Sketch:

- **Slice 1** — measurement spike (consecutive-snapshot churn per collector on
  the real fleet) + this ADR + `_index.md` entry. Decides whether the win
  justifies the feature and which baseline-storage shape to build.
- **Slice 2** — proto fields (`traits_delta`, `traits_base_hash`,
  `resend_traits`) + regenerate; agent delta computation, last-sent baseline,
  resend handling.
- **Slice 3** — server baseline + patch-apply + `resend_traits` on desync;
  reconstruction feeds the existing CheckIn path unchanged.
- **Slice 4** — tests (delta round-trip, desync/restart recovery, mixed-fleet
  full-vs-delta) + a before/after wire-size measurement on the live home-lab
  agents (the synthetic harness can't show it — fakefleet traits are ~400 B).

Cross-references: the gRPC-response-compression and swarm-freshness-decouple
follow-ons (`pending.md`); ADR-028 set the precedent of evolving the
`CheckInRequest` body for cross-cutting agent↔server concerns.
