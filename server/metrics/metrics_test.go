// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAllMetricsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	// Initialize all label-based metrics so they appear in Gather().
	m.CheckinsTotal.WithLabelValues("test").Add(0)
	m.DriftDetected.WithLabelValues("test", "test").Add(0)
	m.ModuleActions.WithLabelValues("test", "test").Add(0)
	m.ConvergenceStatus.WithLabelValues("test", "test").Set(0)
	m.CheckinAgeDist.Observe(0)
	m.RunDuration.WithLabelValues("test").Observe(0)
	m.SigVerifyFailed.WithLabelValues("test").Add(0)
	m.GRPCRequestsTotal.WithLabelValues("test", "test").Add(0)
	m.GRPCRequestDuration.WithLabelValues("test").Observe(0)
	m.PushRunsTotal.WithLabelValues("test").Add(0)
	m.WorkflowRunsTotal.WithLabelValues("test").Add(0)
	m.WebhookDeliveriesTotal.WithLabelValues("test").Add(0)
	m.WebhookDuration.Observe(0)
	m.SMTPSendsTotal.WithLabelValues("test", "test").Add(0)
	m.TungolSteorraLatency.WithLabelValues("test").Observe(0)
	m.TungolSteorraUp.WithLabelValues("test").Set(0)
	m.FleetIndexFlushDuration.Observe(0)
	m.BuildInfo.WithLabelValues("test").Set(1)
	m.EnrollmentsTotal.WithLabelValues("test").Add(0)

	// Gathering from the per-instance registry should include all our metrics.
	mfs, err := reg.Gather()
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
		"vigo_tungol_steorra_latency_seconds",
		"vigo_tungol_steorra_up",
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
	m := NewTestMetrics()

	// Verify counters can be incremented without panic.
	m.CheckinsTotal.WithLabelValues("success").Inc()
	m.DriftDetected.WithLabelValues("testnode", "testmodule").Inc()
	m.ModuleActions.WithLabelValues("file", "create").Inc()
	m.GRPCRequestsTotal.WithLabelValues("/vigo.VigoAgent/CheckIn", "OK").Inc()
	m.PushRunsTotal.WithLabelValues("complete").Inc()
	m.WorkflowRunsTotal.WithLabelValues("complete").Inc()
	m.WebhookDeliveriesTotal.WithLabelValues("success").Inc()
	m.SMTPSendsTotal.WithLabelValues("run.failure", "success").Inc()
	m.EnrollmentsTotal.WithLabelValues("success").Inc()
	m.SecretRotationsTotal.Inc()
	m.SecretResolutionErrorsTotal.Inc()

	// Verify gauges can be set.
	m.EnvoysTotal.Set(5)
	m.EnvoysActive.Set(3)
	m.ConvergencePct.Set(0.85)
	m.ConvergenceStatus.WithLabelValues("testnode", "converged").Set(1)
	m.LicenseEnvoysUsed.Set(10)
	m.LicenseEnvoysMax.Set(100)
	m.LicenseStage.Set(0)
	m.LicenseDaysRemaining.Set(365)
	m.LicenseExpiryTimestamp.Set(1.8e9)
	m.UptimeSeconds.Set(3600)
	m.BuildInfo.WithLabelValues("dev").Set(1)
	m.StreamsActive.Set(5)
	m.TungolSteorraUp.WithLabelValues("dc1").Set(1)
	m.FleetIndexDirtyCount.Set(42)

	// Verify histograms can observe.
	m.CheckinAgeDist.Observe(30.0)
	m.RunDuration.WithLabelValues("testnode").Observe(1.5)
	m.GRPCRequestDuration.WithLabelValues("/vigo.VigoAgent/CheckIn").Observe(0.05)
	m.WebhookDuration.Observe(0.5)
	m.TungolSteorraLatency.WithLabelValues("dc1").Observe(0.1)
	m.FleetIndexFlushDuration.Observe(0.01)
}
