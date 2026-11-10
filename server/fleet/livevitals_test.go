// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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

// TestSetLiveVitalsPowerLossEdge covers the one edge that turns into an
// operator-visible alert. The asymmetry matters in both directions: a count
// that rises is news, and a count that appears for the first time is not.
func TestSetLiveVitalsPowerLossEdge(t *testing.T) {
	const key = "host_self.unclean_shutdowns_30d"

	tests := []struct {
		name string
		old  map[string]string
		next map[string]string
		want bool
	}{
		{
			name: "count rose",
			old:  map[string]string{key: "1"},
			next: map[string]string{key: "2"},
			want: true,
		},
		{
			// The agent carries a 30-day history the server has never seen.
			// Replaying it on every vigosrv restart would page for events the
			// operator already handled.
			name: "first observation is never an edge",
			old:  nil,
			next: map[string]string{key: "3"},
			want: false,
		},
		{
			name: "unchanged count is not an edge",
			old:  map[string]string{key: "2"},
			next: map[string]string{key: "2"},
			want: false,
		},
		{
			// Events aging out of the window lower the count. Not news.
			name: "count fell",
			old:  map[string]string{key: "3"},
			next: map[string]string{key: "1"},
			want: false,
		},
		{
			// A detector that stops reporting must not manufacture an alert.
			name: "key disappeared",
			old:  map[string]string{key: "1"},
			next: map[string]string{"agent_self.open_fds": "10"},
			want: false,
		},
		{
			name: "unparseable new value is not an edge",
			old:  map[string]string{key: "1"},
			next: map[string]string{key: "not-a-number"},
			want: false,
		},
		{
			name: "unparseable old value is not an edge",
			old:  map[string]string{key: ""},
			next: map[string]string{key: "5"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fi := NewFleetIndex()
			fi.AddEnvoy(EnvoyState{ID: "e1", Hostname: "h1"}, nil)
			if tc.old != nil {
				fi.SetLiveVitals("e1", tc.old)
			}
			got := fi.SetLiveVitals("e1", tc.next)
			if got != tc.want {
				t.Errorf("power-loss edge = %v, want %v", got, tc.want)
			}
		})
	}
}
