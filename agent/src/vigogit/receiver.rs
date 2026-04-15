// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Receiver-side vigogit: materialize swarm-delivered git bundles
//! into hybrid bare-repo + working-tree mirrors.
//!
//! The receiver is the consumer side of the vigogit pipeline. Its
//! input is the per-envoy claim list delivered via
//! `CheckInResponse.vigogit_claims` from the server's in-memory
//! `ClaimTable`. It owns:
//!
//! - **`receiver_state.json`** at `/var/lib/vigo/vigogit/receiver_state.json`,
//!   a label → [`MirrorState`] map plus a label → wipe-pending-since
//!   map (the hysteresis state below).
//! - **A bare git repo per claim** at
//!   `/var/lib/vigo/vigogit/mirrors/<seeder>/<user>/<project>.git`,
//!   root-owned. Stores the full git history; never visible to the user.
//! - **A user-owned working tree per claim** at
//!   `~<user>/vigogit/<project>/`, with no `.git/` directory inside.
//!   Receivers cannot `git status` here — intentional, the user wanting
//!   to resurrect work clones from the bare repo.
//!
//! ### Trust model
//!
//! The receiver acts only on `vigogit_claims`. It never reads raw
//! manifest entries to decide what to mirror. The server has already
//! validated each claim's seeder→hostname correspondence in
//! `server/vigogit/builder.go::OnTraitReport` against authoritative
//! fleet data. Wire-level mTLS peer identity is unavailable on the
//! download path (peer connections deliberately
//! [`disable_verification`][crate::swarm::peer_client] for IP-based
//! peer addressing) and is not needed: content integrity comes from
//! SHA256 verification in the swarm cache layer, and trust comes
//! from the authenticated server channel that delivered the claim.
//!
//! ### State.json is the conflict trust boundary
//!
//! `state.mirrors[label]` records that vigogit owns the working-tree
//! and bare-repo paths for that label. The receiver replaces or
//! deletes them freely. If a label is **not** in state, the receiver
//! refuses to touch any pre-existing files at the working-tree path
//! (defends against trampling a source repo with `.git/`, an
//! operator's manual git work, or unrelated user data). The
//! `vigogit.mirror_failures` trait surfaces conflicts so they're
//! visible without operator action; the operator's recovery is
//! `rm -rf` the conflicting directory and the next reactor tick
//! picks up the now-clean slate.
//!
//! ### Hysteresis on wipe
//!
//! When a label disappears from `vigogit_claims`, the receiver
//! records `pending_wipes[label] = now` and waits
//! [`WIPE_HYSTERESIS_SECS`] seconds before actually wiping. This
//! rides out server restarts (in-memory `ClaimTable` rebuilds from
//! the next round of trait reports), source check-in lag, and
//! transient network issues without forcing the server to maintain
//! persistent state. P2P-friendly. Reappearance clears the pending
//! state.
//!
//! ### Self-healing on bundle update
//!
//! When the source publishes a new bundle the manifest entry's
//! sha256 changes. The receiver detects `state.blob_sha != entry.sha256`,
//! triggers download via the swarm wanted-labels mechanism, runs
//! `git bundle unbundle` against the existing bare repo (incremental
//! — git adds the new objects). If the incremental unbundle fails
//! (e.g. the source self-healed and is now publishing full bundles
//! with no shared base), the receiver wipes the bare repo and
//! retries from scratch on the next tick. End-to-end the system
//! always converges.

use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::{Arc, RwLock};

use rand::Rng;
use tracing::{debug, info, warn};

use super::git_ops::{self, RunAs};
use super::state::{
    unix_seconds_now, MirrorState, ReceiverState,
};
use crate::platform;
use crate::swarm::cache::BlobCache;
use crate::swarm::manifest::{BlobEntry, Manifest};

/// How long a label must be missing from `vigogit_claims` before
/// the receiver actually wipes it. Tuned to ride out:
///
/// - Server restarts (the in-memory `ClaimTable` is rebuilt from
///   incoming trait reports on the next source check-in)
/// - Source envoy check-in lag (typical base interval is minutes)
/// - Transient network blips
///
/// Long enough to survive these, short enough that an intentional
/// removal propagates within a check-in cycle.
pub const WIPE_HYSTERESIS_SECS: u64 = 15 * 60;

/// Lower / upper bound of the per-repo `git gc --auto` jitter
/// window after each successful gc, in seconds. The first gc after
/// materialization is jittered uniformly within
/// `0..GC_FIRST_WINDOW_SECS` so a stampede of new mirrors doesn't
/// all gc in lockstep 6 hours later.
pub const GC_BASE_INTERVAL_SECS: u64 = 6 * 3600;
pub const GC_JITTER_SECS: u64 = 30 * 60;
pub const GC_FIRST_WINDOW_SECS: u64 = GC_BASE_INTERVAL_SECS;

/// Subdirectory of `default_vigogit_dir()` that holds receiver
/// bare repos. Kept distinct from the publisher's `state.json` /
/// `traits.json` / `tmp/` so the orphan-cleanup walker can scan
/// inside `mirrors/` without filtering against state files.
pub const MIRRORS_DIR: &str = "mirrors";

/// Compute the `mirrors/` subdirectory path for the given vigogit
/// state base.
pub fn mirrors_dir(base: &Path) -> PathBuf {
    base.join(MIRRORS_DIR)
}

/// Compute the bare-repo path for a label's components. Layout:
/// `<base>/mirrors/<seeder>/<user>/<project>.git`.
fn bare_repo_path(base: &Path, seeder: &str, user: &str, project: &str) -> PathBuf {
    mirrors_dir(base)
        .join(seeder)
        .join(user)
        .join(format!("{}.git", project))
}

/// Cheap-to-clone handle exposing the receiver's wanted-labels set
/// for cross-task readers and writers. The check-in handler writes
/// here on each `CheckInResponse`; the swarm `manager.rs` and the
/// vigogit reactor read here on each tick.
#[derive(Clone)]
pub struct ClaimsHandle {
    inner: Arc<RwLock<HashSet<String>>>,
}

impl ClaimsHandle {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(RwLock::new(HashSet::new())),
        }
    }

    /// Replace the wanted-labels set with the most recent claim
    /// list from the server. Idempotent. Cheap.
    pub fn update(&self, labels: &[String]) {
        let mut set = self.inner.write().unwrap_or_else(|e| e.into_inner());
        set.clear();
        set.extend(labels.iter().cloned());
    }

    /// Snapshot the current wanted set. The reactor and the swarm
    /// manager both call this on every tick.
    pub fn snapshot(&self) -> HashSet<String> {
        self.inner.read().unwrap_or_else(|e| e.into_inner()).clone()
    }

    /// Return the underlying `Arc<RwLock>` for callers that want
    /// a long-lived borrow without going through the `ClaimsHandle`
    /// clone. Used by `agent/src/main.rs` to hand the shared lock
    /// to the swarm manager (which lives in a different crate
    /// boundary from main.rs and so needs the raw type, not the
    /// vigogit wrapper). Not part of the documented vigogit API.
    pub fn shared(&self) -> Arc<RwLock<HashSet<String>>> {
        self.inner.clone()
    }
}

