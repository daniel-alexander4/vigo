// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Persistent bidirectional gRPC stream to the server.
//!
//! Mirrors the Go `StreamConn` — wraps a bidi stream with typed channels for
//! server-initiated pushes (policy change, force push) and request-response
//! correlation (check-in, report-result).

use crate::proto::{
    agent_stream_message, server_stream_message, AgentStreamMessage, CheckInRequest,
    CheckInResponse, DeltaAck, QueryRequest, ReportResultRequest, ServerStreamMessage,
    StreamPolicyPush, TaskDispatch, TunnelRequest,
};
use anyhow::{Context, Result};
use tokio::sync::mpsc;
use tonic::Streaming;
use tracing::warn;

use crate::proto::vigo_agent_client::VigoAgentClient;
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
    /// Server-dispatched ad-hoc tasks.
    pub task_dispatch_rx: mpsc::Receiver<TaskDispatch>,
    /// Server-dispatched live queries.
    pub query_request_rx: mpsc::Receiver<QueryRequest>,
    /// Delta acknowledgements from server.
    pub delta_ack_rx: mpsc::Receiver<DeltaAck>,
    /// Server requests stream closure (work complete).
    pub stream_release_rx: mpsc::Receiver<()>,
    /// Server requests the agent to open a TunnelStream (Scrier).
    pub tunnel_request_rx: mpsc::Receiver<TunnelRequest>,
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
    client: &mut VigoAgentClient<Channel>,
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
    let (task_dispatch_tx, task_dispatch_rx) = mpsc::channel::<TaskDispatch>(4);
    let (query_request_tx, query_request_rx) = mpsc::channel::<QueryRequest>(8);
    let (delta_ack_tx, delta_ack_rx) = mpsc::channel::<DeltaAck>(8);
    let (stream_release_tx, stream_release_rx) = mpsc::channel::<()>(1);
    let (tunnel_request_tx, tunnel_request_rx) = mpsc::channel::<TunnelRequest>(4);
    let (done_tx, done_rx) = tokio::sync::watch::channel(false);

    // Spawn the read loop.
    tokio::spawn(async move {
        read_loop(
            recv_stream,
            policy_push_tx,
            force_push_tx,
            check_in_resp_tx,
            task_dispatch_tx,
            query_request_tx,
            delta_ack_tx,
            stream_release_tx,
            tunnel_request_tx,
        )
        .await;
        let _ = done_tx.send(true); // signal disconnect
    });

    let handle = StreamHandle {
        request_tx,
        policy_push_rx,
        force_push_rx,
        check_in_resp_rx,
        task_dispatch_rx,
        query_request_rx,
        delta_ack_rx,
        stream_release_rx,
        tunnel_request_rx,
        done: done_rx,
    };

    Ok((handle, initial_resp))
}

