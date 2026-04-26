// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Trust-on-first-use (TOFU) cache of `(hostname → Ed25519 pubkey)` for
//! verifying gitmirror multicast announcement signatures.
//!
//! ## Model
//!
//! Each gitmirror-signed announcement embeds the sender's Ed25519 verifying
//! key (hex-encoded) in its `GITMIRROR:` line and appends a `SIG:` line
//! covering the rest of the packet. Receivers:
//!
//! 1. Extract the pubkey and signature from the packet.
//! 2. Look up `hostname → pubkey` in this cache.
//! 3. If absent: cache the embedded pubkey (TOFU — first announcement
//!    wins). Verify the signature against it; on success, accept the
//!    packet and persist the cache.
//! 4. If present and the embedded pubkey matches: verify and accept.
//! 5. If present and the embedded pubkey differs: log WARN and drop
//!    the packet's gitmirror content. Same behavior if signature fails
//!    verification under the cached pubkey.
//!
//! Admin recovery: delete `/var/lib/vigo/swarm/gitmirror/peer_pubkeys.json` to
//! clear the cache after a re-enrollment. A fresh cache will
//! TOFU-accept the new pubkeys.
//!
//! ## Threat coverage
//!
//! | Attack                                          | Blocked? |
//! |-------------------------------------------------|----------|
//! | Spoof `GITMIRROR:` from untrusted hostname          | Yes — signature check fails (attacker doesn't have the real envoy's private key). |
//! | Replay an old, valid announcement               | Yes — signature covers the emission timestamp. |
//! | Race the real envoy to first announcement       | Partial — TOFU caches the attacker's key. Operator notices missing gitmirror flow and clears cache. |
//! | Rotate pubkey (legitimate re-enrollment)        | Requires a cache clear — documented as an admin action. |
//!
//! First-contact race is acceptable for the current threat model
//! (same-LAN attacker). If a stronger guarantee is needed, a follow-up
//! ships server-distributed pubkeys so the cache can be bootstrapped
//! out-of-band.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Mutex;
use tracing::{debug, info, warn};

/// Cache file on disk. Root-owned, 0600. Reload on every startup;
/// persist on every accepted mutation.
pub fn cache_path() -> PathBuf {
    PathBuf::from("/var/lib/vigo/swarm/gitmirror/peer_pubkeys.json")
}

/// Wire-format for the cache file. One entry per known hostname.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
struct Disk {
    version: u32,
    entries: HashMap<String, Entry>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct Entry {
    pubkey_hex: String,
    first_seen_unix: i64,
    last_seen_unix: i64,
}

/// In-memory TOFU cache.
pub struct PeerPubkeyCache {
    inner: Mutex<Disk>,
}

/// Result of a lookup. Drives the receiver's accept/reject decision.
#[derive(Debug, PartialEq, Eq)]
pub enum CheckOutcome {
    /// Unknown hostname — first sighting. Cache the pubkey; verify
    /// the packet against it. Accept on success.
    FirstSighting,
    /// Known hostname and the embedded pubkey matches the cached
    /// value. Verify + accept as usual.
    Match,
    /// Known hostname but the embedded pubkey differs from cache.
    /// Reject — likely a spoof or a rotated key that hasn't been
    /// cleared by an admin.
    Mismatch { cached_prefix: String },
}

impl PeerPubkeyCache {
    /// Empty cache — used by tests.
    pub fn empty() -> Self {
        Self {
            inner: Mutex::new(Disk::default()),
        }
    }

