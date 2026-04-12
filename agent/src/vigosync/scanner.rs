// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Scan users' `~/vigosync/` directories, compute file entries, and detect
//! deletions by diffing against the previous snapshot.

use anyhow::Result;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::SystemTime;

use super::state::{FileEntry, Tombstone, UserState};

/// Maximum file size for vigosync (100 MB). Files larger than this are skipped.
pub const MAX_FILE_SIZE: u64 = 100 * 1024 * 1024;

/// List (username, home_dir) pairs for real users on this machine.
#[cfg(unix)]
pub fn list_home_dirs() -> Vec<(String, PathBuf)> {
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
pub fn list_home_dirs() -> Vec<(String, PathBuf)> {
    let users_dir = PathBuf::from(r"C:\Users");
    let entries = match fs::read_dir(&users_dir) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };
    let mut results = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();
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

/// Scan a single user's ~/vigosync/ directory. Returns a filename → FileEntry map.
/// Non-recursive; only regular files; dotfiles skipped; files > MAX_FILE_SIZE skipped.
pub fn scan_user_dir(sync_dir: &Path) -> BTreeMap<String, FileEntry> {
    let mut out = BTreeMap::new();
    let entries = match fs::read_dir(sync_dir) {
        Ok(e) => e,
        Err(_) => return out,
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
        if name.starts_with('.') {
            continue;
        }
        let meta = match fs::metadata(&path) {
            Ok(m) => m,
            Err(_) => continue,
        };
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
        out.insert(
            name,
            FileEntry { sha256, mtime, size: meta.len() },
        );
    }
    out
}

/// Hash a file with SHA256 → lowercase hex.
pub fn hash_file(path: &Path) -> Result<String> {
    let mut f = fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 64 * 1024];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(hasher.finalize()))
}

/// Compare a fresh scan against the previous snapshot and update the state
/// in place. Detects additions, modifications, deletions, and resurrections
/// (files that had an active tombstone but reappeared).
///
/// `now` is wall-clock time in unix seconds; used as the deletion timestamp
/// for files present in `previous` but missing from `current`.
pub fn merge_scan(
    state: &mut UserState,
    current: BTreeMap<String, FileEntry>,
    previous: &BTreeMap<String, FileEntry>,
    now: i64,
) {
    // Detect deletions: names in previous not in current → new tombstone.
    for name in previous.keys() {
        if !current.contains_key(name) {
            state.tombstones.insert(
                name.clone(),
                Tombstone { deletion_time: now },
            );
        }
    }

    // For files currently present, drop any stale tombstones (resurrection)
    // and update the file map.
    for (name, entry) in &current {
        state.tombstones.remove(name);
        state.files.insert(name.clone(), entry.clone());
    }

    // Remove stale entries from the file map (deleted files).
    state.files.retain(|name, _| current.contains_key(name));
}

/// Current wall-clock time as unix seconds.
pub fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scan_empty_dir_returns_empty() {
        let dir = tempfile::tempdir().unwrap();
        let out = scan_user_dir(dir.path());
        assert!(out.is_empty());
    }

    #[test]
    fn scan_skips_dotfiles_and_subdirs() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(dir.path().join("notes.md"), b"hello").unwrap();
        fs::write(dir.path().join(".hidden"), b"x").unwrap();
        fs::create_dir(dir.path().join("sub")).unwrap();
        let out = scan_user_dir(dir.path());
        assert_eq!(out.len(), 1);
        assert!(out.contains_key("notes.md"));
    }

    #[test]
    fn merge_scan_tombstones_deletions() {
        let mut state = UserState::empty("dan");
        let mut previous = BTreeMap::new();
        previous.insert(
            "gone.txt".to_string(),
            FileEntry { sha256: "a".into(), mtime: 10, size: 1 },
        );
        state.files = previous.clone();

        let current = BTreeMap::new();
        merge_scan(&mut state, current, &previous, 100);

        assert!(state.files.is_empty());
        assert_eq!(state.tombstones.get("gone.txt").unwrap().deletion_time, 100);
    }

    #[test]
    fn merge_scan_resurrection_clears_tombstone() {
        let mut state = UserState::empty("dan");
        state.tombstones.insert(
            "notes.md".into(),
            Tombstone { deletion_time: 50 },
        );
        let previous = BTreeMap::new();
        let mut current = BTreeMap::new();
        current.insert(
            "notes.md".to_string(),
            FileEntry { sha256: "b".into(), mtime: 100, size: 2 },
        );
        merge_scan(&mut state, current, &previous, 200);
        assert!(!state.tombstones.contains_key("notes.md"));
        assert!(state.files.contains_key("notes.md"));
    }
}
