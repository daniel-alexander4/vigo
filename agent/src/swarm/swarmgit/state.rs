// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Trait-snapshot schema for the `swarmgit` trait collector.
//!
//! The reactor writes a [`TraitsSnapshot`] at the end of each pass to
//! `<base>/traits.json`. The `swarmgit` trait collector
//! (`agent/src/traits/swarmgit.rs`) reads it on the next check-in
//! cycle and ships the contents to the server, giving operators a
//! view of current source publishing state without having to subscribe
//! to a live trait stream.
//!
//! The snapshot is a *replacement*, not an append log — each pass
//! overwrites the file. If the reactor has never ticked (cold start,
//! pre-startup races), the file doesn't exist and the collector
//! returns `Value::Null`, matching `swarm_seeds` on empty.
//!
//! ### What used to live here
//!
//! Pre-0.38.16 this module also owned the legacy publisher `State` /
//! `LabelState` schema (per-label seed-publishing tracking) and the
//! receiver `ReceiverState` / `MirrorState` schema, plus a
//! `cleanup_stale_bundles` helper for the old bundle scratch dir.
//! All of that machinery was retired when ADR-015 step 4 replaced
//! the bundle-based swarmgit pipeline with the helper-daemon +
//! git-remote-vigo + push fan-out architecture (shipped 0.35.6). The
//! trait-snapshot schema survives because the trait collector still
//! reads `traits.json` for fleet-wide source/mirror visibility.

use serde::{Deserialize, Serialize};
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::SystemTime;
use tracing::warn;

/// Current traits-snapshot schema version.
pub const TRAITS_SNAPSHOT_VERSION: u32 = 1;

/// Per-source entry in a traits snapshot. Reflects the latest
/// discovered source plus whatever seeding state exists for it.
///
/// The sidecar fields (`max_mirrors`, `tags`, `description`) carry
/// the validated values the operator committed. These flow to the
/// server through the `swarmgit` trait report (see
/// [`TraitsSnapshot`]) for observability and UI display. Keeping the
/// parsed values here (not raw YAML) means the agent is the single
/// source of truth for sidecar validation.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SourceTraitEntry {
    pub label: String,
    pub paused: bool,
    /// `max_mirrors` from the validated sidecar, already clamped
    /// into `[MIN_MIRRORS, MAX_MIRRORS]`.
    pub max_mirrors: usize,
    /// `tags` from the validated sidecar.
    #[serde(default)]
    pub tags: Vec<String>,
    /// `description` from the validated sidecar.
    #[serde(default)]
    pub description: String,
    /// Optional whitelist of envoy hostname patterns. Empty or absent
    /// means "no whitelist" — every otherwise-eligible envoy qualifies.
    /// Evaluated server-side as shell-style globs; the agent only
    /// carries the values through.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub include_envoys: Vec<String>,
    /// Optional blacklist of envoy hostname patterns. Applied after
    /// `include_envoys` — candidates matching any entry here are
    /// dropped even if they pass the whitelist.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub exclude_envoys: Vec<String>,
    /// Optional when-expression string from the sidecar. Carried
    /// through the trait for observability; not currently evaluated
    /// by the P2P LAN selector. Empty string means no filter.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub when: String,
    /// Explicit cross-subnet mirror targets (Phase 7). Hostnames the
    /// source envoy pushes to directly over mTLS on port 1531, on
    /// top of whatever the LAN multicast selector picks. Surfaced in
    /// the `swarmgit.sources[]` trait for observability; the server
    /// ingests this field tolerantly (unknown fields are dropped) so
    /// an older server sees it as noise and a newer one could
    /// surface it in the UI.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cross_subnet_mirrors: Vec<String>,
    /// Last successfully bundled HEAD sha. `None` if the source has
    /// never successfully published (e.g. first-tick failure).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub git_sha: Option<String>,
    /// SHA256 of the most recent bundle file. `None` in the same
    /// first-publish-failure case.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub blob_sha: Option<String>,
    /// Whether the most recent successful publish was a full bundle
    /// (`Some(true)`) or an incremental delta (`Some(false)`).
    /// `None` when the source has never successfully published, or
    /// on reports from agents older than 0.24.1. Surfaced so the
    /// `/swarm` UI and `vigocli repo` can explain why a seed is only
    /// a few KB — an incremental is expected to be small.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_publish_full: Option<bool>,
    /// Size in bytes of the most recent successful bundle file.
    /// `None` in the same "never published / pre-0.24.1" cases.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_bundle_size: Option<u64>,
    /// LAN hostnames the reactor has most recently chosen as
    /// receivers for this source — a snapshot of the publisher's
    /// `pending_claims` entry for this label. Empty when no LAN peers
    /// qualify yet (fresh fleet, pre-multicast-discovery) or when the
    /// source is paused. Updated every reactor tick; upstreamed via
    /// the trait so the `/swarmgit` page can render "where will this
    /// repo go" without querying every envoy's memory directly.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_mirrors: Vec<String>,
}

