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

/// Announce interval.
const ANNOUNCE_INTERVAL: Duration = Duration::from_secs(5);

/// Maximum multicast message size (UDP payload).
const MAX_MSG_SIZE: usize = 1400;

/// Protocol header.
const HEADER: &str = "VIGO-SWARM";

/// Spawn the multicast announcer and listener.
///
/// Returns immediately. Both tasks run until the cancellation token fires.
/// If multicast setup fails, logs a warning and returns — the agent continues
/// without multicast discovery.
pub fn spawn(
    peer_map: Arc<PeerMap>,
    cache: Arc<BlobCache>,
    manifest: Arc<Manifest>,
    tls: Option<TlsCerts>,
    swarm_port: u16,
    cancel: tokio_util::sync::CancellationToken,
) {
    let peer_map_listen = peer_map.clone();
    let manifest_listen = manifest.clone();
    let cancel_listen = cancel.clone();
    let tls_listen = tls.clone();

    // Listener task.
    tokio::spawn(async move {
        if let Err(e) = listen_loop(peer_map_listen, manifest_listen, tls_listen, cancel_listen).await {
            warn!(error = %e, "multicast listener exited");
        }
    });

    // Announcer task.
    tokio::spawn(async move {
        if let Err(e) = announce_loop(cache, peer_map, manifest, swarm_port, cancel).await {
            warn!(error = %e, "multicast announcer exited");
        }
    });
}

/// Listen for multicast announcements and update the peer map.
async fn listen_loop(
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    tls: Option<TlsCerts>,
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
                                if let Some(ref tls_certs) = tls {
                                    let m = manifest.clone();
                                    let t = tls_certs.clone();
                                    tokio::task::spawn_blocking(move || {
                                        sync_manifest_from_peer(&addr, &m, &t);
                                    });
                                }
                            }
                            // Resolve partial-blob sentinels via async /meta
                            // probes so the scheduler sees real chunk sets.
                            for (peer_addr, sha) in result.bitmap_probes {
                                if let Some(ref tls_certs) = tls {
                                    let pm = peer_map.clone();
                                    let t = tls_certs.clone();
                                    tokio::task::spawn_blocking(move || {
                                        probe_peer_bitmap(&peer_addr, &sha, &pm, &t);
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
async fn announce_loop(
    cache: Arc<BlobCache>,
    peer_map: Arc<PeerMap>,
    manifest: Arc<Manifest>,
    swarm_port: u16,
    cancel: tokio_util::sync::CancellationToken,
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

    loop {
        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            _ = tokio::time::sleep(ANNOUNCE_INTERVAL) => {}
        }

        let complete: Vec<String> = cache.list_blobs()
            .into_iter()
            .filter(|sha| !manifest.is_revoked(sha))
            .collect();
        let partials: Vec<String> = cache.list_partials()
            .into_iter()
            .filter(|sha| !manifest.is_revoked(sha))
            .collect();
        if complete.is_empty() && partials.is_empty() {
            continue; // Nothing to announce.
        }

        let msg = build_announcement(&complete, &partials, &cache, &manifest, local_ip, swarm_port);
        if let Err(e) = udp.send_to(msg.as_bytes(), dest).await {
            debug!(error = %e, "multicast announce failed");
        }

        // Periodic GC of expired peer entries + persist to disk.
        peer_map.gc();
        let save_path = crate::platform::paths::default_blobs_dir()
            .parent()
            .map(|p| p.join("peers.json"));
        if let Some(path) = save_path {
            peer_map.save(&path);
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
        let bitmap = cache.partial_chunks(sha);
        if bitmap.is_empty() {
            continue;
        }
        let have = bitmap.len();
        // We don't know total chunks here — use 0 as sentinel.
        // The listener uses the /meta endpoint to get the real total.
        let line = format!("HAVE:{}:{}/0\n", sha, have);
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
    let mut complete_blobs: Vec<(String, HashSet<u32>)> = Vec::new();
    let mut partial_shas: Vec<String> = Vec::new();
    let mut remote_manifest_version: Option<u64> = None;

    for line in lines {
        if let Some(ver_str) = line.strip_prefix("MANIFEST:") {
            remote_manifest_version = ver_str.parse().ok();
            continue;
        }
        if let Some(rest) = line.strip_prefix("HAVE:") {
            if let Some((sha, counts)) = rest.split_once(':') {
                if let Some((have_str, total_str)) = counts.split_once('/') {
                    if let (Ok(have), Ok(total)) = (have_str.parse::<u32>(), total_str.parse::<u32>()) {
                        if total > 0 && have >= total {
                            // Complete blob — chunk set is known without a probe.
                            complete_blobs.push((sha.to_string(), (0..total).collect()));
                        } else if have > 0 {
                            // Partial. We know the peer has `have` chunks
                            // but not which ones. Defer to an async /meta
                            // probe rather than adding a placeholder.
                            partial_shas.push(sha.to_string());
                        }
                        // have == 0 → nothing useful to do
                    }
                }
            }
        } else if let Some(rest) = line.strip_prefix("ADDR:") {
            addr = Some(rest.to_string());
        }
    }

    if let Some(peer_addr) = addr {
        for (sha, chunks) in complete_blobs {
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
fn probe_peer_bitmap(peer_addr: &str, sha: &str, peer_map: &PeerMap, tls: &TlsCerts) {
    let agent = match peer_client::build_peer_agent(tls) {
        Ok(a) => a,
        Err(e) => {
            debug!(error = %e, peer = peer_addr, "failed to build peer agent for bitmap probe");
            return;
        }
    };

    match peer_client::fetch_meta(&agent, peer_addr, sha) {
        Ok(meta) => {
            let chunks: HashSet<u32> = meta.chunks.into_iter().collect();
            let count = chunks.len();
            peer_map.report(sha, peer_addr, chunks, PeerSource::PeerExchange);
            debug!(
                peer = peer_addr,
                sha = &sha[..12.min(sha.len())],
                chunk_count = count,
                "bitmap probe resolved partial sentinel"
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
/// when multicast announces a different manifest version.
fn sync_manifest_from_peer(peer_addr: &str, manifest: &Manifest, tls: &TlsCerts) {
    let agent = match peer_client::build_peer_agent(tls) {
        Ok(a) => a,
        Err(e) => {
            debug!(error = %e, "failed to build peer agent for manifest sync");
            return;
        }
    };

    match peer_client::fetch_manifest(&agent, peer_addr) {
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
}
