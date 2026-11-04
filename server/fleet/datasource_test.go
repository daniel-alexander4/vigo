// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestCertificatesFromTraits pins the nested-JSON cert extraction that
// EnvoyCertDetails now uses (Lever 1c: per-cert detail read from the DB JSON,
// not the pruned in-memory flattened map). Native JSON types: is_ca bool, sans
// []string, not_after.
func TestCertificatesFromTraits(t *testing.T) {
	raw := `{"cert_lifecycle":{"certificates":[
		{"path":"/etc/ssl/svc.pem","cn":"svc.example.com","issuer":"Example CA",
		 "not_after":"2027-01-01T00:00:00Z","is_ca":false,"sans":["a.example.com","b.example.com"]},
		{"cn":"ca","not_after":"2026-06-01T00:00:00Z","is_ca":true}
	]}}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	certs := certificatesFromTraits(parsed)
	if len(certs) != 2 {
		t.Fatalf("got %d certs, want 2", len(certs))
	}
	// Cert 0: full detail, native bool + []string.
	if got := jsonString(certs[0]["cn"]); got != "svc.example.com" {
		t.Errorf("cn = %q", got)
	}
	if jsonBool(certs[0]["is_ca"]) {
		t.Errorf("cert 0 is_ca should be false")
	}
	if got := jsonStringSlice(certs[0]["sans"]); !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com"}) {
		t.Errorf("sans = %v", got)
	}
	if got := jsonString(certs[0]["not_after"]); got != "2027-01-01T00:00:00Z" {
		t.Errorf("not_after = %q", got)
	}
	// Cert 1: is_ca true.
	if got := jsonString(certs[1]["not_after"]); got != "2026-06-01T00:00:00Z" {
		t.Errorf("cert 1 not_after = %q", got)
	}
	if !jsonBool(certs[1]["is_ca"]) {
		t.Errorf("cert 1 is_ca should be true")
	}

	// Absent / malformed → nil.
	if certificatesFromTraits(map[string]any{}) != nil {
		t.Errorf("missing cert_lifecycle should yield nil")
	}
}

// TestEnvoyDetailData_HeaderRowCarriesConvergence pins that the envoy
// detail page header row is built through the canonical envoyRow
// converter — it must carry the same convergence fields the envoys-page
// row does (ResourceCount / ChangedCount / FailedCount / Status / Drift /
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
	if e.ResourceCount != 10 || e.ChangedCount != 3 || e.FailedCount != 1 {
		t.Errorf("counts = %d/%d/%d, want 10/3/1 (module/changed/failed)",
			e.ResourceCount, e.ChangedCount, e.FailedCount)
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

// TestFleetTraitKeyCounts checks the trait-index data source: it counts each
// flattened key across non-revoked envoys and skips revoked ones. Packages are
// pruned upstream in FlattenForIndex (see TestFlattenForIndex) before traits
// ever reach EnvoyState.Traits, so they are already absent here — that is what
// keeps the trait-index modal bounded and fleet-size-stable.
func TestFleetTraitKeyCounts(t *testing.T) {
	fi := NewFleetIndex()
	pubKey := genKey(t)

	fi.AddEnvoy(EnvoyState{ID: "h1", Hostname: "host1", Traits: map[string]string{
		"os.distro": "ubuntu", "memory.total": "16G",
	}}, pubKey)
	fi.AddEnvoy(EnvoyState{ID: "h2", Hostname: "host2", Traits: map[string]string{
		"os.distro": "debian", "disk.0.size": "1T",
	}}, pubKey)
	// A revoked envoy must not contribute keys — mirrors the not-revoked
	// filter the old GetAllLatestTraits SQL applied.
	fi.AddEnvoy(EnvoyState{ID: "h3", Hostname: "host3", Revoked: true, Traits: map[string]string{
		"os.distro": "arch", "gpu.0.model": "nvidia",
	}}, pubKey)

	counts := NewDataSource(fi, nil, nil, nil).FleetTraitKeyCounts()

	if counts["os.distro"] != 2 {
		t.Errorf("os.distro count = %d, want 2 (revoked host3 excluded)", counts["os.distro"])
	}
	if counts["memory.total"] != 1 {
		t.Errorf("memory.total count = %d, want 1", counts["memory.total"])
	}
	if counts["disk.0.size"] != 1 {
		t.Errorf("disk.0.size count = %d, want 1", counts["disk.0.size"])
	}
	if _, ok := counts["gpu.0.model"]; ok {
		t.Error("revoked envoy's keys must not appear in the trait index")
	}
}
