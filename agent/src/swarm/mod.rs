// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm: envoy-to-envoy P2P content distribution.
//!
//! The envoy fleet is the swarm. Agents self-assign blobs by evaluating
//! manifest target specs locally, discover each other via multicast and
//! peer exchange, and download chunks from each other over mTLS HTTPS on
//! port 1531. The server holds the authoritative manifest and can be a
//! fallback source, but is not the primary mover of bytes.
//!
//! Content-addressed, chunked, resumable: blobs are identified by sha256,
//! split into chunks, and in-progress downloads survive process restarts
//! via on-disk partial directories.

pub mod bandwidth;
pub mod bitmap;
pub mod cache;
pub mod cli;
pub mod lockbox;
pub mod discovery;
pub mod download;
pub mod longdrawer;
pub mod gates;
pub mod gitback;
pub mod manager;
pub mod manifest;
pub mod meta;
pub mod orchestrator;
pub mod peer_client;
pub mod peer_health;
pub mod peermap;
pub mod policy;
pub mod prober;
pub mod runtime_limits;
pub mod scheduler;
pub mod puddle;
pub mod server;
pub mod state_mirror;
pub mod hostdox;
pub mod target;

use std::collections::hash_map::DefaultHasher;
use std::hash::{Hash, Hasher};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use crate::state::StateStore;
use tokio::sync::Notify;
use tracing::{info, warn};

use cache::BlobCache;

/// Maximum jitter before starting a swarm download (milliseconds).
/// Spreads download starts across envoys so they don't all begin on the same tick.
const MAX_DOWNLOAD_JITTER_MS: u64 = 1000;

/// Shared signal between the download scheduler and the multicast announcer.
///
/// The announcer's 5-second timer alone is too coarse for LAN-scale chunk
/// trading: by the time a peer learns that another envoy has partial chunks,
/// a fast-LAN download has already finished pulling from the seeder. This
/// signal bridges the gap — the scheduler pokes `notify` on the two moments
/// that actually matter (first chunk written, download complete) and the
/// announcer selects on it alongside its normal timer. `active_downloads`
/// lets the announcer tighten its cadence from 5s to 1s while any download
/// is in flight.
///
/// Scheduler side increments via `begin_download()` (returns a drop guard
/// that decrements on scope exit, so panics and early-return paths both
/// clean up correctly), and calls `fire()` on the announce-worthy events.
/// Announcer side reads `has_active_downloads()` to pick its interval and
/// selects on `notify.notified()` to react to firings between timer ticks.
pub struct AnnounceSignal {
    notify: Notify,
    active_downloads: AtomicUsize,
}

impl AnnounceSignal {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            notify: Notify::new(),
            active_downloads: AtomicUsize::new(0),
        })
    }

    /// Wake the announcer loop's `select!` so it fires a multicast cycle
    /// immediately (subject to its own min-gap rate limit).
    pub fn fire(&self) {
        self.notify.notify_one();
    }

    /// Await a fire notification. Called from the announcer's `select!`.
    pub async fn notified(&self) {
        self.notify.notified().await;
    }

    /// True while at least one download is in progress. Used by the
    /// announcer to pick its interval (1s active vs 5s idle).
    pub fn has_active_downloads(&self) -> bool {
        self.active_downloads.load(Ordering::Relaxed) > 0
    }

    /// Mark the start of a download. Returns a guard that decrements the
    /// counter on drop so panics and early-return paths both clean up.
    pub fn begin_download(self: &Arc<Self>) -> DownloadGuard {
        self.active_downloads.fetch_add(1, Ordering::Relaxed);
        DownloadGuard { signal: self.clone() }
    }
}

/// Drop guard returned by `AnnounceSignal::begin_download`.
pub struct DownloadGuard {
    signal: Arc<AnnounceSignal>,
}

