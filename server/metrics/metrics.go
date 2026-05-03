// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package metrics defines Prometheus metrics for the Vigo server.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// EnvoysTotal is the total number of enrolled envoys (gauge).
	EnvoysTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_nodes_total",
		Help: "Total number of enrolled envoys.",
	})

	// EnvoysActive is the number of envoys seen in the last hour (gauge).
	EnvoysActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_nodes_active_total",
		Help: "Number of envoys that checked in within the last hour.",
	})

	// CheckinsTotal counts check-in RPCs by status (counter).
	CheckinsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_checkins_total",
		Help: "Total number of agent check-ins.",
	}, []string{"status"})

	// RunDuration observes run durations in seconds by envoy (histogram).
	RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vigo_run_duration_seconds",
		Help:    "Duration of agent runs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"envoy"})

	// DriftDetected counts drift detection events by envoy and module (counter).
	DriftDetected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_drift_detected_total",
		Help: "Total number of drift detection events.",
	}, []string{"envoy", "module"})

	// ModuleActions counts module actions by resource type and action (counter).
	ModuleActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_module_actions_total",
		Help: "Total number of module actions taken.",
	}, []string{"type", "action"})

	// ConvergenceStatus tracks per-envoy convergence status (gauge, 1=current status).
	ConvergenceStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigo_convergence_status",
		Help: "Envoy convergence status (1 for current status).",
	}, []string{"envoy", "status"})

	// ConvergencePct is the fleet-wide convergence percentage (gauge, 0-1).
	ConvergencePct = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_pct",
		Help: "Fleet-wide convergence percentage (0.0 to 1.0).",
	})

	// CheckinAgeDist is a histogram of seconds-since-last-checkin across all
	// envoys, replacing the per-envoy gauge that caused cardinality explosion
	// at scale (50k nodes = 50k time-series).
	CheckinAgeDist = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigo_checkin_age_seconds",
		Help:    "Distribution of seconds since last check-in across all envoys.",
		Buckets: []float64{60, 120, 300, 600, 900, 1800, 3600, 7200},
	})

	// SigVerifyFailed counts signature verification failures by reason (counter).
	SigVerifyFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_sig_verify_failed_total",
		Help: "Signature verification failures.",
	}, []string{"reason"})

	// ConfigReloadDuration tracks config reload durations in seconds (histogram).
	ConfigReloadDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigo_config_reload_duration_seconds",
		Help:    "Duration of config reloads in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	})

	// ConfigReloadErrors counts config reload failures (counter).
	ConfigReloadErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigo_config_reload_errors_total",
		Help: "Total number of config reload failures.",
	})

	// DatabaseSizeBytes is the current SQLite database file size (gauge).
	DatabaseSizeBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_database_size_bytes",
		Help: "Current SQLite database file size in bytes.",
	})

	// --- gRPC ---

	// GRPCRequestsTotal counts gRPC requests by method and status code.
	GRPCRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_grpc_requests_total",
		Help: "Total gRPC requests by method and status code.",
	}, []string{"method", "code"})

	// GRPCRequestDuration observes gRPC request latency by method.
	GRPCRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vigo_grpc_request_duration_seconds",
		Help:    "gRPC request duration in seconds.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method"})

	// --- Orchestration ---

	// PushRunsTotal counts completed push runs by status.
	PushRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_push_runs_total",
		Help: "Total push runs by final status.",
	}, []string{"status"})

	// WorkflowRunsTotal counts completed workflow runs by status.
	WorkflowRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_workflow_runs_total",
		Help: "Total workflow runs by final status.",
	}, []string{"status"})

	// --- Webhooks ---

	// WebhookDeliveriesTotal counts webhook delivery outcomes.
	WebhookDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_webhook_deliveries_total",
		Help: "Total webhook deliveries by outcome.",
	}, []string{"status"})

	// WebhookDuration observes webhook delivery latency.
	WebhookDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigo_webhook_duration_seconds",
		Help:    "Webhook delivery duration in seconds.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	})

	// --- SMTP ---

	// SMTPSendsTotal counts SMTP send attempts by event and outcome.
	SMTPSendsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_smtp_sends_total",
		Help: "Total SMTP email sends by event and status.",
	}, []string{"event", "status"})

	// --- Spanner ---

	// SpannerSpokeLatency observes spoke RPC latency by spoke ID.
	SpannerSpokeLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vigo_spanner_spoke_latency_seconds",
		Help:    "Spanner spoke RPC latency in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"spoke"})

	// SpannerSpokeUp indicates spoke reachability (1=up, 0=down).
	SpannerSpokeUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigo_spanner_spoke_up",
		Help: "Whether a spanner spoke is reachable (1=up, 0=down).",
	}, []string{"spoke"})

	// --- License ---

	// LicenseEnvoysUsed is the current active envoy count.
	LicenseEnvoysUsed = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_license_envoys_used",
		Help: "Current number of active (non-revoked) envoys.",
	})

	// LicenseEnvoysMax is the maximum envoys allowed by the license.
	LicenseEnvoysMax = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_license_envoys_max",
		Help: "Maximum envoys allowed by license.",
	})

	// LicenseStage is the current enforcement stage (0=compliant, 1=grace, 2=block, 3=stop).
	LicenseStage = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_license_stage",
		Help: "License enforcement stage (0=compliant, 1=grace, 2=enrollment_block, 3=service_stop).",
	})

	// LicenseDaysRemaining is the number of days until the license expires.
	// Negative values indicate the license has already expired.
	LicenseDaysRemaining = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_license_days_remaining",
		Help: "Days until license expiry (negative if expired).",
	})

	// LicenseExpiryTimestamp is the Unix timestamp of license expiration.
	LicenseExpiryTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_license_expiry_timestamp",
		Help: "Unix timestamp of license expiration date.",
	})

	// LicenseEnforcementErrors counts failures inside the license enforcement
	// gate (e.g., unable to read the active envoy count). The gate fails
	// closed on these — the counter exists so operators can spot repeated
	// internal errors that might otherwise masquerade as "license reached".
	LicenseEnforcementErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigo_license_enforcement_errors_total",
		Help: "Total license enforcement errors (count unavailable, fleet index missing, etc.). Gate fails closed on each increment.",
	})

	// LicenseEnrollmentsBlocked counts enrollments rejected by the hard
	// node-count gate. Separate from EnrollmentsTotal{status="denied"} so
	// license-driven rejections can be alerted on independently.
	LicenseEnrollmentsBlocked = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigo_license_enrollments_blocked_total",
		Help: "Total enrollments rejected because active envoys >= license max.",
	})

	// --- FleetIndex ---

	// FleetIndexFlushDuration observes FleetIndex dirty-set flush latency.
	FleetIndexFlushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vigo_fleetindex_flush_duration_seconds",
		Help:    "FleetIndex dirty-set flush duration in seconds.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1},
	})

	// FleetIndexDirtyCount is the number of dirty entries in the last flush.
	FleetIndexDirtyCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_fleetindex_dirty_count",
		Help: "Number of dirty envoy entries in the last FleetIndex flush.",
	})

	// --- Bootstrap ---

	// EnrollmentsTotal counts bootstrap enrollment attempts by outcome.
	EnrollmentsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vigo_enrollments_total",
		Help: "Total bootstrap enrollment attempts by outcome.",
	}, []string{"status"})

	// --- Server ---

	// UptimeSeconds tracks how long the server has been running.
	UptimeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_uptime_seconds",
		Help: "Seconds since the server process started.",
	})

	// BuildInfo is a constant gauge with the server version as a label.
	BuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigo_build_info",
		Help: "Server build information (version label, always 1).",
	}, []string{"version"})

	// StreamsActive is the current number of connected agent streams.
	StreamsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_streams_active",
		Help: "Number of currently connected agent streams.",
	})

	// --- Secrets ---

	// SecretRotationsTotal counts detected secret rotations.
	SecretRotationsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigo_secret_rotations_total",
		Help: "Total secret rotation events detected.",
	})

	// SecretResolutionErrorsTotal counts secret resolution failures.
	SecretResolutionErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "vigo_secret_resolution_errors_total",
		Help: "Total secret resolution failures.",
	})

	// --- Security Posture ---

	// SecurityCVECritical is the fleet-wide critical CVE count.
	SecurityCVECritical = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_cve_critical",
		Help: "Fleet-wide count of critical CVEs across all envoys.",
	})

	// SecurityCVEHigh is the fleet-wide high CVE count.
	SecurityCVEHigh = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_cve_high",
		Help: "Fleet-wide count of high CVEs across all envoys.",
	})

	// SecurityCVEMedium is the fleet-wide medium CVE count.
	SecurityCVEMedium = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_cve_medium",
		Help: "Fleet-wide count of medium CVEs across all envoys.",
	})

	// SecurityCVELow is the fleet-wide low CVE count.
	SecurityCVELow = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_cve_low",
		Help: "Fleet-wide count of low CVEs across all envoys.",
	})

	// SecurityHardeningAvg is the average Lynis hardening score (0-100).
	SecurityHardeningAvg = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_hardening_avg",
		Help: "Average Lynis hardening score across scanned envoys (0-100).",
	})

	// SecurityRootkitWarnings is the total rootkit warnings fleet-wide.
	SecurityRootkitWarnings = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_rootkit_warnings",
		Help: "Total rootkit warnings across all envoys.",
	})

	// SecurityMalwareDetections is the total ClamAV infections fleet-wide.
	SecurityMalwareDetections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_malware_detections",
		Help: "Total ClamAV malware detections across all envoys.",
	})

	// SecurityIntegrityIssues is the total AIDE changed files fleet-wide.
	SecurityIntegrityIssues = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_integrity_issues",
		Help: "Total file integrity (AIDE) issues across all envoys.",
	})

	// SecurityScannedHosts is the number of hosts with security scan data.
	SecurityScannedHosts = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_security_scanned_hosts",
		Help: "Number of envoys with security scan data.",
	})

	// --- Risk Posture ---

	// RiskAvgScore is the fleet-wide average risk score (0-100).
	RiskAvgScore = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_risk_avg_score",
		Help: "Fleet-wide average risk score (0-100).",
	})

	// RiskMaxScore is the highest single-envoy risk score.
	RiskMaxScore = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_risk_max_score",
		Help: "Highest single-envoy risk score in the fleet.",
	})

	// RiskDistribution counts envoys per risk level.
	RiskDistribution = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vigo_risk_distribution",
		Help: "Number of envoys per risk level.",
	}, []string{"level"})

	// --- Convergence Breakdown — failure axis ---

	// ConvergenceConverged is the number of envoys with zero failed
	// modules in their last run.
	ConvergenceConverged = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_converged",
		Help: "Number of envoys in converged state (0 failed modules in last run).",
	})

	// ConvergenceDegraded is the number of envoys with 1-79% module failures.
	ConvergenceDegraded = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_degraded",
		Help: "Number of envoys with 1-79% of modules failing in last run.",
	})

	// ConvergenceFailed is the number of envoys with >=80% failures or
	// status=blocked.
	ConvergenceFailed = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_failed",
		Help: "Number of envoys with >=80% modules failing or status=blocked.",
	})

	// ConvergenceNoData is the number of envoys with no run data.
	ConvergenceNoData = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_no_data",
		Help: "Number of envoys with no convergence data yet.",
	})

	// --- Drift axis (orthogonal to failure axis) ---

	// ConvergenceChanged is the number of envoys whose latest run had
	// any module changes. Diverged is a strict subset.
	ConvergenceChanged = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_changed",
		Help: "Number of envoys whose latest run made changes.",
	})

	// ConvergenceDiverged is the number of envoys with persistent drift
	// (5+ consecutive runs with changes).
	ConvergenceDiverged = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_diverged",
		Help: "Number of envoys with 5+ consecutive runs of module changes (persistent drift).",
	})

	// --- Reachability ---

	// ConvergenceOffline is the number of envoys past the staleness
	// threshold (2.5x the agent check-in interval).
	ConvergenceOffline = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "vigo_convergence_offline",
		Help: "Number of envoys past the staleness threshold.",
	})
)
