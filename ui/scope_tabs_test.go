// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"net/http/httptest"
	"testing"
)

// The dashboard scope tab strip (ADR-037) renders in standalone mode too,
// so the Fleet | <bolt> chrome is stable from one server up and adding a
// second bolt doesn't restructure the page. In standalone the spannerAdmin
// is nil, so these paths are exercised without a real spanner.

func TestStandaloneScopeTabs(t *testing.T) {
	h := &Handler{serverHostname: "srv1.example.com"} // spannerAdmin nil → standalone
	r := httptest.NewRequest("GET", "/", nil)

	tabs := h.buildScopeTabs(r, h.resolveScope(r))
	if len(tabs) != 2 {
		t.Fatalf("standalone tabs = %d, want 2 (Fleet + local bolt)", len(tabs))
	}
	if !tabs[0].IsFleet || !tabs[0].Active {
		t.Errorf("tab[0] should be the active Fleet tab, got %+v", tabs[0])
	}
	if tabs[1].IsFleet || !tabs[1].IsLocal || tabs[1].Label != "srv1.example.com" {
		t.Errorf("tab[1] should be the local bolt labeled by hostname, got %+v", tabs[1])
	}
	if tabs[1].Active {
		t.Errorf("local tab should not be active under the default Fleet scope")
	}
}

func TestStandaloneScopeTabsHostnameFallback(t *testing.T) {
	h := &Handler{} // no serverHostname configured
	r := httptest.NewRequest("GET", "/", nil)

	tabs := h.buildScopeTabs(r, scopeFleet)
	if len(tabs) != 2 || tabs[1].Label != "localhost" {
		t.Errorf("local tab label = %q, want \"localhost\" fallback", tabs[1].Label)
	}
}

func TestStandaloneScopeTabsActiveToggle(t *testing.T) {
	h := &Handler{serverHostname: "srv1"}
	r := httptest.NewRequest("GET", "/?scope="+scopeLocal, nil)

	tabs := h.buildScopeTabs(r, h.resolveScope(r))
	if tabs[0].Active {
		t.Errorf("Fleet tab should not be active when scope=%s", scopeLocal)
	}
	if !tabs[1].Active {
		t.Errorf("local tab should be active when scope=%s", scopeLocal)
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
