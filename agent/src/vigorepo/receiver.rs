// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Receiver-side vigorepo: materialize swarm-delivered git bundles
//! into root-owned bare-repo mirrors.
//!
//! The receiver is the consumer side of the vigorepo pipeline. Its
//! input is the [`ClaimsHandle`], populated by LAN multicast claim
//! announcements ([`super::pending_claims`]) and cross-subnet
//! `extra_mirrors` direct push. It owns:
//!
//! - **`receiver_state.json`** at `/var/lib/vigo/vigorepo/receiver_state.json`,
//!   a label → [`MirrorState`] map plus a label → wipe-pending-since
//!   map (the hysteresis state below).
//! - **A bare git repo per claim** at
//!   `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git`,
//!   root-owned. Stores the full git history; users recover into a
//!   working clone on demand via `vigo repo recover <project>`.
//!
//! ### Trust model
//!
//! The receiver acts only on [`ClaimsHandle`]. It never reads raw
//! manifest entries to decide what to mirror. Content integrity
//! comes from SHA256 verification in the swarm cache layer; source
//! identity is established by the mTLS peer certificate on the
//! swarm transport (port 1531). For LAN-discovered claims, trust
//! comes from the multicast announcement being backed by a matching
//! manifest entry; for `extra_mirrors` direct push, trust comes
//! from the mTLS client certificate on the push request.
//!
//! ### State.json is the conflict trust boundary
//!
//! `state.mirrors[label]` records that vigorepo owns the bare-repo
//! path for that label. The receiver replaces or deletes it freely.
//! If a label is **not** in state, the receiver refuses to clobber
//! any pre-existing directory at the bare-repo path that contains
//! real git data (defends against trampling operator-managed repos
//! or unrelated content). The `vigorepo.mirror_failures` trait
//! surfaces conflicts so they're visible without operator action;
//! the operator's recovery is `rm -rf` the conflicting directory
//! and the next reactor tick picks up the now-clean slate.
//!
//! ### Startup rescan
//!
//! If [`ReceiverState::load`] returns an empty state (first run,
//! corrupt file, schema mismatch), the constructor calls
//! [`rescan_mirrors_from_disk`] to walk `mirrors/` and re-adopt
//! every valid bare repo before the orphan cleanup runs. Without
//! this, a state-file wipe would cascade into deleting every mirror.
//!
//! ### Hysteresis on wipe
//!
//! When a label disappears from the [`ClaimsHandle`], the receiver
//! records `pending_wipes[label] = now` and waits
//! [`WIPE_HYSTERESIS_SECS`] seconds before actually wiping. This
//! rides out source check-in lag, LAN multicast loss, and transient
//! network issues. Reappearance clears the pending state.
//!
//! ### Self-healing on bundle update
//!
//! When the source publishes a new bundle the manifest entry's
//! sha256 changes. The receiver detects `state.blob_sha != entry.sha256`,
//! triggers download via the swarm wanted-labels mechanism, runs
//! `git fetch` from the bundle into the existing bare repo
//! (incremental — git adds only new objects). If the incremental
//! fetch fails (e.g. the source self-healed and is now publishing
//! full bundles with no shared base), the receiver wipes the bare
//! repo and retries from scratch on the next tick. End-to-end the
//! system always converges.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, RwLock};

use rand::Rng;
use tracing::{debug, info, warn};

use super::git_ops::{self, RunAs};
use super::state::{
    unix_seconds_now, MirrorState, ReceiverState,
};
use crate::platform;
use crate::swarm::cache::BlobCache;
use crate::swarm::manifest::{BlobEntry, Manifest};

/// How long a label must be missing from the [`ClaimsHandle`] before
/// the receiver actually wipes it. Tuned to ride out:
///
/// - Source envoy offline/restarting (LAN claim TTL is 90 s, so a
///   brief outage clears and re-announces)
/// - LAN multicast loss or network blips
///
/// Long enough to survive these, short enough that an intentional
/// removal propagates within a few announcement cycles.
pub const WIPE_HYSTERESIS_SECS: u64 = 15 * 60;

/// Lower / upper bound of the per-repo `git gc --auto` jitter
/// window after each successful gc, in seconds. The first gc after
/// materialization is jittered uniformly within
/// `0..GC_FIRST_WINDOW_SECS` so a stampede of new mirrors doesn't
/// all gc in lockstep 6 hours later.
pub const GC_BASE_INTERVAL_SECS: u64 = 6 * 3600;
pub const GC_JITTER_SECS: u64 = 30 * 60;
pub const GC_FIRST_WINDOW_SECS: u64 = GC_BASE_INTERVAL_SECS;

/// Subdirectory of `default_vigorepo_dir()` that holds receiver
/// bare repos. Kept distinct from the publisher's `state.json` /
/// `traits.json` / `tmp/` so the orphan-cleanup walker can scan
/// inside `mirrors/` without filtering against state files.
pub const MIRRORS_DIR: &str = "mirrors";

