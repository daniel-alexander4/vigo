// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! UDP multicast announcer + listener for filesync.
//!
//! Uses a dedicated multicast group/port distinct from the swarm peer
//! discovery channel. Each announcement carries one user's full state
//! (files + tombstones) serialized as JSON.
//!
//! Message format (LAN-local plaintext, line-delimited):
//! ```text
//! FILESYNC-1
//! <json body>
//! ```

use std::net::{Ipv4Addr, SocketAddr, SocketAddrV4};
use std::sync::Arc;
use std::time::Duration;

use socket2::{Domain, Protocol, Socket, Type};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use super::state::Announcement;

/// Multicast group for filesync — distinct from swarm's 224.0.0.42.
pub const MULTICAST_ADDR: Ipv4Addr = Ipv4Addr::new(224, 0, 0, 43);
pub const MULTICAST_PORT: u16 = 1532;
pub const HEADER: &str = "FILESYNC-1";

/// Maximum UDP payload we'll send/receive. The announcement is a JSON
/// serialization of `UserState` (all of a user's filesync files + tombstones
/// for one machine); per-file overhead is ~150 bytes JSON so the
/// ceiling is ~400 files per announcement at 64 KiB.
///
/// Raised from 16 KiB to 64 KiB in 0.30.10. The old ceiling started
/// failing loudly (via the `warn!` + drop path below) for users with
/// dense home directories; 64 KiB matches the UDP payload maximum and
/// moves the failure horizon out by ~4×. Past this, the correct fix is
/// paginated announcements (split the file map across multiple packets
/// with page + total markers). Not done here — tracked in pending.md
/// as a follow-up for when any real deployment trips the new ceiling.
pub const MAX_PAYLOAD: usize = 64 * 1024;

/// Received announcement with the sender's socket address attached.
#[derive(Debug)]
pub struct IncomingAnnouncement {
    pub from: SocketAddr,
    pub announcement: Announcement,
}

/// Spawn announcer and listener tasks.
///
/// Returns three channel ends: an outbound sender for announcements we
/// want to emit, plus both ends of the inbound announcement channel.
/// The inbound sender is cloned into the HTTPS `POST /filesync/announce`
/// handler so cross-subnet peers can feed announcements into the same
/// reconciler path as multicast discovery.
pub fn spawn(
    cancel: CancellationToken,
) -> (
    mpsc::Sender<Announcement>,
    mpsc::Sender<IncomingAnnouncement>,
    mpsc::Receiver<IncomingAnnouncement>,
) {
    let (out_tx, out_rx) = mpsc::channel::<Announcement>(32);
    let (in_tx, in_rx) = mpsc::channel::<IncomingAnnouncement>(64);

    // Listener task.
    let cancel_listen = cancel.clone();
    let in_tx_listener = in_tx.clone();
    tokio::spawn(async move {
        if let Err(e) = listen_loop(in_tx_listener, cancel_listen).await {
            warn!(error = %e, "filesync multicast listener exited");
        }
    });

    // Announcer task.
    let cancel_announce = cancel.clone();
    tokio::spawn(async move {
        if let Err(e) = announce_loop(out_rx, cancel_announce).await {
            warn!(error = %e, "filesync multicast announcer exited");
        }
    });

    (out_tx, in_tx, in_rx)
}

async fn listen_loop(
    in_tx: mpsc::Sender<IncomingAnnouncement>,
    cancel: CancellationToken,
) -> anyhow::Result<()> {
    let socket = create_multicast_socket()?;
    socket.set_nonblocking(true)?;
    let std_socket: std::net::UdpSocket = socket.into();
    let udp = tokio::net::UdpSocket::from_std(std_socket)?;

    info!("filesync multicast listener started on {}:{}", MULTICAST_ADDR, MULTICAST_PORT);

    let mut buf = vec![0u8; MAX_PAYLOAD];
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            result = udp.recv_from(&mut buf) => {
                match result {
                    Ok((n, src)) => {
                        if let Some(ann) = parse(&buf[..n]) {
                            let _ = in_tx.send(IncomingAnnouncement {
                                from: src,
                                announcement: ann,
                            }).await;
                        }
                    }
                    Err(e) => {
                        if e.kind() != std::io::ErrorKind::WouldBlock {
                            debug!(error = %e, "filesync multicast recv error");
                        }
                    }
                }
            }
        }
    }
}

