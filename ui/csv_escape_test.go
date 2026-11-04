// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import "testing"

// TestCSVEscape_NeutralizesFormulas proves envoy-controlled values that a
// spreadsheet would evaluate as formulas are prefixed with a quote, and that
// CR is quoted like LF.
func TestCSVEscape_NeutralizesFormulas(t *testing.T) {
	cases := map[string]string{
		"web01":             "web01",
		"=HYPERLINK(\"x\")": `"'=HYPERLINK(""x"")"`,
		"+1+1":              "'+1+1",
		"-2+3":              "'-2+3",
		"@SUM(A1)":          "'@SUM(A1)",
		"\tx":               "'\tx",
		"a\rb":              "\"a\rb\"",
		"a,b":               `"a,b"`,
		"":                  "",
	}
	for in, want := range cases {
		if got := csvEscape(in); got != want {
			t.Errorf("csvEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
