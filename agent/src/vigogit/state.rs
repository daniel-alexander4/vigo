// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigogit on-disk state.
//!
//! The source side persists per-label seeding state so the agent does
//! not re-publish an identical bundle after a restart. State lives at
//! `<base>/state.json` (where `base` is
//! [`crate::platform::paths::default_vigogit_dir`]).
//!
//! Bundle scratch files live under `<base>/tmp/`. The directory is
//! created with mode `1777` (sticky, like `/tmp`) so that
//! `git bundle create` can write the bundle as the owning Unix user
//! after privilege drop, then root (the agent) atomically moves the
//! finished bundle into the swarm seeds directory. Orphan cleanup on
//! agent startup deletes any `.bundle` files older than
//! [`STALE_BUNDLE_AGE`].

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};
use tracing::{debug, warn};

/// Age at which an orphan bundle file under `tmp/` is considered
/// stale and eligible for removal on agent startup.
pub const STALE_BUNDLE_AGE: Duration = Duration::from_secs(60 * 60);

/// Resolve the bundle scratch directory relative to the vigogit
/// state base directory.
pub fn tmp_dir(base: &Path) -> PathBuf {
    base.join("tmp")
}

/// Resolve the persistent state file path.
pub fn state_path(base: &Path) -> PathBuf {
    base.join("state.json")
}

/// Create the bundle scratch directory if it does not exist and set
/// it to mode `1777` (sticky). Idempotent. Returns the resolved path
/// so callers can chain into bundle file naming.
///
/// On non-Unix the mode is not enforced (the agent ships only on
/// Unix today; `cargo check --target windows` should still build).
pub fn ensure_tmp_dir(base: &Path) -> std::io::Result<PathBuf> {
    let dir = tmp_dir(base);
    fs::create_dir_all(&dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // 0o1777 — sticky bit + rwx for everyone, matching /tmp.
        let perms = fs::Permissions::from_mode(0o1777);
        fs::set_permissions(&dir, perms)?;
    }
    Ok(dir)
}

/// Per-label seeding state. Tracked so the publisher can:
///
/// 1. Skip re-publishing when `git_sha` matches the current `HEAD`
///    (idempotent across restarts and rescans).
/// 2. Cut an incremental bundle from `git_sha..HEAD` when there are
///    new commits.
/// 3. Self-heal by resetting to `None` when the previously published
///    bundle's `blob_sha` is no longer present in the local swarm
///    manifest (retention-revoked while this envoy was offline).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct LabelState {
    /// Git commit SHA at which the most recent successful bundle was
    /// cut. The next bundle's rev-spec is `<git_sha>..HEAD`.
    pub git_sha: String,
    /// SHA256 of the most recent successful bundle file. Used by the
    /// publisher's self-healing check to verify our prior seed still
    /// exists in the swarm before relying on `<git_sha>` as a base.
    pub blob_sha: String,
}

/// Current state-file schema version.
pub const STATE_VERSION: u32 = 1;

/// Persistent vigogit source state. Versioned to allow schema
/// evolution without inventing migrations (zero users, see CLAUDE.md).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct State {
    /// Schema version; bump on incompatible changes.
    #[serde(default = "default_version")]
    pub version: u32,
    /// label → last successful publish state.
    #[serde(default)]
    pub labels: BTreeMap<String, LabelState>,
}

fn default_version() -> u32 {
    STATE_VERSION
}

impl Default for State {
    fn default() -> Self {
        Self {
            version: STATE_VERSION,
            labels: BTreeMap::new(),
        }
    }
}

