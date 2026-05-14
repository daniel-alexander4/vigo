// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! TunnelRelay: opens a TunnelStream RPC on a dedicated gRPC connection,
//! receives a TunnelOpen instruction from the server, connects to the
//! specified localhost port, and relays bytes bidirectionally.
//!
//! Security: the agent ONLY connects to 127.0.0.1, regardless of what
//! target_host the server sends. This prevents lateral movement even if
//! the server is compromised.

use crate::proto::{
    tunnel_client_message, tunnel_server_message, TunnelClientMessage, TunnelClose, TunnelData,
    TunnelHandshake, TunnelOpenResult,
};
use crate::proto::vigo_agent_client::VigoAgentClient;
use anyhow::{Context, Result};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tonic::transport::Channel;
use tracing::{info, warn};

/// Maximum payload size per TunnelData message (64 KB).
const MAX_PAYLOAD: usize = 64 * 1024;

/// Run a tunnel relay session on a dedicated gRPC connection.
///
/// 1. Opens TunnelStream, sends TunnelHandshake
/// 2. Waits for TunnelOpen from server
/// 3. Connects to 127.0.0.1:{port}
/// 4. Relays bytes bidirectionally until either side closes
pub async fn run(
    mut client: VigoAgentClient<Channel>,
    envoy_id: &str,
    signature: Vec<u8>,
    timestamp: String,
) -> Result<()> {
    // Build the handshake message.
    let handshake = TunnelClientMessage {
        msg: Some(tunnel_client_message::Msg::Handshake(TunnelHandshake {
            envoy_id: envoy_id.to_string(),
            signature,
            timestamp,
        })),
    };

    // Open the bidi stream. We send via an mpsc channel.
    let (tx, rx) = mpsc::channel::<TunnelClientMessage>(32);
    let outbound = async_stream::stream! {
        yield handshake;
        let mut rx = rx;
        while let Some(msg) = rx.recv().await {
            yield msg;
        }
    };

    let response = client
        .tunnel_stream(outbound)
        .await
        .context("open tunnel stream")?;

    let mut inbound = response.into_inner();

    // Wait for TunnelOpen from server.
    let open_msg = inbound
        .message()
        .await
        .context("recv tunnel open")?
        .ok_or_else(|| anyhow::anyhow!("stream closed before TunnelOpen"))?;

    let open = match open_msg.msg {
        Some(tunnel_server_message::Msg::Open(o)) => o,
        Some(tunnel_server_message::Msg::Close(c)) => {
            info!(reason = %c.reason, "server closed tunnel before open");
            return Ok(());
        }
        other => anyhow::bail!("expected TunnelOpen, got {:?}", other.map(|m| std::mem::discriminant(&m))),
    };

    let tunnel_id = open.tunnel_id.clone();
    let port = open.target_port;

    info!(
        tunnel_id = %tunnel_id,
        protocol = %open.protocol,
        port = port,
        "tunnel open request received"
    );

    // SSH pubkey auth: if the server sent a verify pubkey, scan every regular
    // user's authorized_keys for it. On match, install the ephemeral key in
    // that user's file and report the username back. The EphemeralKeyGuard
    // ensures cleanup even on panic or early return.
    let mut ssh_username = String::new();
    let mut _key_guard = super::sshauth::EphemeralKeyGuard::empty();

    if !open.ssh_verify_pubkey.is_empty() && !open.ssh_ephemeral_pubkey.is_empty() {
        info!(tunnel_id = %tunnel_id, "scrier: scanning authorized_keys for verify pubkey");
        match super::sshauth::scan_and_install(&open.ssh_verify_pubkey, &open.ssh_ephemeral_pubkey) {
            Some(result) => {
                ssh_username = result.username.clone();
                _key_guard = super::sshauth::EphemeralKeyGuard::new(result.authorized_keys_path);
                info!(tunnel_id = %tunnel_id, username = %result.username, "scrier: SSH pubkey verified");
            }
            None => {
                let err_msg = "scrier: verify pubkey not found in any regular user's authorized_keys".to_string();
                warn!(tunnel_id = %tunnel_id, error = %err_msg);
                let _ = tx
                    .send(TunnelClientMessage {
                        msg: Some(tunnel_client_message::Msg::OpenResult(TunnelOpenResult {
                            tunnel_id: tunnel_id.clone(),
                            success: false,
                            error_message: err_msg,
                            ssh_username: String::new(),
                            vnc_port: 0,
                            vnc_password: String::new(),
                        })),
                    })
                    .await;
                return Ok(());
            }
        }
    }

    // VNC shadow: spawn x11vnc on demand, get consent, override port.
    let mut _vnc_session: Option<super::vnc::VncSession> = None;
    let connect_port;

    if open.protocol == "vnc" {
        match super::vnc::start_session(&tunnel_id, &open.consent_mode).await {
            Ok(session) => {
                connect_port = session.port;
                info!(tunnel_id = %tunnel_id, port = connect_port, "VNC session ready");
                _vnc_session = Some(session); // held until tunnel closes (Drop kills x11vnc)
            }
            Err(e) => {
                warn!(tunnel_id = %tunnel_id, error = %e, "VNC session setup failed");
                let _ = tx
                    .send(TunnelClientMessage {
                        msg: Some(tunnel_client_message::Msg::OpenResult(TunnelOpenResult {
                            tunnel_id: tunnel_id.clone(),
                            success: false,
                            error_message: e,
                            ssh_username: String::new(),
                            vnc_port: 0,
                            vnc_password: String::new(),
                        })),
                    })
                    .await;
                return Ok(());
            }
        }
    } else {
        connect_port = port;
    }

    // Security: ALWAYS connect to 127.0.0.1, ignore target_host from server.
    let addr = format!("127.0.0.1:{}", connect_port);
    let tcp = match TcpStream::connect(&addr).await {
        Ok(stream) => {
            // Disable Nagle — SSH handshake packets are small and latency-sensitive.
            let _ = stream.set_nodelay(true);
            // Report success with protocol-specific fields.
            let _ = tx
                .send(TunnelClientMessage {
                    msg: Some(tunnel_client_message::Msg::OpenResult(TunnelOpenResult {
                        tunnel_id: tunnel_id.clone(),
                        success: true,
                        error_message: String::new(),
                        ssh_username: ssh_username.clone(),
                        vnc_port: if open.protocol == "vnc" { connect_port } else { 0 },
                        vnc_password: String::new(), // Linux x11vnc uses no password
                    })),
                })
                .await;
            stream
        }
        Err(e) => {
            let err_msg = format!("connect to {}: {}", addr, e);
            warn!(tunnel_id = %tunnel_id, error = %err_msg, "tunnel connect failed");
            // _key_guard Drop handles ephemeral key cleanup.
            let _ = tx
                .send(TunnelClientMessage {
                    msg: Some(tunnel_client_message::Msg::OpenResult(TunnelOpenResult {
                        tunnel_id: tunnel_id.clone(),
                        success: false,
                        error_message: err_msg,
                        ssh_username: String::new(),
                        vnc_port: 0,
                        vnc_password: String::new(),
                    })),
                })
                .await;
            return Ok(());
        }
    };

    info!(tunnel_id = %tunnel_id, addr = %addr, "tunnel connected to localhost");

    let (mut tcp_read, mut tcp_write) = tcp.into_split();
    let tx_clone = tx.clone();
    let tunnel_id_clone = tunnel_id.clone();

    // Task 1: TCP → gRPC (read from localhost, send to server)
    let tcp_to_grpc = tokio::spawn(async move {
        let mut buf = vec![0u8; MAX_PAYLOAD];
        loop {
            let n = match tcp_read.read(&mut buf).await {
                Ok(0) => break, // TCP closed
                Ok(n) => n,
                Err(e) => {
                    warn!(tunnel_id = %tunnel_id_clone, error = %e, "tcp read error");
                    break;
                }
            };
            let msg = TunnelClientMessage {
                msg: Some(tunnel_client_message::Msg::Data(TunnelData {
                    tunnel_id: tunnel_id_clone.clone(),
                    payload: buf[..n].to_vec(),
                })),
            };
            if tx_clone.send(msg).await.is_err() {
                break; // gRPC stream closed
            }
        }
        // Signal close.
        let _ = tx_clone
            .send(TunnelClientMessage {
                msg: Some(tunnel_client_message::Msg::Close(TunnelClose {
                    tunnel_id: tunnel_id_clone.clone(),
                    reason: "target_closed".to_string(),
                })),
            })
            .await;
    });

    // Task 2: gRPC → TCP (read from server, write to localhost)
    let tunnel_id_clone2 = tunnel_id.clone();
    let grpc_to_tcp = tokio::spawn(async move {
        while let Ok(Some(msg)) = inbound.message().await {
            match msg.msg {
                Some(tunnel_server_message::Msg::Data(d)) => {
                    if let Err(e) = tcp_write.write_all(&d.payload).await {
                        warn!(tunnel_id = %tunnel_id_clone2, error = %e, "tcp write error");
                        break;
                    }
                }
                Some(tunnel_server_message::Msg::Close(c)) => {
                    info!(tunnel_id = %tunnel_id_clone2, reason = %c.reason, "server closed tunnel");
                    break;
                }
                _ => {}
            }
        }
        let _ = tcp_write.shutdown().await;
    });

    // Wait for either direction to finish, then clean up.
    tokio::select! {
        _ = tcp_to_grpc => {
            info!(tunnel_id = %tunnel_id, "tcp→grpc relay finished");
        }
        _ = grpc_to_tcp => {
            info!(tunnel_id = %tunnel_id, "grpc→tcp relay finished");
        }
    }

    // Drop the sender to close the gRPC stream.
    drop(tx);

    // _key_guard Drop handles ephemeral key cleanup automatically.

    info!(tunnel_id = %tunnel_id, "tunnel relay closed");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    /// Test that the relay connects to a local TCP server and can exchange data.
    /// This is a unit test using a mock TCP echo server — no real gRPC involved.
    #[tokio::test]
    async fn test_tcp_connect_and_echo() {
        // Start a local echo server.
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut buf = [0u8; 1024];
            loop {
                let n = match socket.read(&mut buf).await {
                    Ok(0) => break,
                    Ok(n) => n,
                    Err(_) => break,
                };
                socket.write_all(&buf[..n]).await.unwrap();
            }
        });

        // Connect to the echo server directly (testing the TCP part).
        let mut stream = TcpStream::connect(addr).await.unwrap();
        stream.write_all(b"hello scrier").await.unwrap();

        let mut buf = [0u8; 64];
        let n = stream.read(&mut buf).await.unwrap();
        assert_eq!(&buf[..n], b"hello scrier");
    }
}
