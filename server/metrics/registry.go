// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
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
	ReportsTotal         *prometheus.CounterVec
	RunDuration          *prometheus.HistogramVec
	DriftDetected        *prometheus.CounterVec
	ConfigcrateActions   *prometheus.CounterVec
	ConvergenceStatus    *prometheus.GaugeVec
	ConvergencePct       prometheus.Gauge
	CheckinAgeDist       prometheus.Histogram
	SigVerifyFailed      *prometheus.CounterVec
	ConfigReloadDuration prometheus.Histogram
	ConfigReloadErrors   prometheus.Counter
	DatabaseSizeBytes    prometheus.Gauge

	// --- gRPC ---

	GRPCRequestsTotal      *prometheus.CounterVec
	GRPCRequestDuration    *prometheus.HistogramVec
	GRPCHandlerPanicsTotal *prometheus.CounterVec
	GRPCTLSHandshakesTotal *prometheus.CounterVec
	GRPCCheckInInFlight    prometheus.Gauge

	// --- Policy delivery ---

	PolicyBundleBytesTotal *prometheus.CounterVec
	PolicyBundleSizeBytes  *prometheus.HistogramVec
	ForcePushesTotal       *prometheus.CounterVec
	ConfigPublishesTotal   prometheus.Counter
	PublishConvergence     prometheus.Histogram

	// --- Orchestration ---

	TaskRunsTotal     *prometheus.CounterVec
	WorkflowRunsTotal *prometheus.CounterVec

	// --- Webhooks ---

	WebhookDeliveriesTotal *prometheus.CounterVec
	WebhookDuration        prometheus.Histogram

	// --- SMTP ---

	SMTPSendsTotal *prometheus.CounterVec

	// --- FleetIndex ---

	FleetIndexFlushDuration  prometheus.Histogram
	FleetIndexDirtyCount     prometheus.Gauge
	FleetIndexDrainDirtyLock prometheus.Histogram

	// --- Bootstrap ---

	EnrollmentsTotal *prometheus.CounterVec

	// --- Server ---

	UptimeSeconds        prometheus.Gauge
	BuildInfo            *prometheus.GaugeVec
	StreamsActive        prometheus.Gauge
	CheckinSignatureMode *prometheus.GaugeVec
	ProxyDetectedTotal   prometheus.Counter

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
	StateTransitions   *prometheus.CounterVec

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
	CuratorS3Requests *prometheus.CounterVec

	// --- Swarm: lockbox ---

	LockboxUsers           prometheus.Gauge
	LockboxFiles           prometheus.Gauge
	LockboxDivergentFiles  prometheus.Gauge
	LockboxFailedFiles     prometheus.Gauge
	LockboxRecipientDrifts prometheus.Gauge

	// --- Swarm: longdrawer ---

	LongdrawerUsers          prometheus.Gauge
	LongdrawerFiles          prometheus.Gauge
	LongdrawerDivergentFiles prometheus.Gauge

	// --- Spanner ---

	SpannerBoltsActive  prometheus.Gauge
	SpannerBoltsRevoked prometheus.Gauge

	// --- AgentStream defensive guards ---

	AgentStreamDBErrors      *prometheus.CounterVec
	AgentStreamHandlerErrors *prometheus.CounterVec

	// --- Auth ---

	APITokenLastUsedUpdateErrors prometheus.Counter
}

