// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Vigorepo source-side reactor loop.
//!
//! The reactor is a single tokio task that wakes every
//! [`TICK_INTERVAL`] seconds and runs one "pass":
//!
//! 1. Re-scan every user's `~/vigorepo/*` via
//!    [`super::source::scan_local`].
//! 2. Load `state.json`.
//! 3. Call [`super::publisher::publish_source`] on every discovered
//!    [`Source`] — which is a no-op `git rev-parse HEAD` check when
//!    `state.git_sha == HEAD`, so steady-state ticks are cheap.
//!
//! ### Why polling instead of inotify
//!
//! The original plan called for an inotify ref-watcher. Polling won
//! on correctness: `git commit` may write to `refs/heads/<branch>`
//! directly *or* pack refs into `packed-refs`, and HEAD / reflog /
//! tempfile+rename all need coverage. On a personal-DR subsystem
//! with a handful of repos per host, a 5-second poll over
//! `git rev-parse HEAD` (~5 ms per call) is strictly simpler and
//! correct by construction. The locked-in 5-second debounce falls
//! out automatically: whatever HEAD is at tick time is what gets
//! published, and multiple commits inside the window collapse into
//! one publish.
//!
//! ### Edge-triggered logging
//!
//! Steady-state ticks are silent — a pass where every source is
//! `UpToDate` logs nothing. Discovery errors and publish failures
//! are tracked across ticks in [`PassState`] and only logged on the
//! edge (first tick where an error appears; follow-up tick where it
//! clears). This keeps a persistent failure from filling the log
//! every 5 seconds.

use std::collections::HashMap;
#[cfg(test)]
use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use super::publisher::{self, Outcome, PublishContext, PublishError};
use super::receiver::{ClaimsHandle, Receiver};
use super::source;
use super::state::{
    unix_seconds_now, PublishErrorEntry, SourceErrorEntry, SourceTraitEntry, State, TraitsSnapshot,
};
use crate::swarm::cache::BlobCache;
use crate::swarm::manifest::Manifest;

/// How often the reactor runs a full pass. 5 seconds matches the
/// locked-in debounce window: multiple commits within this window
/// collapse into a single publish.
pub const TICK_INTERVAL: Duration = Duration::from_secs(5);

/// Spawn the reactor as a tokio task. Returns immediately; the task
/// lives until `cancel` fires.
///
/// `claims` and `manifest` together drive the receiver side: claims
/// is the per-envoy wanted-labels handle written by the check-in
/// loop, manifest is the live in-memory swarm CRDT used to resolve
/// labels to blob shas. Both `None` is allowed and disables the
/// receiver entirely (publish-only operation, useful for tests and
/// for envoys with the swarm subsystem disabled).
pub fn spawn(
    cancel: CancellationToken,
    claims: Option<ClaimsHandle>,
    manifest: Option<Arc<Manifest>>,
    peer_table: Option<Arc<super::peer_table::PeerTable>>,
    pending_claims: Option<Arc<super::pending_claims::PendingClaims>>,
    tls: Option<crate::update::TlsCerts>,
    gate: super::Gate,
) {
    tokio::spawn(run(
        cancel,
        claims,
        manifest,
        peer_table,
        pending_claims,
        tls,
        gate,
    ));
}

