// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user echo state: file snapshot + tombstones, persisted as JSON.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

/// A file currently present in a user's ~/echo/ directory.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FileEntry {
    pub sha256: String,
    /// File mtime as unix seconds.
    pub mtime: i64,
    pub size: u64,
}

/// A deletion record. Compared against peer file mtimes during reconciliation:
/// if `deletion_time > peer_mtime`, the peer's file is deleted.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Tombstone {
    /// When the deletion was observed (unix seconds).
    pub deletion_time: i64,
}

/// Per-user echo state — files currently present plus deletion tombstones.
///
/// Uses BTreeMap for stable serialization order so the sha256 of the serialized
/// state is deterministic across machines.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserState {
    pub user: String,
    pub files: BTreeMap<String, FileEntry>,
    pub tombstones: BTreeMap<String, Tombstone>,
}

impl UserState {
    pub fn empty(user: &str) -> Self {
        Self {
            user: user.to_string(),
            files: BTreeMap::new(),
            tombstones: BTreeMap::new(),
        }
    }

    /// Load a user's state from disk, or return empty state if missing.
    pub fn load(dir: &Path, user: &str) -> Self {
        let path = Self::state_path(dir, user);
        if !path.is_file() {
            return Self::empty(user);
        }
        match fs::read_to_string(&path) {
            Ok(json) => serde_json::from_str(&json).unwrap_or_else(|_| Self::empty(user)),
            Err(_) => Self::empty(user),
        }
    }

    pub fn save(&self, dir: &Path) -> Result<()> {
        fs::create_dir_all(dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = Self::state_path(dir, &self.user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).context("serializing echo state")?;
        fs::write(&tmp, json).with_context(|| format!("writing {}", tmp.display()))?;
        fs::rename(&tmp, &path).with_context(|| format!("renaming to {}", path.display()))?;
        Ok(())
    }

    fn state_path(dir: &Path, user: &str) -> PathBuf {
        dir.join(format!("{}.json", user))
    }

    /// Drop tombstones older than `max_age_secs`.
    pub fn gc_tombstones(&mut self, now: i64, max_age_secs: i64) {
        let cutoff = now - max_age_secs;
        self.tombstones.retain(|_, t| t.deletion_time >= cutoff);
    }
}

/// Full announcement payload: one machine's view of one user's state.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Announcement {
    /// Stable machine identifier (hostname).
    pub machine: String,
    /// Unix seconds when the announcement was built.
    pub emitted_at: i64,
    /// Port for fetching file content (usually the swarm peer server).
    pub port: u16,
    #[serde(flatten)]
    pub state: UserState,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_empty_state() {
        let dir = tempfile::tempdir().unwrap();
        let s = UserState::empty("dan");
        s.save(dir.path()).unwrap();
        let loaded = UserState::load(dir.path(), "dan");
        assert_eq!(loaded.user, "dan");
        assert!(loaded.files.is_empty());
        assert!(loaded.tombstones.is_empty());
    }

    #[test]
    fn roundtrip_populated() {
        let dir = tempfile::tempdir().unwrap();
        let mut s = UserState::empty("dan");
        s.files.insert(
            "notes.md".into(),
            FileEntry { sha256: "a".repeat(64), mtime: 1000, size: 42 },
        );
        s.tombstones.insert(
            "old.txt".into(),
            Tombstone { deletion_time: 999 },
        );
        s.save(dir.path()).unwrap();
        let loaded = UserState::load(dir.path(), "dan");
        assert_eq!(loaded.files.len(), 1);
        assert_eq!(loaded.tombstones.len(), 1);
        assert_eq!(loaded.files.get("notes.md").unwrap().mtime, 1000);
    }

    #[test]
    fn gc_tombstones_drops_old_entries() {
        let mut s = UserState::empty("dan");
        s.tombstones.insert("recent.txt".into(), Tombstone { deletion_time: 100 });
        s.tombstones.insert("old.txt".into(), Tombstone { deletion_time: 1 });
        s.gc_tombstones(101, 50); // cutoff = 51
        assert!(s.tombstones.contains_key("recent.txt"));
        assert!(!s.tombstones.contains_key("old.txt"));
    }
}
