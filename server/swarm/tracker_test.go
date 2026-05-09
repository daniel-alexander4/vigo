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

func TestPeerTracker_Status(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.SetChunkSources("envoy-1", sha, map[string]int{"10.0.1.2:1531": 10}, nil, 5000)
	pt.ReportSkipped("envoy-3", sha, "insufficient_disk:available_mb=512,needed_mb=2048")

	statuses := pt.Status(sha, 10, "")
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	counts := map[string]int{}
	for _, s := range statuses {
		counts[s.Status]++
	}
	if counts["complete"] != 1 {
		t.Errorf("complete = %d, want 1", counts["complete"])
	}
	if counts["skipped"] != 1 {
		t.Errorf("skipped = %d, want 1", counts["skipped"])
	}
}

func TestPeerTracker_ActiveDownloadMsStable(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)

	sha := "abc123" + pad(58)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.SetChunkSources("envoy-1", sha, map[string]int{"server": 5}, nil, 1234)

	statuses := pt.Status(sha, 10, "")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].ActiveDownloadMs != 1234 {
		t.Errorf("ActiveDownloadMs = %d, want 1234", statuses[0].ActiveDownloadMs)
	}

	// Simulate subsequent check-ins — ActiveDownloadMs must not change.
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	time.Sleep(5 * time.Millisecond)
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")

	statuses2 := pt.Status(sha, 10, "")
	if statuses2[0].ActiveDownloadMs != 1234 {
		t.Errorf("ActiveDownloadMs changed after subsequent check-ins: got %d, want 1234",
			statuses2[0].ActiveDownloadMs)
	}
}