async fn run(
    cancel: CancellationToken,
    claims: Option<ClaimsHandle>,
    manifest: Option<Arc<Manifest>>,
    peer_table: Option<Arc<super::peer_table::PeerTable>>,
    pending_claims: Option<Arc<super::pending_claims::PendingClaims>>,
    tls: Option<crate::update::TlsCerts>,
    gate: super::Gate,
) {
    let hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();
    info!(
        hostname = %hostname,
        tick_secs = TICK_INTERVAL.as_secs(),
        receiver_enabled = claims.is_some() && manifest.is_some(),
        "vigorepo: reactor started"
    );

    let mut pass_state = PassState::new(hostname);
    let mut tick = tokio::time::interval(TICK_INTERVAL);
    tick.set_missed_tick_behavior(MissedTickBehavior::Delay);

    // Start conservatively: the first tick observes `gate.enabled()`
    // and, if already on, runs the OFF→ON activation once. This
    // preserves the old behavior of running state-reset + git check
    // before the first publish, without duplicating that logic at
    // spawn time.
    let mut prev_enabled = false;
    let mut activation_failed = false;

    loop {
        tokio::select! {
            biased;

            _ = cancel.cancelled() => {
                info!("vigorepo: reactor shutting down");
                return;
            }

            _ = tick.tick() => {
                let now_enabled = gate.enabled();

                // Transition: OFF → ON. Run the one-shot activation
                // work (cleanup stale bundles, reset publisher state,
                // git version check). If it fails we stay dormant
                // and retry on the next OFF→ON edge.
                if now_enabled && !prev_enabled {
                    info!("vigorepo: subsystem activated");
                    activation_failed = super::activate().is_err();
                }

                // Transition: ON → OFF. Clear the shared LAN peer
                // table and pending-claims map so a dormant envoy
                // doesn't carry stale state, and log once so
                // operators can see the flip in the journal.
                if !now_enabled && prev_enabled {
                    info!("vigorepo: subsystem deactivated");
                    if let Some(pt) = peer_table.as_ref() {
                        pt.clear();
                    }
                    if let Some(pc) = pending_claims.as_ref() {
                        pc.clear();
                    }
                    // Drop in-memory pass bookkeeping so the next
                    // activation starts with a clean slate.
                    pass_state = PassState::new(pass_state.hostname.clone());
                }

                prev_enabled = now_enabled;

                // Prune expired LAN peer entries on every tick.
                // Cheap when the table is empty or small; keeps
                // the Phase 5 TTL promise (5 min) without needing
                // a separate sweeper task. Runs even while dormant
                // (the table should already be empty, and the prune
                // is a no-op in that case).
                if let Some(pt) = peer_table.as_ref() {
                    pt.prune_expired();
                }
                if let Some(ch) = claims.as_ref() {
                    ch.prune_lan_expired(unix_seconds_now());
                }

                // Dormant: no publish, no receive — fast return.
                if !now_enabled || activation_failed {
                    continue;
                }

                let state = std::mem::take(&mut pass_state);
                let claims_clone = claims.clone();
                let manifest_clone = manifest.clone();
                let peer_table_clone = peer_table.clone();
                let pending_claims_clone = pending_claims.clone();
                let tls_clone = tls.clone();
                // Publisher and receiver both do filesystem I/O and
                // spawn git subprocesses — run on a blocking worker
                // so we don't stall the async runtime.
                let joined = tokio::task::spawn_blocking(move || {
                    let mut s = state;
                    let summary = s.run_pass(
                        claims_clone,
                        manifest_clone,
                        peer_table_clone,
                        pending_claims_clone,
                        tls_clone,
                    );
                    (s, summary)
                })
                .await;
                match joined {
                    Ok((s, summary)) => {
                        pass_state = s;
                        summary.log();
                    }
                    Err(e) => {
                        warn!(error = %e, "vigorepo: reactor pass panicked");
                        pass_state = PassState::new_lost();
                    }
                }
            }
        }
    }
}

/// Persistent state carried across reactor ticks so we can
/// edge-trigger log messages instead of spamming every tick.
#[derive(Default)]
pub struct PassState {
    hostname: String,
    /// Paths whose most recent discovery attempt produced an error.
    /// Map value is the reason string, so we can detect reason
    /// changes (same path, different error) as a new edge.
    prev_discovery_errors: HashMap<PathBuf, String>,
    /// Labels whose most recent publish attempt failed. Map value is
    /// the error string for the same reason-change detection.
    prev_publish_errors: HashMap<String, String>,
    /// Lazily-initialized receiver. `Some` once the first tick that
    /// has both `claims` and `manifest` arguments runs (i.e. once
    /// vigorepo's receiver-side wiring is fully present). `None` on
    /// publish-only deployments and during the first tick before
    /// initialization.
    receiver: Option<Receiver>,
    /// Per-label dedup state for Phase 6 LAN claim announcements:
    /// the most recently emitted host list for each source. A tick
    /// that produces the same list as last time skips emission
    /// (unless the safety-net re-announce interval has elapsed).
    prev_claim_lists: HashMap<String, Vec<String>>,
    /// Per-label timestamp of the most recent claim emission. Used
    /// by [`pending_claims::decide_emit`] to force a re-broadcast
    /// every [`pending_claims::RE_ANNOUNCE_INTERVAL_SECS`] even if
    /// the list hasn't changed, so late-joining receivers catch up
    /// before their LAN claim TTL expires.
    last_claim_emit: HashMap<String, u64>,
    /// Phase 7 cross-subnet push dedup state: per `(label, target)`
    /// sha + timestamp so unchanged bundles don't re-upload every
    /// tick. Reset on process restart (acceptable — one fresh push
    /// per target on startup refreshes the receiver's LAN claim).
    extra_push_state: super::extra_mirrors::ExtraPushState,
    /// Per-`(label, target)` last-logged error string. In-memory
    /// dedup for push failures: a persistent DNS miss or refused
    /// connection logs once, then goes silent until the error
    /// changes or clears. Mirrors the publish-error edge-trigger
    /// pattern elsewhere in the reactor.
    prev_push_errors: HashMap<(String, String), String>,
}

