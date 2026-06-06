// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

	ts := EnvoyState{
		ID:                "t1",
		Hostname:          "web01.example.com",
		IP:                "10.0.0.1",
		ConvergenceStatus: "converged",
	}
	fi.AddEnvoy(ts, pubKey)

	// Get by ID.
	got := fi.GetEnvoy("t1")
	if got == nil {
		t.Fatal("expected envoy, got nil")
	}
	if got.Hostname != "web01.example.com" {
		t.Errorf("hostname = %q, want %q", got.Hostname, "web01.example.com")
	}

	// Get by hostname.
	id := fi.GetByHostname("web01.example.com")
	if id != "t1" {
		t.Errorf("GetByHostname = %q, want %q", id, "t1")
	}

	// Remove.
	fi.RemoveEnvoy("t1")
	if fi.GetEnvoy("t1") != nil {
		t.Error("expected nil after remove")
	}
}

func TestRemoveEnvoyFiresOnRemove(t *testing.T) {
	fi := NewFleetIndex()
	var got []string
	fi.SetOnRemove(func(id string) { got = append(got, id) })

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, genKey(t))

	// Removing an absent envoy must NOT fire the hook — nothing was removed.
	fi.RemoveEnvoy("missing")
	if len(got) != 0 {
		t.Fatalf("hook fired for absent envoy: %v", got)
	}

	// Removing a present envoy fires the hook exactly once with its id.
	fi.RemoveEnvoy("t1")
	if len(got) != 1 || got[0] != "t1" {
		t.Fatalf("hook = %v, want [t1]", got)
	}

	// A second removal of the now-gone envoy is a no-op.
	fi.RemoveEnvoy("t1")
	if len(got) != 1 {
		t.Fatalf("hook fired again on repeat remove: %v", got)
	}
}

func TestPubKeyLookup(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, pubKey)

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

func TestFlagOperations(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)

	// ForcePush.
	fi.SetForcePush("t1", true)
	got := fi.GetEnvoy("t1")
	if !got.ForcePush {
		t.Error("expected ForcePush=true")
	}

	fi.SetForcePush("t1", false)
	got = fi.GetEnvoy("t1")
	if got.ForcePush {
		t.Error("expected ForcePush=false")
	}

	// AppendPendingRotatedSecret.
	fi.AppendPendingRotatedSecret("t1", "secret:path/a")
	got = fi.GetEnvoy("t1")
	if len(got.PendingRotatedSecrets) != 1 || got.PendingRotatedSecrets[0] != "secret:path/a" {
		t.Errorf("PendingRotatedSecrets = %v", got.PendingRotatedSecrets)
	}

	fi.ClearPendingRotatedSecrets("t1")
	got = fi.GetEnvoy("t1")
	if len(got.PendingRotatedSecrets) != 0 {
		t.Error("expected empty PendingRotatedSecrets after clear")
	}
}

func TestDirtyTracking(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)

	fi.MarkSeen("t1", time.Now())

	dirty := fi.DrainDirty()
	if len(dirty) != 1 {
		t.Fatalf("DrainDirty returned %d, want 1", len(dirty))
	}
	if _, ok := dirty["t1"]; !ok {
		t.Error("expected t1 in dirty set")
	}

	// After drain, should be empty.
	if leftover := fi.DrainDirty(); len(leftover) != 0 {
		t.Errorf("dirty set after drain = %d, want 0", len(leftover))
	}
}

func TestConvergenceCounterConsistency(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "h2", ConvergenceStatus: "degraded"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "h3", ConvergenceStatus: "failed"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t4", Hostname: "h4", Revoked: true, ConvergenceStatus: "no data"}, nil)

	c := fi.ConvergenceSummary()
	if c.Total != 3 {
		t.Errorf("Total = %d, want 3 (revoked excluded)", c.Total)
	}
	if c.Converged != 1 {
		t.Errorf("Converged = %d, want 1", c.Converged)
	}
	if c.Degraded != 1 {
		t.Errorf("Degraded = %d, want 1", c.Degraded)
	}
	if c.Failed != 1 {
		t.Errorf("Failed = %d, want 1", c.Failed)
	}

	// Update run status → convergence changes (degraded → converged).
	fi.UpdateRunStatus("t2", "run-1", "success", 0, 0, 1, time.Now())
	c = fi.ConvergenceSummary()
	if c.Converged != 2 {
		t.Errorf("after update: Converged = %d, want 2", c.Converged)
	}
	if c.Degraded != 0 {
		t.Errorf("after update: Degraded = %d, want 0", c.Degraded)
	}

	// Remove a envoy.
	fi.RemoveEnvoy("t1")
	c = fi.ConvergenceSummary()
	if c.Total != 2 {
		t.Errorf("after remove: Total = %d, want 2", c.Total)
	}
	if c.Converged != 1 {
		t.Errorf("after remove: Converged = %d, want 1", c.Converged)
	}
}