impl State {
    /// Load state from `<base>/state.json`. Returns an empty default
    /// state if the file is missing or unreadable. Corruption is
    /// logged and treated as missing rather than fatal — the worst
    /// case is one extra full bundle on the next publish.
    pub fn load(base: &Path) -> Self {
        let path = state_path(base);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Self::default(),
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "vigogit: failed to read state.json; starting fresh"
                );
                return Self::default();
            }
        };
        match serde_json::from_slice::<State>(&bytes) {
            Ok(s) => s,
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "vigogit: corrupt state.json; starting fresh"
                );
                Self::default()
            }
        }
    }

    /// Save state to `<base>/state.json` via temp-file + rename.
    /// File mode is `0600` (root-owned, root-only). Caller must hold
    /// any required serialization (the publisher does this from a
    /// single task).
    pub fn save(&self, base: &Path) -> std::io::Result<()> {
        fs::create_dir_all(base)?;
        let final_path = state_path(base);
        let tmp_path = base.join("state.json.tmp");

        let json = serde_json::to_vec_pretty(self)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;

        {
            let mut f = fs::OpenOptions::new()
                .create(true)
                .write(true)
                .truncate(true)
                .open(&tmp_path)?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                f.set_permissions(fs::Permissions::from_mode(0o600))?;
            }
            f.write_all(&json)?;
            f.sync_all()?;
        }

        fs::rename(&tmp_path, &final_path)?;
        Ok(())
    }

    /// Insert or update the state for a single label. Convenience
    /// over directly mutating `self.labels`.
    pub fn record(&mut self, label: &str, git_sha: &str, blob_sha: &str) {
        self.labels.insert(
            label.to_string(),
            LabelState {
                git_sha: git_sha.to_string(),
                blob_sha: blob_sha.to_string(),
            },
        );
    }

    /// Drop the state for a single label, used by the self-healing
    /// reset when a prior bundle's blob is no longer in the manifest.
    pub fn forget(&mut self, label: &str) {
        self.labels.remove(label);
    }
}

// --- traits snapshot -------------------------------------------------
//
// The reactor writes a `TraitsSnapshot` at the end of each pass. The
// `vigogit` trait collector (`agent/src/traits/vigogit.rs`) reads it
// on the next check-in cycle and ships the contents to the server,
// giving operators a view of current source publishing state without
// having to subscribe to a live trait stream.
//
// The snapshot is a *replacement*, not an append log — each pass
// overwrites the file. If the reactor has never ticked (cold start,
// pre-startup races), the file doesn't exist and the collector
// returns `Value::Null`, matching `swarm_seeds` on empty.

/// Current traits-snapshot schema version.
pub const TRAITS_SNAPSHOT_VERSION: u32 = 1;

/// Per-source entry in a traits snapshot. Reflects the latest
/// discovered source plus whatever seeding state exists for it.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SourceTraitEntry {
    pub label: String,
    pub paused: bool,
    /// Last successfully bundled HEAD sha. `None` if the source has
    /// never successfully published (e.g. first-tick failure).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub git_sha: Option<String>,
    /// SHA256 of the most recent bundle file. `None` in the same
    /// first-publish-failure case.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub blob_sha: Option<String>,
}

/// A single discovery-error record. Mirrors
/// [`crate::vigogit::source::SourceError`] but with a string path so
/// the snapshot serializes cleanly across platforms.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SourceErrorEntry {
    pub path: String,
    pub reason: String,
}

/// A single publish-error record. Keyed by the swarm label so the
/// server can correlate it with the ClaimTable view.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PublishErrorEntry {
    pub label: String,
    pub reason: String,
}

/// Snapshot of source-side vigogit state, written at the end of each
/// reactor pass and consumed by the `vigogit` trait collector.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct TraitsSnapshot {
    /// Schema version; bump on incompatible changes.
    #[serde(default = "default_traits_version")]
    pub version: u32,
    /// Unix seconds when this snapshot was written. Operators and
    /// the server can use this to detect a stuck reactor.
    pub updated_at: u64,
    /// Validated discovered sources from the latest pass.
    #[serde(default)]
    pub sources: Vec<SourceTraitEntry>,
    /// Count of non-paused sources in the latest pass (cached so
    /// templates can read `.Traits.vigogit.active_sources` without
    /// walking the list).
    #[serde(default)]
    pub active_sources: usize,
    /// Count of paused sources in the latest pass.
    #[serde(default)]
    pub paused_sources: usize,
    /// Discovery errors surfaced by the latest pass.
    #[serde(default)]
    pub source_errors: Vec<SourceErrorEntry>,
    /// Publish errors surfaced by the latest pass.
    #[serde(default)]
    pub publish_errors: Vec<PublishErrorEntry>,
}

