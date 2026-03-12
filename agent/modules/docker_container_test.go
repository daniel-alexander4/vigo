// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*DockerContainerExecutor)(nil)

func TestDockerContainerExecutor_Running_CreatesAndStarts(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		err: fmt.Errorf("no such container"),
	}
	runner.results["docker create --name myapp -p 8080:80 nginx:latest"] = mockCmdResult{}
	runner.results["docker start myapp"] = mockCmdResult{}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"image":     "nginx:latest",
		"ports":     "8080:80",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestDockerContainerExecutor_Running_AlreadyRunning(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		output: []byte("running\n"),
	}
	runner.results["docker inspect --format {{.Config.Image}} myapp"] = mockCmdResult{
		output: []byte("nginx:latest\n"),
	}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"image":     "nginx:latest",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestDockerContainerExecutor_Running_StartsStopped(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		output: []byte("exited\n"),
	}
	runner.results["docker start myapp"] = mockCmdResult{}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"image":     "nginx:latest",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestDockerContainerExecutor_Stopped(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		output: []byte("running\n"),
	}
	runner.results["docker stop myapp"] = mockCmdResult{}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"state":     "stopped",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestDockerContainerExecutor_Stopped_AlreadyStopped(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		output: []byte("exited\n"),
	}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"state":     "stopped",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestDockerContainerExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		output: []byte("running\n"),
	}
	runner.results["docker stop myapp"] = mockCmdResult{}
	runner.results["docker rm myapp"] = mockCmdResult{}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"state":     "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestDockerContainerExecutor_Absent_NotExists(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		err: fmt.Errorf("no such container"),
	}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"state":     "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestDockerContainerExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		err: fmt.Errorf("no such container"),
	}

	e := &DockerContainerExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"image":     "nginx:latest",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	for _, c := range runner.calls {
		if strings.Contains(c, "docker create") || strings.Contains(c, "docker start") {
			t.Errorf("dry-run should not create/start, got %s", c)
		}
	}
}

func TestDockerContainerExecutor_WithOptions(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker inspect --format {{.State.Status}} myapp"] = mockCmdResult{
		err: fmt.Errorf("no such container"),
	}
	runner.results["docker create"] = mockCmdResult{}
	runner.results["docker start myapp"] = mockCmdResult{}

	e := &DockerContainerExecutor{Runner: runner}
	e.Execute(context.Background(), map[string]string{
		"container": "myapp",
		"image":     "nginx:latest",
		"ports":     "8080:80,443:443",
		"env":       "FOO=bar,BAZ=qux",
		"volumes":   "/host:/container",
		"network":   "mynet",
		"restart":   "always",
	}, false)

	// Verify create call has expected flags.
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "docker create") {
			found = true
			if !strings.Contains(c, "-p 8080:80") {
				t.Error("should contain port mapping")
			}
			if !strings.Contains(c, "-e FOO=bar") {
				t.Error("should contain env var")
			}
			if !strings.Contains(c, "-v /host:/container") {
				t.Error("should contain volume")
			}
			if !strings.Contains(c, "--network mynet") {
				t.Error("should contain network")
			}
			if !strings.Contains(c, "--restart always") {
				t.Error("should contain restart policy")
			}
		}
	}
	if !found {
		t.Errorf("expected docker create call, got %v", runner.calls)
	}
}

func TestDockerContainerExecutor_MissingName(t *testing.T) {
	e := &DockerContainerExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestDockerContainerExecutor_MissingImage(t *testing.T) {
	e := &DockerContainerExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"container": "myapp",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing image")
	}
}
