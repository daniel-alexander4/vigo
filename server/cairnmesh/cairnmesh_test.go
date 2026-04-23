// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package cairnmesh

import "testing"

func TestParseLabel(t *testing.T) {
	tests := []struct {
		in                       string
		seeder, user, project    string
	}{
		{"cairn:danlap:dan:vigo", "danlap", "dan", "vigo"},
		{"cairn:h:u:p", "h", "u", "p"},
		{"cairn:", "", "", ""},
		{"not-a-label", "", "", ""},
		{"cairn:only-two", "", "", ""},
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
		Label:           "cairn:danlap:dan:vigo",
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

// TestSummaryCarriesBlobSha pins the contract the swarm detail
// page relies on: LabelSummary.BlobSha mirrors the publisher's
// source trait so `/cairn` can link each label to `/swarm/<sha>`.
func TestSummaryCarriesBlobSha(t *testing.T) {
	src := sourceTrait{
		Label:   "cairn:danlap:dan:vigo",
		BlobSha: "abc123",
		GitSha:  "deadbeef",
	}
	sum := summaryFromSource("danlap", src, 0, nil)
	if sum.BlobSha != "abc123" {
		t.Errorf("BlobSha = %q; want abc123", sum.BlobSha)
	}
	if sum.GitSha != "deadbeef" {
		t.Errorf("GitSha = %q; want deadbeef", sum.GitSha)
	}
}

// TestSummaryCarriesBundleMetadata pins the 0.24.1 contract: the
// publisher's last_publish_full / last_bundle_size fields flow
// through summaryFromSource into LabelSummary so the swarm detail
// page can explain why a cairn seed is only a few KB ("last bundle
// was incremental") instead of leaving operators guessing.
func TestSummaryCarriesBundleMetadata(t *testing.T) {
	full := false
	src := sourceTrait{
		Label:           "cairn:danlap:dan:vigo",
		LastPublishFull: &full,
		LastBundleSize:  1653,
	}
	sum := summaryFromSource("danlap", src, 0, nil)
	if sum.LastPublishFull == nil || *sum.LastPublishFull != false {
		t.Fatalf("LastPublishFull = %+v; want Some(false)", sum.LastPublishFull)
	}
	if sum.LastBundleSize != 1653 {
		t.Errorf("LastBundleSize = %d; want 1653", sum.LastBundleSize)
	}
}

// TestSummaryTolerantOfLegacyAgents confirms a report from an older
// agent (no bundle metadata fields) produces a LabelSummary with
// LastPublishFull == nil — the UI/CLI then correctly render a dash
// instead of a false "full" or "incremental" claim.
func TestSummaryTolerantOfLegacyAgents(t *testing.T) {
	src := sourceTrait{Label: "cairn:h:u:p"}
	sum := summaryFromSource("h", src, 0, nil)
	if sum.LastPublishFull != nil {
		t.Errorf("LastPublishFull = %+v; want nil for pre-0.24.1 agent", sum.LastPublishFull)
	}
	if sum.LastBundleSize != 0 {
		t.Errorf("LastBundleSize = %d; want 0", sum.LastBundleSize)
	}
}

// TestFleetLookups verifies both lookup helpers used by the swarm
// detail page enrichment path.
func TestFleetLookups(t *testing.T) {
	f := &Fleet{
		Labels: []LabelSummary{
			{Label: "cairn:h:u:a", BlobSha: "aaa"},
			{Label: "cairn:h:u:b", BlobSha: "bbb"},
		},
	}
	if got := f.LookupLabel("cairn:h:u:a"); got == nil || got.BlobSha != "aaa" {
		t.Errorf("LookupLabel miss")
	}
	if got := f.LookupBlobSha("bbb"); got == nil || got.Label != "cairn:h:u:b" {
		t.Errorf("LookupBlobSha miss")
	}
	if got := f.LookupLabel("not-present"); got != nil {
		t.Error("LookupLabel should return nil for unknown")
	}
	if got := f.LookupBlobSha(""); got != nil {
		t.Error("LookupBlobSha should return nil for empty sha")
	}
	var nilFleet *Fleet
	if got := nilFleet.LookupLabel("x"); got != nil {
		t.Error("nil receiver must return nil")
	}
}

// TestOrphanSummary handles the receiver-only case: publisher
// offline so the label only appears in receiver mirrors.
func TestOrphanSummary(t *testing.T) {
	holders := map[string]mirrorTrait{
		"host-x": {Label: "cairn:gone:dan:vigo", GitSha: "x", BlobSha: "xx", Stale: true},
	}
	sum := orphanSummary("cairn:gone:dan:vigo", holders)
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
