// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigogit

import (
	"encoding/json"
	"testing"

	"github.com/daniel-alexander4/vigo/server/fleet"
)

// fakeFleet implements [FleetQuerier] with a caller-supplied slice
// so tests can control the candidate pool precisely.
type fakeFleet struct {
	envoys []fleet.EnvoyState
}

func (f *fakeFleet) AllEnvoys() []fleet.EnvoyState { return f.envoys }

func envoy(id, hostname string, humanUsers ...string) fleet.EnvoyState {
	traits := make(map[string]string)
	for i, u := range humanUsers {
		traits[flattenUserKey(i)] = u
	}
	return fleet.EnvoyState{
		ID:       id,
		Hostname: hostname,
		Traits:   traits,
	}
}

func flattenUserKey(i int) string {
	return "users.human." + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [10]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// --- ExtractSourcesFromTraits -------------------------------------

func TestExtractSourcesFromTraitsEmptyWhenMissing(t *testing.T) {
	sources, skipped := ExtractSourcesFromTraits(map[string]any{})
	if len(sources) != 0 || skipped != 0 {
		t.Errorf("expected empty result, got %d sources, %d skipped", len(sources), skipped)
	}
}

func TestExtractSourcesFromTraitsEmptyWhenNoSourcesKey(t *testing.T) {
	sources, _ := ExtractSourcesFromTraits(map[string]any{
		"vigogit": map[string]any{"active_sources": 0},
	})
	if len(sources) != 0 {
		t.Errorf("expected empty, got %v", sources)
	}
}

func TestExtractSourcesFromTraitsParsesFullEntry(t *testing.T) {
	payload := []byte(`{
		"vigogit": {
			"sources": [
				{
					"label": "vigogit:host:dan:proj",
					"paused": false,
					"max_mirrors": 5,
					"tags": ["personal", "notes"],
					"description": "my repo"
				}
			]
		}
	}`)
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatal(err)
	}

	sources, skipped := ExtractSourcesFromTraits(parsed)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(sources))
	}
	s := sources[0]
	if s.Label != "vigogit:host:dan:proj" || s.MaxMirrors != 5 ||
		len(s.Tags) != 2 || s.Tags[0] != "personal" ||
		s.Description != "my repo" || s.Paused {
		t.Errorf("unexpected: %+v", s)
	}
}

func TestExtractSourcesFromTraitsSkipsEntriesWithoutLabel(t *testing.T) {
	payload := []byte(`{
		"vigogit": {
			"sources": [
				{"paused": false},
				{"label": "vigogit:host:dan:proj", "max_mirrors": 5}
			]
		}
	}`)
	var parsed map[string]any
	_ = json.Unmarshal(payload, &parsed)

	sources, skipped := ExtractSourcesFromTraits(parsed)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(sources) != 1 || sources[0].Label != "vigogit:host:dan:proj" {
		t.Errorf("sources = %v", sources)
	}
}

// --- envoyHasHumanUser ---------------------------------------------

func TestEnvoyHasHumanUserMatches(t *testing.T) {
	traits := map[string]string{
		"users.human.0":  "dan",
		"users.human.1":  "alice",
		"users.os.0":     "root",
		"hostname":       "box1",
	}
	if !envoyHasHumanUser(traits, "dan") {
		t.Error("expected dan to match")
	}
	if !envoyHasHumanUser(traits, "alice") {
		t.Error("expected alice to match")
	}
	if envoyHasHumanUser(traits, "root") {
		t.Error("root is an os user, should not match")
	}
	if envoyHasHumanUser(traits, "nobody") {
		t.Error("nobody should not match")
	}
}

func TestEnvoyHasHumanUserEmptyInputs(t *testing.T) {
	if envoyHasHumanUser(nil, "dan") {
		t.Error("nil traits should not match")
	}
	if envoyHasHumanUser(map[string]string{"users.human.0": "dan"}, "") {
		t.Error("empty user should not match")
	}
}

// --- clampMirrors --------------------------------------------------

func TestClampMirrorsLow(t *testing.T) {
	if got := clampMirrors(1); got != MinMirrors {
		t.Errorf("clampMirrors(1) = %d, want %d", got, MinMirrors)
	}
}

func TestClampMirrorsHigh(t *testing.T) {
	if got := clampMirrors(999); got != MaxMirrors {
		t.Errorf("clampMirrors(999) = %d, want %d", got, MaxMirrors)
	}
}

func TestClampMirrorsInRange(t *testing.T) {
	if got := clampMirrors(5); got != 5 {
		t.Errorf("clampMirrors(5) = %d, want 5", got)
	}
}

// --- Builder.buildCandidatePool -----------------------------------

func TestBuildCandidatePoolExcludesSeeder(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "dan"),
		envoy("e3", "box1", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)
	got, err := b.buildCandidatePool(nil, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	for _, c := range got {
		if c.Hostname == "danlap" {
			t.Errorf("seeder included in candidate pool: %v", c)
		}
	}
}

