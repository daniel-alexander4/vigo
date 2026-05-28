// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Trust-on-first-use (TOFU) cache of `(hostname → Ed25519 pubkey)` for
//! verifying gitback multicast announcement signatures.
//!
//! ## Model
//!
//! Each gitback-signed announcement embeds the sender's Ed25519 verifying
//! key (hex-encoded) in its `GITBACK:` line and appends a `SIG:` line
//! covering the rest of the packet. Receivers:
//!
//! 1. Extract the pubkey and signature from the packet.
//! 2. Look up `hostname → pubkey` in this cache.
//! 3. If absent: cache the embedded pubkey (TOFU — first announcement
//!    wins). Verify the signature against it; on success, accept the
//!    packet and persist the cache.
//! 4. If present and the embedded pubkey matches: verify and accept.
//! 5. If present and the embedded pubkey differs: log WARN and drop
//!    the packet's gitback content. Same behavior if signature fails
//!    verification under the cached pubkey.
//!
//! Admin recovery: delete `/var/lib/vigo/swarm/gitback/peer_pubkeys.json` to
//! clear the cache after a re-enrollment. A fresh cache will
//! TOFU-accept the new pubkeys.
//!
//! ## Threat coverage
//!
//! | Attack                                          | Blocked? |
//! |-------------------------------------------------|----------|
//! | Spoof `GITBACK:` from untrusted hostname          | Yes — signature check fails (attacker doesn't have the real envoy's private key). |
//! | Replay an old, valid announcement               | Yes — signature covers the emission timestamp. |
//! | Race the real envoy to first announcement       | Blocked for envoys present in the cache by the time multicast verification runs: [`super::pubkey_bootstrap`] (default in 0.41.20+) fetches `GET /api/v1/fleet/pubkeys` on agent startup and every 15 min, pre-populating with authoritative pubkeys. The bootstrap fetch and the multicast listener spawn concurrently — there is a brief startup window (sub-second on a healthy network) during which TOFU may still admit the first announce, but the next bootstrap refresh corrects any TOFU mismatch via [`PeerPubkeyCache::bootstrap_set`] with a WARN log line. Operator gets a record either way. |
//! | Rotate pubkey (legitimate re-enrollment)        | Server bootstrap auto-overrides the cached entry on the next 15-min refresh; emits a WARN so the operator can correlate. Manual cache clear no longer required. |

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Mutex;
use tracing::{debug, info, warn};

/// Cache file on disk. Root-owned, 0600. Reload on every startup; persist
/// is throttled — see [`PeerPubkeyCache::accept`]. Routes through
/// [`crate::platform::paths::default_gitback_dir`] so `VIGO_GITBACK_DIR`
/// is honored.
pub fn cache_path() -> PathBuf {
    crate::platform::paths::default_gitback_dir().join("peer_pubkeys.json")
}

/// Minimum seconds between `accept`-triggered disk writes. In-memory
/// updates are not throttled; only the serialize+write side is. Under
/// burst peer-announcement load (measured: 135K announcements/day),
/// per-mutation persist clones+serializes the full cache (~25 MB at
/// 125K entries) and fragments the glibc heap up to peak usage —
/// throttling collapses that to ≤1 write/minute. The trade-off is
/// up-to-60 s of last_seen_unix data lost on crash, which is harmless
/// against a 90-day [`PUBKEY_CACHE_TTL_SECS`] horizon and a TOFU model
/// that re-accepts the same pubkey on next sighting anyway.
const COALESCE_SECS: i64 = 60;

