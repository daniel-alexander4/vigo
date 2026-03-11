package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestPatternIndex_ExactMatch(t *testing.T) {
	entries := []resolvedEntry{
		{match: "web01.example.com"},
		{match: "web02.example.com"},
		{match: "db01.example.com"},
	}
	idx := buildPatternIndex(entries)

	tests := []struct {
		hostname string
		want     int
	}{
		{"web01.example.com", 0},
		{"web02.example.com", 1},
		{"db01.example.com", 2},
		{"unknown.example.com", -1},
	}

	for _, tt := range tests {
		if got := idx.Lookup(tt.hostname); got != tt.want {
			t.Errorf("Lookup(%q) = %d, want %d", tt.hostname, got, tt.want)
		}
	}
}

func TestPatternIndex_GlobMatch(t *testing.T) {
	entries := []resolvedEntry{
		{match: "*.web.example.com"},
		{match: "*.db.example.com"},
	}
	idx := buildPatternIndex(entries)

	tests := []struct {
		hostname string
		want     int
	}{
		{"app1.web.example.com", 0},
		{"app2.web.example.com", 0},
		{"primary.db.example.com", 1},
		{"unknown.example.com", -1},
	}

	for _, tt := range tests {
		if got := idx.Lookup(tt.hostname); got != tt.want {
			t.Errorf("Lookup(%q) = %d, want %d", tt.hostname, got, tt.want)
		}
	}
}

func TestPatternIndex_FirstMatchWins_GlobBeforeExact(t *testing.T) {
	// Glob at index 0 should beat exact at index 1.
	entries := []resolvedEntry{
		{match: "*.example.com"},
		{match: "web01.example.com"},
	}
	idx := buildPatternIndex(entries)

	if got := idx.Lookup("web01.example.com"); got != 0 {
		t.Errorf("Lookup(web01.example.com) = %d, want 0 (glob first)", got)
	}
}

func TestPatternIndex_FirstMatchWins_ExactBeforeGlob(t *testing.T) {
	// Exact at index 0 should beat glob at index 1.
	entries := []resolvedEntry{
		{match: "web01.example.com"},
		{match: "*.example.com"},
	}
	idx := buildPatternIndex(entries)

	if got := idx.Lookup("web01.example.com"); got != 0 {
		t.Errorf("Lookup(web01.example.com) = %d, want 0 (exact first)", got)
	}
	// Non-exact should still match the glob.
	if got := idx.Lookup("web02.example.com"); got != 1 {
		t.Errorf("Lookup(web02.example.com) = %d, want 1 (glob)", got)
	}
}

func TestPatternIndex_MixedOrderingRespected(t *testing.T) {
	entries := []resolvedEntry{
		{match: "*.staging.example.com"}, // 0 - glob
		{match: "web01.example.com"},     // 1 - exact
		{match: "*.example.com"},         // 2 - glob
		{match: "db01.example.com"},      // 3 - exact
	}
	idx := buildPatternIndex(entries)

	tests := []struct {
		hostname string
		want     int
	}{
		// Matches glob at 0.
		{"app.staging.example.com", 0},
		// Matches exact at 1 (before glob at 2).
		{"web01.example.com", 1},
		// Matches glob at 2.
		{"web02.example.com", 2},
		// Matches exact at 3 (not glob at 2 since 2 < 3... wait, glob at 2 IS lower).
		{"db01.example.com", 2},
	}

	for _, tt := range tests {
		if got := idx.Lookup(tt.hostname); got != tt.want {
			t.Errorf("Lookup(%q) = %d, want %d", tt.hostname, got, tt.want)
		}
	}
}

func TestPatternIndex_NoEntries(t *testing.T) {
	idx := buildPatternIndex(nil)
	if got := idx.Lookup("anything"); got != -1 {
		t.Errorf("Lookup on empty index = %d, want -1", got)
	}
}

func TestPatternIndex_QuestionMarkGlob(t *testing.T) {
	entries := []resolvedEntry{
		{match: "web0?.example.com"},
	}
	idx := buildPatternIndex(entries)

	if got := idx.Lookup("web01.example.com"); got != 0 {
		t.Errorf("Lookup(web01.example.com) = %d, want 0", got)
	}
	if got := idx.Lookup("web001.example.com"); got != -1 {
		t.Errorf("Lookup(web001.example.com) = %d, want -1 (? matches single char)", got)
	}
}

func TestPatternIndex_DuplicateExact_FirstWins(t *testing.T) {
	entries := []resolvedEntry{
		{match: "web01.example.com"},
		{match: "web01.example.com"}, // duplicate
	}
	idx := buildPatternIndex(entries)

	if got := idx.Lookup("web01.example.com"); got != 0 {
		t.Errorf("Lookup = %d, want 0 (first occurrence)", got)
	}
}

func BenchmarkPatternIndex_ExactLookup(b *testing.B) {
	// 5000 exact patterns.
	entries := make([]resolvedEntry, 5000)
	for i := range entries {
		entries[i] = resolvedEntry{match: fmt.Sprintf("thrall%04d.example.com", i)}
	}
	idx := buildPatternIndex(entries)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Lookup("node2500.example.com")
	}
}

func BenchmarkPatternIndex_GlobLookup(b *testing.B) {
	// 100 glob patterns + 4900 exact patterns.
	entries := make([]resolvedEntry, 5000)
	for i := 0; i < 100; i++ {
		entries[i] = resolvedEntry{match: fmt.Sprintf("*.group%02d.example.com", i)}
	}
	for i := 100; i < 5000; i++ {
		entries[i] = resolvedEntry{match: fmt.Sprintf("thrall%04d.example.com", i)}
	}
	idx := buildPatternIndex(entries)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Lookup("app1.group50.example.com")
	}
}

func BenchmarkLinearMatch(b *testing.B) {
	// Baseline: linear filepath.Match scan of 5000 entries.
	entries := make([]resolvedEntry, 5000)
	for i := range entries {
		entries[i] = resolvedEntry{match: fmt.Sprintf("thrall%04d.example.com", i)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, e := range entries {
			if matched, _ := filepath.Match(e.match, "node2500.example.com"); matched {
				break
			}
		}
	}
}
