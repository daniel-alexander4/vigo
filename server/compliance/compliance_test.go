// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package compliance

import (
	"testing"
	"time"
)

func TestComputeStatus(t *testing.T) {
	baseInterval := 5 * time.Minute

	tests := []struct {
		name         string
		lastRun      *RunInfo
		lastSeen     time.Time
		baseInterval time.Duration
		want         string
	}{
		{
			name:         "stale when last_seen exceeds 2x base interval",
			lastRun:      &RunInfo{Status: "success", ChangedCount: 0},
			lastSeen:     time.Now().Add(-61 * time.Minute),
			baseInterval: baseInterval,
			want:         "stale",
		},
		{
			name:         "unknown when no last run",
			lastRun:      nil,
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "unknown",
		},
		{
			name:         "compliant when success with zero changes",
			lastRun:      &RunInfo{Status: "success", ChangedCount: 0},
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "compliant",
		},
		{
			name:         "drifted when success with changes",
			lastRun:      &RunInfo{Status: "success", ChangedCount: 3},
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "drifted",
		},
		{
			name:         "failing when status is failure",
			lastRun:      &RunInfo{Status: "failure", ChangedCount: 0},
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "failing",
		},
		{
			name:         "failing when status is partial",
			lastRun:      &RunInfo{Status: "partial", ChangedCount: 1},
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "failing",
		},
		{
			name:         "unknown for unrecognized run status",
			lastRun:      &RunInfo{Status: "running", ChangedCount: 0},
			lastSeen:     time.Now(),
			baseInterval: baseInterval,
			want:         "unknown",
		},
		{
			name:         "stale takes priority over compliant",
			lastRun:      &RunInfo{Status: "success", ChangedCount: 0},
			lastSeen:     time.Now().Add(-2 * time.Hour),
			baseInterval: baseInterval,
			want:         "stale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeStatus(tt.lastRun, tt.lastSeen, tt.baseInterval)
			if got != tt.want {
				t.Errorf("ComputeStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	statuses := []string{"compliant", "compliant", "drifted", "failing", "stale", "unknown", "compliant"}

	got := Summary(statuses)

	want := map[string]int32{
		"compliant": 3,
		"drifted":   1,
		"failing":   1,
		"stale":     1,
		"unknown":   1,
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summary()[%q] = %d, want %d", k, got[k], v)
		}
	}

	// Verify total count
	var total int32
	for _, v := range got {
		total += v
	}
	if total != 7 {
		t.Errorf("Summary total = %d, want 7", total)
	}
}

func TestSummaryEmpty(t *testing.T) {
	got := Summary(nil)
	if len(got) != 0 {
		t.Errorf("Summary(nil) should return empty map, got %v", got)
	}
}

func TestComputeStatusBoundary(t *testing.T) {
	base := 5 * time.Minute

	// Exactly at 2x boundary (10min) — should be stale (> check).
	staleAt := time.Now().Add(-2*base - time.Millisecond)
	got := ComputeStatus(&RunInfo{Status: "success", ChangedCount: 0}, staleAt, base)
	if got != "stale" {
		t.Errorf("at 2x+1ms boundary: got %q, want stale", got)
	}

	// Just inside 2x boundary — should NOT be stale.
	freshAt := time.Now().Add(-2*base + time.Second)
	got = ComputeStatus(&RunInfo{Status: "success", ChangedCount: 0}, freshAt, base)
	if got != "compliant" {
		t.Errorf("at 2x-1s boundary: got %q, want compliant", got)
	}
}

func TestComputeStatusZeroBaseInterval(t *testing.T) {
	// With zero base interval, 2*0 = 0, so any non-zero time.Since will exceed it.
	got := ComputeStatus(&RunInfo{Status: "success"}, time.Now().Add(-time.Millisecond), 0)
	if got != "stale" {
		t.Errorf("zero interval: got %q, want stale", got)
	}
}

func TestComputeStatusNegativeChangedCount(t *testing.T) {
	// Negative ChangedCount should still be treated as "drifted" (not zero).
	got := ComputeStatus(&RunInfo{Status: "success", ChangedCount: -1}, time.Now(), 5*time.Minute)
	if got != "drifted" {
		t.Errorf("negative ChangedCount: got %q, want drifted", got)
	}
}

func TestComputeStatusEmptyStatus(t *testing.T) {
	got := ComputeStatus(&RunInfo{Status: "", ChangedCount: 0}, time.Now(), 5*time.Minute)
	if got != "unknown" {
		t.Errorf("empty status: got %q, want unknown", got)
	}
}
