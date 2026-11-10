// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package convergence

import "testing"

func TestActionString(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{ActionCreate, "create"},
		{ActionUpdate, "update"},
		{ActionDelete, "delete"},
		{ActionNoop, "noop"},
	}
	for _, tc := range cases {
		if got := tc.a.String(); got != tc.want {
			t.Errorf("Action(%d).String() = %q, want %q", tc.a, got, tc.want)
		}
	}
}

func TestClassifyAction(t *testing.T) {
	cases := []struct {
		in   string
		want Action
	}{
		{"create", ActionCreate},
		{"  CREATE ", ActionCreate},
		{"created", ActionCreate}, // agent past-tense vocab
		{"Created", ActionCreate},
		{"update", ActionUpdate},
		{"updated", ActionUpdate}, // agent past-tense vocab
		{"delete", ActionDelete},
		{"deleted", ActionDelete}, // agent past-tense vocab
		{"", ActionNoop},
		{"noop", ActionNoop},
		{"settled", ActionNoop},
		{"none", ActionNoop},
		{"skipped", ActionNoop},
		{"blocked", ActionBlocked},
		{" Blocked ", ActionBlocked},
		{"unknown", ActionNoop},
	}
	for _, tc := range cases {
		if got := ClassifyAction(tc.in); got != tc.want {
			t.Errorf("ClassifyAction(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
