// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package freshness

import (
	"testing"
	"time"
)

func TestStaleThreshold(t *testing.T) {
	tests := []struct {
		baseInterval time.Duration
		want         time.Duration
	}{
		{5 * time.Minute, 12*time.Minute + 30*time.Second},
		{30 * time.Second, 75 * time.Second},
		{5 * time.Second, 12500 * time.Millisecond},
		{1 * time.Minute, 150 * time.Second},
	}
	for _, tt := range tests {
		got := StaleThreshold(tt.baseInterval)
		if got != tt.want {
			t.Errorf("StaleThreshold(%v) = %v, want %v", tt.baseInterval, got, tt.want)
		}
	}
}

func TestIsStale(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		lastSeen     time.Time
		baseInterval time.Duration
		want         bool
	}{
		{"zero time is not stale", time.Time{}, 5 * time.Minute, false},
		{"fresh, well inside threshold", now.Add(-1 * time.Minute), 5 * time.Minute, false},
		{"at 2× interval, still fresh under 2.5×", now.Add(-10 * time.Minute), 5 * time.Minute, false},
		{"past 2.5× interval, stale", now.Add(-15 * time.Minute), 5 * time.Minute, true},
		{"fast interval, fresh", now.Add(-5 * time.Second), 5 * time.Second, false},
		{"fast interval, just over threshold, stale", now.Add(-15 * time.Second), 5 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStale(tt.lastSeen, tt.baseInterval); got != tt.want {
				t.Errorf("IsStale = %v, want %v", got, tt.want)
			}
		})
	}
}