impl PassState {
    pub fn new(hostname: String) -> Self {
        Self {
            hostname,
            prev_discovery_errors: HashMap::new(),
            prev_publish_errors: HashMap::new(),
            receiver: None,
            prev_claim_lists: HashMap::new(),
            last_claim_emit: HashMap::new(),
            extra_push_state: super::extra_mirrors::ExtraPushState::new(),
            prev_push_errors: HashMap::new(),
        }
    }

    /// Construct a fresh state after a tick worker panic. Loses edge
    /// history so the next tick may re-log persistent errors once —
    /// acceptable, and panics are already a warning.
    fn new_lost() -> Self {
        Self::default()
    }

    /// Run one pass: scan + publish, then receiver materialize/wipe/gc,
    /// then write the traits snapshot. Returns a summary for the
    /// reactor loop to log.
    ///
    /// `claims` and `manifest` enable the receiver side. Both `None`
    /// is publish-only mode (test fixtures and swarm-disabled
    /// deployments).
    pub fn run_pass(
        &mut self,
        claims: Option<ClaimsHandle>,
        manifest: Option<Arc<Manifest>>,
        peer_table: Option<Arc<super::peer_table::PeerTable>>,
        pending_claims: Option<Arc<super::pending_claims::PendingClaims>>,
        tls: Option<crate::update::TlsCerts>,
    ) -> PassSummary {
        if self.hostname.is_empty() {
            // Lost-state recovery path — re-read hostname. This only
            // happens after a tick worker panic.
            self.hostname = std::fs::read_to_string("/etc/hostname")
                .unwrap_or_else(|_| "unknown".to_string())
                .trim()
                .to_string();
        }

        let scan = source::scan_local(&self.hostname);

        self.log_discovery_error_edges(&scan.errors);

        // --- LAN claim selection + publish (Phase 6) ---
        // For every non-paused discovered source, run the
        // deterministic selector over the LAN peer table and
        // publish the result into the shared PendingClaims cache.
        // The swarm announcer reads this on each multicast send
        // and emits `VIGOREPOCLAIM:` lines within MTU budget.
        //
        // Rate-limited to content changes plus a safety-net
        // re-announce every RE_ANNOUNCE_INTERVAL_SECS so
        // late-joining receivers catch up before their LAN claim
        // TTL expires.
        if let (Some(pt), Some(pc)) = (peer_table.as_ref(), pending_claims.as_ref()) {
            self.run_lan_claim_selection(&scan.sources, pt, pc);
        }

        let ctx = PublishContext::from_defaults();

        // --- Cross-subnet direct push (Phase 7) ---
        // For every source with non-empty `extra_mirrors`, iterate
        // the named targets and POST the bundle directly over
        // mTLS. Runs after the publisher so the bundle on disk
        // reflects the current HEAD. Skipped entirely when mTLS
        // certs aren't available (e.g. pre-enrollment).
        if let Some(tls_ref) = tls.as_ref() {
            self.run_extra_mirrors_push(&scan.sources, tls_ref, &ctx);
        }

        let mut summary = PassSummary {
            sources: scan.sources.len(),
            ..PassSummary::default()
        };

        // --- receive (phase 1: run tick to collect stale mirror list) ---
        // Run the receiver tick BEFORE the publisher-side trait
        // snapshot write so we can include the stale mirrors list
        // AND the full materialized mirror set in the snapshot. The
        // receiver and publisher operate on disjoint labels (the
        // seeder check excludes the seeder envoy from its own claim
        // list), so tick order between them is safe either way.
        let mut stale_mirrors: Vec<String> = Vec::new();
        let mut mirror_entries: Vec<super::state::MirrorTraitEntry> = Vec::new();
        if let (Some(claims), Some(manifest)) = (claims, manifest) {
            // Lazy-init the Receiver on first tick. Construction
            // does I/O (load state, run orphan cleanup) so we want
            // it inside the blocking worker rather than spawn.
            if self.receiver.is_none() {
                let state_dir = crate::platform::paths::default_vigorepo_dir();
                self.receiver = Some(Receiver::new(state_dir, claims));
            }
            match BlobCache::new(crate::platform::paths::default_blobs_dir()) {
                Ok(cache) => {
                    if let Some(receiver) = self.receiver.as_mut() {
                        stale_mirrors = receiver.tick(&manifest, &cache);
                        mirror_entries = receiver.mirrors_snapshot(unix_seconds_now());
                    }
                }
                Err(e) => {
                    warn!(
                        error = %e,
                        "vigorepo reactor: blob cache unavailable; receiver tick skipped"
                    );
                }
            };
        }

        // Snapshot the publisher's selected-mirrors map so the trait
        // snapshot carries `selected_mirrors` per source. Empty map
        // when pending_claims is absent (swarm subsystem disabled)
        // or when the selector hasn't chosen anyone yet.
        let selected_by_label: HashMap<String, Vec<String>> = pending_claims
            .as_ref()
            .map(|pc| pc.snapshot().into_iter().collect())
            .unwrap_or_default();

        // --- publish ---
        let publisher_state = if scan.sources.is_empty() {
            // Still write a snapshot so the trait collector can
            // report an honest "no sources discovered" state with a
            // fresh timestamp, instead of showing stale data from a
            // previous run.
            write_traits_snapshot(
                &ctx.state_dir,
                &scan.errors,
                &HashMap::new(),
                &[],
                &State::default(),
                &summary,
                &stale_mirrors,
                &selected_by_label,
                mirror_entries.clone(),
            );
            None
        } else {
            let mut state = State::load(&ctx.state_dir);
            let mut current_publish_errors: HashMap<String, String> = HashMap::new();

            for src in &scan.sources {
                match publisher::publish_source(src, &mut state, &ctx) {
                    Ok(Outcome::Paused { .. }) => summary.paused += 1,
                    Ok(Outcome::UpToDate { .. }) => summary.up_to_date += 1,
                    Ok(Outcome::Published { full, .. }) => {
                        summary.published += 1;
                        if full {
                            summary.full_bundles += 1;
                        }
                    }
                    Err(e) => {
                        summary.failed += 1;
                        let label = publish_error_label(&e);
                        current_publish_errors.insert(label.to_string(), e.to_string());
                    }
                }
            }

            self.log_publish_error_edges(&current_publish_errors);

            write_traits_snapshot(
                &ctx.state_dir,
                &scan.errors,
                &current_publish_errors,
                &scan.sources,
                &state,
                &summary,
                &stale_mirrors,
                &selected_by_label,
                mirror_entries.clone(),
            );

            self.prev_publish_errors = current_publish_errors;
            Some(state)
        };
        let _ = publisher_state; // currently consumed only by the snapshot writer above

        summary
    }

