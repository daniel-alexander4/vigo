// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Swarm peer HTTPS server on port 1531.
//!
//! Serves blob chunks to other agents over mTLS. Requires client certificates
//! signed by the same CA used for gRPC enrollment. Three routes:
//!
//! - `GET /health` — 200 OK
//! - `GET /blob/{sha256}/meta` — blob metadata + available chunks
//! - `GET /blob/{sha256}/chunk/{index}` — raw chunk bytes
//!
//! Bounded to 100 concurrent connections via tokio Semaphore. Each connection
//! has a 60-second lifetime timeout — keep-alive enables connection reuse within
//! a download session, while the timeout prevents idle connections from exhausting
//! the semaphore.

use anyhow::{Context, Result};
use http_body_util::Full;
use hyper::body::Bytes;
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode};
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::net::TcpListener;
use tokio::sync::Semaphore;
use tokio_rustls::TlsAcceptor;
use tracing::{debug, info, warn};

use super::bandwidth::BandwidthMonitor;
use super::cache::BlobCache;
use super::manifest::Manifest;
use super::meta::chunks_for_size;
use super::peermap::PeerMap;

/// Default swarm peer port.
pub const DEFAULT_PORT: u16 = 1531;

/// Maximum concurrent connections.
const MAX_CONNECTIONS: usize = 100;

/// Shared state for the peer server.
struct ServerState {
    cache: BlobCache,
    semaphore: Arc<Semaphore>,
    bandwidth: BandwidthMonitor,
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
}

/// Run the swarm peer HTTPS server until cancellation.
pub async fn run(
    port: u16,
    blobs_dir: PathBuf,
    tls_acceptor: TlsAcceptor,
    bandwidth: BandwidthMonitor,
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    cancel: tokio_util::sync::CancellationToken,
) -> Result<()> {
    let cache = BlobCache::new(blobs_dir).context("initializing swarm server blob cache")?;

    let state = Arc::new(ServerState {
        cache,
        semaphore: Arc::new(Semaphore::new(MAX_CONNECTIONS)),
        bandwidth,
        peer_map,
        manifest,
    });

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    let listener = TcpListener::bind(addr)
        .await
        .with_context(|| format!("binding swarm peer server on port {}", port))?;

    info!(port, "swarm peer server listening");

    loop {
        let (tcp_stream, peer_addr) = tokio::select! {
            _ = cancel.cancelled() => {
                info!("swarm peer server shutting down");
                return Ok(());
            }
            result = listener.accept() => {
                match result {
                    Ok(v) => v,
                    Err(e) => {
                        warn!(error = %e, "accept failed");
                        continue;
                    }
                }
            }
        };

        // Rate limit connections.
        let permit = match Arc::clone(&state.semaphore).try_acquire_owned() {
            Ok(p) => p,
            Err(_) => {
                debug!(peer = %peer_addr, "rejecting connection — at capacity");
                drop(tcp_stream);
                continue;
            }
        };

        let acceptor = tls_acceptor.clone();
        let state = state.clone();
        let cancel = cancel.clone();

        tokio::spawn(async move {
            let _permit = permit; // held for connection lifetime

            // TLS handshake with 10s timeout.
            let tls_stream = tokio::select! {
                _ = cancel.cancelled() => return,
                result = tokio::time::timeout(
                    std::time::Duration::from_secs(10),
                    acceptor.accept(tcp_stream),
                ) => {
                    match result {
                        Ok(Ok(s)) => s,
                        Ok(Err(e)) => {
                            debug!(peer = %peer_addr, error = %e, "TLS handshake failed");
                            return;
                        }
                        Err(_) => {
                            debug!(peer = %peer_addr, "TLS handshake timed out");
                            return;
                        }
                    }
                }
            };

            let io = hyper_util::rt::TokioIo::new(tls_stream);
            let svc = service_fn(move |req| {
                let state = state.clone();
                async move { handle_request(req, &state).await }
            });

            // Keep-alive is enabled so clients reuse connections (8 connections
            // serve thousands of chunks). header_read_timeout closes idle
            // connections that sit without sending a new request, preventing
            // semaphore permits from being held indefinitely. Active transfers
            // are not interrupted — only the idle gap between requests is timed.
            let conn = http1::Builder::new()
                .timer(hyper_util::rt::TokioTimer::new())
                .header_read_timeout(std::time::Duration::from_secs(15))
                .serve_connection(io, svc);
            if let Err(e) = conn.await {
                if !e.is_incomplete_message() {
                    debug!(peer = %peer_addr, error = %e, "HTTP connection error");
                }
            }
        });
    }
}

