// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm: peer-to-peer content distribution.
//!
//! Content-addressed blobs are downloaded from the server (fallback) or peers
//! (preferred) over mTLS HTTPS on port 1531. The cache stores complete blobs
//! and in-progress chunk directories for resumable downloads.

pub mod cache;
pub mod download;
pub mod meta;

use crate::proto::SwarmAssignment;
use crate::update::TlsCerts;
use tracing::{info, warn};

use cache::BlobCache;
use meta::BlobMeta;

/// Process swarm assignments from a CheckInResponse.
///
/// For each assignment, checks whether the blob is already cached and
/// downloads it from the server if not. Downloads run sequentially with
/// low I/O priority to avoid starving the host. Later phases add peer
/// downloads as the preferred path before falling back to the server.
pub async fn process_assignments(
    assignments: &[SwarmAssignment],
    server_url: &str,
    tls: Option<&TlsCerts>,
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

        let meta = BlobMeta::new(assign.sha256.clone(), assign.size, assign.label.clone());

        // Phase 3: server-only download. Phase 6 adds multi-peer parallel download.
        match download::download_from_server(&meta, server_url, tls, &cache).await {
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