/// Compute the `mirrors/` subdirectory path for the given vigorepo
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
/// for cross-task readers and writers.
///
/// As of Phase 8 (0.23.0), the only claim source is the LAN
/// multicast listener ingesting `VIGOREPOCLAIM:` lines via
/// [`ClaimsHandle::ingest_lan_claim`] and the cross-subnet
/// direct-push handler (Phase 7) calling the same method. The
/// server-side `ClaimTable` was deleted along with the
/// `vigorepo_claims` proto field, so there's no longer a
/// "server-delivered" set to merge — every entry is keyed by
/// `(label, source_hostname)` with a TTL-based expiry so a
/// silently-dead source's claims age out automatically.
///
/// Consumers (swarm manager, vigorepo reactor) read the current
/// wanted set via [`ClaimsHandle::snapshot`] or the Arc-wrapped
/// `shared()` view.
#[derive(Clone)]
pub struct ClaimsHandle {
    inner: Arc<Mutex<ClaimsInner>>,
    /// Cached union view rebuilt on every mutation. Exposed to
    /// the swarm manager via [`shared`] — which the manager
    /// holds as `Arc<RwLock<HashSet<String>>>` directly. Keeping
    /// this as a separate `RwLock` means reader paths (the swarm
    /// manager's hot path on every manifest evaluation) don't
    /// contend with the writer path (LAN ingest).
    union: Arc<RwLock<HashSet<String>>>,
}

struct ClaimsInner {
    /// LAN-announced claims. Key: (label, source_hostname).
    /// Value: unix seconds when this entry expires if the source
    /// stops re-announcing. The reactor calls
    /// [`ClaimsHandle::prune_lan_expired`] every tick to drop
    /// stale rows.
    lan_claims: HashMap<(String, String), u64>,
}

/// LAN claim TTL in seconds. A VIGOREPOCLAIM entry lives 3× the
/// standard peer announcement cadence — long enough to ride out
/// transient multicast loss but short enough that a silently-dead
/// source is pruned within a minute. `ingest_lan_claim` resets
/// this window on every refresh.
pub const LAN_CLAIM_TTL_SECS: u64 = 90;

impl ClaimsHandle {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(Mutex::new(ClaimsInner {
                lan_claims: HashMap::new(),
            })),
            union: Arc::new(RwLock::new(HashSet::new())),
        }
    }

    /// Ingest a single LAN-announced claim decision.
    ///
    /// `label` is the full vigorepo content-seed label.
    /// `source` is the announcing envoy's canonical hostname.
    /// `in_claim_list` is `true` if this envoy's hostname appeared
    /// in the source's claim list for that label — meaning "the
    /// source wants me to mirror this" — and `false` if it did
    /// not (the source explicitly removed us, or we were never in
    /// the list).
    ///
    /// If `in_claim_list` is true, an entry `(label, source)` is
    /// inserted or refreshed with an expiry of
    /// `now + LAN_CLAIM_TTL_SECS`. If false, any existing entry
    /// for this exact `(label, source)` pair is removed. Entries
    /// for the same label from OTHER sources are not affected —
    /// in the transient case where two source envoys both
    /// announce claim lists for the same label, one dropping the
    /// receiver doesn't override the other's continued claim.
    pub fn ingest_lan_claim(
        &self,
        label: &str,
        source: &str,
        in_claim_list: bool,
        now: u64,
    ) {
        let mut inner = self.inner.lock().unwrap_or_else(|e| e.into_inner());
        let key = (label.to_string(), source.to_string());
        if in_claim_list {
            inner.lan_claims.insert(key, now + LAN_CLAIM_TTL_SECS);
        } else {
            inner.lan_claims.remove(&key);
        }
        let new_union = inner.recompute_union();
        drop(inner);
        let mut u = self.union.write().unwrap_or_else(|e| e.into_inner());
        *u = new_union;
    }

    /// Drop LAN claim entries whose TTL has expired. Called from
    /// the vigorepo reactor tick. Returns the number of rows
    /// removed.
    pub fn prune_lan_expired(&self, now: u64) -> usize {
        let mut inner = self.inner.lock().unwrap_or_else(|e| e.into_inner());
        let before = inner.lan_claims.len();
        inner.lan_claims.retain(|_, exp| *exp > now);
        let removed = before - inner.lan_claims.len();
        if removed > 0 {
            let new_union = inner.recompute_union();
            drop(inner);
            let mut u = self.union.write().unwrap_or_else(|e| e.into_inner());
            *u = new_union;
        }
        removed
    }

    /// Snapshot the current wanted set (union of server + LAN
    /// claims). The reactor and the swarm manager both call this
    /// on every tick.
    pub fn snapshot(&self) -> HashSet<String> {
        self.union.read().unwrap_or_else(|e| e.into_inner()).clone()
    }

    /// Return the underlying `Arc<RwLock>` over the union view for
    /// callers that want a long-lived borrow without going through
    /// the `ClaimsHandle` clone. Used by `agent/src/main.rs` to
    /// hand the shared lock to the swarm manager (which lives in
    /// a different crate boundary from main.rs and so needs the
    /// raw type, not the vigorepo wrapper). Not part of the
    /// documented vigorepo API.
    pub fn shared(&self) -> Arc<RwLock<HashSet<String>>> {
        self.union.clone()
    }
}

