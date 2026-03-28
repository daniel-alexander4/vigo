// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"log/slog"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
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

	// Validate envoy exists and is not revoked.
	envoy, exists, revoked := s.fleetIndex.GetEnvoyLite(envoyID)
	if !exists {
		return status.Errorf(codes.NotFound, "envoy %q not found", envoyID)
	}
	if revoked {
		return status.Errorf(codes.PermissionDenied, "envoy %q is revoked", envoyID)
	}

	slog.Info("scrier tunnel stream opened",
		"envoy_id", envoyID,
		"hostname", envoy.Hostname,
	)

	// Wait for the tunnel manager to assign a tunnel to this stream.
	// The WebSocket handler (Phase 3) will call tunnelMgr.openTunnel() which
	// sends TunnelOpen to this agent. For now, we wait for data from the
	// agent side and relay it to the tunnel's channels.
	//
	// The flow is:
	// 1. WebSocket handler calls openTunnel() → creates activeTunnel with channels
	// 2. WebSocket handler signals this stream (via a pending tunnel registry)
	// 3. This handler sends TunnelOpen to the agent
	// 4. Agent connects to localhost, sends TunnelOpenResult
	// 5. Bidirectional relay: stream ↔ activeTunnel channels ↔ guacd

	// For Phase 2, we register this stream as available for tunneling.
	// The actual tunnel open will be triggered by the WebSocket handler in Phase 3.
	// For now, wait for agent messages and relay them.

	// Register this stream as available for the envoy.
	tunnelCh := make(chan *activeTunnel, 1)
	s.tunnelMgr.registerAgentStream(envoyID, tunnelCh)
	defer s.tunnelMgr.unregisterAgentStream(envoyID)

	// Wait for a tunnel assignment (from WebSocket handler) or stream close.
	select {
	case tunnel := <-tunnelCh:
		return s.runTunnelRelay(stream, tunnel, envoyID, envoy.Hostname)
	case <-stream.Context().Done():
		return nil
	}
}

// runTunnelRelay bridges an assigned tunnel between the agent gRPC stream and
// the tunnel's guacd channels.
func (s *AgentService) runTunnelRelay(
	stream pb.VigoAgent_TunnelStreamServer,
	tunnel *activeTunnel,
	envoyID, hostname string,
) error {
	// Send TunnelOpen to agent.
	err := stream.Send(&pb.TunnelServerMessage{
		Msg: &pb.TunnelServerMessage_Open{
			Open: &pb.TunnelOpen{
				TunnelId:   tunnel.tunnelID,
				TargetHost: "127.0.0.1",
				TargetPort: tunnel.targetPort,
				Protocol:   tunnel.protocol,
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
		s.tunnelMgr.closeTunnel(tunnel.tunnelID)
		slog.Warn("agent failed to open tunnel target",
			"tunnel_id", tunnel.tunnelID,
			"error", result.ErrorMessage,
		)
		return status.Errorf(codes.FailedPrecondition, "agent: %s", result.ErrorMessage)
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
		defer close(done)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			switch m := msg.GetMsg().(type) {
			case *pb.TunnelClientMessage_Data:
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
		for {
			select {
			case data, ok := <-tunnel.agentSend:
				if !ok {
					return
				}
				err := stream.Send(&pb.TunnelServerMessage{
					Msg: &pb.TunnelServerMessage_Data{
						Data: &pb.TunnelData{
							TunnelId: tunnel.tunnelID,
							Payload:  data,
						},
					},
				})
				if err != nil {
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
