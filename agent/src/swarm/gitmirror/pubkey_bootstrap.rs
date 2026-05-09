// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Server-distributed pubkey bootstrap for the gitmirror peer-pubkey
//! TOFU cache.
//!
//! Background: the gitmirror multicast announce embeds the sender's
//! Ed25519 pubkey in every `GITMIRROR:` line; receivers cache it on
//! first sighting (TOFU) and reject subsequent announces that present
//! a different key. The bare-TOFU model leaves a first-contact race —
//! a LAN-resident attacker that beats the real envoy to its first
//! announcement gets the cache slot, locking the real envoy out until
//! an operator manually clears the cache.
//!
//! This module closes that window by fetching the authoritative
//! `(hostname → pubkey)` map from the server (which has every envoy's
//! pubkey from registration). On a 15-minute timer plus a one-shot
//! call at agent startup, it pre-populates the local cache with
//! server-distributed entries. TOFU stays as the fallback for
//! envoys not yet seen by the server.
//!
//! Design choices documented in the pending list and ratified
//! 2026-05-09:
//!
//! * **Endpoint:** `GET /api/v1/fleet/pubkeys` — unauthenticated by
//!   design. Pubkeys are public-by-construction (broadcast on
//!   multicast). mTLS at the transport layer prevents MITM tampering.
//! * **Server bootstrap overrides TOFU on conflict.** The cache's
//!   `bootstrap_set` warns when overwriting a different cached key,
//!   so a TOFU race that the server can now correct is operator-
//!   visible.
//! * **Failure mode:** transient fetch failures (server down, TLS
//!   blip) log WARN and the cache continues with whatever it had.
//!   The TOFU path remains as the fallback.

use anyhow::{anyhow, bail, Context, Result};
use serde::Deserialize;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;
use tokio::time::MissedTickBehavior;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::update::TlsCerts;

use super::peer_pubkey_cache::PeerPubkeyCache;

/// How often the loop refreshes the cache from the server.
const REFRESH_INTERVAL: Duration = Duration::from_secs(15 * 60);

/// Per-request timeout. The server endpoint is a small JSON map; even
/// at 1000-envoy fleet scale the response is a few hundred KB.
const FETCH_TIMEOUT: Duration = Duration::from_secs(15);

/// Spawn the periodic fetch+apply loop. Returns immediately; the loop
/// runs until `cancel` fires. Performs an initial fetch on entry so
/// the cache is hot before any GITMIRROR multicast is processed.
pub fn spawn(
    cache: Arc<PeerPubkeyCache>,
    server_addr: String,
    tls: TlsCerts,
    cancel: CancellationToken,
) {
    tokio::spawn(async move {
        // One-shot at startup. Failures are logged and tolerated —
        // the multicast listener can still TOFU-accept while we wait
        // for the next tick.
        if let Err(e) = fetch_and_apply(&cache, &server_addr, &tls).await {
            warn!(error = %e, "gitmirror pubkey bootstrap: initial fetch failed; falling back to TOFU until next tick");
        }
        let mut interval = tokio::time::interval(REFRESH_INTERVAL);
        interval.set_missed_tick_behavior(MissedTickBehavior::Skip);
        // First tick fires immediately — consume it so the loop body
        // doesn't double-fetch on entry.
        interval.tick().await;
        loop {
            tokio::select! {
                _ = cancel.cancelled() => {
                    info!("gitmirror pubkey bootstrap: cancelled, exiting");
                    return;
                }
                _ = interval.tick() => {
                    if let Err(e) = fetch_and_apply(&cache, &server_addr, &tls).await {
                        warn!(error = %e, "gitmirror pubkey bootstrap: periodic fetch failed");
                    }
                }
            }
        }
    });
}

