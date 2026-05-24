// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! UDP multicast peer discovery for swarm blob distribution.
//!
//! Agents on the same subnet announce which blobs they have via periodic
//! multicast messages on 224.0.0.42:1531. This is the fastest discovery
//! path — peers are found in under 5 seconds without any server round-trip.
//!
//! Message format (plaintext, LAN-local only):
//! ```text
//! VIGO-SWARM
//! HAVE:<sha256>:<chunks_have>/<chunks_total>
//! HAVE:<sha256>:<chunks_have>/<chunks_total>
//! ADDR:<ip>:<port>
//! ```
//!
//! Graceful fallback: if multicast setup fails (containers, VMs, restricted
//! networks), discovery silently falls through to peer exchange and server
//! peer list.

use std::collections::HashSet;
use std::net::{Ipv4Addr, SocketAddr, SocketAddrV4};
use std::sync::Arc;
use std::time::Duration;

use socket2::{Domain, Protocol, Socket, Type};
use tracing::{debug, info, warn};

use super::bitmap;
use super::cache::BlobCache;
use super::manifest::Manifest;
use super::meta::chunks_for_size;
use super::peer_client;
use super::peermap::{PeerMap, PeerSource};
use crate::update::TlsCerts;
use crate::swarm::gitback::peer_table::{LocalInfo as GitbackLocalInfo, PeerTable as GitbackPeerTable};
use crate::swarm::gitback::pending_claims::{format_claim_line, parse_claim_line, PendingClaims as GitbackPendingClaims};
use crate::swarm::gitback::receiver::ClaimsHandle as GitbackClaimsHandle;

/// Multicast group address.
const MULTICAST_ADDR: Ipv4Addr = Ipv4Addr::new(224, 0, 0, 42);

/// Multicast port (same as peer server).
const MULTICAST_PORT: u16 = 1531;

/// Announce interval when no downloads are in flight. The announcer spends
/// most of its life here — gossiping the local inventory so peers discover
/// it without any urgency.
const ANNOUNCE_INTERVAL_IDLE: Duration = Duration::from_secs(5);

/// Announce interval while a download is in progress. Tightens from 5s →
/// 1s so peers learn about partial bitmaps within 1-2 multicast cycles
/// instead of waiting out a full idle period. Combined with the
/// event-driven fire on the first chunk, this closes the discovery gap
/// that previously kept small-blob LAN downloads from ever trading
/// chunks peer-to-peer.
const ANNOUNCE_INTERVAL_ACTIVE: Duration = Duration::from_secs(1);

/// Hard floor between consecutive announces. Even when a burst of fires
/// arrives (first chunk + first-chunk-of-next-blob + timer tick), the
/// announcer will delay the second send until at least this much time
/// has passed since the last. Prevents the event-driven path from
/// producing more than ~2 announces/sec per envoy.
const MIN_ANNOUNCE_GAP: Duration = Duration::from_millis(500);

/// Maximum multicast message size (UDP payload). Emitted packets must
/// stay at or below this after the SIG footer is appended, so all
/// build-time budget checks use [`MAX_CONTENT_SIZE`] — *not* this — to
/// reserve space for signing.
const MAX_MSG_SIZE: usize = 1400;

/// Upper bound on the SIG footer appended by [`sign_and_append`].
/// Shape is `SIG:<unix_seconds>:<128-hex-chars>\n` — 4 + 1 + ≤10 + 1 +
/// 128 + 1 = 145 bytes in the common case. Rounded up to 160 so the
/// budget tolerates timestamp-width stretch without revisiting. The
/// 0.30.12 fix pair: reserve this here and enlarge the receive buffer
/// past MAX_MSG_SIZE — before, build checks capped at MAX_MSG_SIZE
/// and the SIG was appended *after*, producing ~1500-byte packets
/// that the 1400-byte receive buffer silently truncated, stripping
/// the SIG line and killing gitback authenticity on every datagram.
const SIG_FOOTER_RESERVE: usize = 160;

/// Effective content budget for `build_announcement`. Subtract the
/// SIG footer reserve so signed packets still fit in MAX_MSG_SIZE.
const MAX_CONTENT_SIZE: usize = MAX_MSG_SIZE - SIG_FOOTER_RESERVE;

/// Receive buffer size. Deliberately larger than [`MAX_MSG_SIZE`] so a
/// sender that slips past the budget (protocol growth, a mismatched
/// build, a bug) isn't silently truncated — truncation drops the SIG
/// line and kills gitback authenticity for the whole datagram. A
/// warn-once fires if `recv_from` fills this buffer, flagging the
/// sender as misbehaving instead of leaving the investigator to
/// discover the issue via tcpdump.
const RECV_BUF_SIZE: usize = 2048;

/// Protocol header.
const HEADER: &str = "VIGO-SWARM";

/// Spawn the multicast announcer and listener.
///
/// Returns immediately. Both tasks run until the cancellation token fires.
/// If multicast setup fails, logs a warning and returns — the agent continues
/// without multicast discovery.
///
/// A single shared `ureq::Agent` is built once up front and handed to both
/// the listener-triggered bitmap probe path and the manifest sync path.
/// Before this, every listener-initiated /meta probe called
/// `peer_client::build_peer_agent` — which reads three PEM files from disk
/// and rebuilds the rustls config — on every probe. Under the 1-second
/// active announce cadence this was the dominant cost in the swarm
/// subsystem during heavy multicast traffic. The prober loop already does
/// the same one-agent-reuse trick; the listener path wasn't updated when
/// that optimization landed.
pub fn spawn(
    peer_map: Arc<PeerMap>,
    cache: Arc<BlobCache>,
    manifest: Arc<Manifest>,
    tls: Option<TlsCerts>,
    swarm_port: u16,
    cancel: tokio_util::sync::CancellationToken,
    announce: Arc<super::AnnounceSignal>,
    gitback_peer_table: Option<Arc<GitbackPeerTable>>,
    gitback_local_info: Option<GitbackLocalInfo>,
    gitback_pending_claims: Option<Arc<GitbackPendingClaims>>,
    gitback_claims_handle: Option<GitbackClaimsHandle>,
    gitback_gate: crate::swarm::gitback::Gate,
    gitback_signing_key: Option<Arc<ed25519_dalek::SigningKey>>,
    gitback_pubkey_cache: Option<Arc<crate::swarm::gitback::peer_pubkey_cache::PeerPubkeyCache>>,
) {
    // Make the hostdox host index available before the listener
    // starts so early `HOST:` lines aren't dropped on the floor.
    // Idempotent — `hostdox::spawn` also calls this.
    let _ = crate::swarm::hostdox::init_host_map();

    // Build one shared peer agent at spawn time. Wrapped in an Arc so the
    // listener can clone it cheaply into the blocking tasks it spawns per
    // probe. `None` if TLS isn't configured (bootstrap agents before
    // enrollment) or agent construction failed; callers must handle that.
    let shared_agent: Option<Arc<ureq::Agent>> = tls.as_ref().and_then(|t| {
        match peer_client::build_peer_agent(t) {
            Ok(a) => Some(Arc::new(a)),
            Err(e) => {
                warn!(error = %e, "discovery: failed to build shared peer agent");
                None
            }
        }
    });

    let peer_map_listen = peer_map.clone();
    let manifest_listen = manifest.clone();
    let cancel_listen = cancel.clone();
    let listen_agent = shared_agent.clone();
    let gitback_table_listen = gitback_peer_table.clone();
    let gitback_claims_listen = gitback_claims_handle.clone();
    let listen_hostname = gitback_local_info
        .as_ref()
        .map(|li| li.hostname.clone());
    let gate_listen = gitback_gate.clone();
    let pubkey_cache_listen = gitback_pubkey_cache.clone();

    // Listener task.
    tokio::spawn(async move {
        if let Err(e) = listen_loop(
            peer_map_listen,
            manifest_listen,
            listen_agent,
            cancel_listen,
            gitback_table_listen,
            gitback_claims_listen,
            listen_hostname,
            gate_listen,
            pubkey_cache_listen,
        )
        .await
        {
            warn!(error = %e, "multicast listener exited");
        }
    });

    // Announcer task.
    tokio::spawn(async move {
        if let Err(e) = announce_loop(
            cache,
            peer_map,
            manifest,
            swarm_port,
            cancel,
            announce,
            gitback_local_info,
            gitback_pending_claims,
            gitback_gate,
            gitback_signing_key,
        )
        .await
        {
            warn!(error = %e, "multicast announcer exited");
        }
    });
}

