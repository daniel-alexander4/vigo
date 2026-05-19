---
title: "ADR-026: Peer-equal admissions over pull-based snapshots"
---

# ADR-026: Peer-equal admissions over pull-based snapshots

**Status:** Approved — not yet implemented. Slice 2A of the tungol redesign Phase 2.
**Date:** 2026-05-18

## Context

Phase 1 of the tungol redesign (commit `55dc2dcd`, 0.65.10) shipped a per-steorra Ed25519 identity, signed `SteorraStatusReport` messages, and pin-on-first-sighting on the hub. The hub keeps `steorraPins` — a map of `(tungol_id, steorra_id) → SteorraPin{pubkey, pinned_at, last_seen}` — that authorizes incoming reports and rejects mismatches with a `tungol_steorra_pubkey_mismatch` audit event. The integration test landed in commit `796c5ecd` exercises the full Reporter → gRPC → `RecordSteorraReport` path end-to-end.

Phase 1's pin state has three properties Phase 2 needs to fix:

1. **In-memory only.** `Hub.steorraPins` is a `map[string]*SteorraPin` with no persistence. A hub restart drops every pin; the first report from each steorra after restart re-pins under whatever pubkey arrives, which silently re-opens the first-sighting trust window every time the hub bounces.
2. **Hub-centric.** Pins live on one process. There is no peer for the hub to compare pins with; if an attacker compromises the hub, no other vigosrv can detect or contest the pin state.
3. **Read-only from the steorra side.** Steorras assert their identity via signed reports but have no role in admission — the operator-edited `tungol.steorras:` list in the hub's server.yaml is still the source of truth for which steorras are members.

Phase 2 of the tungol redesign moves admissions to a peer-equal CRDT — any steorra can admit or revoke; all steorras converge — so Phase 1's pin layer becomes a view over a replicated, persisted, signature-verified data structure rather than ephemeral in-memory state.

The ratified design (2026-05-16 second-pass /discuss, captured in project memory) called for snapshot-based replication on a Tier-1 30s / Tier-2 5m cadence with two orthogonal CRDTs: an admissions LWW-Map keyed on pubkey and a grow-only consumed-tokens G-Set keyed on token_hash. The architectural framing was "tungol is a sixth swarm content subsystem; reuses `server/swarm/manifest.go` LWW-Map semantics."

Inspection on 2026-05-18 revealed the gap between framing and substrate: `server/swarm/manifest.go`'s `Merge()` is explicitly marked "reserved for future cross-server gossip" and is never invoked across servers. The swarm transport is envoy↔envoy at port 1531; no server↔server gossip channel exists. The ratified reuse is of the LWW-Map *shape*, not of a wire. This ADR locks the wire.

Phase 2 is sliced as:

- **Slice 2A** (this ADR) — admissions LWW-Map: data model, persistence, pull-based replication transport, signature verification, audit events
- **Slice 2B** — consumed-tokens G-Set, riding the same transport
- **Slice 2C** — license attestation observability (publisher surfacing + `tungol_license_attestation_conflict` alarm); no cryptographic close until tessera adds `licensed_to_pubkey`
- **Slice 2D** — multi-vigosrv test harness (in-process dual server, partition simulator, CRDT property tests)

Operator-facing verbs (`vigocli tungol steorra {invite,approve,revoke,leave}`) are a follow-on to 2A, scoped once the CRDT shape ships and stabilizes.

## Decision

### Transport: pull-based snapshots over existing mTLS

Each vigosrv exposes a new admin gRPC `PullTungolSnapshot(PullTungolSnapshotRequest) returns (PullTungolSnapshotResponse)` on its existing mTLS admin endpoint. The RPC is available on **every** vigosrv regardless of `tungol.role` — hub, steorra, and standalone all answer it identically with their own local view of the admissions CRDT.

Each steorra runs a 30s pull loop against every peer it knows about. On each tick:

1. Dial the peer over mTLS (reuses the existing per-steorra `Reporter` dial path).
2. Call `PullTungolSnapshot`. Response carries the peer's full `tungol_members` view, each entry Ed25519-signed by its `updated_by` steorra.
3. For every incoming entry, verify the signature against the pinned pubkey for that `updated_by` steorra (the same pin path Phase 1 established).
4. Merge into the local CRDT using LWW on `(updated_at, updated_by)`. Tie-break: revocation tombstones win over admission entries at equal timestamp; otherwise the higher author string wins (deterministic, not cryptographic — same shape as ADR-008).

The pull is one-shot per tick — no persistent streams, no liveness detection, no reconnect logic. A failed pull logs and retries on the next tick. Failover semantics mirror the Phase 1 Reporter's existing failover behavior.