// NewMetrics constructs the collector set, registering each into the
// given registry. Use prometheus.NewRegistry() for production; tests
// pass their own registry to isolate state.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	// The server serves /metrics from this private registry alone (not the
	// default), so Go runtime + process collectors aren't auto-included.
	// Register them explicitly: go_memstats_*, go_memstats_gc_cpu_fraction,
	// go_goroutines, process_cpu_seconds_total etc. — needed to see GC cost
	// and CPU draw under load, not just the app gauges.
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

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
		ReportsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_reportresults_total",
			Help: "Total number of agent result reports, by status (e.g. shed).",
		}, []string{"status"}),
		RunDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_run_duration_seconds",
			Help:    "Duration of agent runs in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"envoy"}),
		DriftDetected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_drift_detected_total",
			Help: "Total number of drift detection events.",
		}, []string{"envoy", "configcrate"}),
		ConfigcrateActions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_configcrate_actions_total",
			Help: "Total number of configcrate actions taken.",
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
			Name: "vigo_checkin_age_seconds",
			Help: "Distribution of seconds since last check-in across all envoys.",
			// Sub-60s buckets (5/15/30/45) straddle the 30s
			// freshness.MinStaleThreshold floor so the histogram can
			// measure how close healthy check-ins get to the stale
			// boundary under a live 1s cadence — without them the
			// finest bucket (60s) is 2× the floor and blind to margin.
			Buckets: []float64{5, 15, 30, 45, 60, 120, 300, 600, 900, 1800, 3600, 7200},
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
		GRPCHandlerPanicsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_grpc_handler_panics_total",
			Help: "gRPC handler panics recovered by the recovery interceptor, by method. A non-zero value is a bug — the handler panicked and the RPC returned codes.Internal instead of crashing the process.",
		}, []string{"method"}),
		GRPCTLSHandshakesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_grpc_tls_handshakes_total",
			Help: "TLS handshakes completed on the gRPC listener, by outcome. A non-trivial steady-state rate after the fleet has settled indicates connection churn (silent gRPC subchannel reconnects, agent restarts, or enrollment).",
		}, []string{"status"}),
		GRPCCheckInInFlight: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_grpc_checkin_in_flight",
			Help: "Concurrent VigoAgent.CheckIn handlers currently executing. Sustained climb under burst signals queueing past server capacity — the leading edge of cascading overload.",
		}),

		// --- Policy delivery ---

		PolicyBundleBytesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_policy_bundle_bytes_total",
			Help: "Total bytes of policy bundles emitted to agents, by delivery method.",
		}, []string{"method"}),
		PolicyBundleSizeBytes: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_policy_bundle_size_bytes",
			Help:    "Per-response policy bundle size in bytes (proto-encoded), by delivery method.",
			Buckets: []float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216},
		}, []string{"method"}),
		ForcePushesTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_force_pushes_total",
			Help: "Force-push messages successfully dispatched to connected agent streams, by scope (single|all).",
		}, []string{"scope"}),
		ConfigPublishesTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_config_publishes_total",
			Help: "Successful config reloads triggered by a publish. Use as the annotation source for publish-correlated dashboard panels.",
		}),
		PublishConvergence: f.NewHistogram(prometheus.HistogramOpts{
			Name: "vigo_publish_convergence_seconds",
			Help: "Seconds from a config version becoming current to an envoy first applying it. Observed once per envoy per forward version adoption (routine re-converges and catch-up to long-stale versions are excluded). Label-free and fleet-wide — cardinality-safe at 25K nodes.",
			// Convergence latency spans seconds (short-interval fleets) to
			// tens of minutes (long-interval fleets, where the check-in
			// cadence dominates). The +Inf bucket captures waves slower than
			// the top bucket.
			Buckets: []float64{1, 2, 5, 10, 30, 60, 120, 300, 600, 1800},
		}),

		// --- Orchestration ---

		TaskRunsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_task_runs_total",
			Help: "Total task runs by final status.",
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
		FleetIndexDrainDirtyLock: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_fleetindex_draindirty_lock_seconds",
			Help:    "Time the FleetIndex write lock was held during DrainDirty (blocks MarkSeen/SetIP/SetAgentVersion from check-in handlers).",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1},
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
		CheckinSignatureMode: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_checkin_signature_mode",
			Help: "Active held-stream signature verification mode (mode label, always 1). 'all' = every message verified; 'handshake-only' = stream-owner messages trusted after the verified handshake.",
		}, []string{"mode"}),
		ProxyDetectedTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_checkin_proxy_detected_total",
			Help: "Held-stream handshakes where a TLS-terminating proxy was detected (TLS peer cert ≠ the envoy's hostname) while stream_signature=handshake-only; per-message verification was re-enabled for that connection. Non-zero means handshake-only's direct-mTLS precondition is being violated.",
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
			Help: "Number of envoys in converged state (0 failed configcrates in last run).",
		}),
		ConvergenceDegraded: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_degraded",
			Help: "Number of envoys with 1-79% of configcrates failing in last run.",
		}),
		ConvergenceFailed: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_failed",
			Help: "Number of envoys with >=80% configcrates failing or status=blocked.",
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
			Help: "Number of envoys with 5+ consecutive runs of configcrate changes (persistent drift).",
		}),

		// --- Reachability ---

		ConvergenceOffline: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_convergence_offline",
			Help: "Number of envoys past the staleness threshold.",
		}),
		StateTransitions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_envoy_state_transitions_total",
			Help: "Envoy reachability edges: count of non-stale→stale (offline) and stale→non-stale (online) transitions. Rising offline transitions under load indicate the staleness floor is flapping envoys.",
		}, []string{"transition"}),

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
		CuratorS3Requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_curator_s3_requests_total",
			Help: "Curator S3-compatible API requests, by operation (ADR-027).",
		}, []string{"operation"}),

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
		LockboxFailedFiles: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_lockbox_failed_files",
			Help: "Lockbox .failed artifacts across the fleet — files whose encrypt failed permanently, sitting in plaintext and never syncing.",
		}),
		LockboxRecipientDrifts: f.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_lockbox_recipient_drifts",
			Help: "Lockbox users whose recipient set disagrees across envoys (some envoys would encrypt to a different audience).",
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

		// --- AgentStream defensive guards ---

		AgentStreamDBErrors: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_agentstream_db_errors_total",
			Help: "Agent-stream DB write failures by op (soft fields like agent_version/ip — in-memory FleetIndex stays current; DB rehydrates stale on next restart).",
		}, []string{"op"}),
		AgentStreamHandlerErrors: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_agentstream_handler_errors_total",
			Help: "Agent-stream RPC handler errors by handler. Stream survives (transient errors shouldn't tear down a working stream); metric exposes structural failure rates.",
		}, []string{"handler"}),

		// --- Auth ---

		APITokenLastUsedUpdateErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_apitoken_lastused_update_errors_total",
			Help: "API-token last_used_at DB write failures (best-effort; token validation succeeded). Persistent non-zero values indicate DB unhealth on the auth path.",
		}),
	}
}

// NewTestMetrics constructs a *Metrics against a fresh, discarded
// registry. Use in tests that need a *Metrics for a constructor but
// don't care about the metric values themselves.
func NewTestMetrics() *Metrics {
	return NewMetrics(prometheus.NewRegistry())
}