impl Default for ClaimsHandle {
    fn default() -> Self {
        Self::new()
    }
}

/// Receiver runtime state. Owned by the reactor's blocking worker
/// for single-threaded mutation; never shared across tasks.
pub struct Receiver {
    state: ReceiverState,
    state_dir: PathBuf,
    claims: ClaimsHandle,
    /// In-memory dedup of per-label failure reasons. Keyed by label;
    /// value is the most recent failure reason we logged a WARN for.
    /// When a tick produces the same reason as the last one, we drop
    /// the log to DEBUG — otherwise a single stuck label would flood
    /// the journal with identical WARNs every 5 seconds forever.
    /// Not persisted (log-dedup state, not real state): a process
    /// restart re-emits the first failure, which is desirable because
    /// it gives the operator a fresh timestamp to anchor investigation.
    last_failure_reason: BTreeMap<String, String>,
}

impl Receiver {
    /// Construct a Receiver, loading any persisted state and
    /// running orphan cleanup on the bare-repo tree. Call once at
    /// vigogit startup.
    pub fn new(state_dir: PathBuf, claims: ClaimsHandle) -> Self {
        let state = ReceiverState::load(&state_dir);
        cleanup_orphan_bare_repos(&state, &state_dir);
        Self {
            state,
            state_dir,
            claims,
            last_failure_reason: BTreeMap::new(),
        }
    }

    /// Run one receiver pass. Called from the reactor's blocking
    /// worker after the publish pass. Idempotent: multiple calls
    /// per tick are safe but redundant.
    pub fn tick(&mut self, manifest: &Manifest, cache: &BlobCache) {
        let now = unix_seconds_now();
        let claims = self.claims.snapshot();

        let mut state_changed = false;

        // Phase 1: try to materialize each claimed label. If the
        // bundle isn't in the cache yet, try_materialize is a
        // no-op — manager.rs will download it eventually and the
        // next tick will pick it up.
        for label in &claims {
            if self.try_materialize(label, manifest, cache, now) {
                state_changed = true;
            }
        }

        // Phase 2: handle vanished labels (claims diff). For each
        // currently-materialized label not in claims, mark it
        // pending-wipe with a timestamp; on subsequent ticks where
        // it's still absent, wipe once the hysteresis has elapsed.
        // Reappearing labels clear the pending state.
        let materialized: Vec<String> = self.state.mirrors.keys().cloned().collect();
        for label in materialized {
            if claims.contains(&label) {
                if self.state.pending_wipes.remove(&label).is_some() {
                    state_changed = true;
                    debug!(label = %label, "vigogit receiver: pending wipe cleared (label reappeared)");
                }
                continue;
            }
            match self.state.pending_wipes.get(&label).copied() {
                Some(since) if now.saturating_sub(since) >= WIPE_HYSTERESIS_SECS => {
                    info!(
                        label = %label,
                        wait_secs = now.saturating_sub(since),
                        "vigogit receiver: wipe hysteresis elapsed, removing mirror"
                    );
                    self.wipe(&label);
                    state_changed = true;
                }
                Some(_) => {
                    // Still waiting out the hysteresis window — no-op.
                }
                None => {
                    debug!(
                        label = %label,
                        "vigogit receiver: label vanished from claims, marking pending wipe"
                    );
                    self.state.pending_wipes.insert(label.clone(), now);
                    state_changed = true;
                }
            }
        }

        // Phase 3: gc sweep. Each mirror has a per-repo next_gc_at
        // jittered within a 6-hour window. Run gc on any mirror
        // whose deadline has passed and recompute the next deadline.
        let due: Vec<String> = self
            .state
            .mirrors
            .iter()
            .filter(|(_, m)| m.next_gc_at <= now)
            .map(|(k, _)| k.clone())
            .collect();
        for label in due {
            if self.run_gc(&label, now) {
                state_changed = true;
            }
        }

        if state_changed {
            if let Err(e) = self.state.save(&self.state_dir) {
                warn!(
                    error = %e,
                    "vigogit receiver: failed to persist receiver_state.json"
                );
            }
        }
    }

    /// Returns true if `state` was mutated and needs persisting.
    fn try_materialize(
        &mut self,
        label: &str,
        manifest: &Manifest,
        cache: &BlobCache,
        now: u64,
    ) -> bool {
        // 1. Parse label into segments.
        let (seeder, user, project) = match parse_label(label) {
            Some(t) => t,
            None => {
                self.record_failure(label, "malformed label");
                return false;
            }
        };

        // 2. Look up the active manifest entry by label.
        let entry = match find_active_by_label(manifest, label) {
            Some(e) => e,
            None => {
                // Manifest hasn't gossiped the entry yet. Quiet —
                // this is the steady-state for newly-issued claims
                // before the swarm CRDT has propagated.
                return false;
            }
        };

        // 3. If we already have this exact blob materialized,
        // nothing to do.
        if let Some(existing) = self.state.mirrors.get(label) {
            if existing.blob_sha == entry.sha256 {
                return false;
            }
        }

        // 4. Bundle bytes must be in the local blob cache. If not,
        // bail — the swarm manager will download them via the
        // wanted-labels merge, and the next tick will retry.
        if !cache.has_blob(&entry.sha256) {
            return false;
        }
        let blob_path = cache.blob_path(&entry.sha256);

        // 5. Resolve the owning Unix user.
        let user_info = match platform::users::lookup_user(&user) {
            Some(u) => u,
            None => {
                self.record_failure(label, &format!("user '{}' not found", user));
                return false;
            }
        };
        let home = match platform::users::user_home(&user) {
            Some(h) => h,
            None => {
                self.record_failure(label, &format!("home for '{}' not resolvable", user));
                return false;
            }
        };

        // 6. Compute the working-tree path and bare-repo path.
        let working_tree = home.join("vigogit").join(&project);
        let bare_repo = bare_repo_path(&self.state_dir, &seeder, &user, &project);
        let we_own = self.state.mirrors.contains_key(label);

        // 7. Conflict check on first materialization (if state
        // doesn't already claim ownership).
        if !we_own {
            if let Err(reason) = check_no_working_tree_conflict(&working_tree) {
                self.record_failure(label, &reason);
                return false;
            }
            if let Err(reason) = check_no_bare_repo_conflict(&bare_repo) {
                self.record_failure(label, &reason);
                return false;
            }
        }

        // 8. Ensure parent directories exist with correct
        // ownership / mode.
        if let Err(e) = ensure_user_vigogit_dir(&home, user_info.uid, user_info.gid) {
            self.record_failure(label, &format!("user vigogit dir: {}", e));
            return false;
        }
        if let Err(e) = ensure_bare_parent(&bare_repo) {
            self.record_failure(label, &format!("bare repo parent: {}", e));
            return false;
        }

        // 9. (Re-)materialize.
        let materialize_result = materialize_into(
            &bare_repo,
            &working_tree,
            &blob_path,
            user_info.uid,
            user_info.gid,
            we_own,
        );

        let git_sha = match materialize_result {
            Ok(sha) => sha,
            Err(e) => {
                self.record_failure(label, &format!("materialize: {}", e));
                return false;
            }
        };

        // 10. Record / refresh state.
        let next_gc_at = if we_own {
            // Preserve the existing gc schedule across re-materializations
            // (a bundle update shouldn't reset the gc clock).
            self.state.mirrors[label].next_gc_at
        } else {
            now + jittered_first_gc()
        };
        let mirror = MirrorState {
            label: label.to_string(),
            seeder,
            user,
            project,
            uid: user_info.uid,
            gid: user_info.gid,
            bare_repo_path: bare_repo,
            working_tree_path: working_tree,
            blob_sha: entry.sha256.clone(),
            git_sha,
            materialized_at: now,
            next_gc_at,
        };
        info!(
            label = %label,
            blob_sha = %&mirror.blob_sha[..12.min(mirror.blob_sha.len())],
            git_sha = %&mirror.git_sha[..12.min(mirror.git_sha.len())],
            we_own,
            "vigogit receiver: mirror materialized"
        );
        self.state.mirrors.insert(label.to_string(), mirror);
        // Successful materialization → drop any pending-wipe row and
        // clear the failure-reason dedup so a future failure gets a
        // fresh WARN.
        self.state.pending_wipes.remove(label);
        self.clear_failure(label);
        true
    }

