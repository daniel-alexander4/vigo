// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Client for downloading chunks from peer agents over mTLS HTTPS.
//!
//! Uses `swarm::tls::SwarmAgent` (a blocking reqwest client, wrapped in
//! spawn_blocking) with the same CA and client certs used for the gRPC
//! connection to the server. Server-cert verification chains to the fleet
//! CA but skips the hostname check — see `swarm::tls::FleetCaVerifier`.

use std::time::Duration;

use anyhow::{bail, Context, Result};
use reqwest::StatusCode;
use serde::Deserialize;
use tracing::debug;

use crate::swarm::tls::{self, SwarmAgent};
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
    agent: &SwarmAgent,
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
    agent: &SwarmAgent,
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
        .send()
        .map_err(|e| anyhow::anyhow!("meta request to {} failed: {}", peer_addr, e))?;

    let status = resp.status();
    if status == StatusCode::NOT_MODIFIED {
        return Ok(MetaResult::NotModified);
    }
    if status != StatusCode::OK {
        bail!("meta request to {} returned {}", peer_addr, status);
    }

    let etag = resp
        .headers()
        .get("ETag")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string());

    let body = resp.bytes().context("reading peer meta response")?;
    let meta: PeerBlobMeta =
        serde_json::from_slice(&body).context("parsing peer meta response")?;

    Ok(MetaResult::Fresh { meta, etag })
}

/// Fetch a single chunk from a peer.
pub fn fetch_chunk(
    agent: &SwarmAgent,
    peer_addr: &str,
    sha256: &str,
    index: u32,
) -> Result<ChunkResult> {
    let url = format!("https://{}/blob/{}/chunk/{}", peer_addr, sha256, index);

    debug!(peer = peer_addr, chunk = index, "fetching chunk from peer");

    let resp = agent
        .get(&url)
        .send()
        .map_err(|e| anyhow::anyhow!("chunk {} from {} failed: {}", index, peer_addr, e))?;

    if resp.status() != StatusCode::OK {
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
        .bytes()
        .context("reading chunk body from peer")?
        .to_vec();

    Ok(ChunkResult { data, peers })
}

/// Fetch the manifest version from a peer.
pub fn fetch_manifest_version(agent: &SwarmAgent, peer_addr: &str) -> Result<u64> {
    let url = format!("https://{}/manifest/version", peer_addr);
    let resp = agent
        .get(&url)
        .send()
        .map_err(|e| anyhow::anyhow!("manifest version from {} failed: {}", peer_addr, e))?;

    if resp.status() != StatusCode::OK {
        bail!("manifest version from {} returned {}", peer_addr, resp.status());
    }

    #[derive(Deserialize)]
    struct VersionResp {
        version: u64,
    }
    let body = resp.bytes().context("reading manifest version")?;
    let v: VersionResp =
        serde_json::from_slice(&body).context("parsing manifest version")?;
    Ok(v.version)
}

/// Fetch the full manifest from a peer.
pub fn fetch_manifest(
    agent: &SwarmAgent,
    peer_addr: &str,
) -> Result<super::manifest::ManifestData> {
    let url = format!("https://{}/manifest", peer_addr);
    let resp = agent
        .get(&url)
        .send()
        .map_err(|e| anyhow::anyhow!("manifest from {} failed: {}", peer_addr, e))?;

    if resp.status() != StatusCode::OK {
        bail!("manifest from {} returned {}", peer_addr, resp.status());
    }

    let body = resp.bytes().context("reading manifest data")?;
    let data: super::manifest::ManifestData =
        serde_json::from_slice(&body).context("parsing manifest data")?;
    Ok(data)
}

/// Build a mTLS client configured for peer-by-IP connections.
/// 5s connect timeout, 30s transfer timeout.
///
/// Server-cert verification chains the peer's cert to the fleet CA but
/// skips the hostname/SAN check — peers are dialed by IP while their certs
/// are CN'd by agent UUID. See `swarm::tls::FleetCaVerifier`. mTLS is
/// preserved: the client still presents this envoy's client cert.
pub fn build_peer_agent(tls: &TlsCerts) -> Result<SwarmAgent> {
    tls::build_client(tls, Duration::from_secs(5), Duration::from_secs(30))
}
