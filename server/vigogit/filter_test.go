// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigogit

import (
	"testing"

	"github.com/daniel-alexander4/vigo/server/fleet"
)

// taggedEnvoy builds a minimal EnvoyState with hostname, a user trait,
// optional tags, and optional extra flattened traits — just enough to
// exercise the sidecar candidate filters.
func taggedEnvoy(id, hostname, user string, tags []string, extraTraits map[string]string) fleet.EnvoyState {
	traits := map[string]string{
		"users.human.0": user,
	}
	for k, v := range extraTraits {
		traits[k] = v
	}
	return fleet.EnvoyState{
		ID:       id,
		Hostname: hostname,
		Traits:   traits,
		Tags:     tags,
	}
}

// --- include_envoys ----------------------------------------------------

func TestBuildCandidatePool_IncludeEnvoys_KeepsOnlyMatches(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "laptop-work", "dan"),
		envoy("e3", "laptop-home", "dan"),
		envoy("e4", "office01.example.com", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{
		Owner:         "danlap",
		IncludeEnvoys: []string{"laptop-*", "*.example.com"},
	}
	got, err := b.buildCandidatePool(sc, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 (laptop-work, laptop-home, office01.example.com), got %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.Hostname] = true
	}
	for _, h := range []string{"laptop-work", "laptop-home", "office01.example.com"} {
		if !seen[h] {
			t.Errorf("expected %q in pool, missing", h)
		}
	}
}

func TestBuildCandidatePool_IncludeEnvoys_EmptyListIsPassAll(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "laptop", "dan"),
		envoy("e3", "homeserver", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{Owner: "danlap", IncludeEnvoys: nil}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 2 {
		t.Errorf("empty include list should be pass-all, got %d: %+v", len(got), got)
	}
}

func TestBuildCandidatePool_IncludeEnvoys_IsCaseInsensitive(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "LAPTOP-WORK", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{Owner: "danlap", IncludeEnvoys: []string{"laptop-*"}}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 1 || got[0].Hostname != "LAPTOP-WORK" {
		t.Errorf("expected LAPTOP-WORK via case-insensitive glob, got %+v", got)
	}
}

// --- exclude_envoys ----------------------------------------------------

func TestBuildCandidatePool_ExcludeEnvoys_DropsMatches(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "laptop", "dan"),
		envoy("e3", "homeserver", "dan"),
		envoy("e4", "office-box", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{
		Owner:         "danlap",
		ExcludeEnvoys: []string{"home*", "office-*"},
	}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 1 || got[0].Hostname != "laptop" {
		t.Errorf("expected only laptop, got %+v", got)
	}
}

func TestBuildCandidatePool_ExcludeBeatsInclude(t *testing.T) {
	// include_envoys is a broad whitelist; exclude_envoys narrows it.
	// This is the documented semantic ordering: include first, then
	// exclude, so an operator can say "only laptops, but not homelap".
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "laptop-work", "dan"),
		envoy("e3", "laptop-home", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{
		Owner:         "danlap",
		IncludeEnvoys: []string{"laptop-*"},
		ExcludeEnvoys: []string{"laptop-home"},
	}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 1 || got[0].Hostname != "laptop-work" {
		t.Errorf("expected only laptop-work, got %+v", got)
	}
}

// --- when expression ---------------------------------------------------

func TestBuildCandidatePool_When_TagBuiltin(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		taggedEnvoy("e2", "laptop-work", "dan", []string{"work"}, nil),
		taggedEnvoy("e3", "laptop-home", "dan", []string{"personal"}, nil),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{Owner: "danlap", When: "tag('work')"}
	got, err := b.buildCandidatePool(sc, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 1 || got[0].Hostname != "laptop-work" {
		t.Errorf("tag('work') should match only the work laptop, got %+v", got)
	}
}

func TestBuildCandidatePool_When_TraitEqBuiltin(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		taggedEnvoy("e2", "office-box", "dan", nil, map[string]string{"location": "office"}),
		taggedEnvoy("e3", "home-box", "dan", nil, map[string]string{"location": "home"}),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{Owner: "danlap", When: "trait_eq('location', 'office')"}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 1 || got[0].Hostname != "office-box" {
		t.Errorf("trait_eq match failed, got %+v", got)
	}
}

func TestBuildCandidatePool_When_BooleanLogic(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		taggedEnvoy("e2", "a", "dan", []string{"work"}, map[string]string{"env": "prod"}),
		taggedEnvoy("e3", "b", "dan", []string{"work"}, map[string]string{"env": "dev"}),
		taggedEnvoy("e4", "c", "dan", []string{"personal"}, map[string]string{"env": "prod"}),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{
		Owner: "danlap",
		When:  "tag('work') and trait_eq('env', 'prod')",
	}
	got, _ := b.buildCandidatePool(sc, "danlap", "dan")
	if len(got) != 1 || got[0].Hostname != "a" {
		t.Errorf("compound when expression failed, got %+v", got)
	}
}

func TestBuildCandidatePool_When_ParseErrorSurfacesEarly(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "laptop", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{Owner: "danlap", When: "tag('work'"} // unterminated
	_, err := b.buildCandidatePool(sc, "danlap", "dan")
	if err == nil {
		t.Error("expected parse error for malformed when expression")
	}
}

// --- filter ordering + interaction ------------------------------------

func TestBuildCandidatePool_AllFiltersComposed(t *testing.T) {
	// Operator wants: "only laptops, but not the home one, and only
	// when tagged work". This is the canonical everything-at-once
	// pathway the three sidecar fields were designed to support.
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		taggedEnvoy("e2", "laptop-work", "dan", []string{"work"}, nil),
		taggedEnvoy("e3", "laptop-home", "dan", []string{"work"}, nil),     // excluded by exclude_envoys
		taggedEnvoy("e4", "laptop-spare", "dan", []string{"personal"}, nil), // excluded by when
		envoy("e5", "desktop", "dan"),                                      // excluded by include_envoys
	}}
	b := NewBuilder(NewClaimTable(), fq)

	sc := &Sidecar{
		Owner:         "danlap",
		IncludeEnvoys: []string{"laptop-*"},
		ExcludeEnvoys: []string{"laptop-home"},
		When:          "tag('work')",
	}
	got, err := b.buildCandidatePool(sc, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 1 || got[0].Hostname != "laptop-work" {
		t.Errorf("composed filters should leave only laptop-work, got %+v", got)
	}
}