/// Route requests to handlers.
async fn handle_request(
    req: Request<hyper::body::Incoming>,
    state: &ServerState,
) -> Result<Response<Full<Bytes>>, hyper::Error> {
    let path = req.uri().path().to_string();
    let method = req.method().clone();
    let if_none_match: Option<String> = req
        .headers()
        .get("if-none-match")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string());

    let resp = match (&method, path.as_str()) {
        (&Method::GET, "/health") => ok_json(r#"{"status":"ok"}"#),

        // Manifest sync endpoints for gossip protocol.
        (&Method::GET, "/manifest/version") => {
            ok_json(&format!(r#"{{"version":{}}}"#, state.manifest.version()))
        }
        (&Method::GET, "/manifest") => {
            match serde_json::to_string(&state.manifest.snapshot()) {
                Ok(json) => ok_json(&json),
                Err(_) => Response::builder()
                    .status(StatusCode::INTERNAL_SERVER_ERROR)
                    .body(Full::new(Bytes::from("serialization error")))
                    .unwrap(),
            }
        }

        (&Method::GET, p) if p.starts_with("/blob/") => {
            // Blob requests do blocking file I/O (open, seek, read 1 MiB chunks).
            // Use block_in_place so the tokio runtime can schedule other async work
            // (TLS writes, connection management) while the read completes.
            let inm = if_none_match.clone();
            tokio::task::block_in_place(|| handle_blob_request(p, inm.as_deref(), state))
        }

        // Vigosync: serve raw file bytes from a user's ~/vigosync/ directory.
        // /vigosync/{user}/{filename}
        (&Method::GET, p) if p.starts_with("/vigosync/") => {
            tokio::task::block_in_place(|| crate::vigosync::server::handle(p))
        }

        _ => not_found(),
    };

    Ok(resp)
}

/// Handle /blob/{sha256}/meta and /blob/{sha256}/chunk/{index}
fn handle_blob_request(
    path: &str,
    if_none_match: Option<&str>,
    state: &ServerState,
) -> Response<Full<Bytes>> {
    // Parse: /blob/{sha256}/meta or /blob/{sha256}/chunk/{index}
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();

    match segments.as_slice() {
        // /blob/{sha256}/meta
        ["blob", sha256, "meta"] => handle_meta(sha256, if_none_match, state),

        // /blob/{sha256}/chunk/{index}
        ["blob", sha256, "chunk", index_str] => {
            match index_str.parse::<u32>() {
                Ok(index) => handle_chunk(sha256, index, state),
                Err(_) => bad_request("invalid chunk index"),
            }
        }

        _ => not_found(),
    }
}

/// Build the ETag for a /meta response.
///
/// Format: `"<cache_version>-<chunk_count>"`. The cache version is a
/// monotonic counter bumped on every write/evict, so any change to the
/// local blob inventory invalidates all previously-issued ETags. The
/// chunk count is baked in so that clients with a different view of the
/// blob's total chunk count (e.g. after a re-seed at a different size)
/// won't accidentally match.
fn build_meta_etag(cache_version: u64, chunk_count: u32) -> String {
    format!("\"{}-{}\"", cache_version, chunk_count)
}

/// GET /blob/{sha256}/meta
///
/// Returns blob metadata including which chunks this agent has available.
/// Honors `If-None-Match` for conditional probes — returns 304 when the
/// cache version and chunk count match the client's stored ETag.
fn handle_meta(
    sha256: &str,
    if_none_match: Option<&str>,
    state: &ServerState,
) -> Response<Full<Bytes>> {
    if !validate_sha256(sha256) {
        return bad_request("invalid sha256");
    }
    // Don't serve metadata for revoked blobs.
    if state.manifest.is_revoked(sha256) {
        return not_found();
    }

    // Check if we have the blob (complete or partial).
    let blob_path = state.cache.dir().join(sha256);
    let complete = blob_path.is_file();

    let size = if complete {
        match std::fs::metadata(&blob_path) {
            Ok(m) => m.len(),
            Err(_) => return not_found(),
        }
    } else {
        // Partial — we don't know the total size. Return what we have.
        // The requesting agent should already know the size from the server.
        0
    };

    let chunk_count = chunks_for_size(size);
    let available_chunks = state.cache.chunk_bitmap(sha256, chunk_count);

    let mut chunks_arr: Vec<u32> = available_chunks.into_iter().collect();
    chunks_arr.sort_unstable();

    if chunks_arr.is_empty() && !complete {
        return not_found();
    }

    // Read cache version once so the ETag matches X-Peer-Server-Version.
    let cache_version = state.cache.version();
    let etag = build_meta_etag(cache_version, chunk_count);

    // If the client's cached ETag still matches, short-circuit with 304.
    // Empty bodies save a ~200-byte payload per probe; the header overhead
    // is the point — clients can keep the bitmap they already have.
    if let Some(client_etag) = if_none_match {
        if client_etag == etag {
            return Response::builder()
                .status(StatusCode::NOT_MODIFIED)
                .header("ETag", &etag)
                .header("X-Peer-Server-Version", cache_version.to_string())
                .body(Full::new(Bytes::new()))
                .unwrap();
        }
    }

    // Include known peers for peer exchange.
    let peers = state.peer_map.peer_addresses_for(sha256);

    let body = serde_json::json!({
        "sha256": sha256,
        "size": size,
        "chunk_size": super::meta::CHUNK_SIZE,
        "chunk_count": chunk_count,
        "chunks": chunks_arr,
        "peers": peers,
    });

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .header("ETag", &etag)
        .header("X-Peer-Server-Version", cache_version.to_string())
        .body(Full::new(Bytes::from(body.to_string())))
        .unwrap()
}

/// GET /blob/{sha256}/chunk/{index}
///
/// Returns raw chunk bytes. Respects bandwidth throttle.
fn handle_chunk(sha256: &str, index: u32, state: &ServerState) -> Response<Full<Bytes>> {
    if !validate_sha256(sha256) {
        return bad_request("invalid sha256");
    }
    // Don't serve chunks for revoked blobs.
    if state.manifest.is_revoked(sha256) {
        return not_found();
    }

    // Bandwidth throttle — if paused, reject with 503.
    if state.bandwidth.is_paused() {
        return service_unavailable("bandwidth throttled");
    }

    match state.cache.read_chunk(sha256, index) {
        Ok(data) => {
            // Include known peers for peer exchange.
            let peers = state.peer_map.peer_addresses_for(sha256);
            let peers_header = peers.join(",");

            Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", "application/octet-stream")
                .header("Content-Length", data.len().to_string())
                .header("X-Peers", peers_header)
                .body(Full::new(Bytes::from(data)))
                .unwrap()
        }
        Err(_) => not_found(),
    }
}

/// Validate a SHA256 hex string.
fn validate_sha256(s: &str) -> bool {
    s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit())
}

// ── Response helpers ──────────────────────────────────────────────────────

fn ok_json(body: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/json")
        .body(Full::new(Bytes::from(body.to_string())))
        .unwrap()
}

fn not_found() -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::NOT_FOUND)
        .body(Full::new(Bytes::from("not found")))
        .unwrap()
}