/// Fetch the server's fleet pubkey map and apply each entry to the
/// cache. Idempotent — repeated calls with the same server data are
/// no-ops in steady state, and a server-side change (new envoy
/// enrolled, host rekeyed) flows in on the next call.
async fn fetch_and_apply(
    cache: &PeerPubkeyCache,
    server_addr: &str,
    tls: &TlsCerts,
) -> Result<()> {
    let url = format!("{}/api/v1/fleet/pubkeys", server_addr.trim_end_matches('/'));
    let agent = build_mtls_agent(tls)?;

    let body = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let resp = agent
            .get(&url)
            .call()
            .map_err(|e| anyhow!("fleet pubkeys request: {}", e))?;
        if resp.status() != 200 {
            bail!("fleet pubkeys endpoint returned status {}", resp.status());
        }
        resp.into_body()
            .read_to_vec()
            .context("read fleet pubkeys response body")
    })
    .await
    .context("spawn_blocking join")??;

    #[derive(Deserialize)]
    struct Resp {
        pubkeys: HashMap<String, String>,
    }
    let r: Resp = serde_json::from_slice(&body).context("decode fleet pubkeys JSON")?;

    let count = r.pubkeys.len();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    for (hostname, pubkey_hex) in r.pubkeys {
        // Defensive: skip empty values rather than poison the cache.
        if hostname.is_empty() || pubkey_hex.is_empty() {
            debug!(hostname = %hostname, "gitmirror pubkey bootstrap: skipping empty entry");
            continue;
        }
        cache.bootstrap_set(&hostname, &pubkey_hex, now);
    }
    info!(
        envoys = count,
        "gitmirror pubkey bootstrap: applied server-distributed pubkeys"
    );
    Ok(())
}

/// Build an mTLS-configured ureq agent for talking to the server.
/// Mirrors `update::build_mtls_agent` shape but with a shorter timeout
/// since we're hitting a small JSON endpoint, not downloading a binary.
fn build_mtls_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};

    let ca_pem = std::fs::read(&tls.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&tls.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&tls.client_key).context("read client key")?;

    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let root_certs = ureq::tls::RootCerts::new_with_certs(&ca_certs);

    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }

    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;

    let client_id = ureq::tls::ClientCert::new_with_certs(&client_certs, key);

    // Server-bound: hostname verification stays ON (the agent talks to
    // its configured server, whose cert has the right SAN).
    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_global(Some(FETCH_TIMEOUT))
            .build(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::gitmirror::peer_pubkey_cache::CheckOutcome;

    // The HTTP fetch path itself needs a live server (or a stubbed
    // ureq agent) and is exercised end-to-end on the home lab. Unit
    // tests here cover the decode + apply pipeline only.

    #[test]
    fn empty_response_is_a_no_op() {
        let cache = Arc::new(PeerPubkeyCache::empty());
        let body = br#"{"pubkeys": {}}"#;

        #[derive(Deserialize)]
        struct Resp {
            pubkeys: HashMap<String, String>,
        }
        let r: Resp = serde_json::from_slice(body).unwrap();
        for (h, k) in r.pubkeys {
            cache.bootstrap_set(&h, &k, 100);
        }
        // Cache stays empty — no entries added.
        assert_eq!(cache.check("plex", "deadbeef"), CheckOutcome::FirstSighting);
    }

    #[test]
    fn populates_cache_from_server_payload() {
        let cache = Arc::new(PeerPubkeyCache::empty());
        let body =
            br#"{"pubkeys": {"plex":"aaaa","girlslaptop":"bbbb","annlap":"cccc"}}"#;
        #[derive(Deserialize)]
        struct Resp {
            pubkeys: HashMap<String, String>,
        }
        let r: Resp = serde_json::from_slice(body).unwrap();
        for (h, k) in r.pubkeys {
            cache.bootstrap_set(&h, &k, 100);
        }
        assert_eq!(cache.check("plex", "aaaa"), CheckOutcome::Match);
        assert_eq!(cache.check("girlslaptop", "bbbb"), CheckOutcome::Match);
        assert_eq!(cache.check("annlap", "cccc"), CheckOutcome::Match);
    }
}