    /// Run the Phase 7 cross-subnet push loop for one reactor
    /// tick. For each source with a non-empty `extra_mirrors:` list,
    /// iterate the named hosts and POST the current bundle if
    /// [`super::extra_mirrors::ExtraPushState::should_push`]
    /// approves (first-time push, sha change, or re-push interval
    /// elapsed). Errors log once per edge and don't interrupt the
    /// iteration — a broken target doesn't starve the others.
    ///
    /// Paused sources and sources with an empty `extra_mirrors`
    /// list are skipped. Paused source dedup state is cleared so
    /// an un-pause re-sends on the next tick.
    fn run_extra_mirrors_push(
        &mut self,
        sources: &[source::Source],
        tls: &crate::update::TlsCerts,
        ctx: &PublishContext,
    ) {
        let now = unix_seconds_now();

        for src in sources {
            if src.sidecar.paused {
                // Clear state for this label so an un-pause produces
                // a fresh push on the next tick.
                self.extra_push_state.forget_label(&src.label);
                continue;
            }
            if src.sidecar.extra_mirrors.is_empty() {
                continue;
            }

            // Find the current bundle file on disk. The publisher
            // wrote it to `seeds_dir/<label>` (see
            // `publisher::write_seed_from_path`).
            let bundle_path = ctx.seeds_dir.join(&src.label);
            if !bundle_path.exists() {
                // Source has never successfully published — nothing
                // to push yet. The publisher will retry on the next
                // tick and we'll pick up the bundle then.
                continue;
            }
            // Compute sha of the current bundle. Reading the file
            // twice is acceptable — bundles are cached in OS page
            // cache between the sha and the push.
            let sha = match sha256_file(&bundle_path) {
                Ok(s) => s,
                Err(e) => {
                    debug!(
                        label = %src.label,
                        path = %bundle_path.display(),
                        error = %e,
                        "vigorepo: could not sha bundle for extra-mirror push"
                    );
                    continue;
                }
            };

            for target in &src.sidecar.extra_mirrors {
                if !self
                    .extra_push_state
                    .should_push(&src.label, target, &sha, now)
                {
                    continue;
                }

                let result = super::extra_mirrors::push_bundle(
                    target,
                    &src.label,
                    &self.hostname,
                    &bundle_path,
                    tls,
                );

                let err_key = (src.label.clone(), target.clone());
                match result {
                    Ok(()) => {
                        self.extra_push_state.record_success(
                            &src.label, target, &sha, now,
                        );
                        if self.prev_push_errors.remove(&err_key).is_some() {
                            info!(
                                label = %src.label,
                                target = %target,
                                "vigorepo: cross-subnet push recovered"
                            );
                        }
                        debug!(
                            label = %src.label,
                            target = %target,
                            sha = %&sha[..12.min(sha.len())],
                            "vigorepo: cross-subnet push succeeded"
                        );
                    }
                    Err(e) => {
                        let msg = e.to_string();
                        let prev = self.prev_push_errors.get(&err_key);
                        if prev.map(|p| p != &msg).unwrap_or(true) {
                            warn!(
                                label = %src.label,
                                target = %target,
                                error = %msg,
                                "vigorepo: cross-subnet push failed (will retry)"
                            );
                            self.prev_push_errors.insert(err_key, msg);
                        }
                    }
                }
            }
        }
    }

