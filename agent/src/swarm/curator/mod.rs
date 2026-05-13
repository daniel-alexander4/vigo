// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! curator — the fifth swarm content subsystem: a P2P artifact registry.
//!
//! filecast's content-addressed blob transport + gitback's puddle-keyed
//! identity/access machinery + a thin catalog layer (ADR-024). An *artifact*
//! is keyed `artifact_id = sha256(founder_puddle_pubkey ‖ name)` (same shape as
//! a gitback `project_id`, so Alice's `foo` ≠ Bob's `foo`); the human-facing
//! reference is `<founder-puddle-name>/<name>` (the puddle name resolves via the
//! ADR-022 fleet name map). Each artifact has one signed [`catalog::CatalogEntry`]
//! — versions × platforms + a mutable tag map — re-signed last-write-wins on every
//! mutation; the signature is the "writable only by puddle members" enforcement.
//!
//! Slice 1 (this commit): the catalog data model + signed-entry envelope
//! ([`catalog`]). The `swarm_curator` trait collector, the `curatormesh`
//! aggregator, the helper-daemon publish path, the server REST surface, the CLI,
//! and the UI land in later slices (see ADR-024).
//!
//! `catalog.rs` reuses [`crate::swarm::gitback::envelopes::DelegationEnvelope`]
//! (the ADR-019 rekey-bridge envelope) — the same cross-subsystem reuse
//! `crate::swarm::puddle::name` already does. When the shared puddle-rooted
//! ownership/access layer is eventually extracted (the `anchor` follow-up, see
//! `pending.md`), `DelegationEnvelope` moves there and gitback / puddle / curator
//! all import it from `anchor::`.

pub mod catalog;
