// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package fleet

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"
)

func genKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestAddGetRemoveLifecycle(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	ts := ThrallState{
		ID:               "t1",
		Hostname:         "web01.example.com",
		IP:               "10.0.0.1",
		ComplianceStatus: "compliant",
	}
	fi.AddThrall(ts, pubKey)

	// Get by ID.
	got := fi.GetThrall("t1")
	if got == nil {
		t.Fatal("expected thrall, got nil")
	}
	if got.Hostname != "web01.example.com" {
		t.Errorf("hostname = %q, want %q", got.Hostname, "web01.example.com")
	}

	// Get by hostname.
	id := fi.GetByHostname("web01.example.com")
	if id != "t1" {
		t.Errorf("GetByHostname = %q, want %q", id, "t1")
	}

	// Count.
	if fi.Count() != 1 {
		t.Errorf("Count = %d, want 1", fi.Count())
	}

	// Remove.
	fi.RemoveThrall("t1")
	if fi.GetThrall("t1") != nil {
		t.Error("expected nil after remove")
	}
	if fi.Count() != 0 {
		t.Errorf("Count after remove = %d, want 0", fi.Count())
	}
}

func TestPubKeyLookup(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, pubKey)

	got := fi.GetPubKey("t1")
	if got == nil {
		t.Fatal("expected pubkey, got nil")
	}
	if !got.Equal(pubKey) {
		t.Error("pubkey mismatch")
	}

	// Non-existent.
	if fi.GetPubKey("nonexistent") != nil {
		t.Error("expected nil for non-existent")
	}
}

func TestIsRevoked(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", Revoked: false, ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "h2", Revoked: true, ComplianceStatus: "unknown"}, nil)

	exists, revoked := fi.IsRevoked("t1")
	if !exists || revoked {
		t.Errorf("t1: exists=%v revoked=%v, want true false", exists, revoked)
	}

	exists, revoked = fi.IsRevoked("t2")
	if !exists || !revoked {
		t.Errorf("t2: exists=%v revoked=%v, want true true", exists, revoked)
	}

	exists, revoked = fi.IsRevoked("nope")
	if exists {
		t.Error("non-existent should return exists=false")
	}
}

func TestFlagOperations(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)

	// ForcePush.
	fi.SetForcePush("t1", true)
	got := fi.GetThrall("t1")
	if !got.ForcePush {
		t.Error("expected ForcePush=true")
	}

	fi.SetForcePush("t1", false)
	got = fi.GetThrall("t1")
	if got.ForcePush {
		t.Error("expected ForcePush=false")
	}

	// PendingRotatedSecrets.
	fi.SetPendingRotatedSecrets("t1", []string{"secret:path/a"})
	got = fi.GetThrall("t1")
	if len(got.PendingRotatedSecrets) != 1 || got.PendingRotatedSecrets[0] != "secret:path/a" {
		t.Errorf("PendingRotatedSecrets = %v", got.PendingRotatedSecrets)
	}

	fi.ClearPendingRotatedSecrets("t1")
	got = fi.GetThrall("t1")
	if len(got.PendingRotatedSecrets) != 0 {
		t.Error("expected empty PendingRotatedSecrets after clear")
	}
}

func TestDirtyTracking(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)

	fi.MarkSeen("t1", time.Now())
	if fi.DirtyCount() != 1 {
		t.Errorf("DirtyCount = %d, want 1", fi.DirtyCount())
	}

	dirty := fi.DrainDirty()
	if len(dirty) != 1 {
		t.Fatalf("DrainDirty returned %d, want 1", len(dirty))
	}
	if _, ok := dirty["t1"]; !ok {
		t.Error("expected t1 in dirty set")
	}

	// After drain, should be empty.
	if fi.DirtyCount() != 0 {
		t.Errorf("DirtyCount after drain = %d, want 0", fi.DirtyCount())
	}
}