func TestPeerTracker_ActiveDownloadMsFallback(t *testing.T) {
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
	if statuses[0].ActiveDownloadMs != 225 {
		t.Errorf("ActiveDownloadMs fallback = %d, want 225", statuses[0].ActiveDownloadMs)
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

	// Register and revoke.
	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "e1"})
	pt.RevokeSeed(sha)

	seeds := pt.ListSeeds()
	if len(seeds) != 0 {
		t.Fatal("expected 0 active seeds after revoke")
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
	if seeds[sha].Revoked {
		t.Error("re-activated seed should not be revoked")
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

	// Revoked seeds should not be found.
	pt.RevokeSeed(sha1)
	if got := pt.FindSeedByLabel("app.bin"); got != "" {
		t.Errorf("FindSeedByLabel after revoke = %q, want empty", got)
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

// pad returns a string of '0's with the given length.
func pad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

// TestPeerTracker_RevokingRevokedTransition covers the per-envoy state
// machine after a revoke: envoys that still have the blob show as
// "revoking", envoys that have reported eviction (via absence of the
// hash from their swarm.blobs trait, detected in ingestSwarmTraits)
// show as "revoked". The tracker is additive — records are NOT cleared
// by RevokeSeed, which is what lets us remember who had the blob.
func TestPeerTracker_RevokingRevokedTransition(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "eee" + pad(61)

	// Three envoys have reported the complete blob.
	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-1"})
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.ReportComplete("envoy-2", sha, "10.0.1.2:1531")
	pt.SetChunkSources("envoy-2", sha, map[string]int{"10.0.1.1:1531": 10}, nil, 5000)
	pt.ReportComplete("envoy-3", sha, "10.0.1.3:1531")
	pt.SetChunkSources("envoy-3", sha, map[string]int{"10.0.1.1:1531": 8, "10.0.1.2:1531": 2}, nil, 6000)

	// Pre-revoke: envoy-1 is the seeder → "source", the other two →
	// "complete". The source/complete split is the visible distinction
	// between "origin of the blob" and "received every chunk via peer
	// transfer".
	before := pt.Status(sha, 10, "envoy-1")
	if len(before) != 3 {
		t.Fatalf("expected 3 envoys pre-revoke, got %d", len(before))
	}
	statusByEnvoyBefore := make(map[string]string, len(before))
	for _, s := range before {
		statusByEnvoyBefore[s.Hostname] = s.Status
	}
	if statusByEnvoyBefore["envoy-1"] != "source" {
		t.Errorf("envoy-1: pre-revoke status = %q, want source", statusByEnvoyBefore["envoy-1"])
	}
	if statusByEnvoyBefore["envoy-2"] != "complete" {
		t.Errorf("envoy-2: pre-revoke status = %q, want complete", statusByEnvoyBefore["envoy-2"])
	}
	if statusByEnvoyBefore["envoy-3"] != "complete" {
		t.Errorf("envoy-3: pre-revoke status = %q, want complete", statusByEnvoyBefore["envoy-3"])
	}

	// Revoke the seed. Status() should now return "revoking" for every
	// envoy — they all still have the blob in their local cache.
	pt.RevokeSeed(sha)
	afterRevoke := pt.Status(sha, 10, "envoy-1")
	if len(afterRevoke) != 3 {
		t.Fatalf("expected 3 envoys post-revoke (tracker is additive), got %d", len(afterRevoke))
	}
	for _, s := range afterRevoke {
		if s.Status != "revoking" {
			t.Errorf("%s: post-revoke status = %q, want revoking", s.Hostname, s.Status)
		}
	}

	// envoy-2 has finished evicting — ingestSwarmTraits calls MarkEvicted
	// on its next trait report where the hash is absent.
	pt.MarkEvicted("envoy-2", sha)
	afterEvict := pt.Status(sha, 10, "envoy-1")
	statusByEnvoy := make(map[string]string, len(afterEvict))
	for _, s := range afterEvict {
		statusByEnvoy[s.Hostname] = s.Status
	}
	if statusByEnvoy["envoy-1"] != "revoking" {
		t.Errorf("envoy-1: want revoking, got %q", statusByEnvoy["envoy-1"])
	}
	if statusByEnvoy["envoy-2"] != "revoked" {
		t.Errorf("envoy-2: want revoked, got %q", statusByEnvoy["envoy-2"])
	}
	if statusByEnvoy["envoy-3"] != "revoking" {
		t.Errorf("envoy-3: want revoking, got %q", statusByEnvoy["envoy-3"])
	}

	// Remaining envoys finish evicting.
	pt.MarkEvicted("envoy-1", sha)
	pt.MarkEvicted("envoy-3", sha)
	final := pt.Status(sha, 10, "envoy-1")
	for _, s := range final {
		if s.Status != "revoked" {
			t.Errorf("%s: final status = %q, want revoked", s.Hostname, s.Status)
		}
	}
}

// TestPeerTracker_RevokingEnvoyMidDownload covers the edge case where
// a revoke fires while an envoy is still downloading the blob. The
// envoy's status should also flip to "revoking" — the design
// deliberately collapses the two "still has the blob" states into one.
func TestPeerTracker_RevokingEnvoyMidDownload(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "fff" + pad(61)

	pt.RegisterSeed(sha, SeedState{Label: "big.iso", SeederID: "envoy-1"})
	pt.ReportComplete("envoy-1", sha, "10.0.1.1:1531")
	pt.ReportChunks("envoy-2", sha, "10.0.1.2:1531", []uint32{0, 1, 2, 3, 4}, false)

	// Pre-revoke: envoy-1 is the seeder → "source", envoy-2 is
	// mid-download → "downloading".
	before := pt.Status(sha, 10, "envoy-1")
	byEnvoy := make(map[string]string, len(before))
	for _, s := range before {
		byEnvoy[s.Hostname] = s.Status
	}
	if byEnvoy["envoy-1"] != "source" || byEnvoy["envoy-2"] != "downloading" {
		t.Fatalf("pre-revoke expected source+downloading, got %v", byEnvoy)
	}

	// Revoke mid-download.
	pt.RevokeSeed(sha)
	after := pt.Status(sha, 10, "envoy-1")
	for _, s := range after {
		if s.Status != "revoking" {
			t.Errorf("%s: want revoking after revoke-during-download, got %q", s.Hostname, s.Status)
		}
	}
}

// TestPeerTracker_RevokedSeedsReturnsAllRevokedHashes verifies the helper
// used by ingestSwarmTraits to decide which peer records to check for
// eviction after a trait report.
func TestPeerTracker_RevokedSeedsReturnsAllRevokedHashes(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	active := "aaa" + pad(61)
	revoked1 := "bbb" + pad(61)
	revoked2 := "ccc" + pad(61)

	pt.RegisterSeed(active, SeedState{Label: "active.bin", SeederID: "e1"})
	pt.RegisterSeed(revoked1, SeedState{Label: "r1.bin", SeederID: "e1"})
	pt.RegisterSeed(revoked2, SeedState{Label: "r2.bin", SeederID: "e1"})
	pt.RevokeSeed(revoked1)
	pt.RevokeSeed(revoked2)

	got := pt.RevokedSeeds()
	if len(got) != 2 {
		t.Fatalf("expected 2 revoked, got %d: %v", len(got), got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen[revoked1] || !seen[revoked2] {
		t.Errorf("expected both revoked seeds, got %v", got)
	}
	if seen[active] {
		t.Errorf("active seed should not appear in RevokedSeeds, got %v", got)
	}
}

// TestPeerTracker_MarkEvictedNoOpForUnknownEnvoy guards the no-op path
// so trait ingestion doesn't accidentally create phantom PeerState rows
// for envoys that never had the blob.
func TestPeerTracker_MarkEvictedNoOpForUnknownEnvoy(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "ddd" + pad(61)

	pt.RegisterSeed(sha, SeedState{Label: "x.bin", SeederID: "envoy-1"})
	pt.RevokeSeed(sha)
	pt.MarkEvicted("envoy-never-saw-it", sha)

	statuses := pt.Status(sha, 10, "envoy-1")
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses (nothing reported, nothing to mark), got %d: %v", len(statuses), statuses)
	}
}

// TestPeerTracker_StatusFiltersInactiveEnvoys checks the SetActiveChecker
// filter that brings the payload tracker to parity with the lockbox /
// longdrawer / gitmirror mesh aggregators. Without the filter, revoked or
// deleted envoys keep showing up on `/swarm/swarmcast` blob-detail until
// process restart.
func TestPeerTracker_StatusFiltersInactiveEnvoys(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "fff" + pad(61)

	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-active"})
	pt.ReportComplete("envoy-active", sha, "10.0.1.1:1531")
	pt.ReportComplete("envoy-revoked", sha, "10.0.1.2:1531")
	pt.ReportSkipped("envoy-deleted", sha, "trait-mismatch")

	// No checker installed → tracker returns everything (existing behavior).
	all := pt.Status(sha, 10, "envoy-active")
	if len(all) != 3 {
		t.Fatalf("with nil checker, expected 3 statuses, got %d: %+v", len(all), all)
	}

	// Install a checker that rejects the revoked + deleted envoys.
	pt.SetActiveChecker(func(id string) bool { return id == "envoy-active" })
	filtered := pt.Status(sha, 10, "envoy-active")
	if len(filtered) != 1 {
		t.Fatalf("with checker, expected 1 status, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Hostname != "envoy-active" {
		t.Errorf("expected envoy-active to survive filter, got %q", filtered[0].Hostname)
	}

	// StatusSummary respects the same filter.
	c, d, _, sk, src, _, total := pt.StatusSummary(sha)
	if src != 1 || c != 0 || d != 0 || sk != 0 || total != 0 {
		t.Errorf("summary post-filter: source=%d complete=%d downloading=%d skipped=%d total=%d, want only the 1 source",
			src, c, d, sk, total)
	}
}

// TestPeerTracker_StatusSynthesizedSeederFiltered guards the seeder
// synthesis path at line ~294 of tracker.go: when the seeder has not
// reported chunks itself (the common case) and the seeder envoy is now
// inactive, the synthesized "source" row must also be dropped. Without
// this, a revoked seeder still renders on the blob-detail page.
func TestPeerTracker_StatusSynthesizedSeederFiltered(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "f0f" + pad(61)

	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-seeder"})
	// Seeder never reports chunks (typical — it has the blob locally
	// and doesn't download from anyone).
	pt.ReportComplete("envoy-receiver", sha, "10.0.1.2:1531")

	// Active checker rejects the seeder; receiver remains.
	pt.SetActiveChecker(func(id string) bool { return id != "envoy-seeder" })
	statuses := pt.Status(sha, 10, "envoy-seeder")
	for _, s := range statuses {
		if s.Hostname == "envoy-seeder" {
			t.Errorf("inactive seeder leaked through synthesis: %+v", s)
		}
	}
	if len(statuses) != 1 || statuses[0].Hostname != "envoy-receiver" {
		t.Errorf("expected exactly receiver, got %+v", statuses)
	}
}
