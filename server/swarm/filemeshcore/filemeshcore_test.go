// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package filemeshcore

import (
	"sort"
	"testing"
)

// FinalizeFiles is the file-status state machine both meshes share.
// This pins live / divergent / partial / tombstoned in one place — the
// coverage lockboxmesh's copy used to lack, so a fix here can't ship
// green on one mesh and broken on the other.
func TestFinalizeFiles_StatusDerivation(t *testing.T) {
	// Reporting envoys a and b; c reported a cell too but isn't in the
	// reporting set (e.g. stale) — its live cell still counts as
	// present, matching the meshes' behavior.
	files := []FileRow{
		// live on every reporting envoy, matching sha.
		{Name: "agree.pdf", PerEnvoy: []FileCellPerEnvoy{
			{Hostname: "a", State: "live", Sha256: "SHA_X", Size: 10},
			{Hostname: "b", State: "live", Sha256: "SHA_X", Size: 12},
		}},
		// sha disagrees → divergent.
		{Name: "disagree.pdf", PerEnvoy: []FileCellPerEnvoy{
			{Hostname: "a", State: "live", Sha256: "SHA_A", Size: 10},
			{Hostname: "b", State: "live", Sha256: "SHA_B", Size: 10},
		}},
		// present on a, absent on b (reporting) → partial.
		{Name: "partial.pdf", PerEnvoy: []FileCellPerEnvoy{
			{Hostname: "a", State: "live", Sha256: "SHA_P", Size: 10},
		}},
		// tombstoned, nobody holds it live.
		{Name: "gone.pdf", PerEnvoy: []FileCellPerEnvoy{
			{Hostname: "b", State: "tombstoned"},
		}},
	}

	fileCount, totalBytes, divergences := FinalizeFiles(files, []string{"a", "b"})

	status := map[string]string{}
	for _, f := range files {
		status[f.Name] = f.Status
	}
	for name, want := range map[string]string{
		"agree.pdf":    "live",
		"disagree.pdf": "divergent",
		"partial.pdf":  "partial",
		"gone.pdf":     "tombstoned",
	} {
		if status[name] != want {
			t.Errorf("%s: status = %q, want %q", name, status[name], want)
		}
	}

	// partial.pdf gained a "missing" cell for b.
	for _, f := range files {
		if f.Name != "partial.pdf" {
			continue
		}
		var bState string
		for _, c := range f.PerEnvoy {
			if c.Hostname == "b" {
				bState = c.State
			}
		}
		if bState != "missing" {
			t.Errorf("partial.pdf b-cell = %q, want missing", bState)
		}
	}

	// Three files are live-somewhere (agree, disagree, partial); gone is
	// tombstoned-only. TotalBytes sums the largest live copy of each:
	// 12 + 10 + 10 = 32.
	if fileCount != 3 {
		t.Errorf("fileCount = %d, want 3", fileCount)
	}
	if totalBytes != 32 {
		t.Errorf("totalBytes = %d, want 32", totalBytes)
	}

	sort.Strings(divergences)
	want := []string{"disagree.pdf", "partial.pdf"}
	if len(divergences) != len(want) || divergences[0] != want[0] || divergences[1] != want[1] {
		t.Errorf("divergences = %v, want %v", divergences, want)
	}
}

// FinalizeFiles must sort by name so the UI renders stably.
func TestFinalizeFiles_SortedByName(t *testing.T) {
	files := []FileRow{
		{Name: "z.txt", PerEnvoy: []FileCellPerEnvoy{{Hostname: "a", State: "live", Sha256: "S", Size: 1}}},
		{Name: "a.txt", PerEnvoy: []FileCellPerEnvoy{{Hostname: "a", State: "live", Sha256: "S", Size: 1}}},
	}
	FinalizeFiles(files, []string{"a"})
	if files[0].Name != "a.txt" || files[1].Name != "z.txt" {
		t.Errorf("not sorted by name: %q, %q", files[0].Name, files[1].Name)
	}
}

// FinalizeTombstones sorts rows and their KnownOn lists deterministically.
func TestFinalizeTombstones_Sorted(t *testing.T) {
	rows := MergeTombstone(nil, "z.txt", 100, "plex")
	rows = MergeTombstone(rows, "a.txt", 200, "plex")
	rows = MergeTombstone(rows, "z.txt", 150, "annlap")

	FinalizeTombstones(rows)

	if rows[0].Name != "a.txt" || rows[1].Name != "z.txt" {
		t.Fatalf("rows not name-sorted: %+v", rows)
	}
	// z.txt is on both, KnownOn alpha-sorted, latest deletion time kept.
	z := rows[1]
	if len(z.KnownOn) != 2 || z.KnownOn[0] != "annlap" || z.KnownOn[1] != "plex" {
		t.Errorf("z.txt KnownOn not alpha-sorted: %v", z.KnownOn)
	}
	if z.DeletionTime != 150 {
		t.Errorf("z.txt DeletionTime = %d, want 150 (latest)", z.DeletionTime)
	}
}
