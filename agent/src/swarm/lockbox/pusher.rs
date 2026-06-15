// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Lockbox content pusher.
//!
//! Consumes `PushJob` events from the watcher and POSTs each
//! ciphertext blob to:
//!
//! 1. Every live peer in `peers.json` for the job's user, using the
//!    peer's `last_seen_addr` (captured when the announcement arrived).
//! 2. Every `cross_subnet_peers:` hostname listed in the user's
//!    `~/.lockbox/config.yaml`, resolved via DNS.
//!
//! Uses a dedicated `swarm::tls::SwarmAgent` with a 5-minute global
//! timeout — long enough for a 1 GiB push over a reasonable network,
//! but bounded so a stuck connection can't pin the blocking pool
//! forever. Pushes run in `spawn_blocking` so the tokio runtime
//! stays responsive. Server-cert verification chains to the fleet CA
//! but skips the hostname check — see `swarm::tls::FleetCaVerifier`.
//!
//! No retry on failure: the receiver-side `maybe_catchup_files`
//! handler in `mod.rs` watches the `files_digest` on every incoming
//! announcement and pulls any missing blobs via
//! `GET /swarm/lockbox/files/<user>` + `GET /swarm/lockbox/pull/<user>/<filename>`.
//! The pusher's job is best-effort proactive delivery; catch-up is
//! the safety net that closes the empty-peers race here at line 111
//! (push fires with zero live peers → drops silently → next
//! announcement cycle the receiver pulls and converges).

use std::time::Duration;

use anyhow::{Context, Result};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::swarm::tls::{self, SwarmAgent};
use crate::update::TlsCerts;
use super::blobsig;
use super::config;
use super::state::{PeerTable, STALE_SECS};
use super::tombstones::{self, DeleteEnvelope, Tombstone, TombstoneTable};
use super::watcher::{DeleteJob, PushJob};

/// Per-push HTTP timeout. Allows a 1 GiB push over ~30 Mbps without
/// timing out; shorter pushes finish well under this.
const PUSH_TIMEOUT: Duration = Duration::from_secs(300);

/// Max concurrent pushes across all peers. Bounded so a fan-out to
/// a large fleet doesn't starve the blocking threadpool.
const MAX_INFLIGHT: usize = 4;

/// Spawn the pusher. Drains push + delete jobs until cancelled.
pub fn spawn(
    tls: TlsCerts,
    mut push_jobs: mpsc::Receiver<PushJob>,
    mut delete_jobs: mpsc::Receiver<DeleteJob>,
    cancel: CancellationToken,
) {
    tokio::spawn(async move {
        let agent = match build_agent(&tls) {
            Ok(a) => a,
            Err(e) => {
                warn!(error = %e, "lockbox pusher: cannot build HTTPS agent; fan-out disabled");
                return;
            }
        };
        info!("lockbox pusher started");

        let sem = std::sync::Arc::new(tokio::sync::Semaphore::new(MAX_INFLIGHT));
        loop {
            tokio::select! {
                _ = cancel.cancelled() => {
                    info!("lockbox pusher: cancelled, exiting");
                    return;
                }
                maybe = push_jobs.recv() => {
                    let job = match maybe {
                        Some(j) => j,
                        None => return,
                    };
                    let agent_clone = agent.clone();
                    let sem_clone = sem.clone();
                    tokio::spawn(async move {
                        let _permit = sem_clone.acquire_owned().await.ok();
                        if let Err(e) = fan_out(&agent_clone, job).await {
                            debug!(error = %e, "lockbox pusher: fan_out error");
                        }
                    });
                }
                maybe = delete_jobs.recv() => {
                    let job = match maybe {
                        Some(j) => j,
                        None => return,
                    };
                    let agent_clone = agent.clone();
                    let sem_clone = sem.clone();
                    tokio::spawn(async move {
                        let _permit = sem_clone.acquire_owned().await.ok();
                        if let Err(e) = fan_out_delete(&agent_clone, job).await {
                            debug!(error = %e, "lockbox pusher: fan_out_delete error");
                        }
                    });
                }
            }
        }
    });
}

