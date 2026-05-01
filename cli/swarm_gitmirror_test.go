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
// /api/v1/swarm/gitmirror/fleet response, points serverURL at it,
// and returns a cleanup.
func fleetFixture(t *testing.T, fleet fleetResponse) func() {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/swarm/gitmirror/fleet" {
			t.Errorf("expected /api/v1/swarm/gitmirror/fleet, got %s", r.URL.Path)
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

func sampleFleet() fleetResponse {
	pidA := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	pidB := "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"
	return fleetResponse{
		Totals: fleetTotals{
			Projects:      2,
			Hosts:         3,
			DivergentRefs: 0,
			TotalRevokes:  0,
			TotalLeaves:   0,
		},
		Participants: []string{"envoy-a", "envoy-b", "envoy-c"},
		Projects: []fleetProjectSummary{
			{
				ProjectID:        pidA,
				Handle:           "vigo",
				DrFanout:         "fleet-wide",
				MemberFleetCount: 1,
				HostCount:        2,
				DigestUnanimous:  true,
			},
			{
				ProjectID:        pidB,
				Handle:           "notes",
				DrFanout:         "members-only",
				MemberFleetCount: 1,
				HostCount:        1,
				DigestUnanimous:  true,
			},
		},
	}
}

func TestResolveFleetProjectExactProjectID(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetProject(fleet.Projects, fleet.Projects[0].ProjectID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Handle != "vigo" {
		t.Fatalf("expected handle vigo, got %s", m.Handle)
	}
}

func TestResolveFleetProjectExactHandle(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetProject(fleet.Projects, "notes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Handle != "notes" {
		t.Fatalf("expected handle notes, got %s", m.Handle)
	}
}

func TestResolveFleetProjectUniquePrefix(t *testing.T) {
	fleet := sampleFleet()
	m, err := resolveFleetProject(fleet.Projects, "not")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Handle != "notes" {
		t.Fatalf("expected notes, got %s", m.Handle)
	}
}

func TestResolveFleetProjectAmbiguousHandle(t *testing.T) {
	fleet := sampleFleet()
	dup := fleet.Projects[0]
	dup.ProjectID = "ff" + dup.ProjectID[2:]
	fleet.Projects = append(fleet.Projects, dup)
	_, err := resolveFleetProject(fleet.Projects, "vigo")
	if err == nil {
		t.Fatal("expected ambiguity error when two projects share a handle")
	}
}

func TestResolveFleetProjectNoMatch(t *testing.T) {
	_, err := resolveFleetProject(sampleFleet().Projects, "does-not-exist")
	if err == nil {
		t.Fatal("expected no-match error")
	}
}

func TestResolveFleetProjectEmptyFleet(t *testing.T) {
	_, err := resolveFleetProject(nil, "anything")
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
	if got.Totals.Projects != fleet.Totals.Projects {
		t.Errorf("expected %d projects, got %d", fleet.Totals.Projects, got.Totals.Projects)
	}
	if len(got.Projects) != len(fleet.Projects) {
		t.Errorf("expected %d projects, got %d", len(fleet.Projects), len(got.Projects))
	}
	if got.Projects[0].Handle != "vigo" {
		t.Errorf("expected first handle vigo, got %s", got.Projects[0].Handle)
	}
}
