// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

func TestLint_ValidModule(t *testing.T) {
	input := `name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: nginx-conf
    type: file
    target_path: /etc/nginx/nginx.conf
    content: "server {}"
    owner: root
    group: root
    mode: "0644"
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [install-nginx, nginx-conf]
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if r.Output == "" {
		t.Fatal("expected non-empty output")
	}
	// Check that #~ comments are present.
	if !strings.Contains(r.Output, "#~ ") {
		t.Error("expected #~ auto-comments in output")
	}
	// Check specific resource comments.
	if !strings.Contains(r.Output, "Install nginx package") {
		t.Error("expected 'Install nginx package' comment")
	}
	if !strings.Contains(r.Output, "Deploy /etc/nginx/nginx.conf") {
		t.Error("expected 'Deploy /etc/nginx/nginx.conf' comment")
	}
	if !strings.Contains(r.Output, "Ensure nginx is running and enabled") {
		t.Error("expected 'Ensure nginx is running and enabled' comment")
	}
}

func TestLint_RepairTabs(t *testing.T) {
	input := "name: test\nresources:\n\t- name: pkg\n\t  type: package\n\t  package: vim\n"
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !containsWarning(r, "tabs to spaces") {
		t.Error("expected warning about tab conversion")
	}
}

func TestLint_RepairBOM(t *testing.T) {
	input := "\xef\xbb\xbfname: test\nresources:\n  - name: pkg\n    type: package\n    package: vim\n"
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors after BOM stripping, got: %v", r.Errors)
	}
}

func TestLint_RepairLeadingNonASCII(t *testing.T) {
	// ● (U+25CF) followed by space then valid YAML — common from terminal copy-paste.
	input := "\xe2\x97\x8f #~ comment\nname: test\nresources:\n  - name: pkg\n    type: package\n    package: vim\n"
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors after stripping leading non-ASCII, got: %v", r.Errors)
	}
	if !containsWarning(r, "non-ASCII") {
		t.Error("expected warning about stripping non-ASCII bytes")
	}
}

func TestLint_RepairUnquotedBooleans(t *testing.T) {
	input := "name: test\nresources:\n  - name: svc\n    type: service\n    service: nginx\n    enabled: yes\n    state: running\n"
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !containsWarning(r, "converted unquoted") {
		t.Error("expected warning about boolean conversion")
	}
}

func TestLint_MissingName(t *testing.T) {
	input := "resources:\n  - name: pkg\n    type: package\n    package: vim\n"
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected errors for missing module name")
	}
	if !containsError(r, "missing required 'name'") {
		t.Error("expected 'missing name' error")
	}
}

func TestLint_MissingResourceType(t *testing.T) {
	input := "name: test\nresources:\n  - name: thing\n    target_path: /tmp/foo\n"
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for missing type")
	}
	if !containsError(r, "missing type") {
		t.Error("expected 'missing type' error")
	}
}

func TestLint_UnknownType(t *testing.T) {
	input := "name: test\nresources:\n  - name: thing\n    type: foobar_widget\n"
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for unknown type")
	}
	if !containsError(r, "unknown type") {
		t.Error("expected 'unknown type' error")
	}
}

func TestLint_FileMissingContent(t *testing.T) {
	input := `name: test
resources:
  - name: my-file
    type: file
    target_path: /etc/foo
    owner: root
    group: root
    mode: "0644"
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for file missing content")
	}
	if !containsError(r, "needs content or source") {
		t.Error("expected 'needs content or source' error")
	}
}

func TestLint_FileMissingRequiredAttrs(t *testing.T) {
	input := `name: test
resources:
  - name: my-file
    type: file
    content: "hello"
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected errors for missing file attributes")
	}
	for _, field := range []string{"target_path", "owner", "group", "mode"} {
		if !containsError(r, field) {
			t.Errorf("expected error about missing %q", field)
		}
	}
}

func TestLint_ExecNoGuard(t *testing.T) {
	input := `name: test
resources:
  - name: run-thing
    type: exec
    command: echo hello
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for exec without guard")
	}
	if !containsError(r, "idempotency guard") {
		t.Error("expected idempotency guard error")
	}
}