fn bad_request(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::BAD_REQUEST)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn service_unavailable(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::SERVICE_UNAVAILABLE)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

// ── TLS configuration ─────────────────────────────────────────────────────

/// Build a TlsAcceptor for the swarm peer server.
///
/// Requires mTLS: the server presents its own cert and requires clients to
/// present a cert signed by the same CA. Reuses the agent's bootstrap certs.
pub fn build_tls_acceptor(
    ca_cert_path: &str,
    server_cert_path: &str,
    server_key_path: &str,
) -> Result<TlsAcceptor> {
    use rustls::pki_types::{CertificateDer, PrivateKeyDer};
    use rustls::server::WebPkiClientVerifier;
    use std::io::BufReader;

    // Load CA cert for client verification.
    let ca_pem = std::fs::read(ca_cert_path).context("reading CA cert")?;
    let mut ca_reader = BufReader::new(ca_pem.as_slice());
    let mut root_store = rustls::RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut ca_reader) {
        let cert = cert.context("parsing CA cert")?;
        root_store.add(cert).context("adding CA to root store")?;
    }

    // Build client verifier — require client certs signed by our CA.
    let client_verifier = WebPkiClientVerifier::builder(Arc::new(root_store))
        .build()
        .context("building client verifier")?;

    // Load server certificate chain.
    let cert_pem = std::fs::read(server_cert_path).context("reading server cert")?;
    let mut cert_reader = BufReader::new(cert_pem.as_slice());
    let certs: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut cert_reader)
        .filter_map(|r| r.ok())
        .collect();
    if certs.is_empty() {
        anyhow::bail!("no certificates found in {}", server_cert_path);
    }

    // Load server private key.
    let key_pem = std::fs::read(server_key_path).context("reading server key")?;
    let mut key_reader = BufReader::new(key_pem.as_slice());
    let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut key_reader)
        .context("parsing server key")?
        .context("no private key found")?;

    // Build rustls ServerConfig.
    let config = rustls::ServerConfig::builder()
        .with_client_cert_verifier(client_verifier)
        .with_single_cert(certs, key)
        .context("building TLS server config")?;

    Ok(TlsAcceptor::from(Arc::new(config)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state_with_blob(dir: &std::path::Path, sha: &str, size: u64) -> Arc<ServerState> {
        let cache = BlobCache::new(dir.to_path_buf()).unwrap();
        std::fs::write(cache.dir().join(sha), vec![0u8; size as usize]).unwrap();
        let manifest = super::super::manifest::Manifest::load(dir);
        Arc::new(ServerState {
            cache,
            semaphore: Arc::new(Semaphore::new(1)),
            bandwidth: BandwidthMonitor::new(),
            peer_map: Arc::new(PeerMap::new()),
            manifest,
        })
    }

    #[test]
    fn test_build_meta_etag_format() {
        assert_eq!(build_meta_etag(1, 1), r#""1-1""#);
        assert_eq!(build_meta_etag(42, 1000), r#""42-1000""#);
    }

    #[test]
    fn test_handle_meta_sets_etag_and_version_headers() {
        let tmp = tempfile::tempdir().unwrap();
        let sha = "a".repeat(64);
        // 2 MiB → 2 chunks at CHUNK_SIZE=1 MiB.
        let state = state_with_blob(tmp.path(), &sha, 2 * 1024 * 1024);

        let resp = handle_meta(&sha, None, &state);
        assert_eq!(resp.status(), StatusCode::OK);

        let etag = resp
            .headers()
            .get("ETag")
            .and_then(|v| v.to_str().ok())
            .expect("ETag header present")
            .to_string();
        assert!(etag.starts_with('"') && etag.ends_with('"'));
        let version_header = resp
            .headers()
            .get("X-Peer-Server-Version")
            .and_then(|v| v.to_str().ok())
            .expect("X-Peer-Server-Version present")
            .to_string();
        // Cache was just built with no mutations → version should be 1.
        assert_eq!(version_header, "1");
        assert!(etag.contains("1-"), "etag should carry cache version: {}", etag);
    }

    #[test]
    fn test_handle_meta_if_none_match_returns_304() {
        let tmp = tempfile::tempdir().unwrap();
        let sha = "b".repeat(64);
        let state = state_with_blob(tmp.path(), &sha, 2 * 1024 * 1024);

        // Fetch once to learn the ETag.
        let first = handle_meta(&sha, None, &state);
        let etag = first
            .headers()
            .get("ETag")
            .and_then(|v| v.to_str().ok())
            .unwrap()
            .to_string();

        // Second call with If-None-Match must short-circuit.
        let second = handle_meta(&sha, Some(&etag), &state);
        assert_eq!(second.status(), StatusCode::NOT_MODIFIED);
        assert_eq!(
            second.headers().get("ETag").and_then(|v| v.to_str().ok()),
            Some(etag.as_str())
        );
        assert!(second.headers().get("X-Peer-Server-Version").is_some());
    }

    #[test]
    fn test_handle_meta_etag_invalidated_after_cache_write() {
        let tmp = tempfile::tempdir().unwrap();
        let sha_a = "c".repeat(64);
        let state = state_with_blob(tmp.path(), &sha_a, 2 * 1024 * 1024);

        let first = handle_meta(&sha_a, None, &state);
        let old_etag = first
            .headers()
            .get("ETag")
            .and_then(|v| v.to_str().ok())
            .unwrap()
            .to_string();

        // Bump the cache version by writing a chunk for some unrelated blob.
        let sha_b = "d".repeat(64);
        state.cache.write_chunk(&sha_b, 0, b"x").unwrap();

        // Same sha_a, same old_etag — must NOT 304 because cache_version moved.
        let again = handle_meta(&sha_a, Some(&old_etag), &state);
        assert_eq!(again.status(), StatusCode::OK);
        let new_etag = again
            .headers()
            .get("ETag")
            .and_then(|v| v.to_str().ok())
            .unwrap()
            .to_string();
        assert_ne!(old_etag, new_etag);
    }

    #[test]
    fn test_validate_sha256() {
        assert!(validate_sha256(&"a".repeat(64)));
        assert!(validate_sha256(&"0123456789abcdef".repeat(4)));
        assert!(!validate_sha256(&"a".repeat(63)));
        assert!(!validate_sha256(&"a".repeat(65)));
        assert!(!validate_sha256(&"G".repeat(64)));
        assert!(!validate_sha256(""));
    }

    #[test]
    fn test_route_parsing() {
        // Verify segment parsing works.
        let path = "/blob/abcd1234/meta";
        let segs: Vec<&str> = path.trim_start_matches('/').split('/').collect();
        assert_eq!(segs, vec!["blob", "abcd1234", "meta"]);

        let path = "/blob/abcd1234/chunk/42";
        let segs: Vec<&str> = path.trim_start_matches('/').split('/').collect();
        assert_eq!(segs, vec!["blob", "abcd1234", "chunk", "42"]);
    }
}
