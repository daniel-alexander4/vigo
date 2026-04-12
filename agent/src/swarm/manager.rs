// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Long-lived swarm download coordinator.
//!
//! A single tokio task owns all download state. Self-assignment is driven by
//! the manifest CRDT: on `ManifestUpdated` the manager re-evaluates the local
//! manifest against this envoy's hostname + traits and queues any matching
//! blobs that aren't already cached. Downloads are deduplicated — at most one
//! per blob, ever. Partial chunks on disk survive process restarts; the
//! scheduler resumes from them automatically.

use std::collections::HashSet;
use std::sync::Arc;

use tokio::sync::{mpsc, Semaphore};
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::manifest::{BlobEntry, Manifest};
use super::orchestrator;
use super::peermap::PeerMap;
use crate::state::StateStore;
use crate::update::TlsCerts;

/// Maximum concurrent blob downloads (not chunks — each blob uses up to 8
/// chunk-level connections internally via the scheduler).
const MAX_CONCURRENT_DOWNLOADS: usize = 4;

/// Channel buffer size.
const CHANNEL_BUFFER: usize = 64;

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
    pub fn spawn(
        peer_map: Arc<PeerMap>,
        manifest: Arc<Manifest>,
        state: Option<Arc<StateStore>>,
        tls: Option<TlsCerts>,
        cancel: tokio_util::sync::CancellationToken,
    ) -> Self {
        let (tx, rx) = mpsc::channel(CHANNEL_BUFFER);
        tokio::spawn(run(rx, peer_map, manifest, state, tls, cancel));
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
                        info!(
                            sha256 = sha_short,
                            label = %done.label,
                            path = %path.display(),
                            "swarm blob downloaded successfully"
                        );
                    }
                    Err(e) => {
                        // Don't add to completed — allows retry on next manifest update.
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

                        // Self-assignment: evaluate manifest target specs locally.
                        let traits = std::collections::HashMap::new(); // TODO: pass cached traits
                        let matched = manifest.assignments_for(&hostname, &traits);

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
                                orchestrator::download(&entry, &pm, c, tls_clone.as_ref()).await
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