    /// Run the LAN claim selection loop for one reactor tick.
    /// For every non-paused source:
    ///
    /// 1. Snapshot the peer table.
    /// 2. Filter it by the sidecar's include/exclude globs and
    ///    require the repo's `owner` to exist on each candidate.
    /// 3. Run the deterministic [`lan_selector::select_hostnames`]
    ///    to pick up to `max_mirrors`.
    /// 4. Decide whether to publish via
    ///    [`pending_claims::decide_emit`] — changes or safety
    ///    re-announce window.
    /// 5. On `Emit`, update the shared `PendingClaims` cache
    ///    (which the swarm announcer drains on its next send)
    ///    and record the new bookkeeping in `self.prev_claim_lists`
    ///    + `self.last_claim_emit`.
    ///
    /// Paused sources and sources whose sidecar validation failed
    /// are skipped — they don't show up in `scan.sources` at all.
    fn run_lan_claim_selection(
        &mut self,
        sources: &[source::Source],
        peer_table: &Arc<super::peer_table::PeerTable>,
        pending_claims: &Arc<super::pending_claims::PendingClaims>,
    ) {
        use super::pending_claims::{decide_emit, EmitDecision};
        let now = unix_seconds_now();
        let peers = peer_table.snapshot();

        for src in sources {
            if src.sidecar.paused {
                // Drop any previously-announced claim for a newly
                // paused source so receivers stop wanting the label.
                // forget() + the dedup state reset cause the next
                // tick after un-pausing to re-emit cleanly.
                pending_claims.forget(&src.label);
                self.prev_claim_lists.remove(&src.label);
                self.last_claim_emit.remove(&src.label);
                continue;
            }

            // Filter peer table by sidecar rules + owning-user presence.
            // The owning user is the Unix account whose home holds the
            // source directory (also encoded as the `user` segment of
            // the label). A receiver only qualifies as a mirror
            // candidate if that user exists locally — otherwise the
            // recovery flow would clone into a non-existent home dir.
            let filtered: Vec<super::peer_table::PeerEntry> = peers
                .iter()
                .cloned()
                .filter(|p| {
                    if !p.users.iter().any(|u| u == &src.owning_user) {
                        return false;
                    }
                    // Exclude globs win.
                    for pat in &src.sidecar.exclude_envoys {
                        if super::peer_table::hostname_matches_glob(&p.hostname, pat) {
                            return false;
                        }
                    }
                    // Include globs act as a whitelist when non-empty.
                    if !src.sidecar.include_envoys.is_empty() {
                        return src
                            .sidecar
                            .include_envoys
                            .iter()
                            .any(|pat| super::peer_table::hostname_matches_glob(&p.hostname, pat));
                    }
                    true
                })
                .collect();

            // Run the deterministic selector.
            let new_list = super::lan_selector::select_hostnames(
                &src.label,
                &filtered,
                src.sidecar.max_mirrors,
            );

            // Rate-limit decision.
            let prev = self.prev_claim_lists.get(&src.label).map(|v| v.as_slice());
            let last_at = self.last_claim_emit.get(&src.label).copied();
            match decide_emit(prev, &new_list, last_at, now) {
                EmitDecision::Skip => {
                    // No-op.
                }
                EmitDecision::Emit(list) => {
                    // One log per selection CHANGE (not per tick). Reports
                    // the full funnel — peer table size, eligible after
                    // filters, selected after the deterministic pick —
                    // so an empty selected list is diagnosable from the
                    // journal without tcpdump. Bugs like the owning-user
                    // vs sidecar-owner mixup surface instantly here.
                    info!(
                        label = %src.label,
                        peers = peers.len(),
                        eligible = filtered.len(),
                        selected = list.len(),
                        owning_user = %src.owning_user,
                        "vigorepo: LAN selection updated"
                    );
                    pending_claims.publish(&src.label, list.clone());
                    self.prev_claim_lists.insert(src.label.clone(), list);
                    self.last_claim_emit.insert(src.label.clone(), now);
                }
            }
        }
    }

