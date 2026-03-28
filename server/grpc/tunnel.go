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
//
// Phase 1: validates handshake and logs. Tunnel management wired in Phase 2.
func (s *AgentService) TunnelStream(stream pb.VigoAgent_TunnelStreamServer) error {
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

	// Phase 2 will wire this to the tunnel manager and guacd bridge.
	// For now, return Unimplemented after successful handshake validation.
	return status.Error(codes.Unimplemented, "tunnel manager not yet wired (Phase 2)")
}
