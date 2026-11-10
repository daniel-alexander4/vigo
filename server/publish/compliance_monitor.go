// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package publish

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/fleet"
)

// ConvergenceMonitor watches fleet convergence after a config reload.
// If convergence drops below threshold within the rollback window,
// it triggers an automatic rollback to the previous config.
type ConvergenceMonitor struct {
	mu         sync.Mutex
	cancel     context.CancelFunc // cancels the active monitoring goroutine
	fleetIndex *fleet.FleetIndex
	onRollback func() error // called after rollback to trigger config reload
	liveDir    string       // .live/ to roll back
	historyDir string       // snapshot tarballs to restore from
}

// NewConvergenceMonitor creates a ConvergenceMonitor. The onRollback callback
// is invoked after a successful rollback to reload the config store. The
// liveDir and historyDir are the paths Rollback operates on; empty values
// fall back to the Default* constants.
func NewConvergenceMonitor(fleetIndex *fleet.FleetIndex, onRollback func() error, liveDir, historyDir string) *ConvergenceMonitor {
	if liveDir == "" {
		liveDir = DefaultLiveDir
	}
	if historyDir == "" {
		historyDir = DefaultHistoryDir
	}
	return &ConvergenceMonitor{
		fleetIndex: fleetIndex,
		onRollback: onRollback,
		liveDir:    liveDir,
		historyDir: historyDir,
	}
}

// Start begins monitoring fleet convergence. If convergence drops below threshold
// within the window, the most recent config snapshot is restored. Only one
// monitor runs at a time — calling Start cancels any previous monitor.
//
// The mutex is held across the goroutine spawn so cancel-previous + assign-new
// + launch happens as one atomic operation. Without that, a concurrent second
// Start() could interleave between the unlock and the `go` statement and
// briefly leave the "single monitor" invariant unclear at the call site —
// mirrors Stop()'s Lock + defer Unlock shape.
func (m *ConvergenceMonitor) Start(threshold int, window time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Cancel any previous monitor.
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	// Pin the version this monitor guards now: the rollback target is the
	// snapshot that preceded it, however many snapshots appear later.
	published := LiveVersion(m.liveDir)
	daemon.SafeGo("convergence-monitor", func() { m.run(ctx, threshold, window, published) })
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

func (m *ConvergenceMonitor) run(ctx context.Context, threshold int, window time.Duration, published string) {
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
				m.triggerRollback(published)
				return
			}
		}
	}
}

func (m *ConvergenceMonitor) triggerRollback(published string) {
	version, err := rollbackFrom(m.historyDir, m.liveDir, published)
	if err != nil {
		slog.Error("convergence monitor: rollback failed", "error", err)
		return
	}

	slog.Info("convergence monitor: rolled back to previous config, reloading", "version", version)

	if m.onRollback != nil {
		if err := m.onRollback(); err != nil {
			slog.Error("convergence monitor: config reload after rollback failed", "error", err)
		}
	}
}

// rollbackFrom rolls .live back to the snapshot that preceded published,
// choosing the target and swapping under one hold of the publish lock so
// no publish can land between the check and the swap.
func rollbackFrom(historyDir, liveDir, published string) (string, error) {
	unlock, err := lockPublish(liveDir)
	if err != nil {
		return "", err
	}
	defer unlock()
	version, err := rollbackTarget(historyDir, liveDir, published)
	if err != nil {
		return "", err
	}
	if err := rollbackLocked(historyDir, version, liveDir); err != nil {
		return "", err
	}
	return version, recordLiveVersion(liveDir, version)
}

// rollbackTarget returns the snapshot that preceded published — the live
// version the monitor was started for. It refuses when .live no longer
// holds published (another publish or a manual rollback has happened
// since), or when published has no snapshot or no predecessor.
func rollbackTarget(historyDir, liveDir, published string) (string, error) {
	if published == "" {
		return "", fmt.Errorf("the live config version is unknown (no snapshot recorded for it)")
	}
	if cur := LiveVersion(liveDir); cur != published {
		return "", fmt.Errorf("live config changed since the monitor started (%s, now %s); not rolling back", published, cur)
	}
	versions, err := ListVersions(historyDir)
	if err != nil {
		return "", err
	}
	for i, v := range versions {
		if v == published {
			if i+1 >= len(versions) {
				return "", fmt.Errorf("no snapshot older than %s", published)
			}
			return versions[i+1], nil
		}
	}
	return "", fmt.Errorf("snapshot %s not found in %s", published, historyDir)
}