impl Drop for DownloadGuard {
    fn drop(&mut self) {
        self.signal.active_downloads.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Record a retention deadline for a cached blob. Called from the swarm
/// manager on successful download completion.
pub fn record_retention(sha256: &str, retention_seconds: u64, state: &StateStore) {
    if retention_seconds == 0 || sha256.is_empty() {
        return;
    }
    if let Ok(mut wtxn) = state.write_txn() {
        let _ = state.swarm_retention.record(&mut wtxn, sha256, retention_seconds);
        let _ = wtxn.commit();
    }
}

/// Load and flatten the agent's most recently cached traits (any age) into the
/// dotted-key map [`target::match_target`] uses for `key=value` swarm
/// self-assignment patterns. Returns an empty map when no traits are cached or
/// the cache can't be read/parsed — hostname-glob targets still work; only
/// `key=value` targets depend on this. Cached traits can be stale (bounded by
/// the check-in cadence); for self-assignment, stale traits beat no traits.
pub fn cached_traits(state: &StateStore) -> std::collections::HashMap<String, String> {
    let rtxn = match state.read_txn() {
        Ok(t) => t,
        Err(_) => return std::collections::HashMap::new(),
    };
    let json = match state.traits.load_any(&rtxn) {
        Ok(Some(j)) => j,
        _ => return std::collections::HashMap::new(),
    };
    match serde_json::from_slice::<serde_json::Value>(&json) {
        Ok(v) => target::flatten_traits(&v),
        Err(_) => std::collections::HashMap::new(),
    }
}

/// Evict expired swarm blobs from the cache based on retention timestamps in LMDB.
pub fn evict_expired(state: &StateStore) {
    let rtxn = match state.read_txn() {
        Ok(t) => t,
        Err(_) => return,
    };
    let expired = match state.swarm_retention.expired(&rtxn) {
        Ok(e) => e,
        Err(_) => return,
    };
    drop(rtxn);

    if expired.is_empty() {
        return;
    }

    let cache = match BlobCache::new(crate::platform::paths::default_blobs_dir()) {
        Ok(c) => c,
        Err(_) => return,
    };

    for sha256 in &expired {
        cache.evict(sha256);
        if let Ok(mut wtxn) = state.write_txn() {
            let _ = state.swarm_retention.remove(&mut wtxn, sha256);
            let _ = wtxn.commit();
        }
    }
    info!(count = expired.len(), "evicted expired swarm blobs");
}

/// Delete graveyard blobs older than 7 days. Called on every check-in cycle.
/// The graveyard holds blobs that were revoked (operator action or retention
/// expiry) so they can be resurrected by a same-SHA reseed within the grace
/// window. This is the only filesystem unlink in the swarm subsystem.
pub fn cleanup_graveyard() {
    let graveyard_dir = crate::platform::paths::default_graveyard_dir();
    if !graveyard_dir.exists() {
        return;
    }

    let entries = match std::fs::read_dir(&graveyard_dir) {
        Ok(e) => e,
        Err(_) => return,
    };

    let cutoff = std::time::SystemTime::now() - std::time::Duration::from_secs(7 * 24 * 3600);
    let mut count = 0;

    for entry in entries.flatten() {
        if let Ok(meta) = entry.metadata() {
            let mtime = meta.modified().unwrap_or(std::time::SystemTime::UNIX_EPOCH);
            if mtime < cutoff {
                std::fs::remove_file(entry.path()).ok();
                count += 1;
            }
        }
    }

    if count > 0 {
        info!(count, "cleaned up graveyard blobs");
    }
}

/// Remove stale partial download directories older than 24 hours.
/// A partial directory that hasn't gained new chunks in 24 hours is a failed
/// download that will never complete — clean it up to reclaim disk space.
pub fn cleanup_stale_partials() {
    let blobs_dir = crate::platform::paths::default_blobs_dir();
    if !blobs_dir.exists() {
        return;
    }

    let entries = match std::fs::read_dir(&blobs_dir) {
        Ok(e) => e,
        Err(_) => return,
    };

    let cutoff = std::time::SystemTime::now() - std::time::Duration::from_secs(24 * 3600);
    let mut count = 0;

    for entry in entries.flatten() {
        let name = entry.file_name();
        let s = name.to_string_lossy();
        if !s.ends_with(".partial") {
            continue;
        }
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }

        // Find the newest chunk mtime in the partial directory.
        let newest = match std::fs::read_dir(&path) {
            Ok(chunks) => chunks
                .flatten()
                .filter_map(|c| c.metadata().ok()?.modified().ok())
                .max()
                .unwrap_or(std::time::SystemTime::UNIX_EPOCH),
            Err(_) => continue,
        };

        if newest < cutoff {
            std::fs::remove_dir_all(&path).ok();
            count += 1;
        }
    }

    if count > 0 {
        info!(count, "cleaned up stale partial downloads");
    }
}

/// Evict revoked blobs from the cache, clean up files/ symlinks and .sources
/// metadata. Called after manifest merges (both check-in gossip and multicast
/// peer sync). For envoys that originated the seed, this also removes the
/// source file + .swarm manifest from seed/ so the trait collector stops
/// re-reporting it, and moves the cached blob to graveyard/ so a same-SHA
/// reseed within 7 days can resurrect it.
pub fn evict_revoked_blobs(manifest: &manifest::Manifest, peer_map: Option<&std::sync::Arc<peermap::PeerMap>>) {
    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let cache = match cache::BlobCache::new(blobs_dir.clone()) {
        Ok(c) => c,
        Err(_) => return,
    };
    let files_dir = blobs_dir.parent().unwrap_or(&blobs_dir).join("files");
    let seeds_dir = crate::platform::paths::default_seeds_dir();
    let graveyard_dir = crate::platform::paths::default_graveyard_dir();

    for sha256 in manifest.revoked_blobs() {
        let blob_path = blobs_dir.join(&sha256);
        let blob_present = cache.has_blob(&sha256) || blob_path.symlink_metadata().is_ok();

        // Sweep any lingering `.partial/` directory first. A download
        // interrupted by a mid-flight revoke used to leave its chunks
        // on disk forever, and the swarm_cache trait collector kept
        // reporting the sha as `downloading`, which pinned the server's
        // per-envoy status at `revoking`. `delete_partial` is a no-op
        // when no partial exists.
        let partial_path = blobs_dir.join(format!("{}.partial", sha256));
        if partial_path.is_dir() {
            cache.delete_partial(&sha256);
            info!(sha256 = &sha256[..12.min(sha256.len())], "wiped revoked partial");
        }

        if !blob_present {
            continue;
        }

        // If the blob entry is a symlink into seeds_dir, this envoy is the
        // original seeder. Remove the seed source + manifest so the trait
        // collector stops re-reporting it — but only if no other blob entry
        // links to the same file (label-collision protection: a same-filename
        // reseed with different content must not destroy the replacement).
        let is_seeder_link = std::fs::read_link(&blob_path).ok();
        if let Some(ref target) = is_seeder_link {
            if target.starts_with(&seeds_dir) {
                let other_links = std::fs::read_dir(&blobs_dir)
                    .into_iter()
                    .flatten()
                    .flatten()
                    .any(|e| {
                        let p = e.path();
                        p != blob_path
                            && std::fs::read_link(&p).ok().as_ref() == Some(target)
                    });
                if other_links {
                    info!(
                        sha256 = &sha256[..12.min(sha256.len())],
                        "seed source file still referenced by another blob, skipping delete"
                    );
                } else {
                    std::fs::remove_file(target).ok();
                    let swarm_manifest = target.with_extension("swarm");
                    std::fs::remove_file(&swarm_manifest).ok();
                    info!(sha256 = &sha256[..12.min(sha256.len())], "removed seed source file");
                }
            }
        }

        // Move blob to graveyard so a same-SHA reseed within 7 days
        // can resurrect it without re-downloading.
        if let Err(e) = std::fs::create_dir_all(&graveyard_dir) {
            warn!(error = %e, "failed to create graveyard directory");
        } else {
            let dest = graveyard_dir.join(&sha256);
            if std::fs::rename(&blob_path, &dest).is_err() {
                // Symlink whose target was removed above is now dangling — just unlink.
                if blob_path.symlink_metadata().is_ok() {
                    std::fs::remove_file(&blob_path).ok();
                }
            }
        }

        // Drop the cache's in-memory bookkeeping for this blob if it
        // was tracked there (no-op for symlink-only entries).
        if cache.has_blob(&sha256) {
            cache.evict(&sha256);
        }

        // Remove .sources metadata.
        std::fs::remove_file(blobs_dir.join(format!("{}.sources", &sha256))).ok();

        // Remove files/<label> symlinks pointing to this blob.
        if let Ok(entries) = std::fs::read_dir(&files_dir) {
            for entry in entries.flatten() {
                if let Ok(target) = std::fs::read_link(entry.path()) {
                    if target.ends_with(&sha256) {
                        std::fs::remove_file(entry.path()).ok();
                    }
                }
            }
        }

        if let Some(pm) = peer_map {
            pm.flush_sha(&sha256);
        }

        info!(sha256 = &sha256[..12.min(sha256.len())], "evicted revoked blob");
    }
}

/// Register local seed files into the manifest CRDT so they propagate via
/// P2P gossip (multicast + peer HTTPS sync). Called each check-in cycle.
/// Seeds already in the manifest are skipped (idempotent).
pub fn register_seeds_in_manifest(
    manifest: &manifest::Manifest,
    envoy_id: &str,
    swarm_port: u16,
) {
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct SwarmManifest {
        target: String,
        #[serde(default)]
        retention: String,
    }

    let seeds_dir = crate::platform::paths::default_seeds_dir();
    if !seeds_dir.exists() {
        return;
    }
    let entries = match std::fs::read_dir(&seeds_dir) {
        Ok(e) => e,
        Err(_) => return,
    };

    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let local_ip = discovery::local_ipv4().unwrap_or(std::net::Ipv4Addr::LOCALHOST);
    let address = format!("{}:{}", local_ip, swarm_port);

    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };
        if name.starts_with('.') || name.ends_with(".swarm") || name == "admin_rejected" {
            continue;
        }

