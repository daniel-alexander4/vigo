// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAllMetricsRegistered(t *testing.T) {
	// Initialize all label-based metrics so they appear in Gather().
	CheckinsTotal.WithLabelValues("test").Add(0)
	DriftDetected.WithLabelValues("test", "test").Add(0)
	ModuleActions.WithLabelValues("test", "test").Add(0)
	ConvergenceStatus.WithLabelValues("test", "test").Set(0)
	CheckinAgeDist.Observe(0)
	RunDuration.WithLabelValues("test").Observe(0)
	SigVerifyFailed.WithLabelValues("test").Add(0)
	GRPCRequestsTotal.WithLabelValues("test", "test").Add(0)
	GRPCRequestDuration.WithLabelValues("test").Observe(0)
	PushRunsTotal.WithLabelValues("test").Add(0)
	WorkflowRunsTotal.WithLabelValues("test").Add(0)
	WebhookDeliveriesTotal.WithLabelValues("test").Add(0)
	WebhookDuration.Observe(0)
	SMTPSendsTotal.WithLabelValues("test", "test").Add(0)
	SpannerSpokeLatency.WithLabelValues("test").Observe(0)
	SpannerSpokeUp.WithLabelValues("test").Set(0)
	FleetIndexFlushDuration.Observe(0)
	BuildInfo.WithLabelValues("test").Set(1)
	EnrollmentsTotal.WithLabelValues("test").Add(0)

	// Gathering from the default registry should include all our metrics.
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	// Build a set of metric names from gathered families.
	names := make(map[string]bool)
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	required := []string{
		// Original metrics.
		"vigo_nodes_total",
		"vigo_nodes_active_total",
		"vigo_checkins_total",
		"vigo_run_duration_seconds",
		"vigo_drift_detected_total",
		"vigo_module_actions_total",
		"vigo_convergence_status",
		"vigo_convergence_pct",
		"vigo_checkin_age_seconds",
		"vigo_sig_verify_failed_total",
		"vigo_config_reload_duration_seconds",
		"vigo_config_reload_errors_total",
		"vigo_database_size_bytes",
		// New metrics.
		"vigo_grpc_requests_total",
		"vigo_grpc_request_duration_seconds",
		"vigo_push_runs_total",
		"vigo_workflow_runs_total",
		"vigo_webhook_deliveries_total",
		"vigo_webhook_duration_seconds",
		"vigo_smtp_sends_total",
		"vigo_spanner_spoke_latency_seconds",
		"vigo_spanner_spoke_up",
		"vigo_license_envoys_used",
		"vigo_license_envoys_max",
		"vigo_license_stage",
		"vigo_license_days_remaining",
		"vigo_license_expiry_timestamp",
		"vigo_uptime_seconds",
		"vigo_build_info",
		"vigo_streams_active",
		"vigo_fleetindex_flush_duration_seconds",
		"vigo_fleetindex_dirty_count",
		"vigo_enrollments_total",
		"vigo_secret_rotations_total",
		"vigo_secret_resolution_errors_total",
	}

	for _, name := range required {
		if !names[name] {
			t.Errorf("metric %q not registered", name)
		}
	}
}

func TestMetricsCanBeIncremented(t *testing.T) {
	// Verify counters can be incremented without panic.
	CheckinsTotal.WithLabelValues("success").Inc()
	DriftDetected.WithLabelValues("testnode", "testmodule").Inc()
	ModuleActions.WithLabelValues("file", "create").Inc()
	GRPCRequestsTotal.WithLabelValues("/vigo.VigoAgent/CheckIn", "OK").Inc()
	PushRunsTotal.WithLabelValues("complete").Inc()
	WorkflowRunsTotal.WithLabelValues("complete").Inc()
	WebhookDeliveriesTotal.WithLabelValues("success").Inc()
	SMTPSendsTotal.WithLabelValues("run.failure", "success").Inc()
	EnrollmentsTotal.WithLabelValues("success").Inc()
	SecretRotationsTotal.Inc()
	SecretResolutionErrorsTotal.Inc()

	// Verify gauges can be set.
	EnvoysTotal.Set(5)
	EnvoysActive.Set(3)
	ConvergencePct.Set(0.85)
	ConvergenceStatus.WithLabelValues("testnode", "converged").Set(1)
	LicenseEnvoysUsed.Set(10)
	LicenseEnvoysMax.Set(25)
	LicenseStage.Set(0)
	LicenseDaysRemaining.Set(365)
	LicenseExpiryTimestamp.Set(1.8e9)
	UptimeSeconds.Set(3600)
	BuildInfo.WithLabelValues("dev").Set(1)
	StreamsActive.Set(5)
	SpannerSpokeUp.WithLabelValues("dc1").Set(1)
	FleetIndexDirtyCount.Set(42)

	// Verify histograms can observe.
	CheckinAgeDist.Observe(30.0)
	RunDuration.WithLabelValues("testnode").Observe(1.5)
	GRPCRequestDuration.WithLabelValues("/vigo.VigoAgent/CheckIn").Observe(0.05)
	WebhookDuration.Observe(0.5)
	SpannerSpokeLatency.WithLabelValues("dc1").Observe(0.1)
	FleetIndexFlushDuration.Observe(0.01)
}
