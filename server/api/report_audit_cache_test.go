// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

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

	// First call: cold cache → DB hit + Verify, cachedAt set, status populated.
	got1 := h.cachedAuditStatus(ctx)
	if got1.EntryCount != 2 {
		t.Errorf("first call EntryCount = %d, want 2", got1.EntryCount)
	}
	if !got1.Valid {
		t.Errorf("first call Valid = false, want true (chain seeded correctly)")
	}
	firstCachedAt := h.auditStatusCache.cachedAt
	if firstCachedAt.IsZero() {
		t.Fatal("first call did not populate cachedAt")
	}

	// Second call inside the TTL: cache hit, cachedAt MUST NOT change.
	// If cachedAt advances, the cache failed and Verify ran again.
	got2 := h.cachedAuditStatus(ctx)
	if got2 != got1 {
		t.Errorf("cached value changed: first=%+v second=%+v", got1, got2)
	}
	if !h.auditStatusCache.cachedAt.Equal(firstCachedAt) {
		t.Errorf("cachedAt moved on cache-hit call: first=%v second=%v",
			firstCachedAt, h.auditStatusCache.cachedAt)
	}

	// Add a third audit entry — the cache should NOT see it inside the
	// TTL (the whole point of caching is that stale-but-recent is OK).
	insertAuditEntry(t, sqlDB, got1.Error, "config.reload", "carol", "third", now)
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