The Tier-2 5m roster snapshot from the second-pass /discuss is a separate cadence in the same RPC family — out of 2A scope; this ADR locks only Tier-1 (admissions counters + roster). Tier-2 lands in 2B or later when its consumers (envoy-roster aggregation) need it.

### Peer discovery: hub-seeded + gossip-of-gossip

A new steorra on first boot knows only the hub's address (configured at enrollment time, as today). On its first pull cycle it calls `PullTungolSnapshot` against the hub, which returns its CRDT view *plus* a `known_peers` field listing every other steorra it currently pulls from. The new steorra adds those addresses to its own pull set.

On every subsequent pull, the `known_peers` field is exchanged bidirectionally — gossip-of-gossip. A steorra learns about peers it has not yet contacted by seeing them in others' pull sets, and the set converges within ~2–3 cadence ticks of any membership change.

Operators may declare `tungol.peers:` in server.yaml as an explicit seed list. Declared peers are **merged** with hub-discovered peers — not exclusive. Use cases: multi-hub deployments, air-gapped subnets where hub-only-discovery is insufficient, audited environments that require operator-declared topology.

The hub remains the bootstrap-discovery rendezvous in 2A — analogous to how it already mints enrollment tokens and signs CSRs. Retiring the hub role entirely is a later-slice decision; 2A is peer-equal admissions, not peer-equal everything.

### Data model: `tungol_members` LWW-Map

A new SQLite/Postgres table `tungol_members` persists the admissions CRDT:

| Column          | Type    | Notes                                                                  |
|-----------------|---------|------------------------------------------------------------------------|
| `tungol_id`     | TEXT    | Mesh anchor (operator-declared in Phase 1).                            |
| `steorra_id`    | TEXT    | Per-steorra short name.                                                |
| `pubkey`        | BLOB    | 32-byte Ed25519 raw pubkey. Set on first sighting; immutable thereafter for the same `(tungol_id, steorra_id)` — mismatch produces an audit event and a rejected merge. |
| `status`        | TEXT    | `admitted` or `revoked` (tombstone).                                   |
| `admitted_at`   | INTEGER | unix-ms of first admission.                                            |
| `admitted_by`   | TEXT    | `steorra_id` that produced the admission.                              |
| `updated_at`    | INTEGER | unix-ms of latest LWW write — winning timestamp.                       |
| `updated_by`    | TEXT    | `steorra_id` that produced the latest write.                           |
| `signature`     | BLOB    | Ed25519 sig over the canonical entry bytes, by `updated_by`'s key.     |

Primary key: `(tungol_id, steorra_id)`. The signature covers a canonical encoding of the entry with `signature` zeroed — the same convention as `SteorraStatusReport` signing (`server/tungol/reporter.go` Phase 1 path).

Phase 1's `steorraPins` becomes a read-through view over `tungol_members`. On hub or steorra startup, the view is built from the persisted table; pin state survives restart. Mismatch detection (the `tungol_steorra_pubkey_mismatch` event) keeps its Phase 1 behavior — it now fires when an incoming report's pubkey conflicts with the CRDT-persisted pubkey for that `(tungol_id, steorra_id)`, not just an in-memory pin.

### Audit events

Two new event types emitted on local CRDT mutation (not on every merge — only on entries that actually change the local state):

- `tungol_steorra_admitted` — payload `{tungol_id, steorra_id, pubkey_short, admitted_by, admitted_at}`
- `tungol_steorra_revoked` — payload `{tungol_id, steorra_id, pubkey_short, revoked_by, revoked_at, reason?}`

Phase 1's `tungol_steorra_pubkey_mismatch` event is retained — it now fires against CRDT-persisted pubkeys rather than ephemeral pins, but the event shape is unchanged.

### What 2A does *not* ship

The following are explicitly out of this slice:

- **vigocli verbs** (`invite`, `approve`, `revoke`, `leave`) — separate slice. 2A's CRDT is mutable via direct DB writes from a not-yet-built admin path; the verb tree comes once the CRDT shape is stable.
- **Consumed-tokens G-Set** — Slice 2B. Same transport, different CRDT.
- **License attestation observability** — Slice 2C.
- **Retiring `RoleHub`/`RoleSpoke`/`RoleStandalone`** — the enum stays as an architectural marker. Hub still owns bootstrap discovery and license enforcement in 2A.
- **Generalizing `server/swarm/manifest.go` into a shared CRDTStore** — premature. The right time to evaluate is after 2B and 2C land and the pattern is clearer.
- **Full multi-vigosrv test harness** — 2A tests stay at the package level with mocked RPC clients. The integration harness lands in 2D when 2B and 2C make it pay off.
- **Cryptographic close on license attestation** — blocked on tessera adding `licensed_to_pubkey`. 2C ships observability only.
- **Tier-2 (envoy roster, 5m cadence)** — separate RPC; later slice.

