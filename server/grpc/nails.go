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

// ReportNails handles the HobnobAgent.ReportNails RPC. It stores the agent's
// collected nails as JSONB in the database and prunes old entries to enforce
// a 10-snapshot retention policy (DB-04).
func (s *AgentService) ReportNails(ctx context.Context, req *pb.ReportNailsRequest) (*pb.ReportNailsResponse, error) {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	nailsJSON := req.GetNailsJson()
	if len(nailsJSON) == 0 {
		return nil, status.Error(codes.InvalidArgument, "nails_json must not be empty")
	}

	// Validate that nails_json is valid JSON.
	if !json.Valid(nailsJSON) {
		return nil, status.Error(codes.InvalidArgument, "nails_json is not valid JSON")
	}

	// Store the nails.
	_, err = s.queries.InsertNodeNails(ctx, db.InsertNodeNailsParams{
		NodeID: nodeID,
		Nails:  json.RawMessage(nailsJSON),
	})
	if err != nil {
		slog.Error("failed to insert node nails",
			"node_id", nodeID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing nails: %v", err)
	}

	// Prune old nails to enforce 10-snapshot retention.
	if err := s.queries.PruneNodeNails(ctx, nodeID); err != nil {
		slog.Error("failed to prune node nails",
			"node_id", nodeID,
			"error", err,
		)
		// Non-fatal: nails were stored successfully.
	}

	slog.Info("nails reported",
		"node_id", nodeID,
		"nails_size", len(nailsJSON),
	)

	return &pb.ReportNailsResponse{Acknowledged: true}, nil
}
