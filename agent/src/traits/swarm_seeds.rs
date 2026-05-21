// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Reports seed files available for swarm distribution.
//!
//! Scans `/var/lib/vigo/swarm/seed/` for files with a matching `.swarm`
//! JSON manifest. Computes SHA256, reads size, and includes target/retention
//! from the manifest. Also symlinks seed files into the blob cache so the
//! peer server can serve them.
//!
//! Trait namespace: `swarm_seeds`

use anyhow::Result;
use serde::Deserialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::fs;
use std::io::Read;
use std::path::Path;
use std::sync::Mutex;

use crate::platform::paths::{default_blobs_dir, default_graveyard_dir, default_seeds_dir};

#[derive(Deserialize)]
struct SwarmManifest {
    target: String,
    retention: String,
}

/// SHA256 cache map keyed by (filename, size, mtime_secs) → hash.
type HashCacheMap = HashMap<(String, u64, u64), String>;

/// Cached SHA256 keyed by (filename, size, mtime_secs). Seeds don't change
/// after creation, so recomputing the hash every cycle is wasteful for large files.
/// Persisted to disk so cold starts (vigo run) don't rehash multi-GiB files.
static HASH_CACHE: Mutex<Option<HashCacheMap>> = Mutex::new(None);

/// Disk path for the persistent hash cache.
fn hash_cache_path() -> std::path::PathBuf {
    default_blobs_dir().parent().unwrap_or(std::path::Path::new("/var/lib/vigo/swarm")).join(".hash_cache.json")
}

pub fn collect() -> Result<Value> {
    let dir = default_seeds_dir();
    if !dir.exists() {
        return Ok(Value::Null);
    }

    let entries = match fs::read_dir(&dir) {
        Ok(e) => e,
        Err(_) => return Ok(Value::Null),
    };

    let graveyard_dir = default_graveyard_dir();
    let mut seeds = Vec::new();
    let mut broken_seeds = Vec::new();

    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }

        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };

        // Skip dotfiles, .swarm manifests, and the admin_rejected subdirectory.
        if name.starts_with('.') || name.ends_with(".swarm") || name == "admin_rejected" {
            continue;
        }

        // Require a matching .swarm manifest.
        let manifest_path = dir.join(format!("{}.swarm", name));
        if !manifest_path.is_file() {
            broken_seeds.push(json!({
                "filename": name,
                "error": "missing .swarm manifest",
            }));
            continue;
        }

        // Parse manifest.
        let manifest_str = match fs::read_to_string(&manifest_path) {
            Ok(s) => s,
            Err(e) => {
                broken_seeds.push(json!({
                    "filename": name,
                    "error": format!("reading manifest: {}", e),
                }));
                continue;
            }
        };
        let manifest: SwarmManifest = match serde_json::from_str(&manifest_str) {
            Ok(m) => m,
            Err(e) => {
                broken_seeds.push(json!({
                    "filename": name,
                    "error": format!("invalid manifest: {}", e),
                }));
                continue;
            }
        };

        // Get file metadata for size and cache key.
        let meta = match fs::metadata(&path) {
            Ok(m) => m,
            Err(e) => {
                broken_seeds.push(json!({
                    "filename": name,
                    "error": format!("reading metadata: {}", e),
                }));
                continue;
            }
        };
        let size = meta.len();
        let mtime_secs = meta.modified()
            .ok()
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_secs())
            .unwrap_or(0);

        // Compute SHA256 (cached by filename+size+mtime since seeds don't change).
        let sha256 = match cached_hash(&name, size, mtime_secs, &path) {
            Ok(h) => h,
            Err(e) => {
                broken_seeds.push(json!({
                    "filename": name,
                    "error": format!("hashing file: {}", e),
                }));
                continue;
            }
        };

        // Skip seeds that have been revoked — unless the seed file is newer
        // than the graveyard marker, meaning it was re-distributed after revocation.
        let graveyard_path = graveyard_dir.join(&sha256);
        if graveyard_path.symlink_metadata().is_ok() {
            let graveyard_mtime = graveyard_path.symlink_metadata().ok()
                .and_then(|m| m.modified().ok())
                .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                .map(|d| d.as_secs())
                .unwrap_or(0);
            if mtime_secs > graveyard_mtime {
                // Seed was re-distributed after revocation — clear stale marker.
                std::fs::remove_file(&graveyard_path).ok();
            } else {
                continue;
            }
        }

        // Symlink into blob cache so the peer server can serve it.
        symlink_into_cache(&sha256, &path, &name);

        seeds.push(json!({
            "sha256": sha256,
            "label": name,
            "size": size,
            "target": manifest.target,
            "retention": manifest.retention,
        }));
    }

    if seeds.is_empty() && broken_seeds.is_empty() {
        return Ok(Value::Null);
    }

    let mut result = json!({});
    if !seeds.is_empty() {
        result["seeds"] = json!(seeds);
    }
    if !broken_seeds.is_empty() {
        result["broken_seeds"] = json!(broken_seeds);
    }
    Ok(result)
}

