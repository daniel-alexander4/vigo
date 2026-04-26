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
use std::time::{Duration, Instant};
use tokio::net::TcpListener;
use tokio::sync::Semaphore;
use tokio_rustls::TlsAcceptor;
use tracing::{debug, info, warn};

use super::bandwidth::BandwidthMonitor;
use super::cache::BlobCache;
use super::manifest::Manifest;
use super::meta::chunks_for_size;
use super::peermap::PeerMap;
use crate::cairn::receiver::ClaimsHandle as CairnClaimsHandle;

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
    /// Cairn server-side context for cross-subnet `/cairn/push`
    /// handling (Phase 7). `None` when cairn is disabled on this
    /// envoy — in which case the `/cairn/push` route returns
    /// 403 regardless of sender identity, which is the correct
    /// response for a dormant cairn subsystem.
    cairn_ctx: Option<CairnServerCtx>,
}

/// Handles that the cairn push handler needs to validate and
/// accept an incoming bundle. Built once at swarm server startup and
/// cloned into the per-request handler. The `gate` field is consulted
/// on every `/cairn/push` request so enable/disable flips take
/// effect without restarting the agent; when the gate is off the
/// handler returns 403 just as it used to when this context itself
/// was absent.
#[derive(Clone)]
pub struct CairnServerCtx {
    pub claims: CairnClaimsHandle,
    pub manifest: Arc<Manifest>,
    pub local_hostname: String,
    pub gate: crate::cairn::Gate,
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
    cairn_ctx: Option<CairnServerCtx>,
) -> Result<()> {
    let cache = BlobCache::new(blobs_dir).context("initializing swarm server blob cache")?;

    let state = Arc::new(ServerState {
        cache,
        semaphore: Arc::new(Semaphore::new(MAX_CONNECTIONS)),
        bandwidth,
        peer_map,
        manifest,
        cairn_ctx,
    });

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    let listener = TcpListener::bind(addr)
        .await
        .with_context(|| format!("binding swarm peer server on port {}", port))?;

    info!(port, "swarm peer server listening");

    // Rate-limit accept-error logging to at most one warn per second per
    // process. Without this, EMFILE (FD exhaustion) lets the kernel return
    // the error in microseconds and a `warn! + continue` loop fills the
    // journal at syscall rate — observed in the field as 8000 lines/sec
    // sustained, hundreds of GB of syslog. The sleep also gives the
    // pressure that caused the error a chance to clear.
    let mut last_accept_warn: Option<Instant> = None;
    let mut accept_error_suppressed: u64 = 0;

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
                        let should_log = last_accept_warn
                            .map(|t| t.elapsed() >= Duration::from_secs(1))
                            .unwrap_or(true);
                        if should_log {
                            warn!(
                                error = %e,
                                suppressed = accept_error_suppressed,
                                "accept failed"
                            );
                            last_accept_warn = Some(Instant::now());
                            accept_error_suppressed = 0;
                        } else {
                            accept_error_suppressed += 1;
                        }
                        // Back off so we don't tight-loop. EMFILE in particular
                        // returns from the syscall in microseconds; without a
                        // sleep this branch can fire >100k times per second.
                        tokio::time::sleep(Duration::from_millis(100)).await;
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
                let pa = peer_addr;
                async move { handle_request(req, pa, &state).await }
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
    peer_addr: SocketAddr,
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

        // Pre-flight disk-pressure probe — senders ask "is the
        // filesystem where my push will land >10% free?" before
        // committing bytes. Query: ?for=<swarm|sigil|cairn|echo>[&user=...].
        (&Method::GET, "/health/storage") => {
            handle_health_storage(req.uri().query().unwrap_or(""))
        }

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

        // Echo: serve raw file bytes from a user's ~/echo/ directory.
        // /echo/{user}/{filename}
        (&Method::GET, p) if p.starts_with("/echo/") => {
            tokio::task::block_in_place(|| crate::echo::server::handle(p))
        }

        // Echo cross-subnet announce. Source envoys POST their
        // Announcement JSON here (configured via `cross_subnet_peers:` in
        // `~<user>/.echo/config.yaml`). The handler resolves the
        // sender's peer address from the TCP connection and feeds the
        // announcement into the same reconciler path that multicast
        // uses. Returns 503 if echo isn't running on this envoy.
        (&Method::POST, "/echo/announce") => {
            handle_echo_announce(req, peer_addr).await
        }

        // Cairn cross-subnet push (Phase 7). Source envoys POST
        // a bundle directly to this endpoint when a `.cairn.yaml`
        // sidecar lists an explicit `cross_subnet_mirrors:` target. The
        // handler validates the sender, writes the blob to the local
        // swarm cache, injects a manifest entry, and ingests a LAN
        // claim so the cairn receiver picks it up on the next
        // tick — same path as a LAN-discovered mirror.
        (&Method::POST, p) if p.starts_with("/cairn/push/") => {
            handle_cairn_push(p, req, state).await
        }

        // Sigil: pull ciphertext from a user's ~/sigil/.
        // Symmetric with echo's pull but under /sigil/pull/.
        (&Method::GET, p) if p.starts_with("/sigil/pull/") => {
            tokio::task::block_in_place(|| crate::sigil::server::handle_pull(p))
        }

        // Sigil: receive a ciphertext blob from a peer and
        // write it into the target user's ~/sigil/. 1 GiB cap.
        (&Method::POST, p) if p.starts_with("/sigil/push/") => {
            crate::sigil::server::handle_push(p, req).await
        }

        // Sigil: cross-subnet unicast announcement from a peer
        // listed in some local user's `cross_subnet_peers:`. 128 KiB cap.
        // Feeds the sigil reconciler's inbound channel.
        (&Method::POST, "/sigil/announce") => {
            crate::sigil::server::handle_announce(req, peer_addr).await
        }

        // Sigil: receive a signed DELETE envelope from a peer.
        // Records a tombstone locally and removes the local file.
        (&Method::POST, p) if p.starts_with("/sigil/tombstones/") => {
            crate::sigil::server::handle_tombstone_delete(p, req, peer_addr).await
        }

        // Sigil: catch-up pull of the full tombstone list for a user.
        // Returns JSON of the TombstoneTable; peers use this after seeing
        // an announcement digest that differs from their own view.
        (&Method::GET, p) if p.starts_with("/sigil/tombstones/") => {
            tokio::task::block_in_place(|| crate::sigil::server::handle_tombstones_list(p))
        }

        // Sigil: receive a signed LEAVE envelope from a peer that is
        // unenrolling. Verifies the signer is the subject, then drops
        // the pubkey from the peer table, regenerates recipients.txt,
        // drops the reencrypt.pending sentinel, and records the LEAVE.
        (&Method::POST, p) if p.starts_with("/sigil/leaves/") => {
            crate::sigil::server::handle_leave(p, req, peer_addr).await
        }

        // Sigil: catch-up pull of the full leaves table for a user.
        // Returns JSON of the LeavesTable; peers use this after seeing
        // an announcement leaves_digest that differs from their own.
        // Mirrors the tombstones GET handler.
        (&Method::GET, p) if p.starts_with("/sigil/leaves/") => {
            tokio::task::block_in_place(|| crate::sigil::server::handle_leaves_list(p))
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

fn forbidden(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::FORBIDDEN)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn payload_too_large(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::PAYLOAD_TOO_LARGE)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

fn insufficient_storage(msg: &str) -> Response<Full<Bytes>> {
    Response::builder()
        .status(StatusCode::INSUFFICIENT_STORAGE)
        .body(Full::new(Bytes::from(msg.to_string())))
        .unwrap()
}

// ── /health/storage (disk-pressure probe) ──────────────────────────────

/// Answer a sender's pre-flight probe: "what percent of the filesystem
/// that receives a <purpose> payload is free right now?" Response is a
/// short JSON doc `{"free_pct": <f64>}`. Never returns 507 — 507 is
/// reserved for actual push handlers. A malformed query gets 400 and a
/// purpose whose target path can't be resolved (e.g. user doesn't exist
/// on this envoy) gets 503.
fn handle_health_storage(query: &str) -> Response<Full<Bytes>> {
    let params = parse_query(query);
    let kind = match params.get("for") {
        Some(k) => k.as_str(),
        None => return bad_request("missing 'for' query parameter"),
    };
    let user = params.get("user").map(String::as_str);
    let purpose = match super::peer_health::Purpose::parse(kind, user) {
        Some(p) => p,
        None => return bad_request("invalid 'for'/'user' combination"),
    };
    match super::peer_health::local_free_pct(&purpose) {
        Some(pct) => ok_json(&format!(r#"{{"free_pct":{:.4}}}"#, pct)),
        None => service_unavailable("cannot resolve target mountpoint for purpose"),
    }
}

/// Parse a URL-encoded query string into a `HashMap`. Silently drops
/// entries with invalid percent-encoding. Duplicates: last value wins.
fn parse_query(q: &str) -> std::collections::HashMap<String, String> {
    let mut out = std::collections::HashMap::new();
    for pair in q.split('&') {
        if pair.is_empty() {
            continue;
        }
        let (k, v) = match pair.split_once('=') {
            Some(t) => t,
            None => (pair, ""),
        };
        let (Some(k), Some(v)) = (url_decode(k), url_decode(v)) else {
            continue;
        };
        out.insert(k, v);
    }
    out
}

// ── Cairn cross-subnet push handler (Phase 7) ──────────────────────────

/// Handle `POST /cairn/push/<label>` — the cross-subnet direct
/// push endpoint.
///
/// Path format: `/cairn/push/<url-encoded-label>`. The label is
/// a full content-seed label `cairn:<seeder>:<user>:<project>`;
/// we accept it url-encoded (colons percent-escaped) or raw.
///
/// Required headers:
///
/// - `X-Vigo-Seeder` — canonical hostname of the source envoy
///   (self-reported; validated only for shape, not identity — the
///   fleet CA already attested the mTLS connection, so the sender
///   is known-good at the transport layer)
/// - `X-Vigo-Blob-Sha256` — 64-hex sha256 of the bundle body
///
/// Validation order (each rejection returns a specific 4xx):
///
/// 1. Cairn enabled on this envoy (`cairn_ctx.is_some()`)
/// 2. Label parses into (seeder, user, project) segments
/// 3. Label's `<seeder>` segment equals `X-Vigo-Seeder`
/// 4. Local user from `<user>` segment exists
/// 5. Body size ≤ [`crate::cairn::MAX_BUNDLE_SIZE`]
/// 6. Body sha256 matches `X-Vigo-Blob-Sha256`
///
/// On accept:
///
/// - Body is written atomically to the blob cache at the computed
///   sha path.
/// - A manifest entry is recorded locally so the cairn
///   receiver's next tick can resolve the label → blob.
/// - The `ClaimsHandle` receives an `ingest_lan_claim(label,
///   sender, true, now)` so the reactor picks it up on its next
///   tick.
///
/// Returns 200 OK with a short JSON body on success.
async fn handle_cairn_push(
    path: &str,
    req: Request<hyper::body::Incoming>,
    state: &ServerState,
) -> Response<Full<Bytes>> {
    // 1. Cairn must be enabled on this envoy. The context is
    // always present on new builds, but the gate may be off — both
    // conditions return the same 403 to the caller.
    let ctx = match state.cairn_ctx.as_ref() {
        Some(c) => c.clone(),
        None => return forbidden("cairn not enabled on this envoy"),
    };
    if !ctx.gate.enabled() {
        return forbidden("cairn not enabled on this envoy");
    }

    // 2. Extract the label from the URL path.
    let raw_label = path.trim_start_matches("/cairn/push/");
    let label = match url_decode(raw_label) {
        Some(l) => l,
        None => return bad_request("malformed label url-encoding"),
    };

    // 3. Parse label segments.
    let (seeder, user, project) = match parse_cairn_label(&label) {
        Some(t) => t,
        None => return bad_request("malformed cairn label"),
    };

    // 4. Read required headers.
    let seeder_header = match header_string(&req, "x-vigo-seeder") {
        Some(s) => s,
        None => return bad_request("missing X-Vigo-Seeder header"),
    };
    let sha_header = match header_string(&req, "x-vigo-blob-sha256") {
        Some(s) => s,
        None => return bad_request("missing X-Vigo-Blob-Sha256 header"),
    };

    // 5. Seeder in the label must match the X-Vigo-Seeder header.
    // Both are self-reported over mTLS; we require them to agree
    // so a compromised (but fleet-enrolled) envoy cannot push a
    // mirror labeled as a different seeder.
    if seeder_header.to_ascii_lowercase() != seeder.to_ascii_lowercase() {
        return forbidden(&format!(
            "seeder mismatch: header '{}' does not match label seeder '{}'",
            seeder_header, seeder,
        ));
    }

    // 6. Validate sha header shape.
    if !validate_sha256(&sha_header) {
        return bad_request("X-Vigo-Blob-Sha256 is not a 64-char hex sha256");
    }

    // 7. Local user must exist. Cairn won't materialize a
    // mirror for a user with no local account on this envoy.
    if crate::platform::users::lookup_user(&user).is_none() {
        return forbidden(&format!("user '{}' does not exist on this envoy", user));
    }

    let _ = project; // validated as non-empty segment above

    // 7a. Receiver-side disk-pressure guard. The sender probes before
    // push, but the probe is cached 60 s — a concurrent writer could
    // have pushed free-pct below threshold in that window. Cheap
    // statvfs on the cairn mirror mountpoint; reject 507 before
    // reading a byte of the bundle body.
    if super::peer_health::is_self_pressured(&super::peer_health::Purpose::Cairn) {
        return insufficient_storage("cairn mirror filesystem under disk pressure");
    }

    // 8-10. Stream the body straight to disk under .tmp while hashing
    // incrementally. Previously we buffered the entire body in memory
    // via `body.collect().await`, which for a ~10 GiB bundle spiked
    // RSS by ~10 GiB — a principal contributor to the agent's
    // cgroup memory.peak. Streaming caps transient RAM at the hyper
    // frame size (tens of KiB).
    //
    // The tmp path is keyed on the claimed header sha. If the recomputed
    // body sha matches, we rename into the real cache slot (same path).
    // If it doesn't, we reject and delete the tmp file.
    let claimed_sha_lc = sha_header.to_ascii_lowercase();
    let tmp_path = state.cache.blob_path(&claimed_sha_lc).with_extension("tmp");
    if let Some(parent) = tmp_path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            return service_unavailable(&format!("failed to create blob dir: {}", e));
        }
    }

    let (body_len, actual_sha) = {
        use http_body_util::BodyExt;
        use sha2::{Digest, Sha256};
        use std::io::Write;

        let mut body = req.into_body();
        let mut tmp = match std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(true)
            .open(&tmp_path)
        {
            Ok(f) => f,
            Err(e) => {
                return service_unavailable(&format!("open tmp: {}", e));
            }
        };
        let mut hasher = Sha256::new();
        let mut total: u64 = 0;
        loop {
            let frame = match body.frame().await {
                Some(Ok(f)) => f,
                Some(Err(e)) => {
                    let _ = std::fs::remove_file(&tmp_path);
                    return bad_request(&format!("body read error: {}", e));
                }
                None => break,
            };
            if let Ok(data) = frame.into_data() {
                let chunk: &[u8] = &data;
                total = total.saturating_add(chunk.len() as u64);
                if total > crate::cairn::MAX_BUNDLE_SIZE {
                    let _ = std::fs::remove_file(&tmp_path);
                    return payload_too_large(&format!(
                        "bundle size exceeds MAX_BUNDLE_SIZE ({} GiB)",
                        crate::cairn::MAX_BUNDLE_SIZE / (1024 * 1024 * 1024),
                    ));
                }
                hasher.update(chunk);
                if let Err(e) = tmp.write_all(chunk) {
                    let _ = std::fs::remove_file(&tmp_path);
                    return service_unavailable(&format!("write tmp: {}", e));
                }
            }
        }
        if let Err(e) = tmp.sync_all() {
            let _ = std::fs::remove_file(&tmp_path);
            return service_unavailable(&format!("sync tmp: {}", e));
        }
        drop(tmp);

        let digest = hasher.finalize();
        let mut sha = String::with_capacity(64);
        for b in digest.iter() {
            sha.push_str(&format!("{:02x}", b));
        }
        (total, sha)
    };

    // 9. Verify body sha256 matches the header. Trust-but-verify:
    // the sender already sent the sha, but we recompute to catch
    // transport corruption or a misbehaving sender.
    if actual_sha != claimed_sha_lc {
        let _ = std::fs::remove_file(&tmp_path);
        return bad_request(&format!(
            "sha mismatch: body hashes to {} but header claimed {}",
            actual_sha, sha_header,
        ));
    }

    // 10. Move the validated tmp file into its final cache slot.
    let final_path = state.cache.blob_path(&actual_sha);
    if let Err(e) = std::fs::rename(&tmp_path, &final_path) {
        let _ = std::fs::remove_file(&tmp_path);
        warn!(
            error = %e,
            path = %final_path.display(),
            "cairn push: failed to move blob into place"
        );
        return service_unavailable(&format!("failed to write blob: {}", e));
    }

    // 11. Inject a manifest entry so the cairn receiver reactor
    // can resolve the label → blob. The seeder_address field is
    // set to the literal "direct-push" to distinguish manual
    // injections from manifest entries that came through normal
    // swarm gossip; downstream consumers only care that an entry
    // exists.
    ctx.manifest.record_seed(
        &actual_sha,
        &label,
        "*",
        "30d",
        &seeder,
        body_len,
        "direct-push",
    );

    // 12. Ingest the LAN claim so the receiver picks it up on
    // its next tick. Same path as a CAIRNCLAIM multicast —
    // the source hostname is used for dedup keying.
    let now = crate::cairn::state::unix_seconds_now();
    ctx.claims.ingest_lan_claim(&label, &seeder, true, now);

    info!(
        label = %label,
        seeder = %seeder,
        size = body_len,
        sha = %&actual_sha[..12.min(actual_sha.len())],
        "cairn push: accepted bundle"
    );

    ok_json(r#"{"status":"accepted"}"#)
}

/// Handle `POST /echo/announce` — cross-subnet unicast announcements
/// from peers listed in a user's `~<user>/.echo/config.yaml` →
/// `cross_subnet_peers:`. The body is a JSON-serialized echo Announcement.
/// The sender's TCP peer address is used as the reconciler's peer (same
/// model as multicast, where the UDP source IP is used), so subsequent
/// `GET /echo/<user>/<file>` fetches go back to the real remote host
/// rather than a self-reported hostname.
///
/// mTLS already attests the sender at the transport layer; the
/// authorization question here is "is echo accepting work?" — if
/// the daemon hasn't spawned (no TLS certs on this envoy, for example),
/// return 503. Otherwise feed the announcement into the reconciler path.
async fn handle_echo_announce(
    req: Request<hyper::body::Incoming>,
    peer_addr: SocketAddr,
) -> Response<Full<Bytes>> {
    use http_body_util::BodyExt;

    // Cap the body size — announcements are tiny (a few hundred bytes
    // per user-state), but a misbehaving client shouldn't be able to
    // pin memory.
    const MAX_ANNOUNCE_BYTES: usize = 128 * 1024;

    let collected = match req.into_body().collect().await {
        Ok(c) => c,
        Err(_) => return bad_request("read body"),
    };
    let body_bytes = collected.to_bytes();
    if body_bytes.len() > MAX_ANNOUNCE_BYTES {
        return payload_too_large("echo announcement exceeds size limit");
    }

    let announcement: crate::echo::state::Announcement =
        match serde_json::from_slice(&body_bytes) {
            Ok(a) => a,
            Err(e) => return bad_request(&format!("malformed announcement: {}", e)),
        };

    if crate::echo::submit_announcement(peer_addr, announcement) {
        ok_json(r#"{"status":"accepted"}"#)
    } else {
        service_unavailable("echo daemon not running on this envoy")
    }
}

/// Parse a cairn content-seed label `cairn:<seeder>:<user>:<project>`
/// into its three segments. Returns `None` for malformed labels.
fn parse_cairn_label(label: &str) -> Option<(String, String, String)> {
    let rest = label.strip_prefix("cairn:")?;
    let mut parts = rest.splitn(3, ':');
    let seeder = parts.next()?;
    let user = parts.next()?;
    let project = parts.next()?;
    if seeder.is_empty() || user.is_empty() || project.is_empty() {
        return None;
    }
    Some((seeder.to_string(), user.to_string(), project.to_string()))
}

/// Minimal URL-decoding for labels. We only need to handle `%3A`
/// (colon) since label segments can legally contain only alphanum
/// + `-` + `_` + `.`. Anything else we leave as-is and let the
/// label parser reject.
fn url_decode(s: &str) -> Option<String> {
    let mut out = String::with_capacity(s.len());
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let hi = (bytes[i + 1] as char).to_digit(16)?;
            let lo = (bytes[i + 2] as char).to_digit(16)?;
            out.push((hi * 16 + lo) as u8 as char);
            i += 3;
        } else {
            out.push(bytes[i] as char);
            i += 1;
        }
    }
    Some(out)
}

/// Read a single request header by lowercased name and return the
/// trimmed String value.
fn header_string(req: &Request<hyper::body::Incoming>, name: &str) -> Option<String> {
    req.headers()
        .get(name)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.trim().to_string())
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
            cairn_ctx: None,
        })
    }

    #[test]
    fn test_build_meta_etag_format() {
        assert_eq!(build_meta_etag(1, 1), r#""1-1""#);
        assert_eq!(build_meta_etag(42, 1000), r#""42-1000""#);
    }

    // ── Phase 7 cairn push pure helpers ────────────────────

    #[test]
    fn parse_cairn_label_accepts_valid_shape() {
        let (s, u, p) = parse_cairn_label("cairn:danlap:dan:vigo-web").unwrap();
        assert_eq!(s, "danlap");
        assert_eq!(u, "dan");
        assert_eq!(p, "vigo-web");
    }

    #[test]
    fn parse_cairn_label_accepts_project_with_dashes_and_dots() {
        let (_, _, p) = parse_cairn_label("cairn:h:u:my-repo.v2").unwrap();
        assert_eq!(p, "my-repo.v2");
    }

    #[test]
    fn parse_cairn_label_rejects_missing_prefix() {
        assert!(parse_cairn_label("danlap:dan:vigo-web").is_none());
    }

    #[test]
    fn parse_cairn_label_rejects_too_few_segments() {
        assert!(parse_cairn_label("cairn:dan:vigo-web").is_none());
        assert!(parse_cairn_label("cairn:danlap").is_none());
    }

    #[test]
    fn parse_cairn_label_rejects_empty_segment() {
        assert!(parse_cairn_label("cairn::dan:vigo-web").is_none());
        assert!(parse_cairn_label("cairn:dan::vigo-web").is_none());
        assert!(parse_cairn_label("cairn:dan:user:").is_none());
    }

    #[test]
    fn url_decode_handles_percent_colons() {
        assert_eq!(
            url_decode("cairn%3Adanlap%3Adan%3Avigo-web").unwrap(),
            "cairn:danlap:dan:vigo-web",
        );
    }

    #[test]
    fn url_decode_passes_through_unencoded_chars() {
        assert_eq!(url_decode("plain-label").unwrap(), "plain-label");
    }

    #[test]
    fn url_decode_accepts_raw_colons_too() {
        // If someone passes the label without percent-encoding,
        // we still handle it (colons are legal in URL paths).
        assert_eq!(
            url_decode("cairn:danlap:dan:vigo-web").unwrap(),
            "cairn:danlap:dan:vigo-web",
        );
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
