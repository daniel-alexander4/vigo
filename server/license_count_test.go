// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/spanner"
)

// envoyCount is the licensed-count source of truth handed to the license
// enforcer and, via the enforcer's CurrentCount, the count the enrollment gate
// enforces against. These tests pin its mode-aware behavior: the aggregate
// fold (local + gossiped peers, revoked excluded), the local-mode FleetIndex
// path, and the graceful fallback when aggregate is configured without a
// spanner admin wired.

func TestEnvoyCount_AggregateFoldExcludesRevoked(t *testing.T) {
	cache := spanner.NewObservabilityCache()
	admin := spanner.NewSpannerAdmin(nil, cache, nil, "self", spanner.FallbackAdminLocal, "")
	admin.SetLocalSources(
		func() spanner.ConvergenceCounters { return spanner.ConvergenceCounters{} },
		func() []spanner.EnvoyLite {
			return []spanner.EnvoyLite{{ID: "local-1"}, {ID: "local-2", Revoked: true}}
		},
	)
	now := time.Now()
	cache.UpdateFull("peerA", []spanner.EnvoyLite{{ID: "a-1"}, {ID: "a-2", Revoked: true}}, spanner.ConvergenceCounters{}, now)
	cache.UpdateFull("peerB", []spanner.EnvoyLite{{ID: "b-1"}}, spanner.ConvergenceCounters{}, now)

	s := &Server{licenseMode: "aggregate", spannerAdmin: admin}

	got, err := s.envoyCount(context.Background())
	if err != nil {
		t.Fatalf("envoyCount: %v", err)
	}
	// Active: local-1 + a-1 + b-1 = 3. Revoked local-2 and a-2 are excluded.
	if got != 3 {
		t.Errorf("aggregate envoyCount = %d, want 3 (fleet-wide, revoked excluded)", got)
	}
}

func TestEnvoyCount_LocalModeUsesFleetIndex(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "h1", LastSeen: time.Now()}, nil)
	fi.AddEnvoy(fleet.EnvoyState{ID: "e2", Hostname: "h2", LastSeen: time.Now()}, nil)

	s := &Server{licenseMode: "local", fleetIndex: fi}

	got, err := s.envoyCount(context.Background())
	if err != nil {
		t.Fatalf("envoyCount: %v", err)
	}
	if got != 2 {
		t.Errorf("local envoyCount = %d, want 2", got)
	}
}

func TestEnvoyCount_AggregateWithoutSpannerFallsBackToLocal(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "h1", LastSeen: time.Now()}, nil)

	// Aggregate mode but no spanner admin wired (e.g. pre-spanner-stage during
	// startup): the counter degrades to the local FleetIndex rather than
	// erroring or under-counting to zero.
	s := &Server{licenseMode: "aggregate", fleetIndex: fi}

	got, err := s.envoyCount(context.Background())
	if err != nil {
		t.Fatalf("envoyCount: %v", err)
	}
	if got != 1 {
		t.Errorf("fallback envoyCount = %d, want 1 (local fleet)", got)
	}
}
