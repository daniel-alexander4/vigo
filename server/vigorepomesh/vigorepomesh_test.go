// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigorepomesh

import "testing"

func TestParseLabel(t *testing.T) {
	tests := []struct {
		in                       string
		seeder, user, project    string
	}{
		{"vigorepo:danlap:dan:vigo", "danlap", "dan", "vigo"},
		{"vigorepo:h:u:p", "h", "u", "p"},
		{"vigorepo:", "", "", ""},
		{"not-a-label", "", "", ""},
		{"vigorepo:only-two", "", "", ""},
	}
	for _, tc := range tests {
		s, u, p := ParseLabel(tc.in)
		if s != tc.seeder || u != tc.user || p != tc.project {
			t.Errorf("ParseLabel(%q) = (%q,%q,%q); want (%q,%q,%q)",
				tc.in, s, u, p, tc.seeder, tc.user, tc.project)
		}
	}
}

// TestSummaryFromSource_ActivePendingOrphan pins the per-cell
// status derivation that powers the UI mesh view.
func TestSummaryFromSource_ActivePendingOrphan(t *testing.T) {
	src := sourceTrait{
		Label:           "vigorepo:danlap:dan:vigo",
		MaxMirrors:      3,
		SelectedMirrors: []string{"host-a", "host-b"}, // publisher picked these two
	}
	// host-a has materialized; host-b hasn't yet; host-c holds an
	// old copy the publisher no longer selects.
	holders := map[string]mirrorTrait{
		"host-a": {Label: src.Label, GitSha: "a", BlobSha: "aa", LastRefreshed: 100},
		"host-c": {Label: src.Label, GitSha: "c", BlobSha: "cc", LastRefreshed: 50, Stale: true},
	}
	sum := summaryFromSource("publisher", src, 1000, holders)

	if got, want := sum.CountSelected, 2; got != want {
		t.Errorf("CountSelected = %d; want %d", got, want)
	}
	if got, want := sum.CountMaterialized, 2; got != want {
		t.Errorf("CountMaterialized = %d; want %d", got, want)
	}
	if got, want := sum.CountOrphan, 1; got != want {
		t.Errorf("CountOrphan = %d; want %d", got, want)
	}
	if got, want := sum.CountStale, 1; got != want {
		t.Errorf("CountStale = %d; want %d", got, want)
	}

	byHost := map[string]MirrorPresence{}
	for _, m := range sum.Mirrors {
		byHost[m.Host] = m
	}
	if byHost["host-a"].Status != "active" {
		t.Errorf("host-a status = %q; want active", byHost["host-a"].Status)
	}
	if byHost["host-b"].Status != "pending" {
		t.Errorf("host-b status = %q; want pending", byHost["host-b"].Status)
	}
	if byHost["host-c"].Status != "orphan" {
		t.Errorf("host-c status = %q; want orphan", byHost["host-c"].Status)
	}
}

// TestOrphanSummary handles the receiver-only case: publisher
// offline so the label only appears in receiver mirrors.
func TestOrphanSummary(t *testing.T) {
	holders := map[string]mirrorTrait{
		"host-x": {Label: "vigorepo:gone:dan:vigo", GitSha: "x", BlobSha: "xx", Stale: true},
	}
	sum := orphanSummary("vigorepo:gone:dan:vigo", holders)
	if sum.Publisher != "gone" {
		t.Errorf("Publisher = %q; want gone (derived from label)", sum.Publisher)
	}
	if sum.CountOrphan != 1 {
		t.Errorf("CountOrphan = %d; want 1", sum.CountOrphan)
	}
	if sum.CountStale != 1 {
		t.Errorf("CountStale = %d; want 1", sum.CountStale)
	}
	if sum.CountSelected != 0 {
		t.Errorf("CountSelected = %d; want 0 (no publisher)", sum.CountSelected)
	}
}
