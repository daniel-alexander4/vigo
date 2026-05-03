// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/integrations"
)

// ConvergenceMonitor periodically checks fleet convergence percentage against a
// threshold and fires alerts when it drops below. Alerts are only sent once
// per breach; state resets when convergence recovers above threshold.
type ConvergenceMonitor struct {
	fleetIndex   *FleetIndex
	threshold    float64
	integrations *integrations.Dispatcher
	alertActive  bool
	mu           sync.Mutex
}

// NewConvergenceMonitor creates a convergence monitor that fires alerts when fleet
// convergence drops below the given threshold percentage (e.g., 95.0 = 95%).
func NewConvergenceMonitor(fi *FleetIndex, threshold float64, integ *integrations.Dispatcher) *ConvergenceMonitor {
	return &ConvergenceMonitor{
		fleetIndex:   fi,
		threshold:    threshold,
		integrations: integ,
	}
}

// Start runs the monitoring loop, checking every 60 seconds.
func (m *ConvergenceMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Check()
		}
	}
}

// Check evaluates current convergence and fires or clears alerts.
func (m *ConvergenceMonitor) Check() {
	pct := m.fleetIndex.ConvergencePercent()

	m.mu.Lock()
	defer m.mu.Unlock()

	if pct < m.threshold {
		if !m.alertActive {
			m.alertActive = true
			slog.Warn("convergence threshold breached",
				"current", pct,
				"threshold", m.threshold,
			)
			m.fireAlert(pct)
		}
	} else {
		if m.alertActive {
			m.alertActive = false
			slog.Info("convergence recovered above threshold",
				"current", pct,
				"threshold", m.threshold,
			)
		}
	}
}

func (m *ConvergenceMonitor) fireAlert(pct float64) {
	summary := m.fleetIndex.ConvergenceSummary()

	if m.integrations != nil {
		m.integrations.Dispatch(integrations.Event{
			Name:      "convergence.threshold",
			Timestamp: time.Now(),
			Summary: fmt.Sprintf("fleet convergence %.1f%% (threshold %.1f%%)",
				pct, m.threshold),
			Details: map[string]any{
				"percentage": pct,
				"threshold":  m.threshold,
				"total":      summary.Total,
				"converged":  summary.Converged,
				"degraded":   summary.Degraded,
				"failed":     summary.Failed,
				"no_data":    summary.NoData,
				"changed":    summary.Changed,
				"diverged":   summary.Diverged,
				"offline":    summary.Offline,
			},
		})
	}
}
