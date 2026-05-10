// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Puddle — per-user puddle identity primitive (ADR-014).
//!
//! A "puddle" is one user's collection of envoys, treated as a
//! single trust unit by subsystems that consume it. The puddle is
//! identified by a single Ed25519 keypair generated locally on the
//! first envoy where the user runs `vigo swarm puddle init`. The
//! private half is wrapped under the user's passphrase using the
//! same Argon2id + XChaCha20-Poly1305 envelope lockbox uses for
//! its age identity (see [`super::lockbox::crypt::wrap_signing_key`]).
//!
//! Membership of an envoy in a puddle is local: the wrapped
//! private blob lives at `~/.vigo-puddle/identity.wrapped`, the
//! per-envoy salt at `~/.vigo-puddle/identity.salt`. The agent
//! advertises the puddle pubkey to peers as part of swarm gossip;
//! there is no central puddle-membership registry.
//!
//! Distribution between envoys uses **peer-pair**: an already-active
//! envoy issues a 5-minute single-use pairing code; a new envoy
//! presents the code over swarm mTLS (port 1531) and receives the
//! wrapped blob. The passphrase never leaves the destination envoy.
//! Manual scp survives as the air-gapped fallback.
//!
//! Phase 1 scope: Linux + macOS. No multi-puddle (one puddle per
//! user). No Shamir / escrow recovery (re-init from scratch on
//! passphrase loss). See ADR-014 for the resolved scope gates.
//!
//! Module shape:
//! - `config.rs` — paths under `~/.vigo-puddle/` and the daemon-owned
//!   per-user state dir.
//! - `cli.rs` — `vigo swarm puddle {init,pair,join,unlock,lock,
//!   pubkey,members,rotate-passphrase}`.
//! - `pair.rs` — pairing-code generation + the HTTP claim handler
//!   that runs in the daemon's swarm-server context.
//! - `session.rs` — `setsid`-detached per-user session helper
//!   holding the unwrapped Ed25519 SigningKey in memory.

pub mod cli;
pub mod codes;
pub mod config;
pub mod name;
pub mod names_bootstrap;
pub mod names_cache;
pub mod pair;
pub mod rekey;
pub mod session;

use std::path::Path;

use anyhow::Result;

/// Read the local envoy's puddle pubkey if it has been initialized
/// for this user. Returns `Ok(Some(pubkey_hex))` when an
/// `identity.pub` is present, `Ok(None)` when the user hasn't
/// initialized puddle yet, `Err(_)` on read failure.
///
/// Used by sibling subsystems (swarmgit under ADR-015, lockbox
/// membership under ADR-016) to query "is this user puddle-enabled
/// here?" without having to touch the wrapped blob.
pub fn local_puddle_pubkey(home: &Path) -> Result<Option<String>> {
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
/// puddle on this envoy (wrapped blob + salt + pubkey present).
pub fn is_initialized(home: &Path) -> bool {
    config::wrapped_path(home).exists()
        && config::salt_path(home).exists()
        && config::pubkey_path(home).exists()
}

/// Read the local user's friendly-name claim (ADR-022), if any.
/// `Ok(None)` when no name has been claimed on this envoy; `Err` on a
/// present-but-corrupt `name-claim.json`. Thin wrapper over
/// [`name::NameClaim::load`] for siblings (the `swarmgit` traits
/// collector) that just want "does this user have a name claim?".
pub fn local_name_claim(home: &Path) -> Result<Option<name::NameClaim>> {
    name::NameClaim::load(home)
}
