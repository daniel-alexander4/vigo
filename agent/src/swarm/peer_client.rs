// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Client for downloading chunks from peer agents over mTLS HTTPS.
//!
//! Uses ureq (blocking, wrapped in spawn_blocking) with the same CA and
//! client certs used for the gRPC connection to the server.

use anyhow::{bail, Context, Result};
use serde::Deserialize;
use tracing::debug;

use crate::update::TlsCerts;

/// Metadata response from GET /blob/{sha256}/meta.
#[derive(Debug, Deserialize)]
pub struct PeerBlobMeta {
    pub sha256: String,
    pub size: u64,
    pub chunk_size: u64,
    pub chunk_count: u32,
    pub chunks: Vec<u32>,
    #[serde(default)]
    pub peers: Vec<String>,
}

/// Result of fetching a chunk from a peer.
pub struct ChunkResult {
    pub data: Vec<u8>,
    /// Peers reported by this peer via X-Peers header (peer exchange).
    pub peers: Vec<String>,
}

/// Result of a /meta request. Fresh responses carry the peer's current
/// bitmap plus the ETag advertised by the peer's server. NotModified is
/// returned when the peer saw a matching If-None-Match header and replied
/// with 304 — the caller already has the latest bitmap cached.
pub enum MetaResult {
    Fresh {
        meta: PeerBlobMeta,
        etag: Option<String>,
    },
    NotModified,
}

/// Fetch blob metadata from a peer (unconditional). Prefer
/// `fetch_meta_conditional` when caching is in play.
pub fn fetch_meta(
    agent: &ureq::Agent,
    peer_addr: &str,
    sha256: &str,
) -> Result<PeerBlobMeta> {
    match fetch_meta_conditional(agent, peer_addr, sha256, None)? {
        MetaResult::Fresh { meta, .. } => Ok(meta),
        // With no If-None-Match header sent, the peer should never
        // reply 304. Treat it as a protocol error.
        MetaResult::NotModified => bail!("peer {} returned 304 without If-None-Match", peer_addr),
    }
}

/// Fetch blob metadata from a peer, optionally conditional on an ETag.
///
/// When `if_none_match` is supplied and the peer returns 304 Not Modified,
/// this returns `MetaResult::NotModified` — the caller should leave the
/// stored bitmap untouched but may refresh liveness. Otherwise returns
/// `MetaResult::Fresh` carrying the decoded payload and the peer's ETag
/// header (if present), which the caller should store for the next probe.
pub fn fetch_meta_conditional(
    agent: &ureq::Agent,
    peer_addr: &str,
    sha256: &str,
    if_none_match: Option<&str>,
) -> Result<MetaResult> {
    let url = format!("https://{}/blob/{}/meta", peer_addr, sha256);
    let mut req = agent.get(&url);
    if let Some(etag) = if_none_match {
        req = req.header("If-None-Match", etag);
    }
    let resp = req
        .call()
        .map_err(|e| anyhow::anyhow!("meta request to {} failed: {}", peer_addr, e))?;

    let status = resp.status();
    if status == 304 {
        return Ok(MetaResult::NotModified);
    }
    if status != 200 {
        bail!("meta request to {} returned {}", peer_addr, status);
    }

    let etag = resp
        .headers()
        .get("ETag")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string());

    let meta: PeerBlobMeta = resp
        .into_body()
        .read_json()
        .context("parsing peer meta response")?;

    Ok(MetaResult::Fresh { meta, etag })
}

/// Fetch a single chunk from a peer.
pub fn fetch_chunk(
    agent: &ureq::Agent,
    peer_addr: &str,
    sha256: &str,
    index: u32,
) -> Result<ChunkResult> {
    let url = format!("https://{}/blob/{}/chunk/{}", peer_addr, sha256, index);

    debug!(peer = peer_addr, chunk = index, "fetching chunk from peer");

    let resp = agent
        .get(&url)
        .call()
        .map_err(|e| anyhow::anyhow!("chunk {} from {} failed: {}", index, peer_addr, e))?;

    if resp.status() != 200 {
        bail!("chunk {} from {} returned {}", index, peer_addr, resp.status());
    }

    // Parse X-Peers header for peer exchange.
    let peers: Vec<String> = resp
        .headers()
        .get("X-Peers")
        .and_then(|v| v.to_str().ok())
        .map(|v| {
            v.split(',')
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect()
        })
        .unwrap_or_default();

    let data = resp
        .into_body()
        .read_to_vec()
        .context("reading chunk body from peer")?;

    Ok(ChunkResult { data, peers })
}

/// Fetch the manifest version from a peer.
pub fn fetch_manifest_version(agent: &ureq::Agent, peer_addr: &str) -> Result<u64> {
    let url = format!("https://{}/manifest/version", peer_addr);
    let resp = agent
        .get(&url)
        .call()
        .map_err(|e| anyhow::anyhow!("manifest version from {} failed: {}", peer_addr, e))?;

    if resp.status() != 200 {
        bail!("manifest version from {} returned {}", peer_addr, resp.status());
    }

    #[derive(Deserialize)]
    struct VersionResp {
        version: u64,
    }
    let v: VersionResp = resp
        .into_body()
        .read_json()
        .context("parsing manifest version")?;
    Ok(v.version)
}

/// Fetch the full manifest from a peer.
pub fn fetch_manifest(
    agent: &ureq::Agent,
    peer_addr: &str,
) -> Result<super::manifest::ManifestData> {
    let url = format!("https://{}/manifest", peer_addr);
    let resp = agent
        .get(&url)
        .call()
        .map_err(|e| anyhow::anyhow!("manifest from {} failed: {}", peer_addr, e))?;

    if resp.status() != 200 {
        bail!("manifest from {} returned {}", peer_addr, resp.status());
    }

    let data: super::manifest::ManifestData = resp
        .into_body()
        .read_json()
        .context("parsing manifest data")?;
    Ok(data)
}

/// Build a ureq agent configured for mTLS peer connections.
/// 5s connect timeout, 30s transfer timeout.
pub fn build_peer_agent(tls: &TlsCerts) -> Result<ureq::Agent> {
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

    // Disable server cert hostname verification for peer connections.
    // Peers connect by IP but certs have hostname SANs — mismatch is expected.
    // Safety: blob integrity is guaranteed by SHA256 hash verification, and the
    // peer server still verifies our client cert against the CA.
    //
    // A8: this also skips the server cert *chain* check, not just the
    // hostname — the control-plane responses pulled here (e.g. manifest
    // JSON) are NOT all hash-pinned, so a LAN MITM could inject. Same
    // ureq-3.3 API limitation blocks the proper fix here as in
    // lockbox/pusher.rs::build_agent — see the A8 note there for the
    // custom-verifier design and why it can't be wired through ureq.
    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        .disable_verification(true)
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_connect(Some(std::time::Duration::from_secs(5)))
            .timeout_global(Some(std::time::Duration::from_secs(30)))
            .timeout_recv_body(Some(std::time::Duration::from_secs(30)))
            .build(),
    ))
}
