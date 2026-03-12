// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"path/filepath"
	"strings"
	"sync"
)

// patternIndex accelerates hostname → entry lookup. It separates exact
// patterns (no wildcards) from glob patterns, allowing O(1) lookups for
// exact hostnames while maintaining first-match-wins ordering. Glob match
// results are cached per hostname to avoid repeated O(N) scans.
type patternIndex struct {
	exact map[string]int // hostname → entry index (for patterns without wildcards)
	globs []globEntry    // ordered glob patterns (patterns with *, ?, [)

	// globCache caches hostname → entry index for glob lookups. Invalidated
	// on config reload by building a new patternIndex.
	globCacheMu sync.RWMutex
	globCache   map[string]int // hostname → entry index (or -1 for no match)
}

type globEntry struct {
	pattern string
	index   int // position in the original entries slice
}

// isGlobPattern returns true if the pattern contains glob metacharacters.
func isGlobPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// buildPatternIndex constructs an index from the resolved entries.
func buildPatternIndex(entries []resolvedEntry) patternIndex {
	idx := patternIndex{
		exact:     make(map[string]int, len(entries)),
		globCache: make(map[string]int),
	}
	for i, entry := range entries {
		if isGlobPattern(entry.match) {
			idx.globs = append(idx.globs, globEntry{
				pattern: entry.match,
				index:   i,
			})
		} else {
			// Only store the first occurrence (first match wins).
			if _, exists := idx.exact[entry.match]; !exists {
				idx.exact[entry.match] = i
			}
		}
	}
	return idx
}

// Lookup returns the index of the first matching entry for hostname,
// or -1 if no entry matches.
func (idx *patternIndex) Lookup(hostname string) int {
	exactIdx := -1
	if i, ok := idx.exact[hostname]; ok {
		exactIdx = i
	}

	if len(idx.globs) == 0 {
		return exactIdx
	}

	// Check glob cache first.
	idx.globCacheMu.RLock()
	cachedGlobIdx, cached := idx.globCache[hostname]
	idx.globCacheMu.RUnlock()

	if !cached {
		// Compute glob match and cache it.
		cachedGlobIdx = -1
		for _, g := range idx.globs {
			if matched, _ := filepath.Match(g.pattern, hostname); matched {
				cachedGlobIdx = g.index
				break
			}
		}
		idx.globCacheMu.Lock()
		idx.globCache[hostname] = cachedGlobIdx
		idx.globCacheMu.Unlock()
	}

	// Return whichever has the lower index (first-match-wins).
	switch {
	case exactIdx >= 0 && cachedGlobIdx >= 0:
		if exactIdx <= cachedGlobIdx {
			return exactIdx
		}
		return cachedGlobIdx
	case exactIdx >= 0:
		return exactIdx
	default:
		return cachedGlobIdx
	}
}
