// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm orchestrator: top-level entry point for downloading a blob.
//!
//! 1. Wait briefly for multicast discovery (if no peers known)
//! 2. Actively probe known peer addresses as a last resort
//! 3. Run the rarest-first scheduler
//! 4. Assemble and verify the blob
//!
//! All I/O runs at low priority to avoid starving the host.

use std::sync::Arc;

use anyhow::Result;
use tracing::{debug, info};

use super::cache::BlobCache;
use super::manifest::{BlobEntry, Manifest};
use super::meta::BlobMeta;
use super::peermap::PeerMap;
use super::scheduler;
use super::AnnounceSignal;
use crate::update::TlsCerts;

/// Multicast wait time — how long to wait for peer announcements before
/// starting downloads. Keep short — peers announce every 5s.
const MULTICAST_WAIT: std::time::Duration = std::time::Duration::from_secs(2);

/// Download a blob using the full swarm pipeline.
///
/// Waits briefly for multicast discovery if no peers are known, then falls
/// back to active probing of known peer addresses, then runs the rarest-first
/// chunk scheduler.
pub async fn download(
    entry: &BlobEntry,
    peer_map: &Arc<PeerMap>,
    cache: &BlobCache,
    tls: Option<&TlsCerts>,
    manifest: &Arc<Manifest>,
    announce: &Arc<AnnounceSignal>,
) -> Result<std::path::PathBuf> {
    let meta = BlobMeta::new(entry.sha256.clone(), entry.size, entry.label.clone());

    // Already cached?
    if cache.has_blob(&meta.sha256) {
        debug!(sha256 = &meta.sha256[..12], "blob already in cache");
        return Ok(cache.dir().join(&meta.sha256));
    }

    let mut peer_count = peer_map.peers_for(&meta.sha256).len();

    // Wait for multicast discovery when few peers are known. Even when
    // the seeder is pre-seeded (count=1), real peers need time to respond
    // to WANT lines. Without this, the scheduler starts seeder-only.
    if peer_count <= 1 {
        debug!(
            sha256 = &meta.sha256[..12],
            peer_count,
            "few peers known, waiting for multicast discovery"
        );
        tokio::time::sleep(MULTICAST_WAIT).await;
        peer_count = peer_map.peers_for(&meta.sha256).len();
    }

    // Active probing: query known peers for this blob. Even with only
    // the seeder in the PeerMap, other peers may already have chunks
    // from a previous download cycle. One /meta per known peer is cheap.
    if peer_count <= 1 {
        debug!(
            sha256 = &meta.sha256[..12],
            "probing known addresses for additional peers"
        );
        let found = super::prober::probe_for_blob(peer_map, tls, &meta.sha256, meta.chunk_count).await;
        if found > 0 {
            peer_count = peer_map.peers_for(&meta.sha256).len();
        }
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
    scheduler::download_from_swarm(&meta, cache, peer_map, tls, manifest, announce).await
}
