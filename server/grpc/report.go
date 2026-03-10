package grpc

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the HobnobAgent.ReportResult RPC. It stores per-module
// execution results in the database and marks the run as finished.
func (s *AgentService) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	if _, err := uuid.Parse(req.GetNodeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}
	nodeID := req.GetNodeId()

	// Create the run record (server generates the UUID).
	run, err := s.runStore.CreateRun(ctx, db.CreateRunParams{
		NodeID:      nodeID,
		Status:      "running",
		ModuleCount: int64(len(req.GetResults())),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating run: %v", err)
	}
	runID := run.ID

	// Store each module result.
	var changedCount, failedCount, skippedCount int64
	for _, result := range req.GetResults() {
		errMsg := sql.NullString{}
		if result.GetErrorMessage() != "" {
			errMsg = sql.NullString{String: result.GetErrorMessage(), Valid: true}
		}

		var changed int64
		if result.GetChanged() {
			changed = 1
		}

		_, err := s.runStore.CreateRunResult(ctx, db.CreateRunResultParams{
			RunID:        runID,
			ModuleName:   result.GetModuleName(),
			ResourceType: result.GetResourceType(),
			ResourceName: result.GetResourceName(),
			ActionTaken:  result.GetActionTaken(),
			Changed:      changed,
			ErrorMessage: errMsg,
			DurationMs:   int64(result.GetDurationMs()),
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
		if result.GetActionTaken() == "already in desired state" && !result.GetChanged() {
			skippedCount++
		}
	}

	// Compute run status: if any error -> "failure", else "success".
	runStatus := "success"
	if failedCount > 0 {
		runStatus = "failure"
	}

	// Finish the run with computed counts.
	err = s.runStore.FinishRun(ctx, db.FinishRunParams{
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

	// Reuse node from interceptor context (avoids redundant DB lookup).
	hostname := nodeID // fallback
	if n := db.NodeFromContext(ctx); n != nil {
		hostname = n.Hostname
	}

	// Record metrics for each result.
	for _, result := range req.GetResults() {
		metrics.ModuleActions.WithLabelValues(result.GetResourceType(), result.GetActionTaken()).Inc()
		if result.GetChanged() {
			metrics.DriftDetected.WithLabelValues(hostname, result.GetModuleName()).Inc()
		}
	}

	// Record run duration (use the run we already created — no re-fetch needed).
	duration := time.Since(run.StartedAt).Seconds()
	metrics.RunDuration.WithLabelValues(hostname).Observe(duration)

	slog.Info("run completed",
		"run_id", runID,
		"node_id", nodeID,
		"status", runStatus,
		"changed", changedCount,
		"failed", failedCount,
		"skipped", skippedCount,
	)

	// Dispatch webhook event asynchronously.
	if s.dispatcher != nil {
		event := webhook.FormatRunEvent(runStatus)
		s.dispatcher.Dispatch(ctx, event, webhook.EventPayload{
			Node: &webhook.NodeInfo{
				ID:       nodeID,
				Hostname: hostname,
			},
			Run: &webhook.RunInfo{
				ID:      runID,
				Status:  runStatus,
				Changed: int32(changedCount),
				Failed:  int32(failedCount),
			},
		})

		// Also fire drift.detected if any changes detected.
		if changedCount > 0 {
			s.dispatcher.Dispatch(ctx, "drift.detected", webhook.EventPayload{
				Node: &webhook.NodeInfo{
					ID:       nodeID,
					Hostname: hostname,
				},
				Run: &webhook.RunInfo{
					ID:      runID,
					Status:  runStatus,
					Changed: int32(changedCount),
				},
			})
		}
	}

	return &pb.ReportResultResponse{Acknowledged: true}, nil
}
