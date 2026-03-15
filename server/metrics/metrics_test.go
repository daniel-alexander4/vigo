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
	ComplianceStatus.WithLabelValues("test", "test").Set(0)
	CheckinAgeDist.Observe(0)
	RunDuration.WithLabelValues("test").Observe(0)
	SigVerifyFailed.WithLabelValues("test").Add(0)
	GRPCRequestsTotal.WithLabelValues("test", "test").Add(0)
	GRPCRequestDuration.WithLabelValues("test").Observe(0)
	TaskRunsTotal.WithLabelValues("test").Add(0)
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
		"hobnob_nodes_total",
		"hobnob_nodes_active_total",
		"hobnob_checkins_total",
		"hobnob_run_duration_seconds",
		"hobnob_drift_detected_total",
		"hobnob_module_actions_total",
		"hobnob_compliance_status",
		"hobnob_compliance_pct",
		"hobnob_checkin_age_seconds",
		"hobnob_sig_verify_failed_total",
		"hobnob_config_reload_duration_seconds",
		"hobnob_config_reload_errors_total",
		"hobnob_database_size_bytes",
		// New metrics.
		"hobnob_grpc_requests_total",
		"hobnob_grpc_request_duration_seconds",
		"hobnob_task_runs_total",
		"hobnob_workflow_runs_total",
		"hobnob_webhook_deliveries_total",
		"hobnob_webhook_duration_seconds",
		"hobnob_smtp_sends_total",
		"hobnob_spanner_spoke_latency_seconds",
		"hobnob_spanner_spoke_up",
		"hobnob_license_thralls_used",
		"hobnob_license_thralls_max",
		"hobnob_license_stage",
		"hobnob_license_days_remaining",
		"hobnob_license_expiry_timestamp",
		"hobnob_uptime_seconds",
		"hobnob_build_info",
		"hobnob_streams_active",
		"hobnob_fleetindex_flush_duration_seconds",
		"hobnob_fleetindex_dirty_count",
		"hobnob_enrollments_total",
		"hobnob_secret_rotations_total",
		"hobnob_secret_resolution_errors_total",
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
	GRPCRequestsTotal.WithLabelValues("/hobnob.HobnobAgent/CheckIn", "OK").Inc()
	TaskRunsTotal.WithLabelValues("complete").Inc()
	WorkflowRunsTotal.WithLabelValues("complete").Inc()
	WebhookDeliveriesTotal.WithLabelValues("success").Inc()
	SMTPSendsTotal.WithLabelValues("run.failure", "success").Inc()
	EnrollmentsTotal.WithLabelValues("success").Inc()
	SecretRotationsTotal.Inc()
	SecretResolutionErrorsTotal.Inc()

	// Verify gauges can be set.
	ThrallsTotal.Set(5)
	ThrallsActive.Set(3)
	CompliancePct.Set(0.85)
	ComplianceStatus.WithLabelValues("testnode", "compliant").Set(1)
	LicenseThrallsUsed.Set(10)
	LicenseThrallsMax.Set(25)
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
	GRPCRequestDuration.WithLabelValues("/hobnob.HobnobAgent/CheckIn").Observe(0.05)
	WebhookDuration.Observe(0.5)
	SpannerSpokeLatency.WithLabelValues("dc1").Observe(0.1)
	FleetIndexFlushDuration.Observe(0.01)
}
