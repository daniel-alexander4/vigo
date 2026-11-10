// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package publish

import (
	"testing"

	"git.alexander4.io/vigo/server/validate"
)

// TestRetractGeneratorMatchesStateSoT pins the .retract generator's per-type
// state transform to the shared reversal SoT (validate.RetractedStateAttrs).
// If the two drift — e.g. someone changes how services retract in one place —
// this fails. Covers only the state-flipping types; source_package (→ file
// delete) and exec (→ command) retract by a different mechanism by design.
func TestRetractGeneratorMatchesStateSoT(t *testing.T) {
	cases := []retractResource{
		{Name: "f", Type: "file", Attrs: map[string]any{"target_path": "/etc/x"}},
		{Name: "p", Type: "package", Attrs: map[string]any{"package": "nginx"}},
		{Name: "s", Type: "service", Attrs: map[string]any{"service": "nginx"}},
		{Name: "u", Type: "user", Attrs: map[string]any{"username": "bob"}},
		{Name: "c", Type: "cron", Attrs: map[string]any{}},
		{Name: "r", Type: "repository", Attrs: map[string]any{"repository": "deb http://x main"}},
		{Name: "y", Type: "sysctl", Attrs: map[string]any{"key": "net.ipv4.ip_forward"}},
		{Name: "w", Type: "firewall", Attrs: map[string]any{"port": "22"}},
	}
	for _, c := range cases {
		want, ok := validate.RetractedStateAttrs(c.Type)
		if !ok {
			t.Errorf("%s: SoT returned ok=false — conformance list is out of sync with RetractedStateAttrs", c.Type)
			continue
		}
		got, warn := reverseResource(c)
		if got == nil {
			t.Errorf("%s: generator returned nil (warn=%v)", c.Type, warn)
			continue
		}
		for k, v := range want {
			if got.Attrs[k] != v {
				t.Errorf("%s: generator %s=%v, SoT %s=%v — drift", c.Type, k, got.Attrs[k], k, v)
			}
		}
	}
}
