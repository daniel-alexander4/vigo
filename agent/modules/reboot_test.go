// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

var _ Executor = (*RebootExecutor)(nil)

func TestRebootExecutor_Present_Triggers(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["shutdown -r +0 System reboot triggered by Hobnob"] = mockCmdResult{}

	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}

	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "shutdown -r") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shutdown call, got %v", runner.calls)
	}
}

func TestRebootExecutor_WithDelay(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["shutdown -r +5"] = mockCmdResult{}

	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"delay":   "5",
		"message": "Kernel update reboot",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}

	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "+5") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shutdown +5 call, got %v", runner.calls)
	}
}

func TestRebootExecutor_RebootRequiredFile_Missing(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"reboot_required_file": "/nonexistent/reboot-required",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
	if len(runner.calls) > 0 {
		t.Errorf("should not call shutdown, got %v", runner.calls)
	}
}

func TestRebootExecutor_RebootRequiredFile_Exists(t *testing.T) {
	dir := t.TempDir()
	reqFile := filepath.Join(dir, "reboot-required")
	writeFile(t, reqFile, "", 0644)

	runner := newMockCmdRunnerGeneric()
	runner.results["shutdown"] = mockCmdResult{}

	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"reboot_required_file": reqFile,
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
}

func TestRebootExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{}, true)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not run commands, got %v", runner.calls)
	}
}

func TestRebootExecutor_Cancel(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["shutdown -c"] = mockCmdResult{}

	e := &RebootExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"state": "absent",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "shutdown -c") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shutdown -c call, got %v", runner.calls)
	}
}
