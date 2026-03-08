package grpc

import (
	"context"
	"errors"
	"log/slog"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Register handles the HobnobAgent.Register RPC. It delegates to the enrollment
// registry to validate the one-time token, match approved patterns, and create
// the client record.
func (s *AgentService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	slog.Info("register request",
		"hostname", req.GetHostname(),
		"ip", req.GetIpAddress(),
	)

	nodeID, err := s.registry.Register(ctx,
		req.GetHostname(),
		req.GetIpAddress(),
		req.GetPubkeyPem(),
		req.GetToken(),
	)
	if err != nil {
		slog.Warn("registration failed",
			"hostname", req.GetHostname(),
			"error", err,
		)

		switch {
		case errors.Is(err, registry.ErrNoMatchingPattern):
			return &pb.RegisterResponse{
				Success: false,
				Message: "no matching approved pattern found",
			}, nil
		case errors.Is(err, registry.ErrHostnameRevoked):
			return nil, status.Error(codes.PermissionDenied,
				"hostname belongs to a revoked node and cannot re-enroll")
		default:
			return nil, status.Errorf(codes.Internal, "registration failed: %v", err)
		}
	}

	slog.Info("node registered",
		"hostname", req.GetHostname(),
		"node_id", nodeID.String(),
	)

	// Dispatch node.enrolled webhook event.
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(ctx, "node.enrolled", webhook.EventPayload{
			Node: &webhook.NodeInfo{
				ID:       nodeID.String(),
				Hostname: req.GetHostname(),
				IP:       req.GetIpAddress(),
			},
		})
	}

	return &pb.RegisterResponse{
		Success:  true,
		NodeId: nodeID.String(),
		Message:  "registered successfully",
	}, nil
}
