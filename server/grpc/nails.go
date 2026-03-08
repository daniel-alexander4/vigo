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
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
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
	_, err = s.queries.InsertClientNails(ctx, db.InsertClientNailsParams{
		ClientID: clientID,
		Nails:    json.RawMessage(nailsJSON),
	})
	if err != nil {
		slog.Error("failed to insert client nails",
			"client_id", clientID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing nails: %v", err)
	}

	// Prune old nails to enforce 10-snapshot retention.
	if err := s.queries.PruneClientNails(ctx, clientID); err != nil {
		slog.Error("failed to prune client nails",
			"client_id", clientID,
			"error", err,
		)
		// Non-fatal: nails were stored successfully.
	}

	slog.Info("nails reported",
		"client_id", clientID,
		"nails_size", len(nailsJSON),
	)

	return &pb.ReportNailsResponse{Acknowledged: true}, nil
}
