// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics owns the per-instance prometheus collectors. A single
// *Metrics value is constructed against a per-instance *Registry at
// server startup and threaded through every subsystem that emits
// metrics, so multiple servers in one Go test process don't share
// collector state.
//
// Field names, metric Name/Help/Labels mirror the previous package-level
// promauto.New* declarations exactly — operators scraping the existing
// series names see no change.
type Metrics struct {
	// --- Core ---

	EnvoysTotal          prometheus.Gauge
	EnvoysActive         prometheus.Gauge
	CheckinsTotal        *prometheus.CounterVec
	RunDuration          *prometheus.HistogramVec
	DriftDetected        *prometheus.CounterVec
	ModuleActions        *prometheus.CounterVec
	ConvergenceStatus    *prometheus.GaugeVec
	ConvergencePct       prometheus.Gauge
	CheckinAgeDist       prometheus.Histogram
	SigVerifyFailed      *prometheus.CounterVec
	ConfigReloadDuration prometheus.Histogram
	ConfigReloadErrors   prometheus.Counter
	DatabaseSizeBytes    prometheus.Gauge

	// --- gRPC ---

	GRPCRequestsTotal   *prometheus.CounterVec
	GRPCRequestDuration *prometheus.HistogramVec

	// --- Orchestration ---

	PushRunsTotal     *prometheus.CounterVec
	WorkflowRunsTotal *prometheus.CounterVec

	// --- Webhooks ---

	WebhookDeliveriesTotal *prometheus.CounterVec
	WebhookDuration        prometheus.Histogram

	// --- SMTP ---

	SMTPSendsTotal *prometheus.CounterVec

	// --- License ---

	LicenseEnvoysUsed         prometheus.Gauge
	LicenseEnvoysMax          prometheus.Gauge
	LicenseStage              prometheus.Gauge
	LicenseDaysRemaining      prometheus.Gauge
	LicenseExpiryTimestamp    prometheus.Gauge
	LicenseEnforcementErrors  prometheus.Counter
	LicenseEnrollmentsBlocked prometheus.Counter

	// --- FleetIndex ---

	FleetIndexFlushDuration prometheus.Histogram
	FleetIndexDirtyCount    prometheus.Gauge

	// --- Bootstrap ---

	EnrollmentsTotal *prometheus.CounterVec

	// --- Server ---

	UptimeSeconds prometheus.Gauge
	BuildInfo     *prometheus.GaugeVec
	StreamsActive prometheus.Gauge

	// --- Secrets ---

	SecretRotationsTotal        prometheus.Counter
	SecretResolutionErrorsTotal prometheus.Counter

	// --- Security Posture ---

	SecurityCVECritical       prometheus.Gauge
	SecurityCVEHigh           prometheus.Gauge
	SecurityCVEMedium         prometheus.Gauge
	SecurityCVELow            prometheus.Gauge
	SecurityHardeningAvg      prometheus.Gauge
	SecurityRootkitWarnings   prometheus.Gauge
	SecurityMalwareDetections prometheus.Gauge
	SecurityIntegrityIssues   prometheus.Gauge
	SecurityScannedHosts      prometheus.Gauge

	// --- Risk Posture ---

	RiskAvgScore     prometheus.Gauge
	RiskMaxScore     prometheus.Gauge
	RiskDistribution *prometheus.GaugeVec

	// --- Convergence Breakdown — failure axis ---

	ConvergenceConverged prometheus.Gauge
	ConvergenceDegraded  prometheus.Gauge
	ConvergenceFailed    prometheus.Gauge
	ConvergenceNoData    prometheus.Gauge

	// --- Drift axis (orthogonal to failure axis) ---

	ConvergenceChanged  prometheus.Gauge
	ConvergenceDiverged prometheus.Gauge

	// --- Reachability ---

	ConvergenceOffline prometheus.Gauge

	// --- Swarm substrate ---

	SwarmSubstrateEnvoys prometheus.Gauge
	SwarmManifestActive  prometheus.Gauge
	SwarmFootprintBytes  prometheus.Gauge

	// --- Swarm: gitback ---

	GitbackProjects      prometheus.Gauge
	GitbackEnvoys        prometheus.Gauge
	GitbackDivergentRefs prometheus.Gauge

	// --- Swarm: curator ---

	CuratorArtifacts  prometheus.Gauge
	CuratorPublishers prometheus.Gauge

	// --- Swarm: lockbox ---

	LockboxUsers          prometheus.Gauge
	LockboxFiles          prometheus.Gauge
	LockboxDivergentFiles prometheus.Gauge

	// --- Swarm: longdrawer ---

	LongdrawerUsers          prometheus.Gauge
	LongdrawerFiles          prometheus.Gauge
	LongdrawerDivergentFiles prometheus.Gauge

	// --- Spanner ---

	SpannerBoltsActive  prometheus.Gauge
	SpannerBoltsRevoked prometheus.Gauge
}

