// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestComplianceFleetLinks_GatedOnFleetRead: compliance-readable fragments
// list envoy hostnames. The compliance role has no fleet.read and would get a
// 403 from /envoys/{id}, so the hostname renders as plain text for it and as
// a link for a viewer.
func TestComplianceFleetLinks_GatedOnFleetRead(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true

	if h.CanFleetRead(roleRequest(http.MethodGet, "/", "compliance")) {
		t.Fatal("compliance role must not hold fleet.read")
	}
	if !h.CanFleetRead(roleRequest(http.MethodGet, "/", "viewer")) {
		t.Fatal("viewer role must hold fleet.read")
	}

	type covRow struct {
		ID, Hostname, LastSeen                       string
		TotalControls, CoveredControls, MissingCount int
		CoveragePercent                              float64
	}
	for _, can := range []bool{true, false} {
		drill := EvidenceControlDrillData{
			Locale:       "en",
			Envoys:       []EvidenceControlEnvoy{{ID: "e1", Hostname: "web01"}},
			CanFleetRead: can,
		}
		cov := struct {
			Locale       string
			CanFleetRead bool
			Fleet        []covRow
		}{"en", can, []covRow{{ID: "e1", Hostname: "web01"}}}

		for name, render := range map[string]func(w http.ResponseWriter){
			"evidence-control-drill": func(w http.ResponseWriter) {
				h.renderer.RenderFragment(w, "compliance-evidence", "evidence-control-drill", drill)
			},
			"evidence-coverage": func(w http.ResponseWriter) {
				h.renderer.RenderFragment(w, "compliance-evidence", "evidence-coverage", cov)
			},
		} {
			rec := httptest.NewRecorder()
			render(rec)
			body := rec.Body.String()
			if !strings.Contains(body, "web01") {
				t.Fatalf("%s: hostname missing; body:\n%s", name, body)
			}
			hasLink := strings.Contains(body, `href="/envoys/e1"`)
			if hasLink != can {
				t.Errorf("%s CanFleetRead=%v: envoy link present=%v", name, can, hasLink)
			}
		}
	}
}
