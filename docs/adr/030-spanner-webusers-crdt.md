---
title: "ADR-030: Spanner webusers CRDT — cross-bolt user replication + session invalidation"
---

# ADR-030: Spanner webusers CRDT — cross-bolt user replication + session invalidation

**Status:** Approved — not yet implemented. Sequenced after ADR-026 (peer-equal admissions), which provides the gossip + signing substrate this slice extends.
**Date:** 2026-05-24. Direction ratified by Dan in the same session.

## Context

Each vigosrv keeps its own SQLite database, including its own `users` table (web users / API token owners) and its own SCS session store. In standalone mode this is fine — there is exactly one vigosrv per stacks. In spanner mode the bolts are peer-equal control planes for one fleet, but the `users` table is not replicated between them. Two operator-visible bugs follow:

1. **Stale role on demotion.** `vigocli webusers set-role --username alice --role viewer` against bolt A demotes alice in A's DB, and `auth.SessionManager.DestroyUserSessions(ctx, alice.ID)` (shipped 0.66.75) kills A's cached sessions immediately. Bolt B still has alice as admin in its DB. If alice is logged into B's web UI, B keeps serving the old role until the idle (15 min) or absolute (24 h) timeout fires. The hostile-insider story — Dan demotes the alleged hostile, the hostile retains admin on a peer bolt for the full timeout — is real.

2. **Account drift.** `vigocli webusers create --username alice` against A leaves B with no row for alice. She can authenticate against A only; alice's API tokens (created against A) don't validate against B even though both bolts are nominally one control plane. Operators silently develop per-bolt user lists.

Both manifest because the `users` table — unlike `spanner_bolts` (ADR-026) and unlike envoy / config / run / scrier state — has no replication mechanism between peers. The pending direction call (2026-05-24): replicate `users` via the spanner peer-equal CRDT layer, with session invalidation a *consequence* of the merge, not a separate signal.

## Decision

### Shape — extend ADR-026's CRDT substrate

The `users` table becomes a peer-equal LWW-Map keyed on `users.id`, riding the same Tier-1 multicast digest / Tier-2 REST snapshot pull the admissions roster already uses (`server/spanner/crdt.go`, `gossip.go`). Five concrete decisions:

**LWW comparator on `updated_at_ms INTEGER` (unix milliseconds), tiebreaker on `origin_bolt` pubkey-hex.** SQLite's `datetime('now')` is second-precision; spanner_bolts.admitted_at is already ms-since-epoch. Match that type. Same-ms ties (cosmically rare across federated bolts) break on lex-min pubkey-hex of the writing bolt — total order, deterministic, no flapping.

**Tombstone via `deleted_at INTEGER NULL` on the user row** (not a sibling G-Set table). Matches `spanner_bolts.revoked_at` exactly — same shape, same merge SQL pattern. Reads filter `WHERE deleted_at IS NULL`; the merge sees soft-deletes as just another LWW state; the FK cascade on `api_tokens` is preserved because hard-delete still happens locally once the tombstone replicates (peer cascades local tokens on accept of a tombstone row whose deleted_at is fresh). The G-Set alternative was rejected — separate table + UNIQUE(user_id) is more moving parts than one nullable column.

**Per-row origin-bolt signing.** Every user row carries `origin_bolt TEXT` (pubkey-hex of the bolt that last wrote it) and `signature BLOB` (ed25519 over the canonical content). Peers verify: (a) `origin_bolt` is a known *live* roster bolt (look up in local `spanner_bolts`); (b) signature verifies against that bolt's pubkey. Mirrors `MergeSnapshot`'s end-to-end verification on every roster row — a peer can forward signed user rows it received but cannot forge new ones. The signing key is the bolt's own identity (`server/bolt/`, the Ed25519 identity already used for admissions signing and Tier-1 gossip).

**Snapshot extension, not a new endpoint.** `Snapshot{Roster, ConsumedTokens, Users}` — the existing `GET /api/v1/spanner/snapshot` carries the third field, the existing Tier-2 pull merges it, the existing Tier-1 digest expands to cover the users hash. One wire format, one pull cadence, one signing/verify pattern. The withdrawn alternative — a sibling `/users-snapshot` endpoint — was rejected for the same reason ADR-026 picked one snapshot over per-CRDT endpoints: cycle-time and observability stay one event, not three.