// NewMetrics constructs the collector set, registering each into the
// given registry. Use prometheus.NewRegistry() for production; tests
// pass their own registry to isolate state.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		// --- Core ---

		EnvoysTotal: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_nodes_total",
			Help: "Total number of enrolled envoys.",
		}),
		EnvoysActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_nodes_active_total",
			Help: "Number of envoys that checked in within the last hour.",
		}),
		CheckinsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_checkins_total",
			Help: "Total number of agent check-ins.",
		}, []string{"status"}),
		RunDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_run_duration_seconds",
			Help:    "Duration of agent runs in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"envoy"}),
		DriftDetected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_drift_detected_total",
			Help: "Total number of drift detection events.",
		}, []string{"envoy", "module"}),
		ModuleActions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_module_actions_total",
			Help: "Total number of module actions taken.",
		}, []string{"type", "action"}),
		ConvergenceStatus: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_convergence_status",
			Help: "Envoy convergence status (1 for current status).",
		}, []string{"envoy", "status"}),
		ConvergencePct: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_pct",
			Help: "Fleet-wide convergence percentage (0.0 to 1.0).",
		}),
		CheckinAgeDist: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_checkin_age_seconds",
			Help:    "Distribution of seconds since last check-in across all envoys.",
			Buckets: []float64{60, 120, 300, 600, 900, 1800, 3600, 7200},
		}),
		SigVerifyFailed: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_sig_verify_failed_total",
			Help: "Signature verification failures.",
		}, []string{"reason"}),
		ConfigReloadDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_config_reload_duration_seconds",
			Help:    "Duration of config reloads in seconds.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}),
		ConfigReloadErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_config_reload_errors_total",
			Help: "Total number of config reload failures.",
		}),
		DatabaseSizeBytes: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_database_size_bytes",
			Help: "Current SQLite database file size in bytes.",
		}),

		// --- gRPC ---

		GRPCRequestsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_grpc_requests_total",
			Help: "Total gRPC requests by method and status code.",
		}, []string{"method", "code"}),
		GRPCRequestDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_grpc_request_duration_seconds",
			Help:    "gRPC request duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method"}),

		// --- Orchestration ---

		PushRunsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_push_runs_total",
			Help: "Total push runs by final status.",
		}, []string{"status"}),
		WorkflowRunsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_workflow_runs_total",
			Help: "Total workflow runs by final status.",
		}, []string{"status"}),

		// --- Webhooks ---

		WebhookDeliveriesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_webhook_deliveries_total",
			Help: "Total webhook deliveries by outcome.",
		}, []string{"status"}),
		WebhookDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_webhook_duration_seconds",
			Help:    "Webhook delivery duration in seconds.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}),

		// --- SMTP ---

		SMTPSendsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_smtp_sends_total",
			Help: "Total SMTP email sends by event and status.",
		}, []string{"event", "status"}),

		// --- License ---

		LicenseEnvoysUsed: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_license_envoys_used",
			Help: "Current number of active (non-revoked) envoys.",
		}),
		LicenseEnvoysMax: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_license_envoys_max",
			Help: "Maximum envoys allowed by license.",
		}),
		LicenseStage: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_license_stage",
			Help: "License enforcement stage (0=compliant, 1=grace, 2=enrollment_block, 3=service_stop).",
		}),
		LicenseDaysRemaining: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_license_days_remaining",
			Help: "Days until license expiry (negative if expired).",
		}),
		LicenseExpiryTimestamp: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_license_expiry_timestamp",
			Help: "Unix timestamp of license expiration date.",
		}),
		LicenseEnforcementErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_license_enforcement_errors_total",
			Help: "Total license enforcement errors (count unavailable, fleet index missing, etc.). Gate fails closed on each increment.",
		}),
		LicenseEnrollmentsBlocked: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_license_enrollments_blocked_total",
			Help: "Total enrollments rejected because active envoys >= license max.",
		}),

		// --- FleetIndex ---

		FleetIndexFlushDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_fleetindex_flush_duration_seconds",
			Help:    "FleetIndex dirty-set flush duration in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1},
		}),
		FleetIndexDirtyCount: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_fleetindex_dirty_count",
			Help: "Number of dirty envoy entries in the last FleetIndex flush.",
		}),

		// --- Bootstrap ---

		EnrollmentsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_enrollments_total",
			Help: "Total bootstrap enrollment attempts by outcome.",
		}, []string{"status"}),

		// --- Server ---

		UptimeSeconds: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_uptime_seconds",
			Help: "Seconds since the server process started.",
		}),
		BuildInfo: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_build_info",
			Help: "Server build information (version label, always 1).",
		}, []string{"version"}),
		StreamsActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_streams_active",
			Help: "Number of currently connected agent streams.",
		}),

		// --- Secrets ---

		SecretRotationsTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_secret_rotations_total",
			Help: "Total secret rotation events detected.",
		}),
		SecretResolutionErrorsTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_secret_resolution_errors_total",
			Help: "Total secret resolution failures.",
		}),

		// --- Security Posture ---

		SecurityCVECritical: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_cve_critical",
			Help: "Fleet-wide count of critical CVEs across all envoys.",
		}),
		SecurityCVEHigh: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_cve_high",
			Help: "Fleet-wide count of high CVEs across all envoys.",
		}),
		SecurityCVEMedium: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_cve_medium",
			Help: "Fleet-wide count of medium CVEs across all envoys.",
		}),
		SecurityCVELow: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_cve_low",
			Help: "Fleet-wide count of low CVEs across all envoys.",
		}),
		SecurityHardeningAvg: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_hardening_avg",
			Help: "Average Lynis hardening score across scanned envoys (0-100).",
		}),
		SecurityRootkitWarnings: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_rootkit_warnings",
			Help: "Total rootkit warnings across all envoys.",
		}),
		SecurityMalwareDetections: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_malware_detections",
			Help: "Total ClamAV malware detections across all envoys.",
		}),
		SecurityIntegrityIssues: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_integrity_issues",
			Help: "Total file integrity (AIDE) issues across all envoys.",
		}),
		SecurityScannedHosts: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_security_scanned_hosts",
			Help: "Number of envoys with security scan data.",
		}),

		// --- Risk Posture ---

		RiskAvgScore: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_risk_avg_score",
			Help: "Fleet-wide average risk score (0-100).",
		}),
		RiskMaxScore: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_risk_max_score",
			Help: "Highest single-envoy risk score in the fleet.",
		}),
		RiskDistribution: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_risk_distribution",
			Help: "Number of envoys per risk level.",
		}, []string{"level"}),

		// --- Convergence Breakdown — failure axis ---

		ConvergenceConverged: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_converged",
			Help: "Number of envoys in converged state (0 failed modules in last run).",
		}),
		ConvergenceDegraded: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_degraded",
			Help: "Number of envoys with 1-79% of modules failing in last run.",
		}),
		ConvergenceFailed: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_failed",
			Help: "Number of envoys with >=80% modules failing or status=blocked.",
		}),
		ConvergenceNoData: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_no_data",
			Help: "Number of envoys with no convergence data yet.",
		}),

		// --- Drift axis (orthogonal to failure axis) ---

		ConvergenceChanged: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_changed",
			Help: "Number of envoys whose latest run made changes.",
		}),
		ConvergenceDiverged: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_diverged",
			Help: "Number of envoys with 5+ consecutive runs of module changes (persistent drift).",
		}),

		// --- Reachability ---

		ConvergenceOffline: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_offline",
			Help: "Number of envoys past the staleness threshold.",
		}),

		// --- Swarm substrate ---

		SwarmSubstrateEnvoys: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_swarm_envoys_substrate_active",
			Help: "Number of envoys actively participating in the blob substrate.",
		}),
		SwarmManifestActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_swarm_manifest_entries_active",
			Help: "Number of active (non-revoked) seed-manifest entries.",
		}),
		SwarmFootprintBytes: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_swarm_footprint_bytes",
			Help: "Total cached blob bytes across the fleet.",
		}),

		// --- Swarm: gitback ---

		GitbackProjects: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_gitback_projects",
			Help: "Number of distinct gitback projects across the fleet.",
		}),
		GitbackEnvoys: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_gitback_envoys",
			Help: "Number of envoys hosting at least one gitback project.",
		}),
		GitbackDivergentRefs: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_gitback_divergent_refs",
			Help: "Number of gitback refs whose SHA disagrees across hosts.",
		}),

		// --- Swarm: curator ---

		CuratorArtifacts: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_curator_artifacts",
			Help: "Number of distinct artifacts in the fleet curator catalog.",
		}),
		CuratorPublishers: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_curator_publishers",
			Help: "Number of envoys publishing curator artifacts.",
		}),

		// --- Swarm: lockbox ---

		LockboxUsers: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_lockbox_users",
			Help: "Number of users with lockbox state across the fleet.",
		}),
		LockboxFiles: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_lockbox_files",
			Help: "Total encrypted files across all lockbox users.",
		}),
		LockboxDivergentFiles: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_lockbox_divergent_files",
			Help: "Lockbox files whose hash/size disagrees across envoys.",
		}),

		// --- Swarm: longdrawer ---

		LongdrawerUsers: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_longdrawer_users",
			Help: "Number of users with longdrawer sync state across the fleet.",
		}),
		LongdrawerFiles: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_longdrawer_files",
			Help: "Total synced files across all longdrawer users.",
		}),
		LongdrawerDivergentFiles: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_longdrawer_divergent_files",
			Help: "Longdrawer files whose hash/size disagrees across envoys.",
		}),

		// --- Spanner ---

		SpannerBoltsActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_spanner_bolts_active",
			Help: "Number of active (non-revoked) bolts in the spanner roster.",
		}),
		SpannerBoltsRevoked: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_spanner_bolts_revoked",
			Help: "Number of revoked bolts retained in the spanner roster.",
		}),
	}
}

// NewTestMetrics constructs a *Metrics against a fresh, discarded
// registry. Use in tests that need a *Metrics for a constructor but
// don't care about the metric values themselves.
func NewTestMetrics() *Metrics {
	return NewMetrics(prometheus.NewRegistry())
}
