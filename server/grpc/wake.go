// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Wake blocks until the server has urgent work for the agent or the
// check-in interval expires. The agent calls this instead of sleeping
// between check-in cycles, giving the server a way to wake unary agents
// instantly without persistent streaming overhead.
func (s *AgentService) Wake(ctx context.Context, req *pb.WakeRequest) (*pb.WakeResponse, error) {
	envoyID := req.GetEnvoyId()
	if envoyID == "" {
		return nil, status.Error(codes.InvalidArgument, "envoy_id required")
	}

	// Validate envoy exists and is not revoked.
	if s.fleetIndex != nil {
		_, exists, revoked := s.fleetIndex.GetEnvoyLite(envoyID)
		if !exists {
			return nil, status.Error(codes.NotFound, "envoy not found")
		}
		if revoked {
			return nil, status.Error(codes.PermissionDenied, "envoy is revoked")
		}
	}

	// Register a wake channel. The channel is signaled when any subsystem
	// needs this agent urgently (Scrier tunnel, task dispatch, etc.).
	ch := s.fleetIndex.RegisterWake(envoyID)
	defer s.fleetIndex.UnregisterWake(envoyID)

	slog.Debug("agent wake registered", "envoy_id", envoyID)

	select {
	case <-ch:
		slog.Info("agent wake signaled", "envoy_id", envoyID)
		return &pb.WakeResponse{Reason: "signaled"}, nil
	case <-time.After(s.baseInterval):
		return &pb.WakeResponse{}, nil
	case <-ctx.Done():
		return nil, status.Error(codes.Canceled, "client disconnected")
	}
}
