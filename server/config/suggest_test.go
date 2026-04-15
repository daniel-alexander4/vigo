// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"strings"
	"testing"
)

func TestSuggestion_VgoSuffixTrap(t *testing.T) {
	candidates := []string{"base-firewall", "git-gt-2.35", "docker"}
	got := suggestion("git-gt-2.35.vgo", candidates)
	if !strings.Contains(got, `"git-gt-2.35"`) {
		t.Errorf("expected suggestion to point at stripped name, got %q", got)
	}
	if !strings.Contains(got, "name:") {
		t.Errorf("expected suggestion to mention name: field, got %q", got)
	}
}

func TestSuggestion_VgoSuffixOnlyIfStrippedMatches(t *testing.T) {
	candidates := []string{"docker", "grafana"}
	if got := suggestion("something-else.vgo", candidates); got != "" {
		t.Errorf("expected no suggestion when stripped name doesn't match, got %q", got)
	}
}

func TestSuggestion_FuzzyTypo(t *testing.T) {
	candidates := []string{"base-firewall", "grafana", "docker"}
	got := suggestion("base-firwall", candidates)
	if !strings.Contains(got, `"base-firewall"`) {
		t.Errorf("expected fuzzy match to suggest base-firewall, got %q", got)
	}
}

func TestSuggestion_NoMatchBeyondThreshold(t *testing.T) {
	candidates := []string{"base-firewall", "grafana"}
	if got := suggestion("totallyunrelated", candidates); got != "" {
		t.Errorf("expected no suggestion for distant name, got %q", got)
	}
}

func TestSuggestion_CaseInsensitiveFuzzy(t *testing.T) {
	candidates := []string{"base-firewall"}
	got := suggestion("Base-Firewall", candidates)
	if !strings.Contains(got, `"base-firewall"`) {
		t.Errorf("expected case-insensitive match, got %q", got)
	}
}

func TestSuggestion_EmptyCandidates(t *testing.T) {
	if got := suggestion("anything", nil); got != "" {
		t.Errorf("expected no suggestion with empty candidate pool, got %q", got)
	}
}

func TestNotFoundHint_UsesSuggestionWhenAvailable(t *testing.T) {
	hint := notFoundHint("git-gt-2.35.vgo", []string{"git-gt-2.35"}, "add a module definition to modules/")
	if strings.Contains(hint, "add a module definition") {
		t.Errorf("expected suggestion to replace fallback, got %q", hint)
	}
	if !strings.Contains(hint, `"git-gt-2.35"`) {
		t.Errorf("expected suggestion in hint, got %q", hint)
	}
}

func TestNotFoundHint_FallbackWhenNoSuggestion(t *testing.T) {
	hint := notFoundHint("totally-unknown", []string{"docker", "grafana"}, "add a module definition to modules/")
	if hint != " — add a module definition to modules/" {
		t.Errorf("unexpected fallback hint: %q", hint)
	}
}

func TestNotFoundHint_NoFallbackNoSuggestion(t *testing.T) {
	if got := notFoundHint("xxx", []string{"yyy"}, ""); got != "" {
		t.Errorf("expected empty hint, got %q", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"base-firwall", "base-firewall", 1},
	}
	for _, tc := range cases {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