/// Listen for multicast announcements and update the peer map.
#[allow(clippy::too_many_arguments)]
async fn listen_loop(
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    shared_agent: Option<Arc<ureq::Agent>>,
    cancel: tokio_util::sync::CancellationToken,
    gitback_peer_table: Option<Arc<GitbackPeerTable>>,
    gitback_claims_handle: Option<GitbackClaimsHandle>,
    local_hostname: Option<String>,
    gitback_gate: crate::swarm::gitback::Gate,
    gitback_pubkey_cache: Option<Arc<crate::swarm::gitback::peer_pubkey_cache::PeerPubkeyCache>>,
) -> anyhow::Result<()> {
    let socket = create_multicast_socket()?;
    socket.set_nonblocking(true)?;
    let std_socket: std::net::UdpSocket = socket.into();
    let udp = tokio::net::UdpSocket::from_std(std_socket)?;

    info!("multicast listener started on 224.0.0.42:{}", MULTICAST_PORT);

    let mut buf = [0u8; RECV_BUF_SIZE];
    let mut warned_truncation = false;
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            result = udp.recv_from(&mut buf) => {
                match result {
                    Ok((n, src)) => {
                        if n == RECV_BUF_SIZE && !warned_truncation {
                            // Datagram filled the receive buffer — the kernel
                            // may have truncated the tail, which for signed
                            // gitback packets means losing the SIG line and
                            // dropping gitback content for this peer. Warn
                            // once per process so the journal names the
                            // symptom instead of leaving it to tcpdump.
                            warn!(
                                src = %src,
                                recv_buf_size = RECV_BUF_SIZE,
                                "swarm multicast: received datagram filled the buffer; a sender is emitting oversized packets and this one may be truncated"
                            );
                            warned_truncation = true;
                        }
                        if let Ok(msg) = std::str::from_utf8(&buf[..n]) {
                            let mut result = parse_announcement(msg, src, &peer_map, &manifest);
                            // Gitback authenticity (0.30.11+): verify the
                            // SIG line against the embedded pubkey on
                            // the GITBACK line and cross-check the pubkey
                            // against the TOFU cache. On any failure,
                            // strip gitback_peer + gitback_claims so we
                            // don't act on spoofed content. Swarm
                            // HAVE/WANT parsing is kept — those lines
                            // are not security-critical and losing
                            // them on a rollout gap would stall blob
                            // discovery for no gain.
                            if let Some(cache) = gitback_pubkey_cache.as_ref() {
                                if !verify_gitback_authenticity(msg, &result, cache) {
                                    result.gitback_peer = None;
                                    result.gitback_claims.clear();
                                    result.gitback_projects.clear();
                                }
                            }
                            // Ingest any gitback peer info into the
                            // local peer table. Skipped while the
                            // local gate is off — a dormant envoy
                            // drops received GITBACK lines on the
                            // floor so the local peer table doesn't
                            // accumulate state that should not exist.
                            let sender_hostname = result.gitback_peer.as_ref().map(|li| li.hostname.clone());
                            let gate_on = gitback_gate.enabled();
                            if gate_on {
                                if let (Some(table), Some(li)) =
                                    (gitback_peer_table.as_ref(), result.gitback_peer.clone())
                                {
                                    table.update(li.into_peer_entry());
                                }
                            }
                            // Ingest any GITBACKCLAIM entries:
                            // check whether the local hostname is
                            // in each claim list and update the
                            // ClaimsHandle accordingly. Skipped
                            // when the gate is off.
                            if gate_on {
                                if let (Some(ch), Some(my_host), Some(sender)) = (
                                    gitback_claims_handle.as_ref(),
                                    local_hostname.as_ref(),
                                    sender_hostname.as_ref(),
                                ) {
                                    let now = crate::swarm::gitback::state::unix_seconds_now();
                                    for (label, hosts) in &result.gitback_claims {
                                        let in_list = hosts.iter().any(|h| h == my_host);
                                        ch.ingest_lan_claim(label, sender, in_list, now);
                                    }
                                }
                            }
                            // Ingest any GITBACKPROJECT entries —
                            // record (host, pid_hex, src_addr, handle,
                            // dr_scope, now) into the global
                            // ProjectPeers index for the helper-daemon
                            // fan-out hook and the ADR-023 announced-
                            // projects cache. `handle` / `dr_scope` are
                            // `None` from a peer still on the pre-0.49.0
                            // 2-field announce form.
                            if gate_on && !result.gitback_projects.is_empty() {
                                let now = now_unix_seconds();
                                let src_ip = src.ip();
                                for (host, pid_hex, handle, dr_scope) in &result.gitback_projects {
                                    crate::swarm::gitback::project_peers::record_global(
                                        pid_hex,
                                        host,
                                        Some(src_ip),
                                        handle.as_deref(),
                                        dr_scope.as_deref(),
                                        now,
                                    );
                                }
                            }
                            // Feed the hostdox host index. Not gated
                            // on any subsystem — the index is benign;
                            // the hostdox reactor decides whether to
                            // touch /etc/hosts. (HOST: lines are not
                            // signed-content-critical the way GITBACK*
                            // lines are; a spoofed one only mislabels
                            // an IP in a managed /etc/hosts block on a
                            // host whose operator opted into hostdox —
                            // same trust model as the LAN itself.)
                            if let Some(host) = &result.host_name {
                                crate::swarm::hostdox::record_host_global(
                                    host,
                                    src.ip(),
                                    now_unix_seconds(),
                                );
                            }
                            // Trigger manifest sync if a version mismatch was detected.
                            if let Some(addr) = result.manifest_sync_peer {
                                if let Some(ref agent) = shared_agent {
                                    let m = manifest.clone();
                                    let a = agent.clone();
                                    let pm = peer_map.clone();
                                    tokio::task::spawn_blocking(move || {
                                        sync_manifest_from_peer(&addr, &m, &a, &pm);
                                    });
                                }
                            }
                            // Resolve partial-blob sentinels via async /meta
                            // probes so the scheduler sees real chunk sets.
                            // Two filters before we spend a blocking thread:
                            //   1. skip if PeerMap has a fresh bitmap for
                            //      this (peer, sha) — the prober path or a
                            //      recent inline HAVE already got it.
                            //   2. pick up the stored ETag so the probe
                            //      can issue a conditional GET (304 → no
                            //      bitmap transfer).
                            for (peer_addr, sha) in result.bitmap_probes {
                                if !peer_map.should_probe(&sha, &peer_addr) {
                                    continue;
                                }
                                if let Some(ref agent) = shared_agent {
                                    let pm = peer_map.clone();
                                    let a = agent.clone();
                                    let etag = peer_map.stored_etag(&sha, &peer_addr);
                                    tokio::task::spawn_blocking(move || {
                                        probe_peer_bitmap(&peer_addr, &sha, &pm, &a, etag);
                                    });
                                }
                            }
                        }
                    }
                    Err(e) => {
                        // WouldBlock is expected for non-blocking sockets between polls.
                        if e.kind() != std::io::ErrorKind::WouldBlock {
                            debug!(error = %e, "multicast recv error");
                        }
                    }
                }
            }
        }
    }
}

/// Periodically announce our blob inventory and wanted blobs via multicast.
///
/// The loop selects across three inputs: cancellation, the scheduled next
/// send deadline, and an event-driven notify fired by the scheduler when
/// something interesting happens (first chunk written for a download,
/// download complete). The deadline is computed fresh each iteration from
/// `ANNOUNCE_INTERVAL_ACTIVE` if any download is in flight, otherwise
/// `ANNOUNCE_INTERVAL_IDLE`. A notify wake-up that arrives sooner than
/// `MIN_ANNOUNCE_GAP` since the last send is coalesced — we wait out the
/// remaining gap before sending so the event path can't flood the
/// network during a burst of first-chunk firings.
async fn announce_loop(
    cache: Arc<BlobCache>,
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    swarm_port: u16,
    cancel: tokio_util::sync::CancellationToken,
    announce: Arc<super::AnnounceSignal>,
    gitback_local_info: Option<GitbackLocalInfo>,
    gitback_pending_claims: Option<Arc<GitbackPendingClaims>>,
    gitback_gate: crate::swarm::gitback::Gate,
    gitback_signing_key: Option<Arc<ed25519_dalek::SigningKey>>,
) -> anyhow::Result<()> {
    let socket = Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::UDP))?;
    socket.set_multicast_ttl_v4(1)?; // LAN-local only
    socket.set_nonblocking(true)?;
    socket.bind(&SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, 0).into())?;
    let std_socket: std::net::UdpSocket = socket.into();
    let udp = tokio::net::UdpSocket::from_std(std_socket)?;

    let dest = SocketAddr::V4(SocketAddrV4::new(MULTICAST_ADDR, MULTICAST_PORT));

    // Determine our address for ADDR line.
    let local_ip = local_ipv4().unwrap_or(Ipv4Addr::LOCALHOST);

    info!(addr = %local_ip, port = swarm_port, "multicast announcer started");

    // Track the last send so the rate-limit floor can compute the
    // remaining delay for an early fire.
    let mut last_send = tokio::time::Instant::now()
        .checked_sub(ANNOUNCE_INTERVAL_IDLE)
        .unwrap_or_else(tokio::time::Instant::now);

    loop {
        // Pick interval based on whether a download is actively running.
        // Recomputed each iteration so the cadence tracks the real state
        // of the scheduler without needing a separate wake-up channel.
        let interval = if announce.has_active_downloads() {
            ANNOUNCE_INTERVAL_ACTIVE
        } else {
            ANNOUNCE_INTERVAL_IDLE
        };

        // Next scheduled wake-up — whichever is later of "interval since
        // last send" and "min_gap since last send". The min_gap clamp only
        // matters when interval < min_gap, which isn't the case today but
        // future-proofs against tighter cadences.
        let next_deadline = last_send + interval.max(MIN_ANNOUNCE_GAP);

        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            _ = tokio::time::sleep_until(next_deadline) => {}
            _ = announce.notified() => {
                // Event-driven fire. Honor the rate-limit floor: if the
                // last send was very recent, sleep out the remaining gap
                // before proceeding. This bounds the announce rate even
                // under a burst of scheduler firings.
                let since = last_send.elapsed();
                if since < MIN_ANNOUNCE_GAP {
                    tokio::time::sleep(MIN_ANNOUNCE_GAP - since).await;
                }
            }
        }

        // Single directory walk: partition .blobs/ into complete + partial
        // in one pass instead of calling read_dir twice. Both filtered by
        // the revocation set so we don't accidentally re-advertise a sha
        // the manifest CRDT has retired.
        let (complete_all, partials_all) = cache.list_blobs_and_partials();
        let complete: Vec<String> = complete_all
            .into_iter()
            .filter(|sha| !manifest.is_revoked(sha))
            .collect();
        let partials: Vec<String> = partials_all
            .into_iter()
            .filter(|sha| !manifest.is_revoked(sha))
            .collect();
        // The gitback gate may flip at runtime — re-read on every
        // iteration. When off, skip emitting the GITBACK and
        // GITBACKCLAIM lines; receivers will see the envoy drop
        // out of their peer tables on the next TTL cycle.
        let gate_on = gitback_gate.enabled();
        let announce_local_info = if gate_on { gitback_local_info.as_ref() } else { None };

        // Short-circuit only if we have nothing AT ALL to announce.
        // A pure gitback receiver (no swarm blobs cached yet) still
        // needs to announce its gitback peer info so source envoys
        // can pick it — otherwise we'd be invisible to the LAN peer
        // table until we happened to download something.
        if complete.is_empty() && partials.is_empty() && announce_local_info.is_none() {
            last_send = tokio::time::Instant::now();
            continue; // Nothing to announce.
        }

        // Snapshot the shared pending claims map before each send.
        // Decoupled from the reactor's cadence — the announcer
        // emits whatever the reactor has staged, without needing
        // a channel or lock coordination. Empty snapshot when the
        // gate is off so the announcement carries no GITBACKCLAIM
        // lines.
        let claim_snapshot: Vec<(String, Vec<String>)> = if gate_on {
            gitback_pending_claims
                .as_ref()
                .map(|pc| pc.snapshot())
                .unwrap_or_default()
        } else {
            Vec::new()
        };

        // Sign announcements only when the gitback gate is on — an
        // envoy with gitback disabled emits no GITBACK/GITBACKCLAIM lines,
        // so there's nothing for a SIG to cover that matters.
        let sign_for_this_tick = if gate_on {
            gitback_signing_key.as_deref()
        } else {
            None
        };
        // Enumerate locally-held projects under every user's
        // ~/.vigo-gitback/, as (pid_hex, handle, dr_scope). Cheap
        // (filesystem stat + a small JSON read per project); read once
        // per announce tick. Empty when the gate is off so a dormant
        // gitback subsystem advertises no projects.
        let project_snapshot: Vec<(String, String, String)> = if gate_on {
            crate::swarm::gitback::project_peers::enumerate_local_projects()
        } else {
            Vec::new()
        };

        let packets = build_announcement(
            &complete,
            &partials,
            &cache,
            &manifest,
            local_ip,
            swarm_port,
            announce_local_info,
            &claim_snapshot,
            &project_snapshot,
            sign_for_this_tick,
            now_unix_seconds(),
        );
        for msg in &packets {
            if let Err(e) = udp.send_to(msg.as_bytes(), dest).await {
                debug!(error = %e, "multicast announce failed");
            }
        }
        last_send = tokio::time::Instant::now();

        // Periodic GC of expired peer entries. Only persist to disk if
        // something actually changed since the last save — saves the
        // serialize + write cost on idle cycles where nothing has
        // mutated. The dirty flag is cleared atomically by take_dirty
        // so exactly one announcer cycle is responsible for each persist.
        peer_map.gc();
        if peer_map.take_dirty() {
            let save_path = crate::platform::paths::default_blobs_dir()
                .parent()
                .map(|p| p.join("peers.json"));
            if let Some(path) = save_path {
                peer_map.save(&path);
            }
        }
    }
}