async fn fan_out(agent: &SwarmAgent, job: PushJob) -> Result<()> {
    let PushJob { user, filename, bytes, mtime } = job;

    // Sign the blob content so receivers can authenticate the sender and
    // detect a swapped/replayed blob. The descriptor binds user, filename,
    // mtime (LWW version), and a SHA-256 of the ciphertext under this
    // envoy's Ed25519 signing key. Missing key = cold-boot race before
    // enrollment finished; skip the push (the receiver would reject an
    // unsigned blob anyway) and let catch-up converge once the key exists.
    let hostname = super::read_hostname();
    let signing_key = match super::load_local_signing_key(&user) {
        Ok(k) => k,
        Err(e) => {
            warn!(
                user = %user,
                filename = %filename,
                error = %e,
                "pusher: cannot load signing key; skipping push (catch-up will converge)"
            );
            return Ok(());
        }
    };
    let blob_sig = blobsig::build(&signing_key, &hostname, &user, &filename, mtime, &bytes);
    let blob_sig_header = match serde_json::to_string(&blob_sig) {
        Ok(s) => std::sync::Arc::new(s),
        Err(e) => {
            warn!(error = %e, "pusher: serializing blob signature failed; skipping push");
            return Ok(());
        }
    };

    // Collect targets: LAN peers from peers.json + cross_subnet_peers from
    // the user's config.yaml.
    let (lan_targets, stats) = lan_targets_for(&user);
    let extra_targets = extra_targets_for(&user);
    let total = lan_targets.len() + extra_targets.len();
    if total == 0 {
        warn!(
            user = %user,
            filename = %filename,
            peers_total = stats.peers_total,
            stale = stats.stale,
            no_addr = stats.no_addr,
            "pusher: no targets; nothing to push"
        );
        return Ok(());
    }
    info!(
        user = %user,
        filename = %filename,
        size_bytes = bytes.len(),
        lan = lan_targets.len(),
        extra = extra_targets.len(),
        "pusher: fanning out",
    );

    // Fan out. Each POST runs in spawn_blocking so the ureq call
    // (blocking) doesn't tie up the tokio runtime. `bytes` is an Arc,
    // so the per-target clone is a refcount bump — the ciphertext
    // payload is shared across all in-flight sends.
    let mut handles = Vec::with_capacity(total);
    for target in lan_targets.into_iter().chain(extra_targets) {
        let agent = agent.clone();
        let user = user.clone();
        let filename = filename.clone();
        let bytes = std::sync::Arc::clone(&bytes);
        let blob_sig_header = std::sync::Arc::clone(&blob_sig_header);
        handles.push(tokio::task::spawn_blocking(move || {
            push_one(&agent, &target, &user, &filename, &bytes, &blob_sig_header)
        }));
    }
    for h in handles {
        match h.await {
            Ok(Ok(())) => {}
            Ok(Err(e)) => warn!(error = %format!("{e:#}"), "pusher: peer push failed"),
            Err(e) => warn!(error = %e, "pusher: spawn_blocking join failed"),
        }
    }
    Ok(())
}

