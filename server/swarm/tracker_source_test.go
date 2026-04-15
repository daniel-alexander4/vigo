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
	pt.ReportChunks("plex", sha, "192.168.1.29:1531", []uint32{0, 1, 2}, false)

	c, d, p, sk, src, total := pt.StatusSummary(sha)
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
	if total != 3 {
		t.Errorf("total = %d, want 3 (seeder + 2 peers)", total)
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

	c, _, _, _, src, total := pt.StatusSummary(sha)
	if src != 1 {
		t.Errorf("source = %d, want 1", src)
	}
	if c != 0 {
		t.Errorf("complete = %d, want 0 (seeder must not double-count)", c)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}
