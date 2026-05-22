// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
)

// DispatchParams holds all parameters for task dispatch.
type DispatchParams struct {
	Command     string
	TargetSpec  string
	Operator    string
	TimeoutSecs int32
	Nice        int32
	BatchSize   string // count ("5") or percentage ("25%")
	MaxFailures string // count ("2") or percentage ("10%")
	HealthCheck string // shell command run between batches
	DryRun      bool
	TaskName    string            // named task definition to resolve from task store
	Params      map[string]string // parameters for named task definitions
}

// DispatchResult is the result of a task dispatch.
type DispatchResult struct {
	ID      string         `json:"id,omitempty"`
	Status  string         `json:"status"`
	Batches []DryRunBatch  `json:"batches,omitempty"`
	Offline []string       `json:"offline,omitempty"`
	// Dispatched is the count of envoys the task was actually sent to
	// (online + promotable). Populated on real dispatches; zero on
	// dry-runs (use Batches for the planned-batch breakdown there).
	Dispatched int `json:"dispatched,omitempty"`
}

// DryRunBatch describes a planned batch for dry-run output.
type DryRunBatch struct {
	Index     int      `json:"index"`
	Hostnames []string `json:"hostnames"`
}

// parseBatchSpec parses a batch size or max failures spec.
// Supports absolute counts ("5") and percentages ("25%").
// Returns 0 for empty spec. Returns error for invalid specs.
func parseBatchSpec(spec string, total int) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, nil
	}

	if strings.HasSuffix(spec, "%") {
		pct, err := strconv.ParseFloat(strings.TrimSuffix(spec, "%"), 64)
		if err != nil || pct <= 0 || pct > 100 {
			return 0, fmt.Errorf("invalid percentage %q", spec)
		}
		n := int(math.Ceil(pct / 100.0 * float64(total)))
		if n < 1 {
			n = 1
		}
		return n, nil
	}

	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid count %q", spec)
	}
	return n, nil
}

// planBatches splits targets into sequential batches of the given size.
// If batchSize is 0 or >= len(targets), returns a single batch.
func planBatches(targets []targetInfo, batchSize int) [][]targetInfo {
	if len(targets) == 0 {
		return nil
	}
	if batchSize <= 0 || batchSize >= len(targets) {
		return [][]targetInfo{targets}
	}

	var batches [][]targetInfo
	for i := 0; i < len(targets); i += batchSize {
		end := i + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		batches = append(batches, targets[i:end])
	}
	return batches
}

// validateHealthCheck rejects health check commands containing shell
// metacharacters that could enable command chaining or injection.
func validateHealthCheck(command string) error {
	if len(command) > 512 {
		return fmt.Errorf("health check command too long: %d chars (max 512)", len(command))
	}
	// Reject shell metacharacters that enable chaining, redirection, or expansion.
	for _, meta := range []string{";", "&&", "||", "|", "`", "$(", "${", "$", ">", "<", "\n", "\r"} {
		if strings.Contains(command, meta) {
			return fmt.Errorf("health check command contains disallowed shell metacharacter: %q", meta)
		}
	}
	return nil
}