/// Fan a deletion out to every live peer for this user.
///
/// Steps:
/// 1. Remove the local ciphertext file (if it still exists — a
///    conservative second attempt; the watcher fired because the
///    user already `rm`'d it, but the daemon double-checks so the
///    source envoy ends up consistent with what it's telling peers).
/// 2. Record the tombstone in the local store.
/// 3. Build a signed DELETE envelope.
/// 4. POST it to every live LAN peer + cross_subnet_peers host.
///
/// If no signing identity is available (cold-boot race before
/// `ensure_signing_identity` ran), log a warning and return without
/// fanning out — the tombstone is still recorded locally so
/// resurrection is blocked on THIS envoy. Recovery is automatic
/// on the next announce cycle once the signing key is generated.
async fn fan_out_delete(agent: &SwarmAgent, job: DeleteJob) -> Result<()> {
    let DeleteJob {
        user,
        filename,
        deletion_time,
    } = job;

    let hostname = super::read_hostname();

    // Record tombstone locally BEFORE fanning out — if we crash
    // between steps, the authoritative state is "this envoy
    // deleted foo.pdf," and next announce will carry the digest
    // change so peers catch up. Wrapped in TombstoneTable::modify
    // so concurrent DeleteJobs for the same user can't race their
    // load-modify-save pairs (lost-update + tmp-rename collision
    // observed during bulk-delete on §6 livetest run).
    if let Err(e) = TombstoneTable::modify(&user, |table| {
        table.upsert(
            &filename,
            Tombstone {
                deletion_time,
                source_hostname: hostname.clone(),
            },
        )
    }) {
        warn!(
            user = %user,
            filename = %filename,
            error = %e,
            "pusher: tombstone save failed"
        );
    }

    // Double-check local removal. The file should already be gone
    // (watcher fired because the user rm'd it), but an errant rm-rf
    // pattern or a race could leave a stale copy. Idempotent — unlink
    // errors are only logged, not fatal.
    if let Some(home) = super::home_for_user_pub(&user) {
        let content_path = config::content_dir(&home).join(&filename);
        if content_path.exists() {
            if let Err(e) = std::fs::remove_file(&content_path) {
                debug!(
                    path = %content_path.display(),
                    error = %e,
                    "pusher: local file remove during delete fan-out failed"
                );
            }
        }
    }

    // Load the signing key. Missing key = daemon raced enrollment;
    // log and return (retry on next delete event — local tombstone
    // still exists).
    let signing_key = match super::load_local_signing_key(&user) {
        Ok(k) => k,
        Err(e) => {
            warn!(
                user = %user,
                filename = %filename,
                error = %e,
                "pusher: cannot load signing key, tombstone recorded locally but not fanned out"
            );
            return Ok(());
        }
    };

    let envelope =
        tombstones::build_envelope(&signing_key, &hostname, &user, &filename, deletion_time);

    let (lan_targets, stats) = lan_targets_for(&user);
    let extra_targets = extra_targets_for(&user);
    let total = lan_targets.len() + extra_targets.len();
    if total == 0 {
        warn!(
            user = %user,
            filename = %filename,
            peers_total = stats.peers_total,
            stale = stats.stale,
            no_addr = stats.no_addr,
            "pusher: no DELETE targets; tombstone recorded locally only"
        );
        return Ok(());
    }
    info!(
        user = %user,
        filename = %filename,
        deletion_time = deletion_time,
        lan = lan_targets.len(),
        extra = extra_targets.len(),
        "pusher: fanning out DELETE",
    );

    let mut handles = Vec::with_capacity(total);
    for target in lan_targets.into_iter().chain(extra_targets) {
        let agent = agent.clone();
        let envelope = envelope.clone();
        handles.push(tokio::task::spawn_blocking(move || {
            push_delete_one(&agent, &target, &envelope)
        }));
    }
    for h in handles {
        match h.await {
            Ok(Ok(())) => {}
            Ok(Err(e)) => warn!(error = %format!("{e:#}"), "pusher: peer DELETE failed"),
            Err(e) => warn!(error = %e, "pusher: DELETE spawn_blocking join failed"),
        }
    }
    Ok(())
}

/// One DELETE attempt. Blocking; runs inside `spawn_blocking`.
fn push_delete_one(
    agent: &SwarmAgent,
    target: &PushTarget,
    envelope: &DeleteEnvelope,
) -> Result<()> {
    // REST-ish: POST an envelope to /lockbox/tombstones/<user>/<filename>.
    // We want a body on the request (signed envelope is the entire
    // reason for the endpoint), and ureq's DELETE builder doesn't
    // accept bodies — so this stays POST. Routing is unambiguous
    // because the path segment carries the verb semantic.
    let url = format!(
        "https://{}/swarm/lockbox/tombstones/{}/{}",
        target.addr,
        urlencode(&envelope.user),
        urlencode(&envelope.filename),
    );
    let body = serde_json::to_vec(envelope).context("serializing DELETE envelope")?;
    let resp = agent
        .post(&url)
        .header("Content-Type", "application/json")
        .body(body)
        .send()
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        anyhow::bail!("peer {} returned HTTP {}", target.label, status.as_u16());
    }
    debug!(
        target = %target.label,
        user = %envelope.user,
        filename = %envelope.filename,
        "pusher: DELETE accepted"
    );
    Ok(())
}