func TestBuildCandidatePoolExcludesRevokedAndOffline(t *testing.T) {
	e2 := envoy("e2", "homeserver", "dan")
	e2.Revoked = true
	e3 := envoy("e3", "box1", "dan")
	e3.Offline = true
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		e2,
		e3,
		envoy("e4", "laptop", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)
	got, err := b.buildCandidatePool(nil, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(got), got)
	}
	if got[0].Hostname != "laptop" {
		t.Errorf("expected laptop, got %v", got[0])
	}
}

func TestBuildCandidatePoolFiltersByUser(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "alice"),
		envoy("e3", "laptop", "dan", "bob"),
	}}
	b := NewBuilder(NewClaimTable(), fq)
	got, err := b.buildCandidatePool(nil, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	// e2 lacks "dan", e1 is the seeder → only e3 remains.
	if len(got) != 1 || got[0].Hostname != "laptop" {
		t.Errorf("got %+v", got)
	}
}

func TestBuildCandidatePoolIsCaseInsensitiveOnSeeder(t *testing.T) {
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "DANLAP", "dan"),
		envoy("e2", "homeserver", "dan"),
	}}
	b := NewBuilder(NewClaimTable(), fq)
	got, err := b.buildCandidatePool(nil, "danlap", "dan")
	if err != nil {
		t.Fatalf("buildCandidatePool: %v", err)
	}
	if len(got) != 1 || got[0].Hostname != "homeserver" {
		t.Errorf("case-insensitive seeder match failed: %+v", got)
	}
}

// --- Builder.OnTraitReport ----------------------------------------

func TestOnTraitReportUpsertsValidSource(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "dan"),
		envoy("e3", "laptop", "dan"),
	}}
	b := NewBuilder(ct, fq)

	sources := []SourceFromTrait{{
		Label:       "vigogit:danlap:dan:proj",
		Paused:      false,
		MaxMirrors:  3,
		Tags:        []string{"personal"},
		Description: "",
	}}
	result := b.OnTraitReport("e1", "danlap", sources)
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if len(result.Upserted) != 1 {
		t.Fatalf("upserted = %v", result.Upserted)
	}

	state := ct.Get("vigogit:danlap:dan:proj")
	if state == nil {
		t.Fatal("state not in table")
	}
	if len(state.Mirrors) != 2 {
		t.Errorf("mirrors = %d, want 2 (pool=2, max=3)", len(state.Mirrors))
	}
	if !state.UnderReplicated {
		t.Error("should be under-replicated (2 < 3)")
	}
}

func TestOnTraitReportRejectsSeederMismatch(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "dan"),
	}}
	b := NewBuilder(ct, fq)

	// Envoy is "danlap" but the label claims "homeserver" as seeder.
	sources := []SourceFromTrait{{
		Label:      "vigogit:homeserver:dan:proj",
		MaxMirrors: 3,
	}}
	result := b.OnTraitReport("e1", "danlap", sources)
	if len(result.Upserted) != 0 {
		t.Errorf("expected no upserts, got %v", result.Upserted)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if ct.Len() != 0 {
		t.Errorf("ClaimTable should be empty, has %d", ct.Len())
	}
}

func TestOnTraitReportRejectsInvalidLabel(t *testing.T) {
	ct := NewClaimTable()
	b := NewBuilder(ct, &fakeFleet{})
	sources := []SourceFromTrait{{Label: "not-a-vigogit-label", MaxMirrors: 3}}
	result := b.OnTraitReport("e1", "danlap", sources)
	if len(result.Errors) != 1 || len(result.Upserted) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestOnTraitReportRemovesVanishedLabels(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "dan"),
		envoy("e3", "laptop", "dan"),
		envoy("e4", "box4", "dan"),
	}}
	b := NewBuilder(ct, fq)

	// First report: two sources.
	first := []SourceFromTrait{
		{Label: "vigogit:danlap:dan:proj1", MaxMirrors: 3},
		{Label: "vigogit:danlap:dan:proj2", MaxMirrors: 3},
	}
	b.OnTraitReport("e1", "danlap", first)
	if ct.Len() != 2 {
		t.Fatalf("after first report: Len=%d, want 2", ct.Len())
	}

	// Second report: proj1 is gone, proj2 remains.
	second := []SourceFromTrait{
		{Label: "vigogit:danlap:dan:proj2", MaxMirrors: 3},
	}
	result := b.OnTraitReport("e1", "danlap", second)
	if len(result.Removed) != 1 || result.Removed[0] != "vigogit:danlap:dan:proj1" {
		t.Errorf("removed = %v", result.Removed)
	}
	if ct.Len() != 1 {
		t.Errorf("after second report: Len=%d, want 1", ct.Len())
	}
	if ct.Get("vigogit:danlap:dan:proj1") != nil {
		t.Error("proj1 should be gone")
	}
}

