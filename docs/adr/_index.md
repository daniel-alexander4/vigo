---
title: Architecture Decision Records
weight: 90
---

# Architecture Decision Records

Short documents capturing significant design decisions and their rationale. Each ADR explains what was decided, why, and what alternatives were considered.

ADRs are immutable in their decision content. If a decision is reversed, write a new ADR that supersedes the old one rather than editing it. Terminology refreshes may be applied in-place with an `**Updated YYYY-MM-DD:**` note in the header that explains what changed — the decision and rationale stay frozen.

## Decisions

- [ADR-001: YAML-only config path](001-yaml-only-config.md) — no API/DB/CLI config writes
- [ADR-002: First-match-wins match block](002-first-match-wins.md) — hostname matching order
- [ADR-003: mTLS everywhere](003-mtls-everywhere.md) — no plaintext gRPC, ever
- [ADR-004: SQLite over Postgres](004-sqlite-over-postgres.md) — single-file database for simplicity
- [ADR-005: Rust agent, Go server](005-rust-agent-go-server.md) — language split rationale
- [ADR-006: HTMX-first web UI](006-htmx-first-ui.md) — server-rendered fragments over SPA
- [ADR-007: Pluggable secrets backend](007-pluggable-secrets.md) — provider interface, never plaintext
- [ADR-008: LWW-CRDT for swarm manifest](008-lww-crdt-manifest.md) — conflict resolution without coordination
- [ADR-009: Idempotent executors](009-idempotent-executors.md) — check-before-act, never blind apply
- [ADR-010: Convergence vs compliance terminology](010-convergence-vs-compliance.md) — distinct concepts, never crossed
- [ADR-011: Agent-driven swarm](011-agent-driven-swarm.md) — agents self-assign via manifest; server pushes manifest CRDT entries on check-in to bridge operator-initiated revokes to isolated envoys
- [ADR-012: Lockbox auto-encrypt-on-write](012-lockbox-auto-encrypt-on-write.md) — drop-and-forget UX, sweep-on-unlock for plaintext written while locked, three-class failure handling with `.failed` rename
- [ADR-013: Multi-member gitmirror](013-multi-member-gitmirror.md) — **superseded by ADR-015 before implementation**. Original capability-token + self-certifying-project-ID design layered alongside single-user gitmirror; absorbed and extended by the unified substrate
- [ADR-014: Puddle primitive](014-puddle-primitive.md) — per-user puddle identity (ed25519 keypair, passphrase-wrapped); peer-pair onboarding over swarm mTLS, manual scp as air-gapped fallback; foundation for ADR-015 and ADR-016
- [ADR-015: Unified gitmirror substrate](015-unified-gitmirror-substrate.md) — single substrate for personal-DR and multi-member git on top of puddle; orthogonal `members:` × `dr_fanout:` axes; helper-enforced access for non-root users (root retains direct filesystem access by design); plaintext-by-design — confidentiality lives in lockbox
- [ADR-016: Lockbox membership on puddle](016-lockbox-membership-on-puddle.md) — puddle-aware recipient resolution and onboarding; per-envoy age keys preserved for compartmentalization; `lockbox puddle *` / `lockbox standalone *` subtree split
- [ADR-017: No puddle-level revoke](017-no-puddle-level-revoke.md) — puddle-level eviction considered and rejected; shared puddle keypair makes revoke a placebo for adversarial removal, and cooperative removal already has a mechanism (`lockbox leave`); supersedes ADR-016's "eviction is puddle-level" line
- [ADR-018: Puddle rekey](018-puddle-rekey.md) — `rekey-pair` / `rekey` verbs preserve lockbox state across a puddle keypair rotation; 24h graceful window + 5-minute single-use claim codes; deliberately friction-bearing on each surviving envoy (no old-key chain-of-trust shortcut). Gitmirror founder-rekey picked up under ADR-019.
- [ADR-019: Gitmirror founder-rekey via anchor-preserving delegation chains](019-gitmirror-founder-rekey.md) — `project_id` stays anchored to the genesis founder pubkey forever; per-project `DelegationEnvelope` chain bridges retired founder pubkeys to the current signing identity; authority windows close the deposed-founder + antedated-token attacks; `re-invite-all` + `rejoin` mediate cross-puddle member rejoin via fresh OOB capability codes; `re-found` is the escape hatch when the chain hits cap. Five slices, shipped 0.35.18 → 0.35.22.
- [ADR-020: Lockbox is puddle-only](020-lockbox-puddle-only.md) — standalone mode retired in 0.40.0; supersedes ADR-016's standalone branch (recipient-resolution and per-envoy-key decisions stand). Mode 1 (encryption-at-rest, no peers) handled via single-envoy puddle. `WrapMode` enum + dual-mode unlock paths + pairwise-gossip recipient resolution all retire. No migration verb — operators with existing standalone enrollments scrub-and-re-enroll under puddle.
- [ADR-021: Lockbox lifecycle absorbed into puddle](021-lockbox-lifecycle-on-puddle.md) — sibling to ADR-020 in 0.40.0. Lockbox's per-capability lifecycle (`init`, `leave`, `forget <host>`) folds into puddle (`puddle init`/`join` auto-enroll, new `puddle leave`, new `puddle evict <host>`); lockbox subtree shrinks to content-only verbs. Per-envoy `lockbox: true` opt-in retired — lockbox mandatory on puddle membership. Gitmirror's project verbs unchanged (different granularity).
