// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package validate

import (
	"testing"
)

func TestExecWithoutGuard_IsError(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{"command": "echo hello"}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for exec without guard")
	}
	if findings[0].Message != "exec resource has no idempotency guard — add creates, onlyif, or unless" {
		t.Errorf("unexpected message: %s", findings[0].Message)
	}
}

func TestExecWithCreates_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{
				"command": "touch /tmp/done",
				"creates": "/tmp/done",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestExecWithOnlyIf_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{
				"command": "apt-get update",
				"onlyif":  "test -f /var/run/reboot-required",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestExecWithUnless_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "run-thing", Type: "exec", Attributes: map[string]any{
				"command": "curl -fsSL https://example.com/key | gpg --dearmor -o /etc/apt/key.gpg",
				"unless":  "test -f /etc/apt/key.gpg",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestServiceRestarted_WithoutSubscribes_IsWarning(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"name":  "nginx",
				"state": "restarted",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Fatal("restarted service without subscribes should be warning, not error")
	}
	if len(findings) == 0 {
		t.Fatal("expected a warning")
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity, got %d", findings[0].Severity)
	}
}

func TestServiceRestarted_WithSubscribes_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"name":  "nginx",
				"state": "restarted",
			}, Subscribes: []string{"nginx-conf"}},
		},
	}

	findings := ValidateModule(mod)
	if len(findings) > 0 {
		t.Errorf("expected no findings, got: %s", FormatFindings(findings))
	}
}

func TestServiceRunning_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "my-svc", Type: "service", Attributes: map[string]any{
				"name":  "nginx",
				"state": "running",
			}},
		},
	}

	findings := ValidateModule(mod)
	if len(findings) > 0 {
		t.Errorf("expected no findings, got: %s", FormatFindings(findings))
	}
}

func TestInherentlyIdempotentTypes_AreClean(t *testing.T) {
	types := []struct {
		typ   string
		attrs map[string]any
	}{
		{"file", map[string]any{"targetpath": "/etc/foo", "content": "bar", "owner": "root", "group": "root", "mode": "0644"}},
		{"package", map[string]any{"package": "nginx", "state": "present"}},
		{"user", map[string]any{"username": "deploy"}},
		{"repository", map[string]any{"name": "nginx", "type": "apt"}},
		{"archive", map[string]any{"source": "https://example.com/foo.tar.gz", "target": "/opt/foo"}},
		{"cron", map[string]any{"name": "backup", "job": "/usr/local/bin/backup.sh", "schedule": "0 2 * * *", "user": "deploy"}},
	}

	for _, tt := range types {
		mod := Module{
			Name: "test-mod",
			Resources: []Resource{
				{Name: "test-" + tt.typ, Type: tt.typ, Attributes: tt.attrs},
			},
		}
		findings := ValidateModule(mod)
		if HasErrors(findings) {
			t.Errorf("type %q should be inherently idempotent, got: %s", tt.typ, FormatFindings(findings))
		}
	}
}

func TestFileValidation_MissingRequiredFields(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bare-file", Type: "file", Attributes: map[string]any{}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for bare file resource")
	}
	// Should require: targetpath, owner, group, mode, content/source = 5 errors.
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

func TestFileValidation_AbsentSkipsContent(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-file", Type: "file", Attributes: map[string]any{
				"targetpath": "/tmp/gone", "owner": "root", "group": "root", "mode": "0644", "state": "absent",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("state=absent should not require content, got: %s", FormatFindings(findings))
	}
}

func TestFileValidation_SourceSatisfiesContent(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "tmpl-file", Type: "file", Attributes: map[string]any{
				"targetpath": "/etc/app.conf", "source": "templates/app.conf.tmpl",
				"owner": "root", "group": "root", "mode": "0600",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("source should satisfy content requirement, got: %s", FormatFindings(findings))
	}
}

func TestUnknownType_IsWarning(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "wat", Type: "magic", Attributes: map[string]any{}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Fatal("unknown types should be warnings (for custom executors), not errors")
	}
	if len(findings) == 0 {
		t.Fatal("expected a warning for unknown type")
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity, got %d", findings[0].Severity)
	}
}

func TestProcessAndFileLineTypes_AreClean(t *testing.T) {
	for _, typ := range []string{"process", "file_line"} {
		mod := Module{
			Name: "test-mod",
			Resources: []Resource{
				{Name: "test-" + typ, Type: typ, Attributes: map[string]any{"name": "foo"}},
			},
		}
		findings := ValidateModule(mod)
		if HasErrors(findings) {
			t.Errorf("type %q should be known, got: %s", typ, FormatFindings(findings))
		}
	}
}

func TestMissingType_IsError(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "oops", Attributes: map[string]any{}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for missing type")
	}
}

func TestMissingName_IsError(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Type: "file", Attributes: map[string]any{"targetpath": "/etc/foo"}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for missing name")
	}
}

func TestMultipleResources_AllValidated(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-file", Type: "file", Attributes: map[string]any{"targetpath": "/etc/foo", "content": "bar", "owner": "root", "group": "root", "mode": "0644"}},
			{Name: "bad-exec", Type: "exec", Attributes: map[string]any{"command": "echo hello"}},
			{Name: "good-exec", Type: "exec", Attributes: map[string]any{"command": "echo world", "creates": "/tmp/done"}},
		},
	}

	findings := ValidateModule(mod)
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

func TestExecWithSubscribesSecret_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{
				Name:             "set-password",
				Type:             "exec",
				Attributes:       map[string]any{"command": "grafana-cli admin reset-admin-password 'secret'"},
				SubscribesSecret: []string{"hobnob/grafana/admin-password"},
			},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("exec with subscribes_secret should pass validation, got: %s", FormatFindings(findings))
	}
}

