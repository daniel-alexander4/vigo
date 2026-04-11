// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Rarest-first chunk scheduler for multi-peer parallel downloads.
//!
//! Builds an availability matrix from the PeerMap, sorts missing chunks
//! by peer count ascending (rarest first), and assigns chunks to peers
//! with bounded concurrency (max 8 simultaneous downloads).

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use anyhow::Result;
use tokio::sync::{mpsc, Semaphore};
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::meta::BlobMeta;
use super::peer_client;
use super::peermap::{PeerMap, PeerSource};
use crate::update::TlsCerts;

/// Maximum concurrent peer chunk downloads.
const MAX_PEER_CONNECTIONS: usize = 8;

/// Maximum backoff between retry rounds (seconds).
const MAX_BACKOFF_SECS: u64 = 30;

/// How often (in chunks) the producer refreshes peer availability from
/// the PeerMap. At ~100ms per chunk, 100 chunks ≈ 10 seconds — fast
/// enough to pick up new peers discovered via multicast (5s interval).
const PEER_REFRESH_INTERVAL: usize = 100;

/// Result of downloading a single chunk.
struct ChunkDownloadResult {
    index: u32,
    data: Vec<u8>,
    /// Peers discovered via peer exchange.
    new_peers: Vec<String>,
    source: String,
    duration_ms: u64,
}

/// Maximum assembly retries after SHA256 verification failure.
/// Each failure blacklists the peers that served chunks in the bad build.
const MAX_ASSEMBLY_RETRIES: usize = 2;

/// Download all missing chunks of a blob from peers with retry.
///
/// Returns Ok(path) on success (all chunks downloaded + assembled).
pub async fn download_from_swarm(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &Arc<PeerMap>,
    tls: Option<&TlsCerts>,
) -> Result<std::path::PathBuf> {
    // If already complete, return immediately.
    if cache.has_blob(&meta.sha256) {
        return Ok(cache.dir().join(&meta.sha256));
    }

    // Check disk space.
    cache.check_disk_space(meta.size)?;

    // Build peer agent (blocking mTLS client) if we have certs.
    let peer_agent = tls.and_then(|t| match peer_client::build_peer_agent(t) {
        Ok(agent) => Some(agent),
        Err(e) => {
            warn!(error = %e, "failed to build peer TLS agent — chunk downloads will be skipped");
            None
        }
    });

    // Peers blacklisted due to serving corrupted chunks (assembly SHA256 mismatch).
    let mut blacklisted_peers: HashSet<String> = HashSet::new();

    for assembly_attempt in 0..=MAX_ASSEMBLY_RETRIES {
        if assembly_attempt > 0 {
            info!(
                sha256 = &meta.sha256[..12],
                attempt = assembly_attempt,
                blacklisted = blacklisted_peers.len(),
                "retrying download after assembly failure"
            );
        }

        match download_and_assemble(meta, cache, peer_map, &peer_agent, &blacklisted_peers).await {
            Ok(path) => return Ok(path),
            Err(e) => {
                let msg = e.to_string();
                if !msg.contains("SHA256 mismatch") {
                    return Err(e);
                }

                // Assembly failed — identify and blacklist peers that served chunks.
                warn!(
                    sha256 = &meta.sha256[..12],
                    error = %e,
                    "assembly verification failed, blacklisting source peers"
                );
                if let Some(sources) = cache.read_chunk_sources(&meta.sha256) {
                    for peer in sources.counts.keys() {
                        if peer != "server" {
                            blacklisted_peers.insert(peer.clone());
                        }
                    }
                }
                // Delete corrupted partial chunks so they're re-downloaded.
                cache.delete_partial(&meta.sha256);
            }
        }
    }

    anyhow::bail!(
        "assembly failed after {} retries with {} blacklisted peers",
        MAX_ASSEMBLY_RETRIES,
        blacklisted_peers.len()
    )
}