func TestLint_ExecWithCreates(t *testing.T) {
	input := `name: test
resources:
  - name: run-thing
    type: exec
    command: echo hello
    creates: /tmp/done
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("exec with creates should pass, got: %v", r.Errors)
	}
}

func TestLint_ServiceRestartedNoSubscribes(t *testing.T) {
	input := `name: test
resources:
  - name: svc
    type: service
    service: nginx
    state: restarted
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for service restarted without subscribes")
	}
	if !containsError(r, "subscribes") {
		t.Error("expected subscribes error")
	}
}

func TestLint_DependsOnUnknown(t *testing.T) {
	input := `name: test
resources:
  - name: pkg
    type: package
    package: vim
    depends_on: [nonexistent]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for unknown depends_on target")
	}
	if !containsError(r, "unknown resource") {
		t.Error("expected unknown resource error")
	}
}

func TestLint_DependsOnSelf(t *testing.T) {
	input := `name: test
resources:
  - name: pkg
    type: package
    package: vim
    depends_on: [pkg]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for self-referencing depends_on")
	}
	if !containsError(r, "references itself") {
		t.Error("expected self-reference error")
	}
}

func TestLint_DependencyCycle(t *testing.T) {
	input := `name: test
resources:
  - name: a
    type: package
    package: a
    depends_on: [b]
  - name: b
    type: package
    package: b
    depends_on: [a]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for dependency cycle")
	}
	if !containsError(r, "cycle") {
		t.Error("expected cycle error")
	}
}

func TestLint_RedundantTrigger(t *testing.T) {
	input := `name: test
resources:
  - name: conf
    type: file
    target_path: /etc/foo
    content: hello
    owner: root
    group: root
    mode: "0644"
    notify: [svc]
  - name: svc
    type: service
    service: nginx
    state: reloaded
    subscribes: [conf]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for redundant trigger")
	}
	if !containsError(r, "redundant trigger") {
		t.Error("expected redundant trigger error")
	}
}

func TestLint_CommunityRejectsSecrets(t *testing.T) {
	input := `name: test
resources:
  - name: my-file
    type: file
    target_path: /etc/foo
    content: "secret:hobnob/db/dsn"
    owner: root
    group: root
    mode: "0644"
`
	r := Lint([]byte(input), Options{CommunityUpload: true})
	if r.OK() {
		t.Fatal("expected error for secret: in community upload")
	}
	if !containsError(r, "secret:") {
		t.Error("expected secret: error")
	}
}

func TestLint_CommunityGPLHeader(t *testing.T) {
	input := `name: test
resources:
  - name: pkg
    type: package
    package: vim
`
	r := Lint([]byte(input), Options{
		CommunityUpload: true,
		Author:          "testuser",
		Version:         "1.0.0",
	})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !strings.Contains(r.Output, "GPL-3.0") {
		t.Error("expected GPL-3.0 header")
	}
	if !strings.Contains(r.Output, "Author: testuser") {
		t.Error("expected author in header")
	}
	if !strings.Contains(r.Output, "Version: 1.0.0") {
		t.Error("expected version in header")
	}
}

func TestLint_AutoCommentsRegenerated(t *testing.T) {
	// Input with existing #~ comments that should be stripped and regenerated.
	input := `#~ Old stale comment
name: test
resources:
  #~ Outdated resource comment
  - name: pkg
    type: package
    package: nginx
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if strings.Contains(r.Output, "Old stale comment") {
		t.Error("#~ comments should be regenerated, not preserved")
	}
	if strings.Contains(r.Output, "Outdated resource comment") {
		t.Error("old #~ resource comment should be stripped")
	}
	if !strings.Contains(r.Output, "Install nginx package") {
		t.Error("expected fresh auto-comment")
	}
}

func TestLint_HandWrittenCommentsStripped(t *testing.T) {
	// Hand-written # comments exist in the input but the renderer
	// rebuilds from the parsed structure, so they won't appear.
	// This is by design: the output is a clean normalized form.
	input := `name: test
resources:
  # This is a hand-written comment that the user wrote
  - name: pkg
    type: package
    package: vim
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	// Hand-written comments are lost during parse→render.
	// This is a known trade-off for community upload normalization.
}

