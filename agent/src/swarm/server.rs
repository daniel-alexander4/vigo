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
//! Bounded to 100 concurrent connections via tokio Semaphore.

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
use super::meta::chunks_for_size;

/// Default swarm peer port.
pub const DEFAULT_PORT: u16 = 1531;

/// Maximum concurrent connections.
const MAX_CONNECTIONS: usize = 100;

/// Shared state for the peer server.
struct ServerState {
    cache: BlobCache,
    semaphore: Arc<Semaphore>,
    bandwidth: BandwidthMonitor,
}

/// Run the swarm peer HTTPS server until cancellation.
pub async fn run(
    port: u16,
    blobs_dir: PathBuf,
    tls_acceptor: TlsAcceptor,
    bandwidth: BandwidthMonitor,
    cancel: tokio_util::sync::CancellationToken,
) -> Result<()> {
    let cache = BlobCache::new(blobs_dir).context("initializing swarm server blob cache")?;

    let state = Arc::new(ServerState {
        cache,
        semaphore: Arc::new(Semaphore::new(MAX_CONNECTIONS)),
        bandwidth,
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

            if let Err(e) = http1::Builder::new().serve_connection(io, svc).await {
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

    let resp = match (&method, path.as_str()) {
        (&Method::GET, "/health") => ok_json(r#"{"status":"ok"}"#),

        (&Method::GET, p) if p.starts_with("/blob/") => {
            handle_blob_request(p, state)
        }

        _ => not_found(),
    };

    Ok(resp)
}

/// Handle /blob/{sha256}/meta and /blob/{sha256}/chunk/{index}
fn handle_blob_request(path: &str, state: &ServerState) -> Response<Full<Bytes>> {
    // Parse: /blob/{sha256}/meta or /blob/{sha256}/chunk/{index}
    let segments: Vec<&str> = path.trim_start_matches('/').split('/').collect();

    match segments.as_slice() {
        // /blob/{sha256}/meta
        ["blob", sha256, "meta"] => handle_meta(sha256, state),

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

/// GET /blob/{sha256}/meta
///
/// Returns blob metadata including which chunks this agent has available.
fn handle_meta(sha256: &str, state: &ServerState) -> Response<Full<Bytes>> {
    if !validate_sha256(sha256) {
        return bad_request("invalid sha256");
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

    let body = serde_json::json!({
        "sha256": sha256,
        "size": size,
        "chunk_size": super::meta::CHUNK_SIZE,
        "chunk_count": chunk_count,
        "chunks": chunks_arr,
    });

    ok_json(&body.to_string())
}

/// GET /blob/{sha256}/chunk/{index}
///
/// Returns raw chunk bytes. Respects bandwidth throttle.
fn handle_chunk(sha256: &str, index: u32, state: &ServerState) -> Response<Full<Bytes>> {
    if !validate_sha256(sha256) {
        return bad_request("invalid sha256");
    }

    // Bandwidth throttle — if paused, reject with 503.
    if state.bandwidth.is_paused() {
        return service_unavailable("bandwidth throttled");
    }

    match state.cache.read_chunk(sha256, index) {
        Ok(data) => {
            Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", "application/octet-stream")
                .header("Content-Length", data.len().to_string())
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
