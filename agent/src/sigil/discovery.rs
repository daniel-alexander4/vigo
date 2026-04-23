// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! UDP multicast announcer + listener for sigil.
//!
//! Mirrors echo/discovery.rs in shape but on its own multicast
//! group/port and with its own envelope header so the two subsystems
//! never confuse each other's traffic on the wire.
//!
//! Message format (LAN-local plaintext, line-delimited):
//! ```text
//! SIGIL-1
//! <json body>
//! ```

use std::net::{Ipv4Addr, SocketAddr, SocketAddrV4};
use std::time::Duration;

use socket2::{Domain, Protocol, Socket, Type};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use super::state::Announcement;

/// Multicast group for sigil — distinct from echo's 224.0.0.43
/// and swarm's 224.0.0.42. Keep stable across versions.
pub const MULTICAST_ADDR: Ipv4Addr = Ipv4Addr::new(224, 0, 0, 44);
pub const MULTICAST_PORT: u16 = 1533;
pub const HEADER: &str = "SIGIL-1";

/// Max UDP payload. Sigil announcements are tiny (pubkey +
/// hostname + 3 small integers — a few hundred bytes at most), but
/// we size for safety.
pub const MAX_PAYLOAD: usize = 4 * 1024;

/// Received announcement with the sender's socket address attached.
#[derive(Debug)]
pub struct IncomingAnnouncement {
    pub from: SocketAddr,
    pub announcement: Announcement,
}

/// Spawn announcer and listener tasks.
///
/// Returns three channel ends: an outbound sender for announcements
/// we want to emit, plus both ends of the inbound announcement
/// channel. The inbound sender is cloned into the HTTPS
/// `POST /sigil/announce` handler so cross-subnet peers can feed
/// announcements into the same reconciler path as multicast.
pub fn spawn(
    cancel: CancellationToken,
) -> (
    mpsc::Sender<Announcement>,
    mpsc::Sender<IncomingAnnouncement>,
    mpsc::Receiver<IncomingAnnouncement>,
) {
    let (out_tx, out_rx) = mpsc::channel::<Announcement>(32);
    let (in_tx, in_rx) = mpsc::channel::<IncomingAnnouncement>(64);

    // Listener.
    let cancel_listen = cancel.clone();
    let in_tx_listener = in_tx.clone();
    tokio::spawn(async move {
        if let Err(e) = listen_loop(in_tx_listener, cancel_listen).await {
            warn!(error = %e, "sigil multicast listener exited");
        }
    });

    // Announcer.
    let cancel_announce = cancel.clone();
    tokio::spawn(async move {
        if let Err(e) = announce_loop(out_rx, cancel_announce).await {
            warn!(error = %e, "sigil multicast announcer exited");
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

    info!(
        "sigil multicast listener started on {}:{}",
        MULTICAST_ADDR, MULTICAST_PORT
    );

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
                            debug!(error = %e, "sigil multicast recv error");
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

    info!("sigil multicast announcer started");

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
                            "sigil announcement too large, dropping"
                        );
                        continue;
                    }
                    None => continue,
                };
                if let Err(e) = udp.send_to(&payload, dest).await {
                    // warn! so firewall-drop bugs are visible — matches
                    // echo's diagnostic posture after 0.20.9's silent
                    // multicast-drop incident.
                    warn!(
                        error = %e,
                        dest = %dest,
                        bytes = payload.len(),
                        "sigil multicast send failed",
                    );
                } else {
                    debug!(
                        user = %ann.user,
                        machine = %ann.machine,
                        bytes = payload.len(),
                        "sigil announcement sent"
                    );
                }
            }
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
pub fn serialize(ann: &Announcement) -> Option<Vec<u8>> {
    let json = serde_json::to_vec(ann).ok()?;
    let mut out = Vec::with_capacity(HEADER.len() + 1 + json.len());
    out.extend_from_slice(HEADER.as_bytes());
    out.push(b'\n');
    out.extend_from_slice(&json);
    Some(out)
}

/// Parse a UDP payload back into an announcement.
pub fn parse(bytes: &[u8]) -> Option<Announcement> {
    let newline = bytes.iter().position(|b| *b == b'\n')?;
    let header = std::str::from_utf8(&bytes[..newline]).ok()?;
    if header != HEADER {
        return None;
    }
    serde_json::from_slice(&bytes[newline + 1..]).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_announcement() {
        let ann = Announcement {
            machine: "laptop".into(),
            user: "dan".into(),
            pubkey: "age1abc".into(),
            emitted_at: 1_700_000_000,
            port: 1531,
            generation: 1,
        };
        let bytes = serialize(&ann).unwrap();
        let parsed = parse(&bytes).unwrap();
        assert_eq!(parsed, ann);
    }

    #[test]
    fn parse_rejects_unknown_header() {
        let bad = b"ECHO-1\n{}";
        assert!(parse(bad).is_none());
    }

    #[test]
    fn parse_rejects_empty() {
        assert!(parse(b"").is_none());
    }

    #[test]
    fn parse_rejects_garbage_body() {
        let bad = b"SIGIL-1\nnot-json";
        assert!(parse(bad).is_none());
    }
}