// --- ExtractSourcesFromTraits decodes new fields ----------------------

func TestExtractSourcesFromTraits_DecodesFilterFields(t *testing.T) {
	raw := map[string]any{
		"vigogit": map[string]any{
			"sources": []any{
				map[string]any{
					"label":          "vigogit:danlap:dan:proj",
					"paused":         false,
					"max_mirrors":    float64(5),
					"tags":           []any{"notes"},
					"description":    "test",
					"include_envoys": []any{"laptop-*"},
					"exclude_envoys": []any{"homeserver"},
					"when":           "tag('work')",
				},
			},
		},
	}
	sources, skipped := ExtractSourcesFromTraits(raw)
	if skipped != 0 || len(sources) != 1 {
		t.Fatalf("expected 1 source, 0 skipped — got %d sources, %d skipped", len(sources), skipped)
	}
	s := sources[0]
	if len(s.IncludeEnvoys) != 1 || s.IncludeEnvoys[0] != "laptop-*" {
		t.Errorf("include_envoys = %+v", s.IncludeEnvoys)
	}
	if len(s.ExcludeEnvoys) != 1 || s.ExcludeEnvoys[0] != "homeserver" {
		t.Errorf("exclude_envoys = %+v", s.ExcludeEnvoys)
	}
	if s.When != "tag('work')" {
		t.Errorf("when = %q", s.When)
	}
}

// --- LoadSidecar validation for new fields ----------------------------

func TestLoadSidecar_AcceptsMissingFilterFieldsForBackCompat(t *testing.T) {
	yaml := []byte("owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\n")
	sc, _, err := LoadSidecar(yaml)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if sc.IncludeEnvoys != nil || sc.ExcludeEnvoys != nil {
		t.Errorf("expected nil filter slices when absent, got include=%v exclude=%v", sc.IncludeEnvoys, sc.ExcludeEnvoys)
	}
	if sc.When != "" {
		t.Errorf("expected empty When, got %q", sc.When)
	}
}

func TestLoadSidecar_ParsesFilterFields(t *testing.T) {
	yaml := []byte(`owner: danlap
max_mirrors: 5
tags: []
paused: false
description: ""
include_envoys:
  - "laptop-*"
  - "*.office.example.com"
exclude_envoys:
  - homeserver
when: "tag('work')"
`)
	sc, _, err := LoadSidecar(yaml)
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	if len(sc.IncludeEnvoys) != 2 {
		t.Errorf("include_envoys = %+v", sc.IncludeEnvoys)
	}
	if len(sc.ExcludeEnvoys) != 1 || sc.ExcludeEnvoys[0] != "homeserver" {
		t.Errorf("exclude_envoys = %+v", sc.ExcludeEnvoys)
	}
	if sc.When != "tag('work')" {
		t.Errorf("when = %q", sc.When)
	}
}

func TestLoadSidecar_RejectsTooManyPatterns(t *testing.T) {
	var entries string
	for i := 0; i < MaxEnvoyPatterns+1; i++ {
		entries += "  - pat\n"
	}
	yaml := []byte("owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\nexclude_envoys:\n" + entries)
	_, _, err := LoadSidecar(yaml)
	if err == nil {
		t.Fatal("expected too_many_patterns error")
	}
	se, ok := err.(*SidecarError)
	if !ok || se.Kind != "too_many_patterns" {
		t.Errorf("wrong error kind: %+v", err)
	}
}

func TestLoadSidecar_RejectsEmptyPatternEntry(t *testing.T) {
	yaml := []byte("owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\ninclude_envoys: [\"\"]\n")
	_, _, err := LoadSidecar(yaml)
	if err == nil {
		t.Fatal("expected invalid_pattern error")
	}
	se, ok := err.(*SidecarError)
	if !ok || se.Kind != "invalid_pattern" {
		t.Errorf("wrong error kind: %+v", err)
	}
}

func TestLoadSidecar_RejectsOverlongWhenExpression(t *testing.T) {
	long := make([]byte, MaxWhenExprBytes+1)
	for i := range long {
		long[i] = 'x'
	}
	yaml := []byte("owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\nwhen: \"" + string(long) + "\"\n")
	_, _, err := LoadSidecar(yaml)
	if err == nil {
		t.Fatal("expected when_too_long error")
	}
	se, ok := err.(*SidecarError)
	if !ok || se.Kind != "when_too_long" {
		t.Errorf("wrong error kind: %+v", err)
	}
}