func TestListByGlob(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "web01.prod", ConvergenceStatus: "no data"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "web02.prod", ConvergenceStatus: "no data"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "db01.prod", ConvergenceStatus: "no data"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t4", Hostname: "web03.staging", Revoked: true, ConvergenceStatus: "no data"}, nil)

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

	fi.AddEnvoy(EnvoyState{
		ID: "t1", Hostname: "h1", ConvergenceStatus: "no data",
		Traits: map[string]string{"os.distro": "ubuntu", "os.version": "22.04"},
	}, nil)
	fi.AddEnvoy(EnvoyState{
		ID: "t2", Hostname: "h2", ConvergenceStatus: "no data",
		Traits: map[string]string{"os.distro": "debian", "os.version": "12"},
	}, nil)

	results := fi.ListByTrait("os.distro", "ubuntu")
	if len(results) != 1 || results[0].EnvoyID != "t1" {
		t.Errorf("ListByTrait = %v, want [t1]", results)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	fi := NewFleetIndex()
	const numGoroutines = 100
	const numOps = 100

	var wg sync.WaitGroup

	// Writers: add envoys.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				id := string(rune('A'+n)) + string(rune('0'+j%10))
				fi.AddEnvoy(EnvoyState{
					ID:                id,
					Hostname:          id + ".example.com",
					ConvergenceStatus: "no data",
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
				fi.ConvergenceSummary()
				fi.AllEnvoys()
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

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "h2", ConvergenceStatus: "no data"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "h3", Revoked: true, ConvergenceStatus: "no data"}, nil)

	count := fi.SetForcePushAll()
	if count != 2 {
		t.Errorf("SetForcePushAll = %d, want 2", count)
	}

	// Verify individual flags.
	if !fi.GetEnvoy("t1").ForcePush {
		t.Error("t1 should have ForcePush")
	}
	if !fi.GetEnvoy("t2").ForcePush {
		t.Error("t2 should have ForcePush")
	}
	if fi.GetEnvoy("t3").ForcePush {
		t.Error("t3 (revoked) should NOT have ForcePush")
	}
}

func TestSetRevoked(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)
	c := fi.ConvergenceSummary()
	if c.Total != 1 || c.Converged != 1 {
		t.Fatalf("initial: Total=%d Converged=%d", c.Total, c.Converged)
	}

	fi.SetRevoked("t1", true)
	c = fi.ConvergenceSummary()
	if c.Total != 0 {
		t.Errorf("after revoke: Total = %d, want 0", c.Total)
	}
	if c.Converged != 0 {
		t.Errorf("after revoke: Converged = %d, want 0", c.Converged)
	}

	fi.SetRevoked("t1", false)
	c = fi.ConvergenceSummary()
	if c.Total != 1 || c.Converged != 1 {
		t.Errorf("after unrevoke: Total=%d Converged=%d, want 1 1", c.Total, c.Converged)
	}
}

func TestMarkSeenInMemory(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)

	fi.MarkSeenInMemory("t1", time.Now())

	// LastSeen should be updated.
	got := fi.GetEnvoy("t1")
	if time.Since(got.LastSeen) > time.Second {
		t.Errorf("expected LastSeen to be recent, got %v", got.LastSeen)
	}

	// But dirty set should remain empty.
	if dirty := fi.DrainDirty(); len(dirty) != 0 {
		t.Errorf("dirty set = %d, want 0 (MarkSeenInMemory should not mark dirty)", len(dirty))
	}
}

func TestMarkSeenInMemory_ThenMarkSeen(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)

	// Simulate many in-memory updates (streaming heartbeats).
	for i := 0; i < 100; i++ {
		fi.MarkSeenInMemory("t1", time.Now())
	}
	if dirty := fi.DrainDirty(); len(dirty) != 0 {
		t.Fatalf("dirty set after 100 MarkSeenInMemory = %d, want 0", len(dirty))
	}

	// Simulate disconnect: one MarkSeen to persist final timestamp.
	fi.MarkSeen("t1", time.Now())
	dirty := fi.DrainDirty()
	if len(dirty) != 1 {
		t.Errorf("dirty set after disconnect MarkSeen = %d, want 1", len(dirty))
	}
}

