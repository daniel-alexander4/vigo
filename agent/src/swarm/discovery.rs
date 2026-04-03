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
use super::meta::chunks_for_size;
use super::peermap::{PeerMap, PeerSource};

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
    swarm_port: u16,
    cancel: tokio_util::sync::CancellationToken,
) {
    let peer_map_listen = peer_map.clone();
    let cancel_listen = cancel.clone();

    // Listener task.
    tokio::spawn(async move {
        if let Err(e) = listen_loop(peer_map_listen, cancel_listen).await {
            debug!(error = %e, "multicast listener unavailable");
        }
    });

    // Announcer task.
    tokio::spawn(async move {
        if let Err(e) = announce_loop(cache, peer_map, swarm_port, cancel).await {
            debug!(error = %e, "multicast announcer unavailable");
        }
    });
}

/// Listen for multicast announcements and update the peer map.
async fn listen_loop(
    peer_map: Arc<PeerMap>,
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
                            parse_announcement(msg, src, &peer_map);
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

/// Periodically announce our blob inventory via multicast.
async fn announce_loop(
    cache: Arc<BlobCache>,
    peer_map: Arc<PeerMap>,
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

        let blobs = cache.list_blobs();
        if blobs.is_empty() {
            continue; // Nothing to announce.
        }

        let msg = build_announcement(&blobs, &cache, local_ip, swarm_port);
        if let Err(e) = udp.send_to(msg.as_bytes(), dest).await {
            debug!(error = %e, "multicast announce failed");
        }

        // Periodic GC of expired peer entries.
        peer_map.gc();
    }
}

/// Build a multicast announcement message.
fn build_announcement(
    blobs: &[String],
    cache: &BlobCache,
    local_ip: Ipv4Addr,
    port: u16,
) -> String {
    let mut msg = String::from(HEADER);
    msg.push('\n');

    for sha in blobs {
        // Complete blobs: report all chunks.
        let path = cache.dir().join(sha);
        let size = std::fs::metadata(&path).map(|m| m.len()).unwrap_or(0);
        let total = chunks_for_size(size);
        // Truncate to fit MAX_MSG_SIZE — skip blobs if message would be too long.
        let line = format!("HAVE:{}:{}/{}\n", sha, total, total);
        if msg.len() + line.len() + 30 > MAX_MSG_SIZE {
            break;
        }
        msg.push_str(&line);
    }

    msg.push_str(&format!("ADDR:{}:{}\n", local_ip, port));
    msg
}

/// Parse a received multicast announcement and update the peer map.
fn parse_announcement(msg: &str, _src: SocketAddr, peer_map: &PeerMap) {
    let mut lines = msg.lines();

    // Validate header.
    match lines.next() {
        Some(h) if h == HEADER => {}
        _ => return,
    }

    let mut addr: Option<String> = None;
    let mut blob_chunks: Vec<(String, HashSet<u32>)> = Vec::new();

    for line in lines {
        if let Some(rest) = line.strip_prefix("HAVE:") {
            if let Some((sha, counts)) = rest.split_once(':') {
                if let Some((have_str, total_str)) = counts.split_once('/') {
                    if let (Ok(have), Ok(total)) = (have_str.parse::<u32>(), total_str.parse::<u32>()) {
                        let chunks: HashSet<u32> = if have >= total {
                            (0..total).collect()
                        } else {
                            // Partial — we don't know which specific chunks,
                            // but we know the peer has some. The /meta endpoint
                            // will give the exact bitmap. For now, mark as
                            // having 0..have (a placeholder that triggers connection).
                            (0..have).collect()
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
        debug!(peer = %peer_addr, "processed multicast announcement");
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

    #[test]
    fn test_build_announcement() {
        let dir = tempfile::tempdir().unwrap();
        let cache = BlobCache::new(dir.path().to_path_buf()).unwrap();

        // Write a small complete blob.
        let data = b"test data";
        use sha2::Digest;
        let sha = hex::encode(sha2::Sha256::digest(data));
        cache.write_chunk(&sha, 0, data).unwrap();
        cache.assemble(&sha, data.len() as u64, 1).unwrap();

        let msg = build_announcement(&cache.list_blobs(), &cache, Ipv4Addr::new(10, 0, 0, 1), 1531);
        assert!(msg.starts_with("VIGO-SWARM\n"));
        assert!(msg.contains(&format!("HAVE:{}:1/1", sha)));
        assert!(msg.contains("ADDR:10.0.0.1:1531"));
    }

    #[test]
    fn test_parse_announcement() {
        let pm = PeerMap::new();
        let sha = "a".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:5/10\nADDR:10.0.0.2:1531\n",
            sha
        );

        parse_announcement(
            &msg,
            "10.0.0.2:1531".parse().unwrap(),
            &pm,
        );

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].address, "10.0.0.2:1531");
        assert_eq!(peers[0].source, PeerSource::Multicast);
    }

    #[test]
    fn test_parse_invalid_header() {
        let pm = PeerMap::new();
        parse_announcement(
            "NOT-VIGO\nHAVE:abc:1/1\nADDR:10.0.0.1:1531\n",
            "10.0.0.1:1531".parse().unwrap(),
            &pm,
        );
        // Should be ignored — no peers added.
        assert_eq!(pm.len(), 0);
    }

    #[test]
    fn test_parse_complete_blob() {
        let pm = PeerMap::new();
        let sha = "b".repeat(64);
        let msg = format!(
            "VIGO-SWARM\nHAVE:{}:10/10\nADDR:10.0.0.3:1531\n",
            sha
        );

        parse_announcement(&msg, "10.0.0.3:1531".parse().unwrap(), &pm);

        let peers = pm.peers_for(&sha);
        assert_eq!(peers.len(), 1);
        assert_eq!(peers[0].chunks.len(), 10);
    }
}
