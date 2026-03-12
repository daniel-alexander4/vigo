// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*NpmExecutor)(nil)

func TestNpmExecutor_Present_Installs(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}
	runner.results["npm install -g eslint"] = mockCmdResult{}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestNpmExecutor_Present_AlreadyInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		output: []byte(`{"dependencies":{"eslint":{"version":"8.50.0"}}}`),
	}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestNpmExecutor_Present_VersionMismatch(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		output: []byte(`{"dependencies":{"eslint":{"version":"8.49.0"}}}`),
	}
	runner.results["npm install -g eslint@8.50.0"] = mockCmdResult{}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
		"version": "8.50.0",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestNpmExecutor_Latest(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		output: []byte(`{"dependencies":{"eslint":{"version":"8.49.0"}}}`),
	}
	runner.results["npm install -g eslint@latest"] = mockCmdResult{}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
		"state":   "latest",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestNpmExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		output: []byte(`{"dependencies":{"eslint":{"version":"8.50.0"}}}`),
	}
	runner.results["npm uninstall -g eslint"] = mockCmdResult{}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
		"state":   "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestNpmExecutor_Absent_NotInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
		"state":   "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestNpmExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "eslint",
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

func TestNpmExecutor_MissingPackage(t *testing.T) {
	e := &NpmExecutor{}
	result := e.Execute(context.Background(), map[string]string{}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing package")
	}
}

func TestNpmExecutor_NameFallback(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["npm list -g eslint --depth=0 --json"] = mockCmdResult{
		output: []byte(`{"dependencies":{"eslint":{"version":"8.50.0"}}}`),
	}

	e := &NpmExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "eslint",
	}, false)

	if result.ResourceName != "eslint" {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, "eslint")
	}
}
