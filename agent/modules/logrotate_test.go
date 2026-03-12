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

var _ Executor = (*LogrotateExecutor)(nil)

func TestLogrotateExecutor_Present_CreatesConfig(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	result := e.Execute(context.Background(), map[string]string{
		"name":       "myapp",
		"path":       "/var/log/myapp/*.log",
		"frequency":  "daily",
		"rotate":     "7",
		"postrotate": "systemctl reload myapp",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	content := readFile(t, filepath.Join(dir, "myapp"))
	if !strings.Contains(content, "/var/log/myapp/*.log") {
		t.Error("config should contain log path")
	}
	if !strings.Contains(content, "daily") {
		t.Error("config should contain frequency")
	}
	if !strings.Contains(content, "rotate 7") {
		t.Error("config should contain rotate count")
	}
	if !strings.Contains(content, "postrotate") {
		t.Error("config should contain postrotate")
	}
	if !strings.Contains(content, "systemctl reload myapp") {
		t.Error("config should contain postrotate command")
	}
}

func TestLogrotateExecutor_Present_Idempotent(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	params := map[string]string{
		"name": "myapp",
		"path": "/var/log/myapp/*.log",
	}

	// First run creates.
	e.Execute(context.Background(), params, false)
	// Second run is idempotent.
	result := e.Execute(context.Background(), params, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestLogrotateExecutor_Present_Updates(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}

	e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/var/log/myapp/*.log",
	}, false)

	result := e.Execute(context.Background(), map[string]string{
		"name":   "myapp",
		"path":   "/var/log/myapp/*.log",
		"rotate": "14",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	content := readFile(t, filepath.Join(dir, "myapp"))
	if !strings.Contains(content, "rotate 14") {
		t.Error("config should be updated with rotate 14")
	}
}

func TestLogrotateExecutor_Absent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "myapp"), "existing config", 0644)

	e := &LogrotateExecutor{ConfDir: dir}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myapp",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if fileExists(filepath.Join(dir, "myapp")) {
		t.Error("config file should be removed")
	}
}

func TestLogrotateExecutor_Absent_AlreadyAbsent(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myapp",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestLogrotateExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/var/log/myapp/*.log",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if fileExists(filepath.Join(dir, "myapp")) {
		t.Error("dry-run should not create file")
	}
}

func TestLogrotateExecutor_MissingName(t *testing.T) {
	e := &LogrotateExecutor{}
	result := e.Execute(context.Background(), map[string]string{}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLogrotateExecutor_MissingPath(t *testing.T) {
	e := &LogrotateExecutor{ConfDir: t.TempDir()}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestLogrotateExecutor_AllOptions(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	result := e.Execute(context.Background(), map[string]string{
		"name":          "myapp",
		"path":          "/var/log/myapp/*.log",
		"compress":      "false",
		"delaycompress": "false",
		"missingok":     "false",
		"notifempty":    "false",
		"copytruncate":  "true",
		"size":          "100M",
		"maxage":        "30",
		"create":        "0640 root adm",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}

	content := readFile(t, filepath.Join(dir, "myapp"))
	if strings.Contains(content, "compress") && !strings.Contains(content, "copytruncate") {
		t.Error("compress should be disabled")
	}
	if !strings.Contains(content, "copytruncate") {
		t.Error("should contain copytruncate")
	}
	if !strings.Contains(content, "size 100M") {
		t.Error("should contain size")
	}
	if !strings.Contains(content, "maxage 30") {
		t.Error("should contain maxage")
	}
	if !strings.Contains(content, "create 0640 root adm") {
		t.Error("should contain create directive")
	}

	// Verify compress/delaycompress/missingok/notifempty are absent.
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "compress" || trimmed == "delaycompress" || trimmed == "missingok" || trimmed == "notifempty" {
			t.Errorf("should not contain %q when set to false", trimmed)
		}
	}
}

func TestLogrotateExecutor_UnknownState(t *testing.T) {
	e := &LogrotateExecutor{ConfDir: t.TempDir()}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "myapp",
		"state": "maybe",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for unknown state")
	}
}

func TestLogrotateExecutor_ManagedHeader(t *testing.T) {
	dir := t.TempDir()
	e := &LogrotateExecutor{ConfDir: dir}
	e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/var/log/myapp/*.log",
	}, false)

	content, _ := os.ReadFile(filepath.Join(dir, "myapp"))
	if !strings.HasPrefix(string(content), "# Managed by Hobnob") {
		t.Error("config should start with managed header")
	}
}
