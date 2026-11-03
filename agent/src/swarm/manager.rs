// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Long-lived swarm download coordinator.
//!
//! A single tokio task owns all download state. Self-assignment is driven by
//! the manifest CRDT: on `ManifestUpdated` the manager re-evaluates the local
//! manifest against this envoy's hostname + traits and queues any matching
//! blobs that aren't already cached. Downloads are deduplicated — at most one
//! per blob, ever. Partial chunks on disk survive process restarts; the
//! scheduler resumes from them automatically.

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, RwLock};
use std::time::{Duration, Instant};

use tokio::sync::{mpsc, Semaphore};
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::manifest::{BlobEntry, Manifest};
use super::orchestrator;
use super::peermap::PeerMap;
use super::AnnounceSignal;
use crate::state::StateStore;
use crate::update::TlsCerts;

/// Maximum concurrent blob downloads (not chunks — each blob uses up to 8
/// chunk-level connections internally via the scheduler).
const MAX_CONCURRENT_DOWNLOADS: usize = 4;

/// Channel buffer size.
const CHANNEL_BUFFER: usize = 64;

/// How long a stuck-state entry suppresses spawns + WARN logs before the
/// loop retries (in case the underlying condition has cleared, e.g. the
/// operator freed disk space). Long enough to avoid journal flood,
/// short enough that recovery doesn't require an agent restart.
const STUCK_RETRY_INTERVAL: Duration = Duration::from_secs(30 * 60);

/// Per-sha record of "we've already logged this failure, don't re-spam."
/// Tracks the last error reason and when it was first/last logged so the
/// loop can suppress identical retry failures while still surfacing
/// state changes (different error, recovery).
#[derive(Debug)]
struct StuckEntry {
    reason: String,
    /// First time we saw this reason for this sha (for retry-interval gating).
    first_seen: Instant,
    /// Total identical failures observed since `first_seen` — surfaced in
    /// the recovery log line so operators know how long it was stuck.
    repeats: u32,
}

/// Handle for signaling the SwarmManager. Cheap to clone.
#[derive(Clone)]
pub struct SwarmManager {
    tx: mpsc::Sender<ManagerMessage>,
}

enum ManagerMessage {
    /// Manifest was updated — re-evaluate self-assignments.
    ManifestUpdated,
}

impl SwarmManager {
    /// Spawn the manager task. Runs until the cancellation token fires.
    ///
    /// `gitback_wanted`, when supplied, is a shared label set written
    /// by the check-in handler when it processes a `gitback_claims`
    /// payload. On every `ManifestUpdated` evaluation the manager
    /// merges these claim-driven entries into the standard
    /// self-assignment work set, so gitback content seeds — which
    /// are flagged claim-only by [`Manifest::assignments_for`] — get
    /// downloaded only when the server-issued claim list explicitly
    /// names them. Pass `None` to disable gitback integration.
    pub fn spawn(
        peer_map: Arc<PeerMap>,
        manifest: Arc<Manifest>,
        state: Option<Arc<StateStore>>,
        tls: Option<TlsCerts>,
        cancel: tokio_util::sync::CancellationToken,
        gitback_wanted: Option<Arc<RwLock<HashSet<String>>>>,
        announce: Arc<AnnounceSignal>,
    ) -> Self {
        let (tx, rx) = mpsc::channel(CHANNEL_BUFFER);
        tokio::spawn(run(
            rx,
            peer_map.clone(),
            manifest,
            state,
            tls.clone(),
            cancel.clone(),
            gitback_wanted,
            announce,
        ));
        // Background: re-probe stale peers so per-chunk scheduling keeps
        // pace with partial downloads elsewhere in the fleet.
        tokio::spawn(super::prober::run_refresh_loop(peer_map, tls, cancel));
        Self { tx }
    }

    /// Signal that the manifest was updated. Triggers self-assignment evaluation.
    pub async fn manifest_updated(&self) {
        let _ = self.tx.send(ManagerMessage::ManifestUpdated).await;
    }
}

