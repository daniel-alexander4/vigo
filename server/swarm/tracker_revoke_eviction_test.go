// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package swarm

import (
	"strings"
	"testing"
	"time"
)

// evictionTestSHA returns a 64-char hex string with the given prefix.
func evictionTestSHA(prefix string) string {
	return prefix + strings.Repeat("0", 64-len(prefix))
}

// TestStatus_RevokingWhilePeerHasBlob verifies that once a seed is revoked,
// peers that still have the blob show status "revoking" instead of "complete"
// or "downloading". This is the core UX improvement: the operator can tell
// the revoke has been issued even before agents start reporting eviction.
func TestStatus_RevokingWhilePeerHasBlob(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("a1b2c3")

	// Register the seed and two peers — one complete, one mid-download.
	pt.RegisterSeed(sha, SeedState{
		Label:         "app.bin",
		SeederID:      "envoy-seeder",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("envoy-complete", sha, "10.0.0.1:1531")
	pt.SetChunkSources("envoy-complete", sha, map[string]int{"10.0.0.3:1531": 10}, nil, 5000)
	pt.ReportChunks("envoy-downloading", sha, "10.0.0.2:1531", []uint32{0, 1, 2}, false)

	// Sanity: before revoke, statuses are complete/downloading.
	pre := pt.Status(sha, 10, "envoy-seeder")
	statusByEnvoy := map[string]string{}
	for _, s := range pre {
		statusByEnvoy[s.Hostname] = s.Status
	}
	if statusByEnvoy["envoy-complete"] != "complete" {
		t.Errorf("pre-revoke: envoy-complete status = %q, want complete", statusByEnvoy["envoy-complete"])
	}
	if statusByEnvoy["envoy-downloading"] != "downloading" {
		t.Errorf("pre-revoke: envoy-downloading status = %q, want downloading", statusByEnvoy["envoy-downloading"])
	}

	// Revoke the seed.
	pt.RevokeSeed(sha)

	// After revoke, both peers should report "revoking" (they still have
	// the blob locally — the server hasn't seen an eviction report yet).
	post := pt.Status(sha, 10, "envoy-seeder")
	statusByEnvoy = map[string]string{}
	for _, s := range post {
		statusByEnvoy[s.Hostname] = s.Status
	}
	if statusByEnvoy["envoy-complete"] != "revoking" {
		t.Errorf("post-revoke: envoy-complete status = %q, want revoking", statusByEnvoy["envoy-complete"])
	}
	if statusByEnvoy["envoy-downloading"] != "revoking" {
		t.Errorf("post-revoke: envoy-downloading status = %q, want revoking", statusByEnvoy["envoy-downloading"])
	}
}

// TestStatus_RevokedAfterEviction verifies the full state transition:
// revoke → peer reports eviction (via MarkEvicted) → status becomes "revoked".
// This is the assertion requested in pending.md for the revoking → revoked
// transition.
func TestStatus_RevokedAfterEviction(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("d4e5f6")

	pt.RegisterSeed(sha, SeedState{
		Label:         "evicted.bin",
		SeederID:      "envoy-seeder",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("envoy-a", sha, "10.0.0.1:1531")
	pt.ReportComplete("envoy-b", sha, "10.0.0.2:1531")

	pt.RevokeSeed(sha)

	// Simulate envoy-a completing eviction (its next trait report omitted
	// the hash, so ingestSwarmTraits called MarkEvicted).
	pt.MarkEvicted("envoy-a", sha)

	statuses := pt.Status(sha, 1, "envoy-seeder")
	byHost := map[string]string{}
	for _, s := range statuses {
		byHost[s.Hostname] = s.Status
	}
	if byHost["envoy-a"] != "revoked" {
		t.Errorf("envoy-a status = %q, want revoked", byHost["envoy-a"])
	}
	if byHost["envoy-b"] != "revoking" {
		t.Errorf("envoy-b status = %q, want revoking (still holding the blob)", byHost["envoy-b"])
	}
}

// TestStatus_RevokedSeed_SkippedPeersOmitted verifies that "skipped" entries
// are dropped from the status response once the seed is revoked — there's
// nothing useful for the operator to see from an envoy that never had the
// blob in the first place.
func TestStatus_RevokedSeed_SkippedPeersOmitted(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("999999")

	pt.RegisterSeed(sha, SeedState{
		Label:         "skip-test.bin",
		SeederID:      "envoy-seeder",
		DistributedAt: time.Now(),
	})
	pt.ReportComplete("envoy-had-it", sha, "10.0.0.1:1531")
	pt.ReportSkipped("envoy-skipped", sha, "disk full")

	pre := pt.Status(sha, 1, "envoy-seeder")
	// envoy-had-it (complete) + envoy-skipped (skipped) + envoy-seeder
	// (synthesized source entry, since the seeder never reported chunks).
	if len(pre) != 3 {
		t.Fatalf("pre-revoke: expected 3 statuses (source + complete + skipped), got %d", len(pre))
	}

	pt.RevokeSeed(sha)

	post := pt.Status(sha, 1, "envoy-seeder")
	// Post-revoke: skipped entry dropped (nothing to evict), synthetic
	// source entry not added (we don't invent state for revoked seeds).
	// Only the envoy-had-it entry remains, now flagged revoking.
	if len(post) != 1 {
		t.Fatalf("post-revoke: expected 1 status (skipped and synthetic-source entries omitted), got %d", len(post))
	}
	if post[0].Hostname != "envoy-had-it" {
		t.Errorf("post-revoke: only entry should be envoy-had-it, got %q", post[0].Hostname)
	}
}

// TestMarkEvicted_NoPeerRecordIsNoop verifies MarkEvicted does nothing when
// the envoy has no existing record for the hash (can happen if trait ingestion
// races with revocation — an envoy that never had the blob shouldn't be
// synthesized into the peer table via a missing-from-report signal).
func TestMarkEvicted_NoPeerRecordIsNoop(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("e1e2e3")

	// No ReportComplete/ReportChunks/ReportSkipped — envoy has no record.
	pt.MarkEvicted("envoy-ghost", sha)

	// Status should return empty (no per-envoy records exist).
	statuses := pt.Status(sha, 1, "envoy-seeder")
	for _, s := range statuses {
		if s.Hostname == "envoy-ghost" {
			t.Errorf("MarkEvicted synthesized a phantom peer record for envoy-ghost")
		}
	}
}

// TestMarkEvicted_IdempotentOnEvictedPeer verifies that calling MarkEvicted
// twice on the same (envoy, sha) is a no-op — EvictedAt is only set on the
// first transition from has-blob to evicted, preserving the original timestamp.
func TestMarkEvicted_IdempotentOnEvictedPeer(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("f1f2f3")

	pt.RegisterSeed(sha, SeedState{SeederID: "envoy-seeder", DistributedAt: time.Now()})
	pt.ReportComplete("envoy-a", sha, "10.0.0.1:1531")
	pt.RevokeSeed(sha)

	pt.MarkEvicted("envoy-a", sha)
	firstEvictedAt := pt.peers[sha]["envoy-a"].EvictedAt

	time.Sleep(10 * time.Millisecond)
	pt.MarkEvicted("envoy-a", sha)
	secondEvictedAt := pt.peers[sha]["envoy-a"].EvictedAt

	if !firstEvictedAt.Equal(secondEvictedAt) {
		t.Errorf("EvictedAt changed on repeat MarkEvicted: %v → %v", firstEvictedAt, secondEvictedAt)
	}
}

// TestRevokedSeeds_ReturnsOnlyRevoked verifies RevokedSeeds returns only the
// sha256 of seeds currently flagged as revoked. Active seeds are omitted.
func TestRevokedSeeds_ReturnsOnlyRevoked(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	active := evictionTestSHA("aaaaaa")
	revoked1 := evictionTestSHA("bbbbbb")
	revoked2 := evictionTestSHA("cccccc")

	pt.RegisterSeed(active, SeedState{SeederID: "s", DistributedAt: time.Now()})
	pt.RegisterSeed(revoked1, SeedState{SeederID: "s", DistributedAt: time.Now()})
	pt.RegisterSeed(revoked2, SeedState{SeederID: "s", DistributedAt: time.Now()})
	pt.RevokeSeed(revoked1)
	pt.RevokeSeed(revoked2)

	list := pt.RevokedSeeds()
	if len(list) != 2 {
		t.Fatalf("RevokedSeeds returned %d, want 2", len(list))
	}
	inList := map[string]bool{}
	for _, sha := range list {
		inList[sha] = true
	}
	if !inList[revoked1] || !inList[revoked2] {
		t.Errorf("RevokedSeeds missing expected entries: got %v", list)
	}
	if inList[active] {
		t.Errorf("RevokedSeeds included active seed")
	}
}

// TestRevokedSeeds_IndexConsistencyAcrossTransitions exercises the revoked
// membership index through every status transition that maintains it:
// revoke (add), re-register / reactivation (remove), and RemoveSeed (remove).
// The index must agree with the seeds map's Revoked flags at each step — a
// drift here would either miss evictions (false negative) or scan phantom
// hashes forever (false positive).
func TestRevokedSeeds_IndexConsistencyAcrossTransitions(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	a := evictionTestSHA("a00000")
	b := evictionTestSHA("b00000")

	assertRevoked := func(step string, want ...string) {
		t.Helper()
		got := map[string]bool{}
		for _, sha := range pt.RevokedSeeds() {
			got[sha] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s: RevokedSeeds = %d entries, want %d", step, len(got), len(want))
		}
		for _, sha := range want {
			if !got[sha] {
				t.Errorf("%s: RevokedSeeds missing %s", step, sha[:6])
			}
		}
	}

	pt.RegisterSeed(a, SeedState{SeederID: "s", DistributedAt: time.Now()})
	pt.RegisterSeed(b, SeedState{SeederID: "s", DistributedAt: time.Now()})
	assertRevoked("after register (none revoked)")

	pt.RevokeSeed(a)
	pt.RevokeSeed(b)
	assertRevoked("after revoke both", a, b)

	// Re-registering a revoked seed reactivates it — index must drop it.
	pt.RegisterSeed(a, SeedState{SeederID: "s", DistributedAt: time.Now()})
	assertRevoked("after reactivating a", b)

	// Removing the remaining revoked seed must drop it from the index too.
	pt.RemoveSeed(b)
	assertRevoked("after removing b")

	// And the index recovers — re-revoking the reactivated seed re-adds it.
	pt.RevokeSeed(a)
	assertRevoked("after re-revoking a", a)
}

// TestReportBlobs_EquivalentToPerBlobCalls verifies that ReportBlobs produces
// the same tracker state as the per-blob ReportComplete/ReportChunks +
// SetChunkSources sequence it replaces — across complete, downloading, and
// with/without chunk-sources — so the lock-once batching is a pure efficiency
// change with no behavioral drift.
func TestReportBlobs_EquivalentToPerBlobCalls(t *testing.T) {
	complete := evictionTestSHA("c0")      // complete, with sources
	completeNoSrc := evictionTestSHA("c1") // complete, no sources
	downloading := evictionTestSHA("d0")   // partial, with sources
	addr := "10.0.0.1:1531"
	srcs := map[string]int{"10.0.0.2:1531": 3, "10.0.0.3:1531": 2}
	details := []ChunkDetail{{Chunk: 0, Source: "10.0.0.2:1531", DurationMs: 12}}

	// Reference tracker: the old per-blob path.
	ref := NewPeerTracker(60 * time.Second)
	ref.ReportComplete("envoy-a", complete, addr)
	ref.SetChunkSources("envoy-a", complete, srcs, details, 5000)
	ref.ReportComplete("envoy-a", completeNoSrc, addr)
	ref.ReportChunks("envoy-a", downloading, addr, []uint32{0, 1}, false)
	ref.SetChunkSources("envoy-a", downloading, srcs, nil, 1500)

	// Batched tracker: one ReportBlobs call with the same data in the same order.
	got := NewPeerTracker(60 * time.Second)
	got.ReportBlobs("envoy-a", addr, []BlobUpdate{
		{SHA256: complete, Complete: true, Sources: srcs, Details: details, ActiveDownloadMs: 5000},
		{SHA256: completeNoSrc, Complete: true},
		{SHA256: downloading, Chunks: []uint32{0, 1}, Sources: srcs, ActiveDownloadMs: 1500},
	})

	for _, sha := range []string{complete, completeNoSrc, downloading} {
		r := ref.peers[sha]["envoy-a"]
		g := got.peers[sha]["envoy-a"]
		if r == nil || g == nil {
			t.Fatalf("%s: ref=%v got=%v — one path created no PeerState", sha[:2], r, g)
		}
		if g.Complete != r.Complete || len(g.Chunks) != len(r.Chunks) ||
			g.ActiveDownloadMs != r.ActiveDownloadMs || len(g.ChunkSources) != len(r.ChunkSources) ||
			len(g.ChunkDetails) != len(r.ChunkDetails) || g.Address != r.Address {
			t.Errorf("%s: batched PeerState diverges from per-blob:\n ref=%+v\n got=%+v", sha[:2], r, g)
		}
	}
}

// TestReportBlobs_EmptyIsNoop verifies ReportBlobs with no updates touches
// nothing (the common no-swarm-blobs check-in skips the lock entirely).
func TestReportBlobs_EmptyIsNoop(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	pt.ReportBlobs("envoy-a", "10.0.0.1:1531", nil)
	if len(pt.peers) != 0 {
		t.Errorf("empty ReportBlobs created %d peer entries, want 0", len(pt.peers))
	}
}

// TestHasPeerRecord verifies the helper used by ingestSwarmTraits to decide
// whether a missing hash in a trait report represents a fresh eviction.
func TestHasPeerRecord(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("111111")

	if pt.HasPeerRecord("envoy-a", sha) {
		t.Error("HasPeerRecord returned true for non-existent entry")
	}

	pt.ReportComplete("envoy-a", sha, "10.0.0.1:1531")
	if !pt.HasPeerRecord("envoy-a", sha) {
		t.Error("HasPeerRecord returned false after ReportComplete")
	}

	// Different envoy, same blob
	if pt.HasPeerRecord("envoy-b", sha) {
		t.Error("HasPeerRecord returned true for a different envoy on the same blob")
	}

	// Same envoy, different blob
	otherSha := evictionTestSHA("222222")
	if pt.HasPeerRecord("envoy-a", otherSha) {
		t.Error("HasPeerRecord returned true for same envoy on a different blob")
	}
}

// TestRegisterSeed_ReactivationClearsPeerRecords verifies that re-distributing
// a previously-revoked seed (same SHA256) wipes the stale PeerState entries
// from the pre-revoke round. Without this, the display shows every envoy as
// "complete" for one cycle after the re-distribute because the old records
// linger until fresh trait reports arrive.
func TestRegisterSeed_ReactivationClearsPeerRecords(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("beadbe")

	// Round 1: distribute, three envoys complete, one skipped.
	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-seeder", DistributedAt: time.Now()})
	pt.ReportComplete("envoy-a", sha, "10.0.0.1:1531")
	pt.ReportComplete("envoy-b", sha, "10.0.0.2:1531")
	pt.ReportComplete("envoy-c", sha, "10.0.0.3:1531")
	pt.ReportSkipped("envoy-skip", sha, "disk full")

	// Revoke and mark all three evicted.
	pt.RevokeSeed(sha)
	pt.MarkEvicted("envoy-a", sha)
	pt.MarkEvicted("envoy-b", sha)
	pt.MarkEvicted("envoy-c", sha)

	// Confirm pre-reactivation state: peers map still populated (by design —
	// that's how the "revoked" display finds them).
	if len(pt.peers[sha]) != 3 {
		t.Fatalf("pre-reactivation: expected 3 peer records, got %d", len(pt.peers[sha]))
	}
	if _, ok := pt.skipped[sha]; !ok {
		t.Fatalf("pre-reactivation: expected skipped map entry")
	}

	// Round 2: re-distribute (same SHA). RegisterSeed should re-activate AND
	// wipe the stale peer/skipped records.
	isNew := pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-seeder", DistributedAt: time.Now()})
	if !isNew {
		t.Errorf("RegisterSeed should return true for reactivation")
	}

	// Seed is active again.
	seed, ok := pt.GetSeed(sha)
	if !ok || seed.Revoked {
		t.Errorf("seed should be active after reactivation, got Revoked=%v", seed.Revoked)
	}

	// Peer records gone — next Status() call returns no envoys (or just the
	// skipped-already-matched ones, but we cleared skipped too).
	if len(pt.peers[sha]) != 0 {
		t.Errorf("post-reactivation: expected 0 peer records, got %d", len(pt.peers[sha]))
	}
	if _, ok := pt.skipped[sha]; ok {
		t.Errorf("post-reactivation: skipped map entry should be cleared")
	}

	// Status() for the revived seed should report exactly one entry:
	// the synthesized "source" for the seeder. No other envoy has
	// reported yet for the new round.
	statuses := pt.Status(sha, 10, "envoy-seeder")
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status (synthetic source for seeder) immediately after reactivation, got %d", len(statuses))
	}
	if statuses[0].Hostname != "envoy-seeder" || statuses[0].Status != "source" {
		t.Errorf("expected synthesized source entry for envoy-seeder, got hostname=%q status=%q",
			statuses[0].Hostname, statuses[0].Status)
	}
}

// TestRegisterSeed_NonRevokedUpdateDoesNotClearPeers verifies the fix is
// scoped to the reactivation branch — a normal update of an active seed
// (e.g., operator edits .swarm manifest to change target or retention)
// must NOT wipe peer records.
func TestRegisterSeed_NonRevokedUpdateDoesNotClearPeers(t *testing.T) {
	pt := NewPeerTracker(60 * time.Second)
	sha := evictionTestSHA("feed42")

	pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-seeder", Target: "*", DistributedAt: time.Now()})
	pt.ReportComplete("envoy-a", sha, "10.0.0.1:1531")
	pt.ReportComplete("envoy-b", sha, "10.0.0.2:1531")

	// Re-register with a different target — a mutable-fields update,
	// NOT a reactivation (seed isn't revoked).
	isNew := pt.RegisterSeed(sha, SeedState{Label: "app.bin", SeederID: "envoy-seeder", Target: "*.web.*", DistributedAt: time.Now()})
	if isNew {
		t.Errorf("RegisterSeed on an active seed should return false (update, not reactivation)")
	}

	// Peer records must still be there.
	if len(pt.peers[sha]) != 2 {
		t.Errorf("non-reactivation update wiped peer records: got %d, want 2", len(pt.peers[sha]))
	}
}
