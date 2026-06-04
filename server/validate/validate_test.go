// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package validate

import (
	"strings"
	"testing"
)

func TestExecWithoutGuard_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{"command": "longdrawer hello"}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for exec without guard")
	}
	if findings[0].Message != "exec resource has no idempotency guard — add onlyif or unless" {
		t.Errorf("unexpected message: %s", findings[0].Message)
	}
}

func TestExecWithOnlyIf_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{
				"command": "apt-get update",
				"onlyif":  "test -f /var/run/reboot-required",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestExecWithUnless_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{
				"command": "curl -fsSL https://example.com/key | gpg --dearmor -o /etc/apt/key.gpg",
				"unless":  "test -f /etc/apt/key.gpg",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestServiceRestarted_WithoutSubscribes_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"service": "nginx",
				"state":   "restarted",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("restarted service without subscribes should be an error")
	}
}

func TestServiceRestarted_WithSubscribes_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "nginx-conf", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/nginx/nginx.conf", "content": "x", "owner": "root", "group": "root", "mode": "0644", "state": "present",
			}},
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"service": "nginx",
				"state":   "restarted",
			}, Subscribes: []string{"nginx-conf"}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if len(findings) > 0 {
		t.Errorf("expected no findings, got: %s", FormatFindings(findings))
	}
}

func TestServiceRunning_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"service": "nginx",
				"state":   "running",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if len(findings) > 0 {
		t.Errorf("expected no findings, got: %s", FormatFindings(findings))
	}
}

func TestInherentlyIdempotentTypes_AreClean(t *testing.T) {
	types := []struct {
		typ   string
		attrs map[string]any
	}{
		{"file", map[string]any{"target_path": "/etc/foo", "content": "bar", "owner": "root", "group": "root", "mode": "0644", "state": "present"}},
		{"package", map[string]any{"package": "nginx", "state": "present"}},
		{"user", map[string]any{"username": "deploy", "state": "present"}},
		{"repository", map[string]any{"repo": "ppa:nginx/stable", "state": "present"}},
		{"source_package", map[string]any{"url": "https://example.com/foo.tar.gz", "target_path": "/opt/foo", "state": "present"}},
		{"cron", map[string]any{"command": "/usr/local/bin/backup.sh", "schedule": "0 2 * * *", "user": "deploy", "state": "present"}},
	}

	for _, tt := range types {
		mod := Configcrate{
			Name: "test-mod",
			Resources: []Resource{
				{Name: "test-" + tt.typ, Type: tt.typ, Attributes: tt.attrs},
			},
		}
		findings := ValidateConfigcrate(mod)
		if HasErrors(findings) {
			t.Errorf("type %q should be inherently idempotent, got: %s", tt.typ, FormatFindings(findings))
		}
	}
}

func TestPackageOptionalParams_AreClean(t *testing.T) {
	// The generic package executor (agent/src/runner/mod.rs) reads these
	// params, but the schema generator can't scrape mod.rs, so they live in
	// the hand-maintained Supplemental list. Guard that a package resource
	// setting all of them passes without an unknown-attribute error.
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "nginx", Type: "package", Attributes: map[string]any{
				"package":     "nginx",
				"state":       "present",
				"flags":       "--no-install-recommends",
				"timeout":     "120",
				"retries":     "3",
				"retry_delay": "5",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("package with flags/timeout/retries/retry_delay should be clean, got: %s", FormatFindings(findings))
	}
}

func TestFileValidation_MissingRequiredFields(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bare-file", Type: "file", Attributes: map[string]any{}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for bare file resource")
	}
	// Should require: target_path, owner, group, mode, state, content/source = 6 errors.
	errorCount := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			errorCount++
		}
	}
	if errorCount != 6 {
		t.Errorf("expected 6 errors, got %d: %s", errorCount, FormatFindings(findings))
	}
}

func TestFileValidation_AbsentSkipsContent(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-file", Type: "file", Attributes: map[string]any{
				"target_path": "/tmp/gone", "owner": "root", "group": "root", "mode": "0644", "state": "absent",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("state=absent should not require content, got: %s", FormatFindings(findings))
	}
}

func TestFileValidation_SourceSatisfiesContent(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "tmpl-file", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/app.conf", "source": "templates/app.conf.tmpl",
				"owner": "root", "group": "root", "mode": "0600", "state": "present",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("source should satisfy content requirement, got: %s", FormatFindings(findings))
	}
}

func TestUnknownType_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "wat", Type: "magic", Attributes: map[string]any{}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("unknown type without custom: true should be an error")
	}
}

