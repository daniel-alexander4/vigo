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
	if result.Action != "" {
		t.Errorf("Action = %q, want empty", result.Action)
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

// --- Count constraint tests ---

func TestProcessExecutor_ExactCount_Match(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"count":   "3",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestProcessExecutor_ExactCount_TooFew(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"count":   "3",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for count mismatch, got nil")
	}
	if !strings.Contains(result.Error.Error(), "expected 3, found 1") {
		t.Errorf("error = %v, want mention of expected 3 found 1", result.Error)
	}
}

func TestProcessExecutor_ExactCount_TooMany(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102\n103\n104")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"count":   "3",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for count mismatch, got nil")
	}
	if !strings.Contains(result.Error.Error(), "expected 3, found 5") {
		t.Errorf("error = %v, want mention of expected 3 found 5", result.Error)
	}
}

func TestProcessExecutor_MinMax_InRange(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"min":     "2",
		"max":     "5",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}

func TestProcessExecutor_MinOnly_TooFew(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"min":     "3",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Error.Error(), "expected min 3, found 1") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_MaxOnly_TooMany(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102\n103\n104")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"max":     "3",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Error.Error(), "expected max 3, found 5") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_CountAndMinExclusive(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"count":   "3",
		"min":     "1",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for mutually exclusive params")
	}
	if !strings.Contains(result.Error.Error(), "mutually exclusive") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_MinGreaterThanMax(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"min":     "5",
		"max":     "2",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for min > max")
	}
	if !strings.Contains(result.Error.Error(), "min (5) > max (2)") {
		t.Errorf("error = %v", result.Error)
	}
}

// --- Restart tests ---

func TestProcessExecutor_Restart_WhenNotRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	runner.results["systemctl restart nginx"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"restart": "systemctl",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl restart nginx") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected systemctl restart call, got %v", runner.calls)
	}
}

func TestProcessExecutor_Restart_CustomUnit(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	runner.results["systemctl restart nginx.service"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":      "nginx",
		"restart":      "systemctl",
		"restart_unit": "nginx.service",
	}, false)

	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl restart nginx.service") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected systemctl restart nginx.service, got %v", runner.calls)
	}
}

func TestProcessExecutor_Restart_AlreadyRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{output: []byte("1234")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"restart": "systemctl",
	}, false)

	// Already running — no action.
	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Changed {
		t.Error("Changed = true, want false")
	}
}

func TestProcessExecutor_Restart_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"restart": "systemctl",
	}, true)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "systemctl") {
			t.Errorf("dry-run should not call systemctl, got: %s", c)
		}
	}
}

func TestProcessExecutor_Restart_Failure(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x dead"] = mockCmdResult{err: fmt.Errorf("exit status 1")}
	runner.results["systemctl restart dead"] = mockCmdResult{
		output: []byte("Failed to restart"),
		err:    fmt.Errorf("exit status 1"),
	}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "dead",
		"restart": "systemctl",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error from failed restart, got nil")
	}
	if !strings.Contains(result.Error.Error(), "systemctl restart") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_Restart_UnknownMethod(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x nginx"] = mockCmdResult{err: fmt.Errorf("exit status 1")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "nginx",
		"restart": "supervisord",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for unknown restart method")
	}
	if !strings.Contains(result.Error.Error(), "unknown restart method") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_Restart_CountTooFew(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100")}
	runner.results["systemctl restart worker"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"min":     "3",
		"restart": "systemctl",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
}

// --- Kill excess tests ---

func TestProcessExecutor_KillOver_ExactCount(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	// First pgrep for count.
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102\n103\n104")}
	runner.results["kill -TERM 100"] = mockCmdResult{}
	runner.results["kill -TERM 101"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":   "worker",
		"count":     "3",
		"kill_over": "true",
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
	// Should kill exactly 2 excess PIDs.
	killCount := 0
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "kill -TERM") {
			killCount++
		}
	}
	if killCount != 2 {
		t.Errorf("killed %d processes, want 2; calls: %v", killCount, runner.calls)
	}
}

func TestProcessExecutor_KillOver_MaxConstraint(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102\n103")}
	runner.results["kill -TERM 100"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":   "worker",
		"max":       "3",
		"kill_over": "true",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	killCount := 0
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "kill -TERM") {
			killCount++
		}
	}
	if killCount != 1 {
		t.Errorf("killed %d processes, want 1; calls: %v", killCount, runner.calls)
	}
}

func TestProcessExecutor_KillOver_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102\n103")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":   "worker",
		"max":       "2",
		"kill_over": "true",
	}, true)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
	if !result.Changed {
		t.Error("Changed = false, want true")
	}
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "kill") && !strings.HasPrefix(c, "kill_over") {
			t.Errorf("dry-run should not execute kill, got: %s", c)
		}
	}
}

func TestProcessExecutor_KillOver_CustomSignal(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102")}
	runner.results["kill -KILL 100"] = mockCmdResult{}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":   "worker",
		"max":       "2",
		"kill_over": "true",
		"signal":    "KILL",
	}, false)

	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "kill -KILL 100") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected kill -KILL call, got %v", runner.calls)
	}
}

func TestProcessExecutor_KillOver_KillFailure(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -x worker"] = mockCmdResult{output: []byte("100\n101\n102")}
	runner.results["kill -TERM 100"] = mockCmdResult{
		output: []byte("Operation not permitted"),
		err:    fmt.Errorf("exit status 1"),
	}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process":   "worker",
		"count":     "2",
		"kill_over": "true",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error from failed kill, got nil")
	}
	if !strings.Contains(result.Error.Error(), "kill -TERM 100") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_InvalidCount(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"count":   "abc",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for invalid count")
	}
	if !strings.Contains(result.Error.Error(), "invalid count") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_InvalidMin(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "worker",
		"min":     "xyz",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for invalid min")
	}
	if !strings.Contains(result.Error.Error(), "invalid min") {
		t.Errorf("error = %v", result.Error)
	}
}

func TestProcessExecutor_MultipleProcesses_Count(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["pgrep -f gunicorn.*myapp"] = mockCmdResult{output: []byte("200\n201\n202\n203")}

	pe := &ProcessExecutor{Runner: runner}
	result := pe.Execute(context.Background(), map[string]string{
		"process": "gunicorn.*myapp",
		"match":   "pattern",
		"min":     "2",
		"max":     "8",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
	if result.Error != nil {
		t.Errorf("Error = %v, want nil", result.Error)
	}
}
