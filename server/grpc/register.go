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
// the client record. In hub mode, after token validation, the node is enrolled
// on the target spoke and the spoke address is returned to the agent.
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

	var spokeAddr string

	// Hub mode: route the enrollment to the target spoke.
	if s.hub != nil {
		addr, matched := s.hub.RouteHostname(req.GetHostname())
		if matched {
			spokeNodeID, enrollErr := s.hub.EnrollOnSpoke(ctx, addr, req.GetHostname(), req.GetIpAddress(), req.GetPubkeyPem())
			if enrollErr != nil {
				slog.Error("spoke enrollment failed, node registered on hub only",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"error", enrollErr,
				)
			} else {
				// Spoke created the node — use the spoke's node ID and address.
				nodeID = spokeNodeID
				spokeAddr = addr
				slog.Info("node enrolled on spoke",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"node_id", nodeID,
				)
			}
		}
	}

	slog.Info("node registered",
		"hostname", req.GetHostname(),
		"node_id", nodeID,
	)

	// Dispatch node.enrolled webhook event.
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(ctx, "node.enrolled", webhook.EventPayload{
			Node: &webhook.NodeInfo{
				ID:       nodeID,
				Hostname: req.GetHostname(),
				IP:       req.GetIpAddress(),
			},
		})
	}

	return &pb.RegisterResponse{
		Success:   true,
		NodeId:    nodeID,
		Message:   "registered successfully",
		SpokeAddr: spokeAddr,
	}, nil
}
