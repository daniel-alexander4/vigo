// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*AlternativesExecutor)(nil)

func TestAlternativesExecutor_Present_Installs(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query java"] = mockCmdResult{
		err: fmt.Errorf("no alternatives"),
	}
	runner.results["update-alternatives --install /usr/bin/java java /usr/lib/jvm/java-17/bin/java 50"] = mockCmdResult{}
	runner.results["update-alternatives --set java /usr/lib/jvm/java-17/bin/java"] = mockCmdResult{}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "java",
		"path": "/usr/lib/jvm/java-17/bin/java",
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
}

func TestAlternativesExecutor_Present_AlreadySet(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query hobnob-test-editor"] = mockCmdResult{
		output: []byte("Name: hobnob-test-editor\nValue: /usr/bin/vim\n\nAlternative: /usr/bin/vim\nPriority: 50\n"),
	}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "hobnob-test-editor",
		"path": "/usr/bin/vim",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestAlternativesExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query java"] = mockCmdResult{
		output: []byte("Alternative: /usr/lib/jvm/java-17/bin/java\n"),
	}
	runner.results["update-alternatives --remove java /usr/lib/jvm/java-17/bin/java"] = mockCmdResult{}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "java",
		"path":  "/usr/lib/jvm/java-17/bin/java",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestAlternativesExecutor_Absent_NotInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query java"] = mockCmdResult{
		err: fmt.Errorf("no alternatives"),
	}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":  "java",
		"path":  "/usr/lib/jvm/java-17/bin/java",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestAlternativesExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query java"] = mockCmdResult{
		err: fmt.Errorf("no alternatives"),
	}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "java",
		"path": "/usr/lib/jvm/java-17/bin/java",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	// Should only have --query calls, no --install or --set.
	for _, c := range runner.calls {
		if strings.Contains(c, "--install") || strings.Contains(c, "--set") {
			t.Errorf("dry-run should not run %s", c)
		}
	}
}

func TestAlternativesExecutor_MissingName(t *testing.T) {
	e := &AlternativesExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"path": "/usr/bin/foo",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestAlternativesExecutor_MissingPath(t *testing.T) {
	e := &AlternativesExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"name": "java",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestAlternativesExecutor_CustomLinkAndPriority(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["update-alternatives --query editor"] = mockCmdResult{
		err: fmt.Errorf("no alternatives"),
	}
	runner.results["update-alternatives --install /usr/local/bin/editor editor /usr/bin/vim 100"] = mockCmdResult{}
	runner.results["update-alternatives --set editor /usr/bin/vim"] = mockCmdResult{}

	e := &AlternativesExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name":     "editor",
		"path":     "/usr/bin/vim",
		"link":     "/usr/local/bin/editor",
		"priority": "100",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "/usr/local/bin/editor") && strings.Contains(c, "100") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected custom link and priority, got %v", runner.calls)
	}
}
