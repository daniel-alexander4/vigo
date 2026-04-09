---
title: Architecture Decision Records
weight: 90
---

# Architecture Decision Records

Short documents capturing significant design decisions and their rationale. Each ADR explains what was decided, why, and what alternatives were considered.

ADRs are immutable once written. If a decision is reversed, write a new ADR that supersedes the old one rather than editing it.

## Decisions

- [ADR-001: YAML-only config path](001-yaml-only-config.md) — no API/DB/CLI config writes
- [ADR-002: First-match-wins node mapping](002-first-match-wins.md) — hostname matching order
- [ADR-003: mTLS everywhere](003-mtls-everywhere.md) — no plaintext gRPC, ever
- [ADR-004: SQLite over Postgres](004-sqlite-over-postgres.md) — single-file database for simplicity
- [ADR-005: Rust agent, Go server](005-rust-agent-go-server.md) — language split rationale
- [ADR-006: HTMX-first web UI](006-htmx-first-ui.md) — server-rendered fragments over SPA
- [ADR-007: Pluggable secrets backend](007-pluggable-secrets.md) — provider interface, never plaintext
- [ADR-008: LWW-CRDT for swarm manifest](008-lww-crdt-manifest.md) — conflict resolution without coordination
- [ADR-009: Idempotent executors](009-idempotent-executors.md) — check-before-act, never blind apply
- [ADR-010: Convergence vs compliance terminology](010-convergence-vs-compliance.md) — distinct concepts, never crossed
