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
	TimeSinceCheckin.WithLabelValues("test").Set(0)
	RunDuration.WithLabelValues("test").Observe(0)

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
		"isogov_clients_total",
		"isogov_clients_active_total",
		"isogov_checkins_total",
		"isogov_run_duration_seconds",
		"isogov_drift_detected_total",
		"isogov_module_actions_total",
		"isogov_compliance_status",
		"isogov_compliance_pct",
		"isogov_time_since_checkin_seconds",
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
	DriftDetected.WithLabelValues("testclient", "testmodule").Inc()
	ModuleActions.WithLabelValues("file", "create").Inc()

	// Verify gauges can be set.
	ClientsTotal.Set(5)
	ClientsActive.Set(3)
	CompliancePct.Set(0.85)
	ComplianceStatus.WithLabelValues("testclient", "compliant").Set(1)
	TimeSinceCheckin.WithLabelValues("testclient").Set(30.0)

	// Verify histogram can observe.
	RunDuration.WithLabelValues("testclient").Observe(1.5)
}
