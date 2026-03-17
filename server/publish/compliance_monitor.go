// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hobnob/hobnob/server/fleet"
)

// ComplianceMonitor watches fleet compliance after a config reload.
// If compliance drops below threshold within the rollback window,
// it triggers an automatic rollback to the previous config.
type ComplianceMonitor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc // cancels the active monitoring goroutine
	fleetIndex  *fleet.FleetIndex
	onRollback  func() error // called after rollback to trigger config reload
}

// NewComplianceMonitor creates a ComplianceMonitor. The onRollback callback
// is invoked after a successful rollback to reload the config store.
func NewComplianceMonitor(fleetIndex *fleet.FleetIndex, onRollback func() error) *ComplianceMonitor {
	return &ComplianceMonitor{
		fleetIndex: fleetIndex,
		onRollback: onRollback,
	}
}

// Start begins monitoring fleet compliance. If compliance drops below threshold
// within the window, the most recent config snapshot is restored. Only one
// monitor runs at a time — calling Start cancels any previous monitor.
func (m *ComplianceMonitor) Start(threshold int, window time.Duration) {
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
func (m *ComplianceMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *ComplianceMonitor) run(ctx context.Context, threshold int, window time.Duration) {
	slog.Info("compliance monitor started",
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
				slog.Info("compliance monitor: rollback window elapsed, fleet remains healthy")
				return
			}

			summary := m.fleetIndex.ComplianceSummary()
			if summary.Total == 0 {
				slog.Debug("compliance monitor: no thralls enrolled, skipping check")
				continue
			}

			pct := (summary.Compliant * 100) / summary.Total
			slog.Info("compliance monitor: checking fleet",
				"compliant_pct", pct,
				"threshold", threshold,
				"compliant", summary.Compliant,
				"total", summary.Total,
			)

			if pct < threshold {
				slog.Warn("compliance monitor: fleet compliance dropped below threshold, triggering rollback",
					"compliant_pct", pct,
					"threshold", threshold,
				)
				m.triggerRollback()
				return
			}
		}
	}
}

func (m *ComplianceMonitor) triggerRollback() {
	// Find the most recent snapshot version to roll back to.
	versions, err := ListVersions(HistoryDir)
	if err != nil || len(versions) == 0 {
		slog.Error("compliance monitor: cannot rollback, no config snapshots available", "error", err)
		return
	}

	// The latest snapshot is versions[0] — this is the snapshot taken by the
	// publish that just triggered this monitor. We need the one before it
	// (the pre-publish state).
	if len(versions) < 2 {
		slog.Error("compliance monitor: cannot rollback, only one config snapshot available (no previous version)")
		return
	}
	version := versions[1]

	slog.Info("compliance monitor: rolling back to previous config", "version", version)

	if err := RollbackToVersion(HistoryDir, version, LiveDir); err != nil {
		slog.Error("compliance monitor: rollback failed", "error", err)
		return
	}

	slog.Info("compliance monitor: rollback complete, reloading config")

	if m.onRollback != nil {
		if err := m.onRollback(); err != nil {
			slog.Error("compliance monitor: config reload after rollback failed", "error", err)
		}
	}
}
