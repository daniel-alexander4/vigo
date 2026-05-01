// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Self-fleet — per-user fleet identity primitive (ADR-014).
//!
//! A "self-fleet" is one user's collection of envoys, treated as a
//! single trust unit by subsystems that consume it. The fleet is
//! identified by a single Ed25519 keypair generated locally on the
//! first envoy where the user runs `vigo swarm self-fleet init`. The
//! private half is wrapped under the user's passphrase using the
//! same Argon2id + XChaCha20-Poly1305 envelope cryptsync uses for
//! its age identity (see [`super::cryptsync::crypt::wrap_signing_key`]).
//!
//! Membership of an envoy in a self-fleet is local: the wrapped
//! private blob lives at `~/.vigo-fleet/identity.wrapped`, the
//! per-envoy salt at `~/.vigo-fleet/identity.salt`. The agent
//! advertises the fleet pubkey to peers as part of swarm gossip;
//! there is no central fleet-membership registry.
//!
//! Distribution between envoys uses **peer-pair**: an already-active
//! envoy issues a 5-minute single-use pairing code; a new envoy
//! presents the code over swarm mTLS (port 1531) and receives the
//! wrapped blob. The passphrase never leaves the destination envoy.
//! Manual scp survives as the air-gapped fallback.
//!
//! Phase 1 scope: Linux + macOS. No multi-fleet (one fleet per
//! user). No Shamir / escrow recovery (re-init from scratch on
//! passphrase loss). See ADR-014 for the resolved scope gates.
//!
//! Module shape:
//! - `config.rs` — paths under `~/.vigo-fleet/` and the daemon-owned
//!   per-user state dir.
//! - `cli.rs` — `vigo swarm self-fleet {init,pair,join,unlock,lock,
//!   pubkey,members,rotate-passphrase}`.
//! - `pair.rs` — pairing-code generation + the HTTP claim handler
//!   that runs in the daemon's swarm-server context.
//! - `session.rs` — `setsid`-detached per-user session helper
//!   holding the unwrapped Ed25519 SigningKey in memory.

pub mod cli;
pub mod codes;
pub mod config;
pub mod pair;
pub mod rekey;
pub mod session;

use std::path::Path;

use anyhow::Result;

/// Read the local envoy's fleet pubkey if it has been initialized
/// for this user. Returns `Ok(Some(pubkey_hex))` when an
/// `identity.pub` is present, `Ok(None)` when the user hasn't
/// initialized self-fleet yet, `Err(_)` on read failure.
///
/// Used by sibling subsystems (gitmirror under ADR-015, cryptsync
/// membership under ADR-016) to query "is this user fleet-enabled
/// here?" without having to touch the wrapped blob.
pub fn local_fleet_pubkey(home: &Path) -> Result<Option<String>> {
    let path = config::pubkey_path(home);
    match std::fs::read_to_string(&path) {
        Ok(s) => {
            let trimmed = s.trim();
            if trimmed.is_empty() {
                return Ok(None);
            }
            Ok(Some(trimmed.to_string()))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e.into()),
    }
}

/// Convenience predicate: true iff the user has initialized
/// self-fleet on this envoy (wrapped blob + salt + pubkey present).
pub fn is_initialized(home: &Path) -> bool {
    config::wrapped_path(home).exists()
        && config::salt_path(home).exists()
        && config::pubkey_path(home).exists()
}
