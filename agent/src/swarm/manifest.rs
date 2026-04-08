// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm manifest: a LWW-Map CRDT tracking blob lifecycle.
//!
//! Each blob has an entry with status (active/revoked), a timestamp for
//! LWW ordering, and the author who made the change. The manifest is
//! gossipped between envoys — merges are conflict-free by construction.
//!
//! Merge rule:
//! 1. Higher `updated_at` wins
//! 2. Tie: revoked wins over active (safety)
//! 3. Tie + same status: higher `updated_by` string wins (deterministic)
//!
//! Persistence: JSON file at `/var/lib/vigo/swarm/manifest.json`.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, RwLock};
use std::time::{SystemTime, UNIX_EPOCH};
use tracing::{info, warn};

/// Blob status in the manifest.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum BlobStatus {
    Active,
    Revoked,
}

/// A single blob entry in the manifest.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BlobEntry {
    pub sha256: String,
    pub label: String,
    pub target: String,
    pub retention: String,
    pub seeder_id: String,
    pub size: u64,
    pub status: BlobStatus,
    /// Unix milliseconds — used for LWW ordering.
    pub updated_at: u64,
    /// Envoy ID or "server" — who made this change.
    pub updated_by: String,
}

/// The manifest — a LWW-Map of blob entries.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ManifestData {
    /// Lamport clock — incremented on every local mutation.
    pub version: u64,
    /// SHA256 → BlobEntry.
    pub entries: HashMap<String, BlobEntry>,
}

impl ManifestData {
    pub fn new() -> Self {
        Self {
            version: 0,
            entries: HashMap::new(),
        }
    }
}

/// Thread-safe manifest with persistence.
pub struct Manifest {
    data: RwLock<ManifestData>,
    path: PathBuf,
}

impl Manifest {
    /// Load manifest from disk, or create empty if not found.
    pub fn load(dir: &Path) -> Arc<Self> {
        let path = dir.join("manifest.json");
        let data = if path.is_file() {
            match std::fs::read_to_string(&path) {
                Ok(json) => match serde_json::from_str::<ManifestData>(&json) {
                    Ok(m) => {
                        info!(version = m.version, entries = m.entries.len(), "loaded swarm manifest");
                        m
                    }
                    Err(e) => {
                        warn!(error = %e, "corrupt manifest, starting fresh");
                        ManifestData::new()
                    }
                },
                Err(e) => {
                    warn!(error = %e, "reading manifest, starting fresh");
                    ManifestData::new()
                }
            }
        } else {
            ManifestData::new()
        };

        Arc::new(Self {
            data: RwLock::new(data),
            path,
        })
    }

    /// Current manifest version (Lamport clock).
    pub fn version(&self) -> u64 {
        self.data.read().unwrap_or_else(|e| e.into_inner()).version
    }

    /// Check if a blob is active (exists and not revoked).
    pub fn is_active(&self, sha256: &str) -> bool {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        matches!(data.entries.get(sha256), Some(e) if e.status == BlobStatus::Active)
    }

    /// Check if a blob is revoked.
    pub fn is_revoked(&self, sha256: &str) -> bool {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        matches!(data.entries.get(sha256), Some(e) if e.status == BlobStatus::Revoked)
    }

    /// Record a new seed (active blob).
    pub fn record_seed(
        &self,
        sha256: &str,
        label: &str,
        target: &str,
        retention: &str,
        seeder_id: &str,
        size: u64,
    ) {
        let mut data = self.data.write().unwrap_or_else(|e| e.into_inner());

        // Don't overwrite a revocation with a seed from the same timestamp
        // window — the revocation should win.
        if let Some(existing) = data.entries.get(sha256) {
            if existing.status == BlobStatus::Revoked {
                return;
            }
        }

        let now = now_millis();
        data.entries.insert(sha256.to_string(), BlobEntry {
            sha256: sha256.to_string(),
            label: label.to_string(),
            target: target.to_string(),
            retention: retention.to_string(),
            seeder_id: seeder_id.to_string(),
            size,
            status: BlobStatus::Active,
            updated_at: now,
            updated_by: seeder_id.to_string(),
        });
        data.version += 1;
        self.persist_locked(&data);
    }

