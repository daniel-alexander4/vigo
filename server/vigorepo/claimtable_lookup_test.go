// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigorepo

import (
	"testing"

	"github.com/daniel-alexander4/vigo/server/fleet"
)

// TestClaimTable_MirrorIDsForLabel_UnknownLabel covers the
// empty-table / missing-label path. Required to prove the lookup
// is nil-safe for the swarm status handler's fall-through branch.
func TestClaimTable_MirrorIDsForLabel_UnknownLabel(t *testing.T) {
	ct := NewClaimTable()
	if got := ct.MirrorIDsForLabel("vigorepo:host:user:proj"); got != nil {
		t.Errorf("expected nil for unknown label, got %+v", got)
	}
}

// TestClaimTable_MirrorIDsForLabel_ReturnsCopy verifies that
// callers can't mutate the underlying ClaimTable Mirrors slice by
// writing through the returned value. Critical for concurrent safety
// in the swarm status handlers, which may read this slice while the
// Builder is upserting new state.
func TestClaimTable_MirrorIDsForLabel_ReturnsCopy(t *testing.T) {
	ct := NewClaimTable()
	fq := &mirrorLookupFakeFleet{envoys: []fleet.EnvoyState{
		{ID: "a", Hostname: "seeder", Traits: map[string]string{"users.human.0": "dan"}},
		{ID: "b", Hostname: "m1", Traits: map[string]string{"users.human.0": "dan"}},
		{ID: "c", Hostname: "m2", Traits: map[string]string{"users.human.0": "dan"}},
	}}
	b := NewBuilder(ct, fq)
	b.OnTraitReport("a", "seeder", []SourceFromTrait{{
		Label:      "vigorepo:seeder:dan:proj",
		MaxMirrors: 5,
	}})

	first := ct.MirrorIDsForLabel("vigorepo:seeder:dan:proj")
	if len(first) == 0 {
		t.Fatalf("expected non-empty mirror list, got %+v", first)
	}

	// Mutate the returned slice. The internal state must remain untouched.
	first[0] = "mutated"

	second := ct.MirrorIDsForLabel("vigorepo:seeder:dan:proj")
	if second[0] == "mutated" {
		t.Errorf("MirrorIDsForLabel returned an aliased slice — mutation leaked back into the ClaimTable")
	}
}

// TestClaimTable_MirrorIDsForLabel_HappyPath uses the real Builder
// pipeline end-to-end: upsert a source, query by label, get back the
// selected mirrors. This is what the swarm status handler actually
// calls.
func TestClaimTable_MirrorIDsForLabel_HappyPath(t *testing.T) {
	ct := NewClaimTable()
	fq := &mirrorLookupFakeFleet{envoys: []fleet.EnvoyState{
		{ID: "seeder-id", Hostname: "seeder", Traits: map[string]string{"users.human.0": "dan"}},
		{ID: "mirror-a", Hostname: "mirror-a", Traits: map[string]string{"users.human.0": "dan"}},
		{ID: "mirror-b", Hostname: "mirror-b", Traits: map[string]string{"users.human.0": "dan"}},
		{ID: "rasp-id", Hostname: "raspberrypi", Traits: map[string]string{"users.human.0": "dan"}},
	}}
	b := NewBuilder(ct, fq)
	b.OnTraitReport("seeder-id", "seeder", []SourceFromTrait{{
		Label:         "vigorepo:seeder:dan:proj",
		MaxMirrors:    5,
		ExcludeEnvoys: []string{"raspberry*"},
	}})

	mirrors := ct.MirrorIDsForLabel("vigorepo:seeder:dan:proj")
	if len(mirrors) != 2 {
		t.Fatalf("expected 2 mirrors (mirror-a, mirror-b), got %d: %+v", len(mirrors), mirrors)
	}
	set := make(map[string]bool, len(mirrors))
	for _, id := range mirrors {
		set[id] = true
	}
	if !set["mirror-a"] || !set["mirror-b"] {
		t.Errorf("expected mirror-a and mirror-b in the list, got %+v", mirrors)
	}
	for _, id := range []string{"seeder-id", "rasp-id"} {
		if set[id] {
			t.Errorf("unexpected envoy %s in mirror list", id)
		}
	}
}

// mirrorLookupFakeFleet is the minimal FleetQuerier the Builder
// needs. Named distinctly from the fakeFleet in filter_test.go so
// both files can exist in the same package without redeclaration.
type mirrorLookupFakeFleet struct {
	envoys []fleet.EnvoyState
}

func (f *mirrorLookupFakeFleet) AllEnvoys() []fleet.EnvoyState { return f.envoys }
