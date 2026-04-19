// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigorepo on-disk state.
//!
//! The source side persists per-label seeding state so the agent does
//! not re-publish an identical bundle after a restart. State lives at
//! `<base>/state.json` (where `base` is
//! [`crate::platform::paths::default_vigorepo_dir`]).
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
use tracing::{debug, info, warn};

/// Age at which an orphan bundle file under `tmp/` is considered
/// stale and eligible for removal on agent startup.
pub const STALE_BUNDLE_AGE: Duration = Duration::from_secs(60 * 60);

/// Resolve the bundle scratch directory relative to the vigorepo
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
    /// Whether the most recent successful publish was a full bundle
    /// (`true`) or an incremental delta from the previous `git_sha`
    /// (`false`). Surfaced through the `vigorepo` trait so operators
    /// can tell at a glance why a seed is only a few KB: an
    /// incremental is expected to be small; a full bundle should be
    /// repo-sized.
    ///
    /// `None` on state files written by agents older than 0.24.1;
    /// the next publish fills it in.
    #[serde(default)]
    pub last_publish_full: Option<bool>,
    /// Size in bytes of the most recent successful bundle file.
    /// Surfaced alongside `last_publish_full` for the same reason.
    /// `None` on pre-0.24.1 state files.
    #[serde(default)]
    pub last_bundle_size: Option<u64>,
}

/// Current state-file schema version.
pub const STATE_VERSION: u32 = 1;

/// Persistent vigorepo source state. Versioned to allow schema
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
                    "vigorepo: failed to read state.json; starting fresh"
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
                    "vigorepo: corrupt state.json; starting fresh"
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
    pub fn record(
        &mut self,
        label: &str,
        git_sha: &str,
        blob_sha: &str,
        full: bool,
        bundle_size: u64,
    ) {
        self.labels.insert(
            label.to_string(),
            LabelState {
                git_sha: git_sha.to_string(),
                blob_sha: blob_sha.to_string(),
                last_publish_full: Some(full),
                last_bundle_size: Some(bundle_size),
            },
        );
    }

    /// Drop the state for a single label, used by the self-healing
    /// reset when a prior bundle's blob is no longer in the manifest.
    pub fn forget(&mut self, label: &str) {
        self.labels.remove(label);
    }

    /// Drop state for every tracked label. Called at agent startup to
    /// guarantee the next publish pass generates a full bundle rather
    /// than a delta whose base commit might no longer be reachable to
    /// any receiver. Bounds the window of "stale baseline" staleness
    /// to the lifetime of a single agent process — any restart
    /// forces a fresh baseline.
    ///
    /// Cost: one full bundle per agent restart per source, regardless
    /// of whether the source changed. Acceptable tradeoff for the
    /// correctness guarantee.
    pub fn reset_all_labels(&mut self) {
        self.labels.clear();
    }
}

// --- traits snapshot -------------------------------------------------
//
// The reactor writes a `TraitsSnapshot` at the end of each pass. The
// `vigorepo` trait collector (`agent/src/traits/vigorepo.rs`) reads it
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
///
/// The sidecar fields (`max_mirrors`, `tags`, `description`) carry
/// the validated values the operator committed. These flow to the
/// server through the `vigorepo` trait report (see
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
    /// the `vigorepo.sources[]` trait for observability; the server
    /// ingests this field tolerantly (unknown fields are dropped) so
    /// an older server sees it as noise and a newer one could
    /// surface it in the UI.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub extra_mirrors: Vec<String>,
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
    /// the trait so the `/vigorepo` page can render "where will this
    /// repo go" without querying every envoy's memory directly.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_mirrors: Vec<String>,
}

/// A single discovery-error record. Mirrors
/// [`crate::vigorepo::source::SourceError`] but with a string path so
/// the snapshot serializes cleanly across platforms.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SourceErrorEntry {
    pub path: String,
    pub reason: String,
}

/// A single publish-error record. Keyed by the swarm label for
/// correlation with the `vigorepo` trait report.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PublishErrorEntry {
    pub label: String,
    pub reason: String,
}

