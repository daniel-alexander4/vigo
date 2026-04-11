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
use tracing::{debug, info};

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
            debug!(error = %e, "multicast listener unavailable");
        }
    });

    // Announcer task.
    tokio::spawn(async move {
        if let Err(e) = announce_loop(cache, peer_map, manifest, swarm_port, cancel).await {
            debug!(error = %e, "multicast announcer unavailable");
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
                            let sync_addr = parse_announcement(msg, src, &peer_map, &manifest);
                            // Trigger manifest sync if a version mismatch was detected.
                            if let Some(addr) = sync_addr {
                                if let Some(ref tls_certs) = tls {
                                    let m = manifest.clone();
                                    let t = tls_certs.clone();
                                    tokio::task::spawn_blocking(move || {
                                        sync_manifest_from_peer(&addr, &m, &t);
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
        let bitmap = cache.chunk_bitmap(sha, u32::MAX);
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

/// Parse a received multicast announcement and update the peer map.
/// Returns the peer address if a manifest sync is needed (version mismatch).
fn parse_announcement(msg: &str, _src: SocketAddr, peer_map: &PeerMap, manifest: &Manifest) -> Option<String> {
    let mut lines = msg.lines();

    // Validate header.
    match lines.next() {
        Some(h) if h == HEADER => {}
        _ => return None,
    }

    let mut addr: Option<String> = None;
    let mut blob_chunks: Vec<(String, HashSet<u32>)> = Vec::new();
    let mut _remote_manifest_version: Option<u64> = None;

    for line in lines {
        if let Some(ver_str) = line.strip_prefix("MANIFEST:") {
            _remote_manifest_version = ver_str.parse().ok();
            continue;
        }
        if let Some(rest) = line.strip_prefix("HAVE:") {
            if let Some((sha, counts)) = rest.split_once(':') {
                if let Some((have_str, total_str)) = counts.split_once('/') {
                    if let (Ok(have), Ok(total)) = (have_str.parse::<u32>(), total_str.parse::<u32>()) {
                        let chunks: HashSet<u32> = if total > 0 && have >= total {
                            // Complete blob.
                            (0..total).collect()
                        } else if have > 0 {
                            // Partial — we don't know which specific chunks,
                            // but the peer has some. The /meta endpoint gives
                            // the exact bitmap. Placeholder triggers connection.
                            (0..have).collect()
                        } else {
                            continue; // No chunks, skip.
                        };
                        blob_chunks.push((sha.to_string(), chunks));
                    }
                }
            }
        } else if let Some(rest) = line.strip_prefix("ADDR:") {
            addr = Some(rest.to_string());
        }
    }

    if let Some(peer_addr) = addr {
        for (sha, chunks) in blob_chunks {
            peer_map.report(&sha, &peer_addr, chunks, PeerSource::Multicast);
        }

        // If the peer has a different manifest version, signal the caller
        // to trigger a manifest sync over HTTPS.
        if let Some(remote_ver) = _remote_manifest_version {
            let local_ver = manifest.version();
            if remote_ver != local_ver {
                debug!(
                    peer = %peer_addr,
                    local_ver,
                    remote_ver,
                    "manifest version mismatch — triggering sync"
                );
                return Some(peer_addr);
            }
        }

        debug!(peer = %peer_addr, "processed multicast announcement");
    }
    None
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

/// Get the local IPv4 address (outbound default route).
fn local_ipv4() -> Option<Ipv4Addr> {
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
    fn test_parse_announcement() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sha = "a".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nMANIFEST:1\nHAVE:{}:5/10\nADDR:10.0.0.2:1531\n",
            sha
        );

        // Manifest version 1 vs our 0 → should signal sync needed.
        let sync = parse_announcement(
            &msg,
            "10.0.0.2:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        assert!(sync.is_some());

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].address, "10.0.0.2:1531");
        assert_eq!(peers[0].source, PeerSource::Multicast);
    }

    #[test]
    fn test_parse_invalid_header() {
        let pm = PeerMap::new();
        let manifest = test_manifest();
        let sync = parse_announcement(
            "NOT-VIGO\nHAVE:abc:1/1\nADDR:10.0.0.1:1531\n",
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
            &manifest,
        );
        // Should be ignored — no peers added.
        assert_eq!(pm.len(), 0);
        assert!(sync.is_none());
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

        let _sync = parse_announcement(&msg, "10.0.0.3:1531".parse().unwrap(), &pm, &manifest);

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks.len(), 10);
    }
}
