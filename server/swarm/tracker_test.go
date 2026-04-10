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

func TestPeerTracker_DownloadMsStable(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.SetChunkSources("envoy-1", sha, map[string]int{"server": 5}, nil, 1234)

	statuses := pt.Status(sha, 10, "")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].DownloadMs != 1234 {
		t.Errorf("DownloadMs = %d, want 1234", statuses[0].DownloadMs)
	}

	// Simulate subsequent check-ins — DownloadMs must not change.
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	statuses2 := pt.Status(sha, 10, "")
	if statuses2[0].DownloadMs != 1234 {
		t.Errorf("DownloadMs changed after subsequent check-ins: got %d, want 1234",
			statuses2[0].DownloadMs)
	}
}

func TestPeerTracker_DownloadMsFallback(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	// Set chunk details without total download time (pre-0.18.18 agent).
	details := []ChunkDetail{
		{Chunk: 0, Source: "server", DurationMs: 100},
		{Chunk: 1, Source: "10.0.1.2:1531", DurationMs: 50},
		{Chunk: 2, Source: "server", DurationMs: 75},
	}
	pt.SetChunkSources("envoy-1", sha, map[string]int{"server": 2, "10.0.1.2:1531": 1}, details, 0)

	statuses := pt.Status(sha, 3, "")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	// Fallback: sum of chunk durations (100 + 50 + 75 = 225).
	if statuses[0].DownloadMs != 225 {
		t.Errorf("DownloadMs fallback = %d, want 225", statuses[0].DownloadMs)
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

func TestPeerTracker_RegisterSeedReactivation(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "a1b2c3" + pad(58)

	// Register and retire.
	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "e1"})
	pt.RetireSeed(sha)

	seeds := pt.ListSeeds()
	if len(seeds) != 0 {
		t.Fatal("expected 0 active seeds after retire")
	}

	// Re-register should re-activate.
	isNew := pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "e1"})
	if !isNew {
		t.Error("RegisterSeed should return true for re-activated seed")
	}
	seeds = pt.ListSeeds()
	if len(seeds) != 1 {
		t.Fatalf("expected 1 active seed after re-activation, got %d", len(seeds))
	}
	if seeds[sha].Retired {
		t.Error("re-activated seed should not be retired")
	}
}

func TestPeerTracker_FindSeedByLabel(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha1 := "a1b2c3" + pad(58)
	sha2 := "d4e5f6" + pad(58)

	pt.RegisterSeed(sha1, SeedState{Label: "app.bin", SeederID: "e1"})
	pt.RegisterSeed(sha2, SeedState{Label: "config.tar", SeederID: "e1"})

	if got := pt.FindSeedByLabel("app.bin"); got != sha1 {
		t.Errorf("FindSeedByLabel(app.bin) = %q, want %q", got, sha1)
	}
	if got := pt.FindSeedByLabel("missing"); got != "" {
		t.Errorf("FindSeedByLabel(missing) = %q, want empty", got)
	}

	// Retired seeds should not be found.
	pt.RetireSeed(sha1)
	if got := pt.FindSeedByLabel("app.bin"); got != "" {
		t.Errorf("FindSeedByLabel after retire = %q, want empty", got)
	}
}

func TestPeerTracker_SeedsForEnvoy(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha1 := "a1b2c3" + pad(58)
	sha2 := "d4e5f6" + pad(58)

	pt.RegisterSeed(sha1, SeedState{Label: "a", SeederID: "e1"})
	pt.RegisterSeed(sha2, SeedState{Label: "b", SeederID: "e2"})

	seeds := pt.SeedsForEnvoy("e1")
	if len(seeds) != 1 || seeds[0] != sha1 {
		t.Errorf("SeedsForEnvoy(e1) = %v, want [%s]", seeds, sha1)
	}
	if seeds2 := pt.SeedsForEnvoy("e3"); len(seeds2) != 0 {
		t.Errorf("SeedsForEnvoy(e3) should be empty, got %v", seeds2)
	}
}

func TestPeerTracker_HasOtherCompletePeer(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "a1b2c3" + pad(58)

	pt.ReportComplete("e1", sha, "10.0.1.1:1531")
	if pt.HasOtherCompletePeer(sha, "e1") {
		t.Error("should be false when only e1 has the blob")
	}

	pt.ReportComplete("e2", sha, "10.0.1.2:1531")
	if !pt.HasOtherCompletePeer(sha, "e1") {
		t.Error("should be true when e2 also has the blob")
	}
}

func TestMatchTarget(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		traits   map[string]string
		spec     string
		want     bool
	}{
		{"wildcard", "web-01", nil, "*", true},
		{"glob match", "web-01", nil, "web-*", true},
		{"glob no match", "db-01", nil, "web-*", false},
		{"trait match", "web-01", map[string]string{"os": "ubuntu"}, "os=ubuntu", true},
		{"trait no match", "web-01", map[string]string{"os": "debian"}, "os=ubuntu", false},
		{"exclude", "rasp-01", nil, "-rasp*", false},
		{"exclude no match", "web-01", nil, "-rasp*", true},
		{"include and exclude", "web-01", nil, "web-*,-rasp*", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTarget(tt.hostname, tt.traits, tt.spec); got != tt.want {
				t.Errorf("MatchTarget(%q, %q) = %v, want %v", tt.hostname, tt.spec, got, tt.want)
			}
		})
	}
}

func TestPeerTracker_AssignDedup(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "a1b2c3" + pad(58)
	a := AssignmentState{SHA256: sha, Size: 1000, Label: "test"}

	if !pt.Assign("e1", a) {
		t.Error("first Assign should return true")
	}
	if pt.Assign("e1", a) {
		t.Error("duplicate Assign should return false")
	}
	if !pt.Assign("e2", a) {
		t.Error("Assign to different envoy should return true")
	}
}

func TestPeerTracker_CompletePeerCount(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "a1b2c3" + pad(58)

	if pt.CompletePeerCount(sha, "") != 0 {
		t.Error("should be 0 with no peers")
	}

	pt.ReportComplete("e1", sha, "10.0.1.1:1531")
	pt.ReportComplete("e2", sha, "10.0.1.2:1531")
	pt.ReportChunks("e3", sha, "10.0.1.3:1531", []uint32{0, 1}, false)

	if got := pt.CompletePeerCount(sha, "e1"); got != 1 {
		t.Errorf("CompletePeerCount excluding e1 = %d, want 1", got)
	}
	if got := pt.CompletePeerCount(sha, ""); got != 2 {
		t.Errorf("CompletePeerCount excluding none = %d, want 2", got)
	}
}

func TestPeerTracker_AssignedCount(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha1 := "a1b2c3" + pad(58)
	sha2 := "d4e5f6" + pad(58)

	pt.Assign("e1", AssignmentState{SHA256: sha1})
	pt.Assign("e2", AssignmentState{SHA256: sha1})
	pt.Assign("e2", AssignmentState{SHA256: sha2})

	if got := pt.AssignedCount(sha1); got != 2 {
		t.Errorf("AssignedCount(sha1) = %d, want 2", got)
	}
	if got := pt.AssignedCount(sha2); got != 1 {
		t.Errorf("AssignedCount(sha2) = %d, want 1", got)
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
