// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Scan users' `~/filesync/` directories, compute file entries, and detect
//! deletions by diffing against the previous snapshot.

use anyhow::Result;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::SystemTime;

use super::state::{FileEntry, Tombstone, UserState};

/// Maximum file size for filesync (2 GB). Files larger than this are skipped.
///
/// Sized for the "personal files carried between machines" use case — configs,
/// notes, scripts, small datasets, VM images up to this bound. Receivers stream
/// content directly to disk (no full-RAM buffering), so the limit is about
/// what's reasonable to move over a LAN on a 30-second announce cadence rather
/// than a RAM ceiling.
pub const MAX_FILE_SIZE: u64 = 2 * 1024 * 1024 * 1024;

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

/// Scan a single user's ~/filesync/ directory. Returns a filename → FileEntry map.
///
/// Non-recursive; only regular files; dotfiles skipped; files > MAX_FILE_SIZE skipped.
///
/// `previous` is the prior scan's snapshot for this user. If a file's
/// `(name, size, mtime)` triple matches the previous snapshot exactly, the
/// previous sha256 is reused without re-reading the file. This makes scans of
/// a stable directory effectively free regardless of total content size.
pub fn scan_user_dir(
    sync_dir: &Path,
    previous: &BTreeMap<String, FileEntry>,
) -> BTreeMap<String, FileEntry> {
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

        // Reuse cached sha256 if (size, mtime) is unchanged from the prior scan.
        let size = meta.len();
        let sha256 = match previous.get(&name) {
            Some(prev) if prev.size == size && prev.mtime == mtime => prev.sha256.clone(),
            _ => match hash_file(&path) {
                Ok(h) => h,
                Err(_) => continue,
            },
        };

        out.insert(name, FileEntry { sha256, mtime, size });
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
///
/// `dir` is the user's `~/filesync/` directory — used to bump on-disk mtime
/// when a previously-tombstoned file reappears. Without that bump, a file
/// re-added via `mv` keeps its preserved (older) mtime, which loses the
/// reconcile vs any peer's tombstone with `deletion_time > preserved_mtime`
/// and the file gets re-deleted on the next sync round.
pub fn merge_scan(
    state: &mut UserState,
    mut current: BTreeMap<String, FileEntry>,
    previous: &BTreeMap<String, FileEntry>,
    now: i64,
    dir: &Path,
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
    // and update the file map. On resurrection, bump the on-disk mtime to
    // `now` so peers' tombstones can't out-rank the reappearance.
    for (name, entry) in current.iter_mut() {
        if state.tombstones.remove(name).is_some() {
            // Resurrection: a previously-tombstoned file is back. Bump
            // its on-disk mtime; if that succeeds, mirror the bump in
            // the in-memory FileEntry so the next announce digest
            // reflects it on this same cycle.
            if super::applier::set_mtime(&dir.join(name), now) {
                entry.mtime = now;
            }
        }
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
        let out = scan_user_dir(dir.path(), &BTreeMap::new());
        assert!(out.is_empty());
    }

    #[test]
    fn scan_skips_dotfiles_and_subdirs() {
        let dir = tempfile::tempdir().unwrap();
        fs::write(dir.path().join("notes.md"), b"hello").unwrap();
        fs::write(dir.path().join(".hidden"), b"x").unwrap();
        fs::create_dir(dir.path().join("sub")).unwrap();
        let out = scan_user_dir(dir.path(), &BTreeMap::new());
        assert_eq!(out.len(), 1);
        assert!(out.contains_key("notes.md"));
    }

    #[test]
    fn scan_reuses_cached_sha256_when_size_and_mtime_match() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("notes.md");
        fs::write(&path, b"hello").unwrap();
        let meta = fs::metadata(&path).unwrap();
        let mtime = meta
            .modified()
            .unwrap()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64;

        // Seed a previous snapshot with a bogus sha256 but matching size/mtime.
        let mut previous = BTreeMap::new();
        previous.insert(
            "notes.md".to_string(),
            FileEntry {
                sha256: "cached-value-never-re-hashed".to_string(),
                mtime,
                size: 5,
            },
        );

        let out = scan_user_dir(dir.path(), &previous);
        // The bogus sha256 is carried through because (size, mtime) matched.
        assert_eq!(out.get("notes.md").unwrap().sha256, "cached-value-never-re-hashed");
    }

    #[test]
    fn scan_rehashes_when_size_changes() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("notes.md");
        fs::write(&path, b"hello").unwrap();
        let meta = fs::metadata(&path).unwrap();
        let mtime = meta
            .modified()
            .unwrap()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64;

        let mut previous = BTreeMap::new();
        previous.insert(
            "notes.md".to_string(),
            FileEntry {
                sha256: "stale".to_string(),
                mtime,
                size: 999, // wrong size → must re-hash
            },
        );

        let out = scan_user_dir(dir.path(), &previous);
        assert_ne!(out.get("notes.md").unwrap().sha256, "stale");
    }

    #[test]
    fn merge_scan_tombstones_deletions() {
        let dir = tempfile::tempdir().unwrap();
        let mut state = UserState::empty("dan");
        let mut previous = BTreeMap::new();
        previous.insert(
            "gone.txt".to_string(),
            FileEntry { sha256: "a".into(), mtime: 10, size: 1 },
        );
        state.files = previous.clone();

        let current = BTreeMap::new();
        merge_scan(&mut state, current, &previous, 100, dir.path());

        assert!(state.files.is_empty());
        assert_eq!(state.tombstones.get("gone.txt").unwrap().deletion_time, 100);
    }

    #[test]
    fn merge_scan_resurrection_clears_tombstone_and_bumps_mtime() {
        // Set up a real directory + file so set_mtime has a target.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("notes.md");
        fs::write(&path, b"hello").unwrap();
        // Force an "old" on-disk mtime to simulate a `mv`-preserved file.
        let _ = super::super::applier::set_mtime(&path, 100);
        let on_disk_mtime = path.metadata().unwrap()
            .modified().unwrap()
            .duration_since(SystemTime::UNIX_EPOCH).unwrap()
            .as_secs() as i64;
        assert_eq!(on_disk_mtime, 100, "set_mtime fixture must succeed");

        let mut state = UserState::empty("dan");
        // Prior cycle had a tombstone with deletion_time=200 (newer than
        // the file's preserved mtime). Without the resurrection bump,
        // any peer with this tombstone would re-delete the file.
        state.tombstones.insert(
            "notes.md".into(),
            Tombstone { deletion_time: 200 },
        );
        let previous = BTreeMap::new();
        let mut current = BTreeMap::new();
        current.insert(
            "notes.md".to_string(),
            FileEntry { sha256: "b".into(), mtime: 100, size: 5 },
        );

        merge_scan(&mut state, current, &previous, 500, dir.path());

        // Tombstone dropped; file present.
        assert!(!state.tombstones.contains_key("notes.md"));
        assert!(state.files.contains_key("notes.md"));
        // In-memory FileEntry mtime bumped to `now`.
        assert_eq!(state.files["notes.md"].mtime, 500);
        // On-disk mtime bumped to `now` so the next stat returns 500.
        let post_mtime = path.metadata().unwrap()
            .modified().unwrap()
            .duration_since(SystemTime::UNIX_EPOCH).unwrap()
            .as_secs() as i64;
        assert_eq!(post_mtime, 500);
    }

    #[test]
    fn merge_scan_addition_without_tombstone_does_not_bump_mtime() {
        // Adding a fresh file (no prior tombstone) must NOT bump mtime.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("fresh.md");
        fs::write(&path, b"hello").unwrap();
        let _ = super::super::applier::set_mtime(&path, 100);

        let mut state = UserState::empty("dan");
        let previous = BTreeMap::new();
        let mut current = BTreeMap::new();
        current.insert(
            "fresh.md".to_string(),
            FileEntry { sha256: "x".into(), mtime: 100, size: 5 },
        );

        merge_scan(&mut state, current, &previous, 500, dir.path());

        assert_eq!(state.files["fresh.md"].mtime, 100, "no tombstone → no bump");
        let post_mtime = path.metadata().unwrap()
            .modified().unwrap()
            .duration_since(SystemTime::UNIX_EPOCH).unwrap()
            .as_secs() as i64;
        assert_eq!(post_mtime, 100, "no tombstone → on-disk mtime untouched");
    }
}
