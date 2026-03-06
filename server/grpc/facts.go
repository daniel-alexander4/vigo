package grpc

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/db"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportFacts handles the IsgovAgent.ReportFacts RPC. It stores the agent's
// collected facts as JSONB in the database and prunes old entries to enforce
// a 10-snapshot retention policy (DB-04).
func (s *AgentService) ReportFacts(ctx context.Context, req *pb.ReportFactsRequest) (*pb.ReportFactsResponse, error) {
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	factsJSON := req.GetFactsJson()
	if len(factsJSON) == 0 {
		return nil, status.Error(codes.InvalidArgument, "facts_json must not be empty")
	}

	// Validate that facts_json is valid JSON.
	if !json.Valid(factsJSON) {
		return nil, status.Error(codes.InvalidArgument, "facts_json is not valid JSON")
	}

	// Store the facts.
	_, err = s.queries.InsertClientFacts(ctx, db.InsertClientFactsParams{
		ClientID: clientID,
		Facts:    json.RawMessage(factsJSON),
	})
	if err != nil {
		slog.Error("failed to insert client facts",
			"client_id", clientID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "storing facts: %v", err)
	}

	// Prune old facts to enforce 10-snapshot retention.
	if err := s.queries.PruneClientFacts(ctx, clientID); err != nil {
		slog.Error("failed to prune client facts",
			"client_id", clientID,
			"error", err,
		)
		// Non-fatal: facts were stored successfully.
	}

	slog.Info("facts reported",
		"client_id", clientID,
		"facts_size", len(factsJSON),
	)

	return &pb.ReportFactsResponse{Acknowledged: true}, nil
}
