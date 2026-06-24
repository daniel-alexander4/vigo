// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import "testing"

// TestLiveVitalsOverlay covers the ADR-033 Option C companion: SetLiveVitals
// records the churny health fields per tick, and healthTraits overlays them on
// the rarely-re-persisted Traits map — so fleetpressure reads the live value for
// a churny field while inventory-backed fields (only in Traits) still survive,
// the persisted map is left untouched, and a nil overlay is a no-op.
func TestLiveVitalsOverlay(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:       "e1",
		Hostname: "h1",
		Traits: map[string]string{
			"agent_self.open_fds":   "10", // stale persisted churny value (overlaid below)
			"package_updates.count": "5",  // inventory field — only in Traits, must survive
		},
	}, nil)

	// Before any live vitals: healthTraits returns Traits unchanged.
	if got := fi.AllEnvoys()[0].healthTraits()["agent_self.open_fds"]; got != "10" {
		t.Errorf("pre-overlay open_fds = %q, want 10", got)
	}

	// A fresh live value for the churny field overlays the stale persisted one.
	fi.SetLiveVitals("e1", map[string]string{"agent_self.open_fds": "999"})
	es := fi.AllEnvoys()[0]
	ht := es.healthTraits()
	if ht["agent_self.open_fds"] != "999" {
		t.Errorf("overlaid open_fds = %q, want 999 (live)", ht["agent_self.open_fds"])
	}
	if ht["package_updates.count"] != "5" {
		t.Errorf("package_updates.count = %q, want 5 (inventory field from Traits)", ht["package_updates.count"])
	}
	if es.Traits["agent_self.open_fds"] != "10" {
		t.Errorf("persisted Traits must be untouched by the overlay, got %q", es.Traits["agent_self.open_fds"])
	}

	// A nil/empty overlay is a no-op — keeps the last known live value.
	fi.SetLiveVitals("e1", nil)
	if got := fi.AllEnvoys()[0].healthTraits()["agent_self.open_fds"]; got != "999" {
		t.Errorf("nil overlay should keep prior live value, got %q", got)
	}
}
