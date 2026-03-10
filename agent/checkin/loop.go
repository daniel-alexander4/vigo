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
	"github.com/hobnob/hobnob/agent/modules"
	"github.com/hobnob/hobnob/agent/traits"
	"github.com/hobnob/hobnob/agent/policy"
	"github.com/hobnob/hobnob/agent/runner"
	"github.com/hobnob/hobnob/agent/snapshots"
	"github.com/hobnob/hobnob/agent/customtraits"
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
	var protoModules []*pb.Module
	bundle := resp.PolicyBundle

	if resp.NoChange && policyState != nil && policyState.Bundle != nil {
		// Config unchanged — evaluate cached bundle with fresh traits.
		bundle = policyState.Bundle
	}

	if bundle == nil {
		// First check-in with no cached bundle and server said no_change
		// (shouldn't happen, but handle gracefully).
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

	protoModules, err = policy.EvalBundle(bundle, traitsMap)
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
	if !dryRun {
		protoResults := resultsToProto(results)
		if reportErr := client.ReportResult(ctx, runID, protoResults); reportErr != nil {
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

// Loop runs the daemon pull loop until the context is cancelled. On each
// iteration it syncs custom traits (if traitsCacheDir is non-empty), collects
// traits, checks in with traits inline, executes modules, and reports results.
// Server errors trigger exponential backoff; successful runs reset the backoff
// and sleep with jitter per the server's response.
//
// The agent sends traits inline with the CheckIn RPC, eliminating the separate
// ReportTraits round-trip. It also tracks the server's response fingerprint
// to enable short-circuit on subsequent check-ins when nothing has changed.
//
// Stable traits (OS, hardware, network, packages, etc.) are cached and only
// refreshed every 10 cycles. Volatile traits (time, uptime, loadavg,
// mountpoints) are collected every cycle.
func Loop(ctx context.Context, client *Client, executors map[string]modules.Executor, snapshotMgr *snapshots.Manager, updateCfg *UpdateConfig, traitsCacheDir string) error {
	logger := slog.Default()
	bo := NewBackoff()

	// Wire snapshot manager into file executor if present.
	if fe, ok := executors["file"]; ok {
		if fileExec, ok := fe.(*modules.FileExecutor); ok {
			fileExec.SnapshotMgr = snapshotMgr
		}
	}

	// Track response fingerprint across iterations for short-circuit.
	var lastFingerprint string

	// Policy state: cached policy bundle + version for agent-side evaluation.
	policyState := &PolicyState{
		ModuleCache: NewModuleCache(),
	}

	// Cached collector set: stable traits refresh every 10 cycles.
	collectors := traits.NewCachedCollectors(10)

	// Result cache: skip re-executing resources whose params haven't changed
	// and whose last execution was a no-op. Expires after 5 cycles.
	resultCache := runner.NewResultCache(5)

	// Custom traits manifest cached from previous cycle's response.
	var cachedCustomTraits []*pb.CustomTraitEntry

	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon loop shutting down")
			return ctx.Err()
		default:
		}

		// 0. Sync custom traits using manifest cached from previous response.
		var customTraits map[string]any
		if traitsCacheDir != "" && len(cachedCustomTraits) > 0 {
			traitsExec := customtraits.NewExecutor(traitsCacheDir, client, 10*time.Second)
			var syncErr error
			customTraits, syncErr = traitsExec.SyncAndRun(ctx, cachedCustomTraits)
			if syncErr != nil {
				logger.Warn("custom traits sync/run failed", "error", syncErr)
			}
		}

		// 1. Collect traits (volatile every cycle, stable every 10 cycles).
		traitsMap, err := collectors.CollectAll(ctx)
		if err != nil {
			logger.Warn("fact collection failed", "error", err)
			traitsMap = make(map[string]any)
		}

		// 1b. Merge custom traits into traits map.
		if customTraits != nil {
			for k, v := range customTraits {
				traitsMap[k] = v
			}
		}

		// 2. Marshal traits for inline delivery (replaces separate ReportTraits RPC).
		traitsJSON, marshalErr := json.Marshal(traitsMap)
		if marshalErr != nil {
			logger.Warn("failed to marshal traits", "error", marshalErr)
			traitsJSON = nil
		}

		// 3. Run once (check-in with inline traits + execute + report).
		runResult, err := RunOnce(ctx, client, executors, traitsMap, false, "", lastFingerprint, traitsJSON, resultCache, policyState)
		if err != nil {
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

		// Success -- reset backoff and update fingerprint + policy state.
		bo.Reset()
		if runResult.ResponseFingerprint != "" {
			lastFingerprint = runResult.ResponseFingerprint
		}
		if runResult.PolicyVersion > 0 {
			policyState.Version = runResult.PolicyVersion
		}
		if runResult.PolicyBundle != nil {
			policyState.Bundle = runResult.PolicyBundle
			// Cache custom traits manifest for next cycle (eliminates pre-check-in RPC).
			if len(runResult.PolicyBundle.CustomTraits) > 0 {
				cachedCustomTraits = runResult.PolicyBundle.CustomTraits
			}
		}

		if runResult.NoChange {
			logger.Info("no change detected, skipping execution")
		} else {
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
		}

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
}