**On-merge session invalidation, no separate broadcast.** This is the deliberate departure from the pending entry's literal three-piece prescription. The merge handler, after CRDT-applying a user row, inspects what changed: if `role` differs from the pre-merge row, or `deleted_at` flipped non-null, the handler calls local `auth.SessionManager.DestroyUserSessions(userID)`. The change-detect uses the pre-merge row read in the same transaction. Convergence latency for the destroy signal equals CRDT convergence latency — sub-30 seconds via Tier-1 fast-path, ≤5 min worst-case via Tier-2 ticker. This is intentionally slower than a peer-RPC immediate-broadcast would be; see Tradeoffs below.

### Migration story — pre-replication rows

Rows that exist in the `users` table when this slice ships have `updated_at_ms = 0` and `origin_bolt = ''` (the schema defaults). The local merge logic refuses to *export* unsigned rows (signature is required for outbound) but accepts them locally (`origin_bolt == ''` and `signature == nil` together are the "legacy" marker, treated as `updated_at_ms = 0` for merge purposes — any signed row beats them).

First-startup-post-upgrade: the local bolt re-signs every legacy row with its own identity, sets `origin_bolt` to its pubkey-hex, sets `updated_at_ms` to the current wall-clock. That's the implicit "this bolt claims authorship of these pre-replication rows" moment. If two bolts both have a pre-replication row for the same `users.id` (the typical case, since each bolt started its DB independently), the first-cycle merge picks the lex-min-pubkey writer; the loser's row is overwritten. **Operators with pre-existing webusers on multiple bolts must reconcile manually before upgrade** — documented as the migration-time hazard. For the home-lab single-vigosrv deployment this hazard never fires (no peers to conflict with at upgrade time).

### Slice plan

- **Slice 2-iii-a — ADR.** This document. No code.
- **Slice 2-iii-b — Schema + queries.** Migration adds `updated_at_ms`, `origin_bolt`, `deleted_at`, `signature` columns; sqlc queries updated; `DeleteUser` becomes the soft-delete UPDATE.
- **Slice 2-iii-c — Snapshot wire format + signing.** `UsersSnapshotRow` + `SignUserRow` / `VerifyUserRow` mirroring `RosterRow` + the admission-signing pattern. `Snapshot.Users` field. Server-side: `/api/v1/spanner/snapshot` packs `Users` from `ListAllUsers`.
- **Slice 2-iii-d — Merge + session invalidation.** `MergeUserLWW` SQL; `MergeUserRows` Go-side (pre-merge read for change-detect; post-merge `DestroyUserSessions` for role/deleted changes); wire it into `MergeSnapshot`.
- **Slice 2-iii-e — Tier-1 digest extension.** Combined digest covering admissions + users; Tier-1 fast-path fires on either change.
- **Slice 2-iii-f — Upgrade ergonomics.** First-startup re-sign of legacy rows; `vigocli webusers list` flags rows with `origin_bolt == ''` as "not yet replicated"; doctor check surfaces pre-replication state.

Each slice gets its own `/scrutinize` and PR. Slice 2-iii-b is the gate for 2-iii-c onward (schema must exist before signing).

## Consequences / tradeoffs

- **Sub-30-second session invalidation, not sub-second.** A peer-to-peer immediate-broadcast RPC would deliver session destruction in <100ms instead of leaning on the 30s Tier-1 multicast cycle. We chose the slower path because: (a) it adds zero new wire formats, (b) the worst case (30s instead of 100ms) is still 30× faster than today's idle timeout (15 min) and 2880× faster than the absolute timeout (24 h), (c) one signal path is simpler than two. A paranoid operator who needs sub-second guarantees can be served by a future Slice 2-iii-g that adds the RPC layered on top — the merge handler stays the source of truth; the RPC just front-runs it. Flagged here so the trade-off isn't a surprise.

- **Pre-existing webuser rows on multiple bolts conflict on first replication.** Documented in the migration section. The single-bolt home-lab case doesn't hit this; multi-bolt deployments that pre-date this slice will need a reconciliation pass before upgrade. There is no automatic merge — lex-min-pubkey wins, the other bolts' rows are overwritten.

