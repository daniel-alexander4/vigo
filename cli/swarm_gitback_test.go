// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fleetFixture spins up a httptest server that serves a canned
// /api/v1/swarm/gitback/fleet response, points serverURL at it,
// and returns a cleanup.
func fleetFixture(t *testing.T, fleet fleetResponse) func() {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/swarm/gitback/fleet" {
			t.Errorf("expected /api/v1/swarm/gitback/fleet, got %s", r.URL.Path)
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
				DrScope:          "swarm-wide",
				MemberFleetCount: 1,
				HostCount:        2,
				DigestUnanimous:  true,
			},
			{
				ProjectID:        pidB,
				Handle:           "notes",
				DrScope:          "members-only",
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

func TestRenderGitbackMembersEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderGitbackMembers(nil, false, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No gitback projects") {
		t.Fatalf("expected empty-fleet message, got: %s", buf.String())
	}
}

func TestRenderGitbackMembersTable(t *testing.T) {
	pid := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	founder := strings.Repeat("aa", 32)
	mem := strings.Repeat("bb", 32)
	projects := []fleetProjectSummary{
		{
			ProjectID:          pid,
			Handle:             "vigo",
			FounderPuddlePubkey: founder,
			Members:            []string{founder, mem},
			MemberFleetCount:   2,
			DrScope:            "swarm-wide",
		},
	}
	var buf bytes.Buffer
	if err := renderGitbackMembers(projects, false, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "PROJECT: vigo") {
		t.Errorf("expected project header, got: %s", out)
	}
	if !strings.Contains(out, "founder") {
		t.Errorf("expected role 'founder' in output, got: %s", out)
	}
	if !strings.Contains(out, "member") {
		t.Errorf("expected role 'member' in output, got: %s", out)
	}
	if !strings.Contains(out, "ed25519:"+founder) {
		t.Errorf("expected founder pubkey in output, got: %s", out)
	}
	if !strings.Contains(out, "ed25519:"+mem) {
		t.Errorf("expected member pubkey in output, got: %s", out)
	}
}

func TestRenderGitbackMembersSortsByHandle(t *testing.T) {
	founder := strings.Repeat("aa", 32)
	projects := []fleetProjectSummary{
		{
			ProjectID:          "ff" + strings.Repeat("ff", 31),
			Handle:             "zeta",
			FounderPuddlePubkey: founder,
			Members:            []string{founder},
		},
		{
			ProjectID:          "11" + strings.Repeat("11", 31),
			Handle:             "alpha",
			FounderPuddlePubkey: founder,
			Members:            []string{founder},
		},
	}
	var buf bytes.Buffer
	if err := renderGitbackMembers(projects, false, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	alpha := strings.Index(out, "PROJECT: alpha")
	zeta := strings.Index(out, "PROJECT: zeta")
	if alpha < 0 || zeta < 0 {
		t.Fatalf("missing one of the project headers: alpha=%d zeta=%d in:\n%s", alpha, zeta, out)
	}
	if alpha > zeta {
		t.Errorf("expected alpha before zeta in handle-sorted output, got alpha=%d zeta=%d", alpha, zeta)
	}
}

func TestRenderGitbackMembersJSON(t *testing.T) {
	founder := strings.Repeat("aa", 32)
	projects := []fleetProjectSummary{{
		ProjectID:          "0a" + strings.Repeat("0a", 31),
		Handle:             "vigo",
		FounderPuddlePubkey: founder,
		Members:            []string{founder},
		MemberFleetCount:   1,
	}}
	var buf bytes.Buffer
	if err := renderGitbackMembers(projects, true, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []fleetProjectSummary
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("expected valid JSON output, got error %v and body: %s", err, buf.String())
	}
	if len(got) != 1 || got[0].Handle != "vigo" {
		t.Fatalf("unexpected JSON payload: %+v", got)
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