    /// Revoke a blob. Only succeeds if `author` is the original seeder or "server".
    pub fn revoke(&self, sha256: &str, author: &str) -> bool {
        let mut data = self.data.write().unwrap_or_else(|e| e.into_inner());

        // Check authorization: author must be the seeder or "server".
        if let Some(existing) = data.entries.get(sha256) {
            if author != "server" && author != existing.seeder_id {
                return false;
            }
        }

        let now = now_millis();
        if let Some(entry) = data.entries.get_mut(sha256) {
            entry.status = BlobStatus::Revoked;
            entry.updated_at = now;
            entry.updated_by = author.to_string();
        } else {
            // Revocation for unknown blob — record it anyway so it prevents
            // future registration.
            data.entries.insert(sha256.to_string(), BlobEntry {
                sha256: sha256.to_string(),
                label: String::new(),
                target: String::new(),
                retention: String::new(),
                seeder_id: String::new(),
                size: 0,
                status: BlobStatus::Revoked,
                updated_at: now,
                updated_by: author.to_string(),
            });
        }

        data.version += 1;
        self.persist_locked(&data);
        true
    }

    /// Merge a remote manifest into ours. Returns true if anything changed.
    pub fn merge(&self, remote: &ManifestData) -> bool {
        let mut data = self.data.write().unwrap_or_else(|e| e.into_inner());
        let mut changed = false;

        for (sha256, remote_entry) in &remote.entries {
            match data.entries.get(sha256) {
                Some(local_entry) => {
                    if entry_wins(remote_entry, local_entry) {
                        data.entries.insert(sha256.clone(), remote_entry.clone());
                        changed = true;
                    }
                }
                None => {
                    data.entries.insert(sha256.clone(), remote_entry.clone());
                    changed = true;
                }
            }
        }

        if changed {
            // Advance version past both local and remote.
            data.version = data.version.max(remote.version) + 1;
            self.persist_locked(&data);
        }

        changed
    }

    /// Get a snapshot of the manifest data for serialization/gossip.
    pub fn snapshot(&self) -> ManifestData {
        self.data.read().unwrap_or_else(|e| e.into_inner()).clone()
    }

    /// List all active blob hashes.
    pub fn active_blobs(&self) -> Vec<String> {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        data.entries
            .iter()
            .filter(|(_, e)| e.status == BlobStatus::Active)
            .map(|(k, _)| k.clone())
            .collect()
    }

    /// List all revoked blob hashes.
    pub fn revoked_blobs(&self) -> Vec<String> {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        data.entries
            .iter()
            .filter(|(_, e)| e.status == BlobStatus::Revoked)
            .map(|(k, _)| k.clone())
            .collect()
    }

    /// Prune revoked entries older than the given duration.
    pub fn prune_revoked(&self, max_age: std::time::Duration) {
        let cutoff = now_millis().saturating_sub(max_age.as_millis() as u64);
        let mut data = self.data.write().unwrap_or_else(|e| e.into_inner());
        let before = data.entries.len();
        data.entries.retain(|_, e| {
            !(e.status == BlobStatus::Revoked && e.updated_at < cutoff)
        });
        let pruned = before - data.entries.len();
        if pruned > 0 {
            data.version += 1;
            self.persist_locked(&data);
            info!(pruned, "pruned old revoked manifest entries");
        }
    }

    /// Persist to disk (caller holds write lock).
    fn persist_locked(&self, data: &ManifestData) {
        if let Ok(json) = serde_json::to_string_pretty(data) {
            // Atomic write: write to temp file, then rename.
            let tmp = self.path.with_extension("json.tmp");
            if std::fs::write(&tmp, &json).is_ok() {
                std::fs::rename(&tmp, &self.path).ok();
            }
        }
    }
}

/// LWW comparison: does `a` win over `b`?
fn entry_wins(a: &BlobEntry, b: &BlobEntry) -> bool {
    if a.updated_at != b.updated_at {
        return a.updated_at > b.updated_at;
    }
    // Tie: revoked wins over active (safety).
    if a.status != b.status {
        return a.status == BlobStatus::Revoked;
    }
    // Same status, same timestamp: higher author string wins (deterministic).
    a.updated_by > b.updated_by
}

