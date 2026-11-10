// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestCachedEnvoyRowsMemoizesWithinTTL pins the /envoys full-fleet row-build
// memo: repeated calls inside the TTL window reuse one build — the poll
// fragments (stats + table), multiple tabs, and scroll load-more share it
// instead of each rebuilding the whole fleet — and the cache rebuilds once
// the TTL lapses.
func TestCachedEnvoyRowsMemoizesWithinTTL(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1", Hostname: "host1"}, genKey(t))
	fi.AddEnvoy(EnvoyState{ID: "e2", Hostname: "host2"}, genKey(t))
	ds := NewDataSource(fi, nil, nil, nil)

	r1 := ds.cachedEnvoyRows(time.Minute, 30*time.Minute)
	built1 := ds.envoyRowsCache.builtAt
	if len(r1) != 2 {
		t.Fatalf("rows = %d, want 2", len(r1))
	}

	r2 := ds.cachedEnvoyRows(time.Minute, 30*time.Minute)
	if !ds.envoyRowsCache.builtAt.Equal(built1) {
		t.Fatal("rebuilt within TTL — memo not hit")
	}
	if &r1[0] != &r2[0] {
		t.Fatal("within TTL returned a different backing slice — not the memoized build")
	}

	// Lapse the TTL → next call must rebuild (a fresh build timestamp).
	ds.envoyRowsCache.builtAt = time.Now().Add(-envoyRowsCacheTTL - time.Second)
	_ = ds.cachedEnvoyRows(time.Minute, 30*time.Minute)
	if ds.envoyRowsCache.builtAt.Equal(built1) {
		t.Fatal("did not rebuild after TTL lapsed")
	}
}

// TestEnvoysAndDashboardShareCacheNoRace exercises the two surfaces that
// consume the shared read-only memo concurrently — EnvoysData (filters into a
// fresh slice via filterEnvoyRows, then sorts that) and DashboardData (copies
// before its in-place sortByWorstStatus). Run under `go test -race`, it
// catches any in-place mutation of the shared cached slice.
func TestEnvoysAndDashboardShareCacheNoRace(t *testing.T) {
	fi := NewFleetIndex()
	for i := 0; i < 50; i++ {
		s := strconv.Itoa(i)
		fi.AddEnvoy(EnvoyState{ID: "e" + s, Hostname: "host" + s}, genKey(t))
	}
	ds := NewDataSource(fi, nil, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = ds.EnvoysData(time.Minute, 30*time.Minute, "", "", "", "", "", "", "", "", "", "", "", "status", "asc", 0, 100)
		}()
		go func() {
			defer wg.Done()
			_ = ds.DashboardData(time.Minute, 30*time.Minute)
		}()
	}
	wg.Wait()
}