/// Return cached SHA256 or compute and cache it. Cache key is (filename, size, mtime)
/// so the hash is only recomputed when the file changes.
fn cached_hash(name: &str, size: u64, mtime_secs: u64, path: &Path) -> Result<String> {
    let key = (name.to_string(), size, mtime_secs);
    let mut guard = HASH_CACHE.lock().unwrap_or_else(|e| e.into_inner());
    let cache = guard.get_or_insert_with(load_hash_cache_from_disk);
    if let Some(hash) = cache.get(&key) {
        return Ok(hash.clone());
    }
    let hash = hash_file(path)?;
    cache.insert(key, hash.clone());
    save_hash_cache_to_disk(cache);
    Ok(hash)
}

/// Load the hash cache from disk. Returns empty map if the file doesn't exist or is corrupt.
fn load_hash_cache_from_disk() -> HashMap<(String, u64, u64), String> {
    let path = hash_cache_path();
    let data = match fs::read_to_string(&path) {
        Ok(d) => d,
        Err(_) => return HashMap::new(),
    };
    // On-disk format: JSON array of [name, size, mtime, sha256] tuples.
    let entries: Vec<(String, u64, u64, String)> = match serde_json::from_str(&data) {
        Ok(e) => e,
        Err(_) => return HashMap::new(),
    };
    entries.into_iter().map(|(n, s, m, h)| ((n, s, m), h)).collect()
}

/// Persist the hash cache to disk.
fn save_hash_cache_to_disk(cache: &HashMap<(String, u64, u64), String>) {
    let entries: Vec<(&String, &u64, &u64, &String)> = cache
        .iter()
        .map(|((n, s, m), h)| (n, s, m, h))
        .collect();
    if let Ok(json) = serde_json::to_string(&entries) {
        fs::write(hash_cache_path(), json).ok();
    }
}

/// Compute SHA256 of a file.
fn hash_file(path: &Path) -> Result<String> {
    let mut f = fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(hasher.finalize()))
}

/// Create symlinks so the peer server and admin can find the seed file.
fn symlink_into_cache(sha256: &str, seed_path: &Path, label: &str) {
    let blobs_dir = default_blobs_dir();
    let _ = fs::create_dir_all(&blobs_dir);

    // .blobs/<sha256> → seed/<filename>
    let blob_link = blobs_dir.join(sha256);
    if !blob_link.exists() {
        #[cfg(unix)]
        { std::os::unix::fs::symlink(seed_path, &blob_link).ok(); }
        #[cfg(windows)]
        { std::os::windows::fs::symlink_file(seed_path, &blob_link).ok(); }
    }

    // files/<label> → .blobs/<sha256>
    let files_dir = blobs_dir.parent().unwrap_or(&blobs_dir).join("files");
    let _ = fs::create_dir_all(&files_dir);
    let file_link = files_dir.join(label);
    if file_link.exists() || file_link.symlink_metadata().is_ok() {
        fs::remove_file(&file_link).ok();
    }
    #[cfg(unix)]
    { std::os::unix::fs::symlink(&blob_link, &file_link).ok(); }
    #[cfg(windows)]
    { std::os::windows::fs::symlink_file(&blob_link, &file_link).ok(); }
}
