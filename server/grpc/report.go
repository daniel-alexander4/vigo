package grpc

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/webhook"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the HobnobAgent.ReportResult RPC. It stores per-module
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

	// Look up client hostname for metric labels.
	client, cErr := s.queries.GetClientByID(ctx, clientID)
	hostname := clientID.String() // fallback if lookup fails
	if cErr == nil {
		hostname = client.Hostname
	}

	// Record metrics for each result.
	for _, result := range req.GetResults() {
		metrics.ModuleActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			metrics.DriftDetected.WithLabelValues(hostname, result.GetModuleName()).Inc()
		}
	}

	// Record run duration (approximate from run start to now).
	run, runErr := s.queries.GetRunByID(ctx, runID)
	if runErr == nil {
		duration := time.Since(run.StartedAt).Seconds()
		metrics.RunDuration.WithLabelValues(hostname).Observe(duration)
	}

	slog.Info("run completed",
		"run_id", runID,
		"client_id", clientID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"skipped", skippedCount,
	)

	// Dispatch webhook event asynchronously.
	if s.dispatcher != nil {
		event := webhook.FormatRunEvent(runStatus)
		s.dispatcher.Dispatch(ctx, event, webhook.EventPayload{
			Client: &webhook.ClientInfo{
				ID:       clientID.String(),
				Hostname: hostname,
			},
			Run: &webhook.RunInfo{
				ID:      runID.String(),
				Status:  runStatus,
				Changed: changedCount,
				Failed:  failedCount,
			},
		})

		// Also fire drift.detected if any changes detected.
		if changedCount > 0 {
			s.dispatcher.Dispatch(ctx, "drift.detected", webhook.EventPayload{
				Client: &webhook.ClientInfo{
					ID:       clientID.String(),
					Hostname: hostname,
				},
				Run: &webhook.RunInfo{
					ID:      runID.String(),
					Status:  runStatus,
					Changed: changedCount,
				},
			})
		}
	}

	return &pb.ReportResultResponse{Acknowledged: true}, nil
}
