// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*GemExecutor)(nil)

func TestGemExecutor_Present_Installs(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{output: []byte("")}
	runner.results["gem install bundler --no-document"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestGemExecutor_Present_AlreadyInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		output: []byte("bundler (2.4.0)\n"),
	}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestGemExecutor_Present_VersionMismatch(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		output: []byte("bundler (2.3.0)\n"),
	}
	runner.results["gem install bundler --version 2.4.0 --no-document"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"version": "2.4.0",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestGemExecutor_Latest(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		output: []byte("bundler (2.3.0)\n"),
	}
	runner.results["gem update bundler --no-document"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"state":   "latest",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestGemExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		output: []byte("bundler (2.4.0)\n"),
	}
	runner.results["gem uninstall bundler --executables --all"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"state":   "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestGemExecutor_Absent_NotInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{output: []byte("")}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"state":   "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestGemExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{output: []byte("")}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	for _, c := range runner.calls {
		if strings.Contains(c, "install") {
			t.Errorf("dry-run should not call install, got %s", c)
		}
	}
}

func TestGemExecutor_MissingPackage(t *testing.T) {
	e := &GemExecutor{}
	result := e.Execute(context.Background(), map[string]string{}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing package")
	}
}

func TestGemExecutor_MultipleVersions(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		output: []byte("bundler (2.4.0, 2.3.0)\n"),
	}

	e := &GemExecutor{Runner: runner}
	// Should match version 2.4.0 (first listed).
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"version": "2.4.0",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestGemExecutor_NoDocument_Disabled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{output: []byte("")}
	runner.results["gem install bundler"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	e.Execute(context.Background(), map[string]string{
		"package":     "bundler",
		"no_document": "false",
	}, false)

	for _, c := range runner.calls {
		if strings.Contains(c, "--no-document") {
			t.Errorf("should not include --no-document when disabled, got %s", c)
		}
	}
}

func TestGemExecutor_Latest_NotInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["gem list --exact bundler"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}
	runner.results["gem install bundler --no-document"] = mockCmdResult{}

	e := &GemExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "bundler",
		"state":   "latest",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}
