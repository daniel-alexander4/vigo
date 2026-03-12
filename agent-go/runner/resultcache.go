// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package runner

import (
	"encoding/hex"
	"hash/fnv"
	"sort"
	"sync"

	"github.com/hobnob/hobnob/agent/modules"
)

// ResultCache caches no-op resource results to skip redundant executor calls.
// Only resources whose last execution was unchanged (no drift, no error) are
// cached. The cache entry expires after a configurable number of cycles or
// when the resource params change.
type ResultCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry // keyed by resource name
	maxAge  int                    // max cycles before re-check
}

type cacheEntry struct {
	paramsHash string
	result     modules.ResourceResult
	age        int // cycles since last real execution
}

// cacheableTypes are resource types safe to cache (check-only, no side effects).
// exec is excluded because it may have side effects. user and cron are excluded
// because external changes are common.
var cacheableTypes = map[string]bool{
	"file":       true,
	"package":    true,
	"service":    true,
	"repository": true,
	"archive":    true,
}

// NewResultCache creates a cache that expires entries after maxAge cycles.
func NewResultCache(maxAge int) *ResultCache {
	if maxAge < 1 {
		maxAge = 1
	}
	return &ResultCache{
		entries: make(map[string]*cacheEntry),
		maxAge:  maxAge,
	}
}

// Lookup returns a cached result if the resource params haven't changed and
// the cache entry hasn't expired. Returns (result, true) on hit.
func (c *ResultCache) Lookup(name, resType string, params map[string]string) (modules.ResourceResult, bool) {
	if !cacheableTypes[resType] {
		return modules.ResourceResult{}, false
	}

	hash := hashParams(params)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[name]
	if !ok || entry.paramsHash != hash || entry.age >= c.maxAge {
		return modules.ResourceResult{}, false
	}

	entry.age++
	return entry.result, true
}

// Store records a no-op result for future cache hits. Only unchanged,
// error-free results are cached.
func (c *ResultCache) Store(name, resType string, params map[string]string, result modules.ResourceResult) {
	if !cacheableTypes[resType] {
		return
	}
	// Only cache no-op results (no drift found, no error).
	if result.Changed || result.Error != nil {
		// Drift or error — evict any stale cache entry.
		c.mu.Lock()
		delete(c.entries, name)
		c.mu.Unlock()
		return
	}

	hash := hashParams(params)

	c.mu.Lock()
	c.entries[name] = &cacheEntry{
		paramsHash: hash,
		result:     result,
		age:        0,
	}
	c.mu.Unlock()
}

// hashParams produces a deterministic hash of resource params using FNV-1a,
// which is faster than SHA256 for non-cryptographic cache keys.
func hashParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := fnv.New128a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write([]byte(params[k]))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
