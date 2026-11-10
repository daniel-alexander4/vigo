// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package freshness

import (
	"testing"
	"time"
)

func TestStaleThresholdFromCycle(t *testing.T) {
	tests := []struct {
		name             string
		configured       time.Duration
		observed         time.Duration
		offlineThreshold time.Duration
		want             time.Duration
	}{
		{"bootstrap below floor: floored to 30s", 5 * time.Second, 0, 0, 30 * time.Second},
		{"observed below configured, both below floor: floored", 5 * time.Second, 2 * time.Second, 0, 30 * time.Second},
		{"1s default cadence: floored to 30s", 1 * time.Second, 1 * time.Second, 0, 30 * time.Second},
		{"12s cadence: 2.5× hits the floor exactly", 12 * time.Second, 0, 0, 30 * time.Second},
		{"13s cadence: 2.5× clears the floor", 13 * time.Second, 0, 0, 32500 * time.Millisecond},
		{"observed above configured: use observed", 5 * time.Second, 60 * time.Second, 0, 150 * time.Second},
		{"5min interval, no observation", 5 * time.Minute, 0, 0, 12*time.Minute + 30*time.Second},
		{"30s interval, observed 45s", 30 * time.Second, 45 * time.Second, 0, 112500 * time.Millisecond},
		{"offlineThreshold below floor still wins (bypasses floor)", 5 * time.Second, 60 * time.Second, 20 * time.Second, 20 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StaleThresholdFromCycle(tt.configured, tt.observed, tt.offlineThreshold); got != tt.want {
				t.Errorf("StaleThresholdFromCycle(%v, %v, %v) = %v, want %v", tt.configured, tt.observed, tt.offlineThreshold, got, tt.want)
			}
		})
	}
}

func TestStaleScanInterval(t *testing.T) {
	tests := []struct {
		name             string
		baseInterval     time.Duration
		offlineThreshold time.Duration
		want             time.Duration
	}{
		{"1s default: half the 30s floor", 1 * time.Second, 0, 15 * time.Second},
		{"5min interval: tightest/2 < baseInterval, floored to baseInterval", 5 * time.Minute, 0, 5 * time.Minute},
		{"30s interval: tightest/2 (15s) < baseInterval, floored", 30 * time.Second, 0, 30 * time.Second},
		{"10s interval: tightest/2 (15s) > baseInterval", 10 * time.Second, 0, 15 * time.Second},
		{"tight override scales the sweep down", 1 * time.Second, 6 * time.Second, 3 * time.Second},
		{"override below 2× baseInterval: floored to baseInterval", 1 * time.Second, 1 * time.Second, 1 * time.Second},
		{"large override: half of it", 1 * time.Second, 5 * time.Minute, 150 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StaleScanInterval(tt.baseInterval, tt.offlineThreshold); got != tt.want {
				t.Errorf("StaleScanInterval(%v, %v) = %v, want %v", tt.baseInterval, tt.offlineThreshold, got, tt.want)
			}
		})
	}
}