func TestUnknownType_WithSkipValidation_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "deploy", Type: "capistrano", Attributes: map[string]any{
				"skip_validation": true,
				"command":         "cap deploy",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("unknown type with skip_validation: true should pass, got: %s", FormatFindings(findings))
	}
}

func TestProcessAndFileLineTypes_AreClean(t *testing.T) {
	fixtures := map[string]map[string]any{
		"process":   {"process": "nginx", "state": "running"},
		"file_line": {"target_path": "/etc/hosts", "line": "127.0.0.1 myhost", "state": "present"},
	}
	for typ, attrs := range fixtures {
		mod := Configcrate{
			Name: "test-mod",
			Resources: []Resource{
				{Name: "test-" + typ, Type: typ, Attributes: attrs},
			},
		}
		findings := ValidateConfigcrate(mod)
		if HasErrors(findings) {
			t.Errorf("type %q should be known, got: %s", typ, FormatFindings(findings))
		}
	}
}

func TestMissingType_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "oops", Attributes: map[string]any{}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for missing type")
	}
}

func TestMissingName_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Type: "file", Attributes: map[string]any{"target_path": "/etc/foo"}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for missing name")
	}
}

func TestMultipleResources_AllValidated(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-file", Type: "file", Attributes: map[string]any{"target_path": "/etc/foo", "content": "bar", "owner": "root", "group": "root", "mode": "0644", "state": "present"}},
			{Name: "bad-exec", Type: "exec", Attributes: map[string]any{"command": "longdrawer hello"}},
			{Name: "good-exec", Type: "exec", Attributes: map[string]any{"command": "longdrawer world", "unless": "test -f /tmp/done"}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for bad-exec")
	}

	errorCount := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			errorCount++
		}
	}
	if errorCount != 1 {
		t.Errorf("expected 1 error, got %d: %s", errorCount, FormatFindings(findings))
	}
}

func TestExecWithWatchSecret_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{
				Name:        "set-password",
				Type:        "exec",
				Attributes:  map[string]any{"command": "grafana-cli admin reset-admin-password 'secret'"},
				WatchSecret: []string{"vigo/grafana/admin-password"},
			},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("exec with watch_secret should pass validation, got: %s", FormatFindings(findings))
	}
}

func TestCronValidation_MissingScheduleAndUser(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-cron", Type: "cron", Attributes: map[string]any{
				"command": "/usr/local/bin/backup.sh",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for cron without schedule and user")
	}
}

func TestCronValidation_WithSchedule_MissingUser(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "no-user-cron", Type: "cron", Attributes: map[string]any{
				"command":  "/usr/local/bin/backup.sh",
				"schedule": "0 2 * * *",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for cron without user")
	}
}

func TestCronValidation_IndividualFields_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "fields-cron", Type: "cron", Attributes: map[string]any{
				"command": "/usr/local/bin/backup.sh",
				"user":    "deploy",
				"state":   "present",
				"minute":  "0",
				"hour":    "2",
				"day":     "*",
				"month":   "*",
				"weekday": "*",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("cron with all individual fields + user should pass, got: %s", FormatFindings(findings))
	}
}

func TestCronValidation_PartialFields_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "partial-cron", Type: "cron", Attributes: map[string]any{
				"command": "/usr/local/bin/backup.sh",
				"user":    "deploy",
				"minute":  "0",
				"hour":    "2",
				// day, month, weekday omitted — would default to *
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for cron with only some individual fields")
	}
}

func TestCronValidation_Absent_SkipsChecks(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-cron", Type: "cron", Attributes: map[string]any{
				"command": "/usr/local/bin/backup.sh",
				"user":    "deploy",
				"state":   "absent",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("state=absent should skip schedule/user checks, got: %s", FormatFindings(findings))
	}
}

// ── Replace validation tests ─────────────────────────────────────

func TestReplaceValidation_MissingPath(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-replace", Type: "replace", Attributes: map[string]any{
				"match_pattern": "foo", "replacement": "bar",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for replace without path")
	}
}

func TestReplaceValidation_MissingMatch(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-replace", Type: "replace", Attributes: map[string]any{
				"target_path": "/etc/foo", "replacement": "bar",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for replace without match")
	}
}

