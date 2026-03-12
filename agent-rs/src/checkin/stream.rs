// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Persistent bidirectional gRPC stream to the server.
//!
//! Mirrors the Go `StreamConn` — wraps a bidi stream with typed channels for
//! server-initiated pushes (policy change, force push) and request-response
//! correlation (check-in, report-result).

use crate::proto::{
    agent_stream_message, server_stream_message, AgentStreamMessage, CheckInRequest,
    CheckInResponse, ReportResultRequest, ServerStreamMessage, StreamPolicyPush,
};
use anyhow::{Context, Result};
use tokio::sync::mpsc;
use tonic::Streaming;
use tracing::warn;

use crate::proto::hobnob_agent_client::HobnobAgentClient;
use tonic::transport::Channel;

/// Channel-based stream handle for sending and receiving on a bidi stream.
pub struct StreamHandle {
    /// Send requests on the stream.
    pub request_tx: mpsc::Sender<AgentStreamMessage>,
    /// Server-initiated policy pushes.
    pub policy_push_rx: mpsc::Receiver<StreamPolicyPush>,
    /// Server-initiated force pushes.
    pub force_push_rx: mpsc::Receiver<()>,
    /// Check-in response correlation.
    pub check_in_resp_rx: mpsc::Receiver<CheckInResponse>,
    /// Notified when the read loop exits. Clone-friendly.
    pub done: tokio::sync::watch::Receiver<bool>,
}

impl StreamHandle {
    /// Send a check-in request and wait for the response.
    pub async fn check_in(
        request_tx: &mpsc::Sender<AgentStreamMessage>,
        check_in_resp_rx: &mut mpsc::Receiver<CheckInResponse>,
        done: &mut tokio::sync::watch::Receiver<bool>,
        req: CheckInRequest,
    ) -> Result<CheckInResponse> {
        request_tx
            .send(AgentStreamMessage {
                msg: Some(agent_stream_message::Msg::CheckIn(req)),
            })
            .await
            .map_err(|_| anyhow::anyhow!("stream closed"))?;

        tokio::select! {
            resp = check_in_resp_rx.recv() => {
                resp.ok_or_else(|| anyhow::anyhow!("stream closed while waiting for check-in response"))
            }
            _ = done.changed() => {
                anyhow::bail!("stream disconnected")
            }
        }
    }

    /// Send a report-result (fire-and-forget).
    pub async fn report_result(
        request_tx: &mpsc::Sender<AgentStreamMessage>,
        req: ReportResultRequest,
    ) -> Result<()> {
        request_tx
            .send(AgentStreamMessage {
                msg: Some(agent_stream_message::Msg::ReportResult(req)),
            })
            .await
            .map_err(|_| anyhow::anyhow!("stream closed"))?;
        Ok(())
    }
}

/// Open a bidirectional stream, perform the handshake, and return a StreamHandle.
pub async fn open(
    client: &mut HobnobAgentClient<Channel>,
    handshake_req: CheckInRequest,
) -> Result<(StreamHandle, CheckInResponse)> {
    // Create channel for outgoing requests.
    let (request_tx, request_rx) = mpsc::channel::<AgentStreamMessage>(16);

    // Send the handshake as the first message, then forward from the channel.
    let handshake_msg = AgentStreamMessage {
        msg: Some(agent_stream_message::Msg::CheckIn(handshake_req)),
    };

    let request_stream = async_stream::stream! {
        yield handshake_msg;
        let mut rx = request_rx;
        while let Some(msg) = rx.recv().await {
            yield msg;
        }
    };

    let response = client
        .agent_stream(request_stream)
        .await
        .context("open agent stream")?;

    let mut recv_stream = response.into_inner();

    // Wait for the handshake response.
    let first_msg = recv_stream
        .message()
        .await
        .context("recv handshake")?
        .ok_or_else(|| anyhow::anyhow!("stream closed during handshake"))?;

    let initial_resp = match first_msg.msg {
        Some(server_stream_message::Msg::CheckInResponse(resp)) => resp,
        other => anyhow::bail!(
            "expected check_in_response in handshake, got {:?}",
            other.map(|m| std::mem::discriminant(&m))
        ),
    };

    // Create dispatch channels.
    let (policy_push_tx, policy_push_rx) = mpsc::channel::<StreamPolicyPush>(4);
    let (force_push_tx, force_push_rx) = mpsc::channel::<()>(4);
    let (check_in_resp_tx, check_in_resp_rx) = mpsc::channel::<CheckInResponse>(1);
    let (done_tx, done_rx) = tokio::sync::watch::channel(false);

    // Spawn the read loop.
    tokio::spawn(async move {
        read_loop(recv_stream, policy_push_tx, force_push_tx, check_in_resp_tx).await;
        let _ = done_tx.send(true); // signal disconnect
    });

    let handle = StreamHandle {
        request_tx,
        policy_push_rx,
        force_push_rx,
        check_in_resp_rx,
        done: done_rx,
    };

    Ok((handle, initial_resp))
}

/// Read server messages and dispatch to channels.
async fn read_loop(
    mut stream: Streaming<ServerStreamMessage>,
    policy_push_tx: mpsc::Sender<StreamPolicyPush>,
    force_push_tx: mpsc::Sender<()>,
    check_in_resp_tx: mpsc::Sender<CheckInResponse>,
) {
    loop {
        match stream.message().await {
            Ok(Some(msg)) => {
                if let Some(m) = msg.msg {
                    match m {
                        server_stream_message::Msg::CheckInResponse(resp) => {
                            let _ = check_in_resp_tx.try_send(resp);
                        }
                        server_stream_message::Msg::ReportResultAck(_) => {
                            // Fire-and-forget — drain silently.
                        }
                        server_stream_message::Msg::ReportTraitsAck(_) => {
                            // Traits delivered inline — ignore ack.
                        }
                        server_stream_message::Msg::PolicyPush(push) => {
                            if policy_push_tx.try_send(push).is_err() {
                                warn!("policy push dropped (buffer full)");
                            }
                        }
                        server_stream_message::Msg::ForcePush(_) => {
                            let _ = force_push_tx.try_send(());
                        }
                    }
                }
            }
            Ok(None) => return,
            Err(e) => {
                warn!(error = %e, "stream read error");
                return;
            }
        }
    }
}
