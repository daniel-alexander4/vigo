// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user self-fleet paths.
//!
//! All user-side state lives under `~/.vigo-fleet/`:
//!
//! - `identity.wrapped` — Ed25519 private key wrapped under the
//!   user's passphrase. Same envelope as cryptsync's `identity.age`
//!   (1 byte version + 24 byte XChaCha20-Poly1305 nonce + ciphertext
//!   + 16 byte auth tag). 0600 user-owned. Portable across envoys.
//! - `identity.salt` — 16 random bytes, Argon2id input. 0600.
//! - `identity.pub` — the fleet pubkey in `ed25519:<hex>` form, one
//!   line. 0644 (already public). Sibling subsystems read this to
//!   query "is this envoy in dan's fleet, and what's the pubkey?"
//! - `pair.pending` — sentinel dropped by the `pair` CLI verb when
//!   a pairing code has been issued. Picked up by the daemon's
//!   swarm-server pair-claim handler. JSON shape: see
//!   `PairPendingSpec` in `pair.rs`. Removed on first successful
//!   claim or after the 5 min TTL elapses.
//! - `session.unlocked` — sentinel containing the session helper's
//!   PID, written by the helper at startup, removed at shutdown.
//!   Used by sibling subsystems to detect "is the fleet identity
//!   currently unlocked on this envoy?" without having to talk to
//!   the helper. Same pattern cryptsync uses (ADR-012).

use std::path::{Path, PathBuf};

/// `~/.vigo-fleet/` — the per-user self-fleet state dir.
pub fn dot_dir(home: &Path) -> PathBuf {
    home.join(".vigo-fleet")
}

pub fn wrapped_path(home: &Path) -> PathBuf {
    dot_dir(home).join("identity.wrapped")
}

pub fn salt_path(home: &Path) -> PathBuf {
    dot_dir(home).join("identity.salt")
}

pub fn pubkey_path(home: &Path) -> PathBuf {
    dot_dir(home).join("identity.pub")
}

pub fn pair_pending_path(home: &Path) -> PathBuf {
    dot_dir(home).join("pair.pending")
}

/// Single-use sentinel for the rekey claim code (ADR-018). Same shape
/// as `pair.pending` but lives at a separate path so a rekey-in-progress
/// session and an initial-pair session don't trample each other.
pub fn rekey_pending_path(home: &Path) -> PathBuf {
    dot_dir(home).join("rekey.pending")
}

/// Local log of fleet keypairs this envoy has retired, including the
/// active rekey session's 24h graceful window (ADR-018). JSON list
/// of `RetiredFleetEntry` records. 0600 user-owned.
pub fn retired_fleets_path(home: &Path) -> PathBuf {
    dot_dir(home).join("retired_fleets.json")
}

pub fn session_unlocked_path(home: &Path) -> PathBuf {
    dot_dir(home).join("session.unlocked")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn paths_are_under_dot_vigo_fleet() {
        let home = PathBuf::from("/home/alice");
        assert_eq!(dot_dir(&home), PathBuf::from("/home/alice/.vigo-fleet"));
        assert_eq!(
            wrapped_path(&home),
            PathBuf::from("/home/alice/.vigo-fleet/identity.wrapped")
        );
        assert_eq!(
            pubkey_path(&home),
            PathBuf::from("/home/alice/.vigo-fleet/identity.pub")
        );
        assert_eq!(
            pair_pending_path(&home),
            PathBuf::from("/home/alice/.vigo-fleet/pair.pending")
        );
    }
}
