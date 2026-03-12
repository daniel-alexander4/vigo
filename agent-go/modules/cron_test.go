// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronExecutor_CreateJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-backup")

	e := &CronExecutor{}
	result := e.handlePresent(path, "# Managed by hobnob — do not edit\n0 2 * * * root /opt/backup.sh\n", &ResourceResult{ResourceType: "cron", ResourceName: "test-backup"}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("expected Changed=true")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if !strings.Contains(string(data), "/opt/backup.sh") {
		t.Errorf("file content missing job command: %s", data)
	}
	if !strings.Contains(string(data), "0 2 * * *") {
		t.Errorf("file content missing schedule: %s", data)
	}
}

func TestCronExecutor_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-backup")
	content := "# Managed by hobnob — do not edit\n0 2 * * * root /opt/backup.sh\n"

	e := &CronExecutor{}

	// First apply — creates.
	r1 := e.handlePresent(path, content, &ResourceResult{ResourceType: "cron"}, false)
	if r1.Action != ActionCreated {
		t.Fatalf("first apply: action = %q, want %q", r1.Action, ActionCreated)
	}

	// Second apply — no change.
	r2 := e.handlePresent(path, content, &ResourceResult{ResourceType: "cron"}, false)
	if r2.Action != ActionNone {
		t.Errorf("second apply: action = %q, want %q", r2.Action, ActionNone)
	}
	if r2.Changed {
		t.Error("second apply: expected Changed=false")
	}
}

func TestCronExecutor_UpdateJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-backup")

	e := &CronExecutor{}

	// Create initial.
	e.handlePresent(path, "# Managed by hobnob — do not edit\n0 2 * * * root /opt/backup.sh\n", &ResourceResult{ResourceType: "cron"}, false)

	// Update with new schedule.
	r := e.handlePresent(path, "# Managed by hobnob — do not edit\n0 3 * * * root /opt/backup.sh\n", &ResourceResult{ResourceType: "cron"}, false)
	if r.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", r.Action, ActionUpdated)
	}
	if !r.Changed {
		t.Error("expected Changed=true")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "0 3 * * *") {
		t.Errorf("file not updated: %s", data)
	}
}

func TestCronExecutor_Remove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-backup")
	os.WriteFile(path, []byte("old"), 0644)

	e := &CronExecutor{}
	r := e.handleAbsent(path, &ResourceResult{ResourceType: "cron", ResourceName: "test-backup"}, false)

	if r.Action != ActionDeleted {
		t.Errorf("action = %q, want %q", r.Action, ActionDeleted)
	}
	if !r.Changed {
		t.Error("expected Changed=true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be removed")
	}
}

func TestCronExecutor_RemoveAlreadyAbsent(t *testing.T) {
	e := &CronExecutor{}
	r := e.handleAbsent("/tmp/nonexistent-cron-"+t.Name(), &ResourceResult{ResourceType: "cron"}, false)

	if r.Action != ActionNone {
		t.Errorf("action = %q, want %q", r.Action, ActionNone)
	}
	if r.Changed {
		t.Error("expected Changed=false")
	}
}

func TestCronExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-dryrun")

	e := &CronExecutor{}
	r := e.handlePresent(path, "# test\n* * * * * root echo hi\n", &ResourceResult{ResourceType: "cron"}, true)

	if r.Action != ActionCreated {
		t.Errorf("action = %q, want %q", r.Action, ActionCreated)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should not exist after dry run")
	}
}

func TestBuildSchedule_FullExpression(t *testing.T) {
	params := map[string]string{"schedule": "0 2 * * *"}
	got := buildSchedule(params)
	if got != "0 2 * * *" {
		t.Errorf("got %q, want %q", got, "0 2 * * *")
	}
}

func TestBuildSchedule_IndividualFields(t *testing.T) {
	params := map[string]string{
		"minute":  "30",
		"hour":    "3",
		"weekday": "0,6",
	}
	got := buildSchedule(params)
	if got != "30 3 * * 0,6" {
		t.Errorf("got %q, want %q", got, "30 3 * * 0,6")
	}
}

func TestBuildSchedule_Defaults(t *testing.T) {
	got := buildSchedule(map[string]string{})
	if got != "* * * * *" {
		t.Errorf("got %q, want %q", got, "* * * * *")
	}
}

func TestSanitizeCronName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"db-backup", "db-backup"},
		{"my_job", "my_job"},
		{"job.with.dots", "job-with-dots"},
		{"spaces here", "spaces-here"},
		{"ok123", "ok123"},
	}
	for _, tt := range tests {
		got := sanitizeCronName(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeCronName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCronExecutor_Execute_MissingName(t *testing.T) {
	e := &CronExecutor{}
	r := e.Execute(context.Background(), map[string]string{}, false)
	if r.Error == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestCronExecutor_Execute_MissingJob(t *testing.T) {
	e := &CronExecutor{}
	r := e.Execute(context.Background(), map[string]string{"name": "test"}, false)
	if r.Error == nil {
		t.Fatal("expected error for missing job")
	}
}
