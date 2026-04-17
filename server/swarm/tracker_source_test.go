// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"testing"
	"time"
)

// TestStatus_SynthesizesSourceForUnreportedSeeder pins the fix for the
// "vigocli swarm status shows seeder as pending" bug. The seeder has
// every chunk by construction and normally never reports peer stats,
// so it's absent from the tracker's peers[] map. Status() must
// synthesize an entry for it with Status="source" — otherwise the
// API handler's pending fan-out downstream emits the seeder as
// "pending", which is visibly wrong.
func TestStatus_SynthesizesSourceForUnreportedSeeder(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "aaa" + pad(61)

	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})

	statuses := pt.Status(sha, 10, "danlap")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 synthesized source entry, got %d: %+v", len(statuses), statuses)
	}
	got := statuses[0]
	if got.Hostname != "danlap" {
		t.Errorf("hostname = %q, want danlap", got.Hostname)
	}
	if got.Status != "source" {
		t.Errorf("status = %q, want source", got.Status)
	}
	if got.ChunksHave != 10 || got.ChunksNeed != 10 {
		t.Errorf("chunks_have=%d chunks_need=%d, want 10/10", got.ChunksHave, got.ChunksNeed)
	}
}

// TestStatus_SeederOverridesCompleteStatus verifies that when a
// seeder also happens to be in peers[] (e.g., it ran ReportComplete
// on itself as a liveness heartbeat), the "source" label still wins.
// We don't want operators seeing the seeder drift between "complete"
// and "source" depending on whether it self-reported.
func TestStatus_SeederOverridesCompleteStatus(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "bbb" + pad(61)

	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("danlap", sha, "192.168.1.2:1531")

	statuses := pt.Status(sha, 10, "danlap")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(statuses))
	}
	if statuses[0].Status != "source" {
		t.Errorf("seeder status = %q, want source (label must win over complete)", statuses[0].Status)
	}
}

// TestStatus_NoSyntheticSourceForRevokedSeed verifies the honesty
// clause: when the blob is revoked and the seeder hasn't reported
// chunks, Status() must not invent a revoking/revoked entry for it.
// We can't observe what the seeder's local state is post-revoke if
// it never registered any peer stats in the first place.
func TestStatus_NoSyntheticSourceForRevokedSeed(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "ccc" + pad(61)

	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})
	pt.RevokeSeed(sha)

	statuses := pt.Status(sha, 10, "danlap")
	if len(statuses) != 0 {
		t.Errorf("expected 0 entries post-revoke with no peer stats, got %d: %+v",
			len(statuses), statuses)
	}
}

// TestStatusSummary_CountsSeederAsSource pins that the aggregate
// summary (used by the list endpoint and dashboard) also counts the
// seeder in the source bucket rather than silently dropping it out
// of the total.
func TestStatusSummary_CountsSeederAsSource(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "ddd" + pad(61)

	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("annlap", sha, "192.168.1.74:1531")
	pt.SetChunkSources("annlap", sha, map[string]int{"192.168.1.2:1531": 10}, nil, 8000)
	pt.ReportChunks("plex", sha, "192.168.1.29:1531", []uint32{0, 1, 2}, false)

	c, d, p, sk, src, _, total := pt.StatusSummary(sha)
	if src != 1 {
		t.Errorf("source = %d, want 1 (the seeder)", src)
	}
	if c != 1 {
		t.Errorf("complete = %d, want 1 (annlap)", c)
	}
	if d != 1 {
		t.Errorf("downloading = %d, want 1 (plex)", d)
	}
	if p != 0 {
		t.Errorf("pending = %d, want 0", p)
	}
	if sk != 0 {
		t.Errorf("skipped = %d, want 0", sk)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (targets only, excludes seeder)", total)
	}
}

// TestStatusSummary_SeederInPeersBucketsAsSource verifies the seeder
// is counted once in source even when it has a ReportComplete record
// in peers[] — avoids a double-count that would otherwise show
// source=1 AND complete=1 for a single envoy.
func TestStatusSummary_SeederInPeersBucketsAsSource(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "eee" + pad(61)

	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("danlap", sha, "192.168.1.2:1531")

	c, _, _, _, src, _, total := pt.StatusSummary(sha)
	if src != 1 {
		t.Errorf("source = %d, want 1", src)
	}
	if c != 0 {
		t.Errorf("complete = %d, want 0 (seeder must not double-count)", c)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 (only the seeder exists, targets = 0)", total)
	}
}