func TestReplaceValidation_ValidConfig(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-replace", Type: "replace", Attributes: map[string]any{
				"target_path": "/etc/ssh/sshd_config", "match_pattern": "^PermitRootLogin", "replacement": "PermitRootLogin no",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

// ── FieldEdit validation tests ───────────────────────────────────

func TestFieldEditValidation_MissingRequired(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-fe", Type: "field_edit", Attributes: map[string]any{}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for field_edit without required fields")
	}
	// Should require: path, line_match, select_field, state, field_value = 5 errors.
	errorCount := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			errorCount++
		}
	}
	if errorCount != 5 {
		t.Errorf("expected 5 errors, got %d: %s", errorCount, FormatFindings(findings))
	}
}

func TestFieldEditValidation_MissingFieldValue(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "no-value-fe", Type: "field_edit", Attributes: map[string]any{
				"path": "/etc/group", "line_match": "^docker:", "select_field": "4",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for field_edit without field_value")
	}
}

func TestFieldEditValidation_ValidConfig(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-fe", Type: "field_edit", Attributes: map[string]any{
				"path": "/etc/group", "line_match": "^docker:", "select_field": "4",
				"field_value": "deploy", "field_operation": "append", "state": "present",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestFileWithoutContent_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "config", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/myapp/config",
				"owner":       "root",
				"group":       "root",
				"mode":        "0644",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file without content")
	}
}

func TestDirectoryType_NoContentRequired(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "datadir", Type: "directory", Attributes: map[string]any{
				"target_path": "/var/lib/myapp/data",
				"owner":       "root",
				"group":       "root",
				"mode":        "0755",
				"state":       "present",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for directory, got: %s", FormatFindings(findings))
	}
}

func TestDirectoryType_MissingTargetpath(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "baddir", Type: "directory", Attributes: map[string]any{
				"owner": "root",
				"mode":  "0755",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for directory without target_path")
	}
}

func TestSymlinkType_Valid(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "enable-site", Type: "symlink", Attributes: map[string]any{
				"target_path": "/etc/nginx/sites-enabled/mysite",
				"to":          "../sites-available/mysite",
				"state":       "present",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for valid symlink, got: %s", FormatFindings(findings))
	}
}

func TestSymlinkType_MissingTo(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-link", Type: "symlink", Attributes: map[string]any{
				"target_path": "/some/link",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for symlink without to")
	}
}

func TestSymlinkType_AbsentSkipsTo(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-link", Type: "symlink", Attributes: map[string]any{
				"target_path": "/some/link",
				"state":       "absent",
			}},
		},
	}

	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for absent symlink without to, got: %s", FormatFindings(findings))
	}
}

func TestFindingString_Format(t *testing.T) {
	f := Finding{
		Severity:     SeverityError,
		Configcrate:  "nginx",
		ResourceName: "run-setup",
		ResourceType: "exec",
		Message:      "exec resource has no idempotency guard — add creates, onlyif, or unless",
	}
	expected := "[ERROR] nginx/run-setup: exec resource has no idempotency guard — add creates, onlyif, or unless"
	if f.String() != expected {
		t.Errorf("got: %s\nwant: %s", f.String(), expected)
	}
}

// ── Cross-reference validation tests ────────────────────────────

