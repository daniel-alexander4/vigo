// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// FormatSource keeps hand comments and adds/refreshes the `#~` labels.
func TestFormatSource_PreservesHandCommentsAddsLabels(t *testing.T) {
	in := `name: web
resources:
  # deploy the site vhost — hand-written, keep me
  - name: deploy-conf
    type: file
    state: present
    target_path: /etc/nginx/site.conf
    owner: root
    group: root
    mode: "0644"
    content: "server { listen 80; }"
`
	out, r := FormatSource("configcrates/web.vgo", []byte(in), Options{})
	if !r.OK() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	s := string(out)
	if !strings.Contains(s, "# deploy the site vhost — hand-written, keep me") {
		t.Errorf("hand comment was dropped:\n%s", s)
	}
	if !strings.Contains(s, "#~ Manages file resources") {
		t.Errorf("module label missing:\n%s", s)
	}
	if !strings.Contains(s, "  #~ Deploy /etc/nginx/site.conf") {
		t.Errorf("resource label missing:\n%s", s)
	}
}

// A stale `#~` label is refreshed (gains the reversible suffix); the hand
// comment beside it survives.
func TestFormatSource_RefreshesStaleLabel(t *testing.T) {
	in := `#~ Manages timezone resources
name: tz
resources:
  # set it from the fleet var
  #~ Manage set-timezone (timezone)
  - name: set-timezone
    type: timezone
    timezone: "UTC"
`
	out, r := FormatSource("configcrates/tz.vgo", []byte(in), Options{})
	if !r.OK() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	s := string(out)
	if !strings.Contains(s, "# set it from the fleet var") {
		t.Errorf("hand comment dropped:\n%s", s)
	}
	if !strings.Contains(s, "reversible: set revert: true to restore the prior value") {
		t.Errorf("stale label not refreshed with reversible suffix:\n%s", s)
	}
	// Exactly one resource `#~` line (the stale one replaced, not duplicated).
	if n := strings.Count(s, "#~ Manage set-timezone"); n != 1 {
		t.Errorf("expected exactly one set-timezone label, got %d:\n%s", n, s)
	}
}

// Commented-out attributes (`#owner: 0644`) are operator information, not
// disposable comments — preserved both in the block above a resource and
// inside the resource body.
func TestFormatSource_PreservesCommentedOutAttributes(t *testing.T) {
	in := `name: c
resources:
  #owner: 0644
  - name: run-x
    type: exec
    command: "true"
    onlyif: "true"
    #flags: --verbose
`
	out, r := FormatSource("configcrates/c.vgo", []byte(in), Options{})
	if !r.OK() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	s := string(out)
	if !strings.Contains(s, "#owner: 0644") {
		t.Errorf("commented-out attribute above resource dropped:\n%s", s)
	}
	if !strings.Contains(s, "#flags: --verbose") {
		t.Errorf("commented-out attribute in body dropped:\n%s", s)
	}
}

// `#` lines inside a content: block scalar are file DATA and must be untouched
// — no label injected into them, nothing stripped.
func TestFormatSource_LeavesContentBlockUntouched(t *testing.T) {
	in := `name: ssh
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
    owner: root
    group: root
    mode: "0644"
    content: |
      # Authentication
      PermitRootLogin no
      # Logging
      LogLevel VERBOSE
`
	out, r := FormatSource("configcrates/ssh.vgo", []byte(in), Options{})
	if !r.OK() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	s := string(out)
	if !strings.Contains(s, "      # Authentication\n") || !strings.Contains(s, "      # Logging\n") {
		t.Errorf("content-block comments altered:\n%s", s)
	}
	// No reversible/label text leaked into the content body.
	if strings.Contains(s, "PermitRootLogin no\n    #~") {
		t.Errorf("label injected into content body:\n%s", s)
	}
}

// FormatSource is idempotent.
func TestFormatSource_Idempotent(t *testing.T) {
	in := `name: c
resources:
  - name: run-x
    type: exec
    command: "true"
    onlyif: "true"
`
	once, _ := FormatSource("configcrates/c.vgo", []byte(in), Options{})
	twice, _ := FormatSource("configcrates/c.vgo", once, Options{})
	if string(once) != string(twice) {
		t.Errorf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// A resource with no preceding comment gets a fresh `#~` inserted.
func TestFormatSource_InsertsWhenMissing(t *testing.T) {
	in := `name: c
resources:
  - name: run-x
    type: exec
    command: "true"
    onlyif: "true"
`
	out, _ := FormatSource("configcrates/c.vgo", []byte(in), Options{})
	if !strings.Contains(string(out), "  #~ Run run-x — reversible: set revert: true with on_revert: to undo") {
		t.Errorf("label not inserted:\n%s", out)
	}
}

// Invalid files are returned unchanged (never relabel something that fails).
func TestFormatSource_LeavesInvalidUnchanged(t *testing.T) {
	in := `name: c
resources:
  - name: run-x
    type: exec
` // exec with no guard → validation error
	out, r := FormatSource("configcrates/c.vgo", []byte(in), Options{})
	if r.OK() {
		t.Fatalf("expected validation error for guardless exec")
	}
	if string(out) != in {
		t.Errorf("invalid file was modified:\n%s", out)
	}
}

// The StripComments render produces the clean .live form: zero `#~`, content
// preserved.
func TestStripComments_ProducesCleanLiveForm(t *testing.T) {
	in := `name: ssh
resources:
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
    owner: root
    group: root
    mode: "0644"
    content: |
      # Authentication
      PermitRootLogin no
`
	r := Lint([]byte(in), Options{StripComments: true})
	if !r.OK() {
		t.Fatalf("unexpected errors: %v", r.Errors)
	}
	if strings.Contains(r.Output, "#~") {
		t.Errorf(".live form still has #~ labels:\n%s", r.Output)
	}
	if !strings.Contains(r.Output, "# Authentication") {
		t.Errorf("content data lost in stripped form:\n%s", r.Output)
	}
}
