// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.alexander4.io/vigo/server/swarm/cryptsyncmesh"
	"git.alexander4.io/vigo/server/swarm/gitmirrormesh"
	"git.alexander4.io/vigo/server/swarm/filesyncmesh"
)

// mockMesh implements MeshSource with injectable snapshots so the
// split-out page handlers can be exercised without standing up a
// full aggregator + DB.
type mockMesh struct {
	sync  *filesyncmesh.Fleet
	crypt *cryptsyncmesh.Fleet
}

func (m *mockMesh) SyncSnapshot(ctx context.Context) (*filesyncmesh.Fleet, error) {
	return m.sync, nil
}

func (m *mockMesh) CryptSnapshot(ctx context.Context) (*cryptsyncmesh.Fleet, error) {
	return m.crypt, nil
}

func (m *mockMesh) EnvoyFilesyncFiles(_ context.Context, _ string) []filesyncmesh.EnvoyFile {
	return nil
}

func (m *mockMesh) EnvoyCryptsyncFiles(_ context.Context, _ string) []cryptsyncmesh.EnvoyFile {
	return nil
}

type mockGitmirror struct {
	fleet *gitmirrormesh.Fleet
}

func (m *mockGitmirror) FleetSnapshot(ctx context.Context) (*gitmirrormesh.Fleet, error) {
	return m.fleet, nil
}

func (m *mockGitmirror) EnvoyMirrors(_ context.Context, _ string) []gitmirrormesh.EnvoyMirror {
	return nil
}

// TestMeshSplit_RBAC pins admin-only gating on the three new pages.
// Viewers must never see fleet-wide per-user mesh state.
func TestMeshSplit_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{}
	h.gitmirrorFleet = &mockGitmirror{}

	cases := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/filesync", h.Filesync},
		{"/ui/fragments/filesync-user/dan", h.EchoUserDrilldown},
		{"/swarm/cryptsync", h.Cryptsync},
		{"/ui/fragments/cryptsync-user/dan", h.CryptsyncUserDrilldown},
		{"/swarm/gitmirror", h.Gitmirror},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, c.path)
		if c.path == "/ui/fragments/filesync-user/dan" || c.path == "/ui/fragments/cryptsync-user/dan" {
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
		{"/mesh", "/filesync"},
		{"/mesh?tab=sync", "/filesync"},
		{"/mesh?tab=crypt", "/swarm/cryptsync"},
		{"/mesh?tab=unknown", "/filesync"},
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
	h.mesh = &mockMesh{sync: &filesyncmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Filesync(rec, adminRequest(http.MethodGet, "/filesync"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Filesync returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !containsAll(body, "Filesync", "health-banner--ok") {
		t.Errorf("empty filesync should render healthy banner; body sample:\n%s", snippet(body, 1000))
	}
}

func TestCryptsync_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.mesh = &mockMesh{crypt: &cryptsyncmesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Cryptsync(rec, adminRequest(http.MethodGet, "/swarm/cryptsync"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Cryptsync returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Cryptsync", "health-banner--ok") {
		t.Error("empty cryptsync should render healthy banner")
	}
}

func TestGitmirror_EmptyRendersHealthy(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.gitmirrorFleet = &mockGitmirror{fleet: &gitmirrormesh.Fleet{}}

	rec := httptest.NewRecorder()
	h.Gitmirror(rec, adminRequest(http.MethodGet, "/swarm/gitmirror"))
	if rec.Code != http.StatusOK {
		t.Fatalf("Gitmirror returned %d, want 200", rec.Code)
	}
	if !containsAll(rec.Body.String(), "Gitmirror", "health-banner--ok") {
		t.Error("empty gitmirror should render healthy banner")
	}
}

// TestGitmirror_OrphanPromotesBannerErr — when an aggregator surfaces
// orphan mirrors, the banner must be err-level and list the problem.
func TestGitmirror_OrphanPromotesBannerErr(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.gitmirrorFleet = &mockGitmirror{fleet: &gitmirrormesh.Fleet{
		Labels: []gitmirrormesh.LabelSummary{
			{Label: "gitmirror:plex:dan:notes", Publisher: "plex",
				CountOrphan: 2, CountMaterialized: 3, CountSelected: 3, MaxMirrors: 5},
		},
		Totals: gitmirrormesh.Totals{Labels: 1, Mirrors: 3, OrphanMirrors: 2},
	}}

	rec := httptest.NewRecorder()
	h.Gitmirror(rec, adminRequest(http.MethodGet, "/swarm/gitmirror"))
	body := rec.Body.String()
	if !containsAll(body, "health-banner--err", "orphan", "gitmirror:plex:dan:notes") {
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
