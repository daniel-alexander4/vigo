package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Compile-time interface check
var _ Executor = (*ProcessExecutor)(nil)

func TestProcessExecutor_RunningAndStateRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{output: []byte("1234")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"state":   "running",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	if result.ResourceType != "process" {
		t.Errorf("ResourceType = %q, want %q", result.ResourceType, "process")
	}
}

func TestProcessExecutor_NotRunningAndStateRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"state":   "running",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Error.Error(), "not running") {
		t.Errorf("error should mention 'not running', got: %v", result.Error)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestProcessExecutor_RunningAndStateStopped(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x redis-server"] = mockCmdResult{output: []byte("5678")}
	runner.results["pkill -TERM -x redis-server"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "redis-server",
		"state":   "stopped",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "pkill -TERM -x redis-server") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pkill -TERM -x redis-server' call, got %v", runner.calls)
	}
}

func TestProcessExecutor_NotRunningAndStateStopped(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x myproc"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "myproc",
		"state":   "stopped",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestProcessExecutor_DryRunDoesNotKill(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{output: []byte("1234")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"state":   "stopped",
	}, true)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "pkill") {
			t.Errorf("dry-run should not execute pkill, got: %s", c)
		}
	}
}

func TestProcessExecutor_PatternMatch(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -f python.*myapp"] = mockCmdResult{output: []byte("9999")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "python.*myapp",
		"state":   "running",
		"match":   "pattern",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "pgrep -f python.*myapp") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pgrep -f' call for pattern match, got %v", runner.calls)
	}
}

func TestProcessExecutor_PatternMatchStop(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -f python.*worker"] = mockCmdResult{output: []byte("4321")}
	runner.results["pkill -TERM -f python.*worker"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "python.*worker",
		"state":   "stopped",
		"match":   "pattern",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "pkill -TERM -f python.*worker") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pkill -TERM -f python.*worker' call, got %v", runner.calls)
	}
}

func TestProcessExecutor_MissingName(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"state": "running",
	}, false)

	if result.Error == nil {
		t.Error("expected error for missing name, got nil")
	}
	if !strings.Contains(result.Error.Error(), "name param is required") {
		t.Errorf("error should mention 'name param is required', got: %v", result.Error)
	}
}

func TestProcessExecutor_CustomSignal(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x stubborn"] = mockCmdResult{output: []byte("6666")}
	runner.results["pkill -KILL -x stubborn"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "stubborn",
		"state":   "stopped",
		"signal":  "KILL",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "pkill -KILL -x stubborn") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pkill -KILL -x stubborn' call, got %v", runner.calls)
	}
}

func TestProcessExecutor_NameFallback(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x sshd"] = mockCmdResult{output: []byte("22")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"name":  "sshd",
		"state": "running",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	if result.ResourceName != "sshd" {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, "sshd")
	}
}

func TestProcessExecutor_DefaultStateIsRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x crond"] = mockCmdResult{output: []byte("111")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "crond",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestProcessExecutor_UnknownState(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "foo",
		"state":   "restarted",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for unknown state, got nil")
	}
	if !strings.Contains(result.Error.Error(), "unknown state") {
		t.Errorf("error should mention 'unknown state', got: %v", result.Error)
	}
}

func TestProcessExecutor_KillFailure(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x zombie"] = mockCmdResult{output: []byte("999")}
	runner.results["pkill -TERM -x zombie"] = mockCmdResult{
		output: []byte("Operation not permitted"),
		err:    fmt.Errorf("exit status 1"),
	}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "zombie",
		"state":   "stopped",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error from failed pkill, got nil")
	}
	if !strings.Contains(result.Error.Error(), "pkill") {
		t.Errorf("error should mention 'pkill', got: %v", result.Error)
	}
}