impl ClaimsInner {
    /// Recompute the wanted-labels set from (non-expired)
    /// lan_claims entries. Caller is expected to hold the `inner`
    /// lock while calling this and then write the returned set to
    /// `ClaimsHandle.union` under the union write lock.
    fn recompute_union(&self) -> HashSet<String> {
        let mut out = HashSet::new();
        for (label, _src) in self.lan_claims.keys() {
            out.insert(label.clone());
        }
        out
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
    /// Per-label timestamp of the last staleness WARN emission.
    /// Rate-limits the stale-mirror warning to once per 24 hours per
    /// label — otherwise a mirror whose source envoy is permanently
    /// dead would log every 5-second tick forever. Not persisted;
    /// agent restart re-emits a fresh warning for every stale mirror,
    /// which is fine (the operator just got a fresh log anchor).
    last_stale_warn: BTreeMap<String, u64>,
}

/// Minimum interval between repeated staleness warnings for a single
/// label. 24 hours: the warning is advisory, the state doesn't
/// change, and a daily nudge is more than enough for the operator
/// to notice.
const STALE_WARN_INTERVAL_SECS: u64 = 24 * 60 * 60;

impl Receiver {
    /// Construct a Receiver, loading any persisted state and
    /// running orphan cleanup on the bare-repo tree. Call once at
    /// vigorepo startup.
    ///
    /// If [`ReceiverState::load`] returns an empty state (first run,
    /// corrupt file, schema mismatch), fall back to scanning the
    /// `mirrors/` tree on disk via [`rescan_mirrors_from_disk`].
    /// This recovers tracking for bare repos that outlived the state
    /// file — without the rescan, [`cleanup_orphan_bare_repos`] would
    /// wipe every existing mirror because the "known" set is empty,
    /// and the next check-in would re-download them all from scratch.
    /// The rescanned state is persisted immediately so the
    /// out-of-process `vigo repo list` CLI sees it before the first
    /// reactor tick writes.
    pub fn new(state_dir: PathBuf, claims: ClaimsHandle) -> Self {
        let mut state = ReceiverState::load(&state_dir);
        if state.mirrors.is_empty() {
            let rescanned = rescan_mirrors_from_disk(&state_dir);
            if !rescanned.is_empty() {
                info!(
                    count = rescanned.len(),
                    "vigorepo receiver: rescanned mirrors from disk after empty state"
                );
                state.mirrors = rescanned;
                if let Err(e) = state.save(&state_dir) {
                    warn!(
                        error = %e,
                        "vigorepo receiver: failed to persist rescanned state; \
                         next tick will retry"
                    );
                }
            }
        }
        cleanup_orphan_bare_repos(&state, &state_dir);
        Self {
            state,
            state_dir,
            claims,
            last_failure_reason: BTreeMap::new(),
            last_stale_warn: BTreeMap::new(),
        }
    }

    /// Run one receiver pass. Called from the reactor's blocking
    /// worker after the publish pass. Idempotent: multiple calls
    /// per tick are safe but redundant.
    ///
    /// Returns the list of currently-stale mirror labels
    /// (`now - last_refreshed > STALE_THRESHOLD_SECS`). The caller
    /// passes this into the traits snapshot so the staleness is
    /// visible via the `vigorepo.stale_mirrors` trait. Stale mirrors
    /// are never auto-pruned by this function — removal is strictly
    /// manual via `vigo repo prune`.
    pub fn tick(&mut self, manifest: &Manifest, cache: &BlobCache) -> Vec<String> {
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
                    debug!(label = %label, "vigorepo receiver: pending wipe cleared (label reappeared)");
                }
                continue;
            }
            match self.state.pending_wipes.get(&label).copied() {
                Some(since) if now.saturating_sub(since) >= WIPE_HYSTERESIS_SECS => {
                    info!(
                        label = %label,
                        wait_secs = now.saturating_sub(since),
                        "vigorepo receiver: wipe hysteresis elapsed, removing mirror"
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
                        "vigorepo receiver: label vanished from claims, marking pending wipe"
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
                    "vigorepo receiver: failed to persist receiver_state.json"
                );
            }
        }

        // Phase 4: staleness walk. Mirrors whose `last_refreshed` is
        // older than STALE_THRESHOLD_SECS get a rate-limited WARN
        // (once per 24 hours per label) and are returned to the
        // caller so the traits snapshot can surface them. Stale
        // mirrors are never auto-pruned — the backup is valuable
        // exactly when the source is gone, and that's precisely the
        // state that makes a mirror look stale. Manual cleanup only.
        let mut stale = Vec::new();
        for (label, mirror) in &self.state.mirrors {
            if !mirror.is_stale(now) {
                continue;
            }
            stale.push(label.clone());
            let should_warn = match self.last_stale_warn.get(label).copied() {
                Some(last) => now.saturating_sub(last) >= STALE_WARN_INTERVAL_SECS,
                None => true,
            };
            if should_warn {
                let age_days = now.saturating_sub(mirror.last_refreshed) / 86400;
                warn!(
                    label = %label,
                    seeder = %mirror.seeder,
                    age_days,
                    "vigorepo receiver: mirror is stale — source envoy may be gone. \
                     Run `vigo repo prune` to remove manually if you no longer need it."
                );
                self.last_stale_warn.insert(label.clone(), now);
            }
        }

        stale
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

        // 4a. Bundle size guardrail. Refuse to materialize any
        // bundle larger than MAX_BUNDLE_SIZE (20 GiB). Bundles
        // that big are almost certainly a misconfiguration; the
        // cap is very high so a legitimate personal-DR use case
        // will never hit it.
        match std::fs::metadata(&blob_path) {
            Ok(meta) if meta.len() > super::MAX_BUNDLE_SIZE => {
                self.record_failure(
                    label,
                    &format!(
                        "bundle size {} exceeds MAX_BUNDLE_SIZE ({} GiB)",
                        meta.len(),
                        super::MAX_BUNDLE_SIZE / (1024 * 1024 * 1024)
                    ),
                );
                return false;
            }
            Ok(_) => {}
            Err(e) => {
                self.record_failure(label, &format!("stat blob: {}", e));
                return false;
            }
        }

