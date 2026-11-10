// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"strings"
	"testing"
)

func TestLintFleet_DuplicatePackage(t *testing.T) {
	a := `name: web
resources:
  - name: nginx-pkg
    type: package
    package: nginx
    state: present
`
	b := `name: webserver-base
resources:
  - name: nginx
    type: package
    package: nginx
    state: present
`
	findings := LintFleet([]FleetFile{
		{Path: "configcrates/web/web.vgo", Data: []byte(a)},
		{Path: "configcrates/web/webserver-base.vgo", Data: []byte(b)},
	})
	hit := false
	for _, f := range findings {
		if f.RuleID == "fleet-duplicate-package" {
			hit = true
			if f.Severity != Warning {
				t.Errorf("severity=%q, want warning", f.Severity)
			}
			if !strings.Contains(f.Message, "nginx") {
				t.Errorf("message should mention 'nginx'; got %q", f.Message)
			}
			if !strings.Contains(f.Message, "configcrates/web/web.vgo") {
				t.Errorf("message should reference both module paths; got %q", f.Message)
			}
		}
	}
	if !hit {
		t.Fatalf("expected fleet-duplicate-package finding; got %v", findings)
	}
}

func TestLintFleet_DuplicateFile(t *testing.T) {
	a := `name: nginx
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    content: "from-a"
    owner: root
    group: root
    mode: '0644'
`
	b := `name: nginx-override
resources:
  - name: conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    content: "from-b"
    owner: root
    group: root
    mode: '0644'
`
	findings := LintFleet([]FleetFile{
		{Path: "configcrates/web/nginx.vgo", Data: []byte(a)},
		{Path: "configcrates/web/nginx-override.vgo", Data: []byte(b)},
	})
	hit := false
	for _, f := range findings {
		if f.RuleID == "fleet-duplicate-file" {
			hit = true
			if !strings.Contains(f.Message, "/etc/nginx/nginx.conf") {
				t.Errorf("message should mention target_path; got %q", f.Message)
			}
		}
	}
	if !hit {
		t.Fatalf("expected fleet-duplicate-file finding; got %v", findings)
	}
}

func TestLintFleet_DifferentStateNotConflated(t *testing.T) {
	// Same package name but different state values — these are
	// different keys so this test confirms we don't conflate them
	// into a single dup. (Whether "present in module A, absent in
	// module B" is itself worth flagging is a separate rule for
	// later; for now it's two non-overlapping declarations.)
	a := `name: a
resources:
  - name: pkg
    type: package
    package: nginx
    state: present
`
	b := `name: b
resources:
  - name: pkg
    type: package
    package: nginx
    state: absent
`
	findings := LintFleet([]FleetFile{
		{Path: "configcrates/a.vgo", Data: []byte(a)},
		{Path: "configcrates/b.vgo", Data: []byte(b)},
	})
	for _, f := range findings {
		if f.RuleID == "fleet-duplicate-package" {
			t.Errorf("different state values should not collide: %v", f)
		}
	}
}

func TestLintFleet_SingleFileNoDup(t *testing.T) {
	// A single module is not a fleet-dup. (Within-module dups are
	// duplicate-resource-names' problem.)
	a := `name: a
resources:
  - name: a1
    type: package
    package: nginx
    state: present
  - name: a2
    type: package
    package: nginx
    state: present
`
	findings := LintFleet([]FleetFile{
		{Path: "configcrates/a.vgo", Data: []byte(a)},
	})
	for _, f := range findings {
		if f.RuleID == "fleet-duplicate-package" {
			t.Errorf("single-file dup must not surface as a fleet finding: %v", f)
		}
	}
}

func TestLintFleet_DeterministicOrder(t *testing.T) {
	// Two runs over the same input produce findings in the same
	// order — important for golden tests and for human-readable
	// publish output.
	mk := func() []FleetFile {
		return []FleetFile{
			{Path: "configcrates/a.vgo", Data: []byte(`name: a
resources:
  - name: pkg
    type: package
    package: nginx
    state: present
`)},
			{Path: "configcrates/b.vgo", Data: []byte(`name: b
resources:
  - name: pkg
    type: package
    package: nginx
    state: present
`)},
		}
	}
	a1 := LintFleet(mk())
	a2 := LintFleet(mk())
	if len(a1) != len(a2) {
		t.Fatalf("inconsistent finding count: %d vs %d", len(a1), len(a2))
	}
	for i := range a1 {
		if a1[i].Message != a2[i].Message {
			t.Errorf("non-deterministic order at index %d:\n  a: %s\n  b: %s",
				i, a1[i].Message, a2[i].Message)
		}
	}
}
