// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/testutil"
)

// TestEnvoyListQueriesUseEnrolledAtIndex pins migration 000024: the
// (enrolled_at DESC, id) index must eliminate the ORDER BY sort for every
// enrolled_at-ordered envoy list query. enrolled_at is second-granularity
// (datetime('now')), so a plain (enrolled_at) index would leave a temp B-tree on
// the `, id` tiebreak under mass-enrollment ties; the composite (asserted here
// via EXPLAIN QUERY PLAN) does not. Guards against a future query/index change
// silently reintroducing the full-scan sort.
func TestEnvoyListQueriesUseEnrolledAtIndex(t *testing.T) {
	db := testutil.OpenMigratedTestDB(t)

	cases := []struct {
		name  string
		query string
		args  []any
	}{
		{"ListEnvoys", listEnvoys, nil},
		{"ListEnvoysOffset", listEnvoysOffset, []any{100, 0}},
		{"ListActiveEnvoys", listActiveEnvoys, nil},
		{"ListActiveEnvoysOffset", listActiveEnvoysOffset, []any{100, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			defer rows.Close()
			var plan strings.Builder
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan: %v", err)
				}
				plan.WriteString(detail + "\n")
			}
			if got := plan.String(); strings.Contains(got, "TEMP B-TREE") {
				t.Errorf("%s still sorts — idx_envoys_enrolled_at_id should eliminate it:\n%s", tc.name, got)
			}
			if got := plan.String(); !strings.Contains(got, "idx_envoys_enrolled_at_id") {
				t.Errorf("%s does not use idx_envoys_enrolled_at_id:\n%s", tc.name, got)
			}
		})
	}
}