        // 5. Verify the owning Unix user exists on this envoy. We
        // don't resolve the home directory or uid/gid — the receiver
        // no longer touches user-space at all. The user lookup is
        // still load-bearing because it gives us a clean error when
        // a claim arrives for an account that doesn't exist locally.
        if platform::users::lookup_user(&user).is_none() {
            self.record_failure(label, &format!("user '{}' not found", user));
            return false;
        }

        // 6. Compute the bare-repo path.
        let bare_repo = bare_repo_path(&self.state_dir, &seeder, &user, &project);
        let we_own = self.state.mirrors.contains_key(label);

        // 6a. Per-receiver and per-source mirror count caps.
        // Only enforced on FIRST materialization (re-fetches of an
        // existing mirror don't change the count). Stops a runaway
        // source from filling the receiver with mirrors and stops
        // the receiver from holding an unbounded total.
        if !we_own {
            if self.state.mirrors.len() >= super::MAX_MIRRORS_PER_RECEIVER {
                self.record_failure(
                    label,
                    &format!(
                        "receiver at MAX_MIRRORS_PER_RECEIVER ({}); refusing new mirror",
                        super::MAX_MIRRORS_PER_RECEIVER
                    ),
                );
                return false;
            }
            let per_source_count = self
                .state
                .mirrors
                .values()
                .filter(|m| m.seeder == seeder)
                .count();
            if per_source_count >= super::MAX_PER_SOURCE_MIRRORS {
                self.record_failure(
                    label,
                    &format!(
                        "source '{}' at MAX_PER_SOURCE_MIRRORS ({}); refusing new mirror",
                        seeder,
                        super::MAX_PER_SOURCE_MIRRORS
                    ),
                );
                return false;
            }
        }

        // 7. Conflict check on first materialization (if state
        // doesn't already claim ownership).
        if !we_own {
            if let Err(reason) = check_no_bare_repo_conflict(&bare_repo) {
                self.record_failure(label, &reason);
                return false;
            }
        }

        // 8. Ensure parent directories exist.
        if let Err(e) = ensure_bare_parent(&bare_repo) {
            self.record_failure(label, &format!("bare repo parent: {}", e));
            return false;
        }