func TestComplianceCounterConsistency(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "compliant"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "h2", ComplianceStatus: "drifted"}, nil)
	fi.AddThrall(ThrallState{ID: "t3", Hostname: "h3", ComplianceStatus: "failing"}, nil)
	fi.AddThrall(ThrallState{ID: "t4", Hostname: "h4", Revoked: true, ComplianceStatus: "unknown"}, nil)

	c := fi.ComplianceSummary()
	if c.Total != 3 {
		t.Errorf("Total = %d, want 3 (revoked excluded)", c.Total)
	}
	if c.Compliant != 1 {
		t.Errorf("Compliant = %d, want 1", c.Compliant)
	}
	if c.Drifted != 1 {
		t.Errorf("Drifted = %d, want 1", c.Drifted)
	}
	if c.Failing != 1 {
		t.Errorf("Failing = %d, want 1", c.Failing)
	}

	// Update run status → compliance changes.
	fi.UpdateRunStatus("t2", "success", 0, time.Now())
	c = fi.ComplianceSummary()
	if c.Compliant != 2 {
		t.Errorf("after update: Compliant = %d, want 2", c.Compliant)
	}
	if c.Drifted != 0 {
		t.Errorf("after update: Drifted = %d, want 0", c.Drifted)
	}

	// Remove a thrall.
	fi.RemoveThrall("t1")
	c = fi.ComplianceSummary()
	if c.Total != 2 {
		t.Errorf("after remove: Total = %d, want 2", c.Total)
	}
	if c.Compliant != 1 {
		t.Errorf("after remove: Compliant = %d, want 1", c.Compliant)
	}
}

func TestListByGlob(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "web01.prod", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "web02.prod", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t3", Hostname: "db01.prod", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t4", Hostname: "web03.staging", Revoked: true, ComplianceStatus: "unknown"}, nil)

	results := fi.ListByGlob("web*.prod")
	if len(results) != 2 {
		t.Fatalf("ListByGlob returned %d, want 2", len(results))
	}

	// Revoked should be excluded.
	results = fi.ListByGlob("web*")
	if len(results) != 2 {
		t.Errorf("ListByGlob with revoked excluded = %d, want 2", len(results))
	}
}

func TestListByTrait(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{
		ID: "t1", Hostname: "h1", ComplianceStatus: "unknown",
		Traits: map[string]string{"os.distro": "ubuntu", "os.version": "22.04"},
	}, nil)
	fi.AddThrall(ThrallState{
		ID: "t2", Hostname: "h2", ComplianceStatus: "unknown",
		Traits: map[string]string{"os.distro": "debian", "os.version": "12"},
	}, nil)

	results := fi.ListByTrait("os.distro", "ubuntu")
	if len(results) != 1 || results[0].ThrallID != "t1" {
		t.Errorf("ListByTrait = %v, want [t1]", results)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	fi := NewFleetIndex()
	const numGoroutines = 100
	const numOps = 100

	var wg sync.WaitGroup

	// Writers: add thralls.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				id := string(rune('A'+n)) + string(rune('0'+j%10))
				fi.AddThrall(ThrallState{
					ID:               id,
					Hostname:         id + ".example.com",
					ComplianceStatus: "unknown",
				}, nil)
			}
		}(i)
	}

	// Readers: query during writes.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				fi.ComplianceSummary()
				fi.Count()
				fi.AllThralls()
			}
		}()
	}

	wg.Wait()
	// No panics or races = pass.
}

func TestRingBuffer(t *testing.T) {
	rb := newRingBuffer(3)

	rb.Push(RunSummary{RunID: "r1"})
	rb.Push(RunSummary{RunID: "r2"})

	got := rb.Last(5) // ask for more than available
	if len(got) != 2 {
		t.Fatalf("Last(5) returned %d, want 2", len(got))
	}
	if got[0].RunID != "r2" {
		t.Errorf("got[0].RunID = %q, want r2", got[0].RunID)
	}
	if got[1].RunID != "r1" {
		t.Errorf("got[1].RunID = %q, want r1", got[1].RunID)
	}

	// Overflow ring buffer.
	rb.Push(RunSummary{RunID: "r3"})
	rb.Push(RunSummary{RunID: "r4"})

	got = rb.Last(3)
	if len(got) != 3 {
		t.Fatalf("Last(3) after overflow returned %d, want 3", len(got))
	}
	// Should be r4, r3, r2 (r1 evicted).
	if got[0].RunID != "r4" || got[1].RunID != "r3" || got[2].RunID != "r2" {
		t.Errorf("got = [%s, %s, %s], want [r4, r3, r2]", got[0].RunID, got[1].RunID, got[2].RunID)
	}
}