func TestOnTraitReportDoesNotRemoveOtherSeedersLabels(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "homeserver", "dan"),
		envoy("e3", "laptop", "dan"),
		envoy("e4", "box4", "dan"),
	}}
	b := NewBuilder(ct, fq)

	// danlap reports proj1.
	b.OnTraitReport("e1", "danlap",
		[]SourceFromTrait{{Label: "vigogit:danlap:dan:proj1", MaxMirrors: 3}})
	// homeserver reports projX.
	b.OnTraitReport("e2", "homeserver",
		[]SourceFromTrait{{Label: "vigogit:homeserver:dan:projX", MaxMirrors: 3}})

	if ct.Len() != 2 {
		t.Fatalf("Len = %d, want 2", ct.Len())
	}

	// danlap reports empty — must remove only its own label.
	result := b.OnTraitReport("e1", "danlap", nil)
	if len(result.Removed) != 1 {
		t.Errorf("removed = %v", result.Removed)
	}
	if ct.Len() != 1 {
		t.Errorf("Len = %d, want 1 (homeserver's label should survive)", ct.Len())
	}
	if ct.Get("vigogit:homeserver:dan:projX") == nil {
		t.Error("homeserver's label was incorrectly removed")
	}
}

func TestOnTraitReportEmptyHostnameRejectsUpserts(t *testing.T) {
	ct := NewClaimTable()
	b := NewBuilder(ct, &fakeFleet{})
	sources := []SourceFromTrait{{Label: "vigogit:danlap:dan:proj", MaxMirrors: 3}}
	result := b.OnTraitReport("e1", "", sources)
	if len(result.Upserted) != 0 {
		t.Errorf("expected no upserts, got %v", result.Upserted)
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(result.Errors))
	}
}

func TestOnTraitReportIsCaseInsensitiveOnHostname(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "DANLAP", "dan"),
		envoy("e2", "homeserver", "dan"),
		envoy("e3", "laptop", "dan"),
	}}
	b := NewBuilder(ct, fq)

	// Label contains lowercase "danlap"; envoy reports "DANLAP".
	// Both should resolve to the same hostname.
	sources := []SourceFromTrait{{Label: "vigogit:danlap:dan:proj", MaxMirrors: 3}}
	result := b.OnTraitReport("e1", "DANLAP", sources)
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if ct.Get("vigogit:danlap:dan:proj") == nil {
		t.Error("upsert failed")
	}
}

func TestOnTraitReportClampsMaxMirrors(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "h1", "dan"),
		envoy("e3", "h2", "dan"),
		envoy("e4", "h3", "dan"),
		envoy("e5", "h4", "dan"),
		envoy("e6", "h5", "dan"),
		envoy("e7", "h6", "dan"),
		envoy("e8", "h7", "dan"),
		envoy("e9", "h8", "dan"),
		envoy("e10", "h9", "dan"),
		envoy("e11", "h10", "dan"),
		envoy("e12", "h11", "dan"),
	}}
	b := NewBuilder(ct, fq)
	sources := []SourceFromTrait{{Label: "vigogit:danlap:dan:proj", MaxMirrors: 9999}}
	b.OnTraitReport("e1", "danlap", sources)

	state := ct.Get("vigogit:danlap:dan:proj")
	if state == nil {
		t.Fatal("no state")
	}
	if len(state.Mirrors) != MaxMirrors {
		t.Errorf("mirrors = %d, want %d (clamp)", len(state.Mirrors), MaxMirrors)
	}
}

// --- Builder.Rebuild ------------------------------------------------

func TestRebuildReappliesSelectionAgainstCurrentFleet(t *testing.T) {
	ct := NewClaimTable()
	fq := &fakeFleet{envoys: []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "h1", "dan"),
		envoy("e3", "h2", "dan"),
		envoy("e4", "h3", "dan"),
	}}
	b := NewBuilder(ct, fq)

	sources := []SourceFromTrait{{Label: "vigogit:danlap:dan:proj", MaxMirrors: 3}}
	b.OnTraitReport("e1", "danlap", sources)
	firstCount := len(ct.Get("vigogit:danlap:dan:proj").Mirrors)
	if firstCount != 3 {
		t.Fatalf("first upsert mirrors=%d, want 3", firstCount)
	}

	// Drop two envoys from the fleet — Rebuild should shrink the
	// selection and flip UnderReplicated.
	fq.envoys = []fleet.EnvoyState{
		envoy("e1", "danlap", "dan"),
		envoy("e2", "h1", "dan"),
	}
	b.Rebuild()

	state := ct.Get("vigogit:danlap:dan:proj")
	if len(state.Mirrors) != 1 {
		t.Errorf("mirrors = %d, want 1", len(state.Mirrors))
	}
	if !state.UnderReplicated {
		t.Error("expected under-replicated after fleet shrink")
	}
}
