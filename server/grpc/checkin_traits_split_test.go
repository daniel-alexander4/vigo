// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"encoding/json"
	"testing"

	"git.alexander4.io/vigo/server/fleet"
)

// TestMergeTraitsObjects covers the byte-level join of the disjoint inventory
// and vitals objects (ADR-033) — empties, one-sided, and the concat case — and
// asserts the result is always valid JSON carrying both key sets.
func TestMergeTraitsObjects(t *testing.T) {
	cases := []struct{ inv, vit, want string }{
		{`{}`, `{}`, `{}`},
		{``, ``, `{}`},
		{`{"a":1}`, `{}`, `{"a":1}`},
		{`{}`, `{"b":2}`, `{"b":2}`},
		{``, `{"b":2}`, `{"b":2}`},
		{`{"a":1}`, `{"b":2}`, `{"a":1,"b":2}`},
		{`{"a":{"x":1}}`, `{"b":[1,2]}`, `{"a":{"x":1},"b":[1,2]}`},
	}
	for _, c := range cases {
		got := string(mergeTraitsObjects([]byte(c.inv), []byte(c.vit)))
		if got != c.want {
			t.Errorf("merge(%q,%q) = %q, want %q", c.inv, c.vit, got, c.want)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Errorf("merge(%q,%q) produced invalid JSON %q: %v", c.inv, c.vit, got, err)
		}
	}
}

// TestReconstructTraits exercises the cache-and-reconstruct path: first contact
// (inventory present) caches and merges; a vitals-only check-in with a matching
// hash reuses the cached inventory with fresh vitals; a hash mismatch or unknown
// envoy returns resend with no traits.
func TestReconstructTraits(t *testing.T) {
	s := &AgentService{}
	inv := []byte(`{"packages":["a"]}`)
	vit := []byte(`{"loadavg":1}`)

	full, resend := s.reconstructTraits("e1", vit, inv, "h1")
	if resend {
		t.Fatal("inventory-present check-in should not request resend")
	}
	if !json.Valid(full) {
		t.Fatalf("reconstructed traits not valid JSON: %s", full)
	}

	// Vitals-only, matching hash → cached inventory + fresh vitals.
	full2, resend2 := s.reconstructTraits("e1", []byte(`{"loadavg":2}`), nil, "h1")
	if resend2 {
		t.Fatal("cache hit should not request resend")
	}
	var m map[string]any
	if err := json.Unmarshal(full2, &m); err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if _, ok := m["packages"]; !ok {
		t.Error("cached inventory missing from reconstruction")
	}
	if v, _ := m["loadavg"].(float64); v != 2 {
		t.Errorf("vitals not fresh in reconstruction: loadavg=%v", m["loadavg"])
	}

	// Hash mismatch → resend, no traits.
	full3, resend3 := s.reconstructTraits("e1", vit, nil, "h2")
	if !resend3 || full3 != nil {
		t.Errorf("hash mismatch: got (full=%v, resend=%v), want (nil, true)", full3, resend3)
	}

	// Unknown envoy, vitals-only → resend.
	if _, resend4 := s.reconstructTraits("e2", vit, nil, "h1"); !resend4 {
		t.Error("cache miss should request resend")
	}
}

// TestInventoryEvictedOnEnvoyRemoval verifies the lifecycle wiring (ADR-033 #1):
// the inventory cache is tied to fleet membership via FleetIndex.SetOnRemove, so
// removing an envoy drops its cached inventory and the next vitals-only check-in
// falls back to requesting a resend.
func TestInventoryEvictedOnEnvoyRemoval(t *testing.T) {
	s := &AgentService{}
	fi := fleet.NewFleetIndex()
	// Mirror the production wiring in grpc/server.go.
	fi.SetOnRemove(func(id string) { s.inventoryCache.Delete(id) })

	fi.AddEnvoy(fleet.EnvoyState{ID: "e1", Hostname: "h1", ConvergenceStatus: "converged"}, nil)

	// First contact caches the inventory; a vitals-only check-in then hits.
	s.reconstructTraits("e1", []byte(`{"loadavg":1}`), []byte(`{"packages":["a"]}`), "h1")
	if _, resend := s.reconstructTraits("e1", []byte(`{"loadavg":2}`), nil, "h1"); resend {
		t.Fatal("cache hit expected before removal")
	}

	// Removing the envoy evicts its inventory; the next vitals-only check-in misses.
	fi.RemoveEnvoy("e1")
	if _, resend := s.reconstructTraits("e1", []byte(`{"loadavg":3}`), nil, "h1"); !resend {
		t.Fatal("inventory should be evicted after envoy removal → resend expected")
	}
}
