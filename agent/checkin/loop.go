package checkin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/agent/customtraits"
	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/policy"
	"github.com/hobnob/hobnob/agent/runner"
	"github.com/hobnob/hobnob/agent/snapshots"
	"github.com/hobnob/hobnob/agent/traits"
	"github.com/hobnob/hobnob/agent/update"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// UpdateConfig holds configuration for the agent self-update mechanism.
type UpdateConfig struct {
	APIAddr        string // REST API base URL (e.g., "http://localhost:8181")
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
	Results             []modules.ResourceResult
	BaseInterval        time.Duration
	JitterPct           int
	NoChange            bool   // server indicated nothing changed
	ResponseFingerprint string // hash of response for next check-in
	PolicyVersion       uint64           // config version from server
	PolicyBundle        *pb.PolicyBundle // new bundle (nil if no_change or legacy)
}

// PolicyState tracks the agent's cached policy bundle across check-in cycles.
type PolicyState struct {
	Version     uint64
	Bundle      *pb.PolicyBundle
	ModuleCache *ModuleCache // per-module content-addressable cache for delta transfer
}

// converge performs a convergence cycle with an already-received CheckInResponse.
// reportFn is called to send results back to the server (nil skips reporting).
// Used by both RunOnce (unary poll) and the stream event loop.
func converge(ctx context.Context, resp *pb.CheckInResponse, reportFn func(ctx context.Context, runID string, results []*pb.ModuleResult) error, executors map[string]modules.Executor, traitsMap map[string]any, dryRun bool, module string, cache *runner.ResultCache, policyState *PolicyState) (*RunResult, error) {
	// Parse server-provided timing.
	baseInterval := 5 * time.Minute // default
	if resp.BaseInterval != "" {
		if parsed, parseErr := time.ParseDuration(resp.BaseInterval); parseErr == nil {
			baseInterval = parsed
		}
	}
	jitterPct := int(resp.JitterPercent)
	if jitterPct == 0 {
		jitterPct = 10 // default 10%
	}

	// Determine which policy bundle to evaluate.
	bundle := resp.PolicyBundle

	if resp.NoChange && policyState != nil && policyState.Bundle != nil {
		// Config unchanged — evaluate cached bundle with fresh traits.
		bundle = policyState.Bundle
	}

	if bundle == nil {
		// First check-in with no cached bundle and server said no_change.
		return &RunResult{
			BaseInterval:  baseInterval,
			JitterPct:     jitterPct,
			NoChange:      true,
			PolicyVersion: resp.PolicyVersion,
		}, nil
	}

	// Delta transfer: fill in stub modules from the module cache.
	if policyState != nil && policyState.ModuleCache != nil {
		bundle.Modules = MergeStubs(bundle.Modules, policyState.ModuleCache)
	}

	// Update the module cache with fully resolved modules.
	if policyState != nil && policyState.ModuleCache != nil {
		policyState.ModuleCache.Update(bundle.Modules)
	}

	protoModules, err := policy.EvalBundle(bundle, traitsMap)
	if err != nil {
		return nil, fmt.Errorf("evaluating policy bundle: %w", err)
	}

	if len(protoModules) == 0 {
		slog.Warn("server returned zero modules — check that this node is matched in nodes.hnb")
		return &RunResult{
			BaseInterval:        baseInterval,
			JitterPct:           jitterPct,
			ResponseFingerprint: resp.ResponseFingerprint,
			PolicyVersion:       resp.PolicyVersion,
			PolicyBundle:        resp.PolicyBundle,
		}, nil
	}

	runID := uuid.New().String()

	r := runner.NewRunner(executors, traitsMap, dryRun, module)
	r.Cache = cache
	results, err := r.Run(ctx, protoModules)
	if err != nil {
		return nil, fmt.Errorf("run modules: %w", err)
	}

	// Report results back to server (skip in dry-run mode).
	if !dryRun && reportFn != nil {
		protoResults := resultsToProto(results)
		if reportErr := reportFn(ctx, runID, protoResults); reportErr != nil {
			slog.Warn("failed to report results", "error", reportErr)
		}
	}

	return &RunResult{
		Results:             results,
		BaseInterval:        baseInterval,
		JitterPct:           jitterPct,
		ResponseFingerprint: resp.ResponseFingerprint,
		PolicyVersion:       resp.PolicyVersion,
		PolicyBundle:        resp.PolicyBundle,
	}, nil
}

