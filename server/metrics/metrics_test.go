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
		"hobnob_nodes_total",
		"hobnob_nodes_active_total",
		"hobnob_checkins_total",
		"hobnob_run_duration_seconds",
		"hobnob_drift_detected_total",
		"hobnob_module_actions_total",
		"hobnob_compliance_status",
		"hobnob_compliance_pct",
		"hobnob_time_since_checkin_seconds",
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

	// Verify gauges can be set.
	ThrallsTotal.Set(5)
	ThrallsActive.Set(3)
	CompliancePct.Set(0.85)
	ComplianceStatus.WithLabelValues("testnode", "compliant").Set(1)
	TimeSinceCheckin.WithLabelValues("testnode").Set(30.0)

	// Verify histogram can observe.
	RunDuration.WithLabelValues("testnode").Observe(1.5)
}
