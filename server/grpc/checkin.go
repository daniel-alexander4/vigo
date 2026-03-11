package grpc

import (
	"context"
	"errors"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckIn handles the HobnobAgent.CheckIn RPC. It validates the client,
// stores inline traits for admin queries, and returns a policy bundle for
// agent-side evaluation. The server never renders templates or evaluates
// conditionals — the agent does that locally with its own traits.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	if _, err := uuid.Parse(req.GetNodeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}
	nodeID := req.GetNodeId()

	// Validate node is not revoked.
	if err := s.registry.ValidateNode(ctx, nodeID); err != nil {
		metrics.CheckinsTotal.WithLabelValues("error").Inc()
		if errors.Is(err, registry.ErrNodeRevoked) {
			return nil, status.Error(codes.PermissionDenied, "node is revoked")
		}
		if errors.Is(err, registry.ErrNodeNotFound) {
			return nil, status.Error(codes.NotFound, "node not found")
		}
		return nil, status.Errorf(codes.Internal, "validating node: %v", err)
	}

	// Reuse node from interceptor context if available, otherwise cache lookup.
	node, err := s.lookupNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	return s.buildCheckInResponse(ctx, nodeID, node, req)
}

// lookupNode resolves a node from context, cache, or DB.
func (s *AgentService) lookupNode(ctx context.Context, nodeID string) (db.Node, error) {
	if n := db.NodeFromContext(ctx); n != nil {
		return *n, nil
	}
	if s.nodeCache != nil {
		n, err := s.nodeCache.GetByID(ctx, nodeID)
		if err != nil {
			return db.Node{}, status.Errorf(codes.Internal, "looking up node: %v", err)
		}
		return n, nil
	}
	n, err := s.queries.GetNodeByID(ctx, nodeID)
	if err != nil {
		return db.Node{}, status.Errorf(codes.Internal, "looking up node: %v", err)
	}
	return n, nil
}