/// Per-mirror entry in a traits snapshot — receiver-side view of a
/// materialized bare repo. Populated from the on-disk
/// [`ReceiverState`] during every reactor tick and surfaced via the
/// `vigorepo.mirrors[]` trait so the server can join it with the
/// publisher-side `selected_mirrors` to show whole-fleet mirror
/// coverage in the `/vigorepo` UI.
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
    /// True when `now - last_refreshed > STALE_THRESHOLD_SECS`.
    /// Precomputed by the receiver tick so consumers don't have to
    /// carry the threshold constant.
    pub stale: bool,
    pub next_gc_at: u64,
}

/// Snapshot of source-side vigorepo state, written at the end of each
/// reactor pass and consumed by the `vigorepo` trait collector.
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
    /// templates can read `.Traits.vigorepo.active_sources` without
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
    /// than [`super::STALE_THRESHOLD_SECS`] (30 days). Surfaced as
    /// the `vigorepo.stale_mirrors` trait so operators can see the
    /// list in the UI / `vigocli` without log-scraping. List of full
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
    /// fleet-wide vigorepo mesh in the UI.
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
                    "vigorepo: corrupt traits.json; treating as missing"
                );
                Ok(None)
            }
        }
    }

    /// Save the snapshot via temp-file + rename. Mode `0644` —
    /// world-readable so the user-facing `vigo repo source list`
    /// command (which runs as any unprivileged user) can read it
    /// without sudo. Contents are not sensitive: labels, owner
    /// usernames, sidecar config, git/blob shas. All of this
    /// already flows to the server via trait reports and to other
    /// envoys via the swarm manifest, so making the on-disk file
    /// world-readable does not expose anything new. Caller is
    /// expected to hold the only writer (the reactor task).
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

// --- receiver state -------------------------------------------------
//
// The receiver side persists which mirrors are currently materialized
// on this envoy. State.json owns the trust boundary: if a label has
// an entry here, vigorepo owns the working tree path and bare repo
// path and may replace them freely. If a label is not in state, the
// receiver refuses to touch any pre-existing files at those paths.
// See `agent/src/vigorepo/receiver.rs` for the full trust rationale.

/// Current receiver-state schema version. History:
/// - **v1**: pre-0.22.0, included `uid`, `gid`, `working_tree_path`.
/// - **v2**: 0.22.0 working-tree pipeline removed; `MirrorState`
///   shrank to bare-repo-only.
/// - **v3**: 0.22.5 staleness tracking; `materialized_at` renamed to
///   `last_refreshed` to clarify that it updates on every successful
///   refetch (not just the first materialization). Semantic change,
///   not a new field — the old timestamp was always overwritten on
///   re-materialize, so no information is lost.
///
/// Old files (any version < current) are rejected by `load()` and
/// treated as a fresh empty state; the next receiver tick
/// re-materializes every claimed mirror from bundles still in the
/// local swarm cache. No data loss because the bare repos on disk
/// are untouched and the rebuild runs against them.
pub const RECEIVER_STATE_VERSION: u32 = 3;

