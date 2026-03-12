// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*ExecExecutor)(nil)

func TestExecExecutor_RunCommand(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestExecExecutor_CreatesGuard(t *testing.T) {
	dir := t.TempDir()
	createsPath := filepath.Join(dir, "marker.txt")
	os.WriteFile(createsPath, []byte("exists"), 0644)

	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"creates": createsPath,
	}, false)

	if result.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q", result.Action, ActionSkipped)
	}
	if result.Changed {
		t.Error("Changed = true, want false (creates guard)")
	}
	if len(runner.calls) > 0 {
		t.Errorf("command should not run when creates file exists, got %v", runner.calls)
	}
}

func TestExecExecutor_CreatesGuardMissing(t *testing.T) {
	dir := t.TempDir()
	createsPath := filepath.Join(dir, "nonexistent.txt")

	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"creates": createsPath,
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
}

func TestExecExecutor_OnlyIfSuccess(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["test -f /tmp/flag"] = mockCmdResult{}
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"onlyif":  "test -f /tmp/flag",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestExecExecutor_OnlyIfFails(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["test -f /tmp/flag"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"onlyif":  "test -f /tmp/flag",
	}, false)

	if result.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q", result.Action, ActionSkipped)
	}
	if result.Changed {
		t.Error("Changed = true, want false (onlyif guard)")
	}
}

func TestExecExecutor_UnlessSucceeds(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["test -f /tmp/marker"] = mockCmdResult{}
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"unless":  "test -f /tmp/marker",
	}, false)

	if result.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q", result.Action, ActionSkipped)
	}
	if result.Changed {
		t.Error("Changed = true, want false (unless guard)")
	}
}

func TestExecExecutor_UnlessFails(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["test -f /tmp/marker"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"unless":  "test -f /tmp/marker",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestExecExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not execute commands, got %v", runner.calls)
	}
}

func TestExecExecutor_DryRunWithGuards(t *testing.T) {
	dir := t.TempDir()
	createsPath := filepath.Join(dir, "marker.txt")

	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"creates": createsPath,
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestExecExecutor_CommandError(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["echo hello"] = mockCmdResult{err: fmt.Errorf("exit status 1"), output: []byte("failed")}
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
	}, false)

	if result.Error == nil {
		t.Error("expected error from command failure, got nil")
	}
}

func TestExecExecutor_ResourceType(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
	}, true)

	if result.ResourceType != "exec" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "exec")
	}
}

func TestExecExecutor_MissingCommand(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{}, false)

	if result.Error == nil {
		t.Error("expected error for missing command, got nil")
	}
}

func TestExecExecutor_Stdin(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "grafana-cli admin reset-admin-password --stdin",
		"stdin":   "hunter2",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	if len(runner.stdinCalls) != 1 {
		t.Fatalf("expected 1 stdin call, got %d", len(runner.stdinCalls))
	}
	if string(runner.stdinCalls[0].stdin) != "hunter2" {
		t.Errorf("stdin = %q, want %q", string(runner.stdinCalls[0].stdin), "hunter2")
	}
	// The secret should NOT appear in the command args recorded.
	for _, call := range runner.calls {
		if strings.Contains(call, "hunter2") {
			t.Errorf("secret found in command args: %q", call)
		}
	}
}

func TestExecExecutor_StdinDryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "some-cmd --stdin",
		"stdin":   "secret-value",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not execute commands, got %v", runner.calls)
	}
}

func TestExecExecutor_SecretFile(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command":     "mysql --defaults-file={{secret_file}} -e 'SELECT 1'",
		"secret_file": "[client]\npassword=hunter2\n",
	}, false)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	// The command should have the temp file path substituted, not the raw placeholder.
	if strings.Contains(runner.calls[0], "{{secret_file}}") {
		t.Errorf("command still contains placeholder: %q", runner.calls[0])
	}
	// The secret should NOT appear in the command args.
	if strings.Contains(runner.calls[0], "hunter2") {
		t.Errorf("secret found in command args: %q", runner.calls[0])
	}
	// The temp file should be cleaned up.
	// Extract the path from the command — it's the token after --defaults-file=
	for _, part := range strings.Fields(runner.calls[0]) {
		if strings.HasPrefix(part, "--defaults-file=") {
			path := strings.TrimPrefix(part, "--defaults-file=")
			if _, err := os.Stat(path); err == nil {
				t.Errorf("secret temp file was not cleaned up: %s", path)
			}
		}
	}
}

func TestExecExecutor_StdinAndSecretFileMutuallyExclusive(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command":     "some-cmd",
		"stdin":       "value1",
		"secret_file": "value2",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for mutually exclusive params, got nil")
	}
	if !strings.Contains(result.Error.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutually exclusive, got: %v", result.Error)
	}
}

func TestExecExecutor_SecretFileDryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command":     "some-cmd {{secret_file}}",
		"secret_file": "secret-value",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
	if len(runner.calls) > 0 {
		t.Errorf("dry-run should not execute commands, got %v", runner.calls)
	}
}

func TestExecExecutor_GuardOrder_CreatesFirst(t *testing.T) {
	dir := t.TempDir()
	createsPath := filepath.Join(dir, "marker.txt")
	os.WriteFile(createsPath, []byte("exists"), 0644)

	runner := newMockCmdRunnerGeneric()
	ee := &ExecExecutor{Runner: runner}
	result := ee.Execute(context.Background(), map[string]string{
		"command": "echo hello",
		"creates": createsPath,
		"onlyif":  "true",
	}, false)

	if result.Action != ActionSkipped {
		t.Errorf("Action = %q, want %q (creates takes precedence)", result.Action, ActionSkipped)
	}
	if len(runner.calls) > 0 {
		t.Errorf("no commands should run when creates file exists, got %v", runner.calls)
	}
}
