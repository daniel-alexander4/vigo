// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// envoyIDFromMock returns the deterministic envoy ID the default test
// data wires up under m.data.envoys[0]. Lets these tests stay agnostic
// to the random-UUID generation in defaultTestData while still hitting
// the same envoy the rest of the suite uses.
func envoyIDFromMock(t *testing.T, h *Handler) string {
	t.Helper()
	ds, ok := h.dataSource.(*mockDataSource)
	if !ok {
		t.Fatalf("test handler is not using *mockDataSource")
	}
	return ds.data.envoys[0].id.String()
}

// TestEnvoyTraitsFragment_RendersCollapsedShells pins the new lazy-load
// shape: top-level groups render as `<details>` shells with hx-get
// pointing at the per-group fragment endpoint. No nested leaves should
// appear in the initial render — that's the whole point of the
// lazy-load (avoid the 2 MB DOM hang we just diagnosed).
func TestEnvoyTraitsFragment_RendersCollapsedShells(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-traits/"+envoyID, nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitsFragment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Each top-level group MUST point its lazy-load at the per-group endpoint.
	wantURL := "/ui/fragments/envoy-trait-group/" + envoyID + "?path=os"
	if !strings.Contains(body, wantURL) {
		t.Errorf("body missing lazy-load URL %q (so collapsed groups would not fetch their children)", wantURL)
	}
	if !strings.Contains(body, "hx-trigger=\"toggle once from:closest details\"") {
		t.Error("body missing the toggle-once hx-trigger that drives lazy loading")
	}
	// The placeholder body should be a loading-skeleton, not the actual leaves.
	if !strings.Contains(body, "loading-skeleton") {
		t.Error("body missing loading-skeleton placeholder inside group bodies")
	}
	// Sanity: nested leaves from the default test fixture should NOT
	// be present yet — they only appear after a lazy expand.
	if strings.Contains(body, "data-trait-path=\"hardware.cpu_count\"") {
		t.Error("nested leaf rendered inline — defeats the lazy-load fix")
	}
}

// TestEnvoyTraitsFragment_FilterMode confirms that ?q=<query> renders
// the flat matches fragment instead of the collapsed tree, so a search
// hits leaves regardless of which groups are expanded.
func TestEnvoyTraitsFragment_FilterMode(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-traits/"+envoyID+"?q=cpu", nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitsFragment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-trait-path=\"hardware.cpu_count\"") {
		t.Errorf("filter response missing the matching leaf hardware.cpu_count; body=%s", body)
	}
	// Filter result should NOT render the collapsed-shell hx-get wiring —
	// it's a flat list, not a tree.
	if strings.Contains(body, "hx-trigger=\"toggle once from:closest details\"") {
		t.Error("filter response leaked collapsed-shell wiring; should be a flat list")
	}
}

// TestEnvoyTraitsFragment_FilterEmpty: q with no matches still 200s and
// renders a "no matches" body so the operator gets a meaningful empty
// state instead of a blank container.
func TestEnvoyTraitsFragment_FilterEmpty(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-traits/"+envoyID+"?q=nonexistent-key", nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitsFragment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No traits match") {
		t.Errorf("expected empty-state message; body=%s", rec.Body.String())
	}
}

// TestEnvoyTraitGroupFragment_ValidPath: opening a lazy `<details>`
// fires this endpoint; it must return the targeted group's children
// (and only that group's), so we don't re-ship the whole tree.
func TestEnvoyTraitGroupFragment_ValidPath(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-trait-group/"+envoyID+"?path=hardware", nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitGroupFragment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-trait-path=\"hardware.cpu_count\"") {
		t.Errorf("group fragment missing the cpu_count leaf inside hardware; body=%s", body)
	}
	// The other top-level group ("os") must NOT leak into this response.
	if strings.Contains(body, "data-trait-path=\"os.family\"") {
		t.Error("group fragment leaked unrelated tree branch — should render only the requested group's children")
	}
}

// TestEnvoyTraitGroupFragment_UnknownPath returns 404 rather than
// silently rendering an empty body, so a stale or malformed URL
// surfaces as an explicit error instead of a no-op tab.
func TestEnvoyTraitGroupFragment_UnknownPath(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-trait-group/"+envoyID+"?path=does.not.exist", nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitGroupFragment(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown path; got %d", rec.Code)
	}
}

// TestEnvoyTraitGroupFragment_MissingPath: bad request without a path
// param — distinct from an unknown path so the client sees the right
// error class.
func TestEnvoyTraitGroupFragment_MissingPath(t *testing.T) {
	h := newTestHandler(t)
	envoyID := envoyIDFromMock(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-trait-group/"+envoyID, nil)
	req.SetPathValue("id", envoyID)
	rec := httptest.NewRecorder()
	h.EnvoyTraitGroupFragment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing path; got %d", rec.Code)
	}
}

// TestFlattenMatchingLeaves pins the substring-on-path semantic the
// filter endpoint relies on. Case-insensitive; matches against the full
// dotted path; ignores groups (they aren't leaves).
func TestFlattenMatchingLeaves(t *testing.T) {
	tree := []TraitNode{
		{Key: "os", Path: "os", Children: []TraitNode{
			{Key: "family", Path: "os.family", Value: "debian"},
			{Key: "name", Path: "os.name", Value: "Ubuntu"},
		}},
		{Key: "hardware", Path: "hardware", Children: []TraitNode{
			{Key: "cpu_count", Path: "hardware.cpu_count", Value: "4"},
		}},
	}
	cases := []struct {
		name  string
		query string
		want  []string // paths in result, in order
	}{
		{"substring matches one leaf", "cpu", []string{"hardware.cpu_count"}},
		{"matches across groups by path prefix", "os.", []string{"os.family", "os.name"}},
		{"case-insensitive", "FAMILY", []string{"os.family"}},
		{"no match returns empty", "zzzzz", nil},
		{"empty query matches everything", "", []string{"os.family", "os.name", "hardware.cpu_count"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches := flattenMatchingLeaves(tree, tc.query)
			got := make([]string, len(matches))
			for i, m := range matches {
				got[i] = m.Path
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestFindTraitNode pins lookup semantics for the per-group endpoint.
// A bad path must report "not found" rather than returning the closest
// ancestor — otherwise an operator's stale URL silently renders the
// wrong tree branch.
func TestFindTraitNode(t *testing.T) {
	tree := []TraitNode{
		{Key: "os", Path: "os", Children: []TraitNode{
			{Key: "family", Path: "os.family", Value: "debian"},
			{Key: "nested", Path: "os.nested", Children: []TraitNode{
				{Key: "deep", Path: "os.nested.deep", Value: "x"},
			}},
		}},
	}
	cases := []struct {
		name  string
		path  string
		want  string // node.Path if found, "" if not found
		found bool
	}{
		{"top-level hit", "os", "os", true},
		{"two-level hit", "os.nested", "os.nested", true},
		{"three-level hit", "os.nested.deep", "os.nested.deep", true},
		{"unknown top-level", "missing", "", false},
		{"partial path mid-tree", "os.missing", "", false},
		{"empty path", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := findTraitNode(tree, tc.path)
			if ok != tc.found {
				t.Fatalf("found=%v, want %v", ok, tc.found)
			}
			if ok && n.Path != tc.want {
				t.Errorf("got path %q, want %q", n.Path, tc.want)
			}
		})
	}
}
