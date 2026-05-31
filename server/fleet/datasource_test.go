// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"testing"
	"time"
)

// TestEnvoyDetailData_HeaderRowCarriesConvergence pins that the envoy
// detail page header row is built through the canonical envoyRow
// converter — it must carry the same convergence fields the envoys-page
// row does (ConfigcrateCount / ChangedCount / FailedCount / Status / Drift /
// LastRunID), not the half-populated subset EnvoyDetailData used to
// hand-build, which left the header pill permanently in the "no run
// data" branch.
func TestEnvoyDetailData_HeaderRowCarriesConvergence(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1", Hostname: "host1"}, genKey(t))
	// 1 failed module of 10 → degraded; 3 changed → drift "changed".
	fi.UpdateRunStatus("e1", "run-1", "partial", 3, 1, 10, time.Now())

	ds := NewDataSource(fi, nil, nil, nil)
	data, err := ds.EnvoyDetailData("e1", time.Minute, 30*time.Minute)
	if err != nil {
		t.Fatalf("EnvoyDetailData: %v", err)
	}

	e := data.Envoy
	if e.ConfigcrateCount != 10 || e.ChangedCount != 3 || e.FailedCount != 1 {
		t.Errorf("counts = %d/%d/%d, want 10/3/1 (module/changed/failed)",
			e.ConfigcrateCount, e.ChangedCount, e.FailedCount)
	}
	if e.Status != "degraded" {
		t.Errorf("Status = %q, want \"degraded\"", e.Status)
	}
	if e.DriftLabel != "changed" {
		t.Errorf("DriftLabel = %q, want \"changed\"", e.DriftLabel)
	}
	if e.LastRunID != "run-1" {
		t.Errorf("LastRunID = %q, want \"run-1\"", e.LastRunID)
	}
}

// TestEnvoyDetailData_HeroCardCarriesAgentUptime pins that the detail page
// surfaces the agent's process uptime (the `agent_self.uptime_secs` trait) on
// the page root so the hero card can render it, routed through the
// fleetpressure SoT. An envoy with no uptime trait yields Present=false (the
// hero segment hides).
func TestEnvoyDetailData_HeroCardCarriesAgentUptime(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{
		ID:       "e1",
		Hostname: "host1",
		Traits:   map[string]string{"agent_self.uptime_secs": "172800"},
	}, genKey(t))

	ds := NewDataSource(fi, nil, nil, nil)
	data, err := ds.EnvoyDetailData("e1", time.Minute, 30*time.Minute)
	if err != nil {
		t.Fatalf("EnvoyDetailData: %v", err)
	}
	if !data.UptimeSecs.Present || data.UptimeSecs.Value != 172800 {
		t.Errorf("UptimeSecs = %+v, want Present=true Value=172800", data.UptimeSecs)
	}

	fi.AddEnvoy(EnvoyState{ID: "e2", Hostname: "host2"}, genKey(t))
	noTraits, err := ds.EnvoyDetailData("e2", time.Minute, 30*time.Minute)
	if err != nil {
		t.Fatalf("EnvoyDetailData(e2): %v", err)
	}
	if noTraits.UptimeSecs.Present {
		t.Errorf("UptimeSecs.Present = true for envoy with no uptime trait; want false")
	}
}

// TestEnvoyDriftLabel_LookupAndFallback pins that the dataSource exposes
// the envoy's current drift label as a 1-call helper for the dashboard
// recent-runs widget (which can't reach FleetIndex directly), and that
// it falls back to "" when the envoy isn't in the index.
func TestEnvoyDriftLabel_LookupAndFallback(t *testing.T) {
	fi := NewFleetIndex()
	fi.AddEnvoy(EnvoyState{ID: "e1", Hostname: "host1"}, genKey(t))
	// 5 consecutive runs with changes → DriftDiverged.
	now := time.Now()
	for i := 0; i < 5; i++ {
		fi.UpdateRunStatus("e1", "r", "success", 1, 0, 10, now.Add(time.Duration(i)*time.Minute))
	}

	ds := NewDataSource(fi, nil, nil, nil)
	if got := ds.EnvoyDriftLabel("e1"); got != "diverged" {
		t.Errorf("EnvoyDriftLabel(\"e1\") = %q, want \"diverged\"", got)
	}
	if got := ds.EnvoyDriftLabel("unknown-id"); got != "" {
		t.Errorf("EnvoyDriftLabel(unknown) = %q, want \"\" (fallback)", got)
	}
}
