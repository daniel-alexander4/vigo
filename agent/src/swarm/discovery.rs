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

/// Maximum multicast message size (UDP payload).
const MAX_MSG_SIZE: usize = 1400;

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
) {
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

    // Listener task.
    tokio::spawn(async move {
        if let Err(e) = listen_loop(peer_map_listen, manifest_listen, listen_agent, cancel_listen).await {
            warn!(error = %e, "multicast listener exited");
        }
    });

    // Announcer task.
    tokio::spawn(async move {
        if let Err(e) = announce_loop(cache, peer_map, manifest, swarm_port, cancel, announce).await {
            warn!(error = %e, "multicast announcer exited");
        }
    });
}

/// Listen for multicast announcements and update the peer map.
async fn listen_loop(
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    shared_agent: Option<Arc<ureq::Agent>>,
    cancel: tokio_util::sync::CancellationToken,
) -> anyhow::Result<()> {
    let socket = create_multicast_socket()?;
    socket.set_nonblocking(true)?;
    let std_socket: std::net::UdpSocket = socket.into();
    let udp = tokio::net::UdpSocket::from_std(std_socket)?;

    info!("multicast listener started on 224.0.0.42:{}", MULTICAST_PORT);

    let mut buf = [0u8; MAX_MSG_SIZE];
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            result = udp.recv_from(&mut buf) => {
                match result {
                    Ok((n, src)) => {
                        if let Ok(msg) = std::str::from_utf8(&buf[..n]) {
                            let result = parse_announcement(msg, src, &peer_map, &manifest);
                            // Trigger manifest sync if a version mismatch was detected.
                            if let Some(addr) = result.manifest_sync_peer {
                                if let Some(ref agent) = shared_agent {
                                    let m = manifest.clone();
                                    let a = agent.clone();
                                    tokio::task::spawn_blocking(move || {
                                        sync_manifest_from_peer(&addr, &m, &a);
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
        if complete.is_empty() && partials.is_empty() {
            last_send = tokio::time::Instant::now();
            continue; // Nothing to announce.
        }

        let msg = build_announcement(&complete, &partials, &cache, &manifest, local_ip, swarm_port);
        if let Err(e) = udp.send_to(msg.as_bytes(), dest).await {
            debug!(error = %e, "multicast announce failed");
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
fn build_announcement(
    complete: &[String],
    partials: &[String],
    cache: &BlobCache,
    manifest: &Manifest,
    local_ip: Ipv4Addr,
    port: u16,
) -> String {
    let mut msg = String::from(HEADER);
    msg.push('\n');
    msg.push_str(&format!("MANIFEST:{}\n", manifest.version()));

    // Announce complete blobs.
    for sha in complete {
        let path = cache.dir().join(sha);
        let size = std::fs::metadata(&path).map(|m| m.len()).unwrap_or(0);
        let total = chunks_for_size(size);
        let line = format!("HAVE:{}:{}/{}\n", sha, total, total);
        if msg.len() + line.len() + 30 > MAX_MSG_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    // Announce partial blobs so other peers can source chunks from us.
    for sha in partials {
        // Skip stale `.partial/` directories left behind for blobs that have
        // already completed — those are announced via the `complete` loop
        // above. Without this guard, an orphan partial dir would route
        // through the complete-blob branch of `chunk_bitmap` and try to
        // materialize a 40 GB HashSet.
        if cache.has_blob(sha) {
            continue;
        }
        let chunks = cache.partial_chunks(sha);
        if chunks.is_empty() {
            continue;
        }
        let have = chunks.len() as u32;
        // Look up total from the manifest so listeners can decode the
        // bitmap and so the sentinel carries the real denominator. If the
        // manifest has no record (shouldn't happen for an in-flight
        // download, but guard anyway), fall back to the `/0` sentinel and
        // let the listener resolve via /meta.
        let total = manifest
            .get_entry(sha)
            .map(|e| chunks_for_size(e.size))
            .unwrap_or(0);
        let line = if total > 0 && bitmap::encoded_len(total) > 0 {
            let encoded = bitmap::encode(&chunks, total);
            let inline = format!("HAVE:{}:{}/{}:{}\n", sha, have, total, encoded);
            if msg.len() + inline.len() + 30 <= MAX_MSG_SIZE {
                inline
            } else {
                // Bitmap would overflow MTU — emit the bitmap-less form
                // so the listener defers to an async /meta probe.
                format!("HAVE:{}:{}/{}\n", sha, have, total)
            }
        } else {
            format!("HAVE:{}:{}/0\n", sha, have)
        };
        if msg.len() + line.len() + 30 > MAX_MSG_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    // Announce blobs we need (solicitation: "WANT" lines).
    // Peers hearing a WANT for a blob they have will include it in their
    // next announcement, cutting discovery from 5s to <1s.
    for sha in manifest.active_blobs() {
        if complete.contains(&sha) || partials.iter().any(|p| p == &sha) {
            continue; // Already have it (complete or partial).
        }
        let line = format!("WANT:{}\n", sha);
        if msg.len() + line.len() + 30 > MAX_MSG_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    msg.push_str(&format!("ADDR:{}:{}\n", local_ip, port));
    msg
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
fn sync_manifest_from_peer(peer_addr: &str, manifest: &Manifest, agent: &Arc<ureq::Agent>) {
    match peer_client::fetch_manifest(agent, peer_addr) {
        Ok(remote) => {
            let changed = manifest.merge(&remote);
            if changed {
                info!(
                    peer = peer_addr,
                    version = manifest.version(),
                    "manifest synced from peer"
                );
                // Evict newly revoked blobs from cache + clean up symlinks.
                super::evict_revoked_blobs(&manifest);
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
        let path = dir.into_path();
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

        let msg = build_announcement(&cache.list_blobs(), &cache.list_partials(), &cache, &manifest, Ipv4Addr::new(10, 0, 0, 1), 1531);
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

        let msg = build_announcement(
            &cache.list_blobs(),
            &cache.list_partials(),
            &cache,
            &manifest,
            Ipv4Addr::new(10, 0, 0, 9),
            1531,
        );

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
            &msg,
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
}
