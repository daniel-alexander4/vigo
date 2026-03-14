// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	aiRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hobnob_ai_requests_total",
		Help: "Total number of AI assistant requests.",
	})

	aiTokensTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hobnob_ai_tokens_total",
		Help: "Total tokens consumed by AI assistant.",
	})

	aiRequestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hobnob_ai_request_duration_seconds",
		Help:    "Duration of AI assistant requests in seconds.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	})

	aiToolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hobnob_ai_tool_calls_total",
		Help: "Total tool calls by tool name.",
	}, []string{"tool"})

	aiErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hobnob_ai_errors_total",
		Help: "Total AI assistant errors.",
	})
)
