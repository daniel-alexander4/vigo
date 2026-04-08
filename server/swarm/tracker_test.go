// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"testing"
	"time"
)

func TestPeerTracker_ReportAndGetPeers(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	pt.ReportComplete("envoy-1", "abc123"+pad(58), "10.0.1.1:1531")
	pt.ReportChunks("envoy-2", "abc123"+pad(58), "10.0.1.2:1531", []uint32{0, 1, 2}, false)

	peers := pt.GetPeers("abc123"+pad(58), "envoy-3")
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	// Requesting envoy should be excluded.
	peers = pt.GetPeers("abc123"+pad(58), "envoy-1")
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer (excluding self), got %d", len(peers))
	}
}

func TestPeerTracker_StaleExpiry(t *testing.T) {
	pt := NewPeerTracker(1 * time.Millisecond)

	pt.ReportComplete("envoy-1", "abc123"+pad(58), "10.0.1.1:1531")
	time.Sleep(5 * time.Millisecond)

	peers := pt.GetPeers("abc123"+pad(58), "other")
	if len(peers) != 0 {
		t.Errorf("expected 0 peers after expiry, got %d", len(peers))
	}
}

func TestPeerTracker_Prune(t *testing.T) {
	pt := NewPeerTracker(1 * time.Millisecond)

	pt.ReportComplete("envoy-1", "abc123"+pad(58), "10.0.1.1:1531")
	time.Sleep(5 * time.Millisecond)

	pt.Prune()

	// After prune, the blob entry should be fully cleaned up.
	peers := pt.GetPeers("abc123"+pad(58), "other")
	if len(peers) != 0 {
		t.Errorf("expected 0 peers after prune, got %d", len(peers))
	}
}

func TestPeerTracker_Assignment(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	a := AssignmentState{SHA256: "abc123" + pad(58), Size: 1048576, Label: "test.bin"}
	pt.Assign("envoy-1", a)
	pt.Assign("envoy-1", a) // duplicate — should be ignored

	assignments := pt.PopAssignments("envoy-1")
	if len(assignments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(assignments))
	}
	if assignments[0].Label != "test.bin" {
		t.Errorf("label = %q, want %q", assignments[0].Label, "test.bin")
	}
}

func TestPeerTracker_AssignmentSkipsComplete(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.Assign("envoy-1", AssignmentState{SHA256: sha, Size: 1048576})

	// Mark as complete.
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	// Pop should return empty since the envoy already has it.
	assignments := pt.PopAssignments("envoy-1")
	if len(assignments) != 0 {
		t.Errorf("expected 0 assignments (already complete), got %d", len(assignments))
	}
}

func TestPeerTracker_Status(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.Assign("envoy-1", AssignmentState{SHA256: sha, Size: 10485760})
	pt.Assign("envoy-2", AssignmentState{SHA256: sha, Size: 10485760})
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.ReportSkipped("envoy-3", sha, "insufficient_disk:available_mb=512,needed_mb=2048")

	statuses := pt.Status(sha, 10, "")
	if len(statuses) != 3 {
		t.Fatalf("expected 3 statuses, got %d", len(statuses))
	}

	counts := map[string]int{}
	for _, s := range statuses {
		counts[s.Status]++
	}
	if counts["complete"] != 1 {
		t.Errorf("complete = %d, want 1", counts["complete"])
	}
	if counts["pending"] != 1 {
		t.Errorf("pending = %d, want 1", counts["pending"])
	}
	if counts["skipped"] != 1 {
		t.Errorf("skipped = %d, want 1", counts["skipped"])
	}
}

func TestPeerTracker_CompletedAtFreezes(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.ReportChunks("envoy-1", sha, "10.0.1.1:1531", []uint32{0}, false)
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	statuses := pt.Status(sha, 10, "")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	completedAt := statuses[0].CompletedAt
	if completedAt.IsZero() {
		t.Fatal("CompletedAt should not be zero after completion")
	}
	duration := completedAt.Sub(statuses[0].StartedAt)
	if duration <= 0 {
		t.Fatalf("distribution duration should be positive, got %v", duration)
	}

	// Simulate subsequent check-ins — CompletedAt must not change.
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	statuses2 := pt.Status(sha, 10, "")
	if len(statuses2) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses2))
	}
	if !statuses2[0].CompletedAt.Equal(completedAt) {
		t.Errorf("CompletedAt changed after subsequent check-ins: was %v, now %v",
			completedAt, statuses2[0].CompletedAt)
	}
}

func TestParseBlobsTrait(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"abc123" + pad(58), 1},
		{"abc123" + pad(58) + ",def456" + pad(58), 2},
		{"invalid", 0},
		{"abc123" + pad(58) + ", def456" + pad(58), 2}, // with spaces
	}
	for _, tt := range tests {
		got := ParseBlobsTrait(tt.input)
		if len(got) != tt.want {
			t.Errorf("ParseBlobsTrait(%q) returned %d hashes, want %d", tt.input, len(got), tt.want)
		}
	}
}

// pad returns a string of '0's with the given length.
func pad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}
