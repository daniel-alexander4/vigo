// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Lockbox trait snapshot — shared between the `vigo swarm lockbox list`
//! CLI verb and the daemon's `lockbox` trait collector.
//!
//! A snapshot answers one question: "for this user on this envoy, what
//! files are in `~/lockbox/` and what do we know about them?" The
//! output is a small JSON-serializable shape that the trait pipeline
//! ships to the server every 30 seconds for fleet observability.
//!
//! The only potentially-expensive part is computing the SHA-256 of
//! each ciphertext file. For personal-use scale (tens to low hundreds
//! of files totaling <1 GiB) a cold scan is fine, but a repeat scan
//! on every trait cycle would be gratuitous. [`ShaCache`] keyed by
//! `(filename, mtime, size)` at `/var/lib/vigo/swarm/lockbox/<user>/sha_cache.json`
//! takes the cold cost once and keeps it amortized across subsequent
//! cycles.

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use sha2::Digest;

use super::config;
use super::tombstones::TombstoneTable;

/// Cap on how many recent files to carry inline on the trait.
/// Anything beyond this count is summarized (total bytes, total
/// count) but not enumerated — full list is served on demand via
/// the drill-down REST endpoint.
pub const RECENT_FILE_CAP: usize = 20;

/// One ciphertext file's per-envoy state.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FileEntry {
    pub name: String,
    pub size: u64,
    pub mtime: i64,
    pub sha256: String,
    /// Blob-content signature, attached ONLY when this entry is served by
    /// the catch-up file-list endpoint (`GET /swarm/lockbox/files/<user>`)
    /// so the puller can authenticate each blob before accepting it. Left
    /// `None` (and skipped on the wire) everywhere else — the trait
    /// snapshot path keeps `FileEntry` lean and the `files_digest` stays
    /// content-only, so adding this field doesn't perturb the ADR-033
    /// inventory hash.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub blobsig: Option<super::blobsig::BlobSig>,
}

/// A tombstone serialized for the trait payload. Mirrors the local
/// tombstone store but emits a strictly per-user-filename shape —
/// the aggregator can join these across envoys without needing the
/// full `TombstoneTable` structure.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TombstoneEntry {
    pub name: String,
    pub deletion_time: i64,
    pub source_hostname: String,
}

/// Per-user lockbox state as observed on this envoy at snapshot
/// time. Shipped via the standard trait pipeline under
/// `lockbox.users[]`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserSnapshot {
    pub user: String,
    /// Total file count (not bounded — if `files` is truncated to
    /// `RECENT_FILE_CAP`, this still reflects the full count).
    pub file_count: usize,
    /// ADR-012 `.failed` artifacts in `~<user>/lockbox/` — a file whose
    /// encrypt failed permanently, so it sits in plaintext and never syncs.
    /// Excluded from `file_count`/`files`/`files_digest` (it's not part of
    /// the synced corpus); surfaced here so the mesh can expose
    /// `vigo_lockbox_failed_files` for an otherwise journal-only failure.
    #[serde(default)]
    pub failed_file_count: usize,
    pub total_bytes: u64,
    /// Recent files, bounded at `RECENT_FILE_CAP`. Sorted by mtime
    /// descending (newest first). Use the drill-down endpoint for
    /// the full list when `file_count > RECENT_FILE_CAP`.
    pub files: Vec<FileEntry>,
    /// Aggregate digest of the sorted full-file-set
    /// (filename|sha256|size). Lets the aggregator detect
    /// cross-envoy file-set divergence without carrying the full
    /// list on every envoy's trait.
    pub files_digest: String,
    pub tombstone_count: usize,
    pub tombstones: Vec<TombstoneEntry>,
    /// Peers known for this user from the lockbox discovery
    /// table, with basic metadata for UI display.
    pub peers: Vec<PeerSummary>,
}

/// Compact peer view for the trait payload — a strict subset of
/// `lockbox::state::PeerEntry`. We leave the last-seen-addr out
/// on purpose (rebinding across network interfaces makes it a less
/// stable identifier than `hostname`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PeerSummary {
    pub hostname: String,
    pub pubkey: String,
    pub sign_pubkey: String,
    pub last_seen: i64,
}

