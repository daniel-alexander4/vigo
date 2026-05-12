// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
	"git.alexander4.io/vigo/server/swarm/swarmgitmesh"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
)

// mockMesh implements MeshSource with injectable snapshots so the
// split-out page handlers can be exercised without standing up a
// full aggregator + DB.
type mockMesh struct {
	sync  *longdrawermesh.Fleet
	crypt *lockboxmesh.Fleet
}

func (m *mockMesh) SyncSnapshot(ctx context.Context) (*longdrawermesh.Fleet, error) {
	return m.sync, nil
}

func (m *mockMesh) CryptSnapshot(ctx context.Context) (*lockboxmesh.Fleet, error) {
	return m.crypt, nil
}

func (m *mockMesh) EnvoyLongdrawerFiles(_ context.Context, _ string) []longdrawermesh.EnvoyFile {
	return nil
}

func (m *mockMesh) EnvoyLockboxFiles(_ context.Context, _ string) []lockboxmesh.EnvoyFile {
	return nil
}

type mockSwarmgit struct {
	fleet *swarmgitmesh.Fleet
}

func (m *mockSwarmgit) FleetSnapshot(ctx context.Context) (*swarmgitmesh.Fleet, error) {
	return m.fleet, nil
}

func (m *mockSwarmgit) EnvoyProjects(_ context.Context, _ string) []swarmgitmesh.EnvoyProject {
	return nil
}

// TestMeshSplit_RBAC pins admin-only gating on the three new pages.
// Viewers must never see fleet-wide per-user mesh state.
func TestMeshSplit_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{}
	h.swarmgitFleet = &mockSwarmgit{}

	cases := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/longdrawer", h.Longdrawer},
		{"/ui/fragments/longdrawer-user/dan", h.EchoUserDrilldown},
		{"/swarm/lockbox", h.Lockbox},
		{"/ui/fragments/lockbox-user/dan", h.LockboxUserDrilldown},
		{"/swarm/swarmgit", h.Swarmgit},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, c.path)
		if c.path == "/ui/fragments/longdrawer-user/dan" || c.path == "/ui/fragments/lockbox-user/dan" {
			req.SetPathValue("user", "dan")
		}
		c.handler(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want 403", c.path, rec.Code)
		}
	}
}

// TestMeshRedirect_PreservesMuscleMemoryURLs confirms /mesh and
// /mesh?tab=crypt keep working after the split.
func TestMeshRedirect_PreservesMuscleMemoryURLs(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true

	cases := []struct {
		from string
		to   string
	}{
		{"/mesh", "/longdrawer"},
		{"/mesh?tab=sync", "/longdrawer"},
		{"/mesh?tab=crypt", "/swarm/lockbox"},
		{"/mesh?tab=unknown", "/longdrawer"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.from, nil)
		h.MeshRedirect(rec, req)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", c.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != c.to {
			t.Errorf("GET %s location = %q, want %q", c.from, got, c.to)
		}
	}
}

// TestEcho_EmptyRendersHealthy exercises the empty-fleet path —
// aggregator returns a Fleet with no users, health banner should be OK.
func TestEcho_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{sync: &longdrawermesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Longdrawer(rec, adminRequest(http.MethodGet, "/longdrawer"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Longdrawer returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !containsAll(body, "Longdrawer", "health-banner--ok") {
		t.Errorf("empty longdrawer should render healthy banner; body sample:\n%s", snippet(body, 1000))
	}
}

func TestLockbox_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{crypt: &lockboxmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Lockbox(rec, adminRequest(http.MethodGet, "/swarm/lockbox"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Lockbox returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Lockbox", "health-banner--ok") {
		t.Error("empty lockbox should render healthy banner")
	}
}

func TestSwarmgit_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.swarmgitFleet = &mockSwarmgit{fleet: &swarmgitmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Swarmgit(rec, adminRequest(http.MethodGet, "/swarm/swarmgit"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Swarmgit returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Swarmgit", "health-banner--ok") {
		t.Error("empty swarmgit should render healthy banner")
	}
}

// TestSwarmgit_DivergentRefsPromotesBannerWarn — when an aggregator
// surfaces a project where hosts disagree on a head sha, the banner
// must be warn-level and list the problem. (Post-ADR-015 the
// orphan/pending/stale signals from the legacy publisher-receiver
// model are gone; the new conditions are ref divergence and
// envelope-digest skew across hosts.)
func TestSwarmgit_DivergentRefsPromotesBannerWarn(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	pid := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	h.swarmgitFleet = &mockSwarmgit{fleet: &swarmgitmesh.Fleet{
		Projects: []swarmgitmesh.ProjectSummary{
			{
				ProjectID:        pid,
				Handle:           "team-notes",
				DrScope:          "members-only",
				HostCount:        2,
				MemberFleetCount: 1,
				DivergentRefs:    []string{"refs/heads/main"},
				DigestUnanimous:  true,
			},
		},
		Totals: swarmgitmesh.Totals{Projects: 1, Hosts: 2, DivergentRefs: 1},
	}}

	rec := httptest.NewRecorder()
	h.Swarmgit(rec, adminRequest(http.MethodGet, "/swarm/swarmgit"))
	body := rec.Body.String()
	if !containsAll(body, "health-banner--warn", "divergent", "team-notes") {
		t.Errorf("divergent refs should surface in warn-level banner; body sample:\n%s", snippet(body, 1500))
	}
}

// TestSwarmgit_DetailRendersFriendlyURLAndHostSize pins the project
// detail page against a populated summary. Two regression guards in
// one: (1) the host-size cell renders through humanBytes — a uint64
// HostPresence.SizeBytes used to abort the template mid-execute with
// "template error" the moment any host actually held a bare repo;
// (2) when the founder puddle has a friendly name (ADR-022), the
// swarmgit:// URL reflects it, with the canonical hex form alongside.
func TestSwarmgit_DetailRendersFriendlyURLAndHostSize(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	pid := "154e3b59465fe34a2d590edf5af6f81fba82608f27c7ca3b56a1bad5b5069d66"
	h.swarmgitFleet = &mockSwarmgit{fleet: &swarmgitmesh.Fleet{
		Projects: []swarmgitmesh.ProjectSummary{
			{
				ProjectID:           pid,
				Handle:              "vigo",
				FounderPuddlePubkey: "ed2551900000000000000000000000000000000000000000000000000000beef",
				FriendlyName:        "dan",
				DrScope:             "swarm-wide",
				HostCount:           1,
				MemberFleetCount:    1,
				DigestUnanimous:     true,
				Hosts: []swarmgitmesh.HostPresence{
					{Host: "danlap", HasBareRepo: true, SizeBytes: 4096, UpdatedAt: 1, LeafRole: "Admin"},
				},
			},
		},
		Totals: swarmgitmesh.Totals{Projects: 1, Hosts: 1},
	}}

	req := adminRequest(http.MethodGet, "/swarm/swarmgit/"+pid)
	req.SetPathValue("project_id", pid)
	rec := httptest.NewRecorder()
	h.Swarmgit(rec, req)
	body := rec.Body.String()
	if containsSubstr(body, "template error") {
		t.Fatalf("detail page rendered a template error; body sample:\n%s", snippet(body, 2000))
	}
	if !containsAll(body, "swarmgit://dan/vigo", "swarmgit://"+pid+"/vigo", "4.0 KB") {
		t.Errorf("expected friendly URL + canonical URL + host size; body sample:\n%s", snippet(body, 2500))
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !containsSubstr(haystack, n) {
			return false
		}
	}
	return true
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func snippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