/// A single discovery-error record. String path so the snapshot
/// serializes cleanly across platforms.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SourceErrorEntry {
    pub path: String,
    pub reason: String,
}

/// A single publish-error record. Keyed by the swarm label for
/// correlation with the `swarmgit` trait report.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PublishErrorEntry {
    pub label: String,
    pub reason: String,
}

/// Per-mirror entry in a traits snapshot — receiver-side view of a
/// materialized bare repo. Surfaced via the `swarmgit.mirrors[]`
/// trait so the server can join it with the publisher-side
/// `selected_mirrors` to show whole-fleet mirror coverage in the
/// `/swarmgit` UI.
///
/// `bare_repo_path` is intentionally omitted from this struct — it's
/// reconstructible from `(seeder, user, project)` and the receiver's
/// fixed layout, and keeping it off the wire narrows the trait
/// payload.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct MirrorTraitEntry {
    pub label: String,
    pub seeder: String,
    pub user: String,
    pub project: String,
    pub git_sha: String,
    pub blob_sha: String,
    pub last_refreshed: u64,
    /// True when the receiver tick computed the mirror as stale per
    /// its own threshold. Precomputed at trait-write time so consumers
    /// don't have to carry the threshold constant.
    pub stale: bool,
    pub next_gc_at: u64,
    /// Sum of file bytes in the bare repo directory at trait-write
    /// time. Populated by the trait collector so the server-side
    /// /envoys/{id}/swarm-load page can rank per-envoy disk consumers
    /// without a second trip to the envoy. Zero means either the
    /// walk failed (I/O error, bare_repo_path missing) or the repo
    /// genuinely holds no packed objects yet.
    #[serde(default)]
    pub size_bytes: u64,
}

