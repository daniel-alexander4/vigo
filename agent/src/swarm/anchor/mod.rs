// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! anchor — the puddle-rooted ownership/access primitive shared by every
//! swarm content subsystem that's "founded by a puddle, mutable by its members."
//!
//! Today's consumers:
//!
//! - **gitback** (ADR-014, ADR-015) — git-over-swarm projects, where the
//!   founder puddle keys `project_id = sha256(founder_pubkey ‖ handle)`,
//!   membership rides a stack of capability tokens, founder rotations bridge
//!   via the ADR-019 delegation chain.
//! - **curator** (ADR-024) — the P2P artifact registry, where the same
//!   founder-pubkey hashing keys `artifact_id`, the same delegation chain
//!   bridges catalog signatures across founder rekeys, and the same root-owned
//!   helper-daemon pattern gates publishing.
//! - **puddle name claims** (ADR-022) — the friendly-name claim envelopes
//!   reuse the delegation chain so a name follows the founder across rekeys.
//!
//! ### Module layout
//!
//! - [`tokens`] — capability-token membership: `Token`, `TokenBody`, `Role`,
//!   issue + child-of-issue, parent-signature chain verification.
//! - [`envelopes`] (slice 2 of the extract) — `DelegationEnvelope` +
//!   `canonical_delegation_bytes` + the `DELEGATION_*` consts. The
//!   gitback-specific revoke/leave envelopes stay in `swarm::gitback` since
//!   they're keyed to a git project's on-disk layout.
//! - [`peer_uid`] (slice 3 of the extract) — the SO_PEERCRED helper shared by
//!   every root helper daemon (gitback's, curator's, future consumers').
//!
//! ### Wire-compat note (byte-preserved domain strings)
//!
//! The signature-domain constants — `TOKEN_DOMAIN = b"gitback-token-v2\0"`,
//! `DELEGATION_DOMAIN = b"gitback-delegate-v2\0"` — are deliberately kept at
//! their pre-extraction byte values. The names trace back to when this code
//! lived inside `swarm::gitback`; relocating the modules to `anchor` does
//! **not** rename the wire strings. Every token + delegation signed in the
//! fleet today continues to verify after the move; an `anchor-token-v1`
//! rename would invalidate every existing puddle membership and force a
//! fleet-wide re-pair. The cost outweighed the cosmetic benefit
//! (see also Vigo's "no users yet, but no gratuitous wire breaks either"
//! stance — refactors should be reversible, not destructive).
//!
//! ### Why this layer wasn't extracted from day one
//!
//! Until curator landed (ADR-024, 0.51.0), gitback was the sole consumer; an
//! "anchor" with one user is a folder, not an abstraction. The 0.50.0 rename
//! flagged the seam and deferred extraction to the moment a second consumer
//! validated it. That happened with curator slices 1-5; this module is the
//! cleanup pass.

pub mod envelopes;
pub mod peer_uid;
pub mod tokens;
