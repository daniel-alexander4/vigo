// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import "testing"

// TestIsSupersededVigorepo pins the predicate the listing uses to
// decide whether a revoked vigorepo blob should render with the
// Lavender "superseded" pill (publisher rotated in a newer bundle)
// versus the legacy "revoked" treatment (operator revoke or no
// successor). Non-vigorepo labels never supersede regardless of the
// active-label map.
func TestIsSupersededVigorepo(t *testing.T) {
	active := map[string]string{
		"vigorepo:src:dan:vigo": "NEW_SHA",
		"custom-label":          "x",
	}
	cases := []struct {
		name  string
		label string
		sha   string
		want  bool
	}{
		{
			name:  "vigorepo revoked with newer active sha is superseded",
			label: "vigorepo:src:dan:vigo",
			sha:   "OLD_SHA",
			want:  true,
		},
		{
			name:  "vigorepo revoked matches its own sha — not superseded",
			label: "vigorepo:src:dan:vigo",
			sha:   "NEW_SHA",
			want:  false,
		},
		{
			name:  "vigorepo revoked with no active successor — not superseded",
			label: "vigorepo:src:dan:abandoned",
			sha:   "GONE_SHA",
			want:  false,
		},
		{
			name:  "non-vigorepo blob with newer active — still not superseded",
			label: "custom-label",
			sha:   "oldsha",
			want:  false,
		},
		{
			name:  "unlabeled blob — not superseded",
			label: "",
			sha:   "anything",
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isSupersededVigorepo(c.label, c.sha, active)
			if got != c.want {
				t.Errorf("isSupersededVigorepo(%q, %q) = %v, want %v",
					c.label, c.sha, got, c.want)
			}
		})
	}
}
