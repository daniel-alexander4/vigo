// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigocrypt trait snapshot — shared between the `vigo crypt list`
//! CLI verb and the daemon's `vigocrypt` trait collector.
//!
//! A snapshot answers one question: "for this user on this envoy, what
//! files are in `~/vigocrypt/` and what do we know about them?" The
//! output is a small JSON-serializable shape that the trait pipeline
//! ships to the server every 30 seconds for fleet observability.
//!
//! The only potentially-expensive part is computing the SHA-256 of
//! each ciphertext file. For personal-use scale (tens to low hundreds
//! of files totaling <1 GiB) a cold scan is fine, but a repeat scan
//! on every trait cycle would be gratuitous. [`ShaCache`] keyed by
//! `(filename, mtime, size)` at `/var/lib/vigo/vigocrypt/<user>/sha_cache.json`
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

/// Per-user vigocrypt state as observed on this envoy at snapshot
/// time. Shipped via the standard trait pipeline under
/// `vigocrypt.users[]`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserSnapshot {
    pub user: String,
    /// Total file count (not bounded — if `files` is truncated to
    /// `RECENT_FILE_CAP`, this still reflects the full count).
    pub file_count: usize,
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
    /// Peers known for this user from the vigocrypt discovery
    /// table, with basic metadata for UI display.
    pub peers: Vec<PeerSummary>,
    /// True when the per-user session helper has an unlocked
    /// identity in memory. Ambient-security signal for operators.
    pub session_unlocked: bool,
}

/// Compact peer view for the trait payload — a strict subset of
/// `vigocrypt::state::PeerEntry`. We leave the last-seen-addr out
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
/// envoy. Called by the `vigocrypt` trait collector.
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
            Err(e) => tracing::debug!(user = %user, error = %e, "vigocrypt: snapshot build failed"),
        }
    }
    Ok(TraitPayload {
        updated_at: now_secs(),
        users: out,
    })
}

/// Build the per-user snapshot. Shared between the trait collector
/// and `vigo crypt list` via its re-exported accessors.
pub fn build_user_snapshot(user: &str, home: &Path) -> Result<UserSnapshot> {
    let content_dir = config::content_dir(home);
    let mut files_all: Vec<FileEntry> = Vec::new();
    if content_dir.is_dir() {
        let mut cache = ShaCache::load(user);
        let mut cache_dirty = false;
        for entry in fs::read_dir(&content_dir)
            .with_context(|| format!("reading {}", content_dir.display()))?
        {
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
            files_all.push(FileEntry { name, size, mtime, sha256: sha });
        }
        if cache_dirty {
            if let Err(e) = cache.save(user) {
                tracing::debug!(
                    user = %user,
                    error = %e,
                    "vigocrypt: sha cache save failed (non-fatal)"
                );
            }
        }
    }
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

    // Session-helper state: ask via the unix socket. Failure to
    // connect means "locked" — exactly what we want to report.
    let session_unlocked = super::session::request_status().is_ok();

    Ok(UserSnapshot {
        user: user.to_string(),
        file_count,
        total_bytes,
        files: recent,
        files_digest,
        tombstone_count,
        tombstones,
        peers,
        session_unlocked,
    })
}

fn digest_files(files: &[FileEntry]) -> String {
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
/// Lives at `/var/lib/vigo/vigocrypt/<user>/sha_cache.json`,
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
            FileEntry { name: "a".into(), size: 1, mtime: 1, sha256: "sha_a".into() },
            FileEntry { name: "b".into(), size: 2, mtime: 2, sha256: "sha_b".into() },
        ];
        let b = vec![
            FileEntry { name: "b".into(), size: 2, mtime: 2, sha256: "sha_b".into() },
            FileEntry { name: "a".into(), size: 1, mtime: 1, sha256: "sha_a".into() },
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
        }];
        let b = vec![FileEntry {
            name: "foo".into(),
            size: 1,
            mtime: 1,
            sha256: "sha_b".into(),
        }];
        assert_ne!(digest_files(&a), digest_files(&b));
    }
}
