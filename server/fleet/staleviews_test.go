// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package fleet

import (
	"testing"
	"time"
)

// TestStaleViews verifies the lightweight stale-checker accessor: it omits
// revoked and never-seen envoys, and carries LastSeen + the current Offline
// flag so the stale-checker can act only on transitions (vs the old
// AllEnvoys() full-struct copy + per-envoy EnvoyCycle re-walk).
func TestStaleViews(t *testing.T) {
	fi := NewFleetIndex()
	now := time.Now()

	fi.AddEnvoy(EnvoyState{ID: "seen", Hostname: "h-seen", ConvergenceStatus: "no data"}, nil)
	fi.MarkSeen("seen", now)

	fi.AddEnvoy(EnvoyState{ID: "offline", Hostname: "h-off", ConvergenceStatus: "no data"}, nil)
	fi.MarkSeen("offline", now.Add(-time.Hour))
	fi.SetOffline("offline", true) // after MarkSeen, which clears Offline

	// Never seen (zero LastSeen) → excluded.
	fi.AddEnvoy(EnvoyState{ID: "neverseen", Hostname: "h-never", ConvergenceStatus: "no data"}, nil)
	// Revoked → excluded even though it has been seen.
	fi.AddEnvoy(EnvoyState{ID: "revoked", Hostname: "h-rev", Revoked: true, ConvergenceStatus: "no data"}, nil)
	fi.MarkSeen("revoked", now)

	got := make(map[string]StaleView)
	for _, v := range fi.StaleViews() {
		got[v.ID] = v
	}

	if _, ok := got["neverseen"]; ok {
		t.Error("StaleViews included a never-seen envoy")
	}
	if _, ok := got["revoked"]; ok {
		t.Error("StaleViews included a revoked envoy")
	}
	if len(got) != 2 {
		t.Fatalf("StaleViews returned %d views, want 2 (seen + offline)", len(got))
	}

	if v := got["seen"]; v.Hostname != "h-seen" || v.Offline || !v.LastSeen.Equal(now) {
		t.Errorf("seen view = %+v, want {hostname h-seen, offline false, lastSeen %v}", v, now)
	}
	if v := got["offline"]; v.Hostname != "h-off" || !v.Offline {
		t.Errorf("offline view = %+v, want {hostname h-off, offline true}", v)
	}
}