func TestDependsOn_UnknownResource_IsWarning(t *testing.T) {
	// Unknown depends_on targets are Warning (not Error) because single-file
	// lint can't see resources defined in sibling modules. Whole-stacks
	// resolution happens at publish time. Self-references are still Error
	// (covered by other tests).
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				DependsOn: []string{"nonexistent-pkg"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Fatalf("expected only warnings for cross-module reference, got errors: %s", FormatFindings(findings))
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityWarning && strings.Contains(f.Message, `depends_on references unknown resource "nonexistent-pkg"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected depends_on warning, got: %s", FormatFindings(findings))
	}
}

func TestDependsOn_ValidReference_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "pkg", Type: "package", Attributes: map[string]any{"package": "nginx", "state": "present"}},
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				DependsOn: []string{"pkg"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("valid depends_on should pass, got: %s", FormatFindings(findings))
	}
}

func TestDependsOn_SelfReference_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				DependsOn: []string{"svc"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	found := false
	for _, f := range findings {
		if f.Severity == SeverityError && f.Message == "depends_on references itself" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected self-reference error, got: %s", FormatFindings(findings))
	}
}

func TestNotify_UnknownResource_IsWarning(t *testing.T) {
	// Cross-module references are the dominant pattern (vhost → nginx's
	// reload-nginx); single-file validation can't tell a typo from a
	// cross-module hop. Whole-stacks resolution happens at publish.
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "conf", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/app.conf", "content": "x", "owner": "root", "group": "root", "mode": "0644", "state": "present",
			}, Notify: []string{"nonexistent-svc"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Fatalf("expected only warnings, got errors: %s", FormatFindings(findings))
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityWarning && strings.Contains(f.Message, "notify references unknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected notify warning, got: %s", FormatFindings(findings))
	}
}

func TestSubscribes_UnknownResource_IsWarning(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				Subscribes: []string{"nonexistent-conf"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Fatalf("expected only warnings, got errors: %s", FormatFindings(findings))
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityWarning && strings.Contains(f.Message, "subscribes references unknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected subscribes warning, got: %s", FormatFindings(findings))
	}
}

func TestBefore_UnknownResource_IsWarning(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "pkg", Type: "package", Attributes: map[string]any{"package": "nginx", "state": "present"},
				Before: []string{"nonexistent-svc"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Fatalf("expected only warnings, got errors: %s", FormatFindings(findings))
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityWarning && strings.Contains(f.Message, "before references unknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected before warning, got: %s", FormatFindings(findings))
	}
}

func TestRedundantNotifySubscribes_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "conf", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/app.conf", "content": "x", "owner": "root", "group": "root", "mode": "0644", "state": "present",
			}, Notify: []string{"svc"}},
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				Subscribes: []string{"conf"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("redundant trigger should be an error")
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityError && f.ResourceName == "svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected redundant trigger error, got: %s", FormatFindings(findings))
	}
}

func TestFileStateDirectory_IsError(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "mydir", Type: "file", Attributes: map[string]any{
				"target_path": "/srv/data", "owner": "root", "group": "root", "mode": "0755", "state": "directory",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("file with state: directory should be an error")
	}
}

func TestAllReferencesValid_IsClean(t *testing.T) {
	mod := Configcrate{
		Name: "nginx",
		Resources: []Resource{
			{Name: "pkg", Type: "package", Attributes: map[string]any{"package": "nginx", "state": "present"}},
			{Name: "conf", Type: "file", Attributes: map[string]any{
				"target_path": "/etc/nginx/nginx.conf", "content": "x", "owner": "root", "group": "root", "mode": "0644", "state": "present",
			}, DependsOn: []string{"pkg"}, Notify: []string{"svc"}},
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"},
				DependsOn: []string{"pkg"}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("all references valid, expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestAllKnownTypesHaveSchema(t *testing.T) {
	for typeName := range KnownTypes {
		if _, ok := TypeSchemas[typeName]; !ok {
			t.Errorf("KnownTypes has %q but TypeSchemas does not — add a schema entry", typeName)
		}
	}
}

// TestStateMandatoryForStateTypes pins the mandatory-state rule: types whose
// executor consumes `state` (generatedStateTypes) must declare it, so the
// present→absent retraction lever stays visible; types that ignore state must
// not require it. Guards both the rule and the generator's false-positive
// exclusion (ssh_exec / *_interface appear in generatedSchemas via the shared
// network_common helper but never read state themselves).
func TestStateMandatoryForStateTypes(t *testing.T) {
	hasStateErr := func(findings []Finding) bool {
		for _, f := range findings {
			if f.Severity == SeverityError && strings.Contains(f.Message, `missing required attribute "state"`) {
				return true
			}
		}
		return false
	}
	check := func(typ string, attrs map[string]any) []Finding {
		return ValidateConfigcrate(Configcrate{Name: "m", Resources: []Resource{{Name: "r", Type: typ, Attributes: attrs}}})
	}
	fileAttrs := func() map[string]any {
		return map[string]any{"target_path": "/etc/x", "content": "y", "owner": "root", "group": "root", "mode": "0644"}
	}

	// State-consuming type without state → error; with state → no state error.
	if !hasStateErr(check("file", fileAttrs())) {
		t.Error("file without state should error on missing state")
	}
	withState := fileAttrs()
	withState["state"] = "present"
	if hasStateErr(check("file", withState)) {
		t.Error("file with state should not error on state")
	}

	// Exempt action type (exec) must not require state.
	if hasStateErr(check("exec", map[string]any{"command": "echo hi", "unless": "false"})) {
		t.Error("exec should not require state")
	}

	// Schema false-positives: present in generatedSchemas (via network_common)
	// but never consume state — must be excluded from the mandatory set.
	for _, typ := range []string{"ssh_exec", "cisco_interface", "junos_interface"} {
		if generatedStateTypes[typ] {
			t.Errorf("%s is a schema false-positive and must not be in generatedStateTypes", typ)
		}
	}
}

// TestStateValuesKeysAreStateTypes is the parity guard: every type with a
// non-default state vocabulary must actually consume state. Catches a typo'd
// or stale key in stateValues (the value sets themselves are hand-derived from
// the executors and not auto-verified — see the value-drift pending item).
func TestStateValuesKeysAreStateTypes(t *testing.T) {
	for typ := range stateValues {
		if !generatedStateTypes[typ] {
			t.Errorf("stateValues key %q is not a state-consuming type (generatedStateTypes) — typo, or the type no longer reads state", typ)
		}
	}
}

func TestStateValueValidation(t *testing.T) {
	hasInvalidStateErr := func(findings []Finding) bool {
		for _, f := range findings {
			if f.Severity == SeverityError && strings.Contains(f.Message, "invalid state") {
				return true
			}
		}
		return false
	}
	check := func(typ, state string, extra map[string]any) []Finding {
		attrs := map[string]any{"state": state}
		for k, v := range extra {
			attrs[k] = v
		}
		return ValidateConfigcrate(Configcrate{Name: "m", Resources: []Resource{{Name: "r", Type: typ, Attributes: attrs}}})
	}
	fileExtra := map[string]any{"target_path": "/etc/x", "content": "y", "owner": "root", "group": "root", "mode": "0644"}

	cases := []struct {
		typ, state string
		extra      map[string]any
		wantErr    bool
	}{
		// Default present|absent types.
		{"file", "absent", fileExtra, false},
		{"file", "present", fileExtra, false},
		{"file", "stopped", fileExtra, true}, // wrong-vocab flip
		{"file", "absnt", fileExtra, true},   // typo
		// Templated state resolves at apply time — never rejected at publish.
		{"package", "{{.Vars.pkg_state}}", map[string]any{"package": "nginx"}, false},
		// Non-default unions.
		{"service", "running", map[string]any{"service": "nginx"}, false},
		{"service", "maintenance", map[string]any{"service": "nginx"}, false}, // illumos-only
		{"service", "present", map[string]any{"service": "nginx"}, true},      // not a service state
		{"mount", "mounted", map[string]any{"path": "/mnt", "device": "/dev/sdb", "fstype": "ext4"}, false},
		{"mount", "unmounted", map[string]any{"path": "/mnt", "device": "/dev/sdb", "fstype": "ext4"}, false},
		{"swap", "on", map[string]any{"path": "/swap"}, false}, // BSD alias
		{"kernel_module", "loaded", map[string]any{"module": "br_netfilter"}, false},
		{"package", "latest", map[string]any{"package": "nginx"}, false},
		{"vm", "defined", map[string]any{"vm": "web", "image": "x"}, false},
		{"vm", "present", map[string]any{"vm": "web", "image": "x"}, true}, // vm has no present
	}
	for _, c := range cases {
		if got := hasInvalidStateErr(check(c.typ, c.state, c.extra)); got != c.wantErr {
			t.Errorf("%s state=%q: invalid-state error = %v, want %v", c.typ, c.state, got, c.wantErr)
		}
	}
}

func TestSchemaRejectsUnknownAttribute(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-backup", Type: "db_backup", Attributes: map[string]any{
				"db_type": "sqlite", "db_name": "/tmp/test.db", "target_path": "/tmp/backups",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for unknown attribute db_name on db_backup")
	}
	found := false
	for _, f := range findings {
		if f.Severity == SeverityError && f.Message == `unknown attribute "db_name" for db_backup resource` {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown attribute error for db_name, got: %s", FormatFindings(findings))
	}
}

func TestSchemaRequiresDatabase(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "missing-db", Type: "db_backup", Attributes: map[string]any{
				"db_type": "sqlite", "target_path": "/tmp/backups",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	found := false
	for _, f := range findings {
		if f.Severity == SeverityError && f.Message == `db_backup resource missing required attribute "source_db"` {
			found = true
		}
	}
	if !found {
		t.Errorf("expected required attribute error for database, got: %s", FormatFindings(findings))
	}
}

func TestSchemaAcceptsValidBackup(t *testing.T) {
	mod := Configcrate{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-backup", Type: "backup", Attributes: map[string]any{
				"source_dir": "/srv/data", "target_path": "/var/backups/data",
				"retain": "14", "schedule": "daily",
			}},
			{Name: "good-db", Type: "db_backup", Attributes: map[string]any{
				"db_type": "sqlite", "source_db": "/srv/data/app.db",
				"target_path": "/var/backups/db", "retain": "14",
			}},
		},
	}
	findings := ValidateConfigcrate(mod)
	if HasErrors(findings) {
		t.Errorf("valid backup module should have no errors, got: %s", FormatFindings(findings))
	}
}