/// One materialized vigorepo mirror on this envoy. Tracked from
/// successful first materialization through eventual wipe.
///
/// As of 0.22.0 the receiver holds only the root-owned bare repo —
/// there is no user-owned working tree anymore. Recovery happens
/// via `vigo repo recover`, which clones the bare repo into a
/// user-owned path on demand. Consequently this struct no longer
/// carries `uid`, `gid`, or `working_tree_path`: they're derivable
/// from the `user` field on demand, and nothing in the receiver
/// pipeline needs to run as the target user anymore.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct MirrorState {
    /// Full content-seed label (`vigorepo:<seeder>:<user>:<project>`).
    pub label: String,
    /// `<seeder>` segment — the source envoy that publishes this repo.
    pub seeder: String,
    /// `<user>` segment — the Unix user the repo belongs to. Used by
    /// the recovery command to filter "my mirrors" for unprivileged
    /// callers.
    pub user: String,
    /// `<project>` segment — repo name. Used as the default recovery
    /// destination directory name under `~user/vigorepo/`.
    pub project: String,
    /// Root-owned bare repo path
    /// (`/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git`).
    pub bare_repo_path: PathBuf,
    /// SHA256 of the swarm content seed that produced the current
    /// state. The reactor compares this against the manifest entry's
    /// sha256 to detect when a re-materialization is needed.
    pub blob_sha: String,
    /// Git HEAD sha after the most recent successful fetch from the
    /// bundle.
    pub git_sha: String,
    /// Unix seconds of the most recent successful refresh. Updated
    /// every time a new bundle is fetched and materialized. Drives
    /// the staleness detector: if `now - last_refreshed` exceeds
    /// [`super::STALE_THRESHOLD_SECS`], the receiver emits a warning
    /// and surfaces the label through the `vigorepo.stale_mirrors`
    /// trait. Renamed from `materialized_at` in 0.22.5 to clarify
    /// that it always reflects the most recent refresh, not the
    /// first materialization.
    pub last_refreshed: u64,
    /// Unix seconds at which this mirror is next due for
    /// `git gc --auto`. Set to a per-repo random value within the
    /// 6-hour window at first materialization, then refreshed by
    /// the gc sweep.
    pub next_gc_at: u64,
}

impl MirrorState {
    /// Returns true if this mirror has not been refreshed in longer
    /// than [`super::STALE_THRESHOLD_SECS`]. Advisory only — staleness
    /// never triggers automatic eviction.
    pub fn is_stale(&self, now: u64) -> bool {
        now.saturating_sub(self.last_refreshed) > super::STALE_THRESHOLD_SECS
    }

    /// Project this on-disk receiver state into the narrower
    /// `MirrorTraitEntry` shape for upstream reporting. Omits
    /// `bare_repo_path` (reconstructible from seeder/user/project)
    /// to keep the trait payload small.
    pub fn to_trait_entry(&self, now: u64) -> MirrorTraitEntry {
        MirrorTraitEntry {
            label: self.label.clone(),
            seeder: self.seeder.clone(),
            user: self.user.clone(),
            project: self.project.clone(),
            git_sha: self.git_sha.clone(),
            blob_sha: self.blob_sha.clone(),
            last_refreshed: self.last_refreshed,
            stale: self.is_stale(now),
            next_gc_at: self.next_gc_at,
        }
    }
}

/// Persistent receiver state — what we have materialized and what we
/// are in the process of wiping. Atomic-rename-saved like the source
/// state file, mode `0600`, root-owned.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ReceiverState {
    /// Schema version; bump on incompatible changes.
    #[serde(default = "default_receiver_version")]
    pub version: u32,
    /// label → mirror state for every currently-materialized mirror.
    #[serde(default)]
    pub mirrors: BTreeMap<String, MirrorState>,
    /// label → unix seconds when the label first dropped out of
    /// the receiver's [`super::receiver::ClaimsHandle`]. Receiver
    /// applies a hysteresis window before actually wiping so that a
    /// transient claim absence (source offline, LAN multicast lag)
    /// doesn't cause a wipe.
    #[serde(default)]
    pub pending_wipes: BTreeMap<String, u64>,
}

fn default_receiver_version() -> u32 {
    RECEIVER_STATE_VERSION
}

impl Default for ReceiverState {
    fn default() -> Self {
        Self {
            version: RECEIVER_STATE_VERSION,
            mirrors: BTreeMap::new(),
            pending_wipes: BTreeMap::new(),
        }
    }
}

/// Resolve the receiver state file path.
pub fn receiver_state_path(base: &Path) -> PathBuf {
    base.join("receiver_state.json")
}