func TestLint_NoResources(t *testing.T) {
	input := "name: empty\n"
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for module with no resources")
	}
	if !containsError(r, "no resources") {
		t.Error("expected 'no resources' error")
	}
}

func TestLint_DuplicateResourceNames(t *testing.T) {
	input := `name: test
resources:
  - name: pkg
    type: package
    package: vim
  - name: pkg
    type: package
    package: curl
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for duplicate resource names")
	}
	if !containsError(r, "duplicate resource name") {
		t.Error("expected duplicate name error")
	}
}

func TestLint_CronMissingSchedule(t *testing.T) {
	input := `name: test
resources:
  - name: backup
    type: cron
    command: /usr/bin/backup.sh
    user: root
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for cron missing schedule")
	}
	if !containsError(r, "schedule") {
		t.Error("expected schedule error")
	}
}

func TestLint_CronMissingUser(t *testing.T) {
	input := `name: test
resources:
  - name: backup
    type: cron
    command: /usr/bin/backup.sh
    schedule: "0 2 * * *"
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for cron missing user")
	}
	if !containsError(r, "user") {
		t.Error("expected user error")
	}
}

func TestLint_RebootNoGuard(t *testing.T) {
	input := `name: test
resources:
  - name: reboot-box
    type: reboot
    message: "rebooting"
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for unguarded reboot")
	}
	if !containsError(r, "subscribes guard") {
		t.Error("expected subscribes guard error")
	}
}

func TestLint_MultilineContent(t *testing.T) {
	input := `name: test
resources:
  - name: my-conf
    type: file
    target_path: /etc/myapp.conf
    content: |
      line1
      line2
      line3
    owner: root
    group: root
    mode: "0644"
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !strings.Contains(r.Output, "line1") {
		t.Error("expected multiline content preserved")
	}
}

func TestLint_VarsPreserved(t *testing.T) {
	input := `name: test
vars:
  port: "8080"
  host: localhost
resources:
  - name: pkg
    type: package
    package: vim
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !strings.Contains(r.Output, "port:") {
		t.Error("expected vars preserved in output")
	}
	if !strings.Contains(r.Output, "host:") {
		t.Error("expected vars preserved in output")
	}
}

func TestLint_CanonicalKeyOrder(t *testing.T) {
	// Attributes should be reordered to canonical order.
	input := `name: test
resources:
  - name: my-file
    type: file
    mode: "0644"
    group: root
    owner: root
    content: hello
    target_path: /etc/foo
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	// target_path should come before content in canonical file order.
	tpIdx := strings.Index(r.Output, "target_path:")
	cIdx := strings.Index(r.Output, "content:")
	if tpIdx > cIdx {
		t.Error("expected target_path before content in canonical order")
	}
}

func TestLint_ModuleComment(t *testing.T) {
	input := `name: webserver
resources:
  - name: pkg
    type: package
    package: nginx
  - name: conf
    type: file
    target_path: /etc/nginx/nginx.conf
    content: "test"
    owner: root
    group: root
    mode: "0644"
  - name: svc
    type: service
    service: nginx
    state: running
    enabled: true
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	// Should have a module-level comment mentioning the resource types.
	if !strings.Contains(r.Output, "#~ Manages") {
		t.Error("expected module-level #~ comment")
	}
}

func TestLint_NotifyUnknownTarget(t *testing.T) {
	input := `name: test
resources:
  - name: conf
    type: file
    target_path: /etc/foo
    content: hello
    owner: root
    group: root
    mode: "0644"
    notify: [ghost]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for notify to unknown target")
	}
	if !containsError(r, "notify references unknown") {
		t.Error("expected notify unknown resource error")
	}
}

func TestLint_BeforeUnknownTarget(t *testing.T) {
	input := `name: test
resources:
  - name: pkg
    type: package
    package: vim
    before: [nobody]
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for before to unknown target")
	}
	if !containsError(r, "before references unknown") {
		t.Error("expected before unknown resource error")
	}
}

// --- helpers ---

func containsWarning(r *Result, substr string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func containsError(r *Result, substr string) bool {
	for _, e := range r.Errors {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
