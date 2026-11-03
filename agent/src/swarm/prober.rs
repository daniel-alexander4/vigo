// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Active peer probing for swarm blob discovery.
//!
//! Two responsibilities:
//!
//! 1. **Cold-start probing** (`probe_for_blob`) — when the orchestrator has
//!    zero peers for a blob, query known addresses from previous downloads,
//!    multicast, or server hints to find candidates.
//! 2. **Warm refresh loop** (`run_refresh_loop`) — a background task that
//!    periodically re-probes peers whose cached bitmap has gone stale so
//!    the scheduler can keep pace with chunks the peer has downloaded since
//!    the last announcement. Uses `If-None-Match` against the peer's ETag
//!    so unchanged inventories cost a single 304 with an empty body.

use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;

use tokio::time::MissedTickBehavior;
use tracing::{debug, info, warn};

use super::peer_client::{self, MetaResult};
use super::peermap::{PeerMap, PeerSource};
use crate::update::TlsCerts;

/// How often the refresh loop wakes up.
const REFRESH_INTERVAL: Duration = Duration::from_secs(5);

/// A peer entry counts as stale if its bitmap hasn't been refreshed within
/// this window. Matches `REFRESH_INTERVAL` so every peer gets at most one
/// probe per tick.
const STALE_THRESHOLD: Duration = Duration::from_secs(5);

/// Cap on how many (sha, peer) probes a single refresh tick may perform.
/// Guardrail against a very large peer set stampeding the local CPU or
/// the peer fleet. Leftover stale peers are picked up next tick.
const MAX_PROBES_PER_TICK: usize = 64;

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

/// Long-running refresh loop — re-probes stale peers so the scheduler's
/// view of who has which chunks stays current during active downloads.
/// Runs until the cancellation token fires.
pub async fn run_refresh_loop(
    peer_map: Arc<PeerMap>,
    tls: Option<TlsCerts>,
    cancel: tokio_util::sync::CancellationToken,
) {
    let Some(tls) = tls else {
        debug!("prober refresh loop: no TLS certs, not starting");
        return;
    };

    let mut ticker = tokio::time::interval(REFRESH_INTERVAL);
    ticker.set_missed_tick_behavior(MissedTickBehavior::Skip);
    // First tick fires immediately — skip it so we don't probe before the
    // rest of the agent has had a chance to populate PeerMap.
    ticker.tick().await;

    info!(interval_secs = REFRESH_INTERVAL.as_secs(), "prober refresh loop started");

    loop {
        tokio::select! {
            biased;
            _ = cancel.cancelled() => {
                info!("prober refresh loop shutting down");
                return;
            }
            _ = ticker.tick() => {
                refresh_stale_peers(&peer_map, &tls).await;
            }
        }
    }
}

/// One pass of the refresh loop: query every stale peer via /meta with
/// If-None-Match and update PeerMap from the responses. Public for
/// test injection.
pub async fn refresh_stale_peers(peer_map: &Arc<PeerMap>, tls: &TlsCerts) {
    let mut targets = peer_map.stale_probe_targets(STALE_THRESHOLD);
    if targets.is_empty() {
        return;
    }
    if targets.len() > MAX_PROBES_PER_TICK {
        targets.truncate(MAX_PROBES_PER_TICK);
    }

    // One ureq agent reused across all probes this tick — avoids repeatedly
    // rebuilding the TLS stack (which parses the CA + client cert from disk).
    let agent = match tokio::task::spawn_blocking({
        let tls = tls.clone();
        move || peer_client::build_peer_agent(&tls)
    })
    .await
    {
        Ok(Ok(a)) => a,
        Ok(Err(e)) => {
            warn!(error = %e, "prober refresh: failed to build peer agent");
            return;
        }
        Err(_) => return, // task panicked
    };

    let mut refreshed = 0usize;
    let mut unchanged = 0usize;
    let mut failed = 0usize;

    for (sha, addr, etag) in targets {
        let agent = agent.clone();
        let sha_clone = sha.clone();
        let addr_clone = addr.clone();
        let etag_clone = etag.clone();

        let result = tokio::task::spawn_blocking(move || {
            peer_client::fetch_meta_conditional(
                &agent,
                &addr_clone,
                &sha_clone,
                etag_clone.as_deref(),
            )
        })
        .await;

        match result {
            Ok(Ok(MetaResult::Fresh { meta, etag })) => {
                let chunks: HashSet<u32> = meta.chunks.into_iter().collect();
                peer_map.report_meta_probe(&sha, &addr, chunks, etag);
                refreshed += 1;
            }
            Ok(Ok(MetaResult::NotModified)) => {
                peer_map.touch_peer(&sha, &addr);
                unchanged += 1;
            }
            Ok(Err(_)) | Err(_) => {
                failed += 1;
            }
        }
    }

    if refreshed > 0 || unchanged > 0 || failed > 0 {
        debug!(
            refreshed,
            unchanged,
            failed,
            "prober refresh tick complete"
        );
    }
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