        // Read .swarm manifest.
        let manifest_path = seeds_dir.join(format!("{}.swarm", name));
        let manifest_data = match std::fs::read_to_string(&manifest_path) {
            Ok(d) => d,
            Err(_) => continue,
        };
        let swarm_manifest: SwarmManifest = match serde_json::from_str(&manifest_data) {
            Ok(m) => m,
            Err(_) => continue,
        };

        // Get SHA256 from blob cache symlink (trait collector already created it).
        // Scan .blobs/ for a symlink pointing to this seed file.
        // Compare via canonical paths since the symlink target may be relative or absolute.
        let canonical_path = path.canonicalize().unwrap_or_else(|_| path.clone());
        let mut sha256 = None;
        if let Ok(blobs) = std::fs::read_dir(&blobs_dir) {
            for blob_entry in blobs.flatten() {
                let blob_path = blob_entry.path();
                if let Ok(target) = std::fs::read_link(&blob_path) {
                    let canonical_target = if target.is_absolute() {
                        target.canonicalize().unwrap_or(target)
                    } else {
                        blobs_dir.join(&target).canonicalize().unwrap_or(target)
                    };
                    if canonical_target == canonical_path {
                        if let Some(name) = blob_path.file_name().and_then(|n| n.to_str()) {
                            if !name.contains('.') && name.len() == 64 {
                                sha256 = Some(name.to_string());
                                break;
                            }
                        }
                    }
                }
            }
        }