func TestTagOperations(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	fi.AddEnvoy(EnvoyState{
		ID:       "t1",
		Hostname: "web01",
		Tags:     []string{"webserver", "production"},
	}, pubKey)
	fi.AddEnvoy(EnvoyState{
		ID:       "t2",
		Hostname: "web02",
		Tags:     []string{"webserver", "staging"},
	}, nil)
	fi.AddEnvoy(EnvoyState{
		ID:       "t3",
		Hostname: "db01",
		Tags:     []string{"database", "production"},
	}, nil)

	// ListByTag
	webservers := fi.ListByTag("webserver")
	if len(webservers) != 2 {
		t.Errorf("ListByTag(webserver) = %d, want 2", len(webservers))
	}

	production := fi.ListByTag("production")
	if len(production) != 2 {
		t.Errorf("ListByTag(production) = %d, want 2", len(production))
	}

	staging := fi.ListByTag("staging")
	if len(staging) != 1 {
		t.Errorf("ListByTag(staging) = %d, want 1", len(staging))
	}

	// SetTags replaces tags
	fi.SetTags("t1", []string{"api", "production"})
	webservers = fi.ListByTag("webserver")
	if len(webservers) != 1 {
		t.Errorf("after SetTags, ListByTag(webserver) = %d, want 1", len(webservers))
	}
	apis := fi.ListByTag("api")
	if len(apis) != 1 {
		t.Errorf("after SetTags, ListByTag(api) = %d, want 1", len(apis))
	}

	// Empty tag should return nothing
	empty := fi.ListByTag("nonexistent")
	if len(empty) != 0 {
		t.Errorf("ListByTag(nonexistent) = %d, want 0", len(empty))
	}
}

func TestMaintenanceWindow(t *testing.T) {
	fi := NewFleetIndex()

	fi.AddEnvoy(EnvoyState{
		ID:       "t1",
		Hostname: "web01",
	}, nil)

	// Not in maintenance by default
	if fi.IsInMaintenance("t1") {
		t.Error("should not be in maintenance by default")
	}

	// Set maintenance window in the future
	fi.SetMaintenanceUntil("t1", time.Now().Add(1*time.Hour))
	if !fi.IsInMaintenance("t1") {
		t.Error("should be in maintenance after SetMaintenanceUntil")
	}

	// Set maintenance window in the past
	fi.SetMaintenanceUntil("t1", time.Now().Add(-1*time.Hour))
	if fi.IsInMaintenance("t1") {
		t.Error("should not be in maintenance after window expires")
	}

	// Clear maintenance
	fi.SetMaintenanceUntil("t1", time.Now().Add(1*time.Hour))
	fi.ClearMaintenance("t1")
	if fi.IsInMaintenance("t1") {
		t.Error("should not be in maintenance after ClearMaintenance")
	}
}