- **Soft-delete changes `DeleteUser` semantics.** `vigocli webusers delete` no longer hard-removes the row; it sets `deleted_at` and replicates the tombstone. Operators who expected hard-delete (e.g., to free a username for re-use) need to know the username remains reserved by the tombstone unless explicitly purged. We don't add a `--purge` flag in v1 — defer to operator need.

- **`api_tokens` cascade requires a local hook on tombstone-accept.** SQLite's `ON DELETE CASCADE` doesn't fire on soft-deletes. The merge handler explicitly calls `DELETE FROM api_tokens WHERE user_id = ?` whenever it accepts a row whose `deleted_at` is fresh-non-null. Tokens are local-only, never replicated — each peer enforces its own cascade.

- **`oidc_subject` LWW conflict.** If alice has `oidc_subject = "claims:alice@example.com"` from a fresh OIDC login on bolt A, and bolt B independently re-issues a different subject during the same window, LWW picks one and discards the other. In practice this almost never happens (the OIDC provider issues one subject per principal), but it can if the IdP rotates. Documented; not mitigated in v1.

- **TOTP state replicates.** `totp_enabled` and `totp_recovery` are user-row fields; they replicate. An operator who enrolls TOTP on bolt A immediately replicates the secret to bolts B/C — necessary for cross-bolt TOTP to work at all. The TOTP secret is per-row; the existing in-table encryption (whatever it is today) needs to be a peer-portable shape, not bolt-local. Flagged as a Slice 2-iii-c concern: if `totp_recovery` is encrypted with a bolt-local key, replication must either (a) re-encrypt under a shared key (likely the ops-puddle from ADR-025) or (b) accept that TOTP enrollment doesn't replicate. To be resolved in Slice 2-iii-c.

## Implementation notes

- **Mirror the admissions merge closely.** `MergeSnapshot` in `server/spanner/crdt.go` is the template. `MergeUsers(ctx, q, sqlDB, snap.Users, knownBolts) (merged int, err error)` lives next to it; uses the same verify-then-merge-in-tx pattern. The pre-merge read for session-invalidation lives in `MergeUsers` (one extra `qtx.GetUserByID` per row that's about to be merged; cost is per-cycle and bounded by users-count).

- **Sign on every write, not just on replication.** `userStore.Insert/Update/SoftDelete` all sign the row before storing — that way the local store is always in the "replicatable" shape, no separate "prepare for replication" step. The `origin_bolt` field is set to the local bolt's pubkey-hex on every write the local bolt initiates.

- **Audit events.** Reuse the existing `webusers.*` event namespace; add `webusers.replicated_from` (peer A received user X from peer B at time T) and `webusers.tombstone_propagated` (peer accepted a tombstone for user X). These help operators trace the convergence of a demotion through the fleet.

- **Tests.** Slice 2-iii-d's MergeUsers gets the same unit-test pattern as MergeSnapshot — fake users.Snapshot, fake roster (for signing verification), fake SessionManager (asserts DestroyUserSessions called with the right userID). Slice 2-iii-e's combined digest gets a test that flipping a user row changes the digest, mirroring the existing `RosterDigest` tests.

- **No new gRPC RPCs.** Like ADR-026, this slice stays on multicast + REST. The "destroy-broadcast" path explicitly does NOT introduce a peer-RPC; if added later, it's a Slice 2-iii-g additive optimization.

## Cross-references

- **ADR-026**: `docs/adr/026-spanner-peer-equal-admissions.md` — provides the CRDT substrate this slice extends (Tier-1 multicast, Tier-2 REST, snapshot wire format, per-row signing, gossip merge).
- **0.66.75 auth ship** — commit `b1d71c7b` shipped the local-only `auth.SessionManager.DestroyUserSessions` that this slice extends to the cross-bolt case. Comment in `server/auth/session.go` notes the spanner-mode gap; that gap is what this ADR closes.
- **Pending entry** — `~/.claude/projects/-home-dan-repos-vigo/memory/pending.md` "Spanner — cross-bolt session invalidation on role change" — moves to completed when Slice 2-iii-f ships. Direction-call: 2026-05-24.
