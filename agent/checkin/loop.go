package checkin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/isogov/isogov/agent/facts"
	"github.com/isogov/isogov/agent/modules"
	"github.com/isogov/isogov/agent/runner"
	"github.com/isogov/isogov/agent/snapshots"
	"github.com/isogov/isogov/agent/update"
	pb "github.com/isogov/isogov/gen/proto/isogov"
)

// UpdateConfig holds configuration for the agent self-update mechanism.
type UpdateConfig struct {
	APIAddr        string // REST API base URL (e.g., "http://localhost:8080")
	CurrentVersion string // Current agent version for comparison
}

// jitteredDuration adds random jitter to a base duration.
// The result is base +/- (jitterPct% of base).
// If jitterPct is 0, returns base exactly.
func jitteredDuration(base time.Duration, jitterPct int) time.Duration {
	if jitterPct <= 0 {
		return base
	}
	jitter := (rand.Float64()*2 - 1) * float64(base) * float64(jitterPct) / 100.0
	return base + time.Duration(jitter)
}

// stateFingerprint computes a SHA256 hex digest of the facts map for use as
// the check-in state fingerprint.
func stateFingerprint(factsMap map[string]any) string {
	data, _ := json.Marshal(factsMap)
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// resultsToProto converts agent ResourceResults to protobuf ModuleResults.
func resultsToProto(results []modules.ResourceResult) []*pb.ModuleResult {
	out := make([]*pb.ModuleResult, len(results))
	for i, r := range results {
		errMsg := ""
		if r.Error != nil {
			errMsg = r.Error.Error()
		}
		out[i] = &pb.ModuleResult{
			ModuleName:   r.ModuleName,
			ResourceType: r.ResourceType,
			ResourceName: r.ResourceName,
			ActionTaken:  string(r.Action),
			Changed:      r.Changed,
			ErrorMessage: errMsg,
			DurationMs:   r.DurationMs,
		}
	}
	return out
}

// RunResult holds the outcome of a single convergence run plus timing info
// from the server's CheckIn response.
type RunResult struct {
	Results      []modules.ResourceResult
	BaseInterval time.Duration
	JitterPct    int
}

// RunOnce performs a single check-in, executes the returned modules, and reports
// results. It is used by both the one-shot "run" command and the daemon loop.
// If dryRun is true, results are not reported back to the server.
func RunOnce(ctx context.Context, client *Client, executors map[string]modules.Executor, factsMap map[string]any, dryRun bool, module string) (*RunResult, error) {
	fp := stateFingerprint(factsMap)

	resp, err := client.CheckIn(ctx, fp)
	if err != nil {
		return nil, fmt.Errorf("check-in: %w", err)
	}

	// Parse server-provided timing.
	baseInterval := 30 * time.Minute // default
	if resp.BaseInterval != "" {
		if parsed, parseErr := time.ParseDuration(resp.BaseInterval); parseErr == nil {
			baseInterval = parsed
		}
	}
	jitterPct := int(resp.JitterPercent)
	if jitterPct == 0 {
		jitterPct = 10 // default 10%
	}

	// Generate a run ID from current timestamp.
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())

	r := runner.NewRunner(executors, factsMap, dryRun, module)
	results, err := r.Run(ctx, resp.Modules)
	if err != nil {
		return nil, fmt.Errorf("run modules: %w", err)
	}

	// Report results back to server (skip in dry-run mode).
	if !dryRun {
		protoResults := resultsToProto(results)
		if reportErr := client.ReportResult(ctx, runID, protoResults); reportErr != nil {
			slog.Warn("failed to report results", "error", reportErr)
		}
	}

	return &RunResult{
		Results:      results,
		BaseInterval: baseInterval,
		JitterPct:    jitterPct,
	}, nil
}

// Loop runs the daemon pull loop until the context is cancelled. On each
// iteration it collects facts, reports them, checks in, executes modules, and
// reports results. Server errors trigger exponential backoff; successful runs
// reset the backoff and sleep with jitter per the server's response.
//
// If updateCfg is non-nil, the agent checks for a newer version after each
// successful convergence run and self-updates via atomic binary replacement
// and exec restart.
func Loop(ctx context.Context, client *Client, executors map[string]modules.Executor, snapshotMgr *snapshots.Manager, updateCfg *UpdateConfig) error {
	logger := slog.Default()
	bo := NewBackoff()

	// Wire snapshot manager into file executor if present.
	if fe, ok := executors["file"]; ok {
		if fileExec, ok := fe.(*modules.FileExecutor); ok {
			fileExec.SnapshotMgr = snapshotMgr
		}
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon loop shutting down")
			return ctx.Err()
		default:
		}

		// 1. Collect facts.
		factsMap, err := facts.CollectAll(ctx, facts.DefaultCollectors())
		if err != nil {
			logger.Warn("fact collection failed", "error", err)
			// Use empty facts -- collection is best-effort.
			factsMap = make(map[string]any)
		}

		// 2. Report facts to server.
		if err := client.ReportFacts(ctx, factsMap); err != nil {
			logger.Warn("failed to report facts", "error", err)
			// Continue -- facts reporting is non-fatal for convergence.
		}

		// 3. Run once (check-in + execute + report).
		runResult, err := RunOnce(ctx, client, executors, factsMap, false, "")
		if err != nil {
			// Server error -- apply backoff.
			wait := bo.NextBackOff()
			logger.Warn("convergence run failed, backing off",
				"error", err,
				"backoff", wait,
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		// Success -- reset backoff.
		bo.Reset()

		// Log summary.
		changed := 0
		errCount := 0
		for _, r := range runResult.Results {
			if r.Changed {
				changed++
			}
			if r.Error != nil {
				errCount++
			}
		}
		logger.Info("convergence run complete",
			"total", len(runResult.Results),
			"changed", changed,
			"errors", errCount,
		)

		// 4. Check for self-update after successful convergence.
		if updateCfg != nil && updateCfg.APIAddr != "" && updateCfg.CurrentVersion != "" {
			checkSelfUpdate(ctx, logger, updateCfg)
		}

		// 5. Sleep with jitter based on server-provided interval.
		sleepDur := jitteredDuration(runResult.BaseInterval, runResult.JitterPct)
		logger.Info("sleeping until next check-in", "duration", sleepDur)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDur):
		}
	}
}

// checkSelfUpdate queries the server for a newer agent version and applies the
// update if available. Failures are logged but never crash the agent -- the old
// version continues running and will retry on the next cycle.
func checkSelfUpdate(ctx context.Context, logger *slog.Logger, cfg *UpdateConfig) {
	info, err := update.CheckForUpdate(ctx, cfg.APIAddr, cfg.CurrentVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		logger.Warn("self-update version check failed", "error", err)
		return
	}
	if info == nil {
		logger.Debug("agent is up to date", "version", cfg.CurrentVersion)
		return
	}

	logger.Info("newer agent version available, updating",
		"current", cfg.CurrentVersion,
		"new", info.Version,
	)

	currentBinary, err := os.Executable()
	if err != nil {
		logger.Warn("self-update failed: cannot determine current binary path", "error", err)
		return
	}

	execFn := func(binary string, args []string, env []string) error {
		return syscall.Exec(binary, args, env)
	}

	if err := update.Apply(info, currentBinary, &http.Client{Timeout: 60 * time.Second}, execFn); err != nil {
		logger.Warn("self-update apply failed, continuing with current version", "error", err)
		return
	}

	// If Apply + exec succeeded, this line is never reached (process replaced).
	// If execFn returned an error, it was caught above.
}
