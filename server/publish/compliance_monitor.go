// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/fleet"
)

// ConvergenceMonitor watches fleet convergence after a config reload.
// If convergence drops below threshold within the rollback window,
// it triggers an automatic rollback to the previous config.
type ConvergenceMonitor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc // cancels the active monitoring goroutine
	fleetIndex  *fleet.FleetIndex
	onRollback  func() error // called after rollback to trigger config reload
}

// NewConvergenceMonitor creates a ConvergenceMonitor. The onRollback callback
// is invoked after a successful rollback to reload the config store.
func NewConvergenceMonitor(fleetIndex *fleet.FleetIndex, onRollback func() error) *ConvergenceMonitor {
	return &ConvergenceMonitor{
		fleetIndex: fleetIndex,
		onRollback: onRollback,
	}
}

// Start begins monitoring fleet convergence. If convergence drops below threshold
// within the window, the most recent config snapshot is restored. Only one
// monitor runs at a time — calling Start cancels any previous monitor.
func (m *ConvergenceMonitor) Start(threshold int, window time.Duration) {
	m.mu.Lock()
	// Cancel any previous monitor.
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go m.run(ctx, threshold, window)
}

// Stop cancels the active monitor, if any.
func (m *ConvergenceMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *ConvergenceMonitor) run(ctx context.Context, threshold int, window time.Duration) {
	slog.Info("convergence monitor started",
		"threshold", threshold,
		"window", window,
	)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	deadline := time.Now().Add(window)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				slog.Info("convergence monitor: rollback window elapsed, fleet remains healthy")
				return
			}

			summary := m.fleetIndex.ConvergenceSummary()
			if summary.Total == 0 {
				slog.Debug("convergence monitor: no envoys enrolled, skipping check")
				continue
			}

			pct := (summary.Converged * 100) / summary.Total
			slog.Info("convergence monitor: checking fleet",
				"converged_pct", pct,
				"threshold", threshold,
				"converged", summary.Converged,
				"total", summary.Total,
			)

			if pct < threshold {
				slog.Warn("convergence monitor: fleet convergence dropped below threshold, triggering rollback",
					"converged_pct", pct,
					"threshold", threshold,
				)
				m.triggerRollback()
				return
			}
		}
	}
}

func (m *ConvergenceMonitor) triggerRollback() {
	// Find the most recent snapshot version to roll back to.
	versions, err := ListVersions(HistoryDir)
	if err != nil || len(versions) == 0 {
		slog.Error("convergence monitor: cannot rollback, no config snapshots available", "error", err)
		return
	}

	// The latest snapshot is versions[0] — this is the snapshot taken by the
	// publish that just triggered this monitor. We need the one before it
	// (the pre-publish state).
	if len(versions) < 2 {
		slog.Error("convergence monitor: cannot rollback, only one config snapshot available (no previous version)")
		return
	}
	version := versions[1]

	slog.Info("convergence monitor: rolling back to previous config", "version", version)

	if err := RollbackToVersion(HistoryDir, version, StructureDir); err != nil {
		slog.Error("convergence monitor: rollback failed", "error", err)
		return
	}

	slog.Info("convergence monitor: rollback complete, reloading config")

	if m.onRollback != nil {
		if err := m.onRollback(); err != nil {
			slog.Error("convergence monitor: config reload after rollback failed", "error", err)
		}
	}
}
