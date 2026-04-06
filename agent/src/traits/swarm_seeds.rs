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
use std::fs;
use std::io::Read;
use std::path::Path;

use crate::platform::paths::{default_blobs_dir, default_seeds_dir};

#[derive(Deserialize)]
struct SwarmManifest {
    target: String,
    retention: String,
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

    let mut seeds = Vec::new();

    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }

        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };

        // Skip dotfiles and .swarm manifests.
        if name.starts_with('.') || name.ends_with(".swarm") {
            continue;
        }

        // Require a matching .swarm manifest.
        let manifest_path = dir.join(format!("{}.swarm", name));
        if !manifest_path.is_file() {
            continue;
        }

        // Parse manifest.
        let manifest: SwarmManifest = match fs::read_to_string(&manifest_path)
            .ok()
            .and_then(|s| serde_json::from_str(&s).ok())
        {
            Some(m) => m,
            None => continue,
        };

        // Compute SHA256.
        let sha256 = match hash_file(&path) {
            Ok(h) => h,
            Err(_) => continue,
        };

        // Get file size.
        let size = match fs::metadata(&path) {
            Ok(m) => m.len(),
            Err(_) => continue,
        };

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

    if seeds.is_empty() {
        return Ok(Value::Null);
    }

    Ok(json!({ "seeds": seeds }))
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
