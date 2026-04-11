// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Long-lived swarm download coordinator.
//!
//! Replaces the fire-and-forget `process_assignments()` pattern with a single
//! tokio task that owns all download state. Downloads are queued via a channel
//! and deduplicated — at most one download per blob, ever. Partial chunks on
//! disk survive process restarts; the scheduler resumes from them automatically.

use std::collections::HashSet;
use std::sync::Arc;

use tokio::sync::{mpsc, Semaphore};
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::manifest::Manifest;
use super::orchestrator;
use super::peermap::PeerMap;
use crate::proto::SwarmAssignment;
use crate::update::TlsCerts;

/// Maximum concurrent blob downloads (not chunks — each blob uses up to 8
/// chunk-level connections internally via the scheduler).
const MAX_CONCURRENT_DOWNLOADS: usize = 4;

/// Channel buffer size. Large enough to absorb bursts from check-in cycles
/// without blocking the caller.
const CHANNEL_BUFFER: usize = 64;

/// Handle for submitting download requests to the SwarmManager.
///
/// Cheap to clone — holds only a channel sender.
#[derive(Clone)]
pub struct SwarmManager {
    tx: mpsc::Sender<ManagerMessage>,
}

enum ManagerMessage {
    /// New swarm assignments from a check-in response.
    Assignments(Vec<SwarmAssignment>),
    /// Manifest was updated — re-evaluate self-assignments.
    ManifestUpdated,
}

impl SwarmManager {
    /// Spawn the manager task. Returns a handle for submitting requests.
    ///
    /// The manager runs until the cancellation token fires.
    pub fn spawn(
        peer_map: Arc<PeerMap>,
        manifest: Arc<Manifest>,
        tls: Option<TlsCerts>,
        cancel: tokio_util::sync::CancellationToken,
    ) -> Self {
        let (tx, rx) = mpsc::channel(CHANNEL_BUFFER);
        tokio::spawn(run(rx, peer_map, manifest, tls, cancel));
        Self { tx }
    }

    /// Queue swarm assignments for download. Non-blocking — returns immediately.
    /// Duplicate and already-cached blobs are filtered inside the manager task.
    pub async fn submit(&self, assignments: Vec<SwarmAssignment>) {
        if assignments.is_empty() {
            return;
        }
        if let Err(e) = self.tx.send(ManagerMessage::Assignments(assignments)).await {
            warn!(error = %e, "swarm manager channel closed");
        }
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
    tls: Option<TlsCerts>,
    cancel: tokio_util::sync::CancellationToken,
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
    // the server re-sends the same assignment).
    let mut completed: HashSet<String> = HashSet::new();
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
                        if !done.vigosync_user.is_empty() {
                            super::place_vigosync_file(&cache, &done.assignment);
                        }
                        completed.insert(done.sha256.clone());
                        info!(
                            sha256 = sha_short,
                            label = %done.label,
                            path = %path.display(),
                            "swarm blob downloaded successfully"
                        );
                    }
                    Err(e) => {
                        // Don't add to completed — allows retry on next check-in.
                        warn!(
                            sha256 = sha_short,
                            label = %done.label,
                            error = %e,
                            "swarm blob download failed"
                        );
                    }
                }
            }

            // Process messages from check-in loop.
            msg = rx.recv() => {
                let assignments = match msg {
                    Some(ManagerMessage::Assignments(a)) => a,
                    Some(ManagerMessage::ManifestUpdated) => {
                        // Clear completed entries for revoked blobs so re-seeding
                        // the same content (same SHA256) works after a revoke cycle.
                        // Without this, the manager thinks it already downloaded
                        // the blob and silently skips the re-assignment.
                        for sha in manifest.revoked_blobs() {
                            if completed.remove(&sha) {
                                let s = &sha[..12.min(sha.len())];
                                debug!(sha256 = s, "cleared completed for revoked blob");
                            }
                        }

                        // Self-assignment: evaluate manifest target specs locally.
                        let traits = std::collections::HashMap::new(); // TODO: pass cached traits
                        let entries = manifest.assignments_for(&hostname, &traits);
                        let mut converted = Vec::new();
                        for entry in entries {
                            converted.push(SwarmAssignment {
                                sha256: entry.sha256,
                                size: entry.size,
                                label: entry.label,
                                peers: vec![],
                                retention_seconds: parse_retention(&entry.retention),
                                vigosync_user: String::new(),
                                vigosync_filename: String::new(),
                                vigosync_mtime: 0,
                            });
                        }
                        converted
                    }
                    None => return, // channel closed
                };

                for assign in &assignments {
                    if assign.sha256.is_empty() || assign.size == 0 {
                        continue;
                    }

                    let sha_short = &assign.sha256[..12.min(assign.sha256.len())];

                    // Skip revoked blobs.
                    if manifest.is_revoked(&assign.sha256) {
                        debug!(sha256 = sha_short, "skipping revoked blob");
                        continue;
                    }

                    // Already on disk — maintain file links and move on.
                    if cache.has_blob(&assign.sha256) {
                        cache.link_by_name(&assign.sha256, &assign.label);
                        if !assign.vigosync_user.is_empty() {
                            super::place_vigosync_file(&cache, assign);
                        }
                        debug!(sha256 = sha_short, "blob already cached");
                        continue;
                    }

                    // Previously completed but no longer in cache (revoked then
                    // reseeded, manual deletion, or cache corruption). Clear the
                    // stale entry so the download restarts.
                    if completed.remove(&assign.sha256) {
                        info!(sha256 = sha_short, "completed blob missing from cache, re-downloading");
                    }

                    // Already downloading.
                    if in_progress.contains(&assign.sha256) {
                        debug!(sha256 = sha_short, "download already in progress");
                        continue;
                    }

                    in_progress.insert(assign.sha256.clone());

                    // Spawn a download task bounded by the semaphore.
                    let permit = semaphore.clone();
                    let pm = peer_map.clone();
                    let tls_clone = tls.clone();
                    let done_tx = done_tx.clone();
                    let assign = assign.clone();
                    let manifest = manifest.clone();
                    tokio::spawn(async move {
                        let _permit = match permit.acquire().await {
                            Ok(p) => p,
                            Err(_) => return, // semaphore closed — shutting down
                        };
                        let sha_short = &assign.sha256[..12.min(assign.sha256.len())];
                        info!(sha256 = sha_short, label = %assign.label, "starting swarm download");

                        // Deterministic jitter to stagger starts.
                        let jitter_ms = super::download_jitter(&assign.sha256);
                        if jitter_ms > 0 {
                            tokio::time::sleep(std::time::Duration::from_millis(jitter_ms)).await;
                        }

                        // Re-check revocation and cache after waiting for permit + jitter.
                        let cache = BlobCache::new(crate::platform::paths::default_blobs_dir());
                        let result = match cache {
                            Ok(ref c) if c.has_blob(&assign.sha256) => {
                                Ok(c.dir().join(&assign.sha256))
                            }
                            Ok(_) if manifest.is_revoked(&assign.sha256) => {
                                Err(anyhow::anyhow!("blob revoked while queued"))
                            }
                            Ok(ref c) => {
                                orchestrator::download(&assign, &pm, c, tls_clone.as_ref()).await
                            }
                            Err(e) => Err(e),
                        };

                        let _ = done_tx.send(DoneMessage {
                            sha256: assign.sha256.clone(),
                            label: assign.label.clone(),
                            vigosync_user: assign.vigosync_user.clone(),
                            assignment: assign,
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
    vigosync_user: String,
    assignment: SwarmAssignment,
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