// RunOnce performs a single check-in, executes the returned modules, and reports
// results. It is used by both the one-shot "run" command and the daemon loop.
// If dryRun is true, results are not reported back to the server.
//
// stateFingerprint is the response fingerprint from the previous check-in
// (empty on first run). traitsJSON is the pre-marshaled traits for inline
// delivery (nil falls back to legacy separate-RPC mode).
//
// policyState holds the cached policy bundle. If the server confirms the
// version is current (no_change), the cached bundle is evaluated locally
// with fresh traits. Pass nil for one-shot commands.
func RunOnce(ctx context.Context, client *Client, executors map[string]modules.Executor, traitsMap map[string]any, dryRun bool, module string, stateFingerprint string, traitsJSON []byte, cache *runner.ResultCache, policyState *PolicyState) (*RunResult, error) {
	var policyVersion uint64
	var cachedModuleHashes map[string]string
	if policyState != nil {
		policyVersion = policyState.Version
		if policyState.ModuleCache != nil {
			cachedModuleHashes = policyState.ModuleCache.Hashes()
		}
	}

	resp, err := client.CheckIn(ctx, stateFingerprint, traitsJSON, policyVersion, cachedModuleHashes)
	if err != nil {
		return nil, fmt.Errorf("check-in: %w", err)
	}

	return converge(ctx, resp, func(ctx context.Context, runID string, results []*pb.ModuleResult) error {
		return client.ReportResult(ctx, runID, results)
	}, executors, traitsMap, dryRun, module, cache, policyState)
}

// LoopConfig holds tuning parameters for the daemon loop.
type LoopConfig struct {
	TraitRefreshCycles int // stable traits refresh every N cycles (default 10)
	ResultCacheCycles  int // no-op results cached for N cycles (default 5)
}

// Loop runs the daemon loop until the context is cancelled. It attempts to
// open a persistent bidirectional stream to the server first (push-based).
// If streaming is unavailable or disconnects, it falls back to polling.
func Loop(ctx context.Context, client *Client, executors map[string]modules.Executor, snapshotMgr *snapshots.Manager, updateCfg *UpdateConfig, traitsCacheDir string, loopCfg LoopConfig) error {
	traitRefreshCycles := loopCfg.TraitRefreshCycles
	if traitRefreshCycles <= 0 {
		traitRefreshCycles = 10
	}
	resultCacheCycles := loopCfg.ResultCacheCycles
	if resultCacheCycles <= 0 {
		resultCacheCycles = 5
	}
	logger := slog.Default()
	bo := NewBackoff()

	// Wire snapshot manager into file executor if present.
	if fe, ok := executors["file"]; ok {
		if fileExec, ok := fe.(*modules.FileExecutor); ok {
			fileExec.SnapshotMgr = snapshotMgr
		}
	}

	var lastFingerprint string
	policyState := &PolicyState{ModuleCache: NewModuleCache()}
	collectors := traits.NewCachedCollectors(traitRefreshCycles)
	resultCache := runner.NewResultCache(resultCacheCycles)
	var cachedCustomTraits []*pb.CustomTraitEntry

	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon loop shutting down")
			return ctx.Err()
		default:
		}

		// Attempt stream mode. Collect traits first for the handshake.
		traitsMap, traitsJSON := collectTraits(ctx, collectors, cachedCustomTraits, traitsCacheDir, client)
		req := client.BuildCheckInRequest(lastFingerprint, traitsJSON, policyState.Version, getCachedHashes(policyState))

		sc, initialResp, streamErr := OpenStream(ctx, client, req)
		if streamErr == nil {
			logger.Info("stream connected, switching to push mode")
			bo.Reset()

			streamLoopErr := streamLoop(ctx, sc, client, initialResp, traitsMap, executors,
				collectors, resultCache, policyState, &cachedCustomTraits, traitsCacheDir, updateCfg)
			sc.Close()

			if ctx.Err() != nil {
				return ctx.Err()
			}

			logger.Info("stream disconnected, falling back to polling", "error", streamLoopErr)
			continue
		}

		logger.Debug("stream not available, using polling", "error", streamErr)

		// Poll mode: run one cycle using already-collected traits.
		runResult, err := RunOnce(ctx, client, executors, traitsMap, false, "", lastFingerprint, traitsJSON, resultCache, policyState)
		if err != nil {
			wait := bo.NextBackOff()
			logger.Warn("convergence run failed, backing off", "error", err, "backoff", wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		// Success — reset backoff and update state.
		bo.Reset()
		updateState(policyState, runResult, &cachedCustomTraits, &lastFingerprint)
		logRunResult(logger, runResult)

		if updateCfg != nil && updateCfg.APIAddr != "" && updateCfg.CurrentVersion != "" {
			checkSelfUpdate(ctx, logger, updateCfg)
		}

		sleepDur := jitteredDuration(runResult.BaseInterval, runResult.JitterPct)
		logger.Info("sleeping until next check-in",
			"duration", sleepDur,
			"next", time.Now().Add(sleepDur).Format(time.RFC3339))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDur):
		}
	}
}

