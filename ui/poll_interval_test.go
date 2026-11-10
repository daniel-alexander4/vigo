// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"testing"
	"time"
)

// TestFormatPollInterval pins the UI's HTMX poll cadence derivation:
// clamp(check-in interval, 5s, 5m). It tracks the check-in interval in
// between but never polls faster than every 5s (sub-5s refreshes just
// hammer the server) nor slower than every 5m (a long-interval fleet
// shouldn't look frozen). Zero / negative inputs fall back to the cap.
func TestFormatPollInterval(t *testing.T) {
	cases := []struct {
		name    string
		checkIn time.Duration
		want    string
	}{
		{"1s check-in floored to 5s", 1 * time.Second, "5s"},
		{"sub-5s floored to 5s", 4 * time.Second, "5s"},
		{"5s check-in hits the floor exactly", 5 * time.Second, "5s"},
		{"15s default check-in tracks exactly", 15 * time.Second, "15s"},
		{"30s tracks exactly", 30 * time.Second, "30s"},
		{"1m tracks exactly", 1 * time.Minute, "60s"},
		{"5m hits the cap exactly", 5 * time.Minute, "300s"},
		{"10m gets capped at 5m", 10 * time.Minute, "300s"},
		{"1h gets capped at 5m", 1 * time.Hour, "300s"},
		{"zero falls back to cap", 0, "300s"},
		{"negative falls back to cap", -1 * time.Second, "300s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPollInterval(tc.checkIn)
			if got != tc.want {
				t.Errorf("formatPollInterval(%v) = %q, want %q", tc.checkIn, got, tc.want)
			}
		})
	}
}
