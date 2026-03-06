package grpc

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/db"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the IsgovAgent.ReportResult RPC. It stores per-module
// execution results in the database and marks the run as finished.
func (s *AgentService) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	runID, err := uuid.Parse(req.GetRunId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid run_id: %v", err)
	}

	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	// Store each module result.
	var changedCount, failedCount, skippedCount int32
	for _, result := range req.GetResults() {
		errMsg := pgtype.Text{}
		if result.GetErrorMessage() != "" {
			errMsg = pgtype.Text{String: result.GetErrorMessage(), Valid: true}
		}

		_, err := s.queries.CreateRunResult(ctx, db.CreateRunResultParams{
			RunID:        runID,
			ModuleName:   result.GetModuleName(),
			ResourceType: result.GetResourceType(),
			ResourceName: result.GetResourceName(),
			ActionTaken:  result.GetActionTaken(),
			Changed:      result.GetChanged(),
			ErrorMessage: errMsg,
			DurationMs:   result.GetDurationMs(),
		})
		if err != nil {
			slog.Error("failed to store run result",
				"run_id", runID,
				"module", result.GetModuleName(),
				"error", err,
			)
			return nil, status.Errorf(codes.Internal, "storing run result: %v", err)
		}

		if result.GetChanged() {
			changedCount++
		}
		if result.GetErrorMessage() != "" {
			failedCount++
		}
		if result.GetActionTaken() == "none" && !result.GetChanged() {
			skippedCount++
		}
	}

	// Compute run status: if any error -> "failure", else "success".
	runStatus := "success"
	if failedCount > 0 {
		runStatus = "failure"
	}

	// Finish the run with computed counts.
	err = s.queries.FinishRun(ctx, db.FinishRunParams{
		ID:           runID,
		Status:       runStatus,
		ChangedCount: changedCount,
		FailedCount:  failedCount,
		SkippedCount: skippedCount,
	})
	if err != nil {
		slog.Error("failed to finish run",
			"run_id", runID,
			"error", err,
		)
		return nil, status.Errorf(codes.Internal, "finishing run: %v", err)
	}

	slog.Info("run completed",
		"run_id", runID,
		"client_id", clientID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"skipped", skippedCount,
	)

	return &pb.ReportResultResponse{Acknowledged: true}, nil
}
