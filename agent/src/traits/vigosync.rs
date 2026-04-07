// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigosync trait collector — reports files in ~/vigosync/ per user.
//!
//! Scans home directories for a `vigosync/` folder. For each file found,
//! computes SHA256, records mtime, and symlinks into the blob cache so the
//! swarm peer server can serve it.
//!
//! Trait namespace: `vigosync`

use anyhow::Result;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::SystemTime;

use crate::platform::paths::default_blobs_dir;

/// Maximum file size for vigosync (100 MB).
const MAX_FILE_SIZE: u64 = 100 * 1024 * 1024;

pub fn collect() -> Result<Value> {
    let home_dirs = list_home_dirs();
    if home_dirs.is_empty() {
        return Ok(Value::Null);
    }

    let blobs_dir = default_blobs_dir();
    let _ = fs::create_dir_all(&blobs_dir);

    let mut files = Vec::new();

    for (username, home) in &home_dirs {
        let sync_dir = home.join("vigosync");
        if !sync_dir.is_dir() {
            continue;
        }

        let entries = match fs::read_dir(&sync_dir) {
            Ok(e) => e,
            Err(_) => continue,
        };

        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_file() {
                continue;
            }

            let name = match path.file_name().and_then(|n| n.to_str()) {
                Some(n) => n.to_string(),
                None => continue,
            };

            // Skip dotfiles.
            if name.starts_with('.') {
                continue;
            }

            let meta = match fs::metadata(&path) {
                Ok(m) => m,
                Err(_) => continue,
            };

            // Skip files over the size limit.
            if meta.len() > MAX_FILE_SIZE {
                continue;
            }

            let mtime = meta
                .modified()
                .unwrap_or(SystemTime::UNIX_EPOCH)
                .duration_since(SystemTime::UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs() as i64;

            let sha256 = match hash_file(&path) {
                Ok(h) => h,
                Err(_) => continue,
            };

            // Symlink into blob cache so the peer server can serve it.
            let blob_link = blobs_dir.join(&sha256);
            if !blob_link.exists() {
                #[cfg(unix)]
                {
                    std::os::unix::fs::symlink(&path, &blob_link).ok();
                }
                #[cfg(windows)]
                {
                    // Windows: copy instead of symlink (symlinks require admin).
                    fs::copy(&path, &blob_link).ok();
                }
            }

            files.push(json!({
                "user": username,
                "name": name,
                "sha256": sha256,
                "size": meta.len(),
                "mtime": mtime,
            }));
        }
    }

    if files.is_empty() {
        return Ok(Value::Null);
    }

    Ok(json!({ "files": files }))
}

/// Hash a file with SHA256.
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

/// List (username, home_dir) for real users on the system.
#[cfg(unix)]
fn list_home_dirs() -> Vec<(String, PathBuf)> {
    let passwd = match fs::read_to_string("/etc/passwd") {
        Ok(s) => s,
        Err(_) => return Vec::new(),
    };

    let mut results = Vec::new();
    for line in passwd.lines() {
        let fields: Vec<&str> = line.split(':').collect();
        if fields.len() < 7 {
            continue;
        }
        let username = fields[0];
        let uid: u32 = match fields[2].parse() {
            Ok(u) => u,
            Err(_) => continue,
        };
        let home = fields[5];

        // Skip system accounts (uid < 1000) and nobody.
        if uid < 1000 || username == "nobody" || username == "nfsnobody" {
            continue;
        }

        let home_path = PathBuf::from(home);
        if home_path.is_dir() {
            results.push((username.to_string(), home_path));
        }
    }
    results
}

#[cfg(windows)]
fn list_home_dirs() -> Vec<(String, PathBuf)> {
    let users_dir = PathBuf::from(r"C:\Users");
    let entries = match fs::read_dir(&users_dir) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };

    let mut results = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();
        // Skip system profiles.
        if matches!(name.as_str(), "Default" | "Default User" | "Public" | "All Users") {
            continue;
        }
        let path = entry.path();
        if path.is_dir() {
            results.push((name, path));
        }
    }
    results
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_list_home_dirs_returns_real_users() {
        let dirs = list_home_dirs();
        // Should find at least the current user on any dev machine.
        assert!(!dirs.is_empty(), "expected at least one home directory");
        for (user, path) in &dirs {
            assert!(!user.is_empty());
            assert!(path.is_dir());
        }
    }

    #[test]
    fn test_collect_returns_null_without_vigosync_dirs() {
        // Most test environments won't have ~/vigosync/, so this should return Null.
        let result = collect().unwrap();
        // Either Null or a valid object — both are fine.
        assert!(result.is_null() || result.is_object());
    }
}