/// Manager event loop. Runs for the lifetime of the process.
async fn run(
    mut rx: mpsc::Receiver<ManagerMessage>,
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    state: Option<Arc<StateStore>>,
    tls: Option<TlsCerts>,
    cancel: tokio_util::sync::CancellationToken,
    gitback_wanted: Option<Arc<RwLock<HashSet<String>>>>,
    announce: Arc<AnnounceSignal>,
) {
    let cache = match BlobCache::new(crate::platform::paths::default_blobs_dir()) {
        Ok(c) => c,
        Err(e) => {
            warn!(error = %e, "swarm manager: failed to initialize blob cache");
            return;
        }
    };

    // Blobs currently being downloaded.
    let mut in_progress: HashSet<String> = HashSet::new();
    // Blobs completed in this process lifetime (avoids re-downloading after
    // a manifest update re-issues the same entry).
    let mut completed: HashSet<String> = HashSet::new();
    // Stuck-state gate: blobs whose last download attempt returned an
    // error string we've seen before. Suppresses spawn AND the WARN
    // "swarm blob download failed" log line on repeats — without this
    // gate, a permanently-failing condition (e.g. insufficient disk
    // for the blob on this envoy) emits 3 log lines every ~12 s
    // forever, flooding the journal and the disk it sits on. The
    // entry is cleared on (a) successful download for this sha,
    // (b) a different error, (c) a manifest update for this sha, or
    // (d) STUCK_RETRY_INTERVAL elapsing (so transient conditions like
    // "no peers known" eventually re-attempt without manual nudging).
    let mut stuck: HashMap<String, StuckEntry> = HashMap::new();
    // Limits concurrent blob downloads.
    let semaphore = Arc::new(Semaphore::new(MAX_CONCURRENT_DOWNLOADS));
    // Channel for download tasks to report completion/failure back.
    let (done_tx, mut done_rx) = mpsc::channel::<DoneMessage>(64);

    // Hostname for self-assignment target matching.
    let hostname = std::fs::read_to_string("/etc/hostname")
        .unwrap_or_else(|_| "unknown".to_string())
        .trim()
        .to_string();

    info!(hostname = %hostname, "swarm manager started");

    loop {
        tokio::select! {
            biased;

            _ = cancel.cancelled() => {
                info!("swarm manager shutting down");
                return;
            }

            // Process completion/failure reports from download tasks.
            Some(done) = done_rx.recv() => {
                in_progress.remove(&done.sha256);
                let sha_short = &done.sha256[..12.min(done.sha256.len())];
                match done.result {
                    Ok(path) => {
                        cache.link_by_name(&done.sha256, &done.label);
                        // Record retention deadline now that the blob is on disk.
                        if let Some(ref s) = state {
                            super::record_retention(&done.sha256, done.retention_seconds, s);
                        }
                        completed.insert(done.sha256.clone());
                        // State transition: if we were stuck, surface the
                        // recovery so a journal scrape can find both ends
                        // of the stuck window without grepping for absent
                        // log lines.
                        if let Some(prev) = stuck.remove(&done.sha256) {
                            info!(
                                sha256 = sha_short,
                                label = %done.label,
                                previous_error = %prev.reason,
                                repeats = prev.repeats,
                                "swarm blob recovered after stuck-state"
                            );
                        }
                        info!(
                            sha256 = sha_short,
                            label = %done.label,
                            path = %path.display(),
                            "swarm blob downloaded successfully"
                        );
                    }
                    Err(e) => {
                        // Don't add to completed — allows retry on next manifest update.
                        // Anti-flood: if we've already logged this exact
                        // error for this sha, suppress the WARN. The
                        // spawn-side gate further upstream stops re-issuing
                        // the download until the entry expires or the
                        // manifest changes — so this branch only fires on
                        // first-occurrence, error-string changes, or
                        // post-retry-interval re-attempts.
                        let reason = format!("{:#}", e);
                        let entry = stuck.entry(done.sha256.clone()).or_insert_with(|| StuckEntry {
                            reason: reason.clone(),
                            first_seen: Instant::now(),
                            repeats: 0,
                        });
                        if entry.reason == reason {
                            // Same error as before — silent increment.
                            entry.repeats = entry.repeats.saturating_add(1);
                        } else {
                            // Different error — log the transition AND the
                            // count of the previous error so an operator
                            // grepping the journal can correlate.
                            warn!(
                                sha256 = sha_short,
                                label = %done.label,
                                previous_error = %entry.reason,
                                previous_repeats = entry.repeats,
                                new_error = %reason,
                                "swarm blob download failure changed"
                            );
                            entry.reason = reason;
                            entry.first_seen = Instant::now();
                            entry.repeats = 0;
                        }
                        if entry.repeats == 0 {
                            // First time seeing this (sha, reason) combo.
                            warn!(
                                sha256 = sha_short,
                                label = %done.label,
                                error = %entry.reason,
                                "swarm blob download failed (entered stuck state; further identical failures suppressed)"
                            );
                        }
                    }
                }
            }

            // Process messages from check-in loop.
            msg = rx.recv() => {
                let entries: Vec<BlobEntry> = match msg {
                    Some(ManagerMessage::ManifestUpdated) => {
                        // Clear completed entries for revoked blobs so re-seeding
                        // the same content (same SHA256) works after a revoke cycle.
                        for sha in manifest.revoked_blobs() {
                            if completed.remove(&sha) {
                                let s = &sha[..12.min(sha.len())];
                                debug!(sha256 = s, "cleared completed for revoked blob");
                            }
                        }

                        // Self-assignment: evaluate manifest target specs locally
                        // against this envoy's hostname + its most recently
                        // cached traits (so `--target 'os.distro=ubuntu'`-style
                        // specs resolve here, not just hostname globs). Re-read
                        // each manifest update so trait changes are picked up;
                        // empty map (no cache / parse error) ⇒ hostname-only
                        // matching, same as before.
                        let traits = state
                            .as_ref()
                            .map(|s| super::cached_traits(s))
                            .unwrap_or_default();
                        let mut matched = manifest.assignments_for(&hostname, &traits);

                        // Gitback claim-driven downloads. The gitback content
                        // seed namespace is excluded from `assignments_for`
                        // (claim-only filter); the only path to downloading
                        // them is via the per-envoy claim list issued by the
                        // server's `ClaimTable` and shipped through the
                        // check-in handler into the shared wanted set below.
                        if let Some(ref wanted) = gitback_wanted {
                            let snapshot = {
                                let guard = wanted.read().unwrap_or_else(|e| e.into_inner());
                                guard.clone()
                            };
                            if !snapshot.is_empty() {
                                let claim_entries = manifest.entries_for_labels(&snapshot);
                                if !claim_entries.is_empty() {
                                    debug!(
                                        count = claim_entries.len(),
                                        "swarm manager: merging gitback claim-driven entries"
                                    );
                                }
                                matched.extend(claim_entries);
                            }
                        }

                        // Seed PeerMap with seeder addresses from the manifest.
                        for entry in &matched {
                            if !entry.seeder_address.is_empty() {
                                let all_chunks: std::collections::HashSet<u32> =
                                    (0..super::meta::chunks_for_size(entry.size)).collect();
                                peer_map.report(
                                    &entry.sha256,
                                    &entry.seeder_address,
                                    all_chunks,
                                    super::peermap::PeerSource::Server,
                                );
                            }
                        }
                        matched
                    }
                    None => return, // channel closed
                };

                for entry in &entries {
                    if entry.sha256.is_empty() || entry.size == 0 {
                        continue;
                    }

                    let sha_short = &entry.sha256[..12.min(entry.sha256.len())];

                    // Skip revoked blobs.
                    if manifest.is_revoked(&entry.sha256) {
                        debug!(sha256 = sha_short, "skipping revoked blob");
                        continue;
                    }

                    // Already on disk — maintain file links and record retention.
                    if cache.has_blob(&entry.sha256) {
                        cache.link_by_name(&entry.sha256, &entry.label);
                        if let Some(ref s) = state {
                            super::record_retention(
                                &entry.sha256,
                                parse_retention(&entry.retention),
                                s,
                            );
                        }
                        debug!(sha256 = sha_short, "blob already cached");
                        continue;
                    }

                    // Stuck-state gate: if the last download attempt
                    // failed with a reason we've already logged, skip the
                    // spawn until STUCK_RETRY_INTERVAL elapses (operators
                    // may have freed disk space, brought peers online,
                    // etc.) or the manifest update cycle clears the entry.
                    // Without this gate the manager would re-spawn every
                    // ~12 s on permanently-failing conditions, flooding
                    // the journal with identical insufficient_disk lines.
                    if let Some(prev) = stuck.get(&entry.sha256) {
                        if prev.first_seen.elapsed() < STUCK_RETRY_INTERVAL {
                            debug!(
                                sha256 = sha_short,
                                error = %prev.reason,
                                repeats = prev.repeats,
                                "skipping stuck blob (suppressed by anti-flood gate)"
                            );
                            continue;
                        }
                        // Retry interval elapsed — clear so the warn fires
                        // again if the same condition still holds.
                        info!(
                            sha256 = sha_short,
                            error = %prev.reason,
                            repeats = prev.repeats,
                            "stuck-state retry interval elapsed; re-attempting download"
                        );
                        stuck.remove(&entry.sha256);
                    }

                    // Previously completed but no longer in cache (revoked then
                    // reseeded, manual deletion, or cache corruption). Clear the
                    // stale entry so the download restarts.
                    if completed.remove(&entry.sha256) {
                        info!(sha256 = sha_short, "completed blob missing from cache, re-downloading");
                    }

                    // Already downloading.
                    if in_progress.contains(&entry.sha256) {
                        debug!(sha256 = sha_short, "download already in progress");
                        continue;
                    }

                    in_progress.insert(entry.sha256.clone());

                    // Spawn a download task bounded by the semaphore.
                    let permit = semaphore.clone();
                    let pm = peer_map.clone();
                    let tls_clone = tls.clone();
                    let done_tx = done_tx.clone();
                    let entry = entry.clone();
                    let manifest = manifest.clone();
                    let announce = announce.clone();
                    tokio::spawn(async move {
                        let _permit = match permit.acquire().await {
                            Ok(p) => p,
                            Err(_) => return, // semaphore closed — shutting down
                        };
                        let sha_short = &entry.sha256[..12.min(entry.sha256.len())];
                        info!(sha256 = sha_short, label = %entry.label, "starting swarm download");

                        // Deterministic jitter to stagger starts.
                        let jitter_ms = super::download_jitter(&entry.sha256);
                        if jitter_ms > 0 {
                            tokio::time::sleep(std::time::Duration::from_millis(jitter_ms)).await;
                        }

                        // Re-check revocation and cache after waiting for permit + jitter.
                        let cache = BlobCache::new(crate::platform::paths::default_blobs_dir());
                        let result = match cache {
                            Ok(ref c) if c.has_blob(&entry.sha256) => {
                                Ok(c.dir().join(&entry.sha256))
                            }
                            Ok(_) if manifest.is_revoked(&entry.sha256) => {
                                Err(anyhow::anyhow!("blob revoked while queued"))
                            }
                            Ok(ref c) => {
                                orchestrator::download(&entry, &pm, c, tls_clone.as_ref(), &manifest, &announce).await
                            }
                            Err(e) => Err(e),
                        };

                        let _ = done_tx.send(DoneMessage {
                            sha256: entry.sha256.clone(),
                            label: entry.label.clone(),
                            retention_seconds: parse_retention(&entry.retention),
                            result,
                        }).await;
                    });
                }
            }
        }
    }
}

/// Completion report from a download task.
struct DoneMessage {
    sha256: String,
    label: String,
    retention_seconds: u64,
    result: anyhow::Result<std::path::PathBuf>,
}

/// Parse a retention string like "7d", "24h", "30m" into seconds.
fn parse_retention(s: &str) -> u64 {
    let s = s.trim();
    if s.is_empty() {
        return 0;
    }
    let (num, suffix) = s.split_at(s.len().saturating_sub(1));
    let n: u64 = num.parse().unwrap_or(0);
    match suffix {
        "d" => n * 86400,
        "h" => n * 3600,
        "m" => n * 60,
        "s" => n,
        _ => s.parse().unwrap_or(0), // bare number = seconds
    }
}
