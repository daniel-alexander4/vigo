// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
    /// Seeder's peer address (ip:port) for direct P2P download.
    /// Set by the seeding agent; propagated through CRDT gossip.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub seeder_address: String,
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
///
/// The inner `ManifestData` is held under `RwLock<Arc<...>>` so
/// `snapshot()` is an O(1) `Arc::clone` instead of a deep clone of
/// the entries map. Writers use `Arc::make_mut` to get copy-on-write
/// semantics — when no reader is holding a snapshot, the writer
/// mutates the existing allocation in place; when a reader is holding
/// one, the writer detaches by cloning the inner data and rewriting
/// the Arc, so the reader's snapshot remains immutable.
pub struct Manifest {
    data: RwLock<Arc<ManifestData>>,
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
            data: RwLock::new(Arc::new(data)),
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

    /// Returns true if the blob exists in the manifest as active.
    pub fn has_active(&self, sha256: &str) -> bool {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        matches!(data.entries.get(sha256), Some(e) if e.status == BlobStatus::Active)
    }

    /// Look up a single blob entry by SHA256. Returns a clone so callers
    /// can use it without holding the lock. Used by the announcer to
    /// read blob sizes for partial-bitmap advertising without snapshotting
    /// the entire manifest on every cycle.
    pub fn get_entry(&self, sha256: &str) -> Option<BlobEntry> {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        data.entries.get(sha256).cloned()
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
        seeder_address: &str,
    ) {
        let mut guard = self.data.write().unwrap_or_else(|e| e.into_inner());

        // Don't overwrite a revocation with a seed from the same timestamp
        // window — the revocation should win. Peek through the guard
        // before make_mut so an early-return doesn't pay the CoW clone.
        if let Some(existing) = guard.entries.get(sha256) {
            if existing.status == BlobStatus::Revoked {
                return;
            }
        }

        let now = now_millis();
        let data = Arc::make_mut(&mut guard);
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
            seeder_address: seeder_address.to_string(),
        });
        data.version += 1;
        self.persist_locked(data);
    }

    /// Revoke a blob. Only succeeds if `author` is the original seeder or "server".
    pub fn revoke(&self, sha256: &str, author: &str) -> bool {
        let mut guard = self.data.write().unwrap_or_else(|e| e.into_inner());

        // Check authorization through the guard's Arc — done before
        // make_mut so an unauthorized revoke doesn't trigger the CoW
        // clone.
        if let Some(existing) = guard.entries.get(sha256) {
            if author != "server" && author != existing.seeder_id {
                return false;
            }
        }

        let now = now_millis();
        let data = Arc::make_mut(&mut guard);
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
                seeder_address: String::new(),
            });
        }

        data.version += 1;
        self.persist_locked(data);
        true
    }

    /// Merge a remote manifest into ours. Returns true if anything changed.
    pub fn merge(&self, remote: &ManifestData) -> bool {
        let mut guard = self.data.write().unwrap_or_else(|e| e.into_inner());
        let data = Arc::make_mut(&mut guard);
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
            self.persist_locked(data);
        }

        changed
    }

    /// Get a snapshot of the manifest data for serialization/gossip.
    ///
    /// Returns an `Arc<ManifestData>` — O(1) ref-bump regardless of map
    /// size. The returned handle is immutable; concurrent writers either
    /// mutate the underlying allocation in place (when no other snapshot
    /// is live) or clone-then-detach via `Arc::make_mut` so the snapshot
    /// the caller is reading does not change underneath them.
    pub fn snapshot(&self) -> Arc<ManifestData> {
        Arc::clone(&self.data.read().unwrap_or_else(|e| e.into_inner()))
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

    /// Return active blobs whose target spec matches this agent.
    /// Used for self-assignment: the agent evaluates manifest entries locally
    /// instead of relying on the server to assign per-envoy.
    ///
    /// **Claim-only labels are excluded.** Gitback content and meta seeds
    /// (`gitback:*`, `gitback-meta:*`) are routed through the server's
    /// in-memory `ClaimTable` rather than self-assignment — see
    /// `agent/src/gitback/receiver.rs` and the `entries_for_labels` helper
    /// below. Including them here would let every envoy in the fleet
    /// download every gitback bundle (the publisher writes `target: "*"`),
    /// silently bypassing the per-source mirror cap. This filter is the
    /// single chokepoint that keeps cap enforcement on the server side.
    pub fn assignments_for(
        &self,
        hostname: &str,
        traits: &std::collections::HashMap<String, String>,
    ) -> Vec<BlobEntry> {
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        data.entries
            .values()
            .filter(|e| {
                e.status == BlobStatus::Active
                    && !is_claim_only_label(&e.label)
                    && super::target::match_target(hostname, traits, &e.target)
            })
            .cloned()
            .collect()
    }

    /// Return active blobs whose `label` is in `wanted`. Used by subsystems
    /// that have their own out-of-band notion of which blobs to download
    /// (today: gitback, which receives its set from `CheckInResponse.gitback_claims`).
    /// Empty `wanted` returns nothing.
    pub fn entries_for_labels(
        &self,
        wanted: &std::collections::HashSet<String>,
    ) -> Vec<BlobEntry> {
        if wanted.is_empty() {
            return Vec::new();
        }
        let data = self.data.read().unwrap_or_else(|e| e.into_inner());
        data.entries
            .values()
            .filter(|e| e.status == BlobStatus::Active && wanted.contains(&e.label))
            .cloned()
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
        let mut guard = self.data.write().unwrap_or_else(|e| e.into_inner());
        let data = Arc::make_mut(&mut guard);
        let before = data.entries.len();
        data.entries.retain(|_, e| {
            !(e.status == BlobStatus::Revoked && e.updated_at < cutoff)
        });
        let pruned = before - data.entries.len();
        if pruned > 0 {
            data.version += 1;
            self.persist_locked(data);
            info!(pruned, "pruned old revoked manifest entries");
        }
    }

    /// Persist to disk (caller holds write lock). Mode 0644 so user-mode
    /// CLI verbs can read without sudo (0.41.1+); the file is non-secret
    /// — its contents are gossiped to peers in the clear over swarm mTLS.
    fn persist_locked(&self, data: &ManifestData) {
        if let Ok(json) = serde_json::to_string_pretty(data) {
            // Atomic write: write to temp file, then rename.
            let tmp = self.path.with_extension("json.tmp");
            if std::fs::write(&tmp, &json).is_ok() {
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644));
                }
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

/// Labels that are routed through a per-subsystem claim mechanism rather
/// than the standard target-spec self-assignment. Claim-only labels are
/// never returned by [`Manifest::assignments_for`]; the only path to
/// downloading them is via [`Manifest::entries_for_labels`] from the
/// owning subsystem.
///
/// Today the only claim-only namespace is gitback. The publisher writes
/// `target: "*"` for gitback seeds (they're conceptually open to any
/// envoy), but the actual mirror set is decided server-side by the
/// gitback `ClaimTable` and delivered per envoy via
/// `CheckInResponse.gitback_claims`. Without this filter, every envoy
/// in the fleet would self-assign every gitback bundle the moment the
/// existing download loop runs, silently bypassing the per-source
/// mirror cap.
fn is_claim_only_label(label: &str) -> bool {
    label.starts_with("gitback:") || label.starts_with("gitback-meta:")
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
            seeder_address: String::new(),
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
        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024, "");

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

        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024, "");

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

        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024, "");

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
        manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024, "");
        assert!(manifest.is_revoked("abc"));
    }

    #[test]
    fn test_persistence_roundtrip() {
        let dir = tempfile::tempdir().unwrap();

        {
            let manifest = Manifest::load(dir.path());
            manifest.record_seed("abc", "test", "*", "7d", "envoy-a", 1024, "");
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
        let mut guard = manifest.data.write().unwrap();
        let data = Arc::make_mut(&mut guard);
        data.entries.insert("old".to_string(), make_entry("old", BlobStatus::Revoked, 1000, "a"));
        data.entries.insert("new".to_string(), make_entry("new", BlobStatus::Active, now_millis(), "a"));
        data.version += 1;
        drop(guard);

        manifest.prune_revoked(std::time::Duration::from_secs(1));

        assert!(!manifest.is_revoked("old")); // pruned
        assert!(manifest.is_active("new"));   // kept
    }

    fn labelled_entry(sha: &str, label: &str) -> BlobEntry {
        let mut e = make_entry(sha, BlobStatus::Active, now_millis(), "envoy-a");
        e.label = label.to_string();
        e
    }

    #[test]
    fn is_claim_only_label_matches_gitback_namespaces() {
        assert!(is_claim_only_label("gitback:host:user:proj"));
        assert!(is_claim_only_label("gitback-meta:host:user:proj"));
    }

    #[test]
    fn is_claim_only_label_rejects_other_namespaces() {
        assert!(!is_claim_only_label("module-bundle"));
        assert!(!is_claim_only_label("longdrawer:user:file"));
        assert!(!is_claim_only_label("gitbackfoo")); // missing colon
        assert!(!is_claim_only_label(""));
        assert!(!is_claim_only_label("foo:gitback:bar")); // not at start
    }

    #[test]
    fn assignments_for_excludes_claim_only_labels() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        let mut guard = manifest.data.write().unwrap();
        let data = Arc::make_mut(&mut guard);
        // A regular swarm seed with target "*" — should match.
        data.entries.insert(
            "aaa".to_string(),
            labelled_entry("aaa", "module-bundle"),
        );
        // A gitback content seed with target "*" — must NOT match.
        data.entries.insert(
            "bbb".to_string(),
            labelled_entry("bbb", "gitback:danlap:dan:proj"),
        );
        // A gitback meta seed — also must NOT match.
        data.entries.insert(
            "ccc".to_string(),
            labelled_entry("ccc", "gitback-meta:danlap:dan:proj"),
        );
        drop(guard);

        let traits = std::collections::HashMap::new();
        let assigned = manifest.assignments_for("any-host", &traits);
        let labels: Vec<&str> = assigned.iter().map(|e| e.label.as_str()).collect();
        assert!(labels.contains(&"module-bundle"));
        assert!(!labels.iter().any(|l| l.starts_with("gitback")));
    }

    #[test]
    fn entries_for_labels_returns_only_requested_labels() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        let mut guard = manifest.data.write().unwrap();
        let data = Arc::make_mut(&mut guard);
        data.entries.insert(
            "aaa".to_string(),
            labelled_entry("aaa", "gitback:danlap:dan:proj1"),
        );
        data.entries.insert(
            "bbb".to_string(),
            labelled_entry("bbb", "gitback:danlap:dan:proj2"),
        );
        data.entries.insert(
            "ccc".to_string(),
            labelled_entry("ccc", "gitback:danlap:dan:proj3"),
        );
        drop(guard);

        let mut wanted = std::collections::HashSet::new();
        wanted.insert("gitback:danlap:dan:proj1".to_string());
        wanted.insert("gitback:danlap:dan:proj3".to_string());

        let got = manifest.entries_for_labels(&wanted);
        let labels: Vec<&str> = got.iter().map(|e| e.label.as_str()).collect();
        assert_eq!(labels.len(), 2);
        assert!(labels.contains(&"gitback:danlap:dan:proj1"));
        assert!(labels.contains(&"gitback:danlap:dan:proj3"));
        assert!(!labels.contains(&"gitback:danlap:dan:proj2"));
    }

    #[test]
    fn entries_for_labels_empty_set_returns_empty() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());
        let mut guard = manifest.data.write().unwrap();
        let data = Arc::make_mut(&mut guard);
        data.entries.insert(
            "aaa".to_string(),
            labelled_entry("aaa", "gitback:danlap:dan:proj"),
        );
        drop(guard);

        let wanted = std::collections::HashSet::new();
        assert!(manifest.entries_for_labels(&wanted).is_empty());
    }

    #[test]
    fn entries_for_labels_skips_revoked() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        let mut guard = manifest.data.write().unwrap();
        let data = Arc::make_mut(&mut guard);
        let mut e = labelled_entry("aaa", "gitback:danlap:dan:proj");
        e.status = BlobStatus::Revoked;
        data.entries.insert("aaa".to_string(), e);
        drop(guard);

        let mut wanted = std::collections::HashSet::new();
        wanted.insert("gitback:danlap:dan:proj".to_string());
        assert!(manifest.entries_for_labels(&wanted).is_empty());
    }

    #[test]
    fn test_assignments_for() {
        let dir = tempfile::tempdir().unwrap();
        let manifest = Manifest::load(dir.path());

        manifest.record_seed("aaa", "blob-a", "os.distro=ubuntu", "7d", "danlap", 100, "");
        manifest.record_seed("bbb", "blob-b", "*,-rasp*", "7d", "danlap", 100, "");
        // Claim-only label — never self-assigned regardless of its target spec.
        manifest.record_seed("ccc", "gitback:danlap:dan:proj", "*", "7d", "danlap", 100, "");

        let ubuntu: std::collections::HashMap<String, String> =
            [("os.distro".to_string(), "ubuntu".to_string())].into_iter().collect();
        let debian: std::collections::HashMap<String, String> =
            [("os.distro".to_string(), "debian".to_string())].into_iter().collect();
        let labels = |v: Vec<BlobEntry>| -> std::collections::HashSet<String> {
            v.into_iter().map(|e| e.label).collect()
        };

        // danlap + ubuntu: gets the trait-matched blob AND the not-rasp blob;
        // never the claim-only one.
        let got = labels(manifest.assignments_for("danlap", &ubuntu));
        assert!(got.contains("blob-a"), "trait target matched");
        assert!(got.contains("blob-b"), "danlap is not rasp*");
        assert!(!got.contains("gitback:danlap:dan:proj"), "claim-only labels are never self-assigned");

        // danlap + debian: the trait target no longer matches.
        let got = labels(manifest.assignments_for("danlap", &debian));
        assert!(!got.contains("blob-a"));
        assert!(got.contains("blob-b"));

        // raspberrypi: -rasp* on blob-b excludes it; the trait blob still matches.
        let got = labels(manifest.assignments_for("raspberrypi", &ubuntu));
        assert!(got.contains("blob-a"), "trait still matches on raspberrypi");
        assert!(!got.contains("blob-b"), "-rasp* excludes raspberrypi");
    }
}