/// One push attempt. Blocking; runs inside `spawn_blocking`.
fn push_one(
    agent: &SwarmAgent,
    target: &PushTarget,
    user: &str,
    filename: &str,
    bytes: &[u8],
    blob_sig_header: &str,
) -> Result<()> {
    // Pre-flight disk-pressure probe. Peers at or below the free-%
    // threshold get skipped here so we never put bytes on the wire.
    // Cached per (peer, purpose) for 60s — fan-out across N peers
    // probes each one once per minute, not once per file.
    use crate::swarm::peer_health::{probe, Purpose};
    let outcome = probe(
        agent,
        &target.addr,
        &Purpose::Lockbox { user: user.to_string() },
    );
    if !outcome.allow() {
        warn!(
            target = %target.label,
            user = %user,
            filename = %filename,
            reason = outcome.reason(),
            "pusher: skipping push to disk-pressured peer"
        );
        return Ok(());
    }

    let url = format!(
        "https://{}/swarm/lockbox/push/{}/{}",
        target.addr,
        urlencode(user),
        urlencode(filename)
    );
    // reqwest's blocking body takes an owned buffer, so this copies the
    // slice once per target. The payload is still shared in as an
    // `Arc<Vec<u8>>` by the caller; only the per-send body is owned.
    let resp = agent
        .post(&url)
        .header("Content-Type", "application/octet-stream")
        .header(blobsig::BLOBSIG_HEADER, blob_sig_header)
        .body(bytes.to_vec())
        .send()
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        anyhow::bail!("peer {} returned HTTP {}", target.label, status.as_u16());
    }
    debug!(
        target = %target.label,
        user = %user,
        filename = %filename,
        "pusher: pushed"
    );
    Ok(())
}

#[derive(Debug, Clone)]
struct PushTarget {
    /// "<ip>:1531" or "<host>:1531" — what goes into the URL.
    addr: String,
    /// Human-readable label for logs.
    label: String,
}

/// Per-call diagnostic counters from `lan_targets_for`. Surfaces why a
/// fan-out had zero targets when the peers.json appeared populated.
struct LanTargetStats {
    /// Total entries in peers.json (excluding self).
    peers_total: usize,
    /// Peers skipped because `now - last_seen > STALE_SECS`.
    stale: usize,
    /// Peers skipped because `last_seen_addr` was None.
    no_addr: usize,
}

fn lan_targets_for(user: &str) -> (Vec<PushTarget>, LanTargetStats) {
    let table = PeerTable::load(user);
    let now = super::now_secs_pub();
    let mut stats = LanTargetStats {
        peers_total: table.peers.len(),
        stale: 0,
        no_addr: 0,
    };
    let mut out = Vec::new();
    for (host, peer) in &table.peers {
        if now - peer.last_seen > STALE_SECS {
            stats.stale += 1;
            continue;
        }
        let addr = match &peer.last_seen_addr {
            Some(a) => a.clone(),
            None => {
                stats.no_addr += 1;
                continue;
            }
        };
        // Strip any port the SocketAddr display included — we
        // always target the peer server on 1531, not the UDP
        // announce source port.
        let host_only = strip_port(&addr);
        out.push(PushTarget {
            addr: format!("{}:{}", host_only, PEER_PORT),
            label: host.clone(),
        });
    }
    (out, stats)
}

fn extra_targets_for(user: &str) -> Vec<PushTarget> {
    let home = match super::home_for_user_pub(user) {
        Some(h) => h,
        None => return Vec::new(),
    };
    let cfg = config::load_config(&home);
    cfg.cross_subnet_peers
        .into_iter()
        .map(|h| PushTarget {
            addr: format!("{}:{}", h, PEER_PORT),
            label: h,
        })
        .collect()
}

const PEER_PORT: u16 = 1531;

fn strip_port(addr: &str) -> String {
    // For IPv4 "1.2.3.4:1234" or IPv6 "[::1]:1234". Use the last ':'
    // for IPv4 and the right bracket for IPv6.
    if let Some(rb) = addr.rfind(']') {
        // IPv6 literal — keep brackets, drop port.
        return addr[..=rb].to_string();
    }
    match addr.rfind(':') {
        Some(i) => addr[..i].to_string(),
        None => addr.to_string(),
    }
}