async fn announce_loop(
    mut out_rx: mpsc::Receiver<Announcement>,
    cancel: CancellationToken,
) -> anyhow::Result<()> {
    let socket = Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::UDP))?;
    socket.set_multicast_ttl_v4(1)?;
    socket.set_nonblocking(true)?;
    socket.bind(&SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, 0).into())?;
    let std_socket: std::net::UdpSocket = socket.into();
    let udp = tokio::net::UdpSocket::from_std(std_socket)?;
    let dest = SocketAddr::V4(SocketAddrV4::new(MULTICAST_ADDR, MULTICAST_PORT));

    info!("filesync multicast announcer started");

    loop {
        tokio::select! {
            _ = cancel.cancelled() => return Ok(()),
            maybe = out_rx.recv() => {
                let ann = match maybe {
                    Some(a) => a,
                    None => return Ok(()),
                };
                let payload = match serialize(&ann) {
                    Some(p) if p.len() <= MAX_PAYLOAD => p,
                    Some(p) => {
                        warn!(
                            bytes = p.len(),
                            limit = MAX_PAYLOAD,
                            user = %ann.user,
                            "filesync announcement exceeds multicast limit — unexpected for digest-only \
                             announcements, likely malformed state digest or overflowed machine/user \
                             identifier. Not a files problem on the 0.30.11+ wire format.",
                        );
                        continue;
                    }
                    None => continue,
                };
                if let Err(e) = udp.send_to(&payload, dest).await {
                    // warn! (not debug!) so operators actually see this when
                    // something like a firewall rule silently drops outbound
                    // packets. Found during 0.20.9 troubleshooting when filesync
                    // was silently non-functional for days because port 1532
                    // wasn't in the swarm-firewall allow list.
                    warn!(
                        error = %e,
                        dest = %dest,
                        bytes = payload.len(),
                        "filesync multicast send failed",
                    );
                } else {
                    debug!(
                        user = %ann.user,
                        digest = %ann.state_digest,
                        bytes = payload.len(),
                        "filesync announcement sent"
                    );
                }
            }
            // Periodic no-op wake so we don't block forever if no announcements.
            _ = tokio::time::sleep(Duration::from_secs(60)) => {}
        }
    }
}

fn create_multicast_socket() -> anyhow::Result<Socket> {
    let socket = Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::UDP))?;
    socket.set_reuse_address(true)?;
    #[cfg(not(windows))]
    socket.set_reuse_port(true)?;
    socket.bind(&SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, MULTICAST_PORT).into())?;
    socket.join_multicast_v4(&MULTICAST_ADDR, &Ipv4Addr::UNSPECIFIED)?;
    Ok(socket)
}

/// Serialize an announcement into a UDP payload.
fn serialize(ann: &Announcement) -> Option<Vec<u8>> {
    let json = serde_json::to_vec(ann).ok()?;
    let mut out = Vec::with_capacity(HEADER.len() + 1 + json.len());
    out.extend_from_slice(HEADER.as_bytes());
    out.push(b'\n');
    out.extend_from_slice(&json);
    Some(out)
}

/// Parse a UDP payload back into an announcement.
fn parse(bytes: &[u8]) -> Option<Announcement> {
    let newline = bytes.iter().position(|b| *b == b'\n')?;
    let header = std::str::from_utf8(&bytes[..newline]).ok()?;
    if header != HEADER {
        return None;
    }
    serde_json::from_slice(&bytes[newline + 1..]).ok()
}

// Keep Arc used so the compiler doesn't warn if the import is trimmed later.
#[allow(dead_code)]
fn _arc_marker() -> Arc<()> {
    Arc::new(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_announcement() {
        let ann = Announcement {
            machine: "laptop".into(),
            user: "dan".into(),
            emitted_at: 1000,
            port: 1531,
            generation: 1000,
            state_digest: "deadbeef".into(),
        };
        let bytes = serialize(&ann).unwrap();
        let parsed = parse(&bytes).unwrap();
        assert_eq!(parsed.machine, "laptop");
        assert_eq!(parsed.user, "dan");
        assert_eq!(parsed.state_digest, "deadbeef");
    }

    #[test]
    fn parse_rejects_unknown_header() {
        let bad = b"VIGO-SWARM\n{}";
        assert!(parse(bad).is_none());
    }

    /// The announcement must never carry file paths, hashes, or mtimes.
    /// Regression guard — the whole point of 0.30.11's wire-format
    /// change is to stop leaking that metadata to anyone on the LAN.
    #[test]
    fn announcement_reveals_no_file_metadata() {
        let ann = Announcement {
            machine: "laptop".into(),
            user: "dan".into(),
            emitted_at: 1000,
            port: 1531,
            generation: 1000,
            state_digest: "abc123".into(),
        };
        let bytes = serialize(&ann).unwrap();
        let as_str = std::str::from_utf8(&bytes).unwrap();
        assert!(!as_str.contains("files"), "announcement must not contain 'files'");
        assert!(!as_str.contains("tombstones"), "announcement must not contain 'tombstones'");
        assert!(!as_str.contains("mtime"), "announcement must not contain 'mtime'");
        assert!(!as_str.contains("sha256"), "announcement must not contain 'sha256'");
    }
}