        let sha256 = match sha256 {
            Some(s) => s,
            None => continue, // trait collector hasn't created the symlink yet
        };

        // Skip if already in manifest as active.
        if manifest.has_active(&sha256) {
            continue;
        }

        let size = path.metadata().map(|m| m.len()).unwrap_or(0);
        manifest.record_seed(
            &sha256,
            &name,
            &swarm_manifest.target,
            &swarm_manifest.retention,
            envoy_id,
            size,
            &address,
        );
        info!(sha256 = &sha256[..12.min(sha256.len())], label = %name, "registered seed in manifest");
    }
}

/// Merge manifest entries received from the server's check-in response.
/// This is the fallback gossip channel for cross-subnet envoys that can't
/// reach each other via multicast. Newly revoked blobs are evicted from cache.
pub fn merge_manifest_entries(
    manifest: &manifest::Manifest,
    entries: &[crate::proto::SwarmManifestEntry],
    peer_map: Option<&std::sync::Arc<peermap::PeerMap>>,
) {
    if entries.is_empty() {
        return;
    }

    let mut remote = manifest::ManifestData::new();
    for e in entries {
        remote.entries.insert(
            e.sha256.clone(),
            manifest::BlobEntry {
                sha256: e.sha256.clone(),
                label: e.label.clone(),
                target: e.target.clone(),
                retention: e.retention.clone(),
                seeder_id: e.seeder_id.clone(),
                size: e.size,
                status: if e.status == "revoked" {
                    manifest::BlobStatus::Revoked
                } else {
                    manifest::BlobStatus::Active
                },
                updated_at: e.updated_at,
                updated_by: e.updated_by.clone(),
                seeder_address: String::new(),
            },
        );
    }

    if manifest.merge(&remote) {
        info!(entries = entries.len(), "merged manifest entries from server");
        evict_revoked_blobs(manifest, peer_map);
    }
}

