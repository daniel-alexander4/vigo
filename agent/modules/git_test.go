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

var _ Executor = (*GitExecutor)(nil)

func TestGitExecutor_Present_Clones(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")

	runner := newMockCmdRunnerGeneric()
	runner.results["git clone https://github.com/example/repo.git "+dest] = mockCmdResult{}

	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://github.com/example/repo.git",
		"path": dest,
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

	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "git clone") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected git clone call, got %v", runner.calls)
	}
}

func TestGitExecutor_Present_CloneWithBranchAndDepth(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")

	runner := newMockCmdRunnerGeneric()
	runner.results["git clone --depth 1 --branch v2.0 https://example.com/repo.git "+dest] = mockCmdResult{}

	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo":   "https://example.com/repo.git",
		"path":   dest,
		"branch": "v2.0",
		"depth":  "1",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestGitExecutor_Present_AlreadyUpToDate(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	// Create .git dir to simulate existing clone.
	os.MkdirAll(filepath.Join(dest, ".git"), 0755)

	runner := newMockCmdRunnerGeneric()
	runner.results["git -C "+dest+" fetch"] = mockCmdResult{}
	runner.results["git -C "+dest+" rev-parse HEAD"] = mockCmdResult{output: []byte("abc123\n")}
	runner.results["git -C "+dest+" rev-parse @{u}"] = mockCmdResult{output: []byte("abc123\n")}

	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://example.com/repo.git",
		"path": dest,
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestGitExecutor_Present_NeedsUpdate(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(dest, ".git"), 0755)

	runner := newMockCmdRunnerGeneric()
	runner.results["git -C "+dest+" fetch"] = mockCmdResult{}
	runner.results["git -C "+dest+" rev-parse HEAD"] = mockCmdResult{output: []byte("abc123\n")}
	runner.results["git -C "+dest+" rev-parse @{u}"] = mockCmdResult{output: []byte("def456\n")}
	runner.results["git -C "+dest+" pull"] = mockCmdResult{}

	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://example.com/repo.git",
		"path": dest,
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
}

func TestGitExecutor_Absent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	os.MkdirAll(dest, 0755)

	e := &GitExecutor{Runner: newMockCmdRunnerGeneric()}
	result := e.Execute(context.Background(), map[string]string{
		"path":  dest,
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if fileExists(dest) {
		t.Error("directory should be removed")
	}
}

func TestGitExecutor_Absent_AlreadyAbsent(t *testing.T) {
	e := &GitExecutor{Runner: newMockCmdRunnerGeneric()}
	result := e.Execute(context.Background(), map[string]string{
		"path":  "/nonexistent/repo",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestGitExecutor_DryRun(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")

	runner := newMockCmdRunnerGeneric()
	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://example.com/repo.git",
		"path": dest,
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not run commands, got %v", runner.calls)
	}
}

func TestGitExecutor_MissingPath(t *testing.T) {
	e := &GitExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://example.com/repo.git",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestGitExecutor_MissingRepo(t *testing.T) {
	e := &GitExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"path": "/tmp/repo",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing repo")
	}
}

func TestGitExecutor_NameFallback(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")

	runner := newMockCmdRunnerGeneric()
	runner.results["git clone https://example.com/repo.git "+dest] = mockCmdResult{}

	e := &GitExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"repo": "https://example.com/repo.git",
		"name": dest,
	}, false)

	if result.ResourceName != dest {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, dest)
	}
}