/// Full per-envoy trait payload.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TraitPayload {
    pub updated_at: i64,
    pub users: Vec<UserSnapshot>,
}

/// Build the full trait payload for every user enrolled on this
/// envoy. Called by the `lockbox` trait collector.
pub fn build_trait_payload() -> Result<TraitPayload> {
    let users = super::enrolled_users().context("listing enrolled users")?;
    let mut out = Vec::with_capacity(users.len());
    for user in users {
        let home = match super::home_for_user_pub(&user) {
            Some(h) => h,
            None => continue,
        };
        match build_user_snapshot(&user, &home) {
            Ok(s) => out.push(s),
            Err(e) => tracing::debug!(user = %user, error = %e, "lockbox: snapshot build failed"),
        }
    }
    Ok(TraitPayload {
        updated_at: now_secs(),
        users: out,
    })
}

/// Build the per-user snapshot. Shared between the trait collector
/// and `vigo swarm lockbox list` via its re-exported accessors.
pub fn build_user_snapshot(user: &str, home: &Path) -> Result<UserSnapshot> {
    let mut files_all = local_files_for_user(user, home);
    let file_count = files_all.len();
    let total_bytes: u64 = files_all.iter().map(|f| f.size).sum();
    let files_digest = digest_files(&files_all);

    // Sort by mtime descending for `recent_files`, stable-by-name.
    files_all.sort_by(|a, b| b.mtime.cmp(&a.mtime).then_with(|| a.name.cmp(&b.name)));
    let recent: Vec<FileEntry> = files_all.into_iter().take(RECENT_FILE_CAP).collect();

    // Tombstones.
    let tt = TombstoneTable::load(user);
    let tombstone_count = tt.tombstones.len();
    let mut tombstones: Vec<TombstoneEntry> = tt
        .tombstones
        .iter()
        .map(|(name, t)| TombstoneEntry {
            name: name.clone(),
            deletion_time: t.deletion_time,
            source_hostname: t.source_hostname.clone(),
        })
        .collect();
    tombstones.sort_by(|a, b| b.deletion_time.cmp(&a.deletion_time));

    // Peers.
    let table = super::state::PeerTable::load(user);
    let peers: Vec<PeerSummary> = table
        .peers
        .values()
        .map(|p| PeerSummary {
            hostname: p.hostname.clone(),
            pubkey: p.pubkey.clone(),
            sign_pubkey: p.sign_pubkey.clone(),
            last_seen: p.last_seen,
        })
        .collect();

    Ok(UserSnapshot {
        user: user.to_string(),
        file_count,
        failed_file_count: count_failed_artifacts(user, home),
        total_bytes,
        files: recent,
        files_digest,
        tombstone_count,
        tombstones,
        peers,
    })
}

/// Walk `~<user>/lockbox/` and return one `FileEntry` per ciphertext
/// file (sha256 served from the per-user mtime+size cache when warm).
/// Shared between the trait snapshot, the announcement `files_digest`
/// computation, and the catch-up file-list endpoint — there is exactly
/// one walker so the digest two peers compute over the same on-disk
/// state agrees byte-for-byte.
///
/// Returns an empty vec if the lockbox dir doesn't exist or can't be
/// read. The catch-up path interprets that as "I have nothing, pull
/// everything"; the trait collector handles the empty case identically
/// to the historical behavior.
/// count_failed_artifacts counts ADR-012 `.failed` artifacts in
/// `~<user>/lockbox/` — files whose encrypt failed permanently and now sit
/// in plaintext, never syncing. `local_files_for_user` deliberately skips
/// these (they aren't part of the synced corpus); this is the one place that
/// counts them, so the mesh can surface them as a metric instead of leaving
/// the failure journal-only. A separate, full enumeration of the content dir
/// (not the bounded recent-file list) so the count is exact, not truncated.
pub(super) fn count_failed_artifacts(_user: &str, home: &Path) -> usize {
    let content_dir = config::content_dir(home);
    let read = match fs::read_dir(&content_dir) {
        Ok(r) => r,
        Err(_) => return 0,
    };
    read.filter_map(|e| e.ok())
        .filter_map(|e| e.path().file_name().and_then(|n| n.to_str()).map(str::to_string))
        .filter(|name| super::auto_encrypt::is_failed_artifact_name(name))
        .count()
}