        // 9. (Re-)materialize.
        let git_sha = match materialize_into(&bare_repo, &blob_path, we_own) {
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
            bare_repo_path: bare_repo,
            blob_sha: entry.sha256.clone(),
            git_sha,
            last_refreshed: now,
            next_gc_at,
        };
        info!(
            label = %label,
            blob_sha = %&mirror.blob_sha[..12.min(mirror.blob_sha.len())],
            git_sha = %&mirror.git_sha[..12.min(mirror.git_sha.len())],
            we_own,
            "vigorepo receiver: mirror materialized"
        );
        self.state.mirrors.insert(label.to_string(), mirror);
        // Successful materialization → drop any pending-wipe row and
        // clear the failure-reason dedup so a future failure gets a
        // fresh WARN.
        self.state.pending_wipes.remove(label);
        self.clear_failure(label);
        true
    }

    /// Wipe a mirror's bare repo and drop the state entries. The
    /// receiver no longer owns any user-space files (as of 0.22.0);
    /// if the user had recovered this mirror via
    /// `vigo repo recover`, that recovered clone lives under a
    /// user-owned path the receiver never touches, so wipe leaves
    /// it alone.
    fn wipe(&mut self, label: &str) {
        let Some(state) = self.state.mirrors.remove(label) else {
            self.state.pending_wipes.remove(label);
            return;
        };
        if state.bare_repo_path.exists() {
            if let Err(e) = std::fs::remove_dir_all(&state.bare_repo_path) {
                warn!(
                    label = %label,
                    path = %state.bare_repo_path.display(),
                    error = %e,
                    "vigorepo receiver: failed to remove bare repo"
                );
            }
        }
        self.state.pending_wipes.remove(label);
        self.clear_failure(label);
        info!(label = %label, "vigorepo receiver: mirror wiped");
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
                debug!(label = %label, "vigorepo receiver: gc --auto ok");
            }
            Ok(out) => {
                warn!(
                    label = %label,
                    status = %out.status,
                    stderr = %String::from_utf8_lossy(&out.stderr).trim(),
                    "vigorepo receiver: gc --auto reported failure"
                );
            }
            Err(e) => {
                warn!(label = %label, error = %e, "vigorepo receiver: gc --auto could not spawn");
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
    /// The `vigorepo.mirror_failures` trait field is wired up in a
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
                "vigorepo receiver: mirror failure (first occurrence — subsequent identical errors suppressed)"
            );
            self.last_failure_reason
                .insert(label.to_string(), reason.to_string());
        } else {
            debug!(
                label = %label,
                reason = %reason,
                "vigorepo receiver: mirror failure (suppressed — unchanged since last tick)"
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

/// Parse `vigorepo:<seeder>:<user>:<project>` into its three
/// segments. Returns `None` for malformed labels.
fn parse_label(label: &str) -> Option<(String, String, String)> {
    let rest = label.strip_prefix("vigorepo:")?;
    let parts: Vec<&str> = rest.splitn(3, ':').collect();
    if parts.len() != 3 || parts.iter().any(|p| p.is_empty()) {
        return None;
    }
    Some((parts[0].to_string(), parts[1].to_string(), parts[2].to_string()))
}

/// Walk the manifest looking for the first active entry whose
/// label matches. Vigorepo only publishes one active label at a
/// time per source, so the first match is correct.
fn find_active_by_label(manifest: &Manifest, label: &str) -> Option<BlobEntry> {
    let snap = manifest.snapshot();
    snap.entries
        .into_values()
        .find(|e| e.label == label && matches!(e.status, crate::swarm::manifest::BlobStatus::Active))
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

fn ensure_bare_parent(bare_repo: &Path) -> std::io::Result<()> {
    if let Some(parent) = bare_repo.parent() {
        std::fs::create_dir_all(parent)?;
    }
    Ok(())
}

/// Run the bare-repo init + fetch pipeline. Returns the HEAD sha
/// after a successful fetch from the bundle. As of 0.22.0 the
/// receiver no longer maintains a working tree — recovery happens
/// on demand via `vigo repo recover`.
///
/// Each sub-step tags its error with a bracketed step name
/// (`materialize[init]`, `materialize[fetch]`,
/// `materialize[rev-parse]`) so future investigations can grep by
/// stage.
///
/// When `we_own` is false and any step fails, the bare repo is
/// wiped on the error path before returning. Without this, a
/// mid-pipeline failure would leave a populated bare repo on disk;
/// the next tick would hit `check_no_bare_repo_conflict` (which
/// correctly refuses to clobber data it didn't plant) and the
/// receiver would loop forever. Wiping on failure means the next
/// tick starts from a clean slate.
fn materialize_into(
    bare_repo: &Path,
    bundle_path: &Path,
    we_own: bool,
) -> Result<String, String> {
    match materialize_into_inner(bare_repo, bundle_path, we_own) {
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
                            "vigorepo receiver: failed to wipe bare repo after materialize failure"
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
    bundle_path: &Path,
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
            "vigorepo receiver: fetch from bundle failed, wiping and retrying as fresh"
        );
        wipe_and_init_bare(bare_repo).map_err(|e| format!("[init-retry] {}", e))?;
        git_fetch_bundle(bare_repo, bundle_path).map_err(|e| format!("[fetch] {}", e))?;
    }

    let head_sha = git_rev_parse_head(bare_repo).map_err(|e| format!("[rev-parse] {}", e))?;
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

/// Walk `<base>/mirrors/<seeder>/<user>/*.git` and reconstruct a
/// [`MirrorState`] entry for every valid bare repo found. Used to
/// recover receiver tracking after a `receiver_state.json` wipe
/// (schema upgrade, corruption, manual deletion) so existing bare
/// repos on disk don't get nuked by [`cleanup_orphan_bare_repos`]
/// and then re-downloaded from scratch on the next tick.
///
/// Validity check: the directory must end in `.git` and contain a
/// `HEAD` file. `git rev-parse HEAD` must return a 40-char SHA —
/// anything else (empty repo, broken skeleton) is skipped and left
/// for [`cleanup_orphan_bare_repos`] to remove.
///
/// Reconstructed fields:
/// - `blob_sha`: empty. Forces the next receiver tick to re-enter
///   [`Receiver::try_materialize`], which takes the `we_own=true`
///   incremental-fetch path in [`materialize_into`]. Git reuses the
///   existing packs, so the re-fetch transfers only new objects.
/// - `last_refreshed`: `FETCH_HEAD` mtime if present, else `HEAD`
///   mtime, else `now`. Honest-enough baseline for the staleness
///   detector — staleness is advisory and never triggers eviction.
/// - `next_gc_at`: `now + jittered_first_gc()` so rescanned mirrors
///   don't all gc in lockstep.
pub fn rescan_mirrors_from_disk(base: &Path) -> BTreeMap<String, MirrorState> {
    let mut out = BTreeMap::new();
    let mirrors = mirrors_dir(base);
    if !mirrors.exists() {
        return out;
    }

    let seeders = match std::fs::read_dir(&mirrors) {
        Ok(it) => it,
        Err(_) => return out,
    };
    for seeder_entry in seeders.flatten() {
        let seeder_path = seeder_entry.path();
        if !seeder_path.is_dir() {
            continue;
        }
        let Some(seeder) = seeder_path
            .file_name()
            .and_then(|s| s.to_str())
            .map(str::to_string)
        else {
            continue;
        };

        let users = match std::fs::read_dir(&seeder_path) {
            Ok(it) => it,
            Err(_) => continue,
        };
        for user_entry in users.flatten() {
            let user_path = user_entry.path();
            if !user_path.is_dir() {
                continue;
            }
            let Some(user) = user_path
                .file_name()
                .and_then(|s| s.to_str())
                .map(str::to_string)
            else {
                continue;
            };

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
                if !project_path.join("HEAD").is_file() {
                    continue;
                }
                let Some(project) = project_path
                    .file_stem()
                    .and_then(|s| s.to_str())
                    .map(str::to_string)
                else {
                    continue;
                };

                let git_sha = match git_rev_parse_head(&project_path) {
                    Ok(sha) => sha,
                    Err(e) => {
                        warn!(
                            path = %project_path.display(),
                            error = %e,
                            "vigorepo receiver: rescan skipping unreadable bare repo"
                        );
                        continue;
                    }
                };

                let label = format!(
                    "{}{sep}{}{sep}{}{sep}{}",
                    super::CONTENT_LABEL_PREFIX,
                    seeder,
                    user,
                    project,
                    sep = super::LABEL_SEP
                );

                let now = unix_seconds_now();
                let last_refreshed =
                    mirror_last_refreshed_from_mtime(&project_path).unwrap_or(now);

                out.insert(
                    label.clone(),
                    MirrorState {
                        label,
                        seeder: seeder.clone(),
                        user: user.clone(),
                        project,
                        bare_repo_path: project_path,
                        blob_sha: String::new(),
                        git_sha,
                        last_refreshed,
                        next_gc_at: now + jittered_first_gc(),
                    },
                );
            }
        }
    }
    out
}

/// Best-effort "when was this mirror last refreshed" estimate for
/// the rescan path. Prefers `FETCH_HEAD` (written on every successful
/// bundle fetch), falls back to `HEAD` mtime, then the bare repo
/// directory itself. Returns `None` only if the whole repo is
/// somehow unreadable — callers substitute `now` in that case.
fn mirror_last_refreshed_from_mtime(bare_repo: &Path) -> Option<u64> {
    let probe = |p: &Path| -> Option<u64> {
        let meta = std::fs::metadata(p).ok()?;
        let mtime = meta.modified().ok()?;
        mtime
            .duration_since(std::time::UNIX_EPOCH)
            .ok()
            .map(|d| d.as_secs())
    };
    probe(&bare_repo.join("FETCH_HEAD"))
        .or_else(|| probe(&bare_repo.join("HEAD")))
        .or_else(|| probe(bare_repo))
}

/// Walk `<base>/mirrors/<seeder>/<user>/*.git` and delete any bare
/// repo not in `state.mirrors`. Called once at vigorepo startup —
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
                        "vigorepo receiver: removing orphan bare repo"
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
        let (s, u, p) = parse_label("vigorepo:host:dan:proj").unwrap();
        assert_eq!(s, "host");
        assert_eq!(u, "dan");
        assert_eq!(p, "proj");
    }

    #[test]
    fn parse_label_rejects_missing_prefix() {
        assert!(parse_label("notvigorepo:a:b:c").is_none());
    }

    #[test]
    fn parse_label_rejects_meta_prefix() {
        // vigorepo-meta is not a content label and parse_label is
        // strict; "vigorepo-meta:..." doesn't start with the
        // "vigorepo:" prefix exactly.
        assert!(parse_label("vigorepo-meta:host:dan:proj").is_none());
    }

    #[test]
    fn parse_label_rejects_too_few_segments() {
        assert!(parse_label("vigorepo:host").is_none());
        assert!(parse_label("vigorepo:host:dan").is_none());
        assert!(parse_label("vigorepo::dan:proj").is_none());
        assert!(parse_label("vigorepo:host::proj").is_none());
        assert!(parse_label("vigorepo:host:dan:").is_none());
    }

    #[test]
    fn bare_repo_path_layout() {
        let p = bare_repo_path(Path::new("/var/lib/vigo/vigorepo"), "host", "dan", "proj");
        assert_eq!(
            p,
            Path::new("/var/lib/vigo/vigorepo/mirrors/host/dan/proj.git")
        );
    }

    #[test]
    fn mirrors_dir_layout() {
        let d = mirrors_dir(Path::new("/var/lib/vigo/vigorepo"));
        assert_eq!(d, Path::new("/var/lib/vigo/vigorepo/mirrors"));
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
            "vigorepo:host:dan:keep".to_string(),
            MirrorState {
                label: "vigorepo:host:dan:keep".to_string(),
                seeder: "host".to_string(),
                user: "dan".to_string(),
                project: "keep".to_string(),
                bare_repo_path: known_path.clone(),
                blob_sha: "deadbeef".to_string(),
                git_sha: "cafef00d".to_string(),
                last_refreshed: 0,
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
    fn rescan_mirrors_from_disk_missing_dir_returns_empty() {
        let base = tempdir().unwrap();
        assert!(rescan_mirrors_from_disk(base.path()).is_empty());
    }

    #[test]
    fn rescan_mirrors_from_disk_skips_dir_without_head() {
        let base = tempdir().unwrap();
        let bogus = mirrors_dir(base.path())
            .join("host")
            .join("dan")
            .join("proj.git");
        std::fs::create_dir_all(&bogus).unwrap();
        assert!(rescan_mirrors_from_disk(base.path()).is_empty());
    }

    #[test]
    fn rescan_mirrors_from_disk_skips_non_git_extension() {
        let base = tempdir().unwrap();
        let bogus = mirrors_dir(base.path())
            .join("host")
            .join("dan")
            .join("proj");
        std::fs::create_dir_all(&bogus).unwrap();
        std::fs::write(bogus.join("HEAD"), b"ref: refs/heads/main\n").unwrap();
        assert!(rescan_mirrors_from_disk(base.path()).is_empty());
    }

    /// End-to-end rescan of a real `git init --bare` + commit. Skipped
    /// cleanly if git is not on PATH (matches the version-check test's
    /// convention).
    #[test]
    fn rescan_mirrors_from_disk_reconstructs_real_bare_repo() {
        use std::process::Command;
        if Command::new("git").arg("--version").output().is_err() {
            println!("git not available; skipping");
            return;
        }

        let base = tempdir().unwrap();
        let bare = mirrors_dir(base.path())
            .join("hostA")
            .join("dan")
            .join("myproj.git");
        std::fs::create_dir_all(&bare).unwrap();

        // Seed the bare repo with one commit via an external work
        // dir, then push into it. Using a work dir avoids the
        // `commit --allow-empty` in a bare repo gymnastics.
        let work = tempdir().unwrap();
        let run = |args: &[&str], cwd: &Path| {
            let out = Command::new("git")
                .args(args)
                .current_dir(cwd)
                .env("GIT_CONFIG_GLOBAL", "/dev/null")
                .env("GIT_CONFIG_SYSTEM", "/dev/null")
                .env("GIT_AUTHOR_NAME", "rescan-test")
                .env("GIT_AUTHOR_EMAIL", "rescan@test")
                .env("GIT_COMMITTER_NAME", "rescan-test")
                .env("GIT_COMMITTER_EMAIL", "rescan@test")
                .output()
                .expect("git spawn");
            assert!(
                out.status.success(),
                "git {:?} failed: {}",
                args,
                String::from_utf8_lossy(&out.stderr)
            );
        };
        run(
            &["init", "--bare", "-b", "main", bare.to_str().unwrap()],
            work.path(),
        );
        run(&["init", "-b", "main", "."], work.path());
        std::fs::write(work.path().join("README"), "hi").unwrap();
        run(&["add", "README"], work.path());
        run(&["commit", "-m", "seed"], work.path());
        run(&["push", bare.to_str().unwrap(), "main"], work.path());

        let out = rescan_mirrors_from_disk(base.path());
        assert_eq!(out.len(), 1, "expected one reconstructed mirror");
        let entry = out.get("vigorepo:hostA:dan:myproj").expect("label key");
        assert_eq!(entry.seeder, "hostA");
        assert_eq!(entry.user, "dan");
        assert_eq!(entry.project, "myproj");
        assert_eq!(entry.bare_repo_path, bare);
        assert!(
            entry.blob_sha.is_empty(),
            "blob_sha must be empty to force a re-materialize on next tick"
        );
        assert_eq!(entry.git_sha.len(), 40, "git_sha must be full 40-char SHA");
        assert!(entry.git_sha.chars().all(|c| c.is_ascii_hexdigit()));
    }

    #[test]
    fn mirror_last_refreshed_prefers_fetch_head_mtime() {
        let dir = tempdir().unwrap();
        let bare = dir.path().join("proj.git");
        std::fs::create_dir_all(&bare).unwrap();
        std::fs::write(bare.join("HEAD"), b"ref: refs/heads/main\n").unwrap();
        std::fs::write(bare.join("FETCH_HEAD"), b"").unwrap();

        let t = mirror_last_refreshed_from_mtime(&bare).expect("some mtime");
        // Must be a plausible modern unix timestamp (post-2026).
        assert!(t > 1_767_225_600);
    }

    #[test]
    fn mirror_last_refreshed_falls_back_to_head_mtime() {
        let dir = tempdir().unwrap();
        let bare = dir.path().join("proj.git");
        std::fs::create_dir_all(&bare).unwrap();
        std::fs::write(bare.join("HEAD"), b"ref: refs/heads/main\n").unwrap();

        let t = mirror_last_refreshed_from_mtime(&bare).expect("some mtime");
        assert!(t > 1_767_225_600);
    }

    #[test]
    fn claims_handle_clones_share_state() {
        let a = ClaimsHandle::new();
        let b = a.clone();
        a.ingest_lan_claim("x", "peer1", true, 1000);
        assert!(b.snapshot().contains("x"));
    }

    // --- LAN claims (Phase 6) ---

    #[test]
    fn claims_handle_ingest_false_removes_specific_source_only() {
        // Two sources both claim the same label. Dropping one
        // should not affect the other — the label stays in the
        // union as long as at least one source claims it.
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("shared", "peer1", true, 1000);
        h.ingest_lan_claim("shared", "peer2", true, 1000);
        assert!(h.snapshot().contains("shared"));

        h.ingest_lan_claim("shared", "peer1", false, 1000);
        // Still there because peer2 still claims it.
        assert!(h.snapshot().contains("shared"));

        h.ingest_lan_claim("shared", "peer2", false, 1000);
        // Now gone.
        assert!(!h.snapshot().contains("shared"));
    }

    #[test]
    fn claims_handle_prune_lan_expired_drops_stale_entries() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("fresh", "peer1", true, 1000);
        h.ingest_lan_claim("stale", "peer1", true, 1);
        // Between the two ingests, nothing has expired yet.
        // Advance well past the TTL of the second (which was
        // added with now=1).
        let removed = h.prune_lan_expired(1 + LAN_CLAIM_TTL_SECS + 1);
        assert_eq!(removed, 1);
        let snap = h.snapshot();
        // "fresh" was ingested with now=1000, TTL=90, so expires
        // at 1090. The prune-now of (1 + TTL + 1) = 92 is still
        // well before 1090 — "fresh" survives.
        assert!(snap.contains("fresh"));
        assert!(!snap.contains("stale"));
    }

    #[test]
    fn claims_handle_refresh_extends_ttl() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("label", "peer", true, 1000);
        // Simulate a later re-announcement — should refresh the
        // expiry to now=5000 + TTL, not 1000 + TTL.
        h.ingest_lan_claim("label", "peer", true, 5000);
        // Prune at 1000 + TTL + 1: would evict if the first
        // timestamp were still in force, but the refresh means
        // expiry is 5000 + TTL, so the entry survives.
        let removed = h.prune_lan_expired(1000 + LAN_CLAIM_TTL_SECS + 1);
        assert_eq!(removed, 0);
        assert!(h.snapshot().contains("label"));
    }

    #[test]
    fn claims_handle_prune_no_expired_is_cheap() {
        let h = ClaimsHandle::new();
        h.ingest_lan_claim("a", "p", true, 1000);
        h.ingest_lan_claim("b", "p", true, 1000);
        // Prune well before TTL.
        let removed = h.prune_lan_expired(1000);
        assert_eq!(removed, 0);
        assert_eq!(h.snapshot().len(), 2);
    }

    #[test]
    fn claims_handle_shared_view_reflects_lan_ingest() {
        // The swarm manager reads from `shared()` which points
        // at the union view. LAN ingest must show up there as
        // well, not just via snapshot().
        let h = ClaimsHandle::new();
        let shared = h.shared();
        h.ingest_lan_claim("lan-label", "peer", true, 1000);
        let set = shared.read().unwrap().clone();
        assert!(set.contains("lan-label"));
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
        s.pending_wipes.insert("vigorepo:h:u:p".to_string(), 12345);
        let json = serde_json::to_string(&s).unwrap();
        assert!(json.contains("pending_wipes"));
        assert!(json.contains("12345"));

        let back: ReceiverState = serde_json::from_str(&json).unwrap();
        assert_eq!(back.pending_wipes.get("vigorepo:h:u:p"), Some(&12345));
    }

    // --- staleness walk (Phase 3) ---------------------------------

    /// Build a Receiver whose state.mirrors is pre-populated with the
    /// given entries. The blob cache, manifest, and state dir all
    /// live under tempdirs so the test is self-contained. The
    /// claims set is empty — Phase 1 (materialize), Phase 2 (wipe),
    /// Phase 3 (gc) are no-ops, leaving Phase 4 (stale walk) as the
    /// only meaningful work during tick().
    fn receiver_with_mirrors(mirrors: Vec<MirrorState>) -> (Receiver, tempfile::TempDir) {
        let base = tempdir().unwrap();
        let claims = ClaimsHandle::new();
        // Start from the default loaded-empty state then inject
        // mirrors directly. We use Receiver::new so cleanup_orphan
        // runs once (it's a no-op on an empty filesystem).
        let mut r = Receiver::new(base.path().to_path_buf(), claims);
        for m in mirrors {
            // Insert into state AND claims so Phase 2 doesn't mark
            // each mirror pending-wipe (which would corrupt the
            // stale-walk expectation by introducing unrelated state
            // changes). Uses ingest_lan_claim to register each
            // label under a synthetic "test" source.
            r.claims
                .ingest_lan_claim(&m.label, "test-source", true, 0);
            r.state.mirrors.insert(m.label.clone(), m);
        }
        (r, base)
    }

    fn stale_mirror(label: &str, last_refreshed: u64) -> MirrorState {
        MirrorState {
            label: label.to_string(),
            seeder: "host".to_string(),
            user: "dan".to_string(),
            project: label.split(':').next_back().unwrap_or("proj").to_string(),
            bare_repo_path: PathBuf::from(format!("/tmp/fake/{}.git", label)),
            blob_sha: "deadbeef".to_string(),
            git_sha: "cafef00d".to_string(),
            last_refreshed,
            next_gc_at: u64::MAX, // skip gc phase
        }
    }

    #[test]
    fn tick_returns_stale_labels_and_warns_once_per_day() {
        // Use wall-clock now because `tick()` calls unix_seconds_now()
        // internally and the staleness check compares against that.
        let now = unix_seconds_now();
        let very_old = now - (60 * 86400); // 60 days ago
        let fresh = now - (5 * 86400); // 5 days ago
        let border = now - super::super::STALE_THRESHOLD_SECS; // exactly threshold, not stale (strict >)

        let mirrors = vec![
            stale_mirror("vigorepo:host:dan:old1", very_old),
            stale_mirror("vigorepo:host:dan:old2", very_old),
            stale_mirror("vigorepo:host:dan:fresh", fresh),
            stale_mirror("vigorepo:host:dan:border", border),
        ];
        let (mut r, base) = receiver_with_mirrors(mirrors);

        // Need a manifest + cache to call tick; both are empty and
        // satisfy the signature without doing anything to our
        // pre-populated mirrors (the manifest has no entries so
        // Phase 1 materializes nothing and Phase 2 sees matching
        // claims so there are no pending wipes).
        let manifest = Manifest::load(base.path());
        let cache = BlobCache::new(base.path().join(".blobs")).unwrap();

        // First tick: two stale entries returned; both produce WARN
        // (last_stale_warn was empty, rate limiter lets everyone
        // through).
        let stale = r.tick(&manifest, &cache);
        let mut stale_sorted = stale.clone();
        stale_sorted.sort();
        assert_eq!(
            stale_sorted,
            vec![
                "vigorepo:host:dan:old1".to_string(),
                "vigorepo:host:dan:old2".to_string(),
            ],
        );
        // Rate limiter should have recorded both warnings.
        assert_eq!(r.last_stale_warn.len(), 2);

        // Second tick immediately after: same stale list (state
        // didn't change) but no new warnings fired because the
        // rate limit window (24h) hasn't elapsed. The caller
        // still gets the full stale list — rate limiting only
        // affects logging, not the trait payload.
        let before_warn_times: BTreeMap<String, u64> = r.last_stale_warn.clone();
        let stale_again = r.tick(&manifest, &cache);
        let mut stale_again_sorted = stale_again.clone();
        stale_again_sorted.sort();
        assert_eq!(stale_again_sorted, stale_sorted);
        // last_stale_warn unchanged → no fresh warnings fired.
        assert_eq!(r.last_stale_warn, before_warn_times);
    }

    #[test]
    fn tick_empty_state_returns_empty_stale_list() {
        let (mut r, base) = receiver_with_mirrors(Vec::new());
        let manifest = Manifest::load(base.path());
        let cache = BlobCache::new(base.path().join(".blobs")).unwrap();
        let stale = r.tick(&manifest, &cache);
        assert!(stale.is_empty());
        assert!(r.last_stale_warn.is_empty());
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

}