// TestStatus_CachedVsComplete verifies that envoys whose FirstSeen
// predates the seed's DistributedAt are reported as "cached" (they
// already had the blob from a previous distribution), while envoys
// that reported after DistributedAt show as "complete".
func TestStatus_CachedVsComplete(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := "fff" + pad(61)
	totalChunks := 10

	// Phase 1: original distribution. Both envoys download.
	pt.RegisterSeed(sha, SeedState{
		Label:         "win.iso",
		SeederID:      "danlap",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("annlap", sha, "192.168.1.74:1531")
	pt.SetChunkSources("annlap", sha, map[string]int{"192.168.1.2:1531": 10}, nil, 8000)
	pt.ReportComplete("plex", sha, "192.168.1.29:1531")
	pt.SetChunkSources("plex", sha, map[string]int{"192.168.1.2:1531": 6, "192.168.1.74:1531": 4}, nil, 12345)

	// Sanity: both are "complete" after the first distribution.
	pre := pt.Status(sha, totalChunks, "danlap")
	preMap := make(map[string]string)
	for _, s := range pre {
		preMap[s.Hostname] = s.Status
	}
	if preMap["annlap"] != "complete" {
		t.Errorf("pre re-seed: annlap = %q, want complete", preMap["annlap"])
	}

	// Phase 2: re-distribute the same SHA (non-revoked re-seed).
	// RegisterSeed does NOT update DistributedAt (to avoid the trait
	// re-registration race). The Distribute handler calls
	// TouchDistributedAt explicitly. Both envoys' FirstSeen predates
	// the new DistributedAt → both should show "cached".
	time.Sleep(2 * time.Millisecond) // ensure DistributedAt is strictly after FirstSeen
	pt.RegisterSeed(sha, SeedState{
		Label:    "win.iso",
		SeederID: "danlap",
	})
	pt.TouchDistributedAt(sha)

	statuses := pt.Status(sha, totalChunks, "danlap")
	statusMap := make(map[string]string)
	for _, s := range statuses {
		statusMap[s.Hostname] = s.Status
	}

	if statusMap["annlap"] != "cached" {
		t.Errorf("annlap status = %q, want \"cached\" (FirstSeen < DistributedAt)", statusMap["annlap"])
	}
	if statusMap["plex"] != "cached" {
		t.Errorf("plex status = %q, want \"cached\" (FirstSeen < DistributedAt)", statusMap["plex"])
	}
	if statusMap["danlap"] != "source" {
		t.Errorf("danlap status = %q, want \"source\"", statusMap["danlap"])
	}

	// Summary should count correctly.
	c, _, _, _, src, cached, total := pt.StatusSummary(sha)
	if cached != 2 {
		t.Errorf("cached = %d, want 2 (annlap + plex)", cached)
	}
	if c != 0 {
		t.Errorf("complete = %d, want 0 (all targets are cached)", c)
	}
	if src != 1 {
		t.Errorf("source = %d, want 1 (danlap)", src)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (targets only, excludes seeder)", total)
	}

	// Phase 3: a new envoy joins and downloads fresh. Its FirstSeen
	// is after DistributedAt → should show "complete", not "cached".
	time.Sleep(2 * time.Millisecond)
	pt.ReportComplete("girlslaptop", sha, "192.168.1.50:1531")
	pt.SetChunkSources("girlslaptop", sha, map[string]int{"192.168.1.2:1531": 10}, nil, 5000)

	statuses2 := pt.Status(sha, totalChunks, "danlap")
	statusMap2 := make(map[string]string)
	for _, s := range statuses2 {
		statusMap2[s.Hostname] = s.Status
	}
	if statusMap2["girlslaptop"] != "complete" {
		t.Errorf("girlslaptop status = %q, want \"complete\" (new envoy, FirstSeen > DistributedAt)", statusMap2["girlslaptop"])
	}
}
