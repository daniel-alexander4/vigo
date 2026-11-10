// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strconv"
	"testing"
)

// TestPatternIndexGlobCacheBounded: looking up more distinct hostnames than
// the cap never grows the cache past it, and lookups stay correct.
func TestPatternIndexGlobCacheBounded(t *testing.T) {
	idx := buildPatternIndex([]resolvedEntry{{match: "web-*"}})
	for i := 0; i < maxGlobCacheEntries+100; i++ {
		idx.Lookup("junk-" + strconv.Itoa(i))
	}
	idx.globCacheMu.RLock()
	n := len(idx.globCache)
	idx.globCacheMu.RUnlock()
	if n > maxGlobCacheEntries {
		t.Fatalf("globCache holds %d entries, cap is %d", n, maxGlobCacheEntries)
	}
	if got := idx.Lookup("web-01"); got != 0 {
		t.Fatalf("Lookup(web-01) = %d after cache reset, want 0", got)
	}
}
