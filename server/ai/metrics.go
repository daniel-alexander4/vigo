// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ai

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics owns the AI assistant's prometheus collectors. A single
// *Metrics is constructed at Assistant creation against a per-instance
// *prometheus.Registry so multiple servers in one Go test process
// don't share counter state. Mirrors the shape in server/metrics/.
type Metrics struct {
	RequestsTotal   prometheus.Counter
	TokensTotal     prometheus.Counter
	RequestDuration prometheus.Histogram
	ToolCallsTotal  *prometheus.CounterVec
	ErrorsTotal     prometheus.Counter
}

// NewMetrics constructs the AI metric collectors and registers them
// into reg. Pass prometheus.NewRegistry() for production; tests can
// use a discarded registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		RequestsTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_ai_requests_total",
			Help: "Total number of AI assistant requests.",
		}),
		TokensTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_ai_tokens_total",
			Help: "Total tokens consumed by AI assistant.",
		}),
		RequestDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigo_ai_request_duration_seconds",
			Help:    "Duration of AI assistant requests in seconds.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
		}),
		ToolCallsTotal: f.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_ai_tool_calls_total",
			Help: "Total tool calls by tool name.",
		}, []string{"tool"}),
		ErrorsTotal: f.NewCounter(prometheus.CounterOpts{
			Name: "vigo_ai_errors_total",
			Help: "Total AI assistant errors.",
		}),
	}
}
