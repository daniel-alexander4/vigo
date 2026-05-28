// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"testing"
	"time"
)

// TestFormatPollInterval pins the UI's HTMX poll cadence derivation:
// match the configured check-in interval (so the UI is exactly as fresh
// as the data), capped at 5 minutes so a long-interval fleet doesn't
// look frozen. Zero / negative inputs fall back to the cap.
func TestFormatPollInterval(t *testing.T) {
	cases := []struct {
		name    string
		checkIn time.Duration
		want    string
	}{
		{"sub-second floor matches check-in", 1 * time.Second, "1s"},
		{"5s check-in tracks exactly", 5 * time.Second, "5s"},
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
