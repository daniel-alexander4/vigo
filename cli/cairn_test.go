// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fleetFixture spins up a httptest server that serves a canned
// /api/v1/cairn/fleet response, points serverURL at it, and
// returns a cleanup.
func fleetFixture(t *testing.T, fleet fleetResponse) func() {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cairn/fleet" {
			t.Errorf("expected /api/v1/cairn/fleet, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fleet)
	}))
	prev := serverURL
	serverURL = ts.URL
	return func() {
		ts.Close()
		serverURL = prev
	}
}

func fullFlag(b bool) *bool { return &b }

func sampleFleet() fleetResponse {
	return fleetResponse{
		Totals: fleetTotals{
			Sources:       2,
			Labels:        2,
			Mirrors:       3,
			OrphanMirrors: 1,
			StaleMirrors:  0,
		},
		Participants: []string{"envoy-a", "envoy-b", "envoy-c"},
		Labels: []fleetLabelSummary{
			{
				Label:             "cairn:src:dan:vigo",
				Publisher:         "src",
				User:              "dan",
				Project:           "vigo",
				MaxMirrors:        5,
				GitSha:            "97debaead89e",
				LastPublishFull:   fullFlag(false),
				LastBundleSize:    1653,
				CountSelected:     2,
				CountMaterialized: 2,
			},
			{
				Label:             "cairn:src:dan:notes",
				Publisher:         "src",
				User:              "dan",
				Project:           "notes",
				MaxMirrors:        3,
				LastPublishFull:   fullFlag(true),
				LastBundleSize:    204_800,
				CountSelected:     1,
				CountMaterialized: 1,
			},
		},
	}
}

func TestResolveFleetLabelExactLabel(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetLabel(fleet.Labels, "cairn:src:dan:vigo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Project != "vigo" {
		t.Fatalf("expected project vigo, got %s", m.Project)
	}
}

func TestResolveFleetLabelExactProject(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetLabel(fleet.Labels, "notes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Label != "cairn:src:dan:notes" {
		t.Fatalf("expected notes label, got %s", m.Label)
	}
}

func TestResolveFleetLabelUniquePrefix(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetLabel(fleet.Labels, "not")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Project != "notes" {
		t.Fatalf("expected notes, got %s", m.Project)
	}
}

func TestResolveFleetLabelAmbiguousProjectAcrossPublishers(t *testing.T) {
	fleet := sampleFleet()
	fleet.Labels = append(fleet.Labels, fleetLabelSummary{
		Label:     "cairn:src2:dan:vigo",
		Publisher: "src2",
		User:      "dan",
		Project:   "vigo",
	})
	_, err := resolveFleetLabel(fleet.Labels, "vigo")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
}

func TestResolveFleetLabelNoMatch(t *testing.T) {
	_, err := resolveFleetLabel(sampleFleet().Labels, "does-not-exist")
	if err == nil {
		t.Fatal("expected no-match error")
	}
}

func TestResolveFleetLabelEmptyFleet(t *testing.T) {
	_, err := resolveFleetLabel(nil, "anything")
	if err == nil {
		t.Fatal("expected error on empty fleet")
	}
}

func TestFetchFleetHitsAggregatorEndpoint(t *testing.T) {
	// Verifies the CLI talks to the canonical aggregator endpoint.
	// Anything else is a plumbing regression.
	fleet := sampleFleet()
	cleanup := fleetFixture(t, fleet)
	defer cleanup()
	got, err := fetchFleet()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Totals.Sources != fleet.Totals.Sources {
		t.Errorf("expected %d sources, got %d", fleet.Totals.Sources, got.Totals.Sources)
	}
	if len(got.Labels) != len(fleet.Labels) {
		t.Errorf("expected %d labels, got %d", len(fleet.Labels), len(got.Labels))
	}
	// Spot-check bundle metadata passes through intact.
	if got.Labels[0].LastBundleSize != 1653 {
		t.Errorf("expected bundle size 1653, got %d", got.Labels[0].LastBundleSize)
	}
	if got.Labels[0].LastPublishFull == nil || *got.Labels[0].LastPublishFull {
		t.Errorf("expected first label to be incremental, got %+v", got.Labels[0].LastPublishFull)
	}
}
