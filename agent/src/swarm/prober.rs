// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Active peer probing for swarm blob discovery.
//!
//! When the orchestrator has zero peers for a blob, the prober queries known
//! peer addresses (from previous downloads, multicast, or server hints) to
//! check if they have the needed blob. This eliminates the dependency on the
//! server for peer routing.

use std::collections::HashSet;
use std::sync::Arc;

use tracing::{debug, info};

use super::peer_client;
use super::peermap::{PeerMap, PeerSource};
use crate::update::TlsCerts;

/// Probe known peers for a specific blob.
///
/// Collects all known peer addresses from the PeerMap (for ANY blob, not just
/// this one) and sends `GET /blob/{sha256}/meta` to each. Peers that respond
/// are added to the PeerMap with fresh timestamps.
///
/// Returns the number of peers found for the target blob.
pub async fn probe_for_blob(
    peer_map: &Arc<PeerMap>,
    tls: Option<&TlsCerts>,
    sha256: &str,
    _total_chunks: u32,
) -> usize {
    let tls = match tls {
        Some(t) => t,
        None => return 0,
    };

    // Collect unique peer addresses from all blobs.
    let addresses: Vec<String> = peer_map.all_addresses();
    if addresses.is_empty() {
        return 0;
    }

    let sha = sha256.to_string();
    let sha_short = &sha[..12.min(sha.len())];
    debug!(sha256 = sha_short, candidates = addresses.len(), "probing peers for blob");

    let mut found = 0;
    for addr in &addresses {
        let tls_clone = tls.clone();
        let addr_clone = addr.clone();
        let sha_clone = sha.clone();

        // Probe on a blocking thread (ureq is synchronous).
        let result = tokio::task::spawn_blocking(move || {
            let agent = peer_client::build_peer_agent(&tls_clone)?;
            peer_client::fetch_meta(&agent, &addr_clone, &sha_clone)
        })
        .await;

        match result {
            Ok(Ok(meta)) => {
                let chunks: HashSet<u32> = meta.chunks.into_iter().collect();
                if !chunks.is_empty() {
                    peer_map.report(&sha, addr, chunks, PeerSource::PeerExchange);
                    found += 1;
                }
            }
            Ok(Err(_)) => {} // Peer doesn't have this blob — expected.
            Err(_) => {}     // Task panicked — ignore.
        }
    }

    if found > 0 {
        info!(sha256 = sha_short, peers_found = found, "active probing discovered peers");
    }

    found
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_probe_no_tls() {
        let pm = Arc::new(PeerMap::new());
        let found = probe_for_blob(&pm, None, &"a".repeat(64), 10).await;
        assert_eq!(found, 0);
    }

    #[tokio::test]
    async fn test_probe_no_peers() {
        let pm = Arc::new(PeerMap::new());
        let tls = TlsCerts {
            ca_cert: "/nonexistent".to_string(),
            client_cert: "/nonexistent".to_string(),
            client_key: "/nonexistent".to_string(),
        };
        let found = probe_for_blob(&pm, Some(&tls), &"a".repeat(64), 10).await;
        assert_eq!(found, 0);
    }
}