    fn log_discovery_error_edges(&mut self, errors: &[source::SourceError]) {
        let current: HashMap<PathBuf, String> = errors
            .iter()
            .map(|e| (e.path.clone(), e.reason.clone()))
            .collect();

        // New or changed errors.
        for (path, reason) in &current {
            match self.prev_discovery_errors.get(path) {
                Some(prev_reason) if prev_reason == reason => {}
                _ => warn!(
                    path = %path.display(),
                    reason = %reason,
                    "vigorepo: source discovery error"
                ),
            }
        }

        // Errors that cleared.
        for path in self.prev_discovery_errors.keys() {
            if !current.contains_key(path) {
                info!(
                    path = %path.display(),
                    "vigorepo: source discovery error cleared"
                );
            }
        }

        self.prev_discovery_errors = current;
    }

    fn log_publish_error_edges(&self, current: &HashMap<String, String>) {
        for (label, reason) in current {
            match self.prev_publish_errors.get(label) {
                Some(prev) if prev == reason => {}
                _ => warn!(
                    label = %label,
                    error = %reason,
                    "vigorepo: source publish failed"
                ),
            }
        }

        for label in self.prev_publish_errors.keys() {
            if !current.contains_key(label) {
                info!(label = %label, "vigorepo: source publish recovered");
            }
        }
    }
}

/// Build a [`TraitsSnapshot`] reflecting the current pass and save
/// it atomically. Failure to save is logged but never propagated —
/// a write error must not stall the reactor or the publisher, and
/// the next successful pass will overwrite a stale snapshot anyway.
fn write_traits_snapshot(
    state_dir: &std::path::Path,
    discovery_errors: &[source::SourceError],
    publish_errors: &HashMap<String, String>,
    sources: &[source::Source],
    state: &State,
    summary: &PassSummary,
    stale_mirrors: &[String],
    selected_mirrors_by_label: &HashMap<String, Vec<String>>,
    mirrors: Vec<super::state::MirrorTraitEntry>,
) {
    let source_entries: Vec<SourceTraitEntry> = sources
        .iter()
        .map(|src| {
            let entry = state.labels.get(&src.label);
            let selected = selected_mirrors_by_label
                .get(&src.label)
                .cloned()
                .unwrap_or_default();
            SourceTraitEntry {
                label: src.label.clone(),
                paused: src.sidecar.paused,
                max_mirrors: src.sidecar.max_mirrors,
                tags: src.sidecar.tags.clone(),
                description: src.sidecar.description.clone(),
                include_envoys: src.sidecar.include_envoys.clone(),
                exclude_envoys: src.sidecar.exclude_envoys.clone(),
                when: src.sidecar.when.clone(),
                extra_mirrors: src.sidecar.extra_mirrors.clone(),
                git_sha: entry.map(|s| s.git_sha.clone()),
                blob_sha: entry.map(|s| s.blob_sha.clone()),
                last_publish_full: entry.and_then(|s| s.last_publish_full),
                last_bundle_size: entry.and_then(|s| s.last_bundle_size),
                selected_mirrors: selected,
            }
        })
        .collect();

    let source_error_entries: Vec<SourceErrorEntry> = discovery_errors
        .iter()
        .map(|e| SourceErrorEntry {
            path: e.path.display().to_string(),
            reason: e.reason.clone(),
        })
        .collect();

    let publish_error_entries: Vec<PublishErrorEntry> = publish_errors
        .iter()
        .map(|(label, reason)| PublishErrorEntry {
            label: label.clone(),
            reason: reason.clone(),
        })
        .collect();

    let snapshot = TraitsSnapshot {
        version: super::state::TRAITS_SNAPSHOT_VERSION,
        updated_at: unix_seconds_now(),
        active_sources: summary.sources.saturating_sub(summary.paused),
        paused_sources: summary.paused,
        sources: source_entries,
        source_errors: source_error_entries,
        publish_errors: publish_error_entries,
        stale_mirrors: stale_mirrors.to_vec(),
        mirrors,
    };

    if let Err(e) = snapshot.save(state_dir) {
        warn!(
            error = %e,
            dir = %state_dir.display(),
            "vigorepo: failed to save traits snapshot; trait collector will report stale data"
        );
    }
}

