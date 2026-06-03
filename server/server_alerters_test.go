// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
)

// TestStaleScanTickStateTransitions exercises the reachability edge counter
// the burst harness watches to validate the 1s-cadence stale floor. It drives
// staleScanTick directly across ticks so the offline/online edges are
// deterministic (no ticker timing). integrations is nil throughout: the
// counter must increment regardless, since a fleet may run with no notifier
// target configured.
func TestStaleScanTickStateTransitions(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := &Server{
		fleetIndex:   fleet.NewFleetIndex(),
		metrics:      metrics.NewMetrics(reg),
		baseInterval: time.Second,
		// offlineThreshold 0 → default formula floored at MinStaleThreshold (30s).
	}

	const id, host = "envoy-1", "node1.example.com"
	addEnvoy := func(lastSeen time.Time) {
		s.fleetIndex.AddEnvoy(fleet.EnvoyState{ID: id, Hostname: host, LastSeen: lastSeen}, nil)
	}

	offline := func() float64 {
		return testutil.ToFloat64(s.metrics.StateTransitions.WithLabelValues("offline"))
	}
	online := func() float64 {
		return testutil.ToFloat64(s.metrics.StateTransitions.WithLabelValues("online"))
	}

	// Tick 1: envoy seen just now → fresh, no edge.
	addEnvoy(time.Now())
	was := s.staleScanTick(nil)
	if got := offline(); got != 0 {
		t.Fatalf("fresh envoy: offline transitions = %v, want 0", got)
	}

	// Tick 2: last-seen pushed well past the 30s floor → non-stale→stale edge.
	addEnvoy(time.Now().Add(-2 * time.Minute))
	was = s.staleScanTick(was)
	if got := offline(); got != 1 {
		t.Fatalf("gone-stale envoy: offline transitions = %v, want 1", got)
	}
	if got := online(); got != 0 {
		t.Fatalf("gone-stale envoy: online transitions = %v, want 0", got)
	}

	// Tick 3: still stale → no new edge (edge is the transition, not the state).
	was = s.staleScanTick(was)
	if got := offline(); got != 1 {
		t.Fatalf("still-stale envoy: offline transitions = %v, want 1 (no re-count)", got)
	}

	// Tick 4: checks in again → stale→non-stale edge.
	addEnvoy(time.Now())
	was = s.staleScanTick(was)
	if got := online(); got != 1 {
		t.Fatalf("recovered envoy: online transitions = %v, want 1", got)
	}
	if got := offline(); got != 1 {
		t.Fatalf("recovered envoy: offline transitions = %v, want 1 (unchanged)", got)
	}

	// Tick 5: still fresh → no further edges.
	_ = s.staleScanTick(was)
	if got, want := offline(), 1.0; got != want {
		t.Fatalf("steady fresh: offline transitions = %v, want %v", got, want)
	}
	if got, want := online(), 1.0; got != want {
		t.Fatalf("steady fresh: online transitions = %v, want %v", got, want)
	}
}