/// Retention window for stale `(hostname → pubkey)` entries. Matches
/// the lockbox tombstone window for the same "laptop parked for an
/// extended trip" reasoning — short enough that genuinely-departed
/// envoys eventually clear, long enough to cover real-world peer
/// absences without breaking TOFU on return.
pub const PUBKEY_CACHE_TTL_SECS: i64 = 90 * 24 * 60 * 60;

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
    /// Unix seconds of the most recent successful `accept`-triggered
    /// persist. Read+updated under `Relaxed` ordering — coalescing
    /// is best-effort; double-persist on a torn read costs at most one
    /// extra write and never produces inconsistent state.
    last_persisted_at: AtomicI64,
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
            last_persisted_at: AtomicI64::new(0),
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
                    "gitback: could not read peer pubkey cache, starting empty",
                );
                return Self::empty();
            }
        };
        match serde_json::from_slice::<Disk>(&bytes) {
            Ok(disk) => {
                info!(
                    path = %path.display(),
                    entries = disk.entries.len(),
                    "gitback: loaded peer pubkey cache",
                );
                Self {
                    inner: Mutex::new(disk),
                    last_persisted_at: AtomicI64::new(0),
                }
            }
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "gitback: peer pubkey cache is malformed, starting empty",
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
    /// entry; on repeat, refreshes `last_seen_unix`. In-memory update
    /// always happens; persist is throttled to once per
    /// [`COALESCE_SECS`] across all callers — see the constant's
    /// comment for rationale and trade-off. The crash window applies
    /// only to `last_seen_unix` refresh and to TOFU first-sightings
    /// that haven't yet hit the next persist tick; both are recovered
    /// on the peer's next announcement.
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

        if now - self.last_persisted_at.load(Ordering::Relaxed) < COALESCE_SECS {
            return;
        }
        // Advance at attempt time, not success — under sustained disk
        // failure (read-only fs, permission flip) advancing only on
        // success would retry the persist on every mutation,
        // re-introducing the per-mutation churn this throttle exists
        // to prevent. The in-memory cache stays correct; disk lag
        // grows by at most COALESCE_SECS between successful writes.
        self.last_persisted_at.store(now, Ordering::Relaxed);
        let snapshot = disk.clone();
        drop(disk);
        if let Err(e) = persist(&snapshot) {
            debug!(error = %e, "gitback: peer pubkey cache persist failed (continuing)");
        }
    }

    /// Apply a server-distributed pubkey, overriding any TOFU entry.
    /// Server data is authoritative — `(hostname → pubkey)` came from
    /// the registry, not from a multicast announce. If the cache
    /// already holds a different pubkey for this hostname (TOFU'd from
    /// a possibly-spoofed first announcement, or a stale entry from
    /// before a re-enrollment), the entry is replaced and a WARN is
    /// emitted so the operator can correlate. New entries pre-empt
    /// the TOFU path entirely on the next announcement.
    pub fn bootstrap_set(&self, hostname: &str, pubkey_hex: &str, now: i64) {
        let mut disk = self.inner.lock().expect("pubkey cache poisoned");
        disk.version = 1;
        let conflict = match disk.entries.get(hostname) {
            Some(e) if e.pubkey_hex != pubkey_hex => Some(e.pubkey_hex.clone()),
            _ => None,
        };
        match disk.entries.get_mut(hostname) {
            Some(e) => {
                e.pubkey_hex = pubkey_hex.to_string();
                e.last_seen_unix = now;
            }
            None => {
                disk.entries.insert(
                    hostname.to_string(),
                    Entry {
                        pubkey_hex: pubkey_hex.to_string(),
                        first_seen_unix: now,
                        last_seen_unix: now,
                    },
                );
            }
        }
        let snapshot = disk.clone();
        drop(disk);
        if let Some(old) = conflict {
            warn!(
                hostname,
                old_prefix = %old.chars().take(12).collect::<String>(),
                new_prefix = %pubkey_hex.chars().take(12).collect::<String>(),
                "gitback: server-distributed pubkey overrode cached TOFU entry; \
                 prior entry was likely a TOFU race or a stale key from before re-enrollment",
            );
        }
        if let Err(e) = persist(&snapshot) {
            debug!(error = %e, "gitback: peer pubkey cache persist failed (continuing)");
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

    /// Evict entries whose `last_seen_unix` is older than
    /// `now - retention_secs`. Persists synchronously when any entry
    /// is removed so the on-disk file reflects the trimmed table
    /// across restarts. Returns the number of entries removed.
    ///
    /// Called from the gitback prune reactor — see
    /// [`super::run_prune_reactor`]. Sized to match TOFU semantics:
    /// at [`PUBKEY_CACHE_TTL_SECS`] (90 days) a peer that's been
    /// silent that long is treated as gone-for-good and its slot is
    /// reclaimed; the next sighting re-TOFUs.
    pub fn gc(&self, now: i64, retention_secs: i64) -> usize {
        let mut disk = self.inner.lock().expect("pubkey cache poisoned");
        let cutoff = now - retention_secs;
        let before = disk.entries.len();
        disk.entries.retain(|_, e| e.last_seen_unix >= cutoff);
        let removed = before - disk.entries.len();
        if removed == 0 {
            return 0;
        }
        // Same attempt-time advance semantics as `accept` — see
        // its comment for the sustained-failure rationale.
        self.last_persisted_at.store(now, Ordering::Relaxed);
        let snapshot = disk.clone();
        drop(disk);
        if let Err(e) = persist(&snapshot) {
            debug!(error = %e, "gitback: peer pubkey cache persist after gc failed (continuing)");
        }
        removed
    }
}

fn persist(disk: &Disk) -> Result<()> {
    let path = cache_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("creating gitback state dir {}", parent.display()))?;
    }
    let tmp = path.with_extension("json.tmp");
    let bytes = serde_json::to_vec_pretty(disk).context("serializing peer pubkey cache")?;
    std::fs::write(&tmp, bytes).with_context(|| format!("writing {}", tmp.display()))?;
    // Root-owned, 0600 — matches other gitback state files.
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

    #[test]
    fn bootstrap_set_creates_new_entry() {
        let c = PeerPubkeyCache::empty();
        c.bootstrap_set("plex", "deadbeef", 100);
        assert_eq!(c.check("plex", "deadbeef"), CheckOutcome::Match);
    }

    #[test]
    fn bootstrap_set_overrides_tofu_mismatch() {
        let c = PeerPubkeyCache::empty();
        // TOFU'd a (possibly-spoofed) key first.
        c.accept("plex", "aaaa", 100);
        // Server distribution arrives later with the authoritative key.
        c.bootstrap_set("plex", "bbbb", 200);
        // The TOFU entry is replaced; subsequent checks must verify
        // against the server-distributed key, not the original TOFU.
        assert_eq!(c.check("plex", "bbbb"), CheckOutcome::Match);
        match c.check("plex", "aaaa") {
            CheckOutcome::Mismatch { cached_prefix } => {
                assert_eq!(cached_prefix, "bbbb");
            }
            other => panic!("expected stale TOFU key to mismatch the new bootstrap, got {:?}", other),
        }
    }

    #[test]
    fn bootstrap_set_idempotent_on_same_key() {
        let c = PeerPubkeyCache::empty();
        c.bootstrap_set("plex", "deadbeef", 100);
        c.bootstrap_set("plex", "deadbeef", 200);
        assert_eq!(c.check("plex", "deadbeef"), CheckOutcome::Match);
    }

    #[test]
    fn cache_path_lives_under_gitback_dir() {
        let p = cache_path();
        assert_eq!(
            p.parent().map(std::path::Path::to_path_buf),
            Some(crate::platform::paths::default_gitback_dir()),
        );
        assert_eq!(p.file_name().and_then(|s| s.to_str()), Some("peer_pubkeys.json"));
    }

    // -----------------------------------------------------------------
    // gc — evict entries past PUBKEY_CACHE_TTL_SECS.
    // -----------------------------------------------------------------

    /// Helper: read entries.len() through the public Check API would
    /// require knowing every hostname, which defeats the test. Cheat
    /// via the lock — same module, allowed.
    fn entry_count(c: &PeerPubkeyCache) -> usize {
        c.inner.lock().unwrap().entries.len()
    }

    #[test]
    fn gc_evicts_entries_past_retention() {
        let c = PeerPubkeyCache::empty();
        // Three entries, last_seen at t=100. now=t+1d. retention=12h.
        // All three should evict.
        c.accept("a.example", "aa", 100);
        c.accept("b.example", "bb", 100);
        c.accept("c.example", "cc", 100);
        assert_eq!(entry_count(&c), 3);

        let removed = c.gc(100 + 86400, 12 * 3600);
        assert_eq!(removed, 3);
        assert_eq!(entry_count(&c), 0);
    }

    #[test]
    fn gc_keeps_fresh_entries() {
        let c = PeerPubkeyCache::empty();
        c.accept("old.example", "aa", 100);
        c.accept("fresh.example", "bb", 200);
        // now=300, retention=120 → cutoff=180. only "old" evicts.
        let removed = c.gc(300, 120);
        assert_eq!(removed, 1);
        assert_eq!(entry_count(&c), 1);
        assert_eq!(c.check("fresh.example", "bb"), CheckOutcome::Match);
        assert_eq!(c.check("old.example", "aa"), CheckOutcome::FirstSighting);
    }

    #[test]
    fn gc_noop_when_all_fresh_returns_zero() {
        let c = PeerPubkeyCache::empty();
        c.accept("a.example", "aa", 1000);
        c.accept("b.example", "bb", 1000);
        let removed = c.gc(1100, 200);
        assert_eq!(removed, 0);
        assert_eq!(entry_count(&c), 2);
    }

    // -----------------------------------------------------------------
    // accept persist throttle — in-memory update always fires; disk
    // write is coalesced to ≤1 per COALESCE_SECS. Verified via the
    // `last_persisted_at` timestamp it advances on a successful write.
    // -----------------------------------------------------------------

    #[test]
    fn accept_throttles_persist_within_coalesce_window() {
        let c = PeerPubkeyCache::empty();
        // First accept at t=1000 (last_persisted_at starts at 0 →
        // 1000-0 >= 60 → persists, advances to 1000).
        c.accept("a.example", "aa", 1000);
        let after_first = c.last_persisted_at.load(Ordering::Relaxed);
        assert!(after_first >= 1000, "first persist must advance the timestamp");

        // Two more accepts inside the 60s window — in-memory updates
        // land but last_persisted_at MUST NOT move.
        c.accept("b.example", "bb", 1010);
        c.accept("c.example", "cc", 1059);
        assert_eq!(c.last_persisted_at.load(Ordering::Relaxed), after_first);
        // All three entries are in memory regardless.
        assert_eq!(entry_count(&c), 3);
        assert_eq!(c.check("b.example", "bb"), CheckOutcome::Match);
        assert_eq!(c.check("c.example", "cc"), CheckOutcome::Match);
    }

    #[test]
    fn accept_persists_after_coalesce_window_elapses() {
        let c = PeerPubkeyCache::empty();
        c.accept("a.example", "aa", 1000);
        let after_first = c.last_persisted_at.load(Ordering::Relaxed);
        // 60s+ later → persist fires again, timestamp moves.
        c.accept("b.example", "bb", 1060);
        assert!(
            c.last_persisted_at.load(Ordering::Relaxed) > after_first,
            "second persist past COALESCE_SECS must advance the timestamp",
        );
    }
}
