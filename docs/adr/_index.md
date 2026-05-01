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
- [ADR-012: Cryptsync auto-encrypt-on-write](012-cryptsync-auto-encrypt-on-write.md) — drop-and-forget UX, sweep-on-unlock for plaintext written while locked, three-class failure handling with `.failed` rename
- [ADR-013: Multi-member gitmirror](013-multi-member-gitmirror.md) — **superseded by ADR-015 before implementation**. Original capability-token + self-certifying-project-ID design layered alongside single-user gitmirror; absorbed and extended by the unified substrate
- [ADR-014: Self-fleet primitive](014-self-fleet-primitive.md) — per-user fleet identity (ed25519 keypair, passphrase-wrapped); peer-pair onboarding over swarm mTLS, manual scp as air-gapped fallback; foundation for ADR-015 and ADR-016
- [ADR-015: Unified gitmirror substrate](015-unified-gitmirror-substrate.md) — single substrate for personal-DR and multi-member git on top of self-fleet; orthogonal `members:` × `dr_fanout:` axes; helper-enforced access for non-root users (root retains direct filesystem access by design); plaintext-by-design — confidentiality lives in cryptsync
- [ADR-016: Cryptsync membership on self-fleet](016-cryptsync-membership-on-self-fleet.md) — fleet-aware recipient resolution and onboarding; per-envoy age keys preserved for compartmentalization; `cryptsync fleet *` / `cryptsync standalone *` subtree split
- [ADR-017: No fleet-level revoke](017-no-fleet-level-revoke.md) — fleet-level eviction considered and rejected; shared fleet keypair makes revoke a placebo for adversarial removal, and cooperative removal already has a mechanism (`cryptsync leave`); supersedes ADR-016's "eviction is fleet-level" line
- [ADR-018: Self-fleet rekey](018-self-fleet-rekey.md) — `rekey-pair` / `rekey` verbs preserve cryptsync state across a fleet keypair rotation; 24h graceful window + 5-minute single-use claim codes; deliberately friction-bearing on each surviving envoy (no old-key chain-of-trust shortcut); gitmirror founder-rekey deferred to a future ADR