/// Build a multicast announcement message.
/// Append an Ed25519 signature line covering the current packet body.
/// Shape: `SIG:<unix_seconds>:<hex-encoded-64-byte-signature>\n`. The
/// signed payload is the exact bytes of the packet up to (but not
/// including) the SIG line. Timestamp is folded into the signature per
/// `auth::signer::sign_payload` so a stale announcement can't be
/// replayed against a receiver that observed it already.
fn sign_and_append(msg: &mut String, key: &ed25519_dalek::SigningKey, timestamp_secs: i64) {
    let ts_string = timestamp_secs.to_string();
    let sig = crate::auth::signer::sign_payload(msg.as_bytes(), &ts_string, key);
    msg.push_str(&format!("SIG:{}:{}\n", ts_string, hex::encode(sig)));
}

/// Extract the SIG line from a multicast packet, if present. Returns
/// `(body_without_sig, timestamp_string, signature_bytes)`. Unsigned
/// packets return `None` — caller decides how to treat them (current
/// policy: drop gitback_peer + gitback_claims; swarm HAVE/WANT still
/// honored so blob discovery isn't held hostage to sig rollout).
pub fn extract_sig(packet: &str) -> Option<(&str, String, Vec<u8>)> {
    // SIG line is the last non-empty line of the packet. Locate it by
    // scanning backwards — faster than reparsing line-by-line.
    let trimmed = packet.trim_end_matches('\n');
    let sig_start = trimmed.rfind("\nSIG:")?;
    let sig_line = &trimmed[sig_start + 1..];
    let rest = sig_line.strip_prefix("SIG:")?;
    let (ts, sig_hex) = rest.split_once(':')?;
    let sig_bytes = hex::decode(sig_hex).ok()?;
    // Body is everything before the `\nSIG:` separator, PLUS the
    // trailing newline that preceded SIG (the signer included it when
    // it appended SIG, so the verifier must include it too).
    Some((&packet[..=sig_start], ts.to_string(), sig_bytes))
}

/// Verify an incoming packet's gitback authenticity: SIG line present,
/// signature valid under the GITBACK line's embedded pubkey, and that
/// pubkey matches the TOFU cache for the sender's hostname. Returns
/// true iff gitback content should be trusted. Packets without a GITBACK
/// line pass through (nothing to verify). Packets with GITBACK but no
/// valid sig return false and the caller strips gitback state.
fn verify_gitback_authenticity(
    raw_packet: &str,
    result: &AnnouncementResult,
    cache: &std::sync::Arc<crate::swarm::gitback::peer_pubkey_cache::PeerPubkeyCache>,
) -> bool {
    use crate::swarm::gitback::peer_pubkey_cache::CheckOutcome;
    let peer = match result.gitback_peer.as_ref() {
        Some(p) => p,
        None => return true, // no gitback content to verify
    };
    if peer.pubkey_hex.is_empty() {
        warn!(hostname = %peer.hostname, "gitback: announcement missing embedded pubkey, dropping gitback content");
        return false;
    }

    // Parse SIG line (if any) + isolate the signed body.
    let (body, ts_string, sig_bytes) = match extract_sig(raw_packet) {
        Some(parts) => parts,
        None => {
            warn!(hostname = %peer.hostname, "gitback: announcement missing SIG line, dropping gitback content");
            return false;
        }
    };

    // Decode pubkey_hex into a VerifyingKey.
    let pub_bytes = match hex::decode(&peer.pubkey_hex) {
        Ok(b) if b.len() == 32 => b,
        _ => {
            warn!(hostname = %peer.hostname, "gitback: embedded pubkey malformed, dropping gitback content");
            return false;
        }
    };
    let pub_array: [u8; 32] = pub_bytes.try_into().expect("len checked");
    let verifying_key = match ed25519_dalek::VerifyingKey::from_bytes(&pub_array) {
        Ok(k) => k,
        Err(_) => {
            warn!(hostname = %peer.hostname, "gitback: embedded pubkey invalid, dropping gitback content");
            return false;
        }
    };

    if !crate::auth::signer::verify_payload(body.as_bytes(), &ts_string, &sig_bytes, &verifying_key) {
        warn!(hostname = %peer.hostname, "gitback: SIG verification failed, dropping gitback content");
        return false;
    }

    // TOFU cache cross-check. First sighting: cache the pubkey and
    // accept. Match: refresh last-seen. Mismatch: reject — the
    // hostname is announcing with a different key than we've trusted
    // before, either a re-enrollment (admin clears the cache) or a
    // spoof (we never accept silently).
    match cache.check(&peer.hostname, &peer.pubkey_hex) {
        CheckOutcome::FirstSighting | CheckOutcome::Match => {
            cache.accept(&peer.hostname, &peer.pubkey_hex, now_unix_seconds());
            true
        }
        CheckOutcome::Mismatch { cached_prefix } => {
            let cache_path = crate::swarm::gitback::peer_pubkey_cache::cache_path();
            warn!(
                hostname = %peer.hostname,
                announced_pubkey_prefix = %peer.pubkey_hex.chars().take(12).collect::<String>(),
                cached_pubkey_prefix = %cached_prefix,
                "gitback: pubkey mismatch — dropping gitback content. Clear {} if the hostname was legitimately re-enrolled.",
                cache_path.display(),
            );
            false
        }
    }
}

fn now_unix_seconds() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