/// Compute a hex-encoded sha256 over the contents of a file.
/// Used by the Phase 7 cross-subnet push path to compute the
/// bundle sha for dedup + header. Reads in a streaming loop to
/// avoid buffering the whole file in memory — bundles cap at 20
/// GiB but a typical personal-DR case is tens to hundreds of MB.
fn sha256_file(path: &std::path::Path) -> std::io::Result<String> {
    use sha2::{Digest, Sha256};
    use std::io::Read;
    let mut f = std::fs::File::open(path)?;
    let mut h = Sha256::new();
    let mut buf = [0u8; 64 * 1024];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    let out = h.finalize();
    let mut s = String::with_capacity(64);
    for b in out.iter() {
        s.push_str(&format!("{:02x}", b));
    }
    Ok(s)
}

/// Extract a label from a [`PublishError`] for edge-trigger tracking.
/// Every variant currently carries a label; this function exists so
/// that if new variants are added later, the match is exhaustive and
/// the compiler will force the caller to decide how to key them.
fn publish_error_label(e: &PublishError) -> &str {
    match e {
        PublishError::PathReCheck { label, .. }
        | PublishError::HeadParse { label, .. }
        | PublishError::BundleCreate { label, .. }
        | PublishError::OversizedBundle { label, .. }
        | PublishError::Io { label, .. } => label,
    }
}

/// One-tick summary passed from the blocking worker back to the
/// async reactor for logging.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct PassSummary {
    pub sources: usize,
    pub published: usize,
    pub full_bundles: usize,
    pub up_to_date: usize,
    pub paused: usize,
    pub failed: usize,
}

impl PassSummary {
    /// Edge-triggered summary logging. A steady-state "every source
    /// up to date" pass logs nothing; only ticks that actually
    /// published or failed produce a line.
    pub fn log(&self) {
        if self.published == 0 && self.failed == 0 {
            // Quiet tick — debug-level in case someone turns up
            // tracing for diagnostics.
            debug!(
                sources = self.sources,
                up_to_date = self.up_to_date,
                paused = self.paused,
                "vigorepo: reactor pass (quiet)"
            );
            return;
        }
        info!(
            sources = self.sources,
            published = self.published,
            full_bundles = self.full_bundles,
            up_to_date = self.up_to_date,
            paused = self.paused,
            failed = self.failed,
            "vigorepo: reactor pass"
        );
    }

    /// Total non-quiet entries — handy for tests.
    pub fn total_visible_activity(&self) -> usize {
        self.published + self.failed
    }
}

// --- internal helpers used by tests below ---------------------------

/// For tests: compute the set of keys added between two maps.
#[cfg(test)]
fn added<K: Eq + std::hash::Hash + Clone, V>(
    prev: &HashMap<K, V>,
    cur: &HashMap<K, V>,
) -> HashSet<K> {
    cur.keys()
        .filter(|k| !prev.contains_key(*k))
        .cloned()
        .collect()
}

