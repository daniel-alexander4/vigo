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
//   - populate on the first call (cachedAt set, status reflects DB state)
//   - return the same value on subsequent calls inside the TTL window
//     WITHOUT re-running audit.Verify (proven by an unchanged cachedAt)
//   - refresh when the cache is expired
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

	// Next call refreshes: cachedAt advances + EntryCount reflects the new row.
	got4 := h.cachedAuditStatus(ctx)
	if got4.EntryCount != 3 {
		// The 3rd entry's prev_hash is wrong (we used got1.Error as a
		// placeholder), so chain may show Valid=false here — but the
		// count still reflects the row.
		t.Errorf("after TTL expiry, refreshed EntryCount = %d, want 3", got4.EntryCount)
	}
	if !h.auditStatusCache.cachedAt.After(firstCachedAt) {
		t.Errorf("cachedAt did not advance after TTL expiry: first=%v new=%v",
			firstCachedAt, h.auditStatusCache.cachedAt)
	}
}
