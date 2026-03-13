// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package metrics defines Prometheus metrics for the Hobnob server.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ThrallsTotal is the total number of enrolled thralls (gauge).
	ThrallsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_nodes_total",
		Help: "Total number of enrolled thralls.",
	})

	// ThrallsActive is the number of thralls seen in the last hour (gauge).
	ThrallsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_nodes_active_total",
		Help: "Number of thralls that checked in within the last hour.",
	})

	// CheckinsTotal counts check-in RPCs by status (counter).
	CheckinsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_checkins_total",
		Help: "Total number of agent check-ins.",
	}, []string{"status"})

	// RunDuration observes run durations in seconds by thrall (histogram).
	RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hobnob_run_duration_seconds",
		Help:    "Duration of agent runs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"thrall"})

	// DriftDetected counts drift detection events by thrall and module (counter).
	DriftDetected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_drift_detected_total",
		Help: "Total number of drift detection events.",
	}, []string{"thrall", "module"})

	// ModuleActions counts module actions by resource type and action (counter).
	ModuleActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_module_actions_total",
		Help: "Total number of module actions taken.",
	}, []string{"type", "action"})

	// ComplianceStatus tracks per-thrall compliance status (gauge, 1=current status).
	ComplianceStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hobnob_compliance_status",
		Help: "Thrall compliance status (1 for current status).",
	}, []string{"thrall", "status"})

	// CompliancePct is the fleet-wide compliance percentage (gauge, 0-1).
	CompliancePct = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_compliance_pct",
		Help: "Fleet-wide compliance percentage (0.0 to 1.0).",
	})

	// CheckinAgeDist is a histogram of seconds-since-last-checkin across all
	// thralls, replacing the per-thrall gauge that caused cardinality explosion
	// at scale (50k nodes = 50k time-series).
	CheckinAgeDist = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hobnob_checkin_age_seconds",
		Help:    "Distribution of seconds since last check-in across all thralls.",
		Buckets: []float64{60, 120, 300, 600, 900, 1800, 3600, 7200},
	})

	// SigVerifyFailed counts signature verification failures by reason (counter).
	SigVerifyFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_sig_verify_failed_total",
		Help: "Signature verification failures.",
	}, []string{"reason"})

	// ConfigReloadDuration tracks config reload durations in seconds (histogram).
	ConfigReloadDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hobnob_config_reload_duration_seconds",
		Help:    "Duration of config reloads in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	})

	// ConfigReloadErrors counts config reload failures (counter).
	ConfigReloadErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hobnob_config_reload_errors_total",
		Help: "Total number of config reload failures.",
	})

	// DatabaseSizeBytes is the current SQLite database file size (gauge).
	DatabaseSizeBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_database_size_bytes",
		Help: "Current SQLite database file size in bytes.",
	})
)