/// This envoy's hostname for the `HOST:` announce line. `gethostname(2)`;
/// `None` when it's empty or non-UTF-8 — in which case the line is just
/// omitted and receivers fall back to DNS for this envoy.
fn announce_hostname() -> Option<String> {
    hostname::get()
        .ok()
        .and_then(|h| h.into_string().ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Build the multicast announcement(s) for one cycle. Returns one packet
/// in the common case, or two when gitback content would crowd out swarm
/// HAVE/WANT lines.
///
/// Priority inversion fix (0.30.10): earlier revisions appended
/// GITBACK/GITBACKCLAIM last so they'd be the first thing dropped on MTU
/// overflow. That silently broke gitback on any envoy with more than
/// ~15 cached swarm blobs — HAVE lines would fill the packet and the
/// coordination announcements never went out. Gitback content is both
/// low-bandwidth (a few labels × short hostname lists) and
/// high-criticality (receivers fire on GITBACKCLAIM, nothing fires without
/// it), while HAVE content is high-bandwidth and self-healing (the next
/// 5s cycle retries). Pack gitback first; let HAVE spill across cycles.
///
/// Large-fleet edge case: ~60+ gitback labels on one source envoy could
/// push GITBACK+GITBACKCLAIM past MTU on its own. Split into a second
/// packet in that case — receivers tolerate either packet shape
/// (every section is optional past HEADER+MANIFEST+ADDR).
#[allow(clippy::too_many_arguments)]
fn build_announcement(
    complete: &[String],
    partials: &[String],
    cache: &BlobCache,
    manifest: &Manifest,
    local_ip: Ipv4Addr,
    port: u16,
    gitback_local_info: Option<&GitbackLocalInfo>,
    gitback_claims: &[(String, Vec<String>)],
    // Projects this envoy currently holds per-user state for, as
    // (pid_hex, handle, dr_scope). One ADR-023 4-field
    // GITBACKPROJECT:<host>:<pid_hex>:<handle>:<dr_scope> line emitted
    // per project; receivers populate ProjectPeers from these. Empty
    // slice in test fixtures + envoys with the gate off.
    gitback_projects: &[(String, String, String)],
    signing_key: Option<&ed25519_dalek::SigningKey>,
    timestamp_secs: i64,
) -> Vec<String> {
    let manifest_line = format!("MANIFEST:{}\n", manifest.version());
    let addr_line = format!("ADDR:{}:{}\n", local_ip, port);

    // Primary packet. Preamble + GITBACK + as many GITBACKCLAIM as fit under
    // a reserved budget (leave ≥300 bytes for MANIFEST/ADDR/one HAVE).
    // MAX_CONTENT_SIZE already reserves space for the SIG footer, so a
    // packet that fills this budget still fits in MAX_MSG_SIZE after
    // sign_and_append.
    let gitback_budget_limit = MAX_CONTENT_SIZE - 300;
    let mut msg = String::from(HEADER);
    msg.push('\n');
    msg.push_str(&manifest_line);

    if let Some(li) = gitback_local_info {
        let line = format!("{}\n", li.format_announce_line());
        if msg.len() + line.len() <= gitback_budget_limit {
            msg.push_str(&line);
        }
    }

    let mut overflow_claims: Vec<(String, Vec<String>)> = Vec::new();
    for (label, hosts) in gitback_claims {
        let line = format!("{}\n", format_claim_line(label, hosts));
        if msg.len() + line.len() > gitback_budget_limit {
            overflow_claims.push((label.clone(), hosts.clone()));
            continue;
        }
        msg.push_str(&line);
    }

    // GITBACKPROJECT lines (ADR-015 step 4 LAN auto-discovery).
    // One line per locally-held project. Same overflow pattern as
    // GITBACKCLAIM — anything that doesn't fit gets bumped into a
    // spillover packet so a many-project envoy still announces every
    // project within the same 30 s tick.
    let mut overflow_projects: Vec<(String, String, String)> = Vec::new();
    if let Some(li) = gitback_local_info {
        for (pid_hex, handle, dr_scope) in gitback_projects {
            let line = format!(
                "{}\n",
                crate::swarm::gitback::project_peers::format_project_line(
                    &li.hostname,
                    pid_hex,
                    handle,
                    dr_scope,
                )
            );
            if msg.len() + line.len() > gitback_budget_limit {
                overflow_projects.push((pid_hex.clone(), handle.clone(), dr_scope.clone()));
                continue;
            }
            msg.push_str(&line);
        }
    }

    // Swarm blob lines, appended after gitback. Same overflow break pattern
    // as before — losing a HAVE line is safe, the next cycle retries and
    // peers still see the blob via other paths.
    for sha in complete {
        let path = cache.dir().join(sha);
        let size = std::fs::metadata(&path).map(|m| m.len()).unwrap_or(0);
        let total = chunks_for_size(size);
        let line = format!("HAVE:{}:{}/{}\n", sha, total, total);
        if msg.len() + line.len() + 30 > MAX_CONTENT_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    for sha in partials {
        if cache.has_blob(sha) {
            continue;
        }
        let chunks = cache.partial_chunks(sha);
        if chunks.is_empty() {
            continue;
        }
        let have = chunks.len() as u32;
        let total = manifest
            .get_entry(sha)
            .map(|e| chunks_for_size(e.size))
            .unwrap_or(0);
        let line = if total > 0 && bitmap::encoded_len(total) > 0 {
            let encoded = bitmap::encode(&chunks, total);
            let inline = format!("HAVE:{}:{}/{}:{}\n", sha, have, total, encoded);
            if msg.len() + inline.len() + 30 <= MAX_CONTENT_SIZE {
                inline
            } else {
                format!("HAVE:{}:{}/{}\n", sha, have, total)
            }
        } else {
            format!("HAVE:{}:{}/0\n", sha, have)
        };
        if msg.len() + line.len() + 30 > MAX_CONTENT_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    for sha in manifest.active_blobs() {
        if complete.contains(&sha) || partials.iter().any(|p| p == &sha) {
            continue;
        }
        let line = format!("WANT:{}\n", sha);
        if msg.len() + line.len() + 30 > MAX_CONTENT_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    msg.push_str(&addr_line);
    // HOST:<hostname> — the sender's hostname, so receivers can build a
    // fleet `hostname → IP` index for hostdox (`/etc/hosts` block).
    // Always emitted (independent of any subsystem gate); tiny, sits in
    // the always-present tail next to ADDR.
    if let Some(h) = announce_hostname() {
        msg.push_str(&format!("HOST:{}\n", h));
    }
    if let Some(key) = signing_key {
        sign_and_append(&mut msg, key, timestamp_secs);
    }
    let mut packets = vec![msg];

    // Spillover packet for the rare case where GITBACK+GITBACKCLAIM
    // / GITBACKPROJECT alone exceed the gitback budget. Carries
    // only the lines that didn't fit, plus the minimum envelope
    // (HEADER + MANIFEST + GITBACK + ADDR) so the receiver can
    // identify the sender and ingest claims/projects.
    if !overflow_claims.is_empty() || !overflow_projects.is_empty() {
        let mut spill = String::from(HEADER);
        spill.push('\n');
        spill.push_str(&manifest_line);
        if let Some(li) = gitback_local_info {
            spill.push_str(&format!("{}\n", li.format_announce_line()));
        }
        let mut hard_overflow_claims = 0usize;
        for (label, hosts) in &overflow_claims {
            let line = format!("{}\n", format_claim_line(label, hosts));
            // Reserve room for ADDR at the tail. MAX_CONTENT_SIZE leaves
            // room for the SIG footer that will be appended below.
            if spill.len() + line.len() + addr_line.len() > MAX_CONTENT_SIZE {
                hard_overflow_claims += 1;
                continue;
            }
            spill.push_str(&line);
        }
        let mut hard_overflow_projects = 0usize;
        if let Some(li) = gitback_local_info {
            for (pid_hex, handle, dr_scope) in &overflow_projects {
                let line = format!(
                    "{}\n",
                    crate::swarm::gitback::project_peers::format_project_line(
                        &li.hostname,
                        pid_hex,
                        handle,
                        dr_scope,
                    )
                );
                if spill.len() + line.len() + addr_line.len() > MAX_CONTENT_SIZE {
                    hard_overflow_projects += 1;
                    continue;
                }
                spill.push_str(&line);
            }
        }
        spill.push_str(&addr_line);
        if let Some(key) = signing_key {
            sign_and_append(&mut spill, key, timestamp_secs);
        }
        packets.push(spill);
        if hard_overflow_claims > 0 {
            warn!(
                overflow_labels = hard_overflow_claims,
                total_labels = gitback_claims.len(),
                "gitback claim list exceeds two-packet capacity — some labels deferred to next cycle"
            );
        }
        if hard_overflow_projects > 0 {
            warn!(
                overflow_projects = hard_overflow_projects,
                total_projects = gitback_projects.len(),
                "gitback project list exceeds two-packet capacity — some projects deferred to next cycle"
            );
        }
    }

    packets
}

/// Result of parsing a multicast announcement.
#[derive(Debug, Default)]
struct AnnouncementResult {
    /// Peer address to trigger a manifest-sync pull against (set when the
    /// announcer's manifest version differs from ours).
    manifest_sync_peer: Option<String>,
    /// (peer_addr, sha256) pairs where the peer announced a partial blob
    /// via the sentinel `HAVE:<sha>:<have>/<total>` form and we need to
    /// pull `/blob/<sha>/meta` to discover which chunks it actually has.
    bitmap_probes: Vec<(String, String)>,
    /// Parsed `GITBACK:` line if the announcer included one. Used by
    /// the listener to refresh the gitback peer table. Absence means
    /// the announcer is not gitback-enabled (dormant subsystem from
    /// Phase 2's hostcrate gate) — the peer table stays untouched.
    gitback_peer: Option<GitbackLocalInfo>,
    /// Parsed `GITBACKCLAIM:` lines. Each entry is a (label,
    /// host_list) pair naming which envoys the announcer has
    /// selected to mirror that label. The listener pairs each
    /// entry with the sender's hostname (from the `gitback_peer`
    /// field above) and calls `ClaimsHandle::ingest_lan_claim`.
    /// Malformed lines are dropped silently.
    gitback_claims: Vec<(String, Vec<String>)>,
    /// Parsed `GITBACKPROJECT:` lines. Each entry is
    /// `(host, project_id_hex, handle, dr_scope)`; `handle` /
    /// `dr_scope` are `None` from a peer still on the pre-0.49.0
    /// 2-field form. The listener calls
    /// [`crate::swarm::gitback::project_peers::record_global`]
    /// per entry to populate the project-keyed peer index.
    gitback_projects: Vec<(String, String, Option<String>, Option<String>)>,
    /// Parsed `HOST:<hostname>` line, if present — the sender's
    /// hostname. The listener pairs it with the datagram source IP
    /// and feeds `crate::swarm::hostdox::record_host_global` to
    /// build the fleet `hostname → IP` index hostdox renders into
    /// `/etc/hosts`. `None` from a sender on an older agent.
    host_name: Option<String>,
}

/// Parse a received multicast announcement and update the peer map.
/// Complete blobs are recorded directly; partial blobs are deferred to an
/// async `/meta` probe (callers should spawn one per entry in
/// `bitmap_probes`). Never synthesizes a placeholder chunk set from a
/// partial announcement — the peer's real bitmap is some arbitrary
/// subset, not the first `have` chunks, and a placeholder would mislead
/// the scheduler into requesting chunks the peer doesn't have.
fn parse_announcement(msg: &str, _src: SocketAddr, peer_map: &PeerMap, manifest: &Manifest) -> AnnouncementResult {
    let mut result = AnnouncementResult::default();
    let mut lines = msg.lines();

    // Validate header.
    match lines.next() {
        Some(h) if h == HEADER => {}
        _ => return result,
    }

    let mut addr: Option<String> = None;
    // Blobs whose chunk set we can report to the PeerMap without a /meta
    // probe — either complete (we synthesized 0..total) or partial with an
    // inlined, decoded bitmap.
    let mut resolved_blobs: Vec<(String, HashSet<u32>)> = Vec::new();
    let mut partial_shas: Vec<String> = Vec::new();
    let mut remote_manifest_version: Option<u64> = None;

    for line in lines {
        if let Some(ver_str) = line.strip_prefix("MANIFEST:") {
            remote_manifest_version = ver_str.parse().ok();
            continue;
        }
        if let Some(rest) = line.strip_prefix("HAVE:") {
            // Line forms:
            //   HAVE:<sha>:<have>/<total>                  (legacy / oversized → probe)
            //   HAVE:<sha>:<have>/<total>:<bitmap-b64>     (inline bitmap — no probe)
            //   HAVE:<sha>:<have>/0                        (legacy sentinel → probe)
            let mut parts = rest.splitn(3, ':');
            let sha = match parts.next() {
                Some(s) if !s.is_empty() => s,
                _ => continue,
            };
            let counts = match parts.next() {
                Some(c) => c,
                None => continue,
            };
            let bitmap_b64 = parts.next();
            if let Some((have_str, total_str)) = counts.split_once('/') {
                if let (Ok(have), Ok(total)) = (have_str.parse::<u32>(), total_str.parse::<u32>()) {
                    if total > 0 && have >= total {
                        // Complete blob — chunk set is known without a probe.
                        resolved_blobs.push((sha.to_string(), (0..total).collect()));
                    } else if have > 0 {
                        // Partial. If the announcer inlined the bitmap and
                        // it decodes cleanly against the announced total,
                        // we can populate the PeerMap directly and skip
                        // the /meta probe. Otherwise defer.
                        let decoded = match (bitmap_b64, total) {
                            (Some(b64), t) if t > 0 => bitmap::decode(b64, t),
                            _ => None,
                        };
                        match decoded {
                            Some(chunks) if !chunks.is_empty() => {
                                resolved_blobs.push((sha.to_string(), chunks));
                            }
                            _ => partial_shas.push(sha.to_string()),
                        }
                    }
                    // have == 0 → nothing useful to do
                }
            }
        } else if let Some(rest) = line.strip_prefix("ADDR:") {
            addr = Some(rest.to_string());
        } else if let Some(rest) = line.strip_prefix("HOST:") {
            // Sender's hostname for the hostdox fleet `hostname → IP`
            // index. The IP comes from the datagram source, not this
            // line. Last one wins (a packet only carries one).
            let h = rest.trim();
            if !h.is_empty() {
                result.host_name = Some(h.to_string());
            }
        } else if line.starts_with("GITBACKCLAIM:") {
            // Order matters: GITBACKCLAIM: must be tried before
            // GITBACK: because both share the GITBACK prefix.
            if let Some((label, hosts)) = parse_claim_line(line) {
                result.gitback_claims.push((label, hosts));
            }
        } else if line.starts_with("GITBACKPROJECT:") {
            // ADR-015 step 4 LAN auto-discovery (extended by ADR-023
            // to carry the handle + dr_scope) — record into the global
            // ProjectPeers index so the helper-daemon fan-out hook can
            // find peers without explicit cross_subnet_members config,
            // and so `puddle unlock` can auto-adopt founded projects.
            // Order matters: must be tried before plain GITBACK:
            // because both share the GITBACK prefix.
            if let Some((host, pid_hex, handle, dr_scope)) =
                crate::swarm::gitback::project_peers::parse_project_line(line)
            {
                result
                    .gitback_projects
                    .push((host, pid_hex, handle, dr_scope));
            }
        } else if line.starts_with("GITBACK:") {
            // Parse and stash for listener ingest. Malformed lines
            // are dropped silently by parse_announce_line.
            if let Some(li) = GitbackLocalInfo::parse_announce_line(line) {
                result.gitback_peer = Some(li);
            }
        }
    }

    if let Some(peer_addr) = addr {
        for (sha, chunks) in resolved_blobs {
            peer_map.report(&sha, &peer_addr, chunks, PeerSource::Multicast);
        }
        for sha in partial_shas {
            result.bitmap_probes.push((peer_addr.clone(), sha));
        }

        // If the peer has a different manifest version, signal the caller
        // to trigger a manifest sync over HTTPS.
        if let Some(remote_ver) = remote_manifest_version {
            let local_ver = manifest.version();
            if remote_ver != local_ver {
                debug!(
                    peer = %peer_addr,
                    local_ver,
                    remote_ver,
                    "manifest version mismatch — triggering sync"
                );
                result.manifest_sync_peer = Some(peer_addr);
            }
        }
    }
    result
}

/// Probe a peer's `/blob/<sha>/meta` endpoint to discover which chunks it
/// has, and report the real bitmap to the peer map. Called on a blocking
/// thread when the multicast listener sees a partial-blob sentinel and
/// needs to resolve the actual chunks.
///
/// Takes a shared `Arc<ureq::Agent>` built once at discovery spawn time so
/// we don't re-parse TLS certs on every probe. Issues a conditional GET
/// using the stored ETag when available — unchanged peers respond with
/// 304 Not Modified and no body, and we just refresh `bitmap_refreshed_at`
/// via `touch_peer`.
fn probe_peer_bitmap(
    peer_addr: &str,
    sha: &str,
    peer_map: &PeerMap,
    agent: &Arc<ureq::Agent>,
    etag: Option<String>,
) {
    match peer_client::fetch_meta_conditional(agent, peer_addr, sha, etag.as_deref()) {
        Ok(peer_client::MetaResult::Fresh { meta, etag: new_etag }) => {
            let chunks: HashSet<u32> = meta.chunks.into_iter().collect();
            let count = chunks.len();
            peer_map.report_meta_probe(sha, peer_addr, chunks, new_etag);
            debug!(
                peer = peer_addr,
                sha = &sha[..12.min(sha.len())],
                chunk_count = count,
                "bitmap probe resolved partial sentinel"
            );
        }
        Ok(peer_client::MetaResult::NotModified) => {
            // Peer's bitmap hasn't changed since our last probe — no
            // need to touch the chunk set. Just refresh last_seen +
            // bitmap_refreshed_at so the next cooldown check skips us.
            peer_map.touch_peer(sha, peer_addr);
            debug!(
                peer = peer_addr,
                sha = &sha[..12.min(sha.len())],
                "bitmap probe 304 — peer inventory unchanged"
            );
        }
        Err(e) => {
            debug!(
                error = %e,
                peer = peer_addr,
                sha = &sha[..12.min(sha.len())],
                "bitmap probe failed"
            );
        }
    }
}

/// Sync manifest from a peer over mTLS HTTPS. Called on a blocking thread
/// when multicast announces a different manifest version. Takes the shared
/// peer agent so this also avoids the per-call TLS rebuild.
fn sync_manifest_from_peer(peer_addr: &str, manifest: &Manifest, agent: &Arc<ureq::Agent>, peer_map: &Arc<PeerMap>) {
    match peer_client::fetch_manifest(agent, peer_addr) {
        Ok(remote) => {
            let changed = manifest.merge(&remote);
            if changed {
                info!(
                    peer = peer_addr,
                    version = manifest.version(),
                    "manifest synced from peer"
                );
                // Evict newly revoked blobs from cache + clean up symlinks
                // and flush stale PeerMap entries so re-seeding the same SHA
                // doesn't hit 404s from peers that already evicted the blob.
                super::evict_revoked_blobs(manifest, Some(peer_map));
            }
        }
        Err(e) => {
            debug!(peer = peer_addr, error = %e, "manifest sync failed");
        }
    }
}

/// Create and bind a UDP socket joined to the multicast group.
fn create_multicast_socket() -> anyhow::Result<Socket> {
    let socket = Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::UDP))?;
    socket.set_reuse_address(true)?;

    // SO_REUSEPORT where available (Linux, macOS, BSD).
    #[cfg(not(windows))]
    socket.set_reuse_port(true)?;

    socket.bind(&SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, MULTICAST_PORT).into())?;
    socket.join_multicast_v4(&MULTICAST_ADDR, &Ipv4Addr::UNSPECIFIED)?;

    Ok(socket)
}

/// Get the local IPv4 address, preferring real LAN interfaces over tunnels/VPNs.
///
/// Reads /sys/class/net/ to enumerate interfaces, skips virtual/tunnel ones
/// (tun, tap, tailscale, docker, veth, br-, virbr, wg, lo), then picks the
/// first interface with a valid IPv4 address by attempting a UDP bind.
/// Falls back to the default-route trick (connect to 8.8.8.8) if no real
/// interface is found.
pub(crate) fn local_ipv4() -> Option<Ipv4Addr> {
    // Try real interfaces first (Linux sysfs).
    if let Some(ip) = local_ipv4_from_real_interface() {
        return Some(ip);
    }
    // Fallback: default route (may pick a tunnel, but better than nothing).
    local_ipv4_default_route()
}

/// Enumerate physical/real network interfaces via sysfs and return the first
/// IPv4 address found. Skips loopback and virtual/tunnel interfaces.
#[cfg(target_os = "linux")]
fn local_ipv4_from_real_interface() -> Option<Ipv4Addr> {
    let entries = std::fs::read_dir("/sys/class/net/").ok()?;
    let mut candidates = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().to_string();
        if is_virtual_interface(&name) {
            continue;
        }
        // Check the interface is up by reading operstate.
        let operstate_path = format!("/sys/class/net/{}/operstate", name);
        if let Ok(state) = std::fs::read_to_string(&operstate_path) {
            if state.trim() != "up" {
                continue;
            }
        }
        candidates.push(name);
    }
    // Try each real, up interface: bind a UDP socket and see what IP it gets.
    for iface in &candidates {
        // Read the interface's IPv4 address from /proc/net/fib_trie or use
        // SO_BINDTODEVICE. Simpler: parse ip addr output for this interface.
        // Simplest: read /sys/class/net/<iface>/address is MAC only.
        // Use getifaddrs via libc.
        if let Some(ip) = ipv4_for_interface(iface) {
            if !ip.is_loopback() && !ip.is_link_local() {
                return Some(ip);
            }
        }
    }
    None
}

#[cfg(not(target_os = "linux"))]
fn local_ipv4_from_real_interface() -> Option<Ipv4Addr> {
    // macOS/BSD/Windows: fall through to default route.
    None
}

/// Check if an interface name is virtual/tunnel and should be skipped.
fn is_virtual_interface(name: &str) -> bool {
    // Loopback.
    if name == "lo" {
        return true;
    }
    // Tunnel/VPN interfaces.
    let prefixes = ["tun", "tap", "tailscale", "docker", "veth", "br-", "virbr", "wg", "cni", "flannel", "calico"];
    for prefix in &prefixes {
        if name.starts_with(prefix) {
            return true;
        }
    }
    false
}

/// Get the IPv4 address for a specific interface using getifaddrs (Unix).
#[cfg(unix)]
fn ipv4_for_interface(iface: &str) -> Option<Ipv4Addr> {
    // Read /proc/net/fib_trie is complex. Use libc::getifaddrs instead.
    use std::ffi::CStr;
    use std::ptr;

    unsafe {
        let mut ifaddrs: *mut libc::ifaddrs = ptr::null_mut();
        if libc::getifaddrs(&mut ifaddrs) != 0 {
            return None;
        }
        let mut cursor = ifaddrs;
        let mut result = None;
        while !cursor.is_null() {
            let ifa = &*cursor;
            if !ifa.ifa_addr.is_null() && (*ifa.ifa_addr).sa_family == libc::AF_INET as u16 {
                let name_cstr = CStr::from_ptr(ifa.ifa_name);
                if let Ok(name_str) = name_cstr.to_str() {
                    if name_str == iface {
                        let sockaddr_in = ifa.ifa_addr as *const libc::sockaddr_in;
                        let ip_bytes = (*sockaddr_in).sin_addr.s_addr.to_ne_bytes();
                        result = Some(Ipv4Addr::new(ip_bytes[0], ip_bytes[1], ip_bytes[2], ip_bytes[3]));
                        break;
                    }
                }
            }
            cursor = ifa.ifa_next;
        }
        libc::freeifaddrs(ifaddrs);
        result
    }
}

#[cfg(not(unix))]
fn ipv4_for_interface(_iface: &str) -> Option<Ipv4Addr> {
    None
}

/// Fallback: determine local IP via default route (connect UDP to 8.8.8.8).
fn local_ipv4_default_route() -> Option<Ipv4Addr> {
    use std::net::UdpSocket;
    let sock = UdpSocket::bind("0.0.0.0:0").ok()?;
    sock.connect("8.8.8.8:80").ok()?;
    match sock.local_addr().ok()? {
        SocketAddr::V4(a) => Some(*a.ip()),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::bitmap as bitmap_mod;
    use crate::swarm::meta::CHUNK_SIZE;

    fn test_manifest() -> Arc<Manifest> {
        let dir = tempfile::tempdir().unwrap();
        // Leak the tempdir so it lives for the test duration.
        let path = dir.keep();
        Manifest::load(&path)
    }

    #[test]
    fn test_build_announcement() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let manifest = test_manifest();

        // Write a small complete blob.
        let data = b"test data";
        use sha2::Digest;
        let sha = hex::encode(sha2::Sha256::digest(data));
        cache.write_chunk(&sha, 0, data).unwrap();
        cache.assemble(&sha, data.len() as u64, 1).unwrap();

        let packets = build_announcement(&cache.list_blobs(), &cache.list_partials(), &cache, &manifest, Ipv4Addr::new(10, 0, 0, 1), 1531, None, &[], &[], None, 0);
        let msg = &packets[0];
        assert!(msg.starts_with("VIGO-SWARM\n"));
        assert!(msg.contains("MANIFEST:"));
        assert!(msg.contains(&format!("HAVE:{}:1/1", sha)));
        assert!(msg.contains("ADDR:10.0.0.1:1531"));
    }

    #[test]
    fn test_parse_announcement_partial_defers_to_probe() {
        // Regression for the placeholder bug: a partial HAVE line must NOT
        // synthesize (0..have) chunks in the PeerMap. Instead it should
        // return a pending /meta probe so the async resolver can fetch the
        // real bitmap. Before the fix, the scheduler would try to download
        // chunks 0..have from the peer and get 404s for the ones the peer
        // didn't actually have.
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "a".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nMANIFEST:1\nHAVE:{}:5/10\nADDR:10.0.0.2:1531\n",
            sha
        );

        let result = parse_announcement(
            &msg,
            "10.0.0.2:1531".parse().unwrap(),
            &pm,
            &manifest,
        );

        // Manifest version 1 vs our 0 → should signal sync needed.
        assert!(result.manifest_sync_peer.is_some());

        // Critical: no peer added for this sha. The scheduler must not be
        // told the peer has chunks {0,1,2,3,4} when we don't know which
        // five chunks it actually has.
        assert_eq!(pm.peers_for(&sha).len(), 0, "partial announcement must not populate PeerMap");

        // A probe must be queued for the async /meta fetch.
        assert_eq!(result.bitmap_probes.len(), 1);
        assert_eq!(result.bitmap_probes[0], ("10.0.0.2:1531".to_string(), sha));
    }

    #[test]
    fn test_parse_announcement_extracts_host_name() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        // With a HOST: line.
        let msg = "VIGO-SWARM\nMANIFEST:0\nADDR:192.168.1.2:1531\nHOST:danlap\n";
        let result = parse_announcement(msg, "192.168.1.2:1531".parse().unwrap(), &pm, &manifest);
        assert_eq!(result.host_name.as_deref(), Some("danlap"));
        // Without one (older agent) → None.
        let msg = "VIGO-SWARM\nMANIFEST:0\nADDR:192.168.1.2:1531\n";
        let result = parse_announcement(msg, "192.168.1.2:1531".parse().unwrap(), &pm, &manifest);
        assert!(result.host_name.is_none());
        // build_announcement always emits a HOST: line for this envoy.
        let cache_dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(cache_dir.path().to_path_buf()).unwrap();
        let pkts = build_announcement(
            &[], &[], &cache, &manifest, "192.168.1.5".parse().unwrap(), 1531,
            None, &[], &[], None, 0,
        );
        assert!(pkts.iter().any(|p| p.contains("HOST:")), "announcement must carry a HOST: line");
    }

    #[test]
    fn test_parse_announcement_sentinel_total_zero_defers_to_probe() {
        // The /0 sentinel form: peer knows it has some chunks but doesn't
        // know (or won't emit) the total. Same deferred-probe behavior.
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "c".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:3/0\nADDR:10.0.0.4:1531\n",
            sha
        );

        let result = parse_announcement(
            &msg,
            "10.0.0.4:1531".parse().unwrap(),
            &pm,
            &manifest,
        );

        assert_eq!(pm.peers_for(&sha).len(), 0);
        assert_eq!(result.bitmap_probes.len(), 1);
        assert_eq!(result.bitmap_probes[0].1, sha);
    }

    #[test]
    fn test_parse_announcement_have_zero_is_ignored() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "d".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:0/10\nADDR:10.0.0.5:1531\n",
            sha
        );

        let result = parse_announcement(
            &msg,
            "10.0.0.5:1531".parse().unwrap(),
            &pm,
            &manifest,
        );

        assert_eq!(pm.peers_for(&sha).len(), 0);
        assert_eq!(result.bitmap_probes.len(), 0);
    }

    #[test]
    fn test_parse_invalid_header() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            "NOT-VIGO\nHAVE:abc:1/1\nADDR:10.0.0.1:1531\n",
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        // Should be ignored — no peers added, no probes queued, no sync.
        assert_eq!(pm.len(), 0);
        assert!(result.manifest_sync_peer.is_none());
        assert_eq!(result.bitmap_probes.len(), 0);
    }

    #[test]
    fn test_parse_complete_blob() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "b".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:10/10\nADDR:10.0.0.3:1531\n",
            sha
        );

        let result = parse_announcement(&msg, "10.0.0.3:1531".parse().unwrap(), &pm, &manifest);

        // Complete blob → populated directly, no probe needed.
        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks.len(), 10);
        assert_eq!(result.bitmap_probes.len(), 0);
    }

    #[test]
    fn test_parse_mixed_complete_and_partial() {
        // A single announcement with both a complete blob and a partial
        // blob should populate the complete one immediately and queue a
        // probe for the partial one.
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let complete_sha = "e".repeat(64);
        let partial_sha = "f".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:10/10\nHAVE:{}:3/10\nADDR:10.0.0.6:1531\n",
            complete_sha, partial_sha
        );

        let result = parse_announcement(&msg, "10.0.0.6:1531".parse().unwrap(), &pm, &manifest);

        assert_eq!(pm.peers_for(&complete_sha).len(), 1);
        assert_eq!(pm.peers_for(&partial_sha).len(), 0);
        assert_eq!(result.bitmap_probes.len(), 1);
        assert_eq!(result.bitmap_probes[0].1, partial_sha);
    }

    #[test]
    fn test_parse_partial_with_inline_bitmap_populates_peermap() {
        // When the announcer emits the extended HAVE line form with an
        // inline bitmap, the listener decodes it and populates PeerMap
        // directly with the exact chunks — no /meta probe needed.
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "1".repeat(64);
        let chunks: HashSet<u32> = [0u32, 3, 7, 11].iter().copied().collect();
        let encoded = bitmap_mod::encode(&chunks, 16);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:4/16:{}\nADDR:10.0.0.7:1531\n",
            sha, encoded
        );

        let result = parse_announcement(&msg, "10.0.0.7:1531".parse().unwrap(), &pm, &manifest);

        assert_eq!(result.bitmap_probes.len(), 0, "inline bitmap must not queue a probe");
        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks, chunks, "PeerMap must carry the exact decoded chunks");
    }

    #[test]
    fn test_parse_partial_with_invalid_bitmap_falls_back_to_probe() {
        // If the bitmap is garbage (wrong length, bad base64, whatever),
        // the listener must not populate PeerMap with stale guesses — it
        // falls back to the deferred /meta probe path, same as a line
        // without any bitmap at all.
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "2".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:4/16:!!!not-base64!!!\nADDR:10.0.0.8:1531\n",
            sha
        );

        let result = parse_announcement(&msg, "10.0.0.8:1531".parse().unwrap(), &pm, &manifest);

        assert_eq!(pm.peers_for(&sha).len(), 0);
        assert_eq!(result.bitmap_probes.len(), 1);
        assert_eq!(result.bitmap_probes[0].1, sha);
    }

    #[test]
    fn test_build_announcement_partial_inline_bitmap_roundtrip() {
        // End-to-end: write a partial blob to disk, register it in the
        // manifest (so get_entry returns a size), build the announcement,
        // then parse it back and confirm the peer map receives the exact
        // chunk set — no probes, no placeholders.
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();
        let manifest = test_manifest();

        // Construct a 5-chunk blob (CHUNK_SIZE * 5 bytes) and write two
        // arbitrary chunks to the partial dir.
        let sha = "3".repeat(64);
        let total_chunks: u32 = 5;
        let size = CHUNK_SIZE * total_chunks as u64;
        let chunk_data = vec![0xABu8; CHUNK_SIZE as usize];
        cache.write_chunk(&sha, 1, &chunk_data).unwrap();
        cache.write_chunk(&sha, 4, &chunk_data).unwrap();

        manifest.record_seed(&sha, "label", "*", "7d", "seeder", size, "10.0.0.9:1531");

        let packets = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 9),
            1531,
            None,
            &[],
            &[],
            None,
            0,
        );
        let msg = &packets[0];

        // The line must carry an inline bitmap (three colon-separated
        // segments after HAVE:) — no /0 sentinel, no bare have/total form.
        let have_line = msg
            .lines()
            .find(|l| l.starts_with(&format!("HAVE:{}:", sha)))
            .expect("announcement must include a HAVE line for the partial blob");
        let trailing = have_line.strip_prefix("HAVE:").unwrap();
        let colons = trailing.matches(':').count();
        assert_eq!(colons, 2, "expected HAVE:sha:have/total:bitmap, got {}", have_line);

        // Parse the announcement as a peer would and confirm the chunk
        // set lands in the PeerMap exactly.
        let pm = PeerMap::new();
        let listener_manifest = test_manifest();
        let result = parse_announcement(
            msg,
            "10.0.0.9:1531".parse().unwrap(),
            &pm,
            &listener_manifest,
        );
        assert_eq!(result.bitmap_probes.len(), 0);
        let expected: HashSet<u32> = [1u32, 4].iter().copied().collect();
        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks, expected);
    }

    // ----- gitback peer announcement round-trip -----

    #[test]
    fn build_announcement_appends_gitback_line_when_local_info_present() {
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec!["workstation".to_string()],
                    pubkey_hex: "a".repeat(64),
        };
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &[],
            &[],
            None,
            0,
        );
        let msg = &packets[0];
        assert!(msg.contains("GITBACK:danlap:dan:workstation"));
    }

    #[test]
    fn build_announcement_omits_gitback_line_when_none() {
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            None,
            &[],
            &[],
            None,
            0,
        );
        let msg = &packets[0];
        assert!(!msg.contains("GITBACK:"));
    }

    #[test]
    fn parse_announcement_recovers_gitback_line() {
        let pubkey_hex = "a".repeat(64);
        let msg = format!(
            "VIGO-SWARM\n\
             MANIFEST:1\n\
             ADDR:10.0.0.1:1531\n\
             GITBACK:plex:dan,alice:homelab:{}\n",
            pubkey_hex
        );
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            &msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        let li = result.gitback_peer.expect("gitback line must be parsed");
        assert_eq!(li.hostname, "plex");
        assert_eq!(li.users, vec!["dan".to_string(), "alice".to_string()]);
        assert_eq!(li.tags, vec!["homelab".to_string()]);
        assert_eq!(li.pubkey_hex, pubkey_hex);
    }

    #[test]
    fn parse_announcement_without_gitback_line_returns_none() {
        let msg = "VIGO-SWARM\n\
                   MANIFEST:1\n\
                   ADDR:10.0.0.1:1531\n";
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert!(result.gitback_peer.is_none());
    }

    #[test]
    fn parse_announcement_ignores_malformed_gitback_line() {
        let msg = "VIGO-SWARM\n\
                   MANIFEST:1\n\
                   ADDR:10.0.0.1:1531\n\
                   GITBACK:\n";
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert!(result.gitback_peer.is_none());
    }

    // --- GITBACKCLAIM round-trip (Phase 6) ---

    #[test]
    fn build_announcement_appends_gitback_claim_lines() {
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let claims = vec![
            (
                "gitback:danlap:dan:vigo-web".to_string(),
                vec!["plex".to_string(), "annlap".to_string()],
            ),
            (
                "gitback:danlap:dan:dotfiles".to_string(),
                vec!["plex".to_string()],
            ),
        ];
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            None,
            &claims,
            &[],
            None,
            0,
        );
        let msg = &packets[0];
        assert!(msg.contains("GITBACKCLAIM:gitback:danlap:dan:vigo-web:plex,annlap"));
        assert!(msg.contains("GITBACKCLAIM:gitback:danlap:dan:dotfiles:plex"));
    }

    #[test]
    fn build_announcement_emits_gitbackproject_lines_when_provided() {
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
            pubkey_hex: "a".repeat(64),
        };
        let pid_a = "a".repeat(64);
        let pid_b = "b".repeat(64);
        let projects = vec![
            (pid_a.clone(), "vigo".to_string(), "swarm-wide".to_string()),
            (
                pid_b.clone(),
                "dotfiles".to_string(),
                "members-only".to_string(),
            ),
        ];
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &[],
            &projects,
            None,
            0,
        );
        let msg = &packets[0];
        assert!(msg.contains(&format!("GITBACKPROJECT:danlap:{}:vigo:swarm-wide", pid_a)));
        assert!(msg.contains(&format!(
            "GITBACKPROJECT:danlap:{}:dotfiles:members-only",
            pid_b
        )));
    }

    #[test]
    fn parse_announcement_recovers_gitbackproject_lines() {
        let pubkey_hex = "a".repeat(64);
        let pid_legacy = "c".repeat(64);
        let pid_new = "d".repeat(64);
        let msg = format!(
            "VIGO-SWARM\n\
             MANIFEST:1\n\
             ADDR:10.0.0.1:1531\n\
             GITBACK:danlap:dan:workstation:{}\n\
             GITBACKPROJECT:danlap:{}\n\
             GITBACKPROJECT:danlap:{}:vigo:swarm-wide\n",
            pubkey_hex, pid_legacy, pid_new,
        );
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            &msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert_eq!(result.gitback_projects.len(), 2);
        // Legacy 2-field line — handle/dr_scope absent.
        let legacy = &result.gitback_projects[0];
        assert_eq!(legacy.0, "danlap");
        assert_eq!(legacy.1, pid_legacy);
        assert!(legacy.2.is_none());
        assert!(legacy.3.is_none());
        // ADR-023 4-field line — handle/dr_scope present.
        let new = &result.gitback_projects[1];
        assert_eq!(new.0, "danlap");
        assert_eq!(new.1, pid_new);
        assert_eq!(new.2.as_deref(), Some("vigo"));
        assert_eq!(new.3.as_deref(), Some("swarm-wide"));
    }

    #[test]
    fn parse_announcement_drops_malformed_gitbackproject() {
        // wrong prefix → ignored entirely
        let msg = "VIGO-SWARM\n\
                   MANIFEST:1\n\
                   ADDR:10.0.0.1:1531\n\
                   GITBACKPROJECT:host:not-a-pid\n";
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert!(result.gitback_projects.is_empty());
    }

    #[test]
    fn parse_announcement_recovers_gitback_claim_lines() {
        let pubkey_hex = "a".repeat(64);
        let msg = format!(
            "VIGO-SWARM\n\
             MANIFEST:1\n\
             ADDR:10.0.0.1:1531\n\
             GITBACK:danlap:dan:workstation:{}\n\
             GITBACKCLAIM:gitback:danlap:dan:vigo-web:plex,annlap\n\
             GITBACKCLAIM:gitback:danlap:dan:dotfiles:plex\n",
            pubkey_hex
        );
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            &msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert_eq!(result.gitback_peer.as_ref().map(|li| li.hostname.clone()),
            Some("danlap".to_string()));
        assert_eq!(result.gitback_claims.len(), 2);
        assert_eq!(
            result.gitback_claims[0],
            (
                "gitback:danlap:dan:vigo-web".to_string(),
                vec!["plex".to_string(), "annlap".to_string()],
            ),
        );
        assert_eq!(
            result.gitback_claims[1],
            (
                "gitback:danlap:dan:dotfiles".to_string(),
                vec!["plex".to_string()],
            ),
        );
    }

    #[test]
    fn parse_announcement_gitbackclaim_prefix_match_is_specific() {
        // Regression: `GITBACKCLAIM:` must match BEFORE `GITBACK:`
        // otherwise the latter's parser would see a malformed peer
        // line and drop it. Both should work together.
        let msg = "VIGO-SWARM\n\
                   MANIFEST:1\n\
                   ADDR:10.0.0.1:1531\n\
                   GITBACKCLAIM:gitback:h:u:p:some-host\n";
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let result = parse_announcement(
            msg,
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        // GITBACKCLAIM was picked up correctly.
        assert_eq!(result.gitback_claims.len(), 1);
        // And it did NOT also get parsed as a malformed GITBACK
        // peer line.
        assert!(result.gitback_peer.is_none());
    }

    #[test]
    fn build_announcement_packets_fit_under_max_msg_size_when_signed() {
        // 0.30.12 regression guard. The SIG footer appended by
        // `sign_and_append` after build is ~145 bytes. Before
        // MAX_CONTENT_SIZE was introduced, build-time budget checks
        // used MAX_MSG_SIZE directly and the emitted packet could
        // exceed MAX_MSG_SIZE by the full SIG length — the 1400-byte
        // receive buffer then silently truncated the tail, stripping
        // the SIG line and killing gitback authenticity on every
        // datagram. Saturate every section of the primary packet AND
        // force a spillover secondary packet, then confirm both still
        // fit in MAX_MSG_SIZE after signing.
        use sha2::Digest;
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();

        // Enough complete blobs that HAVE lines pack up against the budget.
        for i in 0..40u32 {
            let data = format!("blob-{}", i);
            let sha = hex::encode(sha2::Sha256::digest(data.as_bytes()));
            cache.write_chunk(&sha, 0, data.as_bytes()).unwrap();
            cache.assemble(&sha, data.len() as u64, 1).unwrap();
        }

        let li = GitbackLocalInfo {
            hostname: "danlap.home.arpa".to_string(),
            users: vec!["dan".to_string(), "anja".to_string(), "elena".to_string()],
            tags: vec![],
            pubkey_hex: "a".repeat(64),
        };
        // Enough claims to force GITBACKCLAIM overflow into the spillover
        // packet, so BOTH emitted packets get exercised against the cap.
        let claims: Vec<(String, Vec<String>)> = (0..12)
            .map(|i| {
                (
                    format!("gitback:danlap:dan:project-{:02}", i),
                    vec![
                        "plex.home.arpa".to_string(),
                        "girlslaptop.home.arpa".to_string(),
                        "annlap.home.arpa".to_string(),
                    ],
                )
            })
            .collect();

        let key = ed25519_dalek::SigningKey::from_bytes(&[7u8; 32]);
        let packets = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &claims,
            &[],
            Some(&key),
            1_700_000_000,
        );

        assert!(!packets.is_empty(), "primary packet must be emitted");
        for (i, msg) in packets.iter().enumerate() {
            assert!(
                msg.len() <= MAX_MSG_SIZE,
                "packet {} is {} bytes; exceeds MAX_MSG_SIZE={} — SIG_FOOTER_RESERVE={} may need to grow",
                i, msg.len(), MAX_MSG_SIZE, SIG_FOOTER_RESERVE,
            );
            assert!(
                msg.contains("\nSIG:"),
                "packet {} missing SIG footer, bytes={}: {}",
                i, msg.len(), msg,
            );
        }
    }

    #[test]
    fn build_announcement_drops_claim_line_on_mtu_overflow() {
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        // Synthesize a single claim line large enough to definitely
        // exceed MAX_MSG_SIZE even in a dedicated spillover packet.
        let many_hosts: Vec<String> =
            (0..200).map(|i| format!("peer-with-a-long-name-{}", i)).collect();
        let claims = vec![("gitback:h:u:huge".to_string(), many_hosts)];
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            None,
            &claims,
            &[],
            None,
            0,
        );
        // The oversized claim line must not appear in any emitted packet.
        for msg in &packets {
            assert!(
                !msg.contains("GITBACKCLAIM:gitback:h:u:huge:"),
                "oversized claim line should have been dropped, found in packet: {}",
                msg,
            );
        }
        // The primary packet is still valid.
        assert!(packets[0].starts_with("VIGO-SWARM\n"));
        assert!(packets[0].contains("ADDR:"));
    }

    // ----- 0.30.10 priority-inversion + spillover tests -----

    #[test]
    fn build_announcement_places_gitback_before_have() {
        // Regression: GITBACK/GITBACKCLAIM must be emitted BEFORE HAVE lines
        // so a blob-heavy source still reaches receivers with
        // coordination info. Prior to 0.30.10, HAVE packed first and
        // GITBACK fell off MTU on any cache with ~15+ blobs.
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();

        // A complete blob so HAVE is emitted.
        let data = b"x";
        use sha2::Digest;
        let sha = hex::encode(sha2::Sha256::digest(data));
        cache.write_chunk(&sha, 0, data).unwrap();
        cache.assemble(&sha, data.len() as u64, 1).unwrap();

        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
                    pubkey_hex: "a".repeat(64),
        };
        let claims = vec![("gitback:danlap:dan:vigo".to_string(), vec!["plex".to_string()])];
        let packets = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &claims,
            &[],
            None,
            0,
        );
        let msg = &packets[0];
        let gitback_pos = msg.find("GITBACK:danlap:").expect("GITBACK line present");
        let gitbackclaim_pos = msg.find("GITBACKCLAIM:gitback:danlap:dan:vigo:plex")
            .expect("GITBACKCLAIM line present");
        let have_pos = msg.find("HAVE:").expect("HAVE line present");
        assert!(gitback_pos < have_pos, "GITBACK must precede HAVE");
        assert!(gitbackclaim_pos < have_pos, "GITBACKCLAIM must precede HAVE");
    }

    #[test]
    fn build_announcement_preserves_gitback_under_heavy_have_load() {
        // Simulate the failure mode Dan hit in production on 2026-04-23:
        // ~60 cached blobs starve gitback of MTU budget. The new packing
        // guarantees GITBACK/GITBACKCLAIM survive.
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();

        use sha2::Digest;
        for i in 0..60u32 {
            let data = format!("blob-{}", i);
            let sha = hex::encode(sha2::Sha256::digest(data.as_bytes()));
            cache.write_chunk(&sha, 0, data.as_bytes()).unwrap();
            cache.assemble(&sha, data.len() as u64, 1).unwrap();
        }

        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
                    pubkey_hex: "a".repeat(64),
        };
        let claims = vec![("gitback:danlap:dan:vigo".to_string(), vec!["plex".to_string()])];
        let packets = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &claims,
            &[],
            None,
            0,
        );
        let joined: String = packets.join("");
        assert!(joined.contains("GITBACK:danlap:"), "GITBACK line must survive heavy HAVE load");
        assert!(joined.contains("GITBACKCLAIM:gitback:danlap:dan:vigo:plex"),
            "GITBACKCLAIM line must survive heavy HAVE load");
    }

    #[test]
    fn build_announcement_spills_gitbackclaims_to_second_packet() {
        // A large-fleet publisher with many gitback labels overflows the
        // gitback budget of the primary packet. Remaining GITBACKCLAIM
        // lines land in a second packet on the same cycle.
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();

        // 30 labels × ~40 bytes each = ~1.2 KB of GITBACKCLAIM, past the
        // gitback budget (MAX_MSG_SIZE - 300 = 1100 bytes).
        let claims: Vec<(String, Vec<String>)> = (0..30)
            .map(|i| {
                (
                    format!("gitback:danlap:dan:project-{:02}", i),
                    vec!["plex".to_string(), "annlap".to_string()],
                )
            })
            .collect();
        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
                    pubkey_hex: "a".repeat(64),
        };

        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &claims,
            &[],
            None,
            0,
        );
        assert_eq!(packets.len(), 2, "expected a spillover packet");
        // GITBACK peer line appears in both packets so the receiver can
        // attribute the claims to a sender regardless of which packet
        // arrives first.
        assert!(packets[0].contains("GITBACK:danlap:"));
        assert!(packets[1].contains("GITBACK:danlap:"));
        // Every label lands somewhere across the two packets.
        let joined: String = packets.join("");
        for i in 0..30 {
            let label = format!("GITBACKCLAIM:gitback:danlap:dan:project-{:02}:plex,annlap", i);
            assert!(joined.contains(&label), "missing claim line for {}", i);
        }
    }

    #[test]
    fn build_announcement_spillover_preserves_have_on_primary() {
        // The primary packet should still carry HAVE lines when it
        // triggers a gitback spillover — the spillover is additive, not a
        // replacement. Proves we didn't regress swarm discovery by
        // fixing gitback.
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();

        use sha2::Digest;
        let data = b"a";
        let sha = hex::encode(sha2::Sha256::digest(data));
        cache.write_chunk(&sha, 0, data).unwrap();
        cache.assemble(&sha, data.len() as u64, 1).unwrap();

        let claims: Vec<(String, Vec<String>)> = (0..30)
            .map(|i| {
                (
                    format!("gitback:danlap:dan:project-{:02}", i),
                    vec!["plex".to_string(), "annlap".to_string()],
                )
            })
            .collect();

        let packets = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            None,
            &claims,
            &[],
            None,
            0,
        );
        assert!(!packets.is_empty());
        assert!(packets[0].contains(&format!("HAVE:{}:1/1", sha)),
            "primary packet must still carry HAVE lines when spillover is used");
    }

    // ----- 0.30.11 gitback signature tests -----

    #[test]
    fn signed_announcement_verifies_round_trip() {
        let (verify, sign) = crate::auth::keypair::generate_keypair().unwrap();
        let pubkey_hex = hex::encode(verify.to_bytes());
        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
            pubkey_hex: pubkey_hex.clone(),
        };
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let claims = vec![("gitback:danlap:dan:vigo".to_string(), vec!["plex".to_string()])];
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &claims,
            &[],
            Some(&sign),
            1_700_000_000,
        );
        let msg = &packets[0];

        // Packet must carry a trailing SIG line.
        assert!(msg.ends_with("\n"), "packet must end with newline");
        let sig_line = msg.lines().last().unwrap();
        assert!(sig_line.starts_with("SIG:1700000000:"), "SIG line present: got {}", sig_line);

        // Verify through the public path (extract_sig + verify_payload).
        let (body, ts, sig_bytes) = extract_sig(msg).expect("extract sig");
        assert_eq!(ts, "1700000000");
        assert_eq!(sig_bytes.len(), 64);
        assert!(crate::auth::signer::verify_payload(body.as_bytes(), &ts, &sig_bytes, &verify));
    }

    #[test]
    fn tampered_signed_announcement_fails_verification() {
        let (verify, sign) = crate::auth::keypair::generate_keypair().unwrap();
        let li = GitbackLocalInfo {
            hostname: "danlap".to_string(),
            users: vec!["dan".to_string()],
            tags: vec![],
            pubkey_hex: hex::encode(verify.to_bytes()),
        };
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            Some(&li),
            &[],
            &[],
            Some(&sign),
            1_700_000_000,
        );
        // Tamper with the body (swap the GITBACKCLAIM target). Verification must fail.
        let tampered = packets[0].replace("danlap", "attacker");
        let (body, ts, sig_bytes) = extract_sig(&tampered).expect("extract sig");
        assert!(!crate::auth::signer::verify_payload(body.as_bytes(), &ts, &sig_bytes, &verify),
            "tampered packet must not verify");
    }

    #[test]
    fn unsigned_announcement_has_no_sig_line() {
        // When build_announcement is called without a signing key (pre-
        // 0.30.11 behavior, or a non-gitback envoy), it emits no SIG line.
        let tmp = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(tmp.path().join("blobs")).unwrap();
        let manifest = test_manifest();
        let packets = build_announcement(
            &[],
            &[],
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 1),
            1531,
            None,
            &[],
            &[],
            None,
            0,
        );
        assert!(!packets[0].contains("\nSIG:"), "packet must not carry SIG when unsigned");
        assert_eq!(extract_sig(&packets[0]), None);
    }

    #[test]
    fn tofu_cache_blocks_pubkey_swap() {
        use crate::swarm::gitback::peer_pubkey_cache::{CheckOutcome, PeerPubkeyCache};
        let cache = PeerPubkeyCache::empty();
        let pk1 = "a".repeat(64);
        let pk2 = "b".repeat(64);
        assert_eq!(cache.check("danlap", &pk1), CheckOutcome::FirstSighting);
        cache.accept("danlap", &pk1, 100);
        assert_eq!(cache.check("danlap", &pk1), CheckOutcome::Match);
        // Pubkey swap for the same hostname is rejected.
        match cache.check("danlap", &pk2) {
            CheckOutcome::Mismatch { .. } => {}
            other => panic!("expected Mismatch, got {:?}", other),
        }
    }
}
