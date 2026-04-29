// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// --- compliance-framework canonicalization ---

func TestLint_ComplianceFramework_CaseAutoFix(t *testing.T) {
	input := `name: m
compliance:
  HIPAA: ["164.312(a)(1)"]
resources:
  - name: vim
    type: package
    package: vim
    state: present
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	f := findFinding(r, "compliance-framework")
	if f == nil {
		t.Fatalf("expected compliance-framework finding, got %v", r.Findings)
	}
	if f.Severity != Warning || !f.FixApplied {
		t.Errorf("severity=%q fix=%v, want warning + fix", f.Severity, f.FixApplied)
	}
	if !strings.Contains(r.Output, "hipaa:") {
		t.Errorf("output should contain canonical 'hipaa:'; got:\n%s", r.Output)
	}
	if strings.Contains(r.Output, "HIPAA:") {
		t.Errorf("output should NOT contain uppercase 'HIPAA:'; got:\n%s", r.Output)
	}
}

func TestLint_ComplianceFramework_SpaceAndDashAutoFix(t *testing.T) {
	// Operator types "PCI DSS" or "PCI-DSS" or "pci dss" — all should
	// canonicalize to "pci-dss".
	cases := []string{"PCI DSS", "pci dss", "PCI-DSS", "Pci_Dss"}
	for _, original := range cases {
		input := `name: m
compliance:
  ` + original + `: ["1.1"]
resources:
  - name: vim
    type: package
    package: vim
    state: present
`
		// The YAML parser is happy with unquoted keys like "PCI DSS"
		// when written as a flow scalar in block-mapping position.
		// Dropping it as-is mirrors how operators actually type it.
		r := Lint([]byte(input), Options{})
		if !r.OK() {
			t.Errorf("input %q: expected no errors, got: %v", original, r.Errors)
			continue
		}
		if !strings.Contains(r.Output, "pci-dss:") {
			t.Errorf("input %q: output should canonicalize to 'pci-dss:'; got:\n%s", original, r.Output)
		}
	}
}

func TestLint_ComplianceFramework_UnknownWithSuggestion(t *testing.T) {
	// "hippa" is one edit from "hipaa".
	input := `name: m
compliance:
  hippa: ["164.312(a)(1)"]
resources:
  - name: vim
    type: package
    package: vim
    state: present
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for unknown framework")
	}
	if !containsError(r, `did you mean "hipaa"`) {
		t.Errorf("expected did-you-mean suggestion; got %v", r.Errors)
	}
}

func TestLint_ComplianceFramework_UnknownNoSuggestion(t *testing.T) {
	// Way too far from any known framework.
	input := `name: m
compliance:
  flarbazoid: ["1.1"]
resources:
  - name: vim
    type: package
    package: vim
    state: present
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for unknown framework")
	}
	if !containsError(r, "must be one of") {
		t.Errorf("expected fallback list of canonical names; got %v", r.Errors)
	}
}

// --- community-paths ---

func TestLint_CommunityPaths_KernelStateRejected(t *testing.T) {
	for _, bad := range []string{"/proc/sys/foo", "/sys/class/net", "/dev/null"} {
		input := `name: m
resources:
  - name: peek
    type: file
    target_path: ` + bad + `
    content: "x"
    owner: root
    group: root
    mode: '0644'
`
		r := Lint([]byte(input), Options{CommunityUpload: true})
		if r.OK() {
			t.Errorf("path %q: expected community-paths error in CommunityUpload mode", bad)
			continue
		}
		if !containsError(r, "kernel/runtime state path") {
			t.Errorf("path %q: expected 'kernel/runtime state path' in error; got %v", bad, r.Errors)
		}
	}
}

func TestLint_CommunityPaths_PerHostRejected(t *testing.T) {
	for _, bad := range []string{"/root/.ssh/authorized_keys", "/home/dan/.bashrc", "/boot/grub/grub.cfg"} {
		input := `name: m
resources:
  - name: peek
    type: file
    target_path: ` + bad + `
    content: "x"
    owner: root
    group: root
    mode: '0644'
`
		r := Lint([]byte(input), Options{CommunityUpload: true})
		if r.OK() {
			t.Errorf("path %q: expected community-paths error in CommunityUpload mode", bad)
			continue
		}
		if !containsError(r, "per-host territory") {
			t.Errorf("path %q: expected 'per-host territory' in error; got %v", bad, r.Errors)
		}
	}
}

func TestLint_CommunityPaths_AllowlistOK(t *testing.T) {
	for _, ok := range []string{"/etc/foo.conf", "/usr/local/bin/x", "/var/lib/y", "/opt/app/config", "/var/log/app.log"} {
		input := `name: m
resources:
  - name: f
    type: file
    target_path: ` + ok + `
    content: "x"
    owner: root
    group: root
    mode: '0644'
`
		r := Lint([]byte(input), Options{CommunityUpload: true})
		if findFinding(r, "community-paths") != nil {
			t.Errorf("path %q: should be allowed in community mode", ok)
		}
	}
}

func TestLint_CommunityPaths_NotEnforcedInFleetMode(t *testing.T) {
	// Same path, fleet mode: community-paths must not fire.
	input := `name: m
resources:
  - name: peek
    type: file
    target_path: /proc/sys/foo
    content: "x"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{CommunityUpload: false})
	if findFinding(r, "community-paths") != nil {
		t.Error("community-paths must only fire when CommunityUpload=true")
	}
}