func TestSetForcePushAll(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "h2", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t3", Hostname: "h3", Revoked: true, ComplianceStatus: "unknown"}, nil)

	count := fi.SetForcePushAll()
	if count != 2 {
		t.Errorf("SetForcePushAll = %d, want 2", count)
	}

	// Verify individual flags.
	if !fi.GetThrall("t1").ForcePush {
		t.Error("t1 should have ForcePush")
	}
	if !fi.GetThrall("t2").ForcePush {
		t.Error("t2 should have ForcePush")
	}
	if fi.GetThrall("t3").ForcePush {
		t.Error("t3 (revoked) should NOT have ForcePush")
	}
}

func TestUpdateTraitsFromJSON(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)

	fi.UpdateTraitsFromJSON("t1", `{"os":{"distro":"ubuntu","version":"22.04"},"arch":"amd64"}`)

	results := fi.ListByTrait("os.distro", "ubuntu")
	if len(results) != 1 {
		t.Errorf("ListByTrait after UpdateTraitsFromJSON = %d, want 1", len(results))
	}

	results = fi.ListByTrait("arch", "amd64")
	if len(results) != 1 {
		t.Errorf("ListByTrait arch = %d, want 1", len(results))
	}
}

func TestSetRevoked(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "compliant"}, nil)
	c := fi.ComplianceSummary()
	if c.Total != 1 || c.Compliant != 1 {
		t.Fatalf("initial: Total=%d Compliant=%d", c.Total, c.Compliant)
	}

	fi.SetRevoked("t1", true)
	c = fi.ComplianceSummary()
	if c.Total != 0 {
		t.Errorf("after revoke: Total = %d, want 0", c.Total)
	}
	if c.Compliant != 0 {
		t.Errorf("after revoke: Compliant = %d, want 0", c.Compliant)
	}

	fi.SetRevoked("t1", false)
	c = fi.ComplianceSummary()
	if c.Total != 1 || c.Compliant != 1 {
		t.Errorf("after unrevoke: Total=%d Compliant=%d, want 1 1", c.Total, c.Compliant)
	}
}

func TestMarkSeenInMemory(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)

	fi.MarkSeenInMemory("t1", time.Now())

	// LastSeen should be updated.
	got := fi.GetThrall("t1")
	if time.Since(got.LastSeen) > time.Second {
		t.Errorf("expected LastSeen to be recent, got %v", got.LastSeen)
	}

	// But dirty count should remain 0.
	if fi.DirtyCount() != 0 {
		t.Errorf("DirtyCount = %d, want 0 (MarkSeenInMemory should not mark dirty)", fi.DirtyCount())
	}
}

func TestMarkSeenInMemory_ThenMarkSeen(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)

	// Simulate many in-memory updates (streaming heartbeats).
	for i := 0; i < 100; i++ {
		fi.MarkSeenInMemory("t1", time.Now())
	}
	if fi.DirtyCount() != 0 {
		t.Fatalf("DirtyCount after 100 MarkSeenInMemory = %d, want 0", fi.DirtyCount())
	}

	// Simulate disconnect: one MarkSeen to persist final timestamp.
	fi.MarkSeen("t1", time.Now())
	if fi.DirtyCount() != 1 {
		t.Errorf("DirtyCount after disconnect MarkSeen = %d, want 1", fi.DirtyCount())
	}
}

func TestActiveCount(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddThrall(ThrallState{ID: "t1", Hostname: "h1", ComplianceStatus: "unknown"}, nil)
	fi.AddThrall(ThrallState{ID: "t2", Hostname: "h2", Revoked: true, ComplianceStatus: "unknown"}, nil)

	if fi.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", fi.ActiveCount())
	}
}