fn default_traits_version() -> u32 {
    TRAITS_SNAPSHOT_VERSION
}

impl Default for TraitsSnapshot {
    fn default() -> Self {
        Self {
            version: TRAITS_SNAPSHOT_VERSION,
            updated_at: 0,
            sources: Vec::new(),
            active_sources: 0,
            paused_sources: 0,
            source_errors: Vec::new(),
            publish_errors: Vec::new(),
        }
    }
}

/// Resolve the traits-snapshot file path.
pub fn traits_snapshot_path(base: &Path) -> PathBuf {
    base.join("traits.json")
}

impl TraitsSnapshot {
    /// Load the most recent snapshot. Returns `Ok(None)` when the
    /// file does not exist (reactor has never ticked) or when it
    /// cannot be parsed (corrupt — treated the same as missing so a
    /// bad file can't block trait reporting). Returns `Err` only on
    /// unexpected I/O errors such as permission denied.
    pub fn load(base: &Path) -> std::io::Result<Option<Self>> {
        let path = traits_snapshot_path(base);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(e),
        };
        match serde_json::from_slice::<TraitsSnapshot>(&bytes) {
            Ok(s) => Ok(Some(s)),
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "vigogit: corrupt traits.json; treating as missing"
                );
                Ok(None)
            }
        }
    }

    /// Save the snapshot via temp-file + rename. Mode `0600`
    /// matching `state.json`. Caller is expected to hold the only
    /// writer (the reactor task).
    pub fn save(&self, base: &Path) -> std::io::Result<()> {
        fs::create_dir_all(base)?;
        let final_path = traits_snapshot_path(base);
        let tmp_path = base.join("traits.json.tmp");

        let json = serde_json::to_vec_pretty(self)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;

        {
            let mut f = fs::OpenOptions::new()
                .create(true)
                .write(true)
                .truncate(true)
                .open(&tmp_path)?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                f.set_permissions(fs::Permissions::from_mode(0o600))?;
            }
            f.write_all(&json)?;
            f.sync_all()?;
        }

        fs::rename(&tmp_path, &final_path)?;
        Ok(())
    }
}