/// Read server messages and dispatch to channels.
#[allow(clippy::too_many_arguments)]
async fn read_loop(
    mut stream: Streaming<ServerStreamMessage>,
    policy_push_tx: mpsc::Sender<StreamPolicyPush>,
    force_push_tx: mpsc::Sender<()>,
    check_in_resp_tx: mpsc::Sender<CheckInResponse>,
    task_dispatch_tx: mpsc::Sender<TaskDispatch>,
    query_request_tx: mpsc::Sender<QueryRequest>,
    delta_ack_tx: mpsc::Sender<DeltaAck>,
    stream_release_tx: mpsc::Sender<()>,
    tunnel_request_tx: mpsc::Sender<TunnelRequest>,
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
                        server_stream_message::Msg::TaskDispatch(dispatch) => {
                            if task_dispatch_tx.try_send(dispatch).is_err() {
                                warn!("task dispatch dropped (buffer full)");
                            }
                        }
                        server_stream_message::Msg::QueryRequest(req) => {
                            if query_request_tx.try_send(req).is_err() {
                                warn!("query request dropped (buffer full)");
                            }
                        }
                        server_stream_message::Msg::DeltaAck(ack) => {
                            let _ = delta_ack_tx.try_send(ack);
                        }
                        server_stream_message::Msg::StreamRelease(_) => {
                            let _ = stream_release_tx.try_send(());
                        }
                        server_stream_message::Msg::TunnelRequest(req) => {
                            if tunnel_request_tx.try_send(req).is_err() {
                                warn!("tunnel request dropped (buffer full)");
                            }
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::agent_stream_message;

    /// Test that check_in sends a CheckIn message and returns the response.
    #[tokio::test]
    async fn check_in_sends_request_and_receives_response() {
        let (request_tx, mut request_rx) = mpsc::channel::<AgentStreamMessage>(4);
        let (resp_tx, mut resp_rx) = mpsc::channel::<CheckInResponse>(1);
        let (_done_tx, mut done_rx) = tokio::sync::watch::channel(false);

        let req = CheckInRequest {
            envoy_id: "test-uuid".into(),
            state_fingerprint: "fp-1".into(),
            ..Default::default()
        };

        // Spawn a task to handle the request and send back a response.
        tokio::spawn(async move {
            let msg = request_rx.recv().await.unwrap();
            // Verify it's a CheckIn message.
            match msg.msg {
                Some(agent_stream_message::Msg::CheckIn(ci)) => {
                    assert_eq!(ci.envoy_id, "test-uuid");
                    assert_eq!(ci.state_fingerprint, "fp-1");
                }
                other => panic!("expected CheckIn, got {:?}", other),
            }
            // Send response.
            resp_tx
                .send(CheckInResponse {
                    interval: "30s".into(),
                    no_change: true,
                    ..Default::default()
                })
                .await
                .unwrap();
        });

        let resp = StreamHandle::check_in(&request_tx, &mut resp_rx, &mut done_rx, req)
            .await
            .unwrap();
        assert!(resp.no_change);
        assert_eq!(resp.interval, "30s");
    }

    /// Test that check_in returns an error when the stream disconnects.
    #[tokio::test]
    async fn check_in_errors_on_disconnect() {
        let (request_tx, _request_rx) = mpsc::channel::<AgentStreamMessage>(4);
        let (_resp_tx, mut resp_rx) = mpsc::channel::<CheckInResponse>(1);
        let (done_tx, mut done_rx) = tokio::sync::watch::channel(false);

        let req = CheckInRequest::default();

        // Signal disconnect before any response arrives.
        tokio::spawn(async move {
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
            let _ = done_tx.send(true);
        });

        let result = StreamHandle::check_in(&request_tx, &mut resp_rx, &mut done_rx, req).await;
        assert!(result.is_err());
        assert!(
            result.unwrap_err().to_string().contains("disconnected"),
            "expected disconnected error"
        );
    }

    /// Test that check_in returns an error when the response channel closes.
    #[tokio::test]
    async fn check_in_errors_on_channel_close() {
        let (request_tx, _request_rx) = mpsc::channel::<AgentStreamMessage>(4);
        let (resp_tx, mut resp_rx) = mpsc::channel::<CheckInResponse>(1);
        let (_done_tx, mut done_rx) = tokio::sync::watch::channel(false);

        let req = CheckInRequest::default();

        // Drop the response sender so recv returns None.
        tokio::spawn(async move {
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
            drop(resp_tx);
        });

        let result = StreamHandle::check_in(&request_tx, &mut resp_rx, &mut done_rx, req).await;
        assert!(result.is_err());
    }

    /// Test that check_in errors when the request_tx side is closed.
    #[tokio::test]
    async fn check_in_errors_on_send_failure() {
        let (request_tx, request_rx) = mpsc::channel::<AgentStreamMessage>(4);
        let (_resp_tx, mut resp_rx) = mpsc::channel::<CheckInResponse>(1);
        let (_done_tx, mut done_rx) = tokio::sync::watch::channel(false);

        // Drop receiver so send fails.
        drop(request_rx);

        let result =
            StreamHandle::check_in(&request_tx, &mut resp_rx, &mut done_rx, CheckInRequest::default())
                .await;
        assert!(result.is_err());
        assert!(result.unwrap_err().to_string().contains("stream closed"));
    }

    /// Test that report_result sends the message on the channel.
    #[tokio::test]
    async fn report_result_sends_message() {
        let (request_tx, mut request_rx) = mpsc::channel::<AgentStreamMessage>(4);

        let req = ReportResultRequest {
            run_id: "run-1".into(),
            envoy_id: "uuid-1".into(),
            ..Default::default()
        };

        StreamHandle::report_result(&request_tx, req).await.unwrap();

        let msg = request_rx.recv().await.unwrap();
        match msg.msg {
            Some(agent_stream_message::Msg::ReportResult(rr)) => {
                assert_eq!(rr.run_id, "run-1");
                assert_eq!(rr.envoy_id, "uuid-1");
            }
            other => panic!("expected ReportResult, got {:?}", other),
        }
    }

    /// Test that report_result errors when the channel is closed.
    #[tokio::test]
    async fn report_result_errors_on_closed_channel() {
        let (request_tx, request_rx) = mpsc::channel::<AgentStreamMessage>(4);
        drop(request_rx);

        let result =
            StreamHandle::report_result(&request_tx, ReportResultRequest::default()).await;
        assert!(result.is_err());
        assert!(result.unwrap_err().to_string().contains("stream closed"));
    }

    /// Test the done watch channel signals correctly.
    #[tokio::test]
    async fn done_watch_signals_disconnect() {
        let (done_tx, mut done_rx) = tokio::sync::watch::channel(false);

        assert!(!*done_rx.borrow());

        let _ = done_tx.send(true);

        done_rx.changed().await.unwrap();
        assert!(*done_rx.borrow());
    }

    /// Test that multiple report_results can be sent in sequence.
    #[tokio::test]
    async fn report_result_multiple_sends() {
        let (request_tx, mut request_rx) = mpsc::channel::<AgentStreamMessage>(16);

        for i in 0..5 {
            let req = ReportResultRequest {
                run_id: format!("run-{}", i),
                ..Default::default()
            };
            StreamHandle::report_result(&request_tx, req).await.unwrap();
        }

        for i in 0..5 {
            let msg = request_rx.recv().await.unwrap();
            match msg.msg {
                Some(agent_stream_message::Msg::ReportResult(rr)) => {
                    assert_eq!(rr.run_id, format!("run-{}", i));
                }
                other => panic!("expected ReportResult, got {:?}", other),
            }
        }
    }

    /// Test that the stream_release channel receives a signal when sent.
    #[tokio::test]
    async fn test_stream_release_channel() {
        let (tx, mut rx) = mpsc::channel::<()>(1);

        tx.try_send(()).expect("send should succeed on fresh channel");

        let result = rx.recv().await;
        assert_eq!(result, Some(()));
    }

    /// Test that StreamHandle can be constructed with the stream_release_rx field.
    #[tokio::test]
    async fn test_stream_handle_has_release_channel() {
        let (request_tx, _) = mpsc::channel::<AgentStreamMessage>(4);
        let (_, policy_push_rx) = mpsc::channel::<StreamPolicyPush>(4);
        let (_, force_push_rx) = mpsc::channel::<()>(4);
        let (_, check_in_resp_rx) = mpsc::channel::<CheckInResponse>(1);
        let (_, task_dispatch_rx) = mpsc::channel::<TaskDispatch>(4);
        let (_, query_request_rx) = mpsc::channel::<QueryRequest>(8);
        let (_, delta_ack_rx) = mpsc::channel::<DeltaAck>(8);
        let (_, stream_release_rx) = mpsc::channel::<()>(1);
        let (_, done) = tokio::sync::watch::channel(false);

        let handle = StreamHandle {
            request_tx,
            policy_push_rx,
            force_push_rx,
            check_in_resp_rx,
            task_dispatch_rx,
            query_request_rx,
            delta_ack_rx,
            stream_release_rx,
            done,
        };

        // Verify the stream_release_rx field is accessible and the channel is open.
        assert!(handle.stream_release_rx.is_empty());
    }

    /// Test that try_send on the stream_release channel succeeds once, then fills the buffer.
    #[tokio::test]
    async fn test_stream_release_try_send() {
        let (tx, mut rx) = mpsc::channel::<()>(1);

        // First send should succeed (buffer size 1).
        assert!(tx.try_send(()).is_ok());

        // Second send should fail — buffer is full.
        assert!(tx.try_send(()).is_err());

        // Drain the channel and verify we can send again.
        let val = rx.recv().await;
        assert_eq!(val, Some(()));

        assert!(tx.try_send(()).is_ok());
    }
}