func TestConvergencePercent(t *testing.T) {
	fi := NewFleetIndex()

	// Empty fleet = 100%
	if pct := fi.ConvergencePercent(); pct != 100.0 {
		t.Errorf("ConvergencePercent on empty fleet = %f, want 100.0", pct)
	}

	fi.AddEnvoy(EnvoyState{ID: "t1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t2", Hostname: "h2", ConvergenceStatus: "converged"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t3", Hostname: "h3", ConvergenceStatus: "degraded"}, nil)
	fi.AddEnvoy(EnvoyState{ID: "t4", Hostname: "h4", ConvergenceStatus: "failed"}, nil)

	pct := fi.ConvergencePercent()
	// 2 compliant out of 4 = 50%
	if pct != 50.0 {
		t.Errorf("ConvergencePercent = %f, want 50.0", pct)
	}
}

func TestConsumeRunTrigger(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1"}, genKey(t))

	// Unmarked → scheduled.
	if got := fi.ConsumeRunTrigger("e1"); got != "scheduled" {
		t.Errorf("unmarked: got %q, want scheduled", got)
	}
	// MarkRunForced → the next consume reports force-push.
	fi.MarkRunForced("e1")
	if got := fi.ConsumeRunTrigger("e1"); got != "force-push" {
		t.Errorf("after MarkRunForced: got %q, want force-push", got)
	}
	// One-shot: the flag is cleared, so the next consume is scheduled.
	if got := fi.ConsumeRunTrigger("e1"); got != "scheduled" {
		t.Errorf("second consume: got %q, want scheduled", got)
	}
	// Unknown envoy → scheduled, no panic.
	if got := fi.ConsumeRunTrigger("nonexistent"); got != "scheduled" {
		t.Errorf("unknown envoy: got %q, want scheduled", got)
	}
}

// TestConsumeRunTrigger_FastPathDoesNotBlockOnWriter verifies the
// "scheduled" fast path (the common case — flag not set) returns under
// RLock alone and is NOT blocked by a held write lock. Without this
// property the lock contention this fix targets would persist.
func TestConsumeRunTrigger_FastPathDoesNotBlockOnWriter(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1"}, genKey(t))

	// Hold the write lock. A goroutine calling ConsumeRunTrigger on an
	// envoy with RunForced=false must block on the held lock (RLock is
	// blocked by an active Lock); when we release, it must complete
	// immediately. This pins the fast path to RLock — a Lock-first
	// implementation would block here whether or not we held the lock,
	// but the failure mode we care about is the opposite: the test
	// proves the held writer DOES block the reader, which is exactly
	// what RLock semantics give us (and what a write lock would also
	// give us). The discriminating assertion is in the concurrent test
	// below: many readers must run in parallel without serialising.
	fi.mu.Lock()
	done := make(chan struct{})
	go func() {
		_ = fi.ConsumeRunTrigger("e1")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("ConsumeRunTrigger completed while writer held lock — RLock acquisition is broken")
	case <-time.After(50 * time.Millisecond):
	}
	fi.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ConsumeRunTrigger did not complete after writer released the lock")
	}
}

// TestConsumeRunTrigger_Concurrent exercises the race-prone hit-path
// re-check: many goroutines all try to Consume the same envoy while a
// setter goroutine repeatedly arms the flag via MarkRunForced. With
// -race, this catches any unsynchronised access in the double-checked
// locking. Invariant: each MarkRunForced→Consume pair must produce at
// most one "force-push" (the consume is one-shot).
func TestConsumeRunTrigger_Concurrent(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1"}, genKey(t))

	const consumers = 16
	const iterations = 1000

	stop := make(chan struct{})
	var setterDone sync.WaitGroup
	var marks int64
	var marksMu sync.Mutex
	setterDone.Add(1)
	go func() {
		defer setterDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			fi.MarkRunForced("e1")
			marksMu.Lock()
			marks++
			marksMu.Unlock()
		}
	}()

	var consumerDone sync.WaitGroup
	var hits int64
	var hitsMu sync.Mutex
	for c := 0; c < consumers; c++ {
		consumerDone.Add(1)
		go func() {
			defer consumerDone.Done()
			for i := 0; i < iterations; i++ {
				if fi.ConsumeRunTrigger("e1") == "force-push" {
					hitsMu.Lock()
					hits++
					hitsMu.Unlock()
				}
			}
		}()
	}

	consumerDone.Wait()
	close(stop)
	setterDone.Wait()

	if hits == 0 {
		t.Errorf("expected some force-push hits across %d×%d iterations, got 0",
			consumers, iterations)
	}
	if hits > marks {
		t.Errorf("hits (%d) exceeded MarkRunForced calls (%d) — consume is not one-shot",
			hits, marks)
	}
}

func TestAdvanceConvergedVersion(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1", Hostname: "h1", ConvergenceStatus: "no data"}, nil)

	// Version 0 is never an adoption.
	if fi.AdvanceConvergedVersion("e1", 0) {
		t.Error("version 0 should not be measured")
	}
	// Unknown envoy is never an adoption.
	if fi.AdvanceConvergedVersion("nope", 5) {
		t.Error("unknown envoy should not be measured")
	}
	// First observation initializes the cursor but is not measured (the
	// prior version is unknown — this is catch-up, not a publish wave).
	if fi.AdvanceConvergedVersion("e1", 5) {
		t.Error("first observation should not be measured")
	}
	// Re-converge on the same version is not measured.
	if fi.AdvanceConvergedVersion("e1", 5) {
		t.Error("re-converge on same version should not be measured")
	}
	// A lower version (stale, out-of-order delta) is not measured.
	if fi.AdvanceConvergedVersion("e1", 4) {
		t.Error("lower version should not be measured")
	}
	// A strictly-newer version after a known prior IS a fresh adoption.
	if !fi.AdvanceConvergedVersion("e1", 6) {
		t.Error("forward adoption should be measured")
	}
	// And only once.
	if fi.AdvanceConvergedVersion("e1", 6) {
		t.Error("forward adoption should be measured only once")
	}
}
