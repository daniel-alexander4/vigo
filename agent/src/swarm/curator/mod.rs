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
//! Slice 1: the catalog data model + signed-entry envelope ([`catalog`]) + the
//! `swarm_curator` trait collector + the `curatormesh` aggregator. Slice 2: the
//! `swarm.curator.{enabled,max_bytes}` config + usercrate `curator:` flag +
//! [`SwarmRuntimeLimits::curator_max_bytes`][crate::proto::SwarmRuntimeLimits]
//! plumbing (this commit); the helper-daemon publish path follows in slice 2b.
//! The server REST surface, the CLI, and the UI land in slices 3-5 (see ADR-024).
//!
//! `catalog.rs` reuses [`crate::swarm::gitback::envelopes::DelegationEnvelope`]
//! (the ADR-019 rekey-bridge envelope) — the same cross-subsystem reuse
//! `crate::swarm::puddle::name` already does. When the shared puddle-rooted
//! ownership/access layer is eventually extracted (the `anchor` follow-up, see
//! `pending.md`), `DelegationEnvelope` moves there and gitback / puddle / curator
//! all import it from `anchor::`.

pub mod catalog;

/// Per-blob (one `{name, version, os, arch}` artifact) size cap when the
/// operator hasn't published `swarm.curator.max_bytes` yet, or left it at
/// 0/unset — 2 GiB, matching the post-0.49.3 gitback bundle default
/// ([`crate::swarm::gitback::DEFAULT_MAX_BUNDLE_SIZE`]). Read at the publish
/// gate via [`crate::swarm::runtime_limits::curator_max_bytes`]; raise the
/// server-side knob for large container images.
pub const DEFAULT_MAX_BYTES: u64 = 2 * 1024 * 1024 * 1024;
