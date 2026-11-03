// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"database/sql"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"

	"github.com/stretchr/testify/assert"
)

func TestToolDefs_Count(t *testing.T) {
	defs := toolDefs()
	assert.Equal(t, 13, len(defs), "expected 13 tool definitions")
}

func TestToolDefs_UniqueNames(t *testing.T) {
	defs := toolDefs()
	seen := make(map[string]bool)
	for _, d := range defs {
		assert.False(t, seen[d.Name], "duplicate tool name: %s", d.Name)
		seen[d.Name] = true
	}
}

func TestToolDefs_HaveDescriptions(t *testing.T) {
	for _, d := range toolDefs() {
		assert.NotEmpty(t, d.Description, "tool %s has no description", d.Name)
	}
}

func TestMatchFilters(t *testing.T) {
	traits := map[string]any{
		"os": map[string]any{
			"distro":  "ubuntu",
			"version": "22.04",
		},
		"hardware": map[string]any{
			"memory_mb": 8192,
		},
	}

	assert.True(t, matchFilters(traits, []filter{{path: "os.distro", value: "ubuntu"}}))
	assert.False(t, matchFilters(traits, []filter{{path: "os.distro", value: "debian"}}))
	assert.True(t, matchFilters(traits, []filter{
		{path: "os.distro", value: "ubuntu"},
		{path: "os.version", value: "22.04"},
	}))
	assert.True(t, matchFilters(traits, nil))
}

func TestGetNestedValue(t *testing.T) {
	m := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
		},
	}

	assert.Equal(t, "ubuntu", getNestedValue(m, "os.distro"))
	assert.Nil(t, getNestedValue(m, "os.missing"))
	assert.Nil(t, getNestedValue(m, "missing.path"))
}

// envoyStatus is the single SoT point the AI tool surface uses to
// classify envoys for list_envoys. These tests pin it against
// freshness.IsStaleFromCycle so the AI assistant never drifts from
// the dashboard / doctor / risk-scorer verdict — the bug we just
// fixed (inline `2*baseInterval`) had no test to catch it. Drives
// every branch + verifies observed-cycle widening matches freshness.

func TestEnvoyStatus_Revoked(t *testing.T) {
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{ID: "e1", Revoked: 1})
	assert.Equal(t, "revoked", got)
}

func TestEnvoyStatus_NeverSeen(t *testing.T) {
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{ID: "e1", LastSeen: sql.NullTime{Valid: false}})
	assert.Equal(t, "active", got)
}

func TestEnvoyStatus_FreshIsActive(t *testing.T) {
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{
		ID:       "e1",
		LastSeen: sql.NullTime{Time: time.Now().Add(-1 * time.Minute), Valid: true},
	})
	assert.Equal(t, "active", got)
}

func TestEnvoyStatus_BeyondThresholdIsStale(t *testing.T) {
	// 5min configured, no observed → threshold = 2.5 × 5min = 12.5min.
	// Last seen 20min ago → stale.
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{
		ID:       "e1",
		LastSeen: sql.NullTime{Time: time.Now().Add(-20 * time.Minute), Valid: true},
	})
	assert.Equal(t, "stale", got)
}

func TestEnvoyStatus_BetweenOldAndCanonicalBoundary(t *testing.T) {
	// The bug we fixed used `2*baseInterval` (= 10min) as the threshold;
	// the canonical predicate uses `2.5 × max(configured, observed)`
	// (= 12.5min when observed≤configured). An envoy last seen 11min
	// ago is "stale" under the old buggy math but "active" under the
	// canonical SoT — exactly the divergence the SoT routing closes.
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{
		ID:       "e1",
		LastSeen: sql.NullTime{Time: time.Now().Add(-11 * time.Minute), Valid: true},
	})
	assert.Equal(t, "active", got, "11min < 2.5×5min = 12.5min — canonical SoT must say active")
}

func TestEnvoyStatus_MatchesFreshnessPackageWithObservedCycle(t *testing.T) {
	// Configured 5min, but agent's observed cycle is 10min — effective
	// interval widens to 10min, so threshold becomes 2.5×10min = 25min.
	// An envoy last seen 20min ago is active under that widened window
	// even though it would be stale under configured-only math.
	d := &toolDeps{
		cycleLookup:  func(envoyID string) time.Duration { return 10 * time.Minute },
		baseInterval: 5 * time.Minute,
	}
	lastSeen := time.Now().Add(-20 * time.Minute)

	// Direct freshness package answer is the SoT.
	want := "active"
	if freshness.IsStaleFromCycle(lastSeen, 5*time.Minute, 10*time.Minute, 0) {
		want = "stale"
	}

	got := d.envoyStatus(db.Envoy{
		ID:       "e1",
		LastSeen: sql.NullTime{Time: lastSeen, Valid: true},
	})
	assert.Equal(t, want, got, "AI tool envoyStatus must match freshness.IsStaleFromCycle")
	assert.Equal(t, "active", got, "20min < 2.5×max(5min,10min) = 25min — observed cycle should widen the window")
}

func TestEnvoyStatus_NilCycleLookupFallsBackToConfigured(t *testing.T) {
	// Same scenario as the observed-cycle test but with nil cycleLookup —
	// no per-envoy observed data, so threshold collapses to 2.5×5min =
	// 12.5min. The 20-min last-seen is stale under this narrower window.
	d := &toolDeps{baseInterval: 5 * time.Minute}
	got := d.envoyStatus(db.Envoy{
		ID:       "e1",
		LastSeen: sql.NullTime{Time: time.Now().Add(-20 * time.Minute), Valid: true},
	})
	assert.Equal(t, "stale", got)
}

func TestIntArg(t *testing.T) {
	args := map[string]any{
		"limit":   float64(42),
		"count":   10,
		"invalid": "not a number",
	}

	assert.Equal(t, 42, intArg(args, "limit", 0))
	assert.Equal(t, 10, intArg(args, "count", 0))
	assert.Equal(t, 99, intArg(args, "missing", 99))
	assert.Equal(t, 99, intArg(args, "invalid", 99))
}