/// Current time in Unix milliseconds.
fn now_millis() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_entry(sha: &str, status: BlobStatus, ts: u64, author: &str) -> BlobEntry {
        BlobEntry {
            sha256: sha.to_string(),
            label: "test".to_string(),
            target: "*".to_string(),
            retention: "7d".to_string(),
            seeder_id: "envoy-a".to_string(),
            size: 1024,
            status,
            updated_at: ts,
            updated_by: author.to_string(),
        }
    }

    #[test]
    fn test_lww_higher_timestamp_wins() {
        let old = make_entry("abc", BlobStatus::Active, 100, "a");
        let new = make_entry("abc", BlobStatus::Active, 200, "b");
        assert!(entry_wins(&new, &old));
        assert!(!entry_wins(&old, &new));
    }

    #[test]
    fn test_lww_revoke_wins_tie() {
        let active = make_entry("abc", BlobStatus::Active, 100, "a");
        let revoked = make_entry("abc", BlobStatus::Revoked, 100, "a");
        assert!(entry_wins(&revoked, &active));
        assert!(!entry_wins(&active, &revoked));
    }

    #[test]
    fn test_lww_same_status_same_ts_higher_author_wins() {
        let a = make_entry("abc", BlobStatus::Active, 100, "a");
        let b = make_entry("abc", BlobStatus::Active, 100, "b");
        assert!(entry_wins(&b, &a));
        assert!(!entry_wins(&a, &b));
    }

    #[test]
    fn test_merge_adds_new_entries() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        let mut remote = ManifestData::new();
        remote.version = 1;
        remote.entries.insert("abc".to_string(), make_entry("abc", BlobStatus::Active, 100, "a"));

        assert!(manifest.merge(&remote));
        assert!(manifest.is_active("abc"));
        assert_eq!(manifest.version(), 2); // max(0, 1) + 1
    }

    #[test]
    fn test_merge_revoke_overwrites_active() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        // Local has active at ts=100.
        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024);

        // Remote has revoked at ts=200.
        let mut remote = ManifestData::new();
        remote.version = 5;
        remote.entries.insert("abc".to_string(), make_entry("abc", BlobStatus::Revoked, now_millis() + 1000, "envoy-a"));

        assert!(manifest.merge(&remote));
        assert!(manifest.is_revoked("abc"));
    }

    #[test]
    fn test_merge_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        let mut remote = ManifestData::new();
        remote.version = 1;
        remote.entries.insert("abc".to_string(), make_entry("abc", BlobStatus::Active, 100, "a"));

        assert!(manifest.merge(&remote));
        let v1 = manifest.version();

        // Same merge again — should be idempotent.
        assert!(!manifest.merge(&remote));
        assert_eq!(manifest.version(), v1);
    }

    #[test]
    fn test_revoke_authorization() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024);

        // Wrong author — should fail.
        assert!(!manifest.revoke("abc", "envoy-b"));
        assert!(manifest.is_active("abc"));

        // Correct author — should succeed.
        assert!(manifest.revoke("abc", "envoy-a"));
        assert!(manifest.is_revoked("abc"));
    }

    #[test]
    fn test_server_can_revoke_anything() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024);

        // Server override — always allowed.
        assert!(manifest.revoke("abc", "server"));
        assert!(manifest.is_revoked("abc"));
    }

    #[test]
    fn test_record_seed_blocked_by_revocation() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        // Revoke first.
        manifest.revoke("abc", "server");
        assert!(manifest.is_revoked("abc"));

        // Try to seed — should be blocked.
        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024);
        assert!(manifest.is_revoked("abc"));
    }

    #[test]
    fn test_persistence_roundtrip() {
        let dir = tempfile::tempdir().unwrap();

        {
            let manifest = Manifest::load(dir.path());
            manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024);
        }

        // Reload from disk.
        let manifest = Manifest::load(dir.path());
        assert!(manifest.is_active("abc"));
        assert!(manifest.version() > 0);
    }

    #[test]
    fn test_prune_revoked() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        // Insert a revoked entry with old timestamp.
        let mut data = manifest.data.write().unwrap();
        data.entries.insert("old".to_string(), make_entry("old", BlobStatus::Revoked, 1000, "a"));
        data.entries.insert("new".to_string(), make_entry("new", BlobStatus::Active, now_millis(), "a"));
        data.version += 1;
        drop(data);

        manifest.prune_revoked(std::time::Duration::from_secs(1));

        assert!(!manifest.is_revoked("old")); // pruned
        assert!(manifest.is_active("new"));   // kept
    }
}