func TestIsStaleFromCycle(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		lastSeen   time.Time
		configured time.Duration
		observed   time.Duration
		want       bool
	}{
		{"zero time is not stale", time.Time{}, 5 * time.Minute, 0, false},
		{"fresh, well inside threshold", now.Add(-1 * time.Minute), 5 * time.Minute, 0, false},
		{"at 2× configured, still fresh under 2.5×", now.Add(-10 * time.Minute), 5 * time.Minute, 0, false},
		{"past 2.5× configured, stale", now.Add(-15 * time.Minute), 5 * time.Minute, 0, true},
		{"fast configured, fresh (inside 30s floor)", now.Add(-5 * time.Second), 5 * time.Second, 0, false},
		{"fast cadence, inside the 30s floor, not stale", now.Add(-15 * time.Second), 5 * time.Second, 0, false},
		{"fast cadence, past the 30s floor, stale", now.Add(-31 * time.Second), 1 * time.Second, 1 * time.Second, true},
		// Cycle-aware: same lastSeen age, observed cycle widens the window.
		{"slow cycle widens threshold, no longer stale", now.Add(-30 * time.Second), 5 * time.Second, 60 * time.Second, false},
		// Above the floor, observed below configured doesn't tighten:
		// eff=20s→50s window; had it used observed 5s it'd be floored to
		// 30s and 40s ago would read stale. false proves it used configured.
		{"observed below configured doesn't tighten", now.Add(-40 * time.Second), 20 * time.Second, 5 * time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStaleFromCycle(tt.lastSeen, tt.configured, tt.observed, 0); got != tt.want {
				t.Errorf("IsStaleFromCycle = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaxReportedCycle(t *testing.T) {
	got := StaleThresholdFromCycle(5*time.Second, MaxReportedCycle, 0)
	want := 75 * time.Minute
	if got != want {
		t.Errorf("StaleThresholdFromCycle at MaxReportedCycle = %v, want %v", got, want)
	}
}

func TestResponsiveThreshold(t *testing.T) {
	tests := []struct {
		name             string
		configured       time.Duration
		observed         time.Duration
		offlineThreshold time.Duration
		want             time.Duration
	}{
		// 1.2× the stale threshold. Below the floor the stale boundary
		// is 30s, so the responsive boundary is 1.2×30s = 36s.
		{"bootstrap below floor: 1.2× of 30s floor", 5 * time.Second, 0, 0, 36 * time.Second},
		// Above the floor: 1.2× of 2.5× = 3× effective interval.
		{"observed widens window proportionally", 5 * time.Second, 60 * time.Second, 0, 180 * time.Second},
		{"5m interval, no observation", 5 * time.Minute, 0, 0, 15 * time.Minute},
		// configured=0 + observed=0 + no override hits the
		// fallback branch — preserve the pre-0.38.13 behavior.
		{"zero-config fallback", 0, 0, 0, 15 * time.Minute},
		// Override flows through StaleThresholdFromCycle, so the
		// responsive boundary is 1.2× of the override.
		{"offlineThreshold drives 1.2× boundary", 5 * time.Second, 60 * time.Second, 20 * time.Second, 24 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResponsiveThreshold(tt.configured, tt.observed, tt.offlineThreshold); got != tt.want {
				t.Errorf("ResponsiveThreshold(%v, %v, %v) = %v, want %v", tt.configured, tt.observed, tt.offlineThreshold, got, tt.want)
			}
		})
	}
}

func TestIsResponsive(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		lastSeen   time.Time
		configured time.Duration
		observed   time.Duration
		want       bool
	}{
		{"zero time is not responsive", time.Time{}, 5 * time.Second, 0, false},
		{"fresh, well inside window", now.Add(-3 * time.Second), 5 * time.Second, 0, true},
		{"inside the 36s responsive floor, responsive", now.Add(-13 * time.Second), 5 * time.Second, 0, true},
		{"past the 36s responsive floor, no longer responsive", now.Add(-37 * time.Second), 5 * time.Second, 0, false},
		// Per-envoy cycle awareness: a slow envoy (observed=60s)
		// stays responsive far longer than a 3×configured rule
		// would allow.
		{"slow cycle widens window, still responsive", now.Add(-2 * time.Minute), 5 * time.Second, 60 * time.Second, true},
		{"slow cycle, past widened window", now.Add(-4 * time.Minute), 5 * time.Second, 60 * time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsResponsive(tt.lastSeen, tt.configured, tt.observed, 0); got != tt.want {
				t.Errorf("IsResponsive = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsResponsive_LooserThanIsStale pins the central design
// invariant: an envoy that's *just barely* stale by the UI rule must
// still be considered responsive enough for one round of best-effort
// admin dispatch. Without this margin, a freshly-stale envoy would
// drop off the dispatch routing table the same instant it falls off
// the UI online list — too tight, given clock skew + jitter.
func TestIsResponsive_LooserThanIsStale(t *testing.T) {
	now := time.Now()
	configured := 5 * time.Second
	// At a sub-12s cadence the stale boundary floors to 30s and the
	// responsive boundary to 36s, so 33s ago is past stale (30s) but
	// still inside the responsive window (36s).
	lastSeen := now.Add(-33 * time.Second)
	if !IsStaleFromCycle(lastSeen, configured, 0, 0) {
		t.Fatal("test premise broken: 33s ago should be stale at the 30s floor")
	}
	if !IsResponsive(lastSeen, configured, 0, 0) {
		t.Error("an envoy that's just barely stale must still be responsive")
	}
}