fn urlencode(s: &str) -> String {
    // Percent-encode anything that isn't an unreserved URL char.
    let mut out = String::with_capacity(s.len());
    for b in s.as_bytes() {
        match *b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(*b as char);
            }
            _ => {
                out.push_str(&format!("%{:02X}", b));
            }
        }
    }
    out
}

/// Exposed to mod.rs for the cross_subnet_peers announce loop, which needs
/// the same TLS-configured agent.
pub(crate) fn build_agent_for_mod(tls: &TlsCerts) -> Result<SwarmAgent> {
    build_agent(tls)
}

/// Fan a signed LEAVE envelope out to every live peer + cross-subnet
/// peer for `user`. Called from `mod.rs` when the daemon processes a
/// `leave.pending` sentinel. Offline peers are skipped; they catch
/// up via natural staleness (90 s without announcement → dropped
/// from live_pubkeys on the peer side).
///
/// Best-effort: individual push failures are logged but do not fail
/// the overall call. The caller has already persisted the LEAVE to
/// local `leaves.json` and removed the sentinel — propagation is
/// pure network push.
pub async fn fan_out_leave(
    tls: &TlsCerts,
    envelope: super::leaves::LeaveEnvelope,
) -> Result<()> {
    let agent = build_agent(tls)?;
    let (lan_targets, stats) = lan_targets_for(&envelope.user);
    let extra_targets = extra_targets_for(&envelope.user);
    let total = lan_targets.len() + extra_targets.len();
    if total == 0 {
        warn!(
            user = %envelope.user,
            peers_total = stats.peers_total,
            stale = stats.stale,
            no_addr = stats.no_addr,
            "pusher: no LEAVE targets; envelope recorded locally only"
        );
        return Ok(());
    }
    info!(
        user = %envelope.user,
        age_pubkey = %envelope.age_pubkey_leaving,
        lan = lan_targets.len(),
        extra = extra_targets.len(),
        "pusher: fanning out LEAVE",
    );
    let mut handles = Vec::with_capacity(total);
    for target in lan_targets.into_iter().chain(extra_targets) {
        let agent = agent.clone();
        let envelope = envelope.clone();
        handles.push(tokio::task::spawn_blocking(move || {
            push_leave_one(&agent, &target, &envelope)
        }));
    }
    for h in handles {
        match h.await {
            Ok(Ok(())) => {}
            Ok(Err(e)) => warn!(error = %format!("{e:#}"), "pusher: peer LEAVE failed"),
            Err(e) => warn!(error = %e, "pusher: LEAVE spawn_blocking join failed"),
        }
    }
    Ok(())
}

/// One LEAVE attempt. Blocking; runs inside `spawn_blocking`.
fn push_leave_one(
    agent: &SwarmAgent,
    target: &PushTarget,
    envelope: &super::leaves::LeaveEnvelope,
) -> Result<()> {
    let url = format!(
        "https://{}/swarm/lockbox/leaves/{}",
        target.addr,
        urlencode(&envelope.user),
    );
    let body = serde_json::to_vec(envelope).context("serializing LEAVE envelope")?;
    let resp = agent
        .post(&url)
        .header("Content-Type", "application/json")
        .body(body)
        .send()
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        anyhow::bail!("peer {} returned HTTP {}", target.label, status.as_u16());
    }
    debug!(
        target = %target.label,
        user = %envelope.user,
        age_pubkey = %envelope.age_pubkey_leaving,
        "pusher: LEAVE accepted"
    );
    Ok(())
}