// streamLoop runs the event-driven stream loop. Returns when the stream
// disconnects or the context is cancelled.
func streamLoop(ctx context.Context, sc *StreamConn, client *Client, initialResp *pb.CheckInResponse, initialTraits map[string]any, executors map[string]modules.Executor, collectors *traits.CachedCollectors, resultCache *runner.ResultCache, policyState *PolicyState, cachedCustomTraits *[]*pb.CustomTraitEntry, traitsCacheDir string, updateCfg *UpdateConfig) error {
	logger := slog.Default()

	// Report function sends results over the stream.
	reportFn := func(_ context.Context, runID string, results []*pb.ModuleResult) error {
		return sc.ReportResult(client.BuildReportResultRequest(runID, results))
	}

	// Process initial check-in response.
	rr, err := converge(ctx, initialResp, reportFn, executors, initialTraits, false, "", resultCache, policyState)
	if err != nil {
		logger.Warn("initial stream convergence failed", "error", err)
	} else {
		updateState(policyState, rr, cachedCustomTraits, nil)
		logRunResult(logger, rr)
	}

	// Determine periodic check-in interval.
	interval := 5 * time.Minute
	jitterPct := 10
	if rr != nil && rr.BaseInterval > 0 {
		interval = rr.BaseInterval
	}
	if rr != nil && rr.JitterPct > 0 {
		jitterPct = rr.JitterPct
	}

	timer := time.NewTimer(jitteredDuration(interval, jitterPct))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-sc.Done():
			return fmt.Errorf("stream disconnected")

		case push := <-sc.PolicyPush:
			// Server pushed new config — evaluate immediately.
			logger.Info("received policy push via stream", "version", push.PolicyVersion)
			traitsMap, _ := collectTraits(ctx, collectors, *cachedCustomTraits, traitsCacheDir, client)
			resp := &pb.CheckInResponse{
				PolicyBundle:  push.PolicyBundle,
				PolicyVersion: push.PolicyVersion,
			}
			rr, err := converge(ctx, resp, reportFn, executors, traitsMap, false, "", resultCache, policyState)
			if err != nil {
				logger.Warn("stream policy push convergence failed", "error", err)
			} else {
				updateState(policyState, rr, cachedCustomTraits, nil)
				logRunResult(logger, rr)
			}
			timer.Reset(jitteredDuration(interval, jitterPct))

		case <-sc.ForcePush:
			// Admin requested immediate convergence — do full check-in.
			logger.Info("received force push via stream")
			traitsMap, traitsJSON := collectTraits(ctx, collectors, *cachedCustomTraits, traitsCacheDir, client)
			req := client.BuildCheckInRequest("", traitsJSON, policyState.Version, getCachedHashes(policyState))
			resp, err := sc.CheckIn(ctx, req)
			if err != nil {
				logger.Warn("stream check-in after force push failed", "error", err)
				continue
			}
			rr, err := converge(ctx, resp, reportFn, executors, traitsMap, false, "", resultCache, policyState)
			if err != nil {
				logger.Warn("stream force push convergence failed", "error", err)
			} else {
				updateState(policyState, rr, cachedCustomTraits, nil)
				logRunResult(logger, rr)
			}
			timer.Reset(jitteredDuration(interval, jitterPct))

		case <-timer.C:
			// Periodic check-in — delivers fresh traits and verifies policy version.
			traitsMap, traitsJSON := collectTraits(ctx, collectors, *cachedCustomTraits, traitsCacheDir, client)
			req := client.BuildCheckInRequest("", traitsJSON, policyState.Version, getCachedHashes(policyState))
			resp, err := sc.CheckIn(ctx, req)
			if err != nil {
				logger.Warn("stream periodic check-in failed", "error", err)
				timer.Reset(jitteredDuration(interval, jitterPct))
				continue
			}
			rr, err := converge(ctx, resp, reportFn, executors, traitsMap, false, "", resultCache, policyState)
			if err != nil {
				logger.Warn("stream periodic convergence failed", "error", err)
			} else {
				updateState(policyState, rr, cachedCustomTraits, nil)
				if rr.BaseInterval > 0 {
					interval = rr.BaseInterval
				}
				if rr.JitterPct > 0 {
					jitterPct = rr.JitterPct
				}
				logRunResult(logger, rr)
			}

			if updateCfg != nil && updateCfg.APIAddr != "" && updateCfg.CurrentVersion != "" {
				checkSelfUpdate(ctx, logger, updateCfg)
			}

			timer.Reset(jitteredDuration(interval, jitterPct))
		}
	}
}

