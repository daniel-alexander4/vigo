// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm orchestrator: top-level entry point for downloading a blob.
//!
//! 1. Merge server-provided peers into PeerMap
//! 2. Wait briefly for multicast discovery
//! 3. Run the rarest-first scheduler (peers preferred, server fallback)
//! 4. Assemble and verify the blob
//!
//! All I/O runs at low priority to avoid starving the host.

use std::collections::HashSet;
use std::sync::Arc;

use anyhow::Result;
use tracing::{debug, info};

use super::cache::BlobCache;
use super::meta::BlobMeta;
use super::peermap::{PeerMap, PeerSource};
use super::scheduler;
use crate::proto::SwarmAssignment;
use crate::update::TlsCerts;

/// Multicast wait time — how long to wait for peer announcements before
/// starting downloads. Keep short — peers announce every 5s.
const MULTICAST_WAIT: std::time::Duration = std::time::Duration::from_secs(2);

/// Download a blob using the full swarm pipeline.
///
/// Merges any server-provided peers, waits briefly for multicast discovery,
/// then runs the rarest-first chunk scheduler. Falls back to server download
/// for chunks that no peer has.
pub async fn download(
    assignment: &SwarmAssignment,
    peer_map: &Arc<PeerMap>,
    cache: &BlobCache,
    tls: Option<&TlsCerts>,
) -> Result<std::path::PathBuf> {
    let meta = BlobMeta::new(
        assignment.sha256.clone(),
        assignment.size,
        assignment.label.clone(),
    );

    // Already cached?
    if cache.has_blob(&meta.sha256) {
        debug!(sha256 = &meta.sha256[..12], "blob already in cache");
        return Ok(cache.dir().join(&meta.sha256));
    }

    // Merge server-provided peers into PeerMap.
    for peer in &assignment.peers {
        info!(
            sha256 = &meta.sha256[..12],
            peer_address = %peer.address,
            chunks = peer.chunks.len(),
            "merging server-provided peer"
        );
        let chunks: HashSet<u32> = if peer.chunks.is_empty() {
            // Empty chunks list = peer has all chunks.
            (0..meta.chunk_count).collect()
        } else {
            peer.chunks.iter().copied().collect()
        };
        peer_map.report(&meta.sha256, &peer.address, chunks, PeerSource::Server);
    }

    let mut peer_count = peer_map.peers_for(&meta.sha256).len();

    // Wait for multicast discovery if we don't already have peers.
    if peer_count == 0 {
        debug!(
            sha256 = &meta.sha256[..12],
            "no known peers, waiting for multicast discovery"
        );
        tokio::time::sleep(MULTICAST_WAIT).await;
        peer_count = peer_map.peers_for(&meta.sha256).len();
    }

    // Active probing: query known peers for this blob.
    if peer_count == 0 {
        debug!(
            sha256 = &meta.sha256[..12],
            "no peers after multicast, probing known addresses"
        );
        let found = super::prober::probe_for_blob(peer_map, tls, &meta.sha256, meta.chunk_count).await;
        peer_count = found;
    }

    info!(
        sha256 = &meta.sha256[..12],
        label = %meta.label,
        size = meta.size,
        chunks = meta.chunk_count,
        peers = peer_count,
        "starting swarm download"
    );

    // Run the scheduler.
    scheduler::download_from_swarm(&meta, cache, peer_map, tls).await
}
