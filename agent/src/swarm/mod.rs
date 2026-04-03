// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm: peer-to-peer content distribution.
//!
//! Content-addressed blobs are downloaded from peers (preferred) or the server
//! (fallback) over mTLS HTTPS on port 1531. The cache stores complete blobs
//! and in-progress chunk directories for resumable downloads.
//!
//! Four-layer discovery: multicast → peer exchange → server peer list → server fallback.

pub mod bandwidth;
pub mod cache;
pub mod discovery;
pub mod download;
pub mod meta;
pub mod orchestrator;
pub mod peer_client;
pub mod peermap;
pub mod scheduler;
pub mod server;

use std::sync::Arc;

use crate::proto::SwarmAssignment;
use crate::update::TlsCerts;
use tracing::{info, warn};

use cache::BlobCache;
use meta::BlobMeta;
use peermap::PeerMap;

/// Process swarm assignments from a CheckInResponse.
///
/// When a PeerMap is available (daemon mode with peer server running),
/// uses the full swarm orchestrator: multicast discovery → peer exchange
/// → rarest-first parallel download → server fallback. Otherwise falls
/// back to direct server download.
pub async fn process_assignments(
    assignments: &[SwarmAssignment],
    server_url: &str,
    tls: Option<&TlsCerts>,
    peer_map: Option<&Arc<PeerMap>>,
) {
    let cache = match BlobCache::new(crate::platform::paths::default_blobs_dir()) {
        Ok(c) => c,
        Err(e) => {
            warn!(error = %e, "failed to initialize swarm blob cache");
            return;
        }
    };

    let count = assignments.len();
    info!(count, "processing swarm assignments");

    for assign in assignments {
        if assign.sha256.is_empty() || assign.size == 0 {
            warn!("skipping invalid swarm assignment (empty sha256 or zero size)");
            continue;
        }

        // Already have it.
        if cache.has_blob(&assign.sha256) {
            info!(sha256 = &assign.sha256[..12.min(assign.sha256.len())], "blob already cached");
            continue;
        }

        let result = if let Some(pm) = peer_map {
            // Full swarm pipeline: peers + server fallback.
            orchestrator::download(assign, pm, &cache, server_url, tls).await
        } else {
            // No peer map — direct server download only.
            let meta = BlobMeta::new(assign.sha256.clone(), assign.size, assign.label.clone());
            download::download_from_server(&meta, server_url, tls, &cache).await
        };

        match result {
            Ok(path) => {
                info!(
                    sha256 = &assign.sha256[..12.min(assign.sha256.len())],
                    label = %assign.label,
                    path = %path.display(),
                    "swarm blob downloaded successfully"
                );
            }
            Err(e) => {
                warn!(
                    sha256 = &assign.sha256[..12.min(assign.sha256.len())],
                    label = %assign.label,
                    error = %e,
                    "swarm blob download failed"
                );
            }
        }
    }
}
