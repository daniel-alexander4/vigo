// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package freshness

import (
	"testing"
	"time"
)

func TestStaleThresholdFromCycle(t *testing.T) {
	tests := []struct {
		name       string
		configured time.Duration
		observed   time.Duration
		want       time.Duration
	}{
		{"bootstrap (observed=0): use configured", 5 * time.Second, 0, 12500 * time.Millisecond},
		{"observed below configured: use configured", 5 * time.Second, 2 * time.Second, 12500 * time.Millisecond},
		{"observed above configured: use observed", 5 * time.Second, 60 * time.Second, 150 * time.Second},
		{"5min interval, no observation", 5 * time.Minute, 0, 12*time.Minute + 30*time.Second},
		{"30s interval, observed 45s", 30 * time.Second, 45 * time.Second, 112500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StaleThresholdFromCycle(tt.configured, tt.observed); got != tt.want {
				t.Errorf("StaleThresholdFromCycle(%v, %v) = %v, want %v", tt.configured, tt.observed, got, tt.want)
			}
		})
	}
}

func TestStaleThresholdFromCycle_OverrideWins(t *testing.T) {
	SetOverride(20 * time.Second)
	defer SetOverride(0)

	got := StaleThresholdFromCycle(5*time.Second, 60*time.Second)
	if got != 20*time.Second {
		t.Errorf("override should win: got %v, want 20s", got)
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
		{"fast configured, fresh", now.Add(-5 * time.Second), 5 * time.Second, 0, false},
		{"fast configured, just over threshold (bootstrap), stale", now.Add(-15 * time.Second), 5 * time.Second, 0, true},
		// Cycle-aware: same lastSeen age, observed cycle widens the window.
		{"slow cycle widens threshold, no longer stale", now.Add(-30 * time.Second), 5 * time.Second, 60 * time.Second, false},
		{"observed below configured doesn't tighten", now.Add(-15 * time.Second), 5 * time.Second, 1 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStaleFromCycle(tt.lastSeen, tt.configured, tt.observed); got != tt.want {
				t.Errorf("IsStaleFromCycle = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaxReportedCycle(t *testing.T) {
	got := StaleThresholdFromCycle(5*time.Second, MaxReportedCycle)
	want := 75 * time.Minute
	if got != want {
		t.Errorf("StaleThresholdFromCycle at MaxReportedCycle = %v, want %v", got, want)
	}
}
