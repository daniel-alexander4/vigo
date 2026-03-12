// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

var _ Executor = (*TmpfileExecutor)(nil)

func TestTmpfileExecutor_Present_Creates(t *testing.T) {
	dir := t.TempDir()
	runner := newMockCmdRunnerGeneric()
	confPath := filepath.Join(dir, "myapp.conf")
	runner.results["systemd-tmpfiles --create "+confPath] = mockCmdResult{}

	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/run/myapp",
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

	content := readFile(t, confPath)
	expected := "d /run/myapp 0755 - - -"
	if strings.TrimSpace(content) != expected {
		t.Errorf("conf = %q, want %q", strings.TrimSpace(content), expected)
	}
}

func TestTmpfileExecutor_Present_CustomParams(t *testing.T) {
	dir := t.TempDir()
	runner := newMockCmdRunnerGeneric()
	confPath := filepath.Join(dir, "cache.conf")
	runner.results["systemd-tmpfiles --create "+confPath] = mockCmdResult{}

	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":       "cache",
		"path":       "/tmp/cache",
		"entry_type": "D",
		"mode":       "1777",
		"user":       "root",
		"group":      "root",
		"age":        "10d",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	content := readFile(t, confPath)
	expected := "D /tmp/cache 1777 root root 10d"
	if strings.TrimSpace(content) != expected {
		t.Errorf("conf = %q, want %q", strings.TrimSpace(content), expected)
	}
}

func TestTmpfileExecutor_Present_AlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "myapp.conf")
	writeFile(t, confPath, "d /run/myapp 0755 - - -\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/run/myapp",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestTmpfileExecutor_Present_Updates(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "myapp.conf")
	writeFile(t, confPath, "d /run/myapp 0755 - - -\n", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["systemd-tmpfiles --create "+confPath] = mockCmdResult{}

	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/run/myapp",
		"mode": "0700",
		"user": "app",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	content := readFile(t, confPath)
	expected := "d /run/myapp 0700 app - -"
	if strings.TrimSpace(content) != expected {
		t.Errorf("conf = %q, want %q", strings.TrimSpace(content), expected)
	}
}

func TestTmpfileExecutor_Absent_Removes(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "old.conf")
	writeFile(t, confPath, "d /run/old 0755 - - -\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "old",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if fileExists(confPath) {
		t.Error("conf file should be removed")
	}
}

func TestTmpfileExecutor_Absent_AlreadyAbsent(t *testing.T) {
	dir := t.TempDir()
	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "nonexistent",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestTmpfileExecutor_DryRun_Present(t *testing.T) {
	dir := t.TempDir()
	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "myapp",
		"path": "/run/myapp",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	// No file should be created.
	confPath := filepath.Join(dir, "myapp.conf")
	if fileExists(confPath) {
		t.Error("dry-run should not create config file")
	}
}

func TestTmpfileExecutor_DryRun_Absent(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "old.conf")
	writeFile(t, confPath, "d /run/old 0755 - - -\n", 0644)

	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{TmpfilesDir: dir, Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "old",
		"state": "absent",
	}, true)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	// File should still exist.
	if !fileExists(confPath) {
		t.Error("dry-run should not remove config file")
	}
}

func TestTmpfileExecutor_MissingName(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"path": "/run/test",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(result.Error.Error(), "name param is required") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestTmpfileExecutor_MissingPath(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "test",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(result.Error.Error(), "path param is required") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestTmpfileExecutor_UnknownState(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &TmpfileExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "test",
		"state": "invalid",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for unknown state")
	}
	if !strings.Contains(result.Error.Error(), "unknown state") {
		t.Errorf("error = %v", result.Error)
	}
}
