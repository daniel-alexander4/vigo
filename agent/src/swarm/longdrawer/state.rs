// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Per-user longdrawer state: file snapshot + tombstones, persisted as JSON.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

/// A file currently present in a user's ~/longdrawer/ directory.
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

/// Per-user longdrawer state — files currently present plus deletion tombstones.
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
        let json = serde_json::to_string_pretty(self).context("serializing longdrawer state")?;
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

    /// Compute a deterministic digest of `(files, tombstones)`. Used to
    /// drive the digest-only announcement model: peers include the digest
    /// on every multicast tick, and receivers pull the full state via
    /// mTLS only when the digest changes. Matches cryptsync's approach
    /// (sha256 → 16-byte truncation → hex) — keeps the wire footprint
    /// small (~32 hex chars) and prevents leaking file paths to anyone
    /// on the LAN. Iteration is ordered because both maps are BTreeMap.
    pub fn state_digest(&self) -> String {
        use sha2::{Digest, Sha256};
        let mut h = Sha256::new();
        for (path, fe) in &self.files {
            h.update(path.as_bytes());
            h.update(b"|");
            h.update(fe.sha256.as_bytes());
            h.update(b"|");
            h.update(fe.mtime.to_le_bytes());
            h.update(b"|");
            h.update(fe.size.to_le_bytes());
            h.update(b"\n");
        }
        h.update(b"--tombstones--\n");
        for (path, ts) in &self.tombstones {
            h.update(path.as_bytes());
            h.update(b"|");
            h.update(ts.deletion_time.to_le_bytes());
            h.update(b"\n");
        }
        let full = h.finalize();
        hex::encode(&full[..16])
    }
}

/// Digest-only multicast announcement. Carries everything a peer needs to
/// decide whether to fetch full state, but **no file paths, hashes, or
/// timestamps** — so the packet reveals nothing about user content to
/// observers on the LAN.
///
/// Model is identical to cryptsync (`cryptsync/state.rs::Announcement`): a peer's
/// announcement tells receivers who this machine is, how to reach it, and
/// a short digest of the state. Receivers compare the digest against the
/// last one they observed from this peer; on mismatch, they pull
/// `GET /longdrawer/state/{user}` from the announcing peer via mTLS and hand
/// the full `UserState` to the reconciler.
///
/// The previous wire format (full `UserState` embedded in the packet)
/// leaked every user's directory listing to anyone on the broadcast
/// domain. This struct replaces it; 0.30.11 agents speak only this form.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Announcement {
    /// Stable machine identifier (hostname).
    pub machine: String,
    /// Per-user subject of this announcement.
    pub user: String,
    /// Unix seconds when the announcement was built.
    pub emitted_at: i64,
    /// Port for fetching content + full state (swarm peer mTLS server).
    pub port: u16,
    /// Monotonic counter bumped on every state mutation — lets receivers
    /// cheaply detect replay / reordered packets on noisy multicast.
    #[serde(default)]
    pub generation: u64,
    /// Hex-encoded truncated SHA256 over `(files, tombstones)`. Changes
    /// on every file mutation; receivers compare against the last
    /// observed value per-peer and pull full state on mismatch.
    pub state_digest: String,
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
