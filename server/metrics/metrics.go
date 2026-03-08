// Package metrics defines Prometheus metrics for the Hobnob server.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// NodesTotal is the total number of enrolled nodes (gauge).
	NodesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_nodes_total",
		Help: "Total number of enrolled nodes.",
	})

	// NodesActive is the number of nodes seen in the last hour (gauge).
	NodesActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_nodes_active_total",
		Help: "Number of nodes that checked in within the last hour.",
	})

	// CheckinsTotal counts check-in RPCs by status (counter).
	CheckinsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_checkins_total",
		Help: "Total number of agent check-ins.",
	}, []string{"status"})

	// RunDuration observes run durations in seconds by node (histogram).
	RunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hobnob_run_duration_seconds",
		Help:    "Duration of agent runs in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"node"})

	// DriftDetected counts drift detection events by node and module (counter).
	DriftDetected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_drift_detected_total",
		Help: "Total number of drift detection events.",
	}, []string{"node", "module"})

	// ModuleActions counts module actions by resource type and action (counter).
	ModuleActions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_module_actions_total",
		Help: "Total number of module actions taken.",
	}, []string{"type", "action"})

	// ComplianceStatus tracks per-node compliance status (gauge, 1=current status).
	ComplianceStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hobnob_compliance_status",
		Help: "Node compliance status (1 for current status).",
	}, []string{"node", "status"})

	// CompliancePct is the fleet-wide compliance percentage (gauge, 0-1).
	CompliancePct = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hobnob_compliance_pct",
		Help: "Fleet-wide compliance percentage (0.0 to 1.0).",
	})

	// TimeSinceCheckin tracks seconds since each node's last check-in (gauge).
	TimeSinceCheckin = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hobnob_time_since_checkin_seconds",
		Help: "Seconds since last check-in per node.",
	}, []string{"node"})
)