    /// Wipe a mirror's bare repo and working tree, then drop the
    /// state entries.
    fn wipe(&mut self, label: &str) {
        let Some(state) = self.state.mirrors.remove(label) else {
            self.state.pending_wipes.remove(label);
            return;
        };
        if state.working_tree_path.exists() {
            if let Err(e) = std::fs::remove_dir_all(&state.working_tree_path) {
                warn!(
                    label = %label,
                    path = %state.working_tree_path.display(),
                    error = %e,
                    "vigogit receiver: failed to remove working tree"
                );
            }
        }
        if state.bare_repo_path.exists() {
            if let Err(e) = std::fs::remove_dir_all(&state.bare_repo_path) {
                warn!(
                    label = %label,
                    path = %state.bare_repo_path.display(),
                    error = %e,
                    "vigogit receiver: failed to remove bare repo"
                );
            }
        }
        self.state.pending_wipes.remove(label);
        self.clear_failure(label);
        info!(label = %label, "vigogit receiver: mirror wiped");
    }

    /// Run `git gc --auto` on a mirror's bare repo. Returns true if
    /// the next_gc_at deadline was updated (always, on success or
    /// failure — we don't want to retry-storm a broken gc).
    fn run_gc(&mut self, label: &str, now: u64) -> bool {
        let Some(state) = self.state.mirrors.get_mut(label) else {
            return false;
        };
        let mut cmd = git_ops::build_command(RunAs::Root);
        cmd.arg("-C").arg(&state.bare_repo_path).arg("gc").arg("--auto");
        let result = cmd.output();
        match result {
            Ok(out) if out.status.success() => {
                debug!(label = %label, "vigogit receiver: gc --auto ok");
            }
            Ok(out) => {
                warn!(
                    label = %label,
                    status = %out.status,
                    stderr = %String::from_utf8_lossy(&out.stderr).trim(),
                    "vigogit receiver: gc --auto reported failure"
                );
            }
            Err(e) => {
                warn!(label = %label, error = %e, "vigogit receiver: gc --auto could not spawn");
            }
        }
        state.next_gc_at = now + jittered_subsequent_gc();
        true
    }

    /// Surface a per-label failure to the operator-visible trait
    /// stream. Logs at WARN only on transition — the first time this
    /// reason appears for this label, or when the reason changes.
    /// Subsequent ticks with the same reason drop to DEBUG so a stuck
    /// label does not flood the journal. A label that recovers and
    /// later fails with a *different* reason still produces a fresh
    /// WARN because the stored reason changed.
    ///
    /// The `vigogit.mirror_failures` trait field is wired up in a
    /// follow-up commit alongside the receiver state extension to
    /// `TraitsSnapshot`.
    fn record_failure(&mut self, label: &str, reason: &str) {
        let is_new = self
            .last_failure_reason
            .get(label)
            .map(|prev| prev != reason)
            .unwrap_or(true);
        if is_new {
            warn!(
                label = %label,
                reason = %reason,
                "vigogit receiver: mirror failure (first occurrence — subsequent identical errors suppressed)"
            );
            self.last_failure_reason
                .insert(label.to_string(), reason.to_string());
        } else {
            debug!(
                label = %label,
                reason = %reason,
                "vigogit receiver: mirror failure (suppressed — unchanged since last tick)"
            );
        }
    }

    /// Called after a successful materialize or wipe for `label`.
    /// Drops any remembered failure reason so the next failure (if
    /// any) re-fires a fresh WARN.
    fn clear_failure(&mut self, label: &str) {
        self.last_failure_reason.remove(label);
    }
}

/// Parse `vigogit:<seeder>:<user>:<project>` into its three
/// segments. Returns `None` for malformed labels.
fn parse_label(label: &str) -> Option<(String, String, String)> {
    let rest = label.strip_prefix("vigogit:")?;
    let parts: Vec<&str> = rest.splitn(3, ':').collect();
    if parts.len() != 3 || parts.iter().any(|p| p.is_empty()) {
        return None;
    }
    Some((parts[0].to_string(), parts[1].to_string(), parts[2].to_string()))
}

/// Walk the manifest looking for the first active entry whose
/// label matches. Vigogit only publishes one active label at a
/// time per source, so the first match is correct.
fn find_active_by_label(manifest: &Manifest, label: &str) -> Option<BlobEntry> {
    let snap = manifest.snapshot();
    snap.entries
        .into_values()
        .find(|e| e.label == label && matches!(e.status, crate::swarm::manifest::BlobStatus::Active))
}