pub(super) fn local_files_for_user(user: &str, home: &Path) -> Vec<FileEntry> {
    let content_dir = config::content_dir(home);
    if !content_dir.is_dir() {
        return Vec::new();
    }
    let read = match fs::read_dir(&content_dir) {
        Ok(r) => r,
        Err(e) => {
            tracing::debug!(
                user = %user,
                dir = %content_dir.display(),
                error = %e,
                "lockbox: read_dir failed; returning empty file set"
            );
            return Vec::new();
        }
    };
    let mut cache = ShaCache::load(user);
    let mut cache_dirty = false;
    let mut out: Vec<FileEntry> = Vec::new();
    for entry in read {
        let entry = match entry {
            Ok(e) => e,
            Err(_) => continue,
        };
        let path = entry.path();
        let name = match path.file_name().and_then(|n| n.to_str()) {
            Some(n) if !n.starts_with('.') => n.to_string(),
            _ => continue,
        };
        if !path.is_file() {
            continue;
        }
        // Skip ADR-012 `.failed` artifacts. These are permanent
        // local-only files (the watcher's resolve_event filter also
        // drops them, so they never push). Including them in the
        // walk would inflate file_count / total_bytes / files_digest
        // on the producing envoy and present as a fleet-wide
        // divergence on the mesh page when in fact the file is not
        // part of the synced corpus.
        if super::auto_encrypt::is_failed_artifact_name(&name) {
            continue;
        }
        let meta = match entry.metadata() {
            Ok(m) => m,
            Err(_) => continue,
        };
        let size = meta.len();
        let mtime = meta
            .modified()
            .ok()
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0);
        let sha = match cache.lookup(&name, mtime, size) {
            Some(s) => s,
            None => {
                let s = match compute_sha(&path) {
                    Ok(s) => s,
                    Err(_) => continue,
                };
                cache.insert(&name, mtime, size, &s);
                cache_dirty = true;
                s
            }
        };
        out.push(FileEntry { name, size, mtime, sha256: sha, blobsig: None });
    }
    if cache_dirty {
        if let Err(e) = cache.save(user) {
            tracing::debug!(
                user = %user,
                error = %e,
                "lockbox: sha cache save failed (non-fatal)"
            );
        }
    }
    out
}

pub(super) fn digest_files(files: &[FileEntry]) -> String {
    // Stable, deterministic digest over the full file set. Peers
    // with identical logical state must produce identical digests
    // regardless of enumeration order.
    let mut names: Vec<&FileEntry> = files.iter().collect();
    names.sort_by(|a, b| a.name.cmp(&b.name));
    let mut h = sha2::Sha256::new();
    for f in names {
        h.update(f.name.as_bytes());
        h.update(b"|");
        h.update(f.sha256.as_bytes());
        h.update(b"|");
        h.update(f.size.to_be_bytes());
        h.update(b"\n");
    }
    hex::encode(&h.finalize()[..16])
}

