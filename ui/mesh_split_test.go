// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/vigocryptmesh"
	"git.alexander4.io/vigo/server/vigorepomesh"
	"git.alexander4.io/vigo/server/vigosyncmesh"
)

// mockMesh implements MeshSource with injectable snapshots so the
// split-out page handlers can be exercised without standing up a
// full aggregator + DB.
type mockMesh struct {
	sync  *vigosyncmesh.Fleet
	crypt *vigocryptmesh.Fleet
}

func (m *mockMesh) SyncSnapshot(ctx context.Context) (*vigosyncmesh.Fleet, error) {
	return m.sync, nil
}

func (m *mockMesh) CryptSnapshot(ctx context.Context) (*vigocryptmesh.Fleet, error) {
	return m.crypt, nil
}

type mockVigorepo struct {
	fleet *vigorepomesh.Fleet
}

func (m *mockVigorepo) FleetSnapshot(ctx context.Context) (*vigorepomesh.Fleet, error) {
	return m.fleet, nil
}

// TestMeshSplit_RBAC pins admin-only gating on the three new pages.
// Viewers must never see fleet-wide per-user mesh state.
func TestMeshSplit_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{}
	h.vigorepoFleet = &mockVigorepo{}

	cases := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/vigosync", h.Vigosync},
		{"/ui/fragments/vigosync-user/dan", h.VigosyncUserDrilldown},
		{"/vigocrypt", h.Vigocrypt},
		{"/ui/fragments/vigocrypt-user/dan", h.VigocryptUserDrilldown},
		{"/vigorepo", h.Vigorepo},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, c.path)
		if c.path == "/ui/fragments/vigosync-user/dan" || c.path == "/ui/fragments/vigocrypt-user/dan" {
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
		{"/mesh", "/vigosync"},
		{"/mesh?tab=sync", "/vigosync"},
		{"/mesh?tab=crypt", "/vigocrypt"},
		{"/mesh?tab=unknown", "/vigosync"},
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

// TestVigosync_EmptyRendersHealthy exercises the empty-fleet path —
// aggregator returns a Fleet with no users, health banner should be OK.
func TestVigosync_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{sync: &vigosyncmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Vigosync(rec, adminRequest(http.MethodGet, "/vigosync"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Vigosync returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !containsAll(body, "Vigosync", "health-banner--ok") {
		t.Errorf("empty vigosync should render healthy banner; body sample:\n%s", snippet(body, 1000))
	}
}

func TestVigocrypt_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{crypt: &vigocryptmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Vigocrypt(rec, adminRequest(http.MethodGet, "/vigocrypt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Vigocrypt returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Vigocrypt", "health-banner--ok") {
		t.Error("empty vigocrypt should render healthy banner")
	}
}

func TestVigorepo_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.vigorepoFleet = &mockVigorepo{fleet: &vigorepomesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Vigorepo(rec, adminRequest(http.MethodGet, "/vigorepo"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Vigorepo returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Vigorepo", "health-banner--ok") {
		t.Error("empty vigorepo should render healthy banner")
	}
}

// TestVigorepo_OrphanPromotesBannerErr — when an aggregator surfaces
// orphan mirrors, the banner must be err-level and list the problem.
func TestVigorepo_OrphanPromotesBannerErr(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.vigorepoFleet = &mockVigorepo{fleet: &vigorepomesh.Fleet{
		Labels: []vigorepomesh.LabelSummary{
			{Label: "vigorepo:plex:dan:notes", Publisher: "plex",
				CountOrphan: 2, CountMaterialized: 3, CountSelected: 3, MaxMirrors: 5},
		},
		Totals: vigorepomesh.Totals{Labels: 1, Mirrors: 3, OrphanMirrors: 2},
	}}

	rec := httptest.NewRecorder()
	h.Vigorepo(rec, adminRequest(http.MethodGet, "/vigorepo"))
	body := rec.Body.String()
	if !containsAll(body, "health-banner--err", "orphan", "vigorepo:plex:dan:notes") {
		t.Errorf("orphan mirrors should surface in err-level banner; body sample:\n%s", snippet(body, 1500))
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