/// Hidden agent verb: full sweep of the substrate-shared blob cache,
/// invoked via TaskDispatch when an admin runs `vigocli swarm cleanup`.
/// Idempotent — safe to re-run; reports zero changes when nothing's
/// stale. Touches only filesystem state; does not contact peers.
///
/// Two passes:
///
/// 1. **`files/<label>` symlinks** — readdir + readlink. Any symlink
///    whose target doesn't exist (target file removed but symlink
///    survived an interrupted revoke / rename) is unlinked.
/// 2. **`.partial/` dirs** — any directory whose final blob (`<sha>`)
///    has been assembled is `rm -rf`'d. The download completed but
///    the assemble step skipped the cleanup (interrupted, crash, or
///    a pre-0.34 version of the assembler that didn't sweep).
///
/// What this does **not** clean: revoked blobs that still have a
/// manifest entry (that's `evict_revoked_blobs` on the merge path),
/// retention-expired blobs (RetentionPruner's job server-side), or
/// graveyard entries (those expire on age, separately).
pub fn apply_cleanup() -> anyhow::Result<()> {
    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let files_dir = blobs_dir.parent().unwrap_or(&blobs_dir).join("files");
    let result = run_cleanup(&blobs_dir, &files_dir);
    println!(
        "swarm cleanup: dangling_symlinks={} stale_partials={} errors={}",
        result.dangling_symlinks, result.stale_partials, result.errors
    );
    if result.errors > 0 {
        anyhow::bail!("swarm cleanup completed with {} errors", result.errors);
    }
    Ok(())
}

/// Result of one cleanup pass. Public so tests can inspect counts
/// without parsing stdout.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct CleanupResult {
    pub dangling_symlinks: usize,
    pub stale_partials: usize,
    pub errors: usize,
}

