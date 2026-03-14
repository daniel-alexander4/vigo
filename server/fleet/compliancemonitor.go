// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"log/slog"
	"sync"
	"time"

	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/webhook"
)

// ComplianceMonitor periodically checks fleet compliance percentage against a
// threshold and fires alerts when it drops below. Alerts are only sent once
// per breach; state resets when compliance recovers above threshold.
type ComplianceMonitor struct {
	fleetIndex  *FleetIndex
	threshold   float64
	dispatcher  *webhook.Dispatcher
	notifier    *hobnobsmtp.Notifier
	alertActive bool
	mu          sync.Mutex
}

// NewComplianceMonitor creates a compliance monitor that fires alerts when fleet
// compliance drops below the given threshold percentage (e.g., 95.0 = 95%).
func NewComplianceMonitor(fi *FleetIndex, threshold float64, dispatcher *webhook.Dispatcher, notifier *hobnobsmtp.Notifier) *ComplianceMonitor {
	return &ComplianceMonitor{
		fleetIndex: fi,
		threshold:  threshold,
		dispatcher: dispatcher,
		notifier:   notifier,
	}
}

// Start runs the monitoring loop, checking every 60 seconds.
func (m *ComplianceMonitor) Start(ctx context.Context) {
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
func (m *ComplianceMonitor) Check() {
	pct := m.fleetIndex.CompliancePercent()

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

func (m *ComplianceMonitor) fireAlert(pct float64) {
	summary := m.fleetIndex.ComplianceSummary()

	details := map[string]any{
		"percentage": pct,
		"threshold":  m.threshold,
		"total":      summary.Total,
		"compliant":  summary.Compliant,
		"drifted":    summary.Drifted,
		"failing":    summary.Failing,
		"stale":      summary.Stale,
		"unknown":    summary.Unknown,
	}

	if m.dispatcher != nil {
		m.dispatcher.Dispatch(context.Background(), "compliance.threshold", webhook.EventPayload{
			Event:     "compliance.threshold",
			Timestamp: time.Now(),
			Details:   details,
		})
	}

	if m.notifier != nil {
		m.notifier.Notify(hobnobsmtp.Event{
			Name:      "compliance.threshold",
			Timestamp: time.Now(),
			Details:   details,
		})
	}
}
