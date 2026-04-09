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
use tracing::{debug, info};

use super::cache::BlobCache;
use super::meta::BlobMeta;
use super::peer_client;
use super::peermap::{PeerMap, PeerSource};
use crate::update::TlsCerts;

/// Maximum concurrent peer chunk downloads.
const MAX_PEER_CONNECTIONS: usize = 8;

/// Maximum retry attempts for failed chunks within a single download cycle.
const MAX_RETRIES: usize = 3;

/// Result of downloading a single chunk.
struct ChunkDownloadResult {
    index: u32,
    data: Vec<u8>,
    /// Peers discovered via peer exchange.
    new_peers: Vec<String>,
    source: String,
    duration_ms: u64,
}

/// Download all missing chunks of a blob from peers with retry.
///
/// Returns Ok(path) on success (all chunks downloaded + assembled).
pub async fn download_from_swarm(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &PeerMap,
    tls: Option<&TlsCerts>,
) -> Result<std::path::PathBuf> {
    // If already complete, return immediately.
    if cache.has_blob(&meta.sha256) {
        return Ok(cache.dir().join(&meta.sha256));
    }

    // Check disk space.
    cache.check_disk_space(meta.size)?;

    let existing = cache.chunk_bitmap(&meta.sha256, meta.chunk_count);
    let missing: Vec<u32> = (0..meta.chunk_count)
        .filter(|i| !existing.contains(i))
        .collect();

    if missing.is_empty() {
        // All chunks present, just need assembly.
        return cache.assemble(&meta.sha256, meta.size, meta.chunk_count);
    }

    let download_start = std::time::Instant::now();

    info!(
        sha256 = &meta.sha256[..12],
        missing = missing.len(),
        total = meta.chunk_count,
        "scheduling chunk downloads"
    );

    // Build availability matrix: chunk → Vec<peer_addr>.
    let peers = peer_map.peers_for(&meta.sha256);
    let mut availability: HashMap<u32, Vec<String>> = HashMap::new();
    for chunk_idx in &missing {
        let mut available_peers = Vec::new();
        for p in &peers {
            if p.chunks.contains(chunk_idx) {
                available_peers.push(p.address.clone());
            }
        }
        availability.insert(*chunk_idx, available_peers);
    }

    // Sort by rarest first (fewest peers), break ties by index.
    let mut ordered: Vec<u32> = missing.clone();
    ordered.sort_by(|a, b| {
        let count_a = availability.get(a).map(|v| v.len()).unwrap_or(0);
        let count_b = availability.get(b).map(|v| v.len()).unwrap_or(0);
        count_a.cmp(&count_b).then(a.cmp(b))
    });

    // Build peer agent (blocking mTLS client) if we have certs.
    let peer_agent = tls.and_then(|t| peer_client::build_peer_agent(t).ok());

    let mut source_counts: HashMap<String, u32> = HashMap::new();
    let mut chunk_details: Vec<(u32, String, u64)> = Vec::new();

    // Retry loop: attempt all missing chunks, retry failures up to MAX_RETRIES
    // times with backoff. Between retries, the peer map may gain new sources
    // as other envoys accumulate chunks and announce via multicast.
    let mut remaining: Vec<u32> = ordered;

    for attempt in 0..=MAX_RETRIES {
        if remaining.is_empty() {
            break;
        }

        if attempt > 0 {
            let backoff = std::time::Duration::from_secs(2u64.pow(attempt as u32).min(30));
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
                    if p.chunks.contains(chunk_idx) {
                        available_peers.push(p.address.clone());
                    }
                }
                availability.insert(*chunk_idx, available_peers);
            }
        }

        let (tx, mut rx) = mpsc::channel::<ChunkDownloadResult>(MAX_PEER_CONNECTIONS * 2);
        let sem = Arc::new(Semaphore::new(MAX_PEER_CONNECTIONS));

        for &chunk_idx in &remaining {
            let peer_addrs = availability.get(&chunk_idx).cloned().unwrap_or_default();
            let permit = sem.clone().acquire_owned().await?;
            let tx = tx.clone();
            let sha = meta.sha256.clone();
            let peer_agent = peer_agent.clone();

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
                                debug!(
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

        drop(tx);

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

        remaining.retain(|idx| !succeeded.contains(idx));
    }

    let received = meta.chunk_count - remaining.len() as u32;
    if !remaining.is_empty() {
        anyhow::bail!(
            "incomplete download after {} retries: got {}/{} chunks",
            MAX_RETRIES,
            received,
            meta.chunk_count
        );
    }

    let peer_chunks: u32 = source_counts.values().sum();

    info!(
        sha256 = &meta.sha256[..12],
        peer_chunks,
        "all chunks received, assembling"
    );

    // Write source data so the trait collector can report it.
    let total_ms = download_start.elapsed().as_millis() as u64;
    cache.write_chunk_sources(&meta.sha256, &source_counts, &chunk_details, total_ms);

    // Assemble and verify.
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
