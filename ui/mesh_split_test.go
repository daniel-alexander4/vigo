// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/sigilmesh"
	"git.alexander4.io/vigo/server/cairnmesh"
	"git.alexander4.io/vigo/server/echomesh"
)

// mockMesh implements MeshSource with injectable snapshots so the
// split-out page handlers can be exercised without standing up a
// full aggregator + DB.
type mockMesh struct {
	sync  *echomesh.Fleet
	crypt *sigilmesh.Fleet
}

func (m *mockMesh) SyncSnapshot(ctx context.Context) (*echomesh.Fleet, error) {
	return m.sync, nil
}

func (m *mockMesh) CryptSnapshot(ctx context.Context) (*sigilmesh.Fleet, error) {
	return m.crypt, nil
}

func (m *mockMesh) EnvoyEchoFiles(_ context.Context, _ string) []echomesh.EnvoyFile {
	return nil
}

func (m *mockMesh) EnvoySigilFiles(_ context.Context, _ string) []sigilmesh.EnvoyFile {
	return nil
}

type mockCairn struct {
	fleet *cairnmesh.Fleet
}

func (m *mockCairn) FleetSnapshot(ctx context.Context) (*cairnmesh.Fleet, error) {
	return m.fleet, nil
}

func (m *mockCairn) EnvoyMirrors(_ context.Context, _ string) []cairnmesh.EnvoyMirror {
	return nil
}

// TestMeshSplit_RBAC pins admin-only gating on the three new pages.
// Viewers must never see fleet-wide per-user mesh state.
func TestMeshSplit_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{}
	h.cairnFleet = &mockCairn{}

	cases := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/echo", h.Echo},
		{"/ui/fragments/echo-user/dan", h.EchoUserDrilldown},
		{"/sigil", h.Sigil},
		{"/ui/fragments/sigil-user/dan", h.SigilUserDrilldown},
		{"/cairn", h.Cairn},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, c.path)
		if c.path == "/ui/fragments/echo-user/dan" || c.path == "/ui/fragments/sigil-user/dan" {
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
		{"/mesh", "/echo"},
		{"/mesh?tab=sync", "/echo"},
		{"/mesh?tab=crypt", "/sigil"},
		{"/mesh?tab=unknown", "/echo"},
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
	h.mesh = &mockMesh{sync: &echomesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Echo(rec, adminRequest(http.MethodGet, "/echo"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Echo returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !containsAll(body, "Echo", "health-banner--ok") {
		t.Errorf("empty echo should render healthy banner; body sample:\n%s", snippet(body, 1000))
	}
}

func TestSigil_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{crypt: &sigilmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Sigil(rec, adminRequest(http.MethodGet, "/sigil"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Sigil returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Sigil", "health-banner--ok") {
		t.Error("empty sigil should render healthy banner")
	}
}

func TestCairn_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.cairnFleet = &mockCairn{fleet: &cairnmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Cairn(rec, adminRequest(http.MethodGet, "/cairn"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Cairn returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Cairn", "health-banner--ok") {
		t.Error("empty cairn should render healthy banner")
	}
}

// TestCairn_OrphanPromotesBannerErr — when an aggregator surfaces
// orphan mirrors, the banner must be err-level and list the problem.
func TestCairn_OrphanPromotesBannerErr(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.cairnFleet = &mockCairn{fleet: &cairnmesh.Fleet{
		Labels: []cairnmesh.LabelSummary{
			{Label: "cairn:plex:dan:notes", Publisher: "plex",
				CountOrphan: 2, CountMaterialized: 3, CountSelected: 3, MaxMirrors: 5},
		},
		Totals: cairnmesh.Totals{Labels: 1, Mirrors: 3, OrphanMirrors: 2},
	}}

	rec := httptest.NewRecorder()
	h.Cairn(rec, adminRequest(http.MethodGet, "/cairn"))
	body := rec.Body.String()
	if !containsAll(body, "health-banner--err", "orphan", "cairn:plex:dan:notes") {
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