/// Snapshot of source-side swarmgit state, written at the end of each
/// reactor pass and consumed by the `swarmgit` trait collector.
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
    /// templates can read `.Traits.swarmgit.active_sources` without
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
    /// Receiver-side mirrors that have not been refreshed in longer
    /// than the staleness threshold. Surfaced as the
    /// `swarmgit.stale_mirrors` trait so operators can see the list
    /// in the UI / `vigocli` without log-scraping. List of full
    /// labels, not a count — more actionable.
    ///
    /// Kept as a redundant projection of `mirrors[].stale` for
    /// backward compat with consumers written before `mirrors[]`
    /// existed.
    ///
    /// Empty in publish-only deployments (no receiver running).
    #[serde(default)]
    pub stale_mirrors: Vec<String>,
    /// Receiver-side materialized mirrors held by this envoy. Each
    /// entry carries the (label, seeder, user, project) identity plus
    /// the last successful refresh metadata; the server joins this
    /// against publisher-side `selected_mirrors` to render the
    /// fleet-wide swarmgit mesh in the UI.
    ///
    /// Empty in publish-only deployments (no receiver running).
    #[serde(default)]
    pub mirrors: Vec<MirrorTraitEntry>,
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
            stale_mirrors: Vec::new(),
            mirrors: Vec::new(),
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
                    "swarmgit: corrupt traits.json; treating as missing"
                );
                Ok(None)
            }
        }
    }

    /// Save the snapshot via temp-file + rename. Mode `0644` —
    /// world-readable so the user-facing CLI commands (which run as
    /// any unprivileged user) can read it without sudo. Contents are
    /// not sensitive: labels, owner usernames, sidecar config,
    /// git/blob shas. All of this already flows to the server via
    /// trait reports, so making the on-disk file world-readable does
    /// not expose anything new.
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
                f.set_permissions(fs::Permissions::from_mode(0o644))?;
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

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    fn sample_snapshot() -> TraitsSnapshot {
        TraitsSnapshot {
            version: TRAITS_SNAPSHOT_VERSION,
            updated_at: 1_744_560_000,
            sources: vec![SourceTraitEntry {
                label: "swarmgit:h:u:p".to_string(),
                paused: false,
                max_mirrors: 5,
                tags: vec!["personal".to_string()],
                description: "my repo".to_string(),
                include_envoys: vec![],
                exclude_envoys: vec![],
                when: String::new(),
                cross_subnet_mirrors: vec![],
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
                last_publish_full: Some(true),
                last_bundle_size: Some(4096),
                selected_mirrors: vec![],
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/swarmgit/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "swarmgit:h:u:broken".to_string(),
                reason: "bundle exceeds cap".to_string(),
            }],
            stale_mirrors: Vec::new(),
            mirrors: Vec::new(),
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
    fn traits_snapshot_save_writes_mode_0644() {
        // Regression: in 0.22.0 this file was 0600 root-owned, which
        // made user-facing CLI commands fail with EACCES when run
        // unprivileged. 0.22.1 relaxed it to 0644 because the
        // contents are non-sensitive and flow through the server
        // trait reports anyway.
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        sample_snapshot().save(base.path()).unwrap();
        let mode = fs::metadata(traits_snapshot_path(base.path()))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o644);
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
            label: "swarmgit:h:u:p".to_string(),
            paused: true,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            cross_subnet_mirrors: vec![],
            git_sha: None,
            blob_sha: None,
            last_publish_full: None,
            last_bundle_size: None,
            selected_mirrors: vec![],
        };
        let json = serde_json::to_string(&entry).unwrap();
        assert!(!json.contains("git_sha"), "None fields should be skipped");
        assert!(!json.contains("blob_sha"));
        assert!(!json.contains("last_publish_full"));
        assert!(!json.contains("last_bundle_size"));
        assert!(
            !json.contains("selected_mirrors"),
            "empty selected_mirrors should be skipped"
        );
    }

    #[test]
    fn source_trait_bundle_metadata_round_trips() {
        // The two new 0.24.1 fields must survive a JSON round-trip so
        // the server-side `swarmgitmesh` join can distinguish full vs
        // incremental bundles when rendering a swarmgit seed's status.
        let entry = SourceTraitEntry {
            label: "swarmgit:h:u:p".to_string(),
            paused: false,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            cross_subnet_mirrors: vec![],
            git_sha: Some("abc".to_string()),
            blob_sha: Some("def".to_string()),
            last_publish_full: Some(false),
            last_bundle_size: Some(1653),
            selected_mirrors: vec![],
        };
        let json = serde_json::to_string(&entry).unwrap();
        assert!(json.contains("last_publish_full"));
        assert!(json.contains("last_bundle_size"));
        let back: SourceTraitEntry = serde_json::from_str(&json).unwrap();
        assert_eq!(back.last_publish_full, Some(false));
        assert_eq!(back.last_bundle_size, Some(1653));
    }

    #[test]
    fn source_trait_accepts_older_payload_without_bundle_fields() {
        // Forward-compat: a 0.24.x agent ships a trait payload without
        // the new fields. Deserialization must fill them with None.
        let older = r#"{"label":"swarmgit:h:u:p","paused":false,"max_mirrors":3,"git_sha":"abc","blob_sha":"def"}"#;
        let entry: SourceTraitEntry = serde_json::from_str(older).unwrap();
        assert!(entry.last_publish_full.is_none());
        assert!(entry.last_bundle_size.is_none());
    }

    #[test]
    fn source_trait_selected_mirrors_round_trips() {
        let entry = SourceTraitEntry {
            label: "swarmgit:h:u:p".to_string(),
            paused: false,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            cross_subnet_mirrors: vec![],
            git_sha: Some("abc".to_string()),
            blob_sha: Some("def".to_string()),
            last_publish_full: Some(true),
            last_bundle_size: Some(12_345),
            selected_mirrors: vec!["host-a".to_string(), "host-b".to_string()],
        };
        let json = serde_json::to_string(&entry).unwrap();
        assert!(json.contains("selected_mirrors"));
        let back: SourceTraitEntry = serde_json::from_str(&json).unwrap();
        assert_eq!(back.selected_mirrors, entry.selected_mirrors);
    }

    #[test]
    fn source_trait_accepts_older_payload_without_selected_mirrors() {
        // An older agent wrote a snapshot without the new field —
        // deserialization must fill it with the serde default.
        let older = r#"{"label":"swarmgit:h:u:p","paused":false,"max_mirrors":3}"#;
        let entry: SourceTraitEntry = serde_json::from_str(older).unwrap();
        assert!(entry.selected_mirrors.is_empty());
    }

    #[test]
    fn mirror_trait_entry_round_trips() {
        let m = MirrorTraitEntry {
            label: "swarmgit:src:dan:proj".to_string(),
            seeder: "src".to_string(),
            user: "dan".to_string(),
            project: "proj".to_string(),
            git_sha: "cafef00d".to_string(),
            blob_sha: "deadbeef".to_string(),
            last_refreshed: 1_744_560_000,
            stale: false,
            next_gc_at: 1_744_582_000,
            size_bytes: 1_234_567,
        };
        let json = serde_json::to_string(&m).unwrap();
        let back: MirrorTraitEntry = serde_json::from_str(&json).unwrap();
        assert_eq!(back, m);
    }

    #[test]
    fn mirror_trait_entry_defaults_size_when_absent() {
        // Older agents (or snapshots persisted before the field was
        // added) omit size_bytes. Deserialization must fall back to
        // 0 rather than fail — #[serde(default)] does the work.
        let older = r#"{"label":"swarmgit:src:dan:proj","seeder":"src","user":"dan","project":"proj","git_sha":"c","blob_sha":"b","last_refreshed":1,"stale":false,"next_gc_at":2}"#;
        let entry: MirrorTraitEntry = serde_json::from_str(older).unwrap();
        assert_eq!(entry.size_bytes, 0);
    }

    #[test]
    fn unix_seconds_now_is_reasonable() {
        let s = unix_seconds_now();
        // After 2026-01-01 and before 2100-01-01 — sanity bounds.
        assert!(s > 1_767_225_600);
        assert!(s < 4_102_444_800);
    }
}