    /// Load from disk. Missing or malformed files are treated as empty
    /// (the cache will TOFU-repopulate on first announcement from each
    /// peer). Never fails at startup.
    pub fn load_or_empty() -> Self {
        let path = cache_path();
        let bytes = match std::fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                return Self::empty();
            }
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "gitmirror: could not read peer pubkey cache, starting empty",
                );
                return Self::empty();
            }
        };
        match serde_json::from_slice::<Disk>(&bytes) {
            Ok(disk) => {
                info!(
                    path = %path.display(),
                    entries = disk.entries.len(),
                    "gitmirror: loaded peer pubkey cache",
                );
                Self {
                    inner: Mutex::new(disk),
                }
            }
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "gitmirror: peer pubkey cache is malformed, starting empty",
                );
                Self::empty()
            }
        }
    }

    /// Decide how to treat an announced `(hostname, pubkey_hex)` tuple.
    /// Does NOT mutate — caller observes the outcome, verifies the
    /// packet signature, then calls `accept` on success to record the
    /// sighting.
    pub fn check(&self, hostname: &str, pubkey_hex: &str) -> CheckOutcome {
        let disk = self.inner.lock().expect("pubkey cache poisoned");
        match disk.entries.get(hostname) {
            None => CheckOutcome::FirstSighting,
            Some(e) if e.pubkey_hex == pubkey_hex => CheckOutcome::Match,
            Some(e) => CheckOutcome::Mismatch {
                cached_prefix: e.pubkey_hex.chars().take(12).collect(),
            },
        }
    }

    /// Record a successful sighting. On first sighting, creates the
    /// entry; on repeat, refreshes `last_seen_unix`. Persists to disk.
    pub fn accept(&self, hostname: &str, pubkey_hex: &str, now: i64) {
        let mut disk = self.inner.lock().expect("pubkey cache poisoned");
        disk.version = 1;
        let entry = disk
            .entries
            .entry(hostname.to_string())
            .or_insert_with(|| Entry {
                pubkey_hex: pubkey_hex.to_string(),
                first_seen_unix: now,
                last_seen_unix: now,
            });
        entry.last_seen_unix = now;
        let snapshot = disk.clone();
        drop(disk);
        if let Err(e) = persist(&snapshot) {
            debug!(error = %e, "gitmirror: peer pubkey cache persist failed (continuing)");
        }
    }

    /// Remove an entry — used when the operator explicitly wants a
    /// hostname to be re-TOFU'd after a key rotation.
    #[allow(dead_code)]
    pub fn forget(&self, hostname: &str) {
        let mut disk = self.inner.lock().expect("pubkey cache poisoned");
        if disk.entries.remove(hostname).is_some() {
            let snapshot = disk.clone();
            drop(disk);
            let _ = persist(&snapshot);
        }
    }
}

fn persist(disk: &Disk) -> Result<()> {
    let path = cache_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("creating gitmirror state dir {}", parent.display()))?;
    }
    let tmp = path.with_extension("json.tmp");
    let bytes = serde_json::to_vec_pretty(disk).context("serializing peer pubkey cache")?;
    std::fs::write(&tmp, bytes).with_context(|| format!("writing {}", tmp.display()))?;
    // Root-owned, 0600 — matches other gitmirror state files.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600))
            .with_context(|| format!("chmod 0600 {}", tmp.display()))?;
    }
    std::fs::rename(&tmp, &path)
        .with_context(|| format!("renaming to {}", path.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn first_sighting_then_match() {
        let c = PeerPubkeyCache::empty();
        assert_eq!(c.check("danlap", "aaaa"), CheckOutcome::FirstSighting);
        c.accept("danlap", "aaaa", 100);
        assert_eq!(c.check("danlap", "aaaa"), CheckOutcome::Match);
    }

    #[test]
    fn different_key_for_same_host_is_mismatch() {
        let c = PeerPubkeyCache::empty();
        c.accept("danlap", "aaaa", 100);
        match c.check("danlap", "bbbb") {
            CheckOutcome::Mismatch { cached_prefix } => {
                assert_eq!(cached_prefix, "aaaa");
            }
            other => panic!("expected Mismatch, got {:?}", other),
        }
    }

    #[test]
    fn forget_resets_to_first_sighting() {
        let c = PeerPubkeyCache::empty();
        c.accept("danlap", "aaaa", 100);
        c.forget("danlap");
        assert_eq!(c.check("danlap", "aaaa"), CheckOutcome::FirstSighting);
    }
}