/// Fan a signed RESURRECT envelope out to every live peer +
/// cross-subnet peer for `envelope.user`. Mirror of
/// `fan_out_leave`. Called from `mod.rs::process_pending_resurrects`
/// when the daemon picks up a `~/.lockbox/resurrect.pending`
/// sentinel.
///
/// The CLI awaits sentinel removal before invoking the encrypt
/// path, so by the time the operator's encrypt fans out, every
/// reachable peer has the resurrect record and the
/// `effective_tombstone` gate returns None — the push lands
/// instead of 410'ing. Offline peers catch up via
/// `resurrects_digest` mismatch on their next announce-receive.
///
/// Best-effort: per-peer failures are logged but don't fail the
/// overall call. The local resurrect record has already been
/// persisted by the caller, so even total fanout failure leaves
/// THIS envoy in a coherent state — peers just learn via
/// digest catch-up instead.
pub async fn fan_out_resurrect(
    tls: &TlsCerts,
    envelope: super::resurrects::ResurrectEnvelope,
) -> Result<()> {
    let agent = build_agent(tls)?;
    let (lan_targets, stats) = lan_targets_for(&envelope.user);
    let extra_targets = extra_targets_for(&envelope.user);
    let total = lan_targets.len() + extra_targets.len();
    if total == 0 {
        warn!(
            user = %envelope.user,
            filename = %envelope.filename,
            peers_total = stats.peers_total,
            stale = stats.stale,
            no_addr = stats.no_addr,
            "pusher: no RESURRECT targets; envelope recorded locally only"
        );
        return Ok(());
    }
    info!(
        user = %envelope.user,
        filename = %envelope.filename,
        resurrect_time = envelope.resurrect_time,
        lan = lan_targets.len(),
        extra = extra_targets.len(),
        "pusher: fanning out RESURRECT",
    );
    let mut handles = Vec::with_capacity(total);
    for target in lan_targets.into_iter().chain(extra_targets) {
        let agent = agent.clone();
        let envelope = envelope.clone();
        handles.push(tokio::task::spawn_blocking(move || {
            push_resurrect_one(&agent, &target, &envelope)
        }));
    }
    for h in handles {
        match h.await {
            Ok(Ok(())) => {}
            Ok(Err(e)) => warn!(error = %format!("{e:#}"), "pusher: peer RESURRECT failed"),
            Err(e) => warn!(error = %e, "pusher: RESURRECT spawn_blocking join failed"),
        }
    }
    Ok(())
}

/// One RESURRECT attempt. Blocking; runs inside `spawn_blocking`.
fn push_resurrect_one(
    agent: &SwarmAgent,
    target: &PushTarget,
    envelope: &super::resurrects::ResurrectEnvelope,
) -> Result<()> {
    let url = format!(
        "https://{}/swarm/lockbox/resurrects/{}/{}",
        target.addr,
        urlencode(&envelope.user),
        urlencode(&envelope.filename),
    );
    let body = serde_json::to_vec(envelope).context("serializing RESURRECT envelope")?;
    let resp = agent
        .post(&url)
        .header("Content-Type", "application/json")
        .body(body)
        .send()
        .with_context(|| format!("POST {}", url))?;
    let status = resp.status();
    if !status.is_success() {
        anyhow::bail!("peer {} returned HTTP {}", target.label, status.as_u16());
    }
    debug!(
        target = %target.label,
        user = %envelope.user,
        filename = %envelope.filename,
        "pusher: RESURRECT accepted"
    );
    Ok(())
}

/// Build the mTLS-configured client used for lockbox pushes.
/// Separate from swarm::peer_client's agent because we need a
/// longer timeout (5 min vs. 30 s) to accommodate 1 GiB blobs.
///
/// Server-cert verification chains the peer's cert to the fleet CA but
/// skips the hostname/SAN check (envoy certs are CN'd by IP) — see
/// `swarm::tls::FleetCaVerifier`. mTLS is preserved: the client presents
/// this envoy's client cert, which the peer verifies against the same CA.
fn build_agent(tls: &TlsCerts) -> Result<SwarmAgent> {
    tls::build_client(tls, Duration::from_secs(5), PUSH_TIMEOUT)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strip_port_ipv4() {
        assert_eq!(strip_port("1.2.3.4:1234"), "1.2.3.4");
        assert_eq!(strip_port("1.2.3.4"), "1.2.3.4");
    }

    #[test]
    fn strip_port_ipv6() {
        assert_eq!(strip_port("[::1]:1234"), "[::1]");
        assert_eq!(strip_port("[fe80::1]:9999"), "[fe80::1]");
    }

    #[test]
    fn urlencode_preserves_unreserved() {
        assert_eq!(urlencode("dan"), "dan");
        assert_eq!(urlencode("my-file.pdf"), "my-file.pdf");
        assert_eq!(urlencode("with space"), "with%20space");
        assert_eq!(urlencode("slash/here"), "slash%2Fhere");
    }
}