impl ReceiverState {
    /// Load receiver state from disk. Missing file, corrupt JSON, or
    /// a version mismatch all resolve to a fresh empty state — the
    /// worst case is the reactor re-materializes a few mirrors on
    /// the next tick from bundles still in the local swarm cache.
    /// The version mismatch path is how 0.22.0 handles the schema
    /// break from v1: old files get rejected cleanly, the bare repos
    /// from the old layout get removed by orphan cleanup, and the
    /// next tick rebuilds them under the new bare-repo-only layout.
    pub fn load(base: &Path) -> Self {
        let path = receiver_state_path(base);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Self::default(),
            Err(e) => {
                warn!(
                    path = %path.display(),
                    error = %e,
                    "vigorepo: failed to read receiver_state.json; starting fresh"
                );
                return Self::default();
            }
        };
        match serde_json::from_slice::<ReceiverState>(&bytes) {
            Ok(s) if s.version == RECEIVER_STATE_VERSION => s,
            Ok(s) => {
                warn!(
                    path = %path.display(),
                    found_version = s.version,
                    expected_version = RECEIVER_STATE_VERSION,
                    "vigorepo: receiver_state.json schema version mismatch; starting fresh"
                );
                Self::default()
            }
            Err(e) => {
                // Before calling it corrupt, check if this is a known
                // older schema (e.g. v2→v3 renamed materialized_at →
                // last_refreshed). The version-check branch above can't
                // catch these because serde's strict deserialization
                // fails on the unrecognized field before we read
                // `version`. A quick Value re-parse tells us whether
                // the file is an older but structurally valid schema.
                if let Ok(raw) = serde_json::from_slice::<serde_json::Value>(&bytes) {
                    let raw_ver = raw
                        .get("version")
                        .and_then(|v| v.as_u64())
                        .unwrap_or(0) as u32;
                    let has_old_field = raw
                        .get("mirrors")
                        .and_then(|m| m.as_object())
                        .map_or(false, |obj| {
                            obj.values()
                                .any(|v| v.get("materialized_at").is_some())
                        });
                    if raw_ver != RECEIVER_STATE_VERSION || has_old_field {
                        info!(
                            path = %path.display(),
                            found_version = raw_ver,
                            expected_version = RECEIVER_STATE_VERSION,
                            "vigorepo: receiver_state.json has an older schema; starting fresh"
                        );
                        return Self::default();
                    }
                }
                warn!(
                    path = %path.display(),
                    error = %e,
                    "vigorepo: corrupt receiver_state.json; starting fresh"
                );
                Self::default()
            }
        }
    }

    /// Save receiver state via temp-file + rename. Mode `0644` —
    /// world-readable so the user-facing
    /// `vigo repo {list,show,log,branches,peers}` commands
    /// (which run as any unprivileged user) can read it without
    /// sudo. Contents are not sensitive: labels, bare repo paths,
    /// blob/git shas, timestamps. The bare repo filesystem layout
    /// under `/var/lib/vigo/vigorepo/mirrors/` is already
    /// world-traversable (directories are 0755), so the file
    /// merely serializes what the filesystem already reveals.
    pub fn save(&self, base: &Path) -> std::io::Result<()> {
        fs::create_dir_all(base)?;
        let final_path = receiver_state_path(base);
        let tmp_path = base.join("receiver_state.json.tmp");

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
            warn!(dir = %dir.display(), error = %e, "vigorepo: cannot read tmp dir");
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
                    debug!(path = %path.display(), "vigorepo: removed stale bundle")
                }
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "vigorepo: failed to remove stale bundle")
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
        let base = Path::new("/var/lib/vigo/vigorepo");
        assert_eq!(tmp_dir(base), PathBuf::from("/var/lib/vigo/vigorepo/tmp"));
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
        s.record("vigorepo:host:user:proj", "deadbeef", "cafef00d", true, 12_345);
        s.record("vigorepo:host:user:other", "11111111", "22222222", false, 678);
        s.save(base.path()).unwrap();

        let loaded = State::load(base.path());
        assert_eq!(loaded.version, STATE_VERSION);
        assert_eq!(loaded.labels.len(), 2);
        let proj = loaded.labels.get("vigorepo:host:user:proj").unwrap();
        assert_eq!(proj.git_sha, "deadbeef");
        assert_eq!(proj.blob_sha, "cafef00d");
        assert_eq!(proj.last_publish_full, Some(true));
        assert_eq!(proj.last_bundle_size, Some(12_345));
        let other = loaded.labels.get("vigorepo:host:user:other").unwrap();
        assert_eq!(other.last_publish_full, Some(false));
        assert_eq!(other.last_bundle_size, Some(678));
    }

    #[test]
    fn state_load_accepts_legacy_file_without_bundle_fields() {
        // Backcompat: an older state.json written by 0.24.x lacks the
        // last_publish_full / last_bundle_size fields. Deserialization
        // must fill them with None so the next publish populates them
        // naturally — no bootstrap step required.
        let base = tempdir().unwrap();
        let legacy = br#"{
            "version": 1,
            "labels": {
                "vigorepo:host:user:proj": {
                    "git_sha": "deadbeef",
                    "blob_sha": "cafef00d"
                }
            }
        }"#;
        fs::write(state_path(base.path()), legacy).unwrap();
        let loaded = State::load(base.path());
        let entry = loaded.labels.get("vigorepo:host:user:proj").unwrap();
        assert_eq!(entry.git_sha, "deadbeef");
        assert!(entry.last_publish_full.is_none());
        assert!(entry.last_bundle_size.is_none());
    }

    #[test]
    fn state_save_uses_atomic_rename_no_tmp_left_behind() {
        let base = tempdir().unwrap();
        let mut s = State::default();
        s.record("vigorepo:h:u:p", "abc", "def", true, 1);
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
        s.record("vigorepo:h:u:p", "abc", "def", true, 1);
        assert!(s.labels.contains_key("vigorepo:h:u:p"));
        s.forget("vigorepo:h:u:p");
        assert!(!s.labels.contains_key("vigorepo:h:u:p"));
    }

    #[test]
    fn state_record_overwrites_existing_label() {
        let mut s = State::default();
        s.record("vigorepo:h:u:p", "old_git", "old_blob", true, 100);
        s.record("vigorepo:h:u:p", "new_git", "new_blob", false, 50);
        let entry = s.labels.get("vigorepo:h:u:p").unwrap();
        assert_eq!(entry.git_sha, "new_git");
        assert_eq!(entry.blob_sha, "new_blob");
        assert_eq!(entry.last_publish_full, Some(false));
        assert_eq!(entry.last_bundle_size, Some(50));
    }

    #[test]
    fn state_reset_all_labels_drops_everything() {
        // The agent startup hook calls reset_all_labels() to force
        // the next publish pass to generate a full bundle rather
        // than a delta whose base commit might no longer be
        // reachable to receivers. This test pins that the reset
        // unconditionally clears all label entries — any surviving
        // entry would let the publisher generate another delta
        // and reintroduce the stale-baseline bug.
        let mut s = State::default();
        s.record("vigorepo:host1:dan:one", "git1", "blob1", true, 10);
        s.record("vigorepo:host1:dan:two", "git2", "blob2", false, 20);
        s.record("vigorepo:host2:ann:project", "git3", "blob3", true, 30);
        assert_eq!(s.labels.len(), 3);

        s.reset_all_labels();

        assert!(
            s.labels.is_empty(),
            "reset_all_labels must clear every entry, got {:?}",
            s.labels
        );
    }

    #[test]
    fn state_reset_all_labels_safe_on_empty() {
        let mut s = State::default();
        s.reset_all_labels();
        assert!(s.labels.is_empty());
    }

    #[test]
    fn state_reset_all_labels_persists_through_save_load_cycle() {
        // Verify the full agent-startup flow: load state with
        // existing entries, reset, save, reload — the reloaded
        // state must still be empty, proving the reset was
        // persisted to disk.
        let base = tempdir().unwrap();
        {
            let mut s = State::default();
            s.record("vigorepo:h:u:p", "abc", "def", true, 1);
            s.save(base.path()).unwrap();
        }
        let mut loaded = State::load(base.path());
        assert_eq!(loaded.labels.len(), 1);
        loaded.reset_all_labels();
        loaded.save(base.path()).unwrap();

        let reloaded = State::load(base.path());
        assert!(
            reloaded.labels.is_empty(),
            "reset must persist across save/load cycle, got {:?}",
            reloaded.labels
        );
    }

    // --- traits snapshot -----------------------------------------

    fn sample_snapshot() -> TraitsSnapshot {
        TraitsSnapshot {
            version: TRAITS_SNAPSHOT_VERSION,
            updated_at: 1_744_560_000,
            sources: vec![SourceTraitEntry {
                label: "vigorepo:h:u:p".to_string(),
                paused: false,
                max_mirrors: 5,
                tags: vec!["personal".to_string()],
                description: "my repo".to_string(),
                include_envoys: vec![],
                exclude_envoys: vec![],
                when: String::new(),
                extra_mirrors: vec![],
                git_sha: Some("deadbeef".to_string()),
                blob_sha: Some("cafef00d".to_string()),
                last_publish_full: Some(true),
                last_bundle_size: Some(4096),
                selected_mirrors: vec![],
            }],
            active_sources: 1,
            paused_sources: 0,
            source_errors: vec![SourceErrorEntry {
                path: "/home/dan/vigorepo/bad".to_string(),
                reason: "invalid sidecar".to_string(),
            }],
            publish_errors: vec![PublishErrorEntry {
                label: "vigorepo:h:u:broken".to_string(),
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
        // made `vigo repo source list` (a user-facing command) fail
        // with EACCES when run unprivileged. 0.22.1 relaxed it to 0644
        // because the contents are non-sensitive and flow through the
        // server trait reports and swarm manifest anyway.
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
            label: "vigorepo:h:u:p".to_string(),
            paused: true,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            extra_mirrors: vec![],
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
        // the server-side `vigorepomesh` join can distinguish full vs
        // incremental bundles when rendering a vigorepo seed's status.
        let entry = SourceTraitEntry {
            label: "vigorepo:h:u:p".to_string(),
            paused: false,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            extra_mirrors: vec![],
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
        let older = r#"{"label":"vigorepo:h:u:p","paused":false,"max_mirrors":3,"git_sha":"abc","blob_sha":"def"}"#;
        let entry: SourceTraitEntry = serde_json::from_str(older).unwrap();
        assert!(entry.last_publish_full.is_none());
        assert!(entry.last_bundle_size.is_none());
    }

    #[test]
    fn source_trait_selected_mirrors_round_trips() {
        let entry = SourceTraitEntry {
            label: "vigorepo:h:u:p".to_string(),
            paused: false,
            max_mirrors: 3,
            tags: vec![],
            description: String::new(),
            include_envoys: vec![],
            exclude_envoys: vec![],
            when: String::new(),
            extra_mirrors: vec![],
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
        let older = r#"{"label":"vigorepo:h:u:p","paused":false,"max_mirrors":3}"#;
        let entry: SourceTraitEntry = serde_json::from_str(older).unwrap();
        assert!(entry.selected_mirrors.is_empty());
    }

    #[test]
    fn mirror_trait_entry_round_trips() {
        let m = MirrorTraitEntry {
            label: "vigorepo:src:dan:proj".to_string(),
            seeder: "src".to_string(),
            user: "dan".to_string(),
            project: "proj".to_string(),
            git_sha: "cafef00d".to_string(),
            blob_sha: "deadbeef".to_string(),
            last_refreshed: 1_744_560_000,
            stale: false,
            next_gc_at: 1_744_582_000,
        };
        let json = serde_json::to_string(&m).unwrap();
        let back: MirrorTraitEntry = serde_json::from_str(&json).unwrap();
        assert_eq!(back, m);
    }

    #[test]
    fn mirror_state_to_trait_entry_computes_stale() {
        let m = sample_mirror();
        let recent = m.to_trait_entry(m.last_refreshed + 10);
        assert!(!recent.stale);
        let ancient = m.to_trait_entry(m.last_refreshed + super::super::STALE_THRESHOLD_SECS + 1);
        assert!(ancient.stale);
    }

    #[test]
    fn unix_seconds_now_is_reasonable() {
        let s = unix_seconds_now();
        // After 2026-01-01 and before 2100-01-01 — sanity bounds.
        assert!(s > 1_767_225_600);
        assert!(s < 4_102_444_800);
    }

    // --- receiver state ------------------------------------------

    fn sample_mirror() -> MirrorState {
        MirrorState {
            label: "vigorepo:host:dan:proj".to_string(),
            seeder: "host".to_string(),
            user: "dan".to_string(),
            project: "proj".to_string(),
            bare_repo_path: PathBuf::from(
                "/var/lib/vigo/vigorepo/mirrors/host/dan/proj.git",
            ),
            blob_sha: "deadbeef".to_string(),
            git_sha: "cafef00d".to_string(),
            last_refreshed: 1_744_560_000,
            next_gc_at: 1_744_582_000,
        }
    }

    #[test]
    fn receiver_state_load_missing_returns_default() {
        let base = tempdir().unwrap();
        let s = ReceiverState::load(base.path());
        assert_eq!(s.version, RECEIVER_STATE_VERSION);
        assert!(s.mirrors.is_empty());
        assert!(s.pending_wipes.is_empty());
    }

    #[test]
    fn receiver_state_save_then_load_roundtrip() {
        let base = tempdir().unwrap();
        let mut s = ReceiverState::default();
        let m = sample_mirror();
        s.mirrors.insert(m.label.clone(), m.clone());
        s.pending_wipes
            .insert("vigorepo:host:dan:vanished".to_string(), 1_744_560_000);
        s.save(base.path()).unwrap();

        let loaded = ReceiverState::load(base.path());
        assert_eq!(loaded.version, RECEIVER_STATE_VERSION);
        assert_eq!(loaded.mirrors.len(), 1);
        assert_eq!(loaded.pending_wipes.len(), 1);
        assert_eq!(loaded.mirrors.get(&m.label), Some(&m));
    }

    #[test]
    fn receiver_state_save_uses_atomic_rename_no_tmp_left_behind() {
        let base = tempdir().unwrap();
        ReceiverState::default().save(base.path()).unwrap();
        assert!(receiver_state_path(base.path()).exists());
        assert!(!base.path().join("receiver_state.json.tmp").exists());
    }

    #[test]
    #[cfg(unix)]
    fn receiver_state_save_writes_mode_0644() {
        // Regression: 0.22.0 wrote this file 0600 root-owned, which
        // caused `vigo repo list` and friends to fail with
        // EACCES when an unprivileged user ran them. 0.22.1 relaxed
        // it to 0644 because the contents are non-sensitive and the
        // bare repo layout under /var/lib/vigo/vigorepo/mirrors/ is
        // already world-traversable — this file just serializes
        // what the filesystem already reveals.
        use std::os::unix::fs::PermissionsExt;
        let base = tempdir().unwrap();
        ReceiverState::default().save(base.path()).unwrap();
        let mode = fs::metadata(receiver_state_path(base.path()))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o644);
    }

    #[test]
    fn receiver_state_corrupt_json_treated_as_default() {
        let base = tempdir().unwrap();
        fs::write(receiver_state_path(base.path()), b"not json").unwrap();
        let s = ReceiverState::load(base.path());
        assert!(s.mirrors.is_empty());
    }

    #[test]
    fn receiver_state_v1_file_rejected_by_version_check() {
        // 0.22.0 breaks v1 → v2. A hand-crafted v1 file (shape
        // matches the old MirrorState schema but reports version=1)
        // must be rejected cleanly so the receiver starts fresh and
        // re-materializes into the new layout on the next tick.
        let base = tempdir().unwrap();
        let v1_json = br#"{
            "version": 1,
            "mirrors": {
                "vigorepo:host:dan:proj": {
                    "label": "vigorepo:host:dan:proj",
                    "seeder": "host",
                    "user": "dan",
                    "project": "proj",
                    "uid": 1000,
                    "gid": 1000,
                    "bare_repo_path": "/var/lib/vigo/vigorepo/mirrors/host/dan/proj.git",
                    "working_tree_path": "/home/dan/vigorepo/proj",
                    "blob_sha": "deadbeef",
                    "git_sha": "cafef00d",
                    "materialized_at": 1744560000,
                    "next_gc_at": 1744582000
                }
            },
            "pending_wipes": {}
        }"#;
        fs::write(receiver_state_path(base.path()), v1_json).unwrap();
        let s = ReceiverState::load(base.path());
        assert_eq!(
            s.version, RECEIVER_STATE_VERSION,
            "v1 file must be replaced with a fresh default of the current version"
        );
        assert!(
            s.mirrors.is_empty(),
            "v1 mirror data must be dropped on version mismatch"
        );
    }

    #[test]
    fn receiver_state_v2_file_rejected_by_version_check() {
        // 0.22.5 breaks v2 → v3. A hand-crafted v2 file (shape
        // matches the 0.22.0-0.22.4 MirrorState schema with
        // `materialized_at` instead of `last_refreshed`) must be
        // rejected cleanly so the receiver starts fresh and
        // re-materializes every mirror with the new timestamp field.
        // No data loss because the bare repos on disk are untouched
        // and the rebuild runs against them.
        let base = tempdir().unwrap();
        let v2_json = br#"{
            "version": 2,
            "mirrors": {
                "vigorepo:host:dan:proj": {
                    "label": "vigorepo:host:dan:proj",
                    "seeder": "host",
                    "user": "dan",
                    "project": "proj",
                    "bare_repo_path": "/var/lib/vigo/vigorepo/mirrors/host/dan/proj.git",
                    "blob_sha": "deadbeef",
                    "git_sha": "cafef00d",
                    "materialized_at": 1744560000,
                    "next_gc_at": 1744582000
                }
            },
            "pending_wipes": {}
        }"#;
        fs::write(receiver_state_path(base.path()), v2_json).unwrap();
        let s = ReceiverState::load(base.path());
        assert_eq!(
            s.version, RECEIVER_STATE_VERSION,
            "v2 file must be replaced with a fresh default of the current version"
        );
        assert!(
            s.mirrors.is_empty(),
            "v2 mirror data must be dropped on version mismatch"
        );
    }

    #[test]
    fn mirror_state_is_stale_after_threshold() {
        let mut m = sample_mirror();
        let stale_time = 1_000_000_000_u64;
        m.last_refreshed = stale_time;
        // Exactly threshold seconds later: NOT stale (strict >).
        let now = stale_time + super::super::STALE_THRESHOLD_SECS;
        assert!(!m.is_stale(now));
        // One second past: stale.
        let now = stale_time + super::super::STALE_THRESHOLD_SECS + 1;
        assert!(m.is_stale(now));
        // Way past: still stale (no overflow).
        let now = stale_time + 365 * 86400;
        assert!(m.is_stale(now));
    }

    #[test]
    fn mirror_state_is_not_stale_within_threshold() {
        let mut m = sample_mirror();
        let fresh_time = 1_000_000_000_u64;
        m.last_refreshed = fresh_time;
        assert!(!m.is_stale(fresh_time));
        assert!(!m.is_stale(fresh_time + 1));
        assert!(!m.is_stale(fresh_time + 29 * 86400));
    }

    #[test]
    fn mirror_state_is_stale_handles_now_before_last_refreshed() {
        // Clock skew edge case: now < last_refreshed. saturating_sub
        // yields 0, which is not > threshold, so not stale. Must not
        // panic.
        let mut m = sample_mirror();
        m.last_refreshed = 2_000_000_000;
        assert!(!m.is_stale(1_000_000_000));
    }
}