/// Refuse to materialize at `path` if it exists and would clobber
/// data we don't own.
///
/// - **Empty or missing** — OK.
/// - **Contains `.git/`** — REFUSE (operator's source repo).
/// - **Non-empty without `.git/`** — REFUSE (unknown user data).
fn check_no_working_tree_conflict(path: &Path) -> Result<(), String> {
    if !path.exists() {
        return Ok(());
    }
    if !path.is_dir() {
        return Err(format!(
            "{}: not a directory; refusing to replace",
            path.display()
        ));
    }
    if path.join(".git").exists() {
        return Err(format!(
            "{}: contains .git/; refusing to clobber a source repo",
            path.display()
        ));
    }
    let any_entry = std::fs::read_dir(path)
        .map(|mut it| it.next().is_some())
        .unwrap_or(false);
    if any_entry {
        return Err(format!(
            "{}: non-empty and not owned by vigogit; refusing to clobber",
            path.display()
        ));
    }
    Ok(())
}

fn check_no_bare_repo_conflict(path: &Path) -> Result<(), String> {
    if !path.exists() {
        return Ok(());
    }
    // A bare repo directory with no pack files and no branch refs
    // is either a freshly-initialized skeleton (someone ran
    // `git init --bare` here but never pushed anything) or — far
    // more commonly — our own leftover from a prior failed
    // materialization attempt. It holds zero operator data, so
    // there is nothing to protect: allow the caller to wipe and
    // reinit. Without this, a receiver that crashed or failed
    // mid-materialize one time would stay stuck forever because
    // receiver_state.json wouldn't record the aborted mirror and
    // the defensive check would keep refusing on every subsequent
    // tick.
    match skeleton_contents(path) {
        SkeletonContents::Empty => Ok(()),
        SkeletonContents::Populated {
            pack_empty,
            heads_empty,
            loose_empty,
        } => Err(format!(
            "{}: bare repo path exists and contains data; refusing to clobber (pack_empty={} heads_empty={} loose_empty={})",
            path.display(),
            pack_empty,
            heads_empty,
            loose_empty,
        )),
    }
}

/// Classify a bare-repo-shaped directory as either an empty skeleton
/// (fresh `git init --bare` with no pushed content or a leftover from
/// a failed materialize) or populated (real data we must not clobber).
///
/// When populated, the three component booleans are returned in the
/// variant so the caller can include them in the error message. That
/// way the journal tells the investigator *which* piece of the
/// skeleton triggered the refusal without needing to read source or
/// inspect the filesystem.
#[derive(Debug, PartialEq, Eq)]
enum SkeletonContents {
    Empty,
    Populated {
        pack_empty: bool,
        heads_empty: bool,
        loose_empty: bool,
    },
}

fn skeleton_contents(path: &Path) -> SkeletonContents {
    let pack_dir = path.join("objects").join("pack");
    let heads_dir = path.join("refs").join("heads");
    let pack_empty = dir_is_empty(&pack_dir);
    let heads_empty = dir_is_empty(&heads_dir);
    // Additionally ensure there are no loose objects (subdirs under
    // objects/ named like git hash prefixes — any two-hex-char
    // directory means loose object data). This keeps us defensive
    // against a partially-unpacked repo.
    let loose_empty = no_loose_object_dirs(&path.join("objects"));
    if pack_empty && heads_empty && loose_empty {
        SkeletonContents::Empty
    } else {
        SkeletonContents::Populated {
            pack_empty,
            heads_empty,
            loose_empty,
        }
    }
}

// Kept for the existing test suite: returns true when the given
// directory looks like the output of `git init --bare` with no pushed
// content. New non-test code should call `skeleton_contents` directly
// to get the component breakdown so the journal can include details.
#[cfg(test)]
fn is_empty_bare_skeleton(path: &Path) -> bool {
    matches!(skeleton_contents(path), SkeletonContents::Empty)
}

fn dir_is_empty(path: &Path) -> bool {
    match std::fs::read_dir(path) {
        Ok(mut it) => it.next().is_none(),
        Err(_) => true, // missing dir counts as empty (skeleton has not been init'd)
    }
}

fn no_loose_object_dirs(objects_dir: &Path) -> bool {
    let it = match std::fs::read_dir(objects_dir) {
        Ok(it) => it,
        Err(_) => return true,
    };
    for entry in it.flatten() {
        let name = entry.file_name();
        let name = match name.to_str() {
            Some(s) => s,
            None => continue,
        };
        // git stores loose objects under two-hex-char directories
        // like `objects/a1/`, `objects/de/`. Skip the well-known
        // non-object subdirs (pack, info).
        if name == "pack" || name == "info" {
            continue;
        }
        if name.len() == 2 && name.chars().all(|c| c.is_ascii_hexdigit()) {
            return false;
        }
    }
    true
}

/// Ensure `<home>/vigogit/` exists with the correct ownership
/// (`<uid>:<gid>` mode `0755`). Creates if missing, fixes ownership
/// or mode if wrong. Hands-free: vigogit treats the convention path
/// as its own.
fn ensure_user_vigogit_dir(home: &Path, uid: u32, gid: u32) -> std::io::Result<()> {
    let dir = home.join("vigogit");
    if !dir.exists() {
        std::fs::create_dir_all(&dir)?;
    }
    #[cfg(unix)]
    {
        use nix::unistd::{chown, Gid, Uid};
        use std::os::unix::fs::PermissionsExt;
        let _ = chown(&dir, Some(Uid::from_raw(uid)), Some(Gid::from_raw(gid)));
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o755))?;
    }
    let _ = uid;
    let _ = gid;
    Ok(())
}

fn ensure_bare_parent(bare_repo: &Path) -> std::io::Result<()> {
    if let Some(parent) = bare_repo.parent() {
        std::fs::create_dir_all(parent)?;
    }
    Ok(())
}

/// Run the bare-repo init / unbundle / checkout pipeline. Returns
/// the post-checkout HEAD sha.
///
/// On `we_own`, the bare repo is reused (incremental unbundle). If
/// the incremental unbundle fails — e.g. the source self-healed
/// and is publishing a full bundle whose base isn't in the local
/// repo — the bare repo is wiped and re-initialized from scratch
/// with the same bundle.
/// Run the full receive-side materialization pipeline. Each sub-step
/// tags its error with a bracketed step name so a future investigator
/// can grep the journal by stage (`materialize[init]`,
/// `materialize[fetch]`, `materialize[rev-parse]`,
/// `materialize[worktree-dir]`, `materialize[archive]`) without
/// reading the source.
///
/// When `we_own` is false and any step fails, the bare repo is wiped
/// on the error path before returning. Without this, a mid-pipeline
/// failure would leave a populated bare repo on disk; the next tick
/// would hit `check_no_bare_repo_conflict` (which correctly refuses
/// to clobber data it didn't plant) and the receiver would loop
/// forever. Wiping on failure means the next tick starts from a
/// clean slate and the fix re-attempts the whole pipeline.
fn materialize_into(
    bare_repo: &Path,
    working_tree: &Path,
    bundle_path: &Path,
    uid: u32,
    gid: u32,
    we_own: bool,
) -> Result<String, String> {
    match materialize_into_inner(bare_repo, working_tree, bundle_path, uid, gid, we_own) {
        Ok(sha) => Ok(sha),
        Err(e) => {
            if !we_own {
                // Best-effort cleanup. If this fails we log but don't
                // surface it: the original error is what the operator
                // needs to see.
                if let Err(wipe_err) = std::fs::remove_dir_all(bare_repo) {
                    if wipe_err.kind() != std::io::ErrorKind::NotFound {
                        warn!(
                            bare_repo = %bare_repo.display(),
                            error = %wipe_err,
                            "vigogit receiver: failed to wipe bare repo after materialize failure"
                        );
                    }
                }
            }
            Err(e)
        }
    }
}