/// Testable inner of `apply_cleanup` — takes explicit blob/file dirs
/// so tests can drive the sweep against a tempdir layout.
pub fn run_cleanup(blobs_dir: &std::path::Path, files_dir: &std::path::Path) -> CleanupResult {
    let mut result = CleanupResult::default();

    // Pass 1: dangling files/<label> symlinks.
    if files_dir.is_dir() {
        match std::fs::read_dir(files_dir) {
            Ok(entries) => {
                for entry in entries.flatten() {
                    let path = entry.path();
                    let meta = match path.symlink_metadata() {
                        Ok(m) => m,
                        Err(_) => continue,
                    };
                    if !meta.file_type().is_symlink() {
                        continue;
                    }
                    // exists() follows symlinks — false means the
                    // target is gone, so the symlink is dangling.
                    if !path.exists() {
                        match std::fs::remove_file(&path) {
                            Ok(()) => {
                                result.dangling_symlinks += 1;
                                info!(path = %path.display(), "removed dangling files/ symlink");
                            }
                            Err(e) => {
                                result.errors += 1;
                                warn!(path = %path.display(), error = %e, "failed to remove dangling symlink");
                            }
                        }
                    }
                }
            }
            Err(e) => {
                warn!(dir = %files_dir.display(), error = %e, "cannot read files dir");
            }
        }
    }

    // Pass 2: .partial/ dirs whose final blob has been assembled.
    if blobs_dir.is_dir() {
        match std::fs::read_dir(blobs_dir) {
            Ok(entries) => {
                for entry in entries.flatten() {
                    let path = entry.path();
                    let name = match path.file_name().and_then(|s| s.to_str()) {
                        Some(n) => n,
                        None => continue,
                    };
                    let sha = match name.strip_suffix(".partial") {
                        Some(s) => s,
                        None => continue,
                    };
                    if !path.is_dir() {
                        continue;
                    }
                    // The final blob lives at `<blobs_dir>/<sha>`. If
                    // it exists, the download is done — the partial
                    // is leftover from an unclean assemble.
                    if blobs_dir.join(sha).is_file() {
                        match std::fs::remove_dir_all(&path) {
                            Ok(()) => {
                                result.stale_partials += 1;
                                info!(sha256 = &sha[..12.min(sha.len())], "removed stale .partial/ dir");
                            }
                            Err(e) => {
                                result.errors += 1;
                                warn!(path = %path.display(), error = %e, "failed to remove stale .partial");
                            }
                        }
                    }
                }
            }
            Err(e) => {
                warn!(dir = %blobs_dir.display(), error = %e, "cannot read blobs dir");
            }
        }
    }

    result
}

#[cfg(test)]
mod cleanup_tests {
    use super::*;
    use std::fs;

    #[test]
    fn cleanup_empty_dirs_returns_zero() {
        let dir = tempfile::tempdir().unwrap();
        let blobs = dir.path().join(".blobs");
        let files = dir.path().join("files");
        fs::create_dir_all(&blobs).unwrap();
        fs::create_dir_all(&files).unwrap();
        let r = run_cleanup(&blobs, &files);
        assert_eq!(r, CleanupResult::default());
    }

    #[test]
    fn cleanup_removes_dangling_symlink() {
        let dir = tempfile::tempdir().unwrap();
        let blobs = dir.path().join(".blobs");
        let files = dir.path().join("files");
        fs::create_dir_all(&blobs).unwrap();
        fs::create_dir_all(&files).unwrap();

        // Symlink pointing at a non-existent target.
        let dangling = files.join("missing.bin");
        std::os::unix::fs::symlink(blobs.join("nonexistent"), &dangling).unwrap();

        // Symlink with a live target — must be left alone.
        let live_target = blobs.join("present");
        fs::write(&live_target, b"data").unwrap();
        let live_link = files.join("present.bin");
        std::os::unix::fs::symlink(&live_target, &live_link).unwrap();

        let r = run_cleanup(&blobs, &files);
        assert_eq!(r.dangling_symlinks, 1, "exactly one dangling link should be removed");
        assert_eq!(r.stale_partials, 0);
        assert!(!dangling.exists() && dangling.symlink_metadata().is_err(), "dangling link gone");
        assert!(live_link.symlink_metadata().is_ok(), "live link preserved");
    }

