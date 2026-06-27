// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The header scope selector (ADR-037) renders only for a genuine multi-bolt
// federation. In standalone mode the spannerAdmin is nil and there are no
// bolts to choose between, so no selector is shown — buildScopeTabs returns
// no tabs. These paths are exercised without a real spanner.

func TestStandaloneScopeTabsHidden(t *testing.T) {
	h := &Handler{serverHostname: "srv1.example.com"} // spannerAdmin nil → standalone
	r := httptest.NewRequest("GET", "/", nil)

	if tabs := h.buildScopeTabs(r, h.resolveScope(r)); len(tabs) != 0 {
		t.Fatalf("standalone scope tabs = %d, want 0 (selector hidden in standalone)", len(tabs))
	}
}

func TestStandaloneResolveScopeDefaultsToFleet(t *testing.T) {
	h := &Handler{}
	if got := h.resolveScope(httptest.NewRequest("GET", "/", nil)); got != scopeFleet {
		t.Errorf("standalone default scope = %q, want %q", got, scopeFleet)
	}
	r := httptest.NewRequest("GET", "/?scope="+scopeLocal, nil)
	if got := h.resolveScope(r); got != scopeLocal {
		t.Errorf("explicit scope = %q, want %q", got, scopeLocal)
	}
}

// resolveScope persists the operator's choice via the vigo_scope cookie
// (ADR-037 header selector), with an explicit ?scope= query overriding it so
// deep links still work.
func TestResolveScopeCookiePrecedence(t *testing.T) {
	h := &Handler{}

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: scopeCookieName, Value: "deadbeefpubkey"})
	if got := h.resolveScope(r); got != "deadbeefpubkey" {
		t.Errorf("cookie scope = %q, want deadbeefpubkey", got)
	}

	r2 := httptest.NewRequest("GET", "/?scope="+scopeFleet, nil)
	r2.AddCookie(&http.Cookie{Name: scopeCookieName, Value: "deadbeefpubkey"})
	if got := h.resolveScope(r2); got != scopeFleet {
		t.Errorf("explicit ?scope= should override the cookie, got %q", got)
	}
}

// fleetScope drives the dedicated /fleet/* section (ADR-037 "D"): it is the
// federated view by definition, so it NEVER reads the vigo_scope cookie (which
// belongs to the per-bolt local pages) — only an explicit ?scope=<bolt> drills
// to one bolt, otherwise Fleet.
func TestFleetScopeIgnoresCookie(t *testing.T) {
	h := &Handler{}

	r := httptest.NewRequest("GET", "/fleet/security", nil)
	r.AddCookie(&http.Cookie{Name: scopeCookieName, Value: "deadbeefpubkey"})
	if got := h.fleetScope(r); got != scopeFleet {
		t.Errorf("fleetScope must ignore the cookie, got %q want %q", got, scopeFleet)
	}

	r2 := httptest.NewRequest("GET", "/fleet/security?scope=deadbeefpubkey", nil)
	if got := h.fleetScope(r2); got != "deadbeefpubkey" {
		t.Errorf("explicit ?scope= should drill to the bolt, got %q", got)
	}
}

// Standalone scope is cosmetic: every scope value renders the full local
// dashboard (Path A), never the reduced federated view — so no card is ever
// hidden on a single server.
func TestStandaloneScopedAwayAlwaysFalse(t *testing.T) {
	h := &Handler{}
	for _, scope := range []string{"", scopeFleet, scopeLocal, "deadbeefpubkeyhex"} {
		if h.scopedAway(context.Background(), scope) {
			t.Errorf("standalone scopedAway(%q) = true, want false (always full local render)", scope)
		}
	}
}
