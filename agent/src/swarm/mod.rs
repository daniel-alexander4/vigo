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
pub mod cache;
pub mod discovery;
pub mod download;
pub mod manager;
pub mod manifest;
pub mod meta;
pub mod orchestrator;
pub mod peer_client;
pub mod peermap;
pub mod prober;
pub mod scheduler;
pub mod server;
pub mod target;

use std::collections::hash_map::DefaultHasher;
use std::hash::{Hash, Hasher};

use crate::state::StateStore;
use tracing::{info, warn};

use cache::BlobCache;

/// Maximum jitter before starting a swarm download (milliseconds).
/// Spreads download starts across envoys so they don't all begin on the same tick.
const MAX_DOWNLOAD_JITTER_MS: u64 = 1000;

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

/// Retire blobs the server has marked as expired. Moves blobs from the active
/// cache to the retired directory so the peer server stops serving them.
/// Also removes source files + `.swarm` manifests from seed/ so the
/// swarm_seeds trait collector doesn't re-report them.
pub fn retire_blobs(retired: &[String]) {
    if retired.is_empty() {
        return;
    }

    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let retired_dir = crate::platform::paths::default_retired_dir();
    if let Err(e) = std::fs::create_dir_all(&retired_dir) {
        warn!(error = %e, "failed to create retired directory");
        return;
    }

    let files_dir = blobs_dir.parent().unwrap_or(&blobs_dir).join("files");
    let seeds_dir = crate::platform::paths::default_seeds_dir();
    let mut count = 0;

    for sha256 in retired {
        let blob_path = blobs_dir.join(sha256);
        if !blob_path.exists() {
            continue;
        }

        // If the blob is a symlink to a seed file, resolve and remove the
        // source file + manifest from seed/ so the trait collector stops
        // re-reporting this seed — but only if no other blob also links to
        // the same file. This prevents a label-collision retirement from
        // destroying a reseeded file (same filename, different content hash).
        if let Ok(target) = std::fs::read_link(&blob_path) {
            if target.starts_with(&seeds_dir) {
                let other_links = std::fs::read_dir(&blobs_dir)
                    .into_iter()
                    .flatten()
                    .flatten()
                    .any(|e| {
                        let p = e.path();
                        p != blob_path
                            && std::fs::read_link(&p).ok().as_ref() == Some(&target)
                    });
                if other_links {
                    info!(
                        sha256 = &sha256[..12.min(sha256.len())],
                        "seed source file still referenced by another blob, skipping delete"
                    );
                } else {
                    std::fs::remove_file(&target).ok();
                    let manifest = target.with_extension("swarm");
                    std::fs::remove_file(&manifest).ok();
                    info!(sha256 = &sha256[..12.min(sha256.len())], "removed seed source file");
                }
            }
        }

        // Move blob (or dangling symlink) to retired/.
        let dest = retired_dir.join(sha256);
        if std::fs::rename(&blob_path, &dest).is_err() {
            // For symlinks that are now dangling (source removed above),
            // just delete the link. For regular files, copy+delete.
            if blob_path.symlink_metadata().is_ok() {
                std::fs::remove_file(&blob_path).ok();
            } else if std::fs::copy(&blob_path, &dest).is_ok() {
                std::fs::remove_file(&blob_path).ok();
            }
        }

        // Remove .sources metadata.
        std::fs::remove_file(blobs_dir.join(format!("{}.sources", sha256))).ok();

        // Remove files/<label> symlinks pointing to this blob.
        if let Ok(entries) = std::fs::read_dir(&files_dir) {
            for entry in entries.flatten() {
                if let Ok(target) = std::fs::read_link(entry.path()) {
                    if target.ends_with(sha256) {
                        std::fs::remove_file(entry.path()).ok();
                    }
                }
            }
        }

        count += 1;
    }

    if count > 0 {
        info!(count, "retired swarm blobs");
    }
}

/// Move seeds rejected by the server's authorization policy into admin_rejected/.
/// Removes the .swarm manifest and blob cache symlink so the trait collector
/// stops re-reporting them. The files stay on disk for the operator to inspect.
pub fn reject_seeds(rejected: &[crate::proto::RejectedSeed]) {
    if rejected.is_empty() {
        return;
    }

    let seeds_dir = crate::platform::paths::default_seeds_dir();
    let rejected_dir = crate::platform::paths::default_rejected_dir();
    if let Err(e) = std::fs::create_dir_all(&rejected_dir) {
        warn!(error = %e, "failed to create admin_rejected directory");
        return;
    }

    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let mut count = 0;

    for item in rejected {
        let sha256 = &item.sha256;
        let blob_path = blobs_dir.join(sha256);

        // Resolve the blob symlink back to the seed file.
        if let Ok(source) = std::fs::read_link(&blob_path) {
            if source.starts_with(&seeds_dir) {
                // Move seed file to admin_rejected/.
                if let Some(filename) = source.file_name() {
                    let dest = rejected_dir.join(filename);
                    std::fs::rename(&source, &dest).ok();
                    // Remove the .swarm manifest.
                    let manifest = source.with_extension("swarm");
                    std::fs::remove_file(&manifest).ok();
                    count += 1;
                }
            }
        }

        // Remove the blob cache symlink.
        std::fs::remove_file(&blob_path).ok();
        // Remove .sources metadata.
        std::fs::remove_file(blobs_dir.join(format!("{}.sources", sha256))).ok();
    }

    if count > 0 {
        info!(count, "moved rejected seeds to admin_rejected/");
    }
}

/// Delete retired blobs older than 7 days. Called on every check-in cycle.
pub fn cleanup_retired() {
    let retired_dir = crate::platform::paths::default_retired_dir();
    if !retired_dir.exists() {
        return;
    }

    let entries = match std::fs::read_dir(&retired_dir) {
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
        info!(count, "cleaned up old retired blobs");
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

/// Evict revoked blobs from the cache, clean up files/ symlinks and .sources metadata.
/// Called after manifest merges (both check-in gossip and multicast peer sync).
pub fn evict_revoked_blobs(manifest: &manifest::Manifest) {
    let blobs_dir = crate::platform::paths::default_blobs_dir();
    let cache = match cache::BlobCache::new(blobs_dir.clone()) {
        Ok(c) => c,
        Err(_) => return,
    };
    let files_dir = blobs_dir.parent().unwrap_or(&blobs_dir).join("files");

    for sha256 in manifest.revoked_blobs() {
        if !cache.has_blob(&sha256) {
            continue;
        }
        cache.evict(&sha256);

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
        evict_revoked_blobs(manifest);
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
