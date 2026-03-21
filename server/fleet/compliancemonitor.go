// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"log/slog"
	"sync"
	"time"

	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/webhook"
)

// ConvergenceMonitor periodically checks fleet compliance percentage against a
// threshold and fires alerts when it drops below. Alerts are only sent once
// per breach; state resets when compliance recovers above threshold.
type ConvergenceMonitor struct {
	fleetIndex  *FleetIndex
	threshold   float64
	dispatcher  *webhook.Dispatcher
	notifier    *vigosmtp.Notifier
	alertActive bool
	mu          sync.Mutex
}

// NewConvergenceMonitor creates a compliance monitor that fires alerts when fleet
// compliance drops below the given threshold percentage (e.g., 95.0 = 95%).
func NewConvergenceMonitor(fi *FleetIndex, threshold float64, dispatcher *webhook.Dispatcher, notifier *vigosmtp.Notifier) *ConvergenceMonitor {
	return &ConvergenceMonitor{
		fleetIndex: fi,
		threshold:  threshold,
		dispatcher: dispatcher,
		notifier:   notifier,
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

// Check evaluates current compliance and fires or clears alerts.
func (m *ConvergenceMonitor) Check() {
	pct := m.fleetIndex.ConvergencePercent()

	m.mu.Lock()
	defer m.mu.Unlock()

	if pct < m.threshold {
		if !m.alertActive {
			m.alertActive = true
			slog.Warn("compliance threshold breached",
				"current", pct,
				"threshold", m.threshold,
			)
			m.fireAlert(pct)
		}
	} else {
		if m.alertActive {
			m.alertActive = false
			slog.Info("compliance recovered above threshold",
				"current", pct,
				"threshold", m.threshold,
			)
		}
	}
}

func (m *ConvergenceMonitor) fireAlert(pct float64) {
	summary := m.fleetIndex.ConvergenceSummary()

	details := map[string]any{
		"percentage": pct,
		"threshold":  m.threshold,
		"total":      summary.Total,
		"converged":  summary.Converged,
		"recurring_drift":    summary.RecurringDrift,
		"errors":    summary.Errors,
		"lost_contact":      summary.LostContact,
		"no_data":    summary.NoData,
	}

	if m.dispatcher != nil {
		m.dispatcher.Dispatch(context.Background(), "compliance.threshold", webhook.EventPayload{
			Event:     "compliance.threshold",
			Timestamp: time.Now(),
			Details:   details,
		})
	}

	if m.notifier != nil {
		m.notifier.Notify(vigosmtp.Event{
			Name:      "compliance.threshold",
			Timestamp: time.Now(),
			Details:   details,
		})
	}
}
