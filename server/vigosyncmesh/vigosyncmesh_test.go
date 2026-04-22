// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigosyncmesh

import (
	"sort"
	"testing"
	"time"
)

// buildUserSummary in isolation — no DB round-trip — exercising the
// file-status state machine across reporting / not-reporting /
// stale / missing / divergent / tombstoned cases.
func TestBuildUserSummary_FileStatus(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Unix() - 10
	stale := now.Unix() - int64(StaleAfter.Seconds()) - 5

	rows := []envRow{
		{hostname: "a", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users: []userTrait{{
				User: "dan",
				Files: []fileTrait{
					{Name: "agree.pdf", Sha256: "SHA_X", Size: 10, Mtime: fresh},
					{Name: "disagree.pdf", Sha256: "SHA_A", Size: 10, Mtime: fresh},
					{Name: "partial.pdf", Sha256: "SHA_P", Size: 10, Mtime: fresh},
				},
				FileCount: 3,
			}},
		}},
		{hostname: "b", reported: true, envelope: &traitEnvelope{
			UpdatedAt: fresh,
			Users: []userTrait{{
				User: "dan",
				Files: []fileTrait{
					{Name: "agree.pdf", Sha256: "SHA_X", Size: 10, Mtime: fresh},
					{Name: "disagree.pdf", Sha256: "SHA_B", Size: 10, Mtime: fresh},
					// "partial.pdf" is missing on b — should flag partial.
				},
				FileCount: 2,
				Tombstones: []tombstoneTrait{
					{Name: "gone.pdf", DeletionTime: fresh - 100},
				},
			}},
		}},
		{hostname: "c", reported: true, envelope: &traitEnvelope{
			UpdatedAt: stale,
			Users: []userTrait{{
				User: "dan",
				Files: []fileTrait{
					{Name: "agree.pdf", Sha256: "SHA_X", Size: 10, Mtime: stale},
				},
				FileCount: 1,
			}},
		}},
		{hostname: "d", reported: false, envelope: nil},
	}

	summary := buildUserSummary("dan", rows, now)

	// Envoy statuses.
	gotStatuses := map[string]string{}
	for _, ep := range summary.Envoys {
		gotStatuses[ep.Hostname] = ep.Status
	}
	if gotStatuses["a"] != "reporting" {
		t.Errorf("envoy a: want reporting, got %q", gotStatuses["a"])
	}
	if gotStatuses["b"] != "reporting" {
		t.Errorf("envoy b: want reporting, got %q", gotStatuses["b"])
	}
	if gotStatuses["c"] != "stale" {
		t.Errorf("envoy c: want stale, got %q", gotStatuses["c"])
	}
	if gotStatuses["d"] != "not_reporting" {
		t.Errorf("envoy d: want not_reporting, got %q", gotStatuses["d"])
	}

	// File statuses.
	fileStatus := map[string]string{}
	for _, f := range summary.Files {
		fileStatus[f.Name] = f.Status
	}
	// "agree.pdf" is live on all *reporting* envoys (a and b — c is
	// stale but still reports it, which the aggregator currently
	// treats as present). Status should be live.
	if s := fileStatus["agree.pdf"]; s != "live" {
		t.Errorf("agree.pdf: want live, got %q", s)
	}
	// "disagree.pdf" has mismatched shas on a and b → divergent.
	if s := fileStatus["disagree.pdf"]; s != "divergent" {
		t.Errorf("disagree.pdf: want divergent, got %q", s)
	}
	// "partial.pdf" present on a, missing on b → partial.
	if s := fileStatus["partial.pdf"]; s != "partial" {
		t.Errorf("partial.pdf: want partial, got %q", s)
	}
	// "gone.pdf" is tombstoned on b, nobody has it live.
	if s := fileStatus["gone.pdf"]; s != "tombstoned" {
		t.Errorf("gone.pdf: want tombstoned, got %q", s)
	}

	// Divergences list should contain exactly disagree + partial.
	sort.Strings(summary.Divergences)
	want := []string{"disagree.pdf", "partial.pdf"}
	if len(summary.Divergences) != len(want) {
		t.Fatalf("divergences: got %v, want %v", summary.Divergences, want)
	}
	for i, s := range want {
		if summary.Divergences[i] != s {
			t.Errorf("divergences[%d]: got %q, want %q", i, summary.Divergences[i], s)
		}
	}
}

func TestBuildUserSummary_EmptyFleet(t *testing.T) {
	now := time.Unix(10_000, 0)
	rows := []envRow{
		{hostname: "a", reported: false, envelope: nil},
	}
	summary := buildUserSummary("dan", rows, now)
	// Nothing reported → nothing in files; envoy shows as not_reporting.
	if len(summary.Files) != 0 {
		t.Errorf("expected empty file list, got %d", len(summary.Files))
	}
	if len(summary.Envoys) != 1 || summary.Envoys[0].Status != "not_reporting" {
		t.Errorf("expected single not_reporting envoy, got %+v", summary.Envoys)
	}
}