// runHealthCheck executes a health check command on the server.
// Returns nil if the command exits 0, error otherwise.
func runHealthCheck(command string) error {
	if err := validateHealthCheck(command); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("health check timed out after 30s: %s", command)
		}
		return fmt.Errorf("health check failed: %s (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// targetRecord tracks a resolved target and its DB record ID.
type targetRecord struct {
	info     targetInfo
	targetID string
}

// runRollingExecution executes batches sequentially, checking failure
// thresholds and health checks between batches. Called as a goroutine.
func (s *AgentService) runRollingExecution(
	taskRunID string,
	batches [][]targetRecord,
	command string,
	timeoutSecs int32,
	nice int32,
	maxFailuresSpec string,
	healthCheck string,
	env map[string]string,
	totalConnected int,
) {
	ctx := context.Background()
	batchTimeout := time.Duration(timeoutSecs+30) * time.Second

	for batchIdx, batch := range batches {
		slog.Info("rolling execution: starting batch",
			"task_run_id", taskRunID,
			"batch", batchIdx+1,
			"total_batches", len(batches),
			"targets", len(batch),
		)

		// Register notifiers and track targets for this batch.
		doneChans := make([]chan struct{}, 0, len(batch))
		for _, tr := range batch {
			s.taskRouter.trackTarget(taskRunID, tr.info.EnvoyID, tr.targetID)
			ch := s.taskRouter.registerNotifier(taskRunID, tr.info.EnvoyID)
			doneChans = append(doneChans, ch)
		}

		// Dispatch to each target.
		now := sql.NullTime{Time: time.Now(), Valid: true}
		for _, tr := range batch {
			msg := &pb.ServerStreamMessage{
				Msg: &pb.ServerStreamMessage_TaskDispatch{
					TaskDispatch: &pb.TaskDispatch{
						TaskId:         taskRunID,
						Command:        command,
						TimeoutSeconds: timeoutSecs,
						Env:            env,
						Nice:           nice,
					},
				},
			}

			if !s.streamMgr.dispatchPush(tr.info.EnvoyID, msg) {
				// Stream closed between target creation and dispatch.
				if err := s.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
					ID:         tr.targetID,
					Status:     "offline",
					FinishedAt: now,
				}); err != nil {
					slog.Error("failed to mark target offline",
						"target_id", tr.targetID, "error", err)
				}
				s.taskRouter.removeTarget(taskRunID, tr.info.EnvoyID)
				s.taskRouter.notifyCompletion(taskRunID, tr.info.EnvoyID)
			}
		}

		// Wait for all targets in batch to complete.
		allDone := make(chan struct{})
		go func(chans []chan struct{}) {
			for _, ch := range chans {
				<-ch
			}
			close(allDone)
		}(doneChans)

		select {
		case <-allDone:
			// Batch completed normally.
		case <-time.After(batchTimeout):
			slog.Warn("rolling execution: batch timed out",
				"task_run_id", taskRunID, "batch", batchIdx+1)
		}

		// Clean up notifiers.
		for _, tr := range batch {
			s.taskRouter.removeNotifier(taskRunID, tr.info.EnvoyID)
		}

		// Check max_failures threshold.
		if maxFailuresSpec != "" {
			maxFailures, err := parseBatchSpec(maxFailuresSpec, totalConnected)
			if err == nil && maxFailures > 0 {
				failed := s.countFailedTargets(ctx, taskRunID)
				if failed >= maxFailures {
					slog.Warn("rolling execution: max failures threshold reached, aborting",
						"task_run_id", taskRunID,
						"failed", failed,
						"max_failures", maxFailures,
					)
					s.skipRemainingBatches(ctx, taskRunID, batches[batchIdx+1:])
					break
				}
			}
		}

		// Run health check between batches (not after the last).
		if batchIdx < len(batches)-1 && healthCheck != "" {
			if err := runHealthCheck(healthCheck); err != nil {
				slog.Warn("rolling execution: health check failed, aborting",
					"task_run_id", taskRunID,
					"error", err,
				)
				s.skipRemainingBatches(ctx, taskRunID, batches[batchIdx+1:])
				break
			}
		}
	}

	s.taskRouter.maybeFinishTaskRun(ctx, taskRunID)
}

// countFailedTargets returns the number of failed/timeout/rejected targets.
func (s *AgentService) countFailedTargets(ctx context.Context, taskRunID string) int {
	counts, err := s.queries.CountTaskRunTargetsByStatus(ctx, taskRunID)
	if err != nil {
		return 0
	}
	var failed int
	for _, c := range counts {
		switch c.Status {
		case "failed", "timeout", "rejected":
			failed += int(c.Count)
		}
	}
	return failed
}

// skipRemainingBatches marks all targets in remaining batches as 'skipped'.
func (s *AgentService) skipRemainingBatches(ctx context.Context, taskRunID string, batches [][]targetRecord) {
	now := sql.NullTime{Time: time.Now(), Valid: true}
	for _, batch := range batches {
		for _, tr := range batch {
			if err := s.queries.UpdateTaskRunTarget(ctx, db.UpdateTaskRunTargetParams{
				ID:         tr.targetID,
				Status:     "skipped",
				ErrorMsg:   sql.NullString{String: "skipped: rolling execution aborted", Valid: true},
				FinishedAt: now,
			}); err != nil {
				slog.Error("failed to skip target",
					"target_id", tr.targetID, "error", err)
			}
		}
	}
}
