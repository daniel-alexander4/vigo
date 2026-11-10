// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"log/slog"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TunnelStream handles the Scrier remote access tunnel. The agent opens this
// on a separate gRPC connection (same mTLS certs) to relay bytes between
// a localhost service (RDP/SSH) and the server's guacd bridge.
func (s *AgentService) TunnelStream(stream pb.VigoAgent_TunnelStreamServer) error {
	if s.tunnelMgr == nil {
		return status.Error(codes.Unavailable, "scrier remote access is not enabled")
	}

	// First message must be TunnelHandshake.
	msg, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to receive handshake: %v", err)
	}

	hs := msg.GetHandshake()
	if hs == nil {
		return status.Error(codes.InvalidArgument, "first message must be TunnelHandshake")
	}

	envoyID := hs.EnvoyId
	if envoyID == "" {
		return status.Error(codes.InvalidArgument, "envoy_id required in handshake")
	}
	if len(hs.Signature) == 0 {
		return status.Error(codes.InvalidArgument, "signature required in handshake")
	}

	// Verify the handshake through the same gates as every other agent RPC:
	// revocation + mTLS cert pin (lookupEnvoyAndPubKey), the signature-window
	// timestamp check (an old handshake signature can't be replayed forever),
	// then the ed25519 signature over envoy_id+timestamp.
	envoy, err := verifyEnvoyIDSignature(stream.Context(), s.queries, envoyID, hs.Timestamp, hs.Signature, s.signatureWindow, s.fleetIndex, s.metrics)
	if err != nil {
		slog.Warn("scrier: tunnel handshake rejected", "envoy_id", envoyID, "error", err)
		return err
	}

	slog.Info("scrier tunnel stream opened",
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
	)

	// The agent opened this TunnelStream in response to a TunnelRequest.
	// Deliver the pending tunnel and start relaying.
	tunnel := s.tunnelMgr.deliverPendingTunnel(envoyID)
	if tunnel == nil {
		return status.Errorf(codes.FailedPrecondition, "no pending tunnel for envoy %q", envoyID)
	}

	return s.runTunnelRelay(stream, tunnel, envoyID, envoy.Hostname)
}

// runTunnelRelay bridges an assigned tunnel between the agent gRPC stream and
// the tunnel's guacd channels.
func (s *AgentService) runTunnelRelay(
	stream pb.VigoAgent_TunnelStreamServer,
	tunnel *activeTunnel,
	envoyID, hostname string,
) error {
	// Send TunnelOpen to agent, including SSH pubkeys for auth if present.
	err := stream.Send(&pb.TunnelServerMessage{
		Msg: &pb.TunnelServerMessage_Open{
			Open: &pb.TunnelOpen{
				TunnelId:           tunnel.tunnelID,
				TargetHost:         "127.0.0.1",
				TargetPort:         tunnel.targetPort,
				Protocol:           tunnel.protocol,
				SshVerifyPubkey:    tunnel.sshVerifyPubkey,
				SshEphemeralPubkey: tunnel.sshEphemeralPubkey,
			},
		},
	})
	if err != nil {
		s.tunnelMgr.closeTunnel(tunnel.tunnelID)
		return status.Errorf(codes.Internal, "send tunnel open: %v", err)
	}

	// Wait for TunnelOpenResult from agent.
	msg, err := stream.Recv()
	if err != nil {
		s.tunnelMgr.closeTunnel(tunnel.tunnelID)
		return status.Errorf(codes.Internal, "recv open result: %v", err)
	}

	result := msg.GetOpenResult()
	if result == nil {
		s.tunnelMgr.closeTunnel(tunnel.tunnelID)
		return status.Error(codes.InvalidArgument, "expected TunnelOpenResult")
	}
	if !result.Success {
		// Surface the agent's reason to scrier's setupSSH via sshAuthCh, then
		// return. Do NOT closeTunnel here — setupSSH needs the tunnel alive
		// just long enough to read the error and surface it as the session's
		// failure message. setupSSH closes the tunnel after consuming it.
		select {
		case tunnel.sshAuthCh <- sshAuthResult{ErrorMessage: result.ErrorMessage}:
		default:
		}
		slog.Warn("agent failed to open tunnel target",
			"tunnel_id", tunnel.tunnelID,
			"error", result.ErrorMessage,
		)
		return status.Errorf(codes.FailedPrecondition, "agent: %s", result.ErrorMessage)
	}

	// Signal the username to waiters and continue into the relay loop.
	select {
	case tunnel.sshAuthCh <- sshAuthResult{Username: result.SshUsername}:
	default:
	}

	slog.Info("scrier tunnel relay started",
		"tunnel_id", tunnel.tunnelID,
		"envoy_id", envoyID,
		"hostname", hostname,
		"protocol", tunnel.protocol,
		"port", tunnel.targetPort,
	)

	// Bidirectional relay: agent stream ↔ tunnel channels ↔ guacd.
	done := make(chan struct{})

	// Agent → guacd: read TunnelData from stream, write to tunnel.agentRecv.
	go func() {
		defer daemon.Recover("scrier-tunnel-agent-recv", nil)
		defer close(done)
		recvCount := 0
		totalBytes := 0
		for {
			msg, err := stream.Recv()
			if err != nil {
				slog.Info("scrier gRPC: agent stream recv ended",
					"tunnel_id", tunnel.tunnelID, "error", err,
					"msgs", recvCount, "total_bytes", totalBytes)
				return
			}
			switch m := msg.GetMsg().(type) {
			case *pb.TunnelClientMessage_Data:
				recvCount++
				totalBytes += len(m.Data.Payload)
				select {
				case tunnel.agentRecv <- m.Data.Payload:
				case <-tunnel.done:
					return
				}
			case *pb.TunnelClientMessage_Close:
				slog.Info("agent closed tunnel",
					"tunnel_id", tunnel.tunnelID,
					"reason", m.Close.Reason,
				)
				return
			}
		}
	}()

	// guacd → agent: read from tunnel.agentSend, send as TunnelData on stream.
	go func() {
		defer daemon.Recover("scrier-tunnel-agent-send", nil)
		sendCount := 0
		totalBytes := 0
		for {
			select {
			case data, ok := <-tunnel.agentSend:
				if !ok {
					return
				}
				sendCount++
				totalBytes += len(data)
				err := stream.Send(&pb.TunnelServerMessage{
					Msg: &pb.TunnelServerMessage_Data{
						Data: &pb.TunnelData{
							TunnelId: tunnel.tunnelID,
							Payload:  data,
						},
					},
				})
				if err != nil {
					slog.Warn("scrier gRPC: server→agent send error",
						"tunnel_id", tunnel.tunnelID, "error", err)
					return
				}
			case <-tunnel.done:
				// Send close to agent.
				_ = stream.Send(&pb.TunnelServerMessage{
					Msg: &pb.TunnelServerMessage_Close{
						Close: &pb.TunnelClose{
							TunnelId: tunnel.tunnelID,
							Reason:   "guacd_closed",
						},
					},
				})
				return
			case <-done:
				return
			}
		}
	}()

	// Wait for either direction to finish.
	select {
	case <-done:
	case <-tunnel.done:
	case <-stream.Context().Done():
	}

	s.tunnelMgr.closeTunnel(tunnel.tunnelID)
	return nil
}
