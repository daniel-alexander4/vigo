// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// TestAuditStatusCache_StoreCarriesFirstBroken pins that the cached status
// carries FirstBroken from the verify result — the field the audit-page badge
// renders now that it reads the cache instead of running VerifyFull on load.
func TestAuditStatusCache_StoreCarriesFirstBroken(t *testing.T) {
	c := &auditStatusCache{}
	c.store(&audit.VerifyResult{Valid: false, EntryCount: 9, FirstBroken: 4, Error: "chain broken at 4"})
	if c.status.FirstBroken != 4 {
		t.Errorf("FirstBroken = %d, want 4", c.status.FirstBroken)
	}
	if c.status.Valid || c.status.EntryCount != 9 || c.status.Error != "chain broken at 4" {
		t.Errorf("status = %+v, want the verify result's fields carried through", c.status)
	}
}

// TestReportHandler_CachedAuditStatus pins the TTL-cache behavior of
// cachedAuditStatus. The verify reads + rehashes every audit_entries
// row — at 1.5M entries that's a multi-hundred-millisecond per-call
// cost, and it used to fire per-framework-score (21× per
// PersistComplianceSnapshot). Caching must:
//   - populate on the first (cold) call (cachedAt set, status reflects DB state)
//   - return the same value on subsequent calls inside the TTL window
//     WITHOUT re-running audit.Verify (proven by an unchanged cachedAt)
//   - once expired, serve the stale value immediately and refresh in the
//     background (serve-stale-while-revalidate) — the request never blocks
//     on the O(N) verify
//
// Whitebox test (same package) so it can read auditStatusCache.cachedAt
// directly — the discriminating assertion is "cachedAt did not move".
func TestReportHandler_CachedAuditStatus(t *testing.T) {
	sqlDB := openMigratedTestDB(t)
	queries := db.New(sqlDB)

	// Seed two hash-chained audit entries.
	now := time.Now().UTC().Truncate(time.Second)
	h1 := insertAuditEntry(t, sqlDB, "", "config.reload", "alice", "first", now.Add(-2*time.Minute))
	insertAuditEntry(t, sqlDB, h1, "config.reload", "bob", "second", now.Add(-1*time.Minute))

	h := NewReportHandler(queries, fleet.NewFleetIndex(), nil, "test")
	ctx := context.Background()

	// First (cold) call: returns the not-yet-verified sentinel immediately —
	// no synchronous walk on the request path — and kicks a background verify.
	got1 := h.cachedAuditStatus(ctx)
	if got1.Valid || got1.EntryCount != 0 || got1.Error != "" {
		t.Errorf("cold call should return the not-yet-verified sentinel {Valid:false, EntryCount:0, Error:\"\"}, got %+v", got1)
	}

	// The background verify populates the cache with the real verdict.
	warmDeadline := time.Now().Add(5 * time.Second)
	for {
		h.auditStatusCache.mu.Lock()
		st := h.auditStatusCache.status
		set := !h.auditStatusCache.cachedAt.IsZero()
		refreshing := h.auditStatusCache.refreshing
		h.auditStatusCache.mu.Unlock()
		if !refreshing && set && st.EntryCount == 2 && st.Valid {
			break
		}
		if time.Now().After(warmDeadline) {
			t.Fatalf("cold background verify did not populate cache within 5s (count=%d valid=%v set=%v refreshing=%v)",
				st.EntryCount, st.Valid, set, refreshing)
		}
		time.Sleep(10 * time.Millisecond)
	}
	firstCachedAt := h.auditStatusCache.cachedAt

	// Warm cache hit inside the TTL: returns the populated value; cachedAt
	// MUST NOT change (if it advances, a verify ran again).
	warm := h.cachedAuditStatus(ctx)
	if warm.EntryCount != 2 || !warm.Valid {
		t.Errorf("warm status = %+v, want EntryCount=2 Valid=true", warm)
	}
	if !h.auditStatusCache.cachedAt.Equal(firstCachedAt) {
		t.Errorf("cachedAt moved on cache-hit call: first=%v now=%v",
			firstCachedAt, h.auditStatusCache.cachedAt)
	}

	// Add a third audit entry — the cache should NOT see it inside the
	// TTL (the whole point of caching is that stale-but-recent is OK).
	insertAuditEntry(t, sqlDB, "", "config.reload", "carol", "third", now)
	got3 := h.cachedAuditStatus(ctx)
	if got3.EntryCount != 2 {
		t.Errorf("after new entry, cached EntryCount = %d, want 2 (stale by design)", got3.EntryCount)
	}

	// Force-expire the cache by rewinding cachedAt past the TTL.
	h.auditStatusCache.mu.Lock()
	h.auditStatusCache.cachedAt = time.Now().Add(-auditStatusCacheTTL - time.Second)
	h.auditStatusCache.mu.Unlock()

	// Expired access serves the STALE value immediately (still count 2) and
	// kicks a single background re-verify — it must not block on Verify.
	got4 := h.cachedAuditStatus(ctx)
	if got4.EntryCount != 2 {
		t.Errorf("expired access should serve stale EntryCount=2 immediately, got %d", got4.EntryCount)
	}

	// The background re-verify advances cachedAt and picks up the 3rd row.
	// (Its prev_hash is a placeholder, so Valid may be false — the count
	// still reflects the row.) Poll until the refresh lands.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.auditStatusCache.mu.Lock()
		count := h.auditStatusCache.status.EntryCount
		advanced := h.auditStatusCache.cachedAt.After(firstCachedAt)
		refreshing := h.auditStatusCache.refreshing
		h.auditStatusCache.mu.Unlock()
		if !refreshing && advanced && count == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background re-verify did not refresh within 5s (EntryCount=%d advanced=%v refreshing=%v)",
				count, advanced, refreshing)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
