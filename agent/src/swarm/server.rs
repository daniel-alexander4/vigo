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
use crate::swarm::gitback::receiver::ClaimsHandle as GitbackClaimsHandle;

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
    /// Gitback server-side context for cross-subnet `/gitback/push`
    /// handling (Phase 7). `None` when gitback is disabled on this
    /// envoy — in which case the `/gitback/push` route returns
    /// 403 regardless of sender identity, which is the correct
    /// response for a dormant gitback subsystem.
    gitback_ctx: Option<GitbackServerCtx>,
}

/// Handles that the gitback push handler needs to validate and
/// accept an incoming bundle. Built once at swarm server startup and
/// cloned into the per-request handler. The `gate` field is consulted
/// on every `/gitback/push` request so enable/disable flips take
/// effect without restarting the agent; when the gate is off the
/// handler returns 403 just as it used to when this context itself
/// was absent.
#[derive(Clone)]
pub struct GitbackServerCtx {
    pub claims: GitbackClaimsHandle,
    pub manifest: Arc<Manifest>,
    pub local_hostname: String,
    pub gate: crate::swarm::gitback::Gate,
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
    gitback_ctx: Option<GitbackServerCtx>,
) -> Result<()> {
    let cache = BlobCache::new(blobs_dir).context("initializing swarm server blob cache")?;

    let state = Arc::new(ServerState {
        cache,
        semaphore: Arc::new(Semaphore::new(MAX_CONNECTIONS)),
        bandwidth,
        peer_map,
        manifest,
        gitback_ctx,
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
        // committing bytes. Query: ?for=<swarm|lockbox|gitback|longdrawer>[&user=...].
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

        // Longdrawer: serve raw file bytes from a user's ~/longdrawer/ directory.
        // /longdrawer/{user}/{filename}
        (&Method::GET, p) if p.starts_with("/swarm/longdrawer/") => {
            tokio::task::block_in_place(|| crate::swarm::longdrawer::server::handle(p))
        }

        // Longdrawer cross-subnet announce. Source envoys POST their
        // Announcement JSON here (configured via `cross_subnet_peers:` in
        // `~<user>/.longdrawer/config.yaml`). The handler resolves the
        // sender's peer address from the TCP connection and feeds the
        // announcement into the same reconciler path that multicast
        // uses. Returns 503 if longdrawer isn't running on this envoy.
        (&Method::POST, "/swarm/longdrawer/announce") => {
            handle_echo_announce(req, peer_addr).await
        }

        // Gitback cross-subnet push (Phase 7). Source envoys POST
        // a bundle directly to this endpoint when a `.gitback.yaml`
        // sidecar lists an explicit `cross_subnet_mirrors:` target. The
        // handler validates the sender, writes the blob to the local
        // swarm cache, injects a manifest entry, and ingests a LAN
        // claim so the gitback receiver picks it up on the next
        // tick — same path as a LAN-discovered mirror.
        (&Method::POST, p) if p.starts_with("/swarm/gitback/push/") => {
            // Two cohabiting flavors:
            //  - Unified-substrate (ADR-015 step 4): tail is 64-char
            //    lowercase hex `<project_id>`. Handled inline below.
            //  - Legacy claim-based (pre-ADR-015): tail is the
            //    url-encoded label `gitback:<host>:<user>:<project>`.
            //    Handled by the original receiver path while the
            //    legacy code is still in tree.
            let tail = p.trim_start_matches("/swarm/gitback/push/");
            if is_project_id_path_segment(tail) {
                handle_gitback_push_v2(tail, req).await
            } else {
                handle_gitback_push(p, req, state).await
            }
        }

        // Gitback DR catch-up — closes the swarm-wide-DR bootstrap
        // gap. A member that holds local project state but no bare
        // repo pulls a full `git bundle --all` of the project from any
        // peer that announces holding it; the X-Vigo-Chain header
        // proves membership exactly as it does on a push. 404 when
        // this envoy doesn't hold (a populated copy of) the project.
        (&Method::GET, p) if p.starts_with(crate::swarm::gitback::fan_out::BUNDLE_GET_PREFIX) => {
            let tail = p
                .trim_start_matches(crate::swarm::gitback::fan_out::BUNDLE_GET_PREFIX)
                .to_string();
            let chain = header_string(&req, crate::swarm::gitback::fan_out::CHAIN_HEADER);
            tokio::task::block_in_place(|| handle_gitback_bundle_get(&tail, chain.as_deref()))
        }

        // Envelope gossip endpoints (ADR-015 step 4 fourth slice).
        // Receivers persist signed envelopes idempotently (save_*
        // returns false on no-op) so re-arrival is cheap.
        (&Method::POST, p) if p.starts_with("/swarm/gitback/envelopes/") && p.ends_with("/revokes") => {
            handle_envelope_post(p, req, EnvelopeKind::Revoke).await
        }
        (&Method::POST, p) if p.starts_with("/swarm/gitback/envelopes/") && p.ends_with("/leaves") => {
            handle_envelope_post(p, req, EnvelopeKind::Leave).await
        }
        (&Method::GET, p) if p.starts_with("/swarm/gitback/envelopes/") && p.ends_with("/digest") => {
            tokio::task::block_in_place(|| handle_envelope_digest(p))
        }
        (&Method::GET, p) if p.starts_with("/swarm/gitback/envelopes/") && p.ends_with("/all") => {
            tokio::task::block_in_place(|| handle_envelope_all(p))
        }

        // Lockbox: pull ciphertext from a user's ~/lockbox/.
        // Symmetric with longdrawer's pull but under /lockbox/pull/.
        (&Method::GET, p) if p.starts_with("/swarm/lockbox/pull/") => {
            tokio::task::block_in_place(|| crate::swarm::lockbox::server::handle_pull(p))
        }

        // Lockbox: receive a ciphertext blob from a peer and
        // write it into the target user's ~/lockbox/. 1 GiB cap.
        (&Method::POST, p) if p.starts_with("/swarm/lockbox/push/") => {
            crate::swarm::lockbox::server::handle_push(p, req).await
        }

        // Lockbox: cross-subnet unicast announcement from a peer
        // listed in some local user's `cross_subnet_peers:`. 128 KiB cap.
        // Feeds the lockbox reconciler's inbound channel.
        (&Method::POST, "/swarm/lockbox/announce") => {
            crate::swarm::lockbox::server::handle_announce(req, peer_addr).await
        }

        // Lockbox: receive a signed DELETE envelope from a peer.
        // Records a tombstone locally and removes the local file.
        (&Method::POST, p) if p.starts_with("/swarm/lockbox/tombstones/") => {
            crate::swarm::lockbox::server::handle_tombstone_delete(p, req, peer_addr).await
        }

        // Lockbox: catch-up pull of the full tombstone list for a user.
        // Returns JSON of the TombstoneTable; peers use this after seeing
        // an announcement digest that differs from their own view.
        (&Method::GET, p) if p.starts_with("/swarm/lockbox/tombstones/") => {
            tokio::task::block_in_place(|| crate::swarm::lockbox::server::handle_tombstones_list(p))
        }

        // Lockbox: receive a signed LEAVE envelope from a peer that is
        // unenrolling. Verifies the signer is the subject, then drops
        // the pubkey from the peer table, regenerates recipients.txt,
        // drops the reencrypt.pending sentinel, and records the LEAVE.
        (&Method::POST, p) if p.starts_with("/swarm/lockbox/leaves/") => {
            crate::swarm::lockbox::server::handle_leave(p, req, peer_addr).await
        }

        // Lockbox: catch-up pull of the full leaves table for a user.
        // Returns JSON of the LeavesTable; peers use this after seeing
        // an announcement leaves_digest that differs from their own.
        // Mirrors the tombstones GET handler.
        (&Method::GET, p) if p.starts_with("/swarm/lockbox/leaves/") => {
            tokio::task::block_in_place(|| crate::swarm::lockbox::server::handle_leaves_list(p))
        }

        // Lockbox: receive a signed RESURRECT envelope from a peer.
        // Mirror of the tombstone DELETE handler — verifies sender +
        // signature, then records the resurrect locally so subsequent
        // pushes of the same filename pass the effective_tombstone gate.
        (&Method::POST, p) if p.starts_with("/swarm/lockbox/resurrects/") => {
            crate::swarm::lockbox::server::handle_resurrect(p, req, peer_addr).await
        }

        // Lockbox: catch-up pull of the full resurrects table for a
        // user. Mirror of handle_tombstones_list — pulled when an
        // announcement's resurrects_digest disagrees with the local
        // digest, used by `merge` to converge state across peers that
        // were offline during a resurrect.
        (&Method::GET, p) if p.starts_with("/swarm/lockbox/resurrects/") => {
            tokio::task::block_in_place(|| crate::swarm::lockbox::server::handle_resurrects_list(p))
        }

        // Lockbox: catch-up pull of the user's ciphertext file list
        // (name + sha256 + size per entry). Peers that observe a
        // `files_digest` mismatch on an announcement fetch this list,
        // diff against their local set, and pull each missing blob
        // via `/swarm/lockbox/pull/...`. Safety net for the empty-peers
        // race in pusher.rs.
        (&Method::GET, p) if p.starts_with("/swarm/lockbox/files/") => {
            tokio::task::block_in_place(|| crate::swarm::lockbox::server::handle_files_list(p))
        }

        // Puddle: receive a pair-claim from a peer envoy that ran
        // `vigo swarm puddle join --from <this-host>`. Validates
        // the incoming code against the ~/.vigo-puddle/pair.pending
        // sentinel for the target user and returns the wrapped
        // identity blob on match. ADR-014 §Distribution.
        (&Method::POST, p) if p.starts_with(crate::swarm::puddle::pair::PAIR_CLAIM_PREFIX) => {
            match crate::swarm::puddle::pair::handle_pair_claim(p, req).await {
                Ok(r) => r,
                Err(e) => {
                    tracing::warn!(error = %e, "puddle pair-claim handler error");
                    not_found()
                }
            }
        }

        // Puddle rekey-claim: surviving envoys POST a 5-min code
        // here during the founder's 24h rekey window to receive the
        // post-rotation wrapped puddle blob. ADR-018.
        (&Method::POST, p) if p.starts_with(crate::swarm::puddle::rekey::REKEY_CLAIM_PREFIX) => {
            match crate::swarm::puddle::rekey::handle_rekey_claim(p, req).await {
                Ok(r) => r,
                Err(e) => {
                    tracing::warn!(error = %e, "puddle rekey-claim handler error");
                    not_found()
                }
            }
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

/// True when `seg` looks like a unified-substrate gitback project_id:
/// 64 lowercase hex chars and nothing else. Used to disambiguate the
/// new POST `/swarm/gitback/push/<project_id_hex>` route from the
/// legacy label-based POST `/swarm/gitback/push/<gitback:...>`
/// handler that still ships while the legacy receiver is in tree.
fn is_project_id_path_segment(seg: &str) -> bool {
    seg.len() == 64
        && seg
            .chars()
            .all(|c| c.is_ascii_digit() || ('a'..='f').contains(&c))
}

/// Handle `POST /swarm/gitback/push/<project_id_hex>` — the
/// unified-substrate (ADR-015 step 4) fan-out endpoint.
///
/// Required headers:
/// - `X-Vigo-Chain` — base64 of the JSON-encoded
///   [`crate::swarm::gitback::project_state::TokenChain`] proving
///   the pushing puddle is an authorized member.
///
/// Body: a `git bundle create --all` blob, capped at the per-host
/// `swarm.gitback.max_bundle_bytes` (default 2 GiB; resolved via
/// [`crate::swarm::runtime_limits::gitback_max_bundle_bytes`]).
///
/// Validation:
/// 1. project_id_hex parses as a 32-byte project_id.
/// 2. X-Vigo-Chain decodes + verifies via
///    [`crate::swarm::gitback::fan_out::verify_chain_for_push`]
///    (chain anchors to `sha256(founder_pubkey) == project_id`,
///    no link is in the local revocation set).
/// 3. Body size ≤ the configured per-bundle cap; streamed to a temp
///    file to bound transient memory.
/// 4. Bundle ingests cleanly via
///    [`crate::swarm::gitback::fan_out::ingest_bundle_file`] (which
///    runs `git bundle verify` before `git fetch`).
///
/// On success returns `200 OK` with JSON body
/// `{"accepted": true, "refs_changed": <bool>}`. The `refs_changed`
/// flag is the cycle-prevention signal — receivers chain-fan-out
/// only when refs actually advanced (chain-fan-out itself is not
/// implemented in this slice; see `fan_out.rs` module docs).
async fn handle_gitback_push_v2(
    pid_hex: &str,
    req: Request<hyper::body::Incoming>,
) -> Response<Full<Bytes>> {
    use http_body_util::BodyExt;

    let project_id = match crate::swarm::gitback::project_state::parse_hex_id(pid_hex) {
        Ok(p) => p,
        Err(e) => return bad_request(&format!("invalid project_id: {}", e)),
    };

    let chain_header = match header_string(&req, crate::swarm::gitback::fan_out::CHAIN_HEADER) {
        Some(s) => s,
        None => {
            return bad_request(&format!(
                "missing {} header",
                crate::swarm::gitback::fan_out::CHAIN_HEADER
            ));
        }
    };

    let chain = match crate::swarm::gitback::fan_out::decode_chain_header(&chain_header) {
        Ok(c) => c,
        Err(e) => return bad_request(&format!("decode chain header: {:#}", e)),
    };

    let gitback_base = crate::platform::paths::default_gitback_dir();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    if let Err(e) = crate::swarm::gitback::fan_out::verify_chain_for_push(
        &chain,
        &project_id,
        &gitback_base,
        now,
    ) {
        return forbidden(&format!("chain rejected: {:#}", e));
    }

    // Stream the body into a temp file under incoming/ so we never
    // hold a multi-GiB bundle in process memory. Body size capped at
    // the per-host `swarm.gitback.max_bundle_bytes`.
    let max_bundle_bytes = crate::swarm::runtime_limits::gitback_max_bundle_bytes();
    let staging = gitback_base.join("incoming");
    if let Err(e) = std::fs::create_dir_all(&staging) {
        return service_unavailable(&format!("create incoming dir: {}", e));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&staging, std::fs::Permissions::from_mode(0o700));
    }
    let nonce: u128 = rand::random();
    let bundle_path = staging.join(format!("{}-{:032x}.bundle", pid_hex, nonce));

    let total = {
        use std::io::Write;
        let mut body = req.into_body();
        let mut tmp = match std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(true)
            .open(&bundle_path)
        {
            Ok(f) => f,
            Err(e) => return service_unavailable(&format!("open temp bundle: {}", e)),
        };
        let mut total: u64 = 0;
        loop {
            let frame = match body.frame().await {
                Some(Ok(f)) => f,
                Some(Err(e)) => {
                    let _ = std::fs::remove_file(&bundle_path);
                    return bad_request(&format!("body read error: {}", e));
                }
                None => break,
            };
            if let Ok(data) = frame.into_data() {
                let chunk: &[u8] = &data;
                total = total.saturating_add(chunk.len() as u64);
                if total > max_bundle_bytes {
                    let _ = std::fs::remove_file(&bundle_path);
                    return payload_too_large(&format!(
                        "bundle exceeds the {}-byte cap (swarm.gitback.max_bundle_bytes)",
                        max_bundle_bytes,
                    ));
                }
                if let Err(e) = tmp.write_all(chunk) {
                    let _ = std::fs::remove_file(&bundle_path);
                    return service_unavailable(&format!("write temp bundle: {}", e));
                }
            }
        }
        if let Err(e) = tmp.sync_all() {
            let _ = std::fs::remove_file(&bundle_path);
            return service_unavailable(&format!("sync temp bundle: {}", e));
        }
        drop(tmp);
        total
    };

    // The body is already streamed to `bundle_path`; ingest it in
    // place — no read-back into RAM.
    let refs_changed_result = tokio::task::block_in_place(|| {
        crate::swarm::gitback::fan_out::ingest_bundle_file(
            &gitback_base,
            &project_id,
            &bundle_path,
        )
    });
    let _ = std::fs::remove_file(&bundle_path);

    let refs_changed = match refs_changed_result {
        Ok(b) => b,
        Err(e) => {
            warn!(
                project_id = %pid_hex,
                error = %e,
                "gitback push: ingest failed"
            );
            return bad_request(&format!("ingest failed: {:#}", e));
        }
    };

    info!(
        project_id = %pid_hex,
        bytes = total,
        refs_changed,
        "gitback push: accepted"
    );

    let body = format!(
        "{{\"accepted\":true,\"refs_changed\":{}}}",
        refs_changed
    );
    ok_json(&body)
}

/// Handle `GET /swarm/gitback/bundle/<project_id_hex>` — the
/// receiver-initiated DR catch-up endpoint.
///
/// Required header: `X-Vigo-Chain` — base64 of the JSON-encoded
/// [`crate::swarm::gitback::fan_out::PushChain`] proving the
/// requesting puddle is an authorized member (same envelope and same
/// verification as the push endpoint).
///
/// Validation, in order:
/// 1. `<project_id_hex>` is a 64-char lowercase-hex project_id.
/// 2. `X-Vigo-Chain` decodes + verifies via
///    [`crate::swarm::gitback::fan_out::verify_chain_for_push`]
///    (anchors to `project_id`, no link revoked).
/// 3. This envoy holds a non-empty bare repo for the project — else
///    `404` ("we don't have it; ask another holder").
///
/// On success returns `200 OK` with `Content-Type:
/// application/x-git-bundle` and a `git bundle create - --all` body.
fn handle_gitback_bundle_get(
    pid_hex: &str,
    chain_header: Option<&str>,
) -> Response<Full<Bytes>> {
    if !is_project_id_path_segment(pid_hex) {
        return bad_request("invalid project_id path segment");
    }
    let project_id = match crate::swarm::gitback::project_state::parse_hex_id(pid_hex) {
        Ok(p) => p,
        Err(e) => return bad_request(&format!("invalid project_id: {}", e)),
    };
    let chain_header = match chain_header {
        Some(s) => s,
        None => {
            return bad_request(&format!(
                "missing {} header",
                crate::swarm::gitback::fan_out::CHAIN_HEADER
            ));
        }
    };
    let push = match crate::swarm::gitback::fan_out::decode_chain_header(chain_header) {
        Ok(c) => c,
        Err(e) => return bad_request(&format!("decode chain header: {:#}", e)),
    };

    let gitback_base = crate::platform::paths::default_gitback_dir();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    if let Err(e) = crate::swarm::gitback::fan_out::verify_chain_for_push(
        &push,
        &project_id,
        &gitback_base,
        now,
    ) {
        return forbidden(&format!("chain rejected: {:#}", e));
    }

    // We can only serve a project we hold a populated bare repo for.
    let repo =
        crate::swarm::gitback::project_state::project_repo_path(&gitback_base, &project_id);
    let has_refs = repo.exists()
        && crate::swarm::gitback::fan_out::snapshot_refs(&repo)
            .map(|m| !m.is_empty())
            .unwrap_or(false);
    if !has_refs {
        return not_found();
    }

    // Build the bundle into a temp file under incoming/, read it
    // back, hand it over. Bounded by the capped pushes that built
    // this bare repo; symmetric with the push handler's temp-file
    // dance.
    let staging = gitback_base.join("incoming");
    if let Err(e) = std::fs::create_dir_all(&staging) {
        return service_unavailable(&format!("create incoming dir: {}", e));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&staging, std::fs::Permissions::from_mode(0o700));
    }
    let nonce: u128 = rand::random();
    let bundle_path = staging.join(format!("{}-serve-{:032x}.bundle", pid_hex, nonce));
    let bytes = match crate::swarm::gitback::fan_out::build_bundle(&repo, &bundle_path)
        .and_then(|_| std::fs::read(&bundle_path).map_err(Into::into))
    {
        Ok(b) => b,
        Err(e) => {
            let _ = std::fs::remove_file(&bundle_path);
            warn!(project_id = %pid_hex, error = %e, "gitback bundle GET: build failed");
            return service_unavailable(&format!("build bundle: {:#}", e));
        }
    };
    let _ = std::fs::remove_file(&bundle_path);

    info!(project_id = %pid_hex, bytes = bytes.len(), "gitback bundle GET: served");
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", "application/x-git-bundle")
        .body(Full::new(Bytes::from(bytes)))
        .unwrap()
}

#[derive(Debug, Clone, Copy)]
enum EnvelopeKind {
    Revoke,
    Leave,
}

/// Extract the `<pid_hex>` segment from a path of shape
/// `/swarm/gitback/envelopes/<pid_hex>/<suffix>`. Returns `None`
/// if the shape is wrong or `<pid_hex>` isn't valid.
fn parse_envelope_path(path: &str) -> Option<[u8; crate::swarm::anchor::tokens::ID_LEN]> {
    let stripped = path.strip_prefix("/swarm/gitback/envelopes/")?;
    let (pid_hex, _suffix) = stripped.split_once('/')?;
    if !is_project_id_path_segment(pid_hex) {
        return None;
    }
    crate::swarm::gitback::project_state::parse_hex_id(pid_hex).ok()
}

/// Cap on accepted envelope JSON body size. Envelopes are tiny
/// (~600B for a leave, ~700B for a revoke); a kilobyte ceiling
/// rejects abuse without rejecting legitimate input.
const MAX_ENVELOPE_BODY: u64 = 16 * 1024;

async fn handle_envelope_post(
    path: &str,
    req: Request<hyper::body::Incoming>,
    kind: EnvelopeKind,
) -> Response<Full<Bytes>> {
    use http_body_util::BodyExt;

    let project_id = match parse_envelope_path(path) {
        Some(p) => p,
        None => return bad_request("malformed envelope path"),
    };

    // Read the body with a tight cap.
    let mut body = req.into_body();
    let mut buf = Vec::with_capacity(1024);
    let mut total: u64 = 0;
    loop {
        let frame = match body.frame().await {
            Some(Ok(f)) => f,
            Some(Err(e)) => return bad_request(&format!("body read error: {}", e)),
            None => break,
        };
        if let Ok(data) = frame.into_data() {
            let chunk: &[u8] = &data;
            total = total.saturating_add(chunk.len() as u64);
            if total > MAX_ENVELOPE_BODY {
                return payload_too_large("envelope body exceeds 16KiB");
            }
            buf.extend_from_slice(chunk);
        }
    }

    let gitback_base = crate::platform::paths::default_gitback_dir();
    let result = tokio::task::block_in_place(|| -> Result<bool, anyhow::Error> {
        match kind {
            EnvelopeKind::Revoke => {
                let env: crate::swarm::gitback::envelopes::RevokeEnvelope =
                    serde_json::from_slice(&buf)
                        .map_err(|e| anyhow::anyhow!("parse revoke envelope: {}", e))?;
                let raw = env
                    .verify_signature()
                    .map_err(|e| anyhow::anyhow!("verify_signature: {}", e))?;
                if raw.project_id != project_id {
                    return Err(anyhow::anyhow!(
                        "envelope project_id does not match URL"
                    ));
                }
                let changed =
                    crate::swarm::gitback::envelopes::save_revoke(&gitback_base, &env)?;
                Ok(changed)
            }
            EnvelopeKind::Leave => {
                let env: crate::swarm::gitback::envelopes::LeaveEnvelope =
                    serde_json::from_slice(&buf)
                        .map_err(|e| anyhow::anyhow!("parse leave envelope: {}", e))?;
                let raw = env
                    .verify_signature()
                    .map_err(|e| anyhow::anyhow!("verify_signature: {}", e))?;
                if raw.project_id != project_id {
                    return Err(anyhow::anyhow!(
                        "envelope project_id does not match URL"
                    ));
                }
                let changed =
                    crate::swarm::gitback::envelopes::save_leave(&gitback_base, &env)?;
                Ok(changed)
            }
        }
    });

    match result {
        Ok(changed) => {
            let body = format!("{{\"accepted\":true,\"changed\":{}}}", changed);
            ok_json(&body)
        }
        Err(e) => {
            warn!(
                kind = ?kind,
                error = %e,
                "envelope POST: rejected"
            );
            bad_request(&format!("rejected: {:#}", e))
        }
    }
}

fn handle_envelope_digest(path: &str) -> Response<Full<Bytes>> {
    let project_id = match parse_envelope_path(path) {
        Some(p) => p,
        None => return bad_request("malformed envelope path"),
    };
    let gitback_base = crate::platform::paths::default_gitback_dir();
    match crate::swarm::gitback::fan_out::DigestResponse::for_project(
        &gitback_base,
        &project_id,
    ) {
        Ok(d) => match serde_json::to_string(&d) {
            Ok(s) => ok_json(&s),
            Err(e) => service_unavailable(&format!("serialize digest: {}", e)),
        },
        Err(e) => service_unavailable(&format!("compute digest: {:#}", e)),
    }
}

fn handle_envelope_all(path: &str) -> Response<Full<Bytes>> {
    let project_id = match parse_envelope_path(path) {
        Some(p) => p,
        None => return bad_request("malformed envelope path"),
    };
    let gitback_base = crate::platform::paths::default_gitback_dir();
    match crate::swarm::gitback::fan_out::EnvelopesBundle::for_project(
        &gitback_base,
        &project_id,
    ) {
        Ok(b) => match serde_json::to_string(&b) {
            Ok(s) => ok_json(&s),
            Err(e) => service_unavailable(&format!("serialize bundle: {}", e)),
        },
        Err(e) => service_unavailable(&format!("load bundle: {:#}", e)),
    }
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

// ── Gitback cross-subnet push handler (Phase 7) ──────────────────────────

/// Handle `POST /gitback/push/<label>` — the cross-subnet direct
/// push endpoint.
///
/// Path format: `/gitback/push/<url-encoded-label>`. The label is
/// a full content-seed label `gitback:<seeder>:<user>:<project>`;
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
/// 1. Gitback enabled on this envoy (`gitback_ctx.is_some()`)
/// 2. Label parses into (seeder, user, project) segments
/// 3. Label's `<seeder>` segment equals `X-Vigo-Seeder`
/// 4. Local user from `<user>` segment exists
/// 5. Body size ≤ the per-host `swarm.gitback.max_bundle_bytes`
/// 6. Body sha256 matches `X-Vigo-Blob-Sha256`
///
/// On accept:
///
/// - Body is written atomically to the blob cache at the computed
///   sha path.
/// - A manifest entry is recorded locally so the gitback
///   receiver's next tick can resolve the label → blob.
/// - The `ClaimsHandle` receives an `ingest_lan_claim(label,
///   sender, true, now)` so the reactor picks it up on its next
///   tick.
///
/// Returns 200 OK with a short JSON body on success.
async fn handle_gitback_push(
    path: &str,
    req: Request<hyper::body::Incoming>,
    state: &ServerState,
) -> Response<Full<Bytes>> {
    // 1. Gitback must be enabled on this envoy. The context is
    // always present on new builds, but the gate may be off — both
    // conditions return the same 403 to the caller.
    let ctx = match state.gitback_ctx.as_ref() {
        Some(c) => c.clone(),
        None => return forbidden("gitback not enabled on this envoy"),
    };
    if !ctx.gate.enabled() {
        return forbidden("gitback not enabled on this envoy");
    }

    // 2. Extract the label from the URL path.
    let raw_label = path.trim_start_matches("/swarm/gitback/push/");
    let label = match url_decode(raw_label) {
        Some(l) => l,
        None => return bad_request("malformed label url-encoding"),
    };

    // 3. Parse label segments.
    let (seeder, user, project) = match parse_gitback_label(&label) {
        Some(t) => t,
        None => return bad_request("malformed gitback label"),
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
    if !seeder_header.eq_ignore_ascii_case(&seeder) {
        return forbidden(&format!(
            "seeder mismatch: header '{}' does not match label seeder '{}'",
            seeder_header, seeder,
        ));
    }

    // 6. Validate sha header shape.
    if !validate_sha256(&sha_header) {
        return bad_request("X-Vigo-Blob-Sha256 is not a 64-char hex sha256");
    }

    // 7. Local user must exist. Gitback won't materialize a
    // mirror for a user with no local account on this envoy.
    if crate::platform::users::lookup_user(&user).is_none() {
        return forbidden(&format!("user '{}' does not exist on this envoy", user));
    }

    let _ = project; // validated as non-empty segment above

    // 7a. Receiver-side disk-pressure guard. The sender probes before
    // push, but the probe is cached 60 s — a concurrent writer could
    // have pushed free-pct below threshold in that window. Cheap
    // statvfs on the gitback mirror mountpoint; reject 507 before
    // reading a byte of the bundle body.
    if super::peer_health::is_self_pressured(&super::peer_health::Purpose::Gitback) {
        return insufficient_storage("gitback mirror filesystem under disk pressure");
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
    let max_bundle_bytes = crate::swarm::runtime_limits::gitback_max_bundle_bytes();

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
                if total > max_bundle_bytes {
                    let _ = std::fs::remove_file(&tmp_path);
                    return payload_too_large(&format!(
                        "bundle exceeds the {}-byte cap (swarm.gitback.max_bundle_bytes)",
                        max_bundle_bytes,
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
            "gitback push: failed to move blob into place"
        );
        return service_unavailable(&format!("failed to write blob: {}", e));
    }

    // 11. Inject a manifest entry so the gitback receiver reactor
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
    // its next tick. Same path as a GITBACKCLAIM multicast —
    // the source hostname is used for dedup keying.
    let now = crate::swarm::gitback::state::unix_seconds_now();
    ctx.claims.ingest_lan_claim(&label, &seeder, true, now);

    info!(
        label = %label,
        seeder = %seeder,
        size = body_len,
        sha = %&actual_sha[..12.min(actual_sha.len())],
        "gitback push: accepted bundle"
    );

    ok_json(r#"{"status":"accepted"}"#)
}

/// Handle `POST /swarm/longdrawer/announce` — cross-subnet unicast announcements
/// from peers listed in a user's `~<user>/.longdrawer/config.yaml` →
/// `cross_subnet_peers:`. The body is a JSON-serialized longdrawer Announcement.
/// The sender's TCP peer address is used as the reconciler's peer (same
/// model as multicast, where the UDP source IP is used), so subsequent
/// `GET /swarm/longdrawer/<user>/<file>` fetches go back to the real remote host
/// rather than a self-reported hostname.
///
/// mTLS already attests the sender at the transport layer; the
/// authorization question here is "is longdrawer accepting work?" — if
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
        return payload_too_large("longdrawer announcement exceeds size limit");
    }

    let announcement: crate::swarm::longdrawer::state::Announcement =
        match serde_json::from_slice(&body_bytes) {
            Ok(a) => a,
            Err(e) => return bad_request(&format!("malformed announcement: {}", e)),
        };

    if crate::swarm::longdrawer::submit_announcement(peer_addr, announcement) {
        ok_json(r#"{"status":"accepted"}"#)
    } else {
        service_unavailable("longdrawer daemon not running on this envoy")
    }
}

/// Parse a gitback content-seed label `gitback:<seeder>:<user>:<project>`
/// into its three segments. Returns `None` for malformed labels.
fn parse_gitback_label(label: &str) -> Option<(String, String, String)> {
    let rest = label.strip_prefix("gitback:")?;
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
/// plus `-`, `_`, and `.`. Anything else we leave as-is and let the
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
            gitback_ctx: None,
        })
    }

    #[test]
    fn test_build_meta_etag_format() {
        assert_eq!(build_meta_etag(1, 1), r#""1-1""#);
        assert_eq!(build_meta_etag(42, 1000), r#""42-1000""#);
    }

    // ── Phase 7 gitback push pure helpers ────────────────────

    #[test]
    fn parse_gitback_label_accepts_valid_shape() {
        let (s, u, p) = parse_gitback_label("gitback:danlap:dan:vigo-web").unwrap();
        assert_eq!(s, "danlap");
        assert_eq!(u, "dan");
        assert_eq!(p, "vigo-web");
    }

    #[test]
    fn parse_gitback_label_accepts_project_with_dashes_and_dots() {
        let (_, _, p) = parse_gitback_label("gitback:h:u:my-repo.v2").unwrap();
        assert_eq!(p, "my-repo.v2");
    }

    #[test]
    fn parse_gitback_label_rejects_missing_prefix() {
        assert!(parse_gitback_label("danlap:dan:vigo-web").is_none());
    }

    #[test]
    fn parse_gitback_label_rejects_too_few_segments() {
        assert!(parse_gitback_label("gitback:dan:vigo-web").is_none());
        assert!(parse_gitback_label("gitback:danlap").is_none());
    }

    #[test]
    fn parse_gitback_label_rejects_empty_segment() {
        assert!(parse_gitback_label("gitback::dan:vigo-web").is_none());
        assert!(parse_gitback_label("gitback:dan::vigo-web").is_none());
        assert!(parse_gitback_label("gitback:dan:user:").is_none());
    }

    #[test]
    fn url_decode_handles_percent_colons() {
        assert_eq!(
            url_decode("gitback%3Adanlap%3Adan%3Avigo-web").unwrap(),
            "gitback:danlap:dan:vigo-web",
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
            url_decode("gitback:danlap:dan:vigo-web").unwrap(),
            "gitback:danlap:dan:vigo-web",
        );
    }

    #[test]
    fn is_project_id_path_segment_accepts_64_lowercase_hex() {
        assert!(is_project_id_path_segment(&"0".repeat(64)));
        assert!(is_project_id_path_segment(&"a".repeat(64)));
        assert!(is_project_id_path_segment(&"abcdef0123456789".repeat(4)));
    }

    #[test]
    fn is_project_id_path_segment_rejects_wrong_length() {
        assert!(!is_project_id_path_segment(&"a".repeat(63)));
        assert!(!is_project_id_path_segment(&"a".repeat(65)));
        assert!(!is_project_id_path_segment(""));
    }

    #[test]
    fn is_project_id_path_segment_rejects_uppercase() {
        // Canonical form is lowercase hex; rejecting uppercase keeps
        // the route disambiguator unambiguous (legacy labels can also
        // contain ASCII letters, but neither flavor uses uppercase
        // ASCII alphabetics in the project_id position).
        assert!(!is_project_id_path_segment(&"A".repeat(64)));
    }

    #[test]
    fn is_project_id_path_segment_rejects_legacy_label() {
        // Legacy "gitback:host:user:project" labels never match —
        // colons are not hex and the length almost never aligns to 64.
        assert!(!is_project_id_path_segment("gitback:danlap:dan:vigo-web"));
        // Even a 64-char string with non-hex chars must be rejected.
        let mostly_hex = format!("{}!", "a".repeat(63));
        assert!(!is_project_id_path_segment(&mostly_hex));
    }

    #[test]
    fn handle_gitback_bundle_get_rejects_bad_inputs() {
        // Non-hex path segment → 400, before any filesystem touch.
        let r = handle_gitback_bundle_get("not-a-pid", Some("whatever"));
        assert_eq!(r.status(), StatusCode::BAD_REQUEST);
        // Valid pid but no chain header → 400.
        let r = handle_gitback_bundle_get(&"a".repeat(64), None);
        assert_eq!(r.status(), StatusCode::BAD_REQUEST);
        // Valid pid, garbage chain header → 400 (decode fails).
        let r = handle_gitback_bundle_get(&"a".repeat(64), Some("!!!not base64!!!"));
        assert_eq!(r.status(), StatusCode::BAD_REQUEST);
    }

    #[test]
    fn parse_envelope_path_extracts_canonical_pid() {
        let pid = "0".repeat(64);
        let parsed = parse_envelope_path(&format!(
            "/swarm/gitback/envelopes/{}/digest",
            pid
        ))
        .unwrap();
        assert_eq!(parsed, [0u8; 32]);
    }

    #[test]
    fn parse_envelope_path_handles_revokes_suffix() {
        let pid = "f".repeat(64);
        let parsed = parse_envelope_path(&format!(
            "/swarm/gitback/envelopes/{}/revokes",
            pid
        ))
        .unwrap();
        assert_eq!(parsed, [0xFFu8; 32]);
    }

    #[test]
    fn parse_envelope_path_rejects_short_pid() {
        assert!(parse_envelope_path("/swarm/gitback/envelopes/abc/digest").is_none());
    }

    #[test]
    fn parse_envelope_path_rejects_missing_suffix() {
        let pid = "a".repeat(64);
        assert!(parse_envelope_path(&format!("/swarm/gitback/envelopes/{}", pid)).is_none());
    }

    #[test]
    fn parse_envelope_path_rejects_uppercase_pid() {
        let pid = "A".repeat(64);
        assert!(parse_envelope_path(&format!("/swarm/gitback/envelopes/{}/digest", pid)).is_none());
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
