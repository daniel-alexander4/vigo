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

/// Result of downloading a single chunk.
struct ChunkDownloadResult {
    index: u32,
    data: Vec<u8>,
    /// Peers discovered via peer exchange.
    new_peers: Vec<String>,
    source: String,
}

/// Download all missing chunks of a blob from peers, falling back to the
/// server for chunks that no peer has or when peer downloads fail.
///
/// Returns Ok(path) on success (all chunks downloaded + assembled).
pub async fn download_from_swarm(
    meta: &BlobMeta,
    cache: &BlobCache,
    peer_map: &PeerMap,
    server_url: &str,
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

    // Channel for completed chunks.
    let (tx, mut rx) = mpsc::channel::<ChunkDownloadResult>(MAX_PEER_CONNECTIONS * 2);
    let sem = Arc::new(Semaphore::new(MAX_PEER_CONNECTIONS));

    // Spawn download tasks for each missing chunk.
    let meta_clone = meta.clone();
    let server_url_owned = server_url.to_string();
    let tls_owned = tls.cloned();

    for chunk_idx in ordered {
        let peer_addrs = availability.get(&chunk_idx).cloned().unwrap_or_default();
        let permit = sem.clone().acquire_owned().await?;
        let tx = tx.clone();
        let sha = meta_clone.sha256.clone();
        let server_url = server_url_owned.clone();
        let tls = tls_owned.clone();
        let peer_agent = peer_agent.clone();

        tokio::task::spawn_blocking(move || {
            let _permit = permit;

            // Try peers first (in order of availability).
            if let Some(ref agent) = peer_agent {
                for peer_addr in &peer_addrs {
                    match peer_client::fetch_chunk(agent, peer_addr, &sha, chunk_idx) {
                        Ok(result) => {
                            let _ = tx.blocking_send(ChunkDownloadResult {
                                index: chunk_idx,
                                data: result.data,
                                new_peers: result.peers,
                                source: peer_addr.clone(),
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

            // Fallback: download from server.
            match download_chunk_from_server(&server_url, &sha, chunk_idx, tls.as_ref()) {
                Ok(data) => {
                    let _ = tx.blocking_send(ChunkDownloadResult {
                        index: chunk_idx,
                        data,
                        new_peers: Vec::new(),
                        source: "server".to_string(),
                    });
                }
                Err(e) => {
                    warn!(chunk = chunk_idx, error = %e, "chunk download failed from all sources");
                }
            }
        });
    }

    // Drop our sender so rx completes when all tasks finish.
    drop(tx);

    // Collect results and write chunks to cache.
    let mut received = existing.len() as u32;
    let mut peer_downloads = 0u32;
    let mut server_downloads = 0u32;

    while let Some(result) = rx.recv().await {
        cache.write_chunk(&meta.sha256, result.index, &result.data)?;
        received += 1;

        if result.source == "server" {
            server_downloads += 1;
        } else {
            peer_downloads += 1;
        }

        // Merge discovered peers into the peer map.
        for peer_addr in &result.new_peers {
            peer_map.report(
                &meta.sha256,
                peer_addr,
                HashSet::new(), // We don't know their chunks yet.
                PeerSource::PeerExchange,
            );
        }

        debug!(
            chunk = result.index,
            source = result.source.as_str(),
            progress = format!("{}/{}", received, meta.chunk_count).as_str(),
            "chunk received"
        );
    }

    if received < meta.chunk_count {
        anyhow::bail!(
            "incomplete download: got {}/{} chunks",
            received,
            meta.chunk_count
        );
    }

    info!(
        sha256 = &meta.sha256[..12],
        peer_chunks = peer_downloads,
        server_chunks = server_downloads,
        "all chunks received, assembling"
    );

    // Assemble and verify.
    cache.assemble(&meta.sha256, meta.size, meta.chunk_count)
}

/// Download a single chunk from the server using Range requests.
fn download_chunk_from_server(
    server_url: &str,
    sha256: &str,
    index: u32,
    tls: Option<&TlsCerts>,
) -> Result<Vec<u8>> {
    use super::meta::CHUNK_SIZE;

    let agent = if let Some(certs) = tls {
        super::download::build_download_agent(Some(certs))?
    } else {
        super::download::build_download_agent(None)?
    };

    let url = format!("{}/api/v1/swarm/blob/{}", server_url, sha256);
    let start = index as u64 * CHUNK_SIZE;
    // We don't know the exact blob size here, so request a full chunk.
    // The server will return fewer bytes for the last chunk.
    let end = start + CHUNK_SIZE - 1;
    let range = format!("bytes={}-{}", start, end);

    let resp = agent
        .get(&url)
        .header("Range", &range)
        .call()
        .map_err(|e| anyhow::anyhow!("server chunk {} download failed: {}", index, e))?;

    if resp.status() != 206 && resp.status() != 200 {
        anyhow::bail!("server returned {} for chunk {}", resp.status(), index);
    }

    let data = resp
        .into_body()
        .read_to_vec()
        .map_err(|e| anyhow::anyhow!("reading chunk {} body: {}", index, e))?;

    Ok(data)
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
