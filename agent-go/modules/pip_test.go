// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*PipExecutor)(nil)

func TestPipExecutor_Present_Installs(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{err: fmt.Errorf("not found")}
	runner.results["pip3 install flask"] = mockCmdResult{}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestPipExecutor_Present_AlreadyInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{
		output: []byte("Name: Flask\nVersion: 2.3.0\n"),
	}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestPipExecutor_Present_VersionMismatch(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{
		output: []byte("Name: Flask\nVersion: 2.2.0\n"),
	}
	runner.results["pip3 install flask==2.3.0"] = mockCmdResult{}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
		"version": "2.3.0",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestPipExecutor_Latest(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{
		output: []byte("Name: Flask\nVersion: 2.2.0\n"),
	}
	runner.results["pip3 install --upgrade flask"] = mockCmdResult{}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
		"state":   "latest",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestPipExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{
		output: []byte("Name: Flask\nVersion: 2.3.0\n"),
	}
	runner.results["pip3 uninstall -y flask"] = mockCmdResult{}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
		"state":   "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestPipExecutor_Absent_NotInstalled(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{err: fmt.Errorf("not found")}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
		"state":   "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestPipExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{err: fmt.Errorf("not found")}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package": "flask",
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

func TestPipExecutor_Virtualenv(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["/opt/venv/bin/pip show flask"] = mockCmdResult{err: fmt.Errorf("not found")}
	runner.results["/opt/venv/bin/pip install flask"] = mockCmdResult{}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"package":    "flask",
		"virtualenv": "/opt/venv",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}

	found := false
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "/opt/venv/bin/pip") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected venv pip call, got %v", runner.calls)
	}
}

func TestPipExecutor_NameFallback(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pip3 show flask"] = mockCmdResult{
		output: []byte("Name: Flask\nVersion: 2.3.0\n"),
	}

	e := &PipExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"name": "flask",
	}, false)

	if result.ResourceName != "flask" {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, "flask")
	}
}

func TestPipExecutor_MissingPackage(t *testing.T) {
	e := &PipExecutor{}
	result := e.Execute(context.Background(), map[string]string{}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing package")
	}
}
