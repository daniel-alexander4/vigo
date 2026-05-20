---
title: "ADR-026: Peer-equal admissions over multicast-gossiped LWW-Map (tungol Slice 2A)"
---

# ADR-026: Peer-equal admissions over multicast-gossiped LWW-Map (tungol Slice 2A)

**Status:** Approved — not yet implemented. Slice 2A of the tungol redesign Phase 2 (see `docs/internal.docs/plans/vigo-plan-tungol-redesign-2026-05-16.md`).
**Date:** 2026-05-19. Supersedes the withdrawn 2026-05-18 version (commit `05f95938`) — that draft diverged from the canonical plan on 8 substantive axes and is replaced by a faithful transcription of the plan plus the 5 open-question decisions ratified today.

## Context

Phase 1 of the tungol redesign (shipped 0.65.10, commit `55dc2dcd`) gave each steorra an Ed25519 identity, signed `SteorraStatusReport` announcements, and an in-memory `Hub.steorraPins` map for pin-on-first-sighting verification. The pins are observer-only — the hub records them, but membership decisions (which steorras can speak for which hostname patterns, who's allowed to write to the tungol's roster) still flow through the hub as a singleton write authority.

Phase 2 retires that pattern. Membership becomes peer-equal — every steorra holds the same admission roster, every steorra can write to it under signature, and the hub's write-authority role is replaced by a CRDT that converges across steorras without any one of them being primary. The Phase 1 `steorraPins` map and the hub's role as the membership write authority are both consequences of the pre-redesign hub-spoke shape; Slice 2A is the step that retires the singleton-write assumption while keeping the hub as a bootstrap-discovery rendezvous (the hub retires fully at Phase 3 per the plan's slice ordering).

The withdrawn 2026-05-18 draft of this ADR proposed a different shape — a pull-based snapshot RPC, a composite `(tungol_id, steorra_id)` table key, split admissions / consumed-tokens slices, and the hub retained as a gRPC rendezvous in 2A. The canonical plan (ratified + scrutinized twice on 2026-05-16) settled on a different design: multicast gossip for Tier-1 counters with a REST endpoint for Tier-2 bytes, a `pubkey`-keyed `tungol_steorras` table with revoke-wins semantics, and admissions atomically coupled to the `consumed_tokens` G-Set via FK constraint. This ADR re-issues against the plan.

## Decision

### Scope — Slice 2A only

Slice 2A delivers the peer-equal admissions CRDT and the transport that gossips it. Out of 2A scope (separate slices, separate ADRs if any):

- **Slice 2B** — `consumed_tokens` G-Set lifecycle beyond admissions-coupling: explicit revocation flows, vigocli `invite`/`approve`/`revoke`/`leave` verbs.
- **Slice 2C** — License-attestation observability defense.
- **Slice 2D** — Full multi-vigosrv test harness (the Phase A/B/C test-harness refactor at `796c5ecd` is foundation only; the multi-`*Server` fixture is built when Phase 2 code lands).
- **Phase 3** — hub retirement (`RoleHub`/`RoleSpoke`/`RoleStandalone` removal, hard-error on `role:` field, `spanner:` YAML key retirement).

The plan is the canonical source for everything below — this ADR doesn't restate it. The decisions section names the architectural shape and points at the plan for the full spec.

### Architectural shape (transcribed from the plan)

- **`tungol_steorras` table.** Keyed by `pubkey` (the steorra's Ed25519 pubkey from Phase 1). Revoke-wins semantics via nullable `revoked_at`/`revoked_by` columns — pubkey rotation is forbidden (rotation = revoke + new admission with a new pubkey). LWW arbitration uses `(updated_at, updated_by)` for non-revoke fields.
- **`consumed_tokens` G-Set.** UNIQUE on `consumed_token_hash`. Admissions reference `consumed_tokens` via FK; admission insert + token consumption land as one transaction. The two CRDTs cannot be sliced apart — that constraint is why the withdrawn ADR's 2A/2B split was wrong.
- **Transport — two tiers.** Tier-1 counters gossip on multicast group `224.0.0.45:1534` (new group; sibling to longdrawer `1532` and lockbox `1533`) every 30s. Tier-2 roster bytes serve over `GET /api/v1/tungol/snapshots/<pubkey>/<seq>` on the existing 8443 REST surface; pulled on counter-mismatch every 5m or on first-sighting from a new peer.
- **No new gRPC RPCs.** The plan's transport is multicast + REST, not gRPC. (The withdrawn ADR introduced `PullTungolSnapshot` as a new admin gRPC; this is dropped.)
- **`JoinTungol` admission ceremony.** New gRPC RPC (admin-side, not the agent stream). The founding steorra issues an invite bundle (one-time token + tungol metadata + bootstrap addresses); the joining steorra presents the token to any reachable peer via `JoinTungol`; the peer verifies the token isn't in `consumed_tokens`, atomically (a) inserts the admission row and (b) adds the token hash to the G-Set, and (c) signs the new row with its own steorra identity. The new steorra learns peers from the response and from subsequent gossip.
- **Audit events.** Per the plan: `tungol_id_initialized`, `tungol_join_offered`, `tungol_join_accepted`, `tungol_join_rejected`, `tungol_steorra_admitted`, `tungol_steorra_revoked`, license-attestation events (deferred to 2C, but reserve the namespace). Plus `tungol_pattern_overlap_detected` from the Resolved Question 1 below.
- **`server.yaml` shape.** Phase 2 replaces the existing `tungol.*` keys wholesale with `mode` (`founder|joiner|standalone`), `tungol_id`, `patterns`, `snapshot`, `transport`, `freshness`, `fallback_admin`. The loader hard-errors on the retired fields (`hub_addr`, `hub_fallback_addrs`, `spokes`, `auto_failover`, `role`) with a one-line migration pointer. The hub still runs as a bootstrap rendezvous in 2A, but it's configured as a peer with `mode: founder` and the same membership shape as everyone else.

### Resolved open questions (2026-05-19)

The plan's "Open questions" list at lines 833–854 had 5 items still open. All 5 ratified today:

1. **Pattern conflict policy across steorras — refuse, layered.** Three enforcement points: (a) pre-publish validation in `vigocli config publish` refuses to publish a steorra config whose patterns overlap with any other steorra in the same tungol; (b) load-time refusal — a steorra refuses to boot when its DB-persisted membership (post-CRDT-merge) shows pattern overlap with another live member; (c) mid-flight gossip-introduced overlap emits `tungol_pattern_overlap_detected` audit event + surfaces in `doctor` and the UI. Refuse-and-loud beats warn-and-arbitrate because silent partition makes "which steorra owns this envoy" unprovable, violating Vigo's "honest enforcement claims" principle.

2. **Join token TTL — 24h default + `--ttl <duration>` override.** Default tuned for next-day setup; override flag on `vigocli tungol steorra invite` for the MSP-vendor / scheduled-maintenance-window case (`--ttl 7d`). Orthogonal to the `consumed_tokens` G-Set one-shot enforcement: G-Set guarantees a token is only ever consumed once, TTL bounds the unconsumed-token exposure window. Belt and suspenders.

3. **`vigocli tungol steorra invite` output — stdout default + `--out <path>` opt-in + stderr warning when stdout is a TTY.** Matches existing vigocli credential-output convention (`secrets reveal`, `tokens generate`), preserves pipe composition (`invite ... | ssh peer 'vigocli tungol steorra join --from -'`), and the TTY warning catches the paste-accident risk window without adding friction to the redirected-output workflow. The 24h TTL bounds the leak-and-forget failure mode.

4. **Founding-steorra `init` — require `tungol_id` pre-set in `server.yaml`.** `vigocli tungol init` does NOT auto-write to `server.yaml`. Operator workflow: edit `server.yaml` (add `tungol.mode: founder`, `tungol.tungol_id: <name>`, other tungol fields) → restart vigosrv → run `vigocli tungol init`. Matches the source-of-truth rule across the rest of vigocli (`config publish`, `secrets init`, `webusers create`, `backup`) — commands consume YAML-declared state, never mutate it. Running `init` with `tungol_id` absent emits an actionable error naming the missing field.

5. **Steorra-hostname re-announcement — restart only, no rename verb in v1.** Hostname is identity-tied (cert SANs, mTLS, pinned `(tungol_id, steorra_id) → pubkey` records). A `vigocli tungol steorra rename` verb that mutates live announcement state either silently rewrites `server.yaml` (Item 4 anti-pattern) or diverges from it until the next restart "flickers" the rename back. Renames are rare → convenience savings minimal → cost of the parallel mechanism paid forever. Operator edits `server.yaml`, restarts, gossip picks up the new hostname on the next cycle (≤30s). YAGNI for v1; add the verb when a concrete operator need surfaces.

## Consequences / tradeoffs

- **Slice 2A cannot ship without Slice 2B's `consumed_tokens` G-Set.** The plan's FK-coupling between admissions and consumed tokens is structural; the original ADR's split-slice assumption was wrong. Effort estimate from the plan stays correct (Phase 2 = ~3–4 weeks for admissions CRDT + vigocli + license attestation + test infrastructure), but the internal slicing must honor the coupling.
- **New multicast group `224.0.0.45:1534`.** Sits alongside longdrawer (`224.0.0.43:1532`) and lockbox (`224.0.0.44:1533`). Operators on networks that block multicast for swarm subsystems will need to extend their network policy to cover the new group. Documented in the plan's `transport:` config section.
- **Hub retained in 2A as a bootstrap rendezvous, not a write authority.** Distinct from the "kept as primary" shape; the hub answers `JoinTungol` requests at first contact and is one peer among many in gossip. Phase 3 retires the hub-role distinction entirely.
- **Phase 1 `steorraPins` map is superseded.** The in-memory pin table becomes a query against the DB-persisted `tungol_steorras` table. Pin-on-first-sighting still happens — at admission time rather than at announcement-receive time. Existing pin integration test (`796c5ecd`) needs an analog covering the new flow.
- **`server.yaml` migration is hard-error, no soft transition.** Operators on pre-Phase-2 deployments edit their `server.yaml` to the new shape during the upgrade window. The loader's one-line migration pointer names the retired field and its replacement. Matches the broader tungol-redesign hybrid-refusal stance (security invariant #8 in the plan).
- **The withdrawn `PullTungolSnapshot` admin gRPC is not added.** The plan's transport is multicast + REST. No new admin gRPC for snapshot pulls — REST handles it on existing 8443.

## Implementation notes

- **Multi-vigosrv test harness lands with this slice.** Slice 2D's "full multi-vigosrv test harness" is the deferral target for *exhaustive* multi-peer coverage; basic two-`*Server` integration tests covering CRDT replication, admission/revoke ordering, and pattern-overlap refusal land alongside Slice 2A code. The Phase A/B/C refactor (`d5c0b4e4`, `ab859797`, `796c5ecd`) is the foundation those tests build on.
- **`tungol_steorras` table migration is in-place per the zero-users rule.** Phase 1's any-shape `steorra_pins` storage gets replaced; no incremental migration files, no parallel-table shim, no version negotiation.
- **CRDT merge logic lives at `server/tungol/crdt.go` or similar.** Mirror the existing `server/swarm/manifest.go` LWW-Map semantics — same arbitration shape (`updated_at`, `updated_by`), same signature verification pattern. Don't reimplement the merge primitive; lift the helper if generalization is warranted, otherwise duplicate the small piece needed.
- **Audit events get fresh slot in `server/audit/`.** No new event-type registry — add the seven new event names to the existing enum.

## Cross-references

- **Canonical plan**: `docs/internal.docs/plans/vigo-plan-tungol-redesign-2026-05-16.md` — single source of truth for the tungol redesign; this ADR cites without restating.
- **ADR-025**: `docs/adr/025-stockpile-in-gitback.md` — gates Phase 0; shipped 0.65.8/0.65.9 (Slices 3+4).
- **Phase 1 implementation**: commit `55dc2dcd` (0.65.10) — per-steorra Ed25519 identity + signed announcements + pin-on-first-sighting. Pin-on-first-sighting integration test at `796c5ecd`.
- **Withdrawn ADR-026 (2026-05-18)**: commit `31aba7a1` (added) + `05f95938` (withdrawn). Replaced by this document.
