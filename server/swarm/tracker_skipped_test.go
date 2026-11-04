// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"testing"
	"time"
)

// Skip records are dropped with the seed they describe, and Prune clears
// records for blobs no seed or peer references any more.
func TestTrackerSkippedPruned(t *testing.T) {
	pt := NewPeerTracker(time.Millisecond)
	pt.RegisterSeed("sha-a", SeedState{SeederID: "s", Label: "a"})
	pt.ReportSkipped("e1", "sha-a", "disk full")
	pt.RemoveSeed("sha-a")
	if _, ok := pt.skipped["sha-a"]; ok {
		t.Fatal("RemoveSeed left the blob's skip records behind")
	}

	pt.ReportSkipped("e2", "sha-orphan", "disk full")
	pt.Prune()
	if _, ok := pt.skipped["sha-orphan"]; ok {
		t.Fatal("Prune kept skip records for a blob with no seed and no peers")
	}

	pt.RegisterSeed("sha-live", SeedState{SeederID: "s", Label: "l"})
	pt.ReportSkipped("e3", "sha-live", "disk full")
	pt.Prune()
	if _, ok := pt.skipped["sha-live"]; !ok {
		t.Fatal("Prune dropped skip records for a live seed")
	}
}
