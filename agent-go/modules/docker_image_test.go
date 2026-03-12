// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

var _ Executor = (*DockerImageExecutor)(nil)

func TestDockerImageExecutor_Present_Pulls(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:latest"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}
	runner.results["docker pull nginx:latest"] = mockCmdResult{}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}
}

func TestDockerImageExecutor_Present_AlreadyExists(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:1.25"] = mockCmdResult{output: []byte("[{}]")}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx:1.25",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestDockerImageExecutor_Present_Force(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:latest"] = mockCmdResult{output: []byte("[{}]")}
	runner.results["docker pull nginx:latest"] = mockCmdResult{}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
		"force": "true",
	}, false)

	if result.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", result.Action, ActionUpdated)
	}
}

func TestDockerImageExecutor_Absent(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:latest"] = mockCmdResult{output: []byte("[{}]")}
	runner.results["docker rmi nginx:latest"] = mockCmdResult{}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
		"state": "absent",
	}, false)

	if result.Action != ActionDeleted {
		t.Errorf("Action = %q, want %q", result.Action, ActionDeleted)
	}
}

func TestDockerImageExecutor_Absent_NotExists(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:latest"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
		"state": "absent",
	}, false)

	if result.Action != ActionNone {
		t.Errorf("Action = %q, want %q", result.Action, ActionNone)
	}
}

func TestDockerImageExecutor_DryRun(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:latest"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
	}, true)

	if result.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", result.Action, ActionCreated)
	}

	for _, c := range runner.calls {
		if strings.Contains(c, "docker pull") {
			t.Errorf("dry-run should not pull, got %s", c)
		}
	}
}

func TestDockerImageExecutor_CustomTag(t *testing.T) {
	runner := newMockCmdRunnerGeneric()
	runner.results["docker image inspect nginx:alpine"] = mockCmdResult{
		err: fmt.Errorf("not found"),
	}
	runner.results["docker pull nginx:alpine"] = mockCmdResult{}

	e := &DockerImageExecutor{Runner: runner}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
		"tag":   "alpine",
	}, false)

	if result.Error != nil {
		t.Fatalf("Error = %v", result.Error)
	}
	if result.ResourceName != "nginx:alpine" {
		t.Errorf("ResourceName = %q, want %q", result.ResourceName, "nginx:alpine")
	}
}

func TestDockerImageExecutor_MissingImage(t *testing.T) {
	e := &DockerImageExecutor{}
	result := e.Execute(context.Background(), map[string]string{}, false)
	if result.Error == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestDockerImageExecutor_UnknownState(t *testing.T) {
	e := &DockerImageExecutor{}
	result := e.Execute(context.Background(), map[string]string{
		"image": "nginx",
		"state": "maybe",
	}, false)
	if result.Error == nil {
		t.Fatal("expected error for unknown state")
	}
}