/// Inner download + assemble cycle. Separated so the outer function can
/// retry with blacklisted peers on assembly failure.
async fn download_and_assemble(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &Arc<PeerMap>,
    peer_agent: &Option<ureq::Agent>,
    blacklisted_peers: &HashSet<String>,
) -> Result<std::path::PathBuf> {
    let existing = cache.chunk_bitmap(&meta.sha256, meta.chunk_count);
    let missing: Vec<u32> = (0..meta.chunk_count)
        .filter(|i| !existing.contains(i))
        .collect();

    if missing.is_empty() {
        return cache.assemble(&meta.sha256, meta.size, meta.chunk_count);
    }

    let download_start = std::time::Instant::now();

    info!(
        sha256 = &meta.sha256[..12],
        missing = missing.len(),
        total = meta.chunk_count,
        blacklisted = blacklisted_peers.len(),
        "scheduling chunk downloads"
    );

    // Build availability matrix, excluding blacklisted peers.
    let peers = peer_map.peers_for(&meta.sha256);
    let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
    for chunk_idx in &missing {
        let mut available_peers = Vec::new();
        for p in &peers {
            if p.chunks.contains(chunk_idx) && !blacklisted_peers.contains(&p.address) {
                available_peers.push(p.address.clone());
            }
        }
        availability.insert(*chunk_idx, available_peers);
    }

    let mut ordered: Vec<u32> = missing;
    ordered.sort_by(|a, b| {
        let count_a = availability.get(a).map(|v| v.len()).unwrap_or(0);
        let count_b = availability.get(b).map(|v| v.len()).unwrap_or(0);
        count_a.cmp(&count_b).then(a.cmp(b))
    });

    let mut source_counts: HashMap<String, u32> = HashMap::new();
    let mut chunk_details: Vec<(u32, String, u64)> = Vec::new();

    // Retry loop: attempt all missing chunks, retry until no progress is made.
    let mut remaining: Vec<u32> = ordered;
    let mut attempt: u32 = 0;

    loop {
        if remaining.is_empty() {
            break;
        }

        if attempt > 0 {
            let backoff = std::time::Duration::from_secs(2u64.pow(attempt.min(5)).min(MAX_BACKOFF_SECS));
            info!(
                sha256 = &meta.sha256[..12],
                attempt,
                remaining = remaining.len(),
                backoff_secs = backoff.as_secs(),
                "retrying failed chunks"
            );
            tokio::time::sleep(backoff).await;

            // Refresh peer availability — new peers may have appeared.
            let peers = peer_map.peers_for(&meta.sha256);
            for chunk_idx in &remaining {
                let mut available_peers = Vec::new();
                for p in &peers {
                    if p.chunks.contains(chunk_idx) && !blacklisted_peers.contains(&p.address) {
                        available_peers.push(p.address.clone());
                    }
                }
                availability.insert(*chunk_idx, available_peers);
            }
        }

        let (tx, mut rx) = mpsc::channel::<ChunkDownloadResult>(MAX_PEER_CONNECTIONS * 2);
        let sem = Arc::new(Semaphore::new(MAX_PEER_CONNECTIONS));

        // Spawn the producer in a separate task so the consumer can drain
        // results concurrently. Without this, the for loop and recv loop
        // are sequential — the channel fills up after 16 results, blocking
        // senders that hold semaphore permits, deadlocking the for loop.
        let spawn_remaining = remaining.clone();
        let mut spawn_availability = availability.clone();
        let spawn_peer_agent = peer_agent.clone();
        let spawn_sha = meta.sha256.clone();
        let spawn_sem = sem.clone();
        let spawn_peer_map = peer_map.clone();
        let spawn_blacklist = blacklisted_peers.clone();
        tokio::spawn(async move {
            let mut prev_peer_count = spawn_availability.values().next().map(|v| v.len()).unwrap_or(0);
            for (i, &chunk_idx) in spawn_remaining.iter().enumerate() {
                // Refresh peer availability periodically — picks up new peers
                // discovered via multicast while we're downloading.
                if i > 0 && i % PEER_REFRESH_INTERVAL == 0 {
                    let fresh_peers = spawn_peer_map.peers_for(&spawn_sha);
                    if fresh_peers.len() != prev_peer_count {
                        info!(
                            sha256 = &spawn_sha[..12],
                            peers = fresh_peers.len(),
                            prev = prev_peer_count,
                            chunk = i,
                            "refreshed peer list mid-download"
                        );
                        prev_peer_count = fresh_peers.len();
                    }
                    for &remaining_chunk in &spawn_remaining[i..] {
                        let mut available = Vec::new();
                        for p in &fresh_peers {
                            if p.chunks.contains(&remaining_chunk) && !spawn_blacklist.contains(&p.address) {
                                available.push(p.address.clone());
                            }
                        }
                        spawn_availability.insert(remaining_chunk, available);
                    }
                }

                let mut peer_addrs = spawn_availability.get(&chunk_idx).cloned().unwrap_or_default();
                if peer_addrs.len() > 1 {
                    let start = chunk_idx as usize % peer_addrs.len();
                    peer_addrs.rotate_left(start);
                }
                let permit = match spawn_sem.clone().acquire_owned().await {
                    Ok(p) => p,
                    Err(_) => return, // semaphore closed
                };
                let tx = tx.clone();
                let sha = spawn_sha.clone();
                let peer_agent = spawn_peer_agent.clone();

                tokio::task::spawn_blocking(move || {
                    let _permit = permit;
                    let start = std::time::Instant::now();

                    if let Some(ref agent) = peer_agent {
                        for peer_addr in &peer_addrs {
                            match peer_client::fetch_chunk(agent, peer_addr, &sha, chunk_idx) {
                                Ok(result) => {
                                    let _ = tx.blocking_send(ChunkDownloadResult {
                                        index: chunk_idx,
                                        data: result.data,
                                        new_peers: result.peers,
                                        source: peer_addr.clone(),
                                        duration_ms: start.elapsed().as_millis() as u64,
                                    });
                                    return;
                                }
                                Err(e) => {
                                    warn!(
                                        peer = peer_addr.as_str(),
                                        chunk = chunk_idx,
                                        error = %e,
                                        "peer chunk download failed, trying next"
                                    );
                                }
                            }
                        }
                    }
                });
            }
            // tx is dropped here when all tasks are spawned, closing the channel.
        });

        let mut succeeded: HashSet<u32> = HashSet::new();
        while let Some(result) = rx.recv().await {
            cache.write_chunk(&meta.sha256, result.index, &result.data)?;
            succeeded.insert(result.index);

            *source_counts.entry(result.source.clone()).or_insert(0) += 1;
            chunk_details.push((result.index, result.source.clone(), result.duration_ms));

            for peer_addr in &result.new_peers {
                peer_map.report(
                    &meta.sha256,
                    peer_addr,
                    HashSet::new(),
                    PeerSource::PeerExchange,
                );
            }

            debug!(
                chunk = result.index,
                source = result.source.as_str(),
                "chunk received"
            );
        }

        let before = remaining.len();
        remaining.retain(|idx| !succeeded.contains(idx));

        if remaining.is_empty() {
            break;
        }

        if succeeded.is_empty() {
            let received = meta.chunk_count - remaining.len() as u32;
            anyhow::bail!(
                "no progress: got {}/{} chunks, {} remaining with no available peers",
                received,
                meta.chunk_count,
                remaining.len()
            );
        }

        info!(
            sha256 = &meta.sha256[..12],
            fetched = before - remaining.len(),
            remaining = remaining.len(),
            "round complete, continuing"
        );
        attempt += 1;
    }

    let peer_chunks: u32 = source_counts.values().sum();

    info!(
        sha256 = &meta.sha256[..12],
        peer_chunks,
        "all chunks received, assembling"
    );

    let total_ms = download_start.elapsed().as_millis() as u64;
    cache.write_chunk_sources(&meta.sha256, &source_counts, &chunk_details, total_ms);

    cache.assemble(&meta.sha256, meta.size, meta.chunk_count)
}

#[cfg(test)]
mod tests {
    use super::*;
    use super::super::meta::CHUNK_SIZE;

    #[test]
    fn test_rarest_first_ordering() {
        // Simulate availability: chunk 0 has 3 peers, chunk 1 has 1 peer, chunk 2 has 0.
        let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
        availability.insert(0, vec!["a".into(), "b".into(), "c".into()]);
        availability.insert(1, vec!["a".into()]);
        availability.insert(2, Vec::new());

        let mut ordered: Vec<u32> = vec![0, 1, 2];
        ordered.sort_by(|a, b| {
            let count_a = availability.get(a).map(|v| v.len()).unwrap_or(0);
            let count_b = availability.get(b).map(|v| v.len()).unwrap_or(0);
            count_a.cmp(&count_b).then(a.cmp(b))
        });

        // Rarest first: chunk 2 (0 peers), chunk 1 (1 peer), chunk 0 (3 peers).
        assert_eq!(ordered, vec![2, 1, 0]);
    }
}