fn materialize_into_inner(
    bare_repo: &Path,
    working_tree: &Path,
    bundle_path: &Path,
    uid: u32,
    gid: u32,
    we_own: bool,
) -> Result<String, String> {
    if !we_own || !bare_repo.exists() {
        wipe_and_init_bare(bare_repo).map_err(|e| format!("[init] {}", e))?;
    }

    if let Err(e) = git_fetch_bundle(bare_repo, bundle_path) {
        // Self-heal: wipe the bare repo and try again from scratch.
        // git fetch from a bundle can fail when the bundle is a
        // thin/incremental delta whose base sha we don't have
        // locally (publisher self-healed to a full bundle between
        // our ticks, agent restarted mid-materialize, etc.).
        info!(
            bare_repo = %bare_repo.display(),
            first_error = %e,
            "vigogit receiver: fetch from bundle failed, wiping and retrying as fresh"
        );
        wipe_and_init_bare(bare_repo).map_err(|e| format!("[init-retry] {}", e))?;
        git_fetch_bundle(bare_repo, bundle_path).map_err(|e| format!("[fetch] {}", e))?;
    }

    let head_sha = git_rev_parse_head(bare_repo).map_err(|e| format!("[rev-parse] {}", e))?;

    ensure_working_tree_dir(working_tree, uid, gid)
        .map_err(|e| format!("[worktree-dir] {}", e))?;

    extract_tree_via_archive(bare_repo, working_tree, uid, gid)
        .map_err(|e| format!("[archive] {}", e))?;

    Ok(head_sha)
}

