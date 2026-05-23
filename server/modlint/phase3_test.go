// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "testing"

// --- compliance-key retirement (0.66.48) ---
//
// Per-module `compliance:` is retired — the framework-canonicalization
// rule that handled `HIPAA → hipaa` / `PCI DSS → pci-dss` cleanup is
// gone too; operators write `provides:` / `bundle:` instead, both of
// which reference Vigo-curated catalogs that are compile-time
// validated (so framework typos can't reach a module YAML). This
// retired-key rule is the single error path when `compliance:` appears
// in a module.

func TestLint_ComplianceKey_RetiredError(t *testing.T) {
	input := `name: m
compliance:
  hipaa: ["164.312(a)(1)"]
resources:
  - name: vim
    type: package
    package: vim
    state: present
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for retired compliance: key")
	}
	if !containsError(r, "retired") {
		t.Errorf("expected 'retired' in error; got %v", r.Errors)
	}
	if !containsError(r, "provides:") || !containsError(r, "bundle:") {
		t.Errorf("expected provides: / bundle: in did-you-mean message; got %v", r.Errors)
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