## Consequences

- Phase 1 pin state survives hub restart. The first-sighting trust window opens exactly once per steorra, ever — not once per hub-restart.
- Any steorra can admit or revoke; eventual consistency in ~30s typical, ~60s worst-case (one pull window for the local write to reach every peer).
- Federation membership scales without N×N config edits — adding the seventh steorra means editing zero existing server.yamls; the new steorra is discovered and admitted via the CRDT path.
- Hub becomes a bootstrap-discovery node, not a write coordinator. The `RoleHub`/`RoleSpoke`/`RoleStandalone` enum stays as an architectural marker; later slices may retire it after the CRDT-driven admission flow stabilizes.
- Every pull verifies signatures against pinned pubkeys — a compromised hub cannot forge admissions for other steorras' identities.
- Pull cadence of 30s means admissions propagate in seconds, not milliseconds. Acceptable for admission events (rare, not user-facing latency-sensitive). Sub-second propagation is non-goal.
- N steorras × M peers = N×M RPCs per pull window. Bounded by snapshot size — `tungol_members` is O(steorras), not O(envoys); at the v1 target (10 steorras × 5K envoys = 50K envoys), the snapshot is at most a few KB. The N×M factor is acceptable up to the documented v1 target; past that, a snapshot-delta protocol becomes worth designing.
- Operators may declare `tungol.peers:` explicitly for audited or multi-hub topologies. The declared list is additive to hub-seeded discovery.
- Trade-off: 30s pull cadence means a revoked steorra has up to one pull window to misuse its identity before peers stop accepting its reports. Mitigation — once any one peer learns the revocation, the next pull propagates it everywhere; the worst case is bounded by 2 × cadence ≈ 60s.
- Trade-off: gossip-of-gossip means a partition between two clusters of steorras converges only when at least one cross-cluster pull path exists. Operators with intentional partition tolerance requirements should declare `tungol.peers:` explicitly to guarantee cross-partition discovery.

## Alternatives considered

- **Hub-relay gossip (Path B from the 2026-05-18 /scrutinize).** Hub stays as the write coordinator; steorras pull from the hub only; the hub fans out admissions via existing admin RPCs. Rejected because it collapses to the existing hub-spoke shape with extra CRDT mechanics — doesn't deliver peer-equal write semantics, just adds eventual-consistency machinery on top of a centralized topology. The peer-equal target is structural, not just semantic.
- **Bidirectional streaming over existing mTLS.** Each pair of steorras holds a persistent gRPC stream and pushes deltas as they occur. Rejected because (a) it's a substantially heavier wire surface for the same eventual-consistency guarantee pull gives, (b) it doesn't match the ratified Tier-1 30s / Tier-2 5m snapshot cadence, (c) the existing `AgentStream` pattern is shaped for one-server-many-agents fan-out, not many-server peer mesh — adopting it for peer mesh would require new lifecycle code (broken pipe detection, reconnect storms, half-open detection) that pull-based avoids entirely.
- **mDNS / multicast peer discovery.** Rejected on operational grounds — works on LAN, breaks across subnets, adds a multicast attack surface, and provides no auth signal until the CRDT verifies signatures anyway. Hub-seeded discovery achieves the same result without these constraints.
- **Required `tungol.peers:` operator-declared list.** Cleaner config model (everything in YAML, no dynamic discovery state) but doesn't scale past ~5 steorras without N×N edit friction at every membership change. Contradicts the peer-equal direction and reintroduces the static-config problem that the redesign is supposed to retire. Accepted as an *optional* supplement to hub-seeded discovery (above).
- **Generalize `server/swarm/manifest.go` into a shared CRDTStore now.** Rejected as scope creep. 2A doesn't need the generalization; the right time to evaluate is after 2B (consumed-tokens) and 2C (license attestation) land and the second + third consumers of the pattern make the generic surface obvious. Premature generalization with one consumer locks in shape decisions that may not fit the next consumer.
- **In-memory CRDT, no DB persistence.** Rejected because it doesn't fix Phase 1's restart-loses-state problem — that's one of the three structural issues 2A exists to address. DB persistence is non-negotiable.

## References

- ADR-008 (LWW-CRDT for swarm manifest) — borrowed CRDT shape; 2A introduces a separate type in `server/tungol/`, not a shared CRDTStore (see "Alternatives considered")
- ADR-025 (stockpile-in-gitback) — pre-Phase-0 design that gates the tungol redesign's Phase 0
- Phase 1: commit `55dc2dcd` (0.65.10, per-steorra identity + signed announcements + pin-on-first-sighting); commit `796c5ecd` (2026-05-18, Phase 1 integration test)
- Project memory: `project_tungol_rename_2026-05-16.md` (full design ratification, two scrutiny passes, 28 decisions)
