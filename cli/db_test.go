// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import "testing"

func TestSortedTableCounts(t *testing.T) {
	// JSON-decoded numbers arrive as float64 when unmarshalled into
	// `any` — toInt64 handles that, same as the live db-stats path.
	tables := map[string]any{
		"runs":         float64(1250),
		"envoys":       float64(42),
		"envoy_traits": float64(42),
		"audit_log":    float64(9000),
		"empty":        float64(0),
	}
	got := sortedTableCounts(tables)

	// Row count descending; ties broken by name ascending
	// ("envoy_traits" < "envoys").
	wantOrder := []string{"audit_log", "runs", "envoy_traits", "envoys", "empty"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d rows, want %d", len(got), len(wantOrder))
	}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, name)
		}
	}
	if got[0].Rows != 9000 {
		t.Errorf("audit_log rows = %d, want 9000", got[0].Rows)
	}
}

func TestSortedTableCountsEmpty(t *testing.T) {
	if got := sortedTableCounts(map[string]any{}); len(got) != 0 {
		t.Errorf("empty map: got %d rows, want 0", len(got))
	}
}
