// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// The route-to-owner mechanism (ADR-037 slice 5) ships a page's full data
// struct as JSON: the owning bolt marshals its ui.<Page>Data, the viewer
// unmarshals into the same type and renders. This test pins that the
// richest security struct — including the per-host tables and nested CVE
// entries — survives the round-trip byte-identical, which is the load-
// bearing assumption of the whole mechanism.
func TestSecurityPageDataJSONRoundTrip(t *testing.T) {
	want := SecurityPageData{
		ConvergencePct: 97.5,
		Total:          1996,
		Converged:      1990,
		Degraded:       3,
		Failed:         3,
		Offline:        2,
		Security: SecurityScanPageData{
			UniqueCVEs:     42,
			UniqueCritical: 5,
			UniqueHigh:     12,
			LynisAvgScore:  78,
			RootkitWarning: 1,
			ClamavInfected: 0,
			ScannedHosts:   1990,
			TotalHosts:     1996,
			CVEs: []CVEEntry{
				{CVEID: "CVE-2024-1234", Severity: "critical", Packages: 3},
				{CVEID: "CVE-2023-9999", Severity: "high", Packages: 1},
			},
			PerHost: []SecurityHostRow{
				{
					ID: "envoy-a", Hostname: "web-01.bolt2",
					UniqueCritical: 2, UniqueHigh: 4, LynisScore: 71,
					RootkitClean: false, RkhunterWarnings: 1,
					ClamavClean: true, AideClean: false, AideChanged: 2,
					CVEs:     []CVEEntry{{CVEID: "CVE-2024-1234", Severity: "critical", Packages: 3}},
					LastScan: "2026-06-25T12:00:00Z",
				},
			},
			AnyTruncated: true,
		},
		Certs: CertExpirySummary{
			ExpiringSoon: 7, Expired: 1, ExpiringIn7: 2, ExpiringIn30: 5, HostsAffected: 4,
			PerHost: []CertHostRow{{ID: "envoy-a", Hostname: "web-01.bolt2"}},
		},
		CVETrend:       []float64{10, 8, 6, 5},
		HardeningTrend: []float64{70, 72, 75, 78},
	}

	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SecurityPageData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round-trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}

// routeScopedPage must NOT route (renders locally) in standalone mode and
// for the non-peer scopes — fleet, the standalone local tab, and empty.
// (The peer-bolt routing path needs a live spannerAdmin + roster, exercised
// by the spanner integration paths.)
func TestRouteScopedPageStandaloneNeverRoutes(t *testing.T) {
	h := &Handler{} // spannerAdmin nil → standalone
	for _, scope := range []string{"", scopeFleet, scopeLocal, "somepeerpubkey"} {
		var data SecurityPageData
		if h.routeScopedPage(context.Background(), scope, pageSecurity, "", &data) {
			t.Errorf("standalone routeScopedPage(%q) routed; want local render (false)", scope)
		}
	}
}
