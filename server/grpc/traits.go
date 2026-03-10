package grpc

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportTraits handles the HobnobAgent.ReportTraits RPC. It stores the agent's
// collected traits as JSONB in the database and prunes old entries to enforce
// a 10-snapshot retention policy (DB-04).
func (s *AgentService) ReportTraits(ctx context.Context, req *pb.ReportTraitsRequest) (*pb.ReportTraitsResponse, error) {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	traitsJSON := req.GetTraitsJson()
	if len(traitsJSON) == 0 {
		return nil, status.Error(codes.InvalidArgument, "traits_json must not be empty")
	}

	// Validate that traits_json is valid JSON.
	if !json.Valid(traitsJSON) {
		return nil, status.Error(codes.InvalidArgument, "traits_json is not valid JSON")
	}

	// Store the traits.
	_, err = s.queries.InsertNodeTraits(ctx, db.InsertNodeTraitsParams{
		NodeID: nodeID,
		Traits:  json.RawMessage(traitsJSON),
	})
	if err != nil {
		slog.Error("failed to insert node traits",
			"node_id", nodeID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing traits: %v", err)
	}

	// Prune old traits to enforce 10-snapshot retention.
	if err := s.queries.PruneNodeTraits(ctx, nodeID); err != nil {
		slog.Error("failed to prune node traits",
			"node_id", nodeID,
			"error", err,
		)
		// Non-fatal: traits were stored successfully.
	}

	slog.Info("traits reported",
		"node_id", nodeID,
		"traits_size", len(traitsJSON),
	)

	return &pb.ReportTraitsResponse{Acknowledged: true}, nil
}
