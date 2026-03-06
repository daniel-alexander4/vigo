package compliance

import (
	"testing"
	"time"
)

func TestComputeStatus(t *testing.T) {
	baseInterval := 30 * time.Minute

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
