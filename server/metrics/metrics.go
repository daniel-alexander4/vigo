// Package metrics defines Prometheus metrics for the Isogov server.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ClientsTotal is the total number of enrolled clients (gauge).
	ClientsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "isogov_clients_total",
		Help: "Total number of enrolled clients.",
	})

	// ClientsActive is the number of clients seen in the last hour (gauge).
	ClientsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "isogov_clients_active_total",
		Help: "Number of clients that checked in within the last hour.",
	})

	// CheckinsTotal counts check-in RPCs by status (counter).
	CheckinsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "isogov_checkins_total",
		Help: "Total number of agent check-ins.",
	}, []string{"status"})

	// RunDuration observes run durations in seconds by client (histogram).
	RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "isogov_run_duration_seconds",
		Help:    "Duration of agent runs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"client"})

	// DriftDetected counts drift detection events by client and module (counter).
	DriftDetected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "isogov_drift_detected_total",
		Help: "Total number of drift detection events.",
	}, []string{"client", "module"})

	// ModuleActions counts module actions by resource type and action (counter).
	ModuleActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "isogov_module_actions_total",
		Help: "Total number of module actions taken.",
	}, []string{"type", "action"})

	// ComplianceStatus tracks per-client compliance status (gauge, 1=current status).
	ComplianceStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "isogov_compliance_status",
		Help: "Client compliance status (1 for current status).",
	}, []string{"client", "status"})

	// CompliancePct is the fleet-wide compliance percentage (gauge, 0-1).
	CompliancePct = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "isogov_compliance_pct",
		Help: "Fleet-wide compliance percentage (0.0 to 1.0).",
	})

	// TimeSinceCheckin tracks seconds since each client's last check-in (gauge).
	TimeSinceCheckin = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "isogov_time_since_checkin_seconds",
		Help: "Seconds since last check-in per client.",
	}, []string{"client"})
)
