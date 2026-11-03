// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import "testing"

// TestLintFile_DispatchAndStateBugs pins the file-kind dispatch shared by
// `vigocli config validate` and the publish path, plus the two regressions
// that motivated routing validate through it: a usercrate without `name:`
// (path-derived IsUsercrate exemption) and a `state: restarted` service with
// bare `when: changed` (the When field reaching validateService).
func TestLintFile_DispatchAndStateBugs(t *testing.T) {
	// Usercrate with no top-level name: clean under a usercrates/ path…
	usercrate := "resources:\n  - name: create-elena\n    type: user\n    state: present\n    username: elena\n"
	if r := LintFile("stacks/usercrates/elena.vgo", []byte(usercrate), Options{}); !r.OK() {
		t.Errorf("usercrate without name under usercrates/ should validate clean, got: %v", r.Errors)
	}
	// …but the SAME content as a configcrate still requires name (fix is exact).
	if r := LintFile("stacks/configcrates/elena.vgo", []byte(usercrate), Options{}); r.OK() {
		t.Error("configcrate without name should still error on missing name")
	}

	// Service: state restarted + bare `when: changed` — must not false-error
	// (When must survive the mapping into validateService).
	svc := "name: cis\nresources:\n  - name: restart-sshd\n    type: service\n    when: changed\n    service: sshd\n    state: restarted\n"
	if r := LintFile("stacks/configcrates/cis.vgo", []byte(svc), Options{}); !r.OK() {
		t.Errorf("restarted service with when: changed should validate clean, got: %v", r.Errors)
	}

	// compliance.vgo is loader-validated, not modlint-validated — skipped here
	// for parity with publish (must not be linted as a configcrate).
	comp := "compliance:\n  hipaa:\n    - \"164.312(a)(1)\"\n"
	if r := LintFile("stacks/compliance.vgo", []byte(comp), Options{}); !r.OK() || len(r.Findings) != 0 {
		t.Errorf("compliance.vgo should be skipped (no findings), got OK=%v findings=%d", r.OK(), len(r.Findings))
	}

	// Basename dispatch: a hostcrate file routes to the hostcrate linter, not
	// the configcrate linter (which would flag "no resources").
	host := "envoys:\n  - match: \"web-*\"\n    roles: [web]\n"
	if r := LintFile("stacks/envoys.vgo", []byte(host), Options{}); !r.OK() {
		t.Errorf("envoys.vgo should route to hostcrate linter and validate clean, got: %v", r.Errors)
	}
}

func TestIsUsercratePath(t *testing.T) {
	cases := map[string]bool{
		"stacks/usercrates/elena.vgo":     true,
		"usercrates/elena.vgo":               true,
		"stacks/usercrates/sub/elena.vgo": false, // not directly in usercrates/
		"stacks/configcrates/nginx.vgo":   false,
		"usercrates":                         false, // leaf, no file
	}
	for path, want := range cases {
		if got := IsUsercratePath(path); got != want {
			t.Errorf("IsUsercratePath(%q) = %v, want %v", path, got, want)
		}
	}
}
