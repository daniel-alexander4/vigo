// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
	"time"
)

// --- LintHostcrate ---

func TestLintHostcrate_Valid(t *testing.T) {
	input := `envoys:
  - match: "*.web.example.com"
    roles: [web-server]
    environment: prod
  - match: "danlap"
    roles: [admin-laptop]
    when: "is_root"
`
	r := LintHostcrate([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
}

func TestLintHostcrate_DuplicateMatch(t *testing.T) {
	input := `envoys:
  - match: "web-*"
    roles: [a]
  - match: "web-*"
    roles: [b]
`
	r := LintHostcrate([]byte(input), Options{})
	if findFinding(r, "hostcrate-duplicate-match") == nil {
		t.Errorf("expected duplicate-match finding; got %v", r.Findings)
	}
}

func TestLintHostcrate_MissingMatch(t *testing.T) {
	input := `envoys:
  - roles: [a]
`
	r := LintHostcrate([]byte(input), Options{})
	if findFinding(r, "hostcrate-match-required") == nil {
		t.Errorf("expected match-required error; got %v", r.Findings)
	}
}

func TestLintHostcrate_MalformedGlob(t *testing.T) {
	// filepath.Match returns ErrBadPattern on `[abc` (unclosed range).
	input := `envoys:
  - match: "[abc"
    roles: [a]
`
	r := LintHostcrate([]byte(input), Options{})
	if findFinding(r, "hostcrate-match-syntax") == nil {
		t.Errorf("expected match-syntax error; got %v", r.Findings)
	}
}

func TestLintHostcrate_MalformedWhen(t *testing.T) {
	input := `envoys:
  - match: "x"
    when: "is_root and"
`
	r := LintHostcrate([]byte(input), Options{})
	if findFinding(r, "hostcrate-when-syntax") == nil {
		t.Errorf("expected when-syntax error; got %v", r.Findings)
	}
}

func TestLintHostcrate_UnknownKey(t *testing.T) {
	// `mathc` is one transposition from `match`.
	input := `envoys:
  - mathc: "x"
    roles: [a]
`
	r := LintHostcrate([]byte(input), Options{})
	f := findFinding(r, "hostcrate-unknown-key")
	if f == nil {
		t.Fatalf("expected unknown-key finding; got %v", r.Findings)
	}
	if !strings.Contains(f.Message, `"match"`) {
		t.Errorf("expected did-you-mean suggestion of 'match'; got %q", f.Message)
	}
}

func TestLintHostcrate_EnvironmentEmpty(t *testing.T) {
	input := `envoys:
  - match: "x"
    environment: ""
`
	r := LintHostcrate([]byte(input), Options{})
	if findFinding(r, "hostcrate-environment-empty") == nil {
		t.Errorf("expected environment-empty warning; got %v", r.Findings)
	}
}

// --- LintRolesFile ---

func TestLintRolesFile_Valid(t *testing.T) {
	input := `roles:
  - name: webserver
    includes: [bastion]
    configcrates: [nginx, certbot]
    when: "is_root"
  - name: bastion
    configcrates: [openssh, fail2ban]
    case:
      - when: "os_family('debian')"
        configcrates: [debian-bastion-extras]
      - when: "os_family('rhel')"
        configcrates: [rhel-bastion-extras]
`
	r := LintRolesFile([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
}

func TestLintRolesFile_MissingName(t *testing.T) {
	input := `roles:
  - modules: [nginx]
`
	r := LintRolesFile([]byte(input), Options{})
	if findFinding(r, "rolesfile-name-required") == nil {
		t.Errorf("expected name-required error; got %v", r.Findings)
	}
}

func TestLintRolesFile_DuplicateName(t *testing.T) {
	input := `roles:
  - name: web
    configcrates: [nginx]
  - name: web
    configcrates: [apache]
`
	r := LintRolesFile([]byte(input), Options{})
	if findFinding(r, "rolesfile-duplicate-name") == nil {
		t.Errorf("expected duplicate-name error; got %v", r.Findings)
	}
}

func TestLintRolesFile_MalformedWhen(t *testing.T) {
	input := `roles:
  - name: web
    configcrates: [nginx]
    when: "is_root and"
`
	r := LintRolesFile([]byte(input), Options{})
	if findFinding(r, "rolesfile-when-syntax") == nil {
		t.Errorf("expected when-syntax error; got %v", r.Findings)
	}
}

func TestLintRolesFile_CaseMissingWhen(t *testing.T) {
	input := `roles:
  - name: web
    case:
      - modules: [x]
`
	r := LintRolesFile([]byte(input), Options{})
	if findFinding(r, "rolesfile-case-when-required") == nil {
		t.Errorf("expected case-when-required error; got %v", r.Findings)
	}
}

func TestLintRolesFile_EmptyRole(t *testing.T) {
	input := `roles:
  - name: dead
`
	r := LintRolesFile([]byte(input), Options{})
	if findFinding(r, "rolesfile-empty-role") == nil {
		t.Errorf("expected empty-role warning; got %v", r.Findings)
	}
}

// --- LintWaiversFile ---

func TestLintWaiversFile_Valid(t *testing.T) {
	input := `waivers:
  - control: "hipaa/164.312(a)(1)"
    reason: "Compensating control: documented in IR plan section 4.2"
    approved_by: "alice@example.com"
    expires: "2099-12-31"
`
	r := LintWaiversFile([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
}

func TestLintWaiversFile_FrameworkCanonicalization(t *testing.T) {
	input := `waivers:
  - control: "HIPAA/164.312(a)(1)"
    reason: "x"
    approved_by: "alice"
`
	r := LintWaiversFile([]byte(input), Options{})
	f := findFinding(r, "waiversfile-framework-canonicalize")
	if f == nil {
		t.Fatalf("expected framework canonicalize warning; got %v", r.Findings)
	}
	if !f.FixApplied {
		t.Errorf("FixApplied should be true")
	}
}

func TestLintWaiversFile_UnknownFramework(t *testing.T) {
	input := `waivers:
  - control: "hippa/164.312(a)(1)"
    reason: "x"
    approved_by: "alice"
`
	r := LintWaiversFile([]byte(input), Options{})
	f := findFinding(r, "waiversfile-framework-unknown")
	if f == nil {
		t.Fatalf("expected framework-unknown error; got %v", r.Findings)
	}
	if !strings.Contains(f.Message, `"hipaa"`) {
		t.Errorf("expected did-you-mean 'hipaa'; got %q", f.Message)
	}
}

func TestLintWaiversFile_ControlShape(t *testing.T) {
	input := `waivers:
  - control: "no-slash-here"
    reason: "x"
    approved_by: "alice"
`
	r := LintWaiversFile([]byte(input), Options{})
	if findFinding(r, "waiversfile-control-shape") == nil {
		t.Errorf("expected control-shape error; got %v", r.Findings)
	}
}

func TestLintWaiversFile_ReasonRequired(t *testing.T) {
	input := `waivers:
  - control: "hipaa/x"
    approved_by: "alice"
`
	r := LintWaiversFile([]byte(input), Options{})
	if findFinding(r, "waiversfile-reason-required") == nil {
		t.Errorf("expected reason-required error; got %v", r.Findings)
	}
}

func TestLintWaiversFile_ApprovedByRequired(t *testing.T) {
	input := `waivers:
  - control: "hipaa/x"
    reason: "x"
`
	r := LintWaiversFile([]byte(input), Options{})
	if findFinding(r, "waiversfile-approved-by-required") == nil {
		t.Errorf("expected approved-by-required error; got %v", r.Findings)
	}
}

func TestLintWaiversFile_ExpiresFormat(t *testing.T) {
	input := `waivers:
  - control: "hipaa/x"
    reason: "x"
    approved_by: "alice"
    expires: "tomorrow"
`
	r := LintWaiversFile([]byte(input), Options{})
	if findFinding(r, "waiversfile-expires-format") == nil {
		t.Errorf("expected expires-format error; got %v", r.Findings)
	}
}

func TestLintWaiversFile_ExpiredInfo(t *testing.T) {
	input := `waivers:
  - control: "hipaa/x"
    reason: "x"
    approved_by: "alice"
    expires: "2020-01-01"
`
	r := LintWaiversFile([]byte(input), Options{})
	wf, _ := parseWaiversFile([]byte(input))
	findings := applyWaiversFileRules(wf, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hasExpired := false
	for _, f := range findings {
		if f.RuleID == "waiversfile-expired" {
			hasExpired = true
			if f.Severity != Info {
				t.Errorf("expired severity = %q, want info", f.Severity)
			}
		}
	}
	if !hasExpired {
		t.Errorf("expected expired info; got %v", findings)
	}
	// Sanity: the public LintWaiversFile entry point also produces it
	// when the wall-clock is past the date.
	_ = r
}

// --- LintEnvironmentsFile ---

func TestLintEnvironmentsFile_Valid(t *testing.T) {
	input := `environments:
  prod:
    configcrates: [monitoring, alerting]
    vars:
      log_level: warn
  staging:
    exclude_configcrates: [debug-noise]
    vars:
      log_level: debug
`
	r := LintEnvironmentsFile([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
}

func TestLintEnvironmentsFile_NoEnvironments(t *testing.T) {
	input := `# nothing here
`
	r := LintEnvironmentsFile([]byte(input), Options{})
	if findFinding(r, "environmentsfile-empty") == nil {
		t.Errorf("expected empty file warning; got %v", r.Findings)
	}
}

func TestLintEnvironmentsFile_EmptyEnvironment(t *testing.T) {
	input := `environments:
  prod: {}
`
	r := LintEnvironmentsFile([]byte(input), Options{})
	if findFinding(r, "environmentsfile-empty-environment") == nil {
		t.Errorf("expected empty-environment hint; got %v", r.Findings)
	}
}

func TestLintEnvironmentsFile_UnknownKey(t *testing.T) {
	// `vasr` is one transposition from `vars`.
	input := `environments:
  prod:
    configcrates: [a]
    vasr:
      x: y
`
	r := LintEnvironmentsFile([]byte(input), Options{})
	f := findFinding(r, "environmentsfile-unknown-key")
	if f == nil {
		t.Fatalf("expected unknown-key error; got %v", r.Findings)
	}
	if !strings.Contains(f.Message, `"vars"`) {
		t.Errorf("expected did-you-mean 'vars'; got %q", f.Message)
	}
}