func TestCronValidation_MissingScheduleAndUser(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-cron", Type: "cron", Attributes: map[string]any{
				"job": "/usr/local/bin/backup.sh",
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for cron without schedule and user")
	}
	errorCount := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			errorCount++
		}
	}
	if errorCount != 2 {
		t.Errorf("expected 2 errors (schedule + user), got %d: %s", errorCount, FormatFindings(findings))
	}
}

func TestCronValidation_WithSchedule_MissingUser(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "no-user-cron", Type: "cron", Attributes: map[string]any{
				"job":      "/usr/local/bin/backup.sh",
				"schedule": "0 2 * * *",
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for cron without user")
	}
	if len(findings) != 1 {
		t.Errorf("expected 1 error, got %d: %s", len(findings), FormatFindings(findings))
	}
}

func TestCronValidation_IndividualFields_IsClean(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "fields-cron", Type: "cron", Attributes: map[string]any{
				"job":     "/usr/local/bin/backup.sh",
				"user":    "deploy",
				"minute":  "0",
				"hour":    "2",
				"day":     "*",
				"month":   "*",
				"weekday": "*",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("cron with all individual fields + user should pass, got: %s", FormatFindings(findings))
	}
}

func TestCronValidation_PartialFields_IsError(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "partial-cron", Type: "cron", Attributes: map[string]any{
				"job":    "/usr/local/bin/backup.sh",
				"user":   "deploy",
				"minute": "0",
				"hour":   "2",
				// day, month, weekday omitted — would default to *
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for cron with only some individual fields")
	}
}

func TestCronValidation_Absent_SkipsChecks(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-cron", Type: "cron", Attributes: map[string]any{
				"state": "absent",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("state=absent should skip schedule/user checks, got: %s", FormatFindings(findings))
	}
}

// ── Replace validation tests ─────────────────────────────────────

func TestReplaceValidation_MissingPath(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-replace", Type: "replace", Attributes: map[string]any{
				"match": "foo", "replace": "bar",
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for replace without path")
	}
}

func TestReplaceValidation_MissingMatch(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-replace", Type: "replace", Attributes: map[string]any{
				"path": "/etc/foo", "replace": "bar",
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for replace without match")
	}
}

func TestReplaceValidation_ValidConfig(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-replace", Type: "replace", Attributes: map[string]any{
				"path": "/etc/ssh/sshd_config", "match": "^PermitRootLogin", "replace": "PermitRootLogin no",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

// ── FieldEdit validation tests ───────────────────────────────────

func TestFieldEditValidation_MissingRequired(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-fe", Type: "field_edit", Attributes: map[string]any{}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected errors for field_edit without required fields")
	}
	// Should require: path, line_match, select_field, field_value = 4 errors.
	errorCount := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			errorCount++
		}
	}
	if errorCount != 4 {
		t.Errorf("expected 4 errors, got %d: %s", errorCount, FormatFindings(findings))
	}
}

func TestFieldEditValidation_MissingFieldValue(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "no-value-fe", Type: "field_edit", Attributes: map[string]any{
				"path": "/etc/group", "line_match": "^docker:", "select_field": "4",
			}},
		},
	}
	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for field_edit without field_value")
	}
}

func TestFieldEditValidation_ValidConfig(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "good-fe", Type: "field_edit", Attributes: map[string]any{
				"path": "/etc/group", "line_match": "^docker:", "select_field": "4",
				"field_value": "deploy", "field_operation": "append",
			}},
		},
	}
	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors, got: %s", FormatFindings(findings))
	}
}

func TestFileWithoutContent_IsError(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "config", Type: "file", Attributes: map[string]any{
				"targetpath": "/etc/myapp/config",
				"owner":      "root",
				"group":      "root",
				"mode":       "0644",
			}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file without content")
	}
}

func TestDirectoryType_NoContentRequired(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "datadir", Type: "directory", Attributes: map[string]any{
				"targetpath": "/var/lib/myapp/data",
				"owner":      "root",
				"group":      "root",
				"mode":       "0755",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for directory, got: %s", FormatFindings(findings))
	}
}

func TestDirectoryType_MissingTargetpath(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "baddir", Type: "directory", Attributes: map[string]any{
				"owner": "root",
				"mode":  "0755",
			}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for directory without targetpath")
	}
}

func TestSymlinkType_Valid(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "enable-site", Type: "symlink", Attributes: map[string]any{
				"targetpath": "/etc/nginx/sites-enabled/mysite",
				"to":         "../sites-available/mysite",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for valid symlink, got: %s", FormatFindings(findings))
	}
}

func TestSymlinkType_MissingTo(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "bad-link", Type: "symlink", Attributes: map[string]any{
				"targetpath": "/some/link",
			}},
		},
	}

	findings := ValidateModule(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for symlink without to")
	}
}

func TestSymlinkType_AbsentSkipsTo(t *testing.T) {
	mod := Module{
		Name: "test-mod",
		Resources: []Resource{
			{Name: "rm-link", Type: "symlink", Attributes: map[string]any{
				"targetpath": "/some/link",
				"state":      "absent",
			}},
		},
	}

	findings := ValidateModule(mod)
	if HasErrors(findings) {
		t.Errorf("expected no errors for absent symlink without to, got: %s", FormatFindings(findings))
	}
}

func TestFindingString_Format(t *testing.T) {
	f := Finding{
		Severity:     SeverityError,
		Module:       "nginx",
		ResourceName: "run-setup",
		ResourceType: "exec",
		Message:      "exec resource has no idempotency guard — add creates, onlyif, or unless",
	}
	expected := "[ERROR] nginx/run-setup: exec resource has no idempotency guard — add creates, onlyif, or unless"
	if f.String() != expected {
		t.Errorf("got: %s\nwant: %s", f.String(), expected)
	}
}
