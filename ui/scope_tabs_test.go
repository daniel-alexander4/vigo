// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The /fleet per-bolt drill strip (ADR-037 "D") renders only for a genuine
// multi-bolt federation. In standalone mode the spannerAdmin is nil and there
// are no bolts to choose between, so buildScopeTabs returns no tabs. These
// paths are exercised without a real spanner.

func TestStandaloneScopeTabsHidden(t *testing.T) {
	h := &Handler{serverHostname: "srv1.example.com"} // spannerAdmin nil → standalone
	r := httptest.NewRequest("GET", "/", nil)

	if tabs := h.buildScopeTabs(r, h.resolveScope(r)); len(tabs) != 0 {
		t.Fatalf("standalone scope tabs = %d, want 0 (strip hidden in standalone)", len(tabs))
	}
}

// resolveScope drives the per-bolt local pages + shared poll fragments
// (ADR-037 "D"): an explicit ?scope= wins (the /fleet drill links carry it),
// otherwise scopeLocal — THIS bolt. There is no scope cookie anymore.
func TestResolveScopeDefaultsToLocal(t *testing.T) {
	h := &Handler{}
	if got := h.resolveScope(httptest.NewRequest("GET", "/", nil)); got != scopeLocal {
		t.Errorf("default scope = %q, want %q (this bolt)", got, scopeLocal)
	}
	r := httptest.NewRequest("GET", "/?scope="+scopeFleet, nil)
	if got := h.resolveScope(r); got != scopeFleet {
		t.Errorf("explicit ?scope= = %q, want %q", got, scopeFleet)
	}
}

// Post-D the local pages are purely local: resolveScope no longer reads a
// scope cookie, so a stale pre-D vigo_scope cookie can't silently scope a
// local page to a bolt. The /fleet drill's ?scope= still wins.
func TestResolveScopeIgnoresStaleCookie(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("GET", "/security", nil)
	r.AddCookie(&http.Cookie{Name: "vigo_scope", Value: "deadbeefpubkey"})
	if got := h.resolveScope(r); got != scopeLocal {
		t.Errorf("stale scope cookie must be ignored, got %q want %q", got, scopeLocal)
	}
	r2 := httptest.NewRequest("GET", "/security?scope="+scopeFleet, nil)
	if got := h.resolveScope(r2); got != scopeFleet {
		t.Errorf("explicit ?scope= should win, got %q", got)
	}
}

// fleetScope drives the dedicated /fleet/* section (ADR-037 "D"): the
// federated view by definition. It never reads a cookie — only an explicit
// ?scope=<bolt> drills to one bolt, otherwise Fleet.
func TestFleetScopeIgnoresCookie(t *testing.T) {
	h := &Handler{}

	r := httptest.NewRequest("GET", "/fleet/security", nil)
	r.AddCookie(&http.Cookie{Name: "vigo_scope", Value: "deadbeefpubkey"})
	if got := h.fleetScope(r); got != scopeFleet {
		t.Errorf("fleetScope must ignore the cookie, got %q want %q", got, scopeFleet)
	}

	r2 := httptest.NewRequest("GET", "/fleet/security?scope=deadbeefpubkey", nil)
	if got := h.fleetScope(r2); got != "deadbeefpubkey" {
		t.Errorf("explicit ?scope= should drill to the bolt, got %q", got)
	}
}

// Standalone scope is cosmetic: every scope value renders the full local
// page, never the reduced federated view — so no card is ever hidden on a
// single server.
func TestStandaloneScopedAwayAlwaysFalse(t *testing.T) {
	h := &Handler{}
	for _, scope := range []string{"", scopeFleet, scopeLocal, "deadbeefpubkeyhex"} {
		if h.scopedAway(context.Background(), scope) {
			t.Errorf("standalone scopedAway(%q) = true, want false (always full local render)", scope)
		}
	}
}
