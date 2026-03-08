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

	clientID, err := s.registry.Register(ctx,
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
				"hostname belongs to a revoked client and cannot re-enroll")
		default:
			return nil, status.Errorf(codes.Internal, "registration failed: %v", err)
		}
	}

	slog.Info("client registered",
		"hostname", req.GetHostname(),
		"client_id", clientID.String(),
	)

	// Dispatch client.enrolled webhook event.
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(ctx, "client.enrolled", webhook.EventPayload{
			Client: &webhook.ClientInfo{
				ID:       clientID.String(),
				Hostname: req.GetHostname(),
				IP:       req.GetIpAddress(),
			},
		})
	}

	return &pb.RegisterResponse{
		Success:  true,
		ClientId: clientID.String(),
		Message:  "registered successfully",
	}, nil
}