fn wipe_and_init_bare(bare_repo: &Path) -> Result<(), String> {
    if bare_repo.exists() {
        std::fs::remove_dir_all(bare_repo)
            .map_err(|e| format!("remove existing bare repo: {}", e))?;
    }
    std::fs::create_dir_all(bare_repo)
        .map_err(|e| format!("create bare repo dir: {}", e))?;
    let mut cmd = git_ops::build_command(RunAs::Root);
    cmd.arg("init").arg("--bare").arg(bare_repo);
    let out = cmd
        .output()
        .map_err(|e| format!("git init --bare spawn: {}", e))?;
    if !out.status.success() {
        return Err(format!(
            "git init --bare exit {}: {}",
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    Ok(())
}

// git_fetch_bundle imports the objects from a git bundle file AND
// creates a local branch ref pointing at the bundle's HEAD. `git
// bundle unbundle` alone would only import objects — no refs would
// be created, leaving HEAD unborn and `git rev-parse HEAD` returning
// the literal string "HEAD". Using `git fetch <bundle> HEAD:branch`
// gets both steps in one command.
//
// The destination branch is always `refs/heads/main`. The bundle
// publisher ships a ref named literally `HEAD` (from `git bundle
// create <path> HEAD`), which we rewrite to `main` on the receiver
// side. This means the local branch name on receivers is always
// `main` regardless of what the source envoy's default branch is —
// acceptable for a read-only mirror since the commit content is
// identical and the working-tree checkout is what operators use.
fn git_fetch_bundle(bare_repo: &Path, bundle_path: &Path) -> Result<(), String> {
    let mut cmd = git_ops::build_command(RunAs::Root);
    cmd.arg("-C")
        .arg(bare_repo)
        .arg("fetch")
        .arg(bundle_path)
        .arg("+HEAD:refs/heads/main");
    let out = cmd
        .output()
        .map_err(|e| format!("git fetch bundle spawn: {}", e))?;
    if !out.status.success() {
        return Err(format!(
            "git fetch bundle exit {}: {}",
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    // After the fetch, make sure HEAD points at the branch we just
    // created. `git init --bare` sets HEAD → refs/heads/main by
    // default, but set it explicitly in case the git default ever
    // changes or `init.defaultBranch` is configured otherwise.
    let mut sym = git_ops::build_command(RunAs::Root);
    sym.arg("-C")
        .arg(bare_repo)
        .arg("symbolic-ref")
        .arg("HEAD")
        .arg("refs/heads/main");
    let sym_out = sym
        .output()
        .map_err(|e| format!("git symbolic-ref spawn: {}", e))?;
    if !sym_out.status.success() {
        return Err(format!(
            "git symbolic-ref exit {}: {}",
            sym_out.status,
            String::from_utf8_lossy(&sym_out.stderr).trim()
        ));
    }
    Ok(())
}

fn git_rev_parse_head(bare_repo: &Path) -> Result<String, String> {
    let mut cmd = git_ops::build_command(RunAs::Root);
    cmd.arg("-C").arg(bare_repo).arg("rev-parse").arg("HEAD");
    let out = cmd
        .output()
        .map_err(|e| format!("git rev-parse spawn: {}", e))?;
    if !out.status.success() {
        return Err(format!(
            "git rev-parse exit {}: {}",
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    let sha = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if sha.len() != 40 || !sha.chars().all(|c| c.is_ascii_hexdigit()) {
        return Err(format!("unexpected rev-parse output: {:?}", sha));
    }
    Ok(sha)
}

fn ensure_working_tree_dir(working_tree: &Path, uid: u32, gid: u32) -> std::io::Result<()> {
    if !working_tree.exists() {
        std::fs::create_dir_all(working_tree)?;
    }
    #[cfg(unix)]
    {
        use nix::unistd::{chown, Gid, Uid};
        let _ = chown(working_tree, Some(Uid::from_raw(uid)), Some(Gid::from_raw(gid)));
    }
    let _ = uid;
    let _ = gid;
    Ok(())
}

/// Populate `working_tree` with the tree at `HEAD` of `bare_repo`.
///
/// We cannot use `git checkout --work-tree=X --git-dir=Y` here: the
/// bare repo is **root-owned** (it's operator data, never writable by
/// the target user), but the checkout has to run as the target user
/// to produce user-owned working-tree files. `git checkout` is not
/// a pure writer into GIT_WORK_TREE — it unconditionally creates
/// `$GIT_DIR/index.lock`, `$GIT_DIR/index`, and updates
/// `$GIT_DIR/logs/HEAD`, which fails with EACCES when GIT_DIR is
/// root-owned and git is running as a non-root user. Investigation
/// 2026-04-14 confirmed this is the exact failure symptom on annlap.
///
/// `git archive --format=tar HEAD` is a pure reader of GIT_DIR — it
/// streams a tar of the tree without touching any files inside the
/// bare repo. Piped into `tar -xf - -C <work_tree>` running as the
/// target user, it produces user-owned working-tree files and zero
/// writes against GIT_DIR. Single streaming pass, no index, no
/// reflog, no permission issues.
///
/// Before extracting, the function clears existing entries in the
/// working tree (but not the directory itself — `ensure_working_tree_dir`
/// sized + chowned it for us). This handles re-materialization after
/// a bundle update: stale files from the previous HEAD that are no
/// longer part of the new tree would otherwise linger.
fn extract_tree_via_archive(
    bare_repo: &Path,
    working_tree: &Path,
    uid: u32,
    gid: u32,
) -> Result<(), String> {
    clear_working_tree_contents(working_tree)
        .map_err(|e| format!("clear working tree: {}", e))?;

    // git archive: stream a tar of HEAD to stdout. Runs as the
    // target user — only reads GIT_DIR, so having no write access
    // is fine. `--git-dir` arg ensures no ambiguity even though
    // `GIT_DIR` env var would also work.
    let mut git = git_ops::build_command(RunAs::User { uid, gid });
    git.arg("--git-dir").arg(bare_repo);
    git.arg("archive").arg("--format=tar").arg("HEAD");
    git.stdout(Stdio::piped());
    git.stderr(Stdio::piped());
    let mut git_child = git
        .spawn()
        .map_err(|e| format!("git archive spawn: {}", e))?;

    let git_stdout = git_child
        .stdout
        .take()
        .ok_or_else(|| "git archive stdout not captured".to_string())?;

    // tar: extract from stdin into the working tree, running as
    // the target user so extracted files are user-owned. Platform
    // tar is fine — no bsdtar-specific flags. `-f -` reads from
    // stdin.
    let mut tar = std::process::Command::new("tar");
    crate::platform::users::run_as_user(&mut tar, uid, gid);
    tar.arg("-x").arg("-f").arg("-").arg("-C").arg(working_tree);
    tar.stdin(Stdio::from(git_stdout));
    tar.stderr(Stdio::piped());
    let tar_out = tar
        .output()
        .map_err(|e| format!("tar spawn: {}", e))?;

    // Wait for git archive to reap it and collect its status.
    let git_out = git_child
        .wait_with_output()
        .map_err(|e| format!("git archive wait: {}", e))?;

    if !git_out.status.success() {
        return Err(format!(
            "git archive exit {}: {}",
            git_out.status,
            String::from_utf8_lossy(&git_out.stderr).trim()
        ));
    }
    if !tar_out.status.success() {
        return Err(format!(
            "tar exit {}: {}",
            tar_out.status,
            String::from_utf8_lossy(&tar_out.stderr).trim()
        ));
    }
    Ok(())
}

/// Remove every entry inside `working_tree`, preserving the
/// directory itself (so `ensure_working_tree_dir`'s ownership and
/// mode survive). A missing directory counts as "already clear."
fn clear_working_tree_contents(working_tree: &Path) -> std::io::Result<()> {
    let entries = match std::fs::read_dir(working_tree) {
        Ok(it) => it,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e),
    };
    for entry in entries {
        let entry = entry?;
        let path = entry.path();
        let file_type = entry.file_type()?;
        if file_type.is_dir() && !file_type.is_symlink() {
            std::fs::remove_dir_all(&path)?;
        } else {
            std::fs::remove_file(&path)?;
        }
    }
    Ok(())
}

/// First-materialization gc deadline: uniform random in
/// `[0, GC_FIRST_WINDOW_SECS)`. Spreads the gc stampede on a fresh
/// receiver across the whole 6-hour window.
fn jittered_first_gc() -> u64 {
    rand::thread_rng().gen_range(0..GC_FIRST_WINDOW_SECS)
}

/// Subsequent-gc deadline: `GC_BASE_INTERVAL_SECS ± GC_JITTER_SECS`.
/// Steady-state gc happens roughly every 6 hours per repo with
/// jitter to keep them desynchronized.
fn jittered_subsequent_gc() -> u64 {
    let jitter = rand::thread_rng().gen_range(0..(2 * GC_JITTER_SECS));
    GC_BASE_INTERVAL_SECS + jitter - GC_JITTER_SECS
}

/// Walk `<base>/mirrors/<seeder>/<user>/*.git` and delete any bare
/// repo not in `state.mirrors`. Called once at vigogit startup —
/// handles crash-recovery (partial materialization), state.json
/// corruption (state was reset, repos remain), and code rollback
/// (label namespace dropped). Conservative: only removes things we
/// don't recognize.
pub fn cleanup_orphan_bare_repos(state: &ReceiverState, base: &Path) {
    let known: HashSet<PathBuf> = state
        .mirrors
        .values()
        .map(|m| m.bare_repo_path.clone())
        .collect();

    let mirrors = mirrors_dir(base);
    if !mirrors.exists() {
        return;
    }

    let seeders = match std::fs::read_dir(&mirrors) {
        Ok(it) => it,
        Err(_) => return,
    };
    for seeder_entry in seeders.flatten() {
        let seeder_path = seeder_entry.path();
        if !seeder_path.is_dir() {
            continue;
        }
        let users = match std::fs::read_dir(&seeder_path) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for user_entry in users.flatten() {
            let user_path = user_entry.path();
            if !user_path.is_dir() {
                continue;
            }
            let projects = match std::fs::read_dir(&user_path) {
                Ok(it) => it,
                Err(_) => continue,
            };
            for project_entry in projects.flatten() {
                let project_path = project_entry.path();
                if !project_path.is_dir() {
                    continue;
                }
                if project_path.extension().and_then(|s| s.to_str()) != Some("git") {
                    continue;
                }
                if !known.contains(&project_path) {
                    info!(
                        path = %project_path.display(),
                        "vigogit receiver: removing orphan bare repo"
                    );
                    let _ = std::fs::remove_dir_all(&project_path);
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
    fn parse_label_valid() {
        let (s, u, p) = parse_label("vigogit:host:dan:proj").unwrap();
        assert_eq!(s, "host");
        assert_eq!(u, "dan");
        assert_eq!(p, "proj");
    }

    #[test]
    fn parse_label_rejects_missing_prefix() {
        assert!(parse_label("notvigogit:a:b:c").is_none());
    }

    #[test]
    fn parse_label_rejects_meta_prefix() {
        // vigogit-meta is not a content label and parse_label is
        // strict; "vigogit-meta:..." doesn't start with the
        // "vigogit:" prefix exactly.
        assert!(parse_label("vigogit-meta:host:dan:proj").is_none());
    }

    #[test]
    fn parse_label_rejects_too_few_segments() {
        assert!(parse_label("vigogit:host").is_none());
        assert!(parse_label("vigogit:host:dan").is_none());
        assert!(parse_label("vigogit::dan:proj").is_none());
        assert!(parse_label("vigogit:host::proj").is_none());
        assert!(parse_label("vigogit:host:dan:").is_none());
    }

    #[test]
    fn bare_repo_path_layout() {
        let p = bare_repo_path(Path::new("/var/lib/vigo/vigogit"), "host", "dan", "proj");
        assert_eq!(
            p,
            Path::new("/var/lib/vigo/vigogit/mirrors/host/dan/proj.git")
        );
    }

    #[test]
    fn mirrors_dir_layout() {
        let d = mirrors_dir(Path::new("/var/lib/vigo/vigogit"));
        assert_eq!(d, Path::new("/var/lib/vigo/vigogit/mirrors"));
    }

    #[test]
    fn check_no_working_tree_conflict_accepts_missing() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("nope");
        assert!(check_no_working_tree_conflict(&p).is_ok());
    }

    #[test]
    fn check_no_working_tree_conflict_accepts_empty_dir() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("empty");
        std::fs::create_dir_all(&p).unwrap();
        assert!(check_no_working_tree_conflict(&p).is_ok());
    }

    #[test]
    fn check_no_working_tree_conflict_rejects_dot_git() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("repo");
        std::fs::create_dir_all(p.join(".git")).unwrap();
        let err = check_no_working_tree_conflict(&p).unwrap_err();
        assert!(err.contains(".git"));
    }

    #[test]
    fn check_no_working_tree_conflict_rejects_non_empty() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("operator-data");
        std::fs::create_dir_all(&p).unwrap();
        std::fs::write(p.join("hello.txt"), b"hi").unwrap();
        let err = check_no_working_tree_conflict(&p).unwrap_err();
        assert!(err.contains("non-empty"));
    }

    #[test]
    fn check_no_bare_repo_conflict_accepts_missing() {
        let dir = tempdir().unwrap();
        let p = dir.path().join("missing.git");
        assert!(check_no_bare_repo_conflict(&p).is_ok());
    }

    // Note: the old check_no_bare_repo_conflict_rejects_existing test
    // was removed when the safety check was relaxed to allow empty
    // bare-repo skeletons (post-failed-init recovery path). The new
    // semantic is pinned by is_empty_bare_skeleton_* and
    // check_no_bare_repo_conflict_* tests above.

    #[test]
    fn jittered_first_gc_in_window() {
        for _ in 0..20 {
            let v = jittered_first_gc();
            assert!(v < GC_FIRST_WINDOW_SECS);
        }
    }

    #[test]
    fn jittered_subsequent_gc_within_jitter_band() {
        for _ in 0..50 {
            let v = jittered_subsequent_gc();
            assert!(v >= GC_BASE_INTERVAL_SECS - GC_JITTER_SECS);
            assert!(v < GC_BASE_INTERVAL_SECS + GC_JITTER_SECS);
        }
    }

    #[test]
    fn cleanup_orphan_bare_repos_handles_missing_dir() {
        let base = tempdir().unwrap();
        let state = ReceiverState::default();
        // mirrors/ doesn't exist — must not panic.
        cleanup_orphan_bare_repos(&state, base.path());
    }

    #[test]
    fn cleanup_orphan_bare_repos_keeps_known_and_removes_unknown() {
        let base = tempdir().unwrap();
        let mirrors = mirrors_dir(base.path());

        // Create a known bare repo at host/dan/keep.git.
        let known_path = mirrors.join("host").join("dan").join("keep.git");
        std::fs::create_dir_all(&known_path).unwrap();
        std::fs::write(known_path.join("HEAD"), b"ref: refs/heads/main\n").unwrap();

        // Create an orphan bare repo at host/dan/orphan.git.
        let orphan_path = mirrors.join("host").join("dan").join("orphan.git");
        std::fs::create_dir_all(&orphan_path).unwrap();

        // State only knows about the keep.git path.
        let mut state = ReceiverState::default();
        state.mirrors.insert(
            "vigogit:host:dan:keep".to_string(),
            MirrorState {
                label: "vigogit:host:dan:keep".to_string(),
                seeder: "host".to_string(),
                user: "dan".to_string(),
                project: "keep".to_string(),
                uid: 1000,
                gid: 1000,
                bare_repo_path: known_path.clone(),
                working_tree_path: PathBuf::from("/tmp/unused"),
                blob_sha: "deadbeef".to_string(),
                git_sha: "cafef00d".to_string(),
                materialized_at: 0,
                next_gc_at: 0,
            },
        );

        cleanup_orphan_bare_repos(&state, base.path());

        assert!(known_path.exists(), "known bare repo should survive");
        assert!(!orphan_path.exists(), "orphan bare repo should have been removed");
    }

    #[test]
    fn cleanup_orphan_bare_repos_ignores_non_git_dirs() {
        let base = tempdir().unwrap();
        let mirrors = mirrors_dir(base.path());
        let weird = mirrors.join("host").join("dan").join("not-a-bare-repo");
        std::fs::create_dir_all(&weird).unwrap();

        let state = ReceiverState::default();
        cleanup_orphan_bare_repos(&state, base.path());

        assert!(weird.exists(), "non-.git directory should be left alone");
    }

    #[test]
    fn claims_handle_update_replaces_set() {
        let h = ClaimsHandle::new();
        h.update(&["a".to_string(), "b".to_string()]);
        let s = h.snapshot();
        assert_eq!(s.len(), 2);
        assert!(s.contains("a"));
        assert!(s.contains("b"));

        h.update(&["c".to_string()]);
        let s = h.snapshot();
        assert_eq!(s.len(), 1);
        assert!(s.contains("c"));
        assert!(!s.contains("a"));
    }

    #[test]
    fn claims_handle_update_empty_clears_set() {
        let h = ClaimsHandle::new();
        h.update(&["a".to_string()]);
        h.update(&[]);
        assert!(h.snapshot().is_empty());
    }

    #[test]
    fn claims_handle_clones_share_state() {
        let a = ClaimsHandle::new();
        let b = a.clone();
        a.update(&["x".to_string()]);
        assert!(b.snapshot().contains("x"));
    }

    #[test]
    fn pending_wipe_hysteresis_constants_reasonable() {
        // Sanity-check: the hysteresis is at least 5 minutes (long
        // enough to ride out a server restart) and at most 1 hour
        // (short enough to feel responsive). These are static
        // `const_assert`-style bounds — clippy flags them because
        // the condition is constant at compile time, which is
        // exactly the point: if someone edits WIPE_HYSTERESIS_SECS
        // to an out-of-range value, the test fails at build time
        // via panic.
        #[allow(clippy::assertions_on_constants)]
        {
            assert!(WIPE_HYSTERESIS_SECS >= 5 * 60);
            assert!(WIPE_HYSTERESIS_SECS <= 60 * 60);
        }
    }

    #[test]
    fn receiver_state_pending_wipe_serialization() {
        let mut s = ReceiverState::default();
        s.pending_wipes.insert("vigogit:h:u:p".to_string(), 12345);
        let json = serde_json::to_string(&s).unwrap();
        assert!(json.contains("pending_wipes"));
        assert!(json.contains("12345"));

        let back: ReceiverState = serde_json::from_str(&json).unwrap();
        assert_eq!(back.pending_wipes.get("vigogit:h:u:p"), Some(&12345));
    }

    // Note: full end-to-end materialize_into() coverage is gated on
    // a manual home-lab run after a git upgrade — host git here is
    // below the MVP floor of 2.35, same situation as the publisher
    // tests. The unit tests above cover the pure-logic surfaces
    // (parsing, layout, conflict detection, jitter, orphan walker,
    // claims handle, state serialization).

    // --- is_empty_bare_skeleton / check_no_bare_repo_conflict -------

    fn make_bare_skeleton(dir: &Path) {
        // Simulate `git init --bare`: objects/pack/, objects/info/,
        // refs/heads/, refs/tags/, HEAD, config. No pack files, no
        // refs, no loose objects.
        std::fs::create_dir_all(dir.join("objects/pack")).unwrap();
        std::fs::create_dir_all(dir.join("objects/info")).unwrap();
        std::fs::create_dir_all(dir.join("refs/heads")).unwrap();
        std::fs::create_dir_all(dir.join("refs/tags")).unwrap();
        std::fs::write(dir.join("HEAD"), "ref: refs/heads/main\n").unwrap();
        std::fs::write(dir.join("config"), "[core]\n\trepositoryformatversion = 0\n").unwrap();
    }

    #[test]
    fn is_empty_bare_skeleton_accepts_fresh_git_init_bare() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        assert!(
            is_empty_bare_skeleton(&bare),
            "freshly-initialized bare repo should be treated as empty skeleton"
        );
    }

    #[test]
    fn is_empty_bare_skeleton_rejects_repo_with_pack() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        std::fs::write(
            bare.join("objects/pack/pack-deadbeef.pack"),
            b"fake pack bytes",
        )
        .unwrap();
        assert!(
            !is_empty_bare_skeleton(&bare),
            "bare repo with a pack file must NOT be treated as empty — that's operator data"
        );
    }

    #[test]
    fn is_empty_bare_skeleton_rejects_repo_with_branch_ref() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        std::fs::write(
            bare.join("refs/heads/main"),
            "f43f6ef3eea29e3780d73aa2cee4a78193419a69\n",
        )
        .unwrap();
        assert!(
            !is_empty_bare_skeleton(&bare),
            "bare repo with a branch ref must NOT be treated as empty"
        );
    }

    #[test]
    fn is_empty_bare_skeleton_rejects_loose_object_dir() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        std::fs::create_dir_all(bare.join("objects/a1")).unwrap();
        std::fs::write(bare.join("objects/a1/b2c3d4e5f6"), b"loose object").unwrap();
        assert!(
            !is_empty_bare_skeleton(&bare),
            "bare repo with a loose object dir must NOT be treated as empty"
        );
    }

    #[test]
    fn check_no_bare_repo_conflict_allows_empty_skeleton() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        // This is the regression case: the safety check used to
        // refuse any existing path, trapping receivers in a loop
        // after their own failed materializations left a skeleton
        // behind. The check should now allow the caller to wipe
        // and reinit the directory.
        assert!(
            check_no_bare_repo_conflict(&bare).is_ok(),
            "check_no_bare_repo_conflict should allow an empty skeleton (post-failed-init state)"
        );
    }

    #[test]
    fn check_no_bare_repo_conflict_still_rejects_populated_repo() {
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        std::fs::write(bare.join("objects/pack/pack-x.pack"), b"data").unwrap();
        assert!(
            check_no_bare_repo_conflict(&bare).is_err(),
            "a populated bare repo must still be rejected to protect operator data"
        );
    }

    #[test]
    fn check_no_bare_repo_conflict_error_includes_component_details() {
        // Regression: the stuck-receiver investigation on 2026-04-14
        // had to deduce which part of the skeleton triggered the
        // refusal by reading source and inspecting the filesystem.
        // The error message must now include the specific booleans
        // so the journal answers that question directly.
        let base = tempdir().unwrap();
        let bare = base.path().join("proj.git");
        make_bare_skeleton(&bare);
        std::fs::write(bare.join("objects/pack/pack-x.pack"), b"data").unwrap();
        std::fs::write(
            bare.join("refs/heads/main"),
            "f43f6ef3eea29e3780d73aa2cee4a78193419a69\n",
        )
        .unwrap();
        let err = check_no_bare_repo_conflict(&bare)
            .expect_err("populated repo must be rejected");
        assert!(
            err.contains("pack_empty=false"),
            "error must state pack_empty status, got: {}",
            err
        );
        assert!(
            err.contains("heads_empty=false"),
            "error must state heads_empty status, got: {}",
            err
        );
        assert!(
            err.contains("loose_empty=true"),
            "error must state loose_empty status, got: {}",
            err
        );
    }

    // --- clear_working_tree_contents ------------------------------

    #[test]
    fn clear_working_tree_contents_removes_files_and_dirs_but_preserves_root() {
        let base = tempdir().unwrap();
        let tree = base.path().join("wt");
        std::fs::create_dir_all(tree.join("subdir/nested")).unwrap();
        std::fs::write(tree.join("a.txt"), b"hello").unwrap();
        std::fs::write(tree.join("subdir/b.txt"), b"world").unwrap();
        std::fs::write(tree.join("subdir/nested/c.txt"), b"!").unwrap();

        clear_working_tree_contents(&tree).unwrap();

        assert!(tree.exists(), "root dir must be preserved");
        let remaining: Vec<_> = std::fs::read_dir(&tree)
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert!(
            remaining.is_empty(),
            "all contents must have been removed, found: {:?}",
            remaining.iter().map(|e| e.path()).collect::<Vec<_>>()
        );
    }

    #[test]
    fn clear_working_tree_contents_is_noop_on_missing_dir() {
        let base = tempdir().unwrap();
        let tree = base.path().join("does-not-exist");
        assert!(
            clear_working_tree_contents(&tree).is_ok(),
            "missing working tree must be treated as already clear"
        );
    }
}
