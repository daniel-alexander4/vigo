// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package metrics defines Prometheus metrics for the Vigo server.
//
// Collector definitions live in registry.go (Metrics struct + NewMetrics).
// The scrape-time aggregator that fills gauges from DB + fleet state lives
// in collector.go. Two *Server instances in one Go test process never share
// collector state — each builds its own *Metrics against a per-instance
// *prometheus.Registry.
package metrics