// collectTraits syncs custom traits and collects all trait data for a check-in.
func collectTraits(ctx context.Context, collectors *traits.CachedCollectors, cachedCustomTraits []*pb.CustomTraitEntry, traitsCacheDir string, client *Client) (map[string]any, []byte) {
	logger := slog.Default()

	// Sync custom traits.
	var customTraits map[string]any
	if traitsCacheDir != "" && len(cachedCustomTraits) > 0 {
		traitsExec := customtraits.NewExecutor(traitsCacheDir, client, 10*time.Second)
		var syncErr error
		customTraits, syncErr = traitsExec.SyncAndRun(ctx, cachedCustomTraits)
		if syncErr != nil {
			logger.Warn("custom traits sync/run failed", "error", syncErr)
		}
	}

	traitsMap, err := collectors.CollectAll(ctx)
	if err != nil {
		logger.Warn("trait collection failed", "error", err)
		traitsMap = make(map[string]any)
	}

	for k, v := range customTraits {
		traitsMap[k] = v
	}

	traitsJSON, err := json.Marshal(traitsMap)
	if err != nil {
		logger.Warn("failed to marshal traits", "error", err)
		traitsJSON = nil
	}

	return traitsMap, traitsJSON
}

// updateState updates the policy state and custom traits cache from a run result.
// lastFingerprint is optional (nil to skip fingerprint update).
func updateState(ps *PolicyState, rr *RunResult, cachedCustomTraits *[]*pb.CustomTraitEntry, lastFingerprint *string) {
	if lastFingerprint != nil && rr.ResponseFingerprint != "" {
		*lastFingerprint = rr.ResponseFingerprint
	}
	if rr.PolicyVersion > 0 {
		ps.Version = rr.PolicyVersion
	}
	if rr.PolicyBundle != nil {
		ps.Bundle = rr.PolicyBundle
		if len(rr.PolicyBundle.CustomTraits) > 0 {
			*cachedCustomTraits = rr.PolicyBundle.CustomTraits
		}
	}
}

// getCachedHashes returns the module cache hashes for delta transfer.
func getCachedHashes(ps *PolicyState) map[string]string {
	if ps != nil && ps.ModuleCache != nil {
		return ps.ModuleCache.Hashes()
	}
	return nil
}

// logRunResult logs a summary of a convergence run.
func logRunResult(logger *slog.Logger, rr *RunResult) {
	if rr.NoChange {
		logger.Info("no change detected, skipping execution")
		return
	}
	changed := 0
	errCount := 0
	for _, r := range rr.Results {
		if r.Changed {
			changed++
		}
		if r.Error != nil {
			errCount++
		}
	}
	logger.Info("convergence run complete",
		"total", len(rr.Results),
		"changed", changed,
		"errors", errCount,
	)
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
}