    #[test]
    fn cleanup_removes_partial_when_final_blob_exists() {
        let dir = tempfile::tempdir().unwrap();
        let blobs = dir.path().join(".blobs");
        let files = dir.path().join("files");
        fs::create_dir_all(&blobs).unwrap();
        fs::create_dir_all(&files).unwrap();

        let sha = "a".repeat(64);
        // Final blob present + stale partial dir alongside it.
        fs::write(blobs.join(&sha), b"complete").unwrap();
        let stale_partial = blobs.join(format!("{}.partial", sha));
        fs::create_dir_all(&stale_partial).unwrap();
        fs::write(stale_partial.join("chunk_0000"), b"x").unwrap();

        // Active partial (no final blob) — must be left alone.
        let active_sha = "b".repeat(64);
        let active_partial = blobs.join(format!("{}.partial", active_sha));
        fs::create_dir_all(&active_partial).unwrap();
        fs::write(active_partial.join("chunk_0000"), b"y").unwrap();

        let r = run_cleanup(&blobs, &files);
        assert_eq!(r.stale_partials, 1, "exactly the completed-blob's partial should be removed");
        assert_eq!(r.dangling_symlinks, 0);
        assert!(!stale_partial.exists(), "stale partial gone");
        assert!(active_partial.is_dir(), "active partial preserved");
    }

    #[test]
    fn cleanup_idempotent_second_run_zero_changes() {
        let dir = tempfile::tempdir().unwrap();
        let blobs = dir.path().join(".blobs");
        let files = dir.path().join("files");
        fs::create_dir_all(&blobs).unwrap();
        fs::create_dir_all(&files).unwrap();

        // Lay down both kinds of stale state.
        let dangling = files.join("missing.bin");
        std::os::unix::fs::symlink(blobs.join("gone"), &dangling).unwrap();
        let sha = "c".repeat(64);
        fs::write(blobs.join(&sha), b"complete").unwrap();
        fs::create_dir_all(blobs.join(format!("{}.partial", sha))).unwrap();

        let first = run_cleanup(&blobs, &files);
        assert_eq!((first.dangling_symlinks, first.stale_partials), (1, 1));
        let second = run_cleanup(&blobs, &files);
        assert_eq!(second, CleanupResult::default(), "second run finds nothing to do");
    }

    #[test]
    fn cleanup_skips_missing_dirs() {
        let dir = tempfile::tempdir().unwrap();
        let r = run_cleanup(&dir.path().join("missing-blobs"), &dir.path().join("missing-files"));
        assert_eq!(r, CleanupResult::default());
    }
}

/// Compute a deterministic download jitter for a blob on this machine.
/// Returns 0..MAX_DOWNLOAD_JITTER_MS milliseconds based on hashing hostname + SHA256.
/// Each machine gets a different delay for the same blob, spreading starts.
pub(crate) fn download_jitter(sha256: &str) -> u64 {
    let hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| format!("{}", std::process::id()));
    let mut hasher = DefaultHasher::new();
    hostname.hash(&mut hasher);
    sha256.hash(&mut hasher);
    hasher.finish() % MAX_DOWNLOAD_JITTER_MS
}

#[cfg(test)]
mod cached_traits_tests {
    use super::*;

    #[test]
    fn test_cached_traits_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let store = StateStore::open(dir.path()).unwrap();

        // Nothing cached yet → empty map (so a `trait=value` target just
        // matches nothing, rather than the call panicking).
        assert!(cached_traits(&store).is_empty());

        let mut wtxn = store.write_txn().unwrap();
        store
            .traits
            .store(&mut wtxn, br#"{"os":{"distro":"ubuntu"},"is_container":false}"#)
            .unwrap();
        wtxn.commit().unwrap();

        let f = cached_traits(&store);
        assert_eq!(f.get("os.distro").map(String::as_str), Some("ubuntu"));
        assert_eq!(f.get("is_container").map(String::as_str), Some("false"));
    }
}
