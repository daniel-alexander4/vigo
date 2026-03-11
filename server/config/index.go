package config

import (
	"path/filepath"
	"strings"
)

// patternIndex accelerates hostname → entry lookup. It separates exact
// patterns (no wildcards) from glob patterns, allowing O(1) lookups for
// exact hostnames while maintaining first-match-wins ordering.
type patternIndex struct {
	exact map[string]int // hostname → entry index (for patterns without wildcards)
	globs []globEntry    // ordered glob patterns (patterns with *, ?, [)
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
		exact: make(map[string]int, len(entries)),
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

	// Find first matching glob. Globs are ordered by entry index.
	for _, g := range idx.globs {
		// If we have an exact match with a lower index, it wins.
		if exactIdx >= 0 && g.index > exactIdx {
			return exactIdx
		}
		if matched, _ := filepath.Match(g.pattern, hostname); matched {
			return g.index
		}
	}

	return exactIdx
}