fn compute_sha(path: &Path) -> Result<String> {
    let bytes = fs::read(path).with_context(|| format!("reading {}", path.display()))?;
    let mut h = sha2::Sha256::new();
    h.update(&bytes);
    Ok(hex::encode(h.finalize()))
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ---------- SHA cache ----------

/// Persistent sha256 cache keyed by `(filename, mtime, size)`.
/// Lives at `/var/lib/vigo/swarm/lockbox/<user>/sha_cache.json`,
/// root-owned 0600. Never a security-sensitive file — stores only
/// ciphertext hashes (which are already public on the wire) — but
/// root-only because it lives in the agent state dir.
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct ShaCache {
    entries: BTreeMap<String, ShaCacheEntry>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct ShaCacheEntry {
    mtime: i64,
    size: u64,
    sha256: String,
}

impl ShaCache {
    pub fn load(user: &str) -> Self {
        let path = cache_path(user);
        match fs::read_to_string(&path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
            Err(_) => Self::default(),
        }
    }

    pub fn lookup(&self, name: &str, mtime: i64, size: u64) -> Option<String> {
        let e = self.entries.get(name)?;
        if e.mtime == mtime && e.size == size {
            Some(e.sha256.clone())
        } else {
            None
        }
    }

    pub fn insert(&mut self, name: &str, mtime: i64, size: u64, sha: &str) {
        self.entries.insert(
            name.to_string(),
            ShaCacheEntry {
                mtime,
                size,
                sha256: sha.to_string(),
            },
        );
    }

    pub fn save(&self, user: &str) -> Result<()> {
        let dir = config::agent_state_dir(user);
        fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
        let path = cache_path(user);
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string(self).context("serializing sha cache")?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            let mut f = fs::OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .mode(0o600)
                .open(&tmp)
                .with_context(|| format!("creating {}", tmp.display()))?;
            f.write_all(json.as_bytes())?;
            f.sync_all().ok();
        }
        #[cfg(not(unix))]
        {
            fs::write(&tmp, json.as_bytes())
                .with_context(|| format!("writing {}", tmp.display()))?;
        }
        fs::rename(&tmp, &path).with_context(|| format!("renaming to {}", path.display()))?;
        Ok(())
    }
}

fn cache_path(user: &str) -> PathBuf {
    config::agent_state_dir(user).join("sha_cache.json")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn count_failed_artifacts_counts_only_failed_suffix() {
        let home = tempfile::tempdir().unwrap();
        let content = config::content_dir(home.path());
        std::fs::create_dir_all(&content).unwrap();
        // Two genuine `.failed` artifacts (with the unix-ts shape), one
        // synced ciphertext, one user-named "report.failed" (NOT ours).
        std::fs::write(content.join("a.txt.20260531.failed"), b"x").unwrap();
        std::fs::write(content.join("b.conf.1234567890.failed"), b"x").unwrap();
        std::fs::write(content.join("c.age"), b"x").unwrap();
        std::fs::write(content.join("report.failed"), b"x").unwrap();

        assert_eq!(count_failed_artifacts("dan", home.path()), 2);
    }

    #[test]
    fn count_failed_artifacts_zero_when_dir_absent() {
        let home = tempfile::tempdir().unwrap();
        // No lockbox/ dir created.
        assert_eq!(count_failed_artifacts("dan", home.path()), 0);
    }

    #[test]
    fn sha_cache_hits_on_same_mtime_size() {
        let mut c = ShaCache::default();
        c.insert("foo.pdf", 100, 4096, "abc");
        assert_eq!(c.lookup("foo.pdf", 100, 4096), Some("abc".to_string()));
    }

    #[test]
    fn sha_cache_misses_on_mtime_change() {
        let mut c = ShaCache::default();
        c.insert("foo.pdf", 100, 4096, "abc");
        assert!(c.lookup("foo.pdf", 200, 4096).is_none());
    }

    #[test]
    fn sha_cache_misses_on_size_change() {
        let mut c = ShaCache::default();
        c.insert("foo.pdf", 100, 4096, "abc");
        assert!(c.lookup("foo.pdf", 100, 8192).is_none());
    }

    #[test]
    fn digest_stable_under_enum_order() {
        let a = vec![
            FileEntry { name: "a".into(), size: 1, mtime: 1, sha256: "sha_a".into(), blobsig: None },
            FileEntry { name: "b".into(), size: 2, mtime: 2, sha256: "sha_b".into(), blobsig: None },
        ];
        let b = vec![
            FileEntry { name: "b".into(), size: 2, mtime: 2, sha256: "sha_b".into(), blobsig: None },
            FileEntry { name: "a".into(), size: 1, mtime: 1, sha256: "sha_a".into(), blobsig: None },
        ];
        assert_eq!(digest_files(&a), digest_files(&b));
    }

    #[test]
    fn digest_changes_on_content_change() {
        let a = vec![FileEntry {
            name: "foo".into(),
            size: 1,
            mtime: 1,
            sha256: "sha_a".into(),
            blobsig: None,
        }];
        let b = vec![FileEntry {
            name: "foo".into(),
            size: 1,
            mtime: 1,
            sha256: "sha_b".into(),
            blobsig: None,
        }];
        assert_ne!(digest_files(&a), digest_files(&b));
    }
}