/// For tests: compute the set of keys removed between two maps.
#[cfg(test)]
fn removed<K: Eq + std::hash::Hash + Clone, V>(
    prev: &HashMap<K, V>,
    cur: &HashMap<K, V>,
) -> HashSet<K> {
    prev.keys()
        .filter(|k| !cur.contains_key(*k))
        .cloned()
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pass_summary_quiet_when_no_activity() {
        let s = PassSummary {
            sources: 3,
            up_to_date: 3,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 0);
    }

    #[test]
    fn pass_summary_visible_when_published() {
        let s = PassSummary {
            sources: 2,
            published: 1,
            up_to_date: 1,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 1);
    }

    #[test]
    fn pass_summary_visible_when_failed() {
        let s = PassSummary {
            sources: 1,
            failed: 1,
            ..Default::default()
        };
        assert_eq!(s.total_visible_activity(), 1);
    }

    #[test]
    fn publish_error_label_extracts_from_every_variant() {
        let e1 = PublishError::PathReCheck {
            label: "l1".to_string(),
            reason: "r".to_string(),
        };
        let e2 = PublishError::HeadParse {
            label: "l2".to_string(),
            reason: "r".to_string(),
        };
        let e3 = PublishError::BundleCreate {
            label: "l3".to_string(),
            reason: "r".to_string(),
        };
        let e4 = PublishError::OversizedBundle {
            label: "l4".to_string(),
            size: 1,
            limit: 1,
        };
        let e5 = PublishError::Io {
            label: "l5".to_string(),
            source: std::io::Error::other("x"),
        };
        assert_eq!(publish_error_label(&e1), "l1");
        assert_eq!(publish_error_label(&e2), "l2");
        assert_eq!(publish_error_label(&e3), "l3");
        assert_eq!(publish_error_label(&e4), "l4");
        assert_eq!(publish_error_label(&e5), "l5");
    }

    #[test]
    fn added_and_removed_helpers_compute_symmetric_difference() {
        let mut prev: HashMap<String, ()> = HashMap::new();
        prev.insert("a".to_string(), ());
        prev.insert("b".to_string(), ());

        let mut cur: HashMap<String, ()> = HashMap::new();
        cur.insert("b".to_string(), ());
        cur.insert("c".to_string(), ());

        let added_set = added(&prev, &cur);
        let removed_set = removed(&prev, &cur);

        assert_eq!(added_set, HashSet::from(["c".to_string()]));
        assert_eq!(removed_set, HashSet::from(["a".to_string()]));
    }

    #[test]
    fn pass_state_discovery_error_edges_log_new_and_cleared() {
        // Smoke-test: construct a PassState, feed it a discovery
        // error list, then an empty list, and verify the internal
        // map updates correctly. (We can't capture tracing output
        // here without a subscriber, but we can at least prove the
        // bookkeeping is right.)
        let mut s = PassState::new("danlap".to_string());

        let err1 = source::SourceError {
            path: PathBuf::from("/home/dan/vigorepo/a"),
            reason: "boom".to_string(),
        };
        s.log_discovery_error_edges(&[err1]);
        assert_eq!(s.prev_discovery_errors.len(), 1);
        assert_eq!(
            s.prev_discovery_errors
                .get(&PathBuf::from("/home/dan/vigorepo/a"))
                .unwrap(),
            "boom"
        );

        s.log_discovery_error_edges(&[]);
        assert!(s.prev_discovery_errors.is_empty());
    }

    #[test]
    fn pass_state_discovery_error_reason_change_counts_as_edge() {
        let mut s = PassState::new("danlap".to_string());

        let err1 = source::SourceError {
            path: PathBuf::from("/home/dan/vigorepo/a"),
            reason: "boom".to_string(),
        };
        s.log_discovery_error_edges(&[err1]);

        let err2 = source::SourceError {
            path: PathBuf::from("/home/dan/vigorepo/a"),
            reason: "different".to_string(),
        };
        s.log_discovery_error_edges(&[err2]);
        // The new reason is now the stored one.
        assert_eq!(
            s.prev_discovery_errors
                .get(&PathBuf::from("/home/dan/vigorepo/a"))
                .unwrap(),
            "different"
        );
    }

    #[test]
    fn pass_state_publish_error_edges_track_current() {
        let s = PassState::new("danlap".to_string());
        let mut current: HashMap<String, String> = HashMap::new();
        current.insert("vigorepo:h:u:p".to_string(), "err-a".to_string());
        // Just exercises the function — no panic, no output capture.
        s.log_publish_error_edges(&current);
    }
}