/// Return the current time as unix seconds. Falls back to `0` if
/// the system clock is before the epoch (ie. never in practice).
pub fn unix_seconds_now() -> u64 {
    SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Delete any `*.bundle` file under `<base>/tmp/` older than
/// [`STALE_BUNDLE_AGE`]. Called on agent startup to clean up after
/// interrupted bundle-create operations.
pub fn cleanup_stale_bundles(base: &Path) {
    let cutoff = match SystemTime::now().checked_sub(STALE_BUNDLE_AGE) {
        Some(t) => t,
        None => return,
    };
    cleanup_stale_bundles_with_cutoff(base, cutoff);
}

/// Testable variant: removes `*.bundle` files whose mtime is older
/// than `cutoff`.
fn cleanup_stale_bundles_with_cutoff(base: &Path, cutoff: SystemTime) {
    let dir = tmp_dir(base);
    if !dir.exists() {
        return;
    }
    let entries = match fs::read_dir(&dir) {
        Ok(e) => e,
        Err(e) => {
            warn!(dir = %dir.display(), error = %e, "vigogit: cannot read tmp dir");
            return;
        }
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) != Some("bundle") {
            continue;
        }
        let modified = match entry.metadata().and_then(|m| m.modified()) {
            Ok(m) => m,
            Err(_) => continue,
        };
        if modified < cutoff {
            match fs::remove_file(&path) {
                Ok(_) => {
                    debug!(path = %path.display(), "vigogit: removed stale bundle")
                }
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "vigogit: failed to remove stale bundle")
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn tmp_dir_joins_base() {
        let base = Path::new("/var/lib/vigo/vigogit");
        assert_eq!(tmp_dir(base), PathBuf::from("/var/lib/vigo/vigogit/tmp"));
    }

    #[test]
    fn cleanup_handles_missing_dir() {
        let base = tempdir().unwrap();
        // Don't create tmp dir.
        cleanup_stale_bundles(base.path()); // must not panic
    }

    #[test]
    fn cleanup_keeps_fresh_bundles() {
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        fs::create_dir_all(&tmp).unwrap();
        let fresh = tmp.join("fresh.bundle");
        fs::write(&fresh, b"new").unwrap();

        cleanup_stale_bundles(base.path());

        assert!(fresh.exists(), "freshly written bundles should survive");
    }

    #[test]
    fn cleanup_removes_stale_bundles_via_future_cutoff() {
        // Use a cutoff in the future so every file looks "older than"
        // the cutoff — this exercises the removal path without having
        // to manipulate mtimes.
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        fs::create_dir_all(&tmp).unwrap();

        let stale = tmp.join("stale.bundle");
        let notes = tmp.join("notes.txt");
        fs::write(&stale, b"data").unwrap();
        fs::write(&notes, b"keep").unwrap();

        let future = SystemTime::now() + Duration::from_secs(60);
        cleanup_stale_bundles_with_cutoff(base.path(), future);

        assert!(
            !stale.exists(),
            ".bundle files should be removed when cutoff is future"
        );
        assert!(
            notes.exists(),
            "non-.bundle files should be preserved regardless"
        );
    }

    #[test]
    fn cleanup_ignores_subdirs() {
        let base = tempdir().unwrap();
        let tmp = tmp_dir(base.path());
        let subdir = tmp.join("subdir.bundle"); // dir, not file
        fs::create_dir_all(&subdir).unwrap();

        let future = SystemTime::now() + Duration::from_secs(60);
        cleanup_stale_bundles_with_cutoff(base.path(), future);

        // Subdirectory should not be removed (remove_file would fail).
        assert!(subdir.exists());
    }

    #[test]
    #[cfg(unix)]
    fn ensure_tmp_dir_creates_with_sticky_world_writable() {
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        let dir = ensure_tmp_dir(base.path()).unwrap();
        assert!(dir.exists());
        let mode = fs::metadata(&dir).unwrap().permissions().mode();
        // Mask off file-type bits, leave the bottom 12 bits.
        assert_eq!(mode & 0o7777, 0o1777);
    }

    #[test]
    #[cfg(unix)]
    fn ensure_tmp_dir_resets_mode_on_existing_dir() {
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        let dir = tmp_dir(base.path());
        fs::create_dir_all(&dir).unwrap();
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).unwrap();
        ensure_tmp_dir(base.path()).unwrap();
        let mode = fs::metadata(&dir).unwrap().permissions().mode();
        assert_eq!(mode & 0o7777, 0o1777);
    }

    #[test]
    fn state_load_missing_returns_default() {
        let base = tempdir().unwrap();
        let s = State::load(base.path());
        assert!(s.labels.is_empty());
        assert_eq!(s.version, STATE_VERSION);
    }

    #[test]
    fn state_save_then_load_roundtrip() {
        let base = tempdir().unwrap();
        let mut s = State::default();
        s.record("vigogit:host:user:proj", "deadbeef", "cafef00d");
        s.record("vigogit:host:user:other", "11111111", "22222222");
        s.save(base.path()).unwrap();

        let loaded = State::load(base.path());
        assert_eq!(loaded.version, STATE_VERSION);
        assert_eq!(loaded.labels.len(), 2);
        assert_eq!(
            loaded.labels.get("vigogit:host:user:proj").unwrap().git_sha,
            "deadbeef"
        );
        assert_eq!(
            loaded
                .labels
                .get("vigogit:host:user:proj")
                .unwrap()
                .blob_sha,
            "cafef00d"
        );
    }

    #[test]
    fn state_save_uses_atomic_rename_no_tmp_left_behind() {
        let base = tempdir().unwrap();
        let mut s = State::default();
        s.record("vigogit:h:u:p", "abc", "def");
        s.save(base.path()).unwrap();

        assert!(state_path(base.path()).exists());
        assert!(!base.path().join("state.json.tmp").exists());
    }

    #[test]
    #[cfg(unix)]
    fn state_save_writes_mode_0600() {
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        let s = State::default();
        s.save(base.path()).unwrap();
        let mode = fs::metadata(state_path(base.path()))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
    }

    #[test]
    fn state_corrupt_json_treated_as_missing() {
        let base = tempdir().unwrap();
        fs::write(state_path(base.path()), b"not json {{{").unwrap();
        let s = State::load(base.path());
        assert!(s.labels.is_empty());
    }

    #[test]
    fn state_forget_removes_label() {
        let mut s = State::default();
        s.record("vigogit:h:u:p", "abc", "def");
        assert!(s.labels.contains_key("vigogit:h:u:p"));
        s.forget("vigogit:h:u:p");
        assert!(!s.labels.contains_key("vigogit:h:u:p"));
    }

    #[test]
    fn state_record_overwrites_existing_label() {
        let mut s = State::default();
        s.record("vigogit:h:u:p", "old_git", "old_blob");
        s.record("vigogit:h:u:p", "new_git", "new_blob");
        let entry = s.labels.get("vigogit:h:u:p").unwrap();
        assert_eq!(entry.git_sha, "new_git");
        assert_eq!(entry.blob_sha, "new_blob");
    }

    // --- traits snapshot -----------------------------------------

    fn sample_snapshot() -> TraitsSnapshot {
        TraitsSnapshot {
            version: TRAITS_SNAPSHOT_VERSION,
            updated_at: 1_744_560_000,
            sources: vec![SourceTraitEntry {
                label: "vigogit:h:u:p".to_string(),
                paused: false,
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/vigogit/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "vigogit:h:u:broken".to_string(),
                reason: "bundle exceeds cap".to_string(),
            }],
        }
    }

    #[test]
    fn traits_snapshot_load_missing_returns_none() {
        let base = tempdir().unwrap();
        let got = TraitsSnapshot::load(base.path()).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn traits_snapshot_save_then_load_roundtrip() {
        let base = tempdir().unwrap();
        let s = sample_snapshot();
        s.save(base.path()).unwrap();

        let loaded = TraitsSnapshot::load(base.path()).unwrap().unwrap();
        assert_eq!(loaded, s);
    }

    #[test]
    fn traits_snapshot_save_uses_atomic_rename_no_tmp_left_behind() {
        let base = tempdir().unwrap();
        sample_snapshot().save(base.path()).unwrap();

        assert!(traits_snapshot_path(base.path()).exists());
        assert!(!base.path().join("traits.json.tmp").exists());
    }

    #[test]
    #[cfg(unix)]
    fn traits_snapshot_save_writes_mode_0600() {
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        sample_snapshot().save(base.path()).unwrap();
        let mode = fs::metadata(traits_snapshot_path(base.path()))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o600);
    }

    #[test]
    fn traits_snapshot_corrupt_json_treated_as_missing() {
        let base = tempdir().unwrap();
        fs::write(traits_snapshot_path(base.path()), b"not json").unwrap();
        let got = TraitsSnapshot::load(base.path()).unwrap();
        assert!(got.is_none());
    }

    #[test]
    fn traits_snapshot_default_has_current_version() {
        let s = TraitsSnapshot::default();
        assert_eq!(s.version, TRAITS_SNAPSHOT_VERSION);
        assert_eq!(s.updated_at, 0);
        assert!(s.sources.is_empty());
        assert!(s.source_errors.is_empty());
        assert!(s.publish_errors.is_empty());
    }

    #[test]
    fn traits_snapshot_serialization_omits_none_git_sha() {
        let entry = SourceTraitEntry {
            label: "vigogit:h:u:p".to_string(),
            paused: true,
            git_sha: None,
            blob_sha: None,
        };
        let json = serde_json::to_string(&entry).unwrap();
        assert!(!json.contains("git_sha"), "None fields should be skipped");
        assert!(!json.contains("blob_sha"));
    }

    #[test]
    fn unix_seconds_now_is_reasonable() {
        let s = unix_seconds_now();
        // After 2026-01-01 and before 2100-01-01 — sanity bounds.
        assert!(s > 1_767_225_600);
        assert!(s < 4_102_444_800);
    }
}
