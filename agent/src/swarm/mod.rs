// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm: peer-to-peer content distribution.
//!
//! Content-addressed blobs are downloaded from peers over mTLS HTTPS on
//! port 1531. The cache stores complete blobs and in-progress chunk
//! directories for resumable downloads.
//!
//! Discovery: server peer hints → multicast → peer exchange.

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

use crate::proto::SwarmAssignment;
use crate::state::StateStore;
use tracing::{info, warn};

use cache::BlobCache;

/// Maximum jitter before starting a swarm download (milliseconds).
/// Spreads download starts across envoys so they don't all begin on the same tick.
const MAX_DOWNLOAD_JITTER_MS: u64 = 1000;

/// Place a vigosync file in the target user's ~/vigosync/ directory.
pub(crate) fn place_vigosync_file(cache: &BlobCache, assign: &crate::proto::SwarmAssignment) {
    let user = &assign.vigosync_user;
    let filename = if assign.vigosync_filename.is_empty() {
        &assign.label
    } else {
        &assign.vigosync_filename
    };

    if user.is_empty() || filename.is_empty() {
        return;
    }

    // Resolve home directory.
    let home = match resolve_home_dir(user) {
        Some(h) => h,
        None => {
            warn!(user = user.as_str(), "vigosync: cannot resolve home directory");
            return;
        }
    };

    let sync_dir = home.join("vigosync");
    if let Err(e) = std::fs::create_dir_all(&sync_dir) {
        warn!(user = user.as_str(), error = %e, "vigosync: cannot create ~/vigosync/");
        return;
    }

    let dest = sync_dir.join(filename);

    // Read blob from cache and write to destination.
    let blob_path = cache.blob_path(&assign.sha256);
    if let Err(e) = std::fs::copy(&blob_path, &dest) {
        warn!(user = user.as_str(), file = filename.as_str(), error = %e, "vigosync: copy failed");
        return;
    }

    // Set ownership and mtime.
    #[cfg(unix)]
    {
        set_file_owner(&dest, user);
        if assign.vigosync_mtime > 0 {
            let times = [
                libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
                libc::timespec {
                    tv_sec: assign.vigosync_mtime as libc::time_t,
                    tv_nsec: 0,
                },
            ];
            let c_path = std::ffi::CString::new(dest.to_string_lossy().as_bytes()).ok();
            if let Some(p) = c_path {
                unsafe { libc::utimensat(libc::AT_FDCWD, p.as_ptr(), times.as_ptr(), 0) };
            }
        }
        // Set ownership on the sync dir too.
        set_file_owner(&sync_dir, user);
    }

    info!(
        user = user.as_str(),
        file = filename.as_str(),
        sha256 = &assign.sha256[..12.min(assign.sha256.len())],
        "vigosync: file placed"
    );
}

/// Resolve a username to their home directory.
#[cfg(unix)]
fn resolve_home_dir(username: &str) -> Option<std::path::PathBuf> {
    let passwd = std::fs::read_to_string("/etc/passwd").ok()?;
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() >= 6 && fields[0] == username {
            return Some(std::path::PathBuf::from(fields[5]));
        }
    }
    None
}

#[cfg(windows)]
fn resolve_home_dir(username: &str) -> Option<std::path::PathBuf> {
    let path = std::path::PathBuf::from(format!(r"C:\Users\{}", username));
    if path.is_dir() { Some(path) } else { None }
}

/// Set file ownership to a user (best-effort).
#[cfg(unix)]
fn set_file_owner(path: &std::path::Path, username: &str) {
    use std::ffi::CString;
    let c_name = match CString::new(username) {
        Ok(n) => n,
        Err(_) => return,
    };
    unsafe {
        let pw = libc::getpwnam(c_name.as_ptr());
        if !pw.is_null() {
            let c_path = match CString::new(path.to_string_lossy().as_bytes()) {
                Ok(p) => p,
                Err(_) => return,
            };
            libc::chown(c_path.as_ptr(), (*pw).pw_uid, (*pw).pw_gid);
        }
    }
}

/// Record retention timestamps for swarm assignments that have been cached.
/// Called from the main check-in loop (not spawned) after assignments are processed.
pub fn record_retention(assignments: &[SwarmAssignment], state: &StateStore) {
    let cache = match BlobCache::new(crate::platform::paths::default_blobs_dir()) {
        Ok(c) => c,
        Err(_) => return,
    };
    for assign in assignments {
        if assign.retention_seconds == 0 || assign.sha256.is_empty() {
            continue;
        }
        // Only record if the blob is actually cached (downloaded or was already present).
        if cache.has_blob(&assign.sha256) {
            if let Ok(mut wtxn) = state.write_txn() {
                let _ = state.swarm_retention.record(&mut wtxn, &assign.sha256, assign.retention_seconds);
                let _ = wtxn.commit();
            }
        }
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
        // re-reporting this seed.
        if let Ok(target) = std::fs::read_link(&blob_path) {
            if target.starts_with(&seeds_dir) {
                std::fs::remove_file(&target).ok();
                // Remove the .swarm manifest alongside it.
                let manifest = target.with_extension("swarm");
                std::fs::remove_file(&manifest).ok();
                info!(sha256 = &sha256[..12.min(sha256.len())], "removed seed source file");
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
