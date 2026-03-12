// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DockerContainerExecutor manages Docker container lifecycle.
// Params:
//   - container (or name): container name (required)
//   - image: Docker image (required for state=present/running)
//   - state: running (default), stopped, present, absent
//   - ports: published ports, comma-separated (e.g., "8080:80,443:443")
//   - env: environment variables, comma-separated KEY=VALUE pairs
//   - volumes: bind mounts, comma-separated (e.g., "/host:/container,/data:/data:ro")
//   - network: Docker network name
//   - restart: restart policy (no, always, unless-stopped, on-failure)
//   - command: override container command
//   - pull: "always", "missing" (default), or "never"
type DockerContainerExecutor struct {
	Runner CommandRunner
}

func (e *DockerContainerExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *DockerContainerExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	name := params["container"]
	if name == "" {
		name = params["name"]
	}
	image := params["image"]
	state := params["state"]
	if state == "" {
		state = "running"
	}

	result := ResourceResult{
		ResourceType: "docker_container",
		ResourceName: name,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if name == "" {
		result.Error = fmt.Errorf("docker_container executor: container or name param is required")
		return result
	}

	switch state {
	case "running":
		if image == "" {
			result.Error = fmt.Errorf("docker_container executor: image param is required for state=running")
			return result
		}
		return e.ensureRunning(ctx, name, image, params, dryRun, result)
	case "present":
		if image == "" {
			result.Error = fmt.Errorf("docker_container executor: image param is required for state=present")
			return result
		}
		return e.ensurePresent(ctx, name, image, params, dryRun, result)
	case "stopped":
		return e.ensureStopped(ctx, name, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, name, dryRun, result)
	default:
		result.Error = fmt.Errorf("docker_container executor: unknown state %q", state)
		return result
	}
}

func (e *DockerContainerExecutor) ensureRunning(ctx context.Context, name, image string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	status := e.containerStatus(ctx, name)

	if status == "running" {
		// Check if using correct image.
		currentImage := e.containerImage(ctx, name)
		if currentImage == image || strings.HasPrefix(currentImage, image+":") || strings.HasSuffix(currentImage, "/"+image) {
			result.Action = ActionNone
			return result
		}
		// Image changed — recreate.
		result.Action = ActionUpdated
		result.Changed = true
		if dryRun {
			return result
		}
		e.runner().Run(ctx, "docker", "stop", name)
		e.runner().Run(ctx, "docker", "rm", name)
		return e.createContainer(ctx, name, image, params, true, result)
	}

	if status != "" {
		// Container exists but not running.
		result.Action = ActionUpdated
		result.Changed = true
		if dryRun {
			return result
		}
		if out, err := e.runner().Run(ctx, "docker", "start", name); err != nil {
			result.Error = fmt.Errorf("docker start %s: %w: %s", name, err, string(out))
		}
		return result
	}

	// Container doesn't exist — create and start.
	result.Action = ActionCreated
	result.Changed = true
	if dryRun {
		return result
	}

	pull := params["pull"]
	if pull == "" {
		pull = "missing"
	}
	if pull == "always" {
		e.runner().Run(ctx, "docker", "pull", image)
	}

	return e.createContainer(ctx, name, image, params, true, result)
}

func (e *DockerContainerExecutor) ensurePresent(ctx context.Context, name, image string, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	if e.containerStatus(ctx, name) != "" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionCreated
	result.Changed = true
	if dryRun {
		return result
	}

	return e.createContainer(ctx, name, image, params, false, result)
}

func (e *DockerContainerExecutor) ensureStopped(ctx context.Context, name string, dryRun bool, result ResourceResult) ResourceResult {
	status := e.containerStatus(ctx, name)

	if status == "" {
		result.Action = ActionNone
		return result
	}

	if status != "running" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, "docker", "stop", name); err != nil {
		result.Error = fmt.Errorf("docker stop %s: %w: %s", name, err, string(out))
	}

	return result
}

func (e *DockerContainerExecutor) ensureAbsent(ctx context.Context, name string, dryRun bool, result ResourceResult) ResourceResult {
	status := e.containerStatus(ctx, name)

	if status == "" {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if status == "running" {
		if out, err := e.runner().Run(ctx, "docker", "stop", name); err != nil {
			result.Error = fmt.Errorf("docker stop %s: %w: %s", name, err, string(out))
			return result
		}
	}

	if out, err := e.runner().Run(ctx, "docker", "rm", name); err != nil {
		result.Error = fmt.Errorf("docker rm %s: %w: %s", name, err, string(out))
	}

	return result
}

func (e *DockerContainerExecutor) createContainer(ctx context.Context, name, image string, params map[string]string, start bool, result ResourceResult) ResourceResult {
	args := []string{"create", "--name", name}

	if ports := params["ports"]; ports != "" {
		for _, p := range strings.Split(ports, ",") {
			args = append(args, "-p", strings.TrimSpace(p))
		}
	}
	if env := params["env"]; env != "" {
		for _, e := range strings.Split(env, ",") {
			args = append(args, "-e", strings.TrimSpace(e))
		}
	}
	if volumes := params["volumes"]; volumes != "" {
		for _, v := range strings.Split(volumes, ",") {
			args = append(args, "-v", strings.TrimSpace(v))
		}
	}
	if network := params["network"]; network != "" {
		args = append(args, "--network", network)
	}
	if restart := params["restart"]; restart != "" {
		args = append(args, "--restart", restart)
	}

	args = append(args, image)

	if cmd := params["command"]; cmd != "" {
		args = append(args, strings.Fields(cmd)...)
	}

	if out, err := e.runner().Run(ctx, "docker", args...); err != nil {
		result.Error = fmt.Errorf("docker create %s: %w: %s", name, err, string(out))
		return result
	}

	if start {
		if out, err := e.runner().Run(ctx, "docker", "start", name); err != nil {
			result.Error = fmt.Errorf("docker start %s: %w: %s", name, err, string(out))
		}
	}

	return result
}

func (e *DockerContainerExecutor) containerStatus(ctx context.Context, name string) string {
	out, err := e.runner().Run(ctx, "docker", "inspect", "--format", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (e *DockerContainerExecutor) containerImage(ctx context.Context, name string) string {
	out, err := e.runner().Run(ctx, "docker", "inspect", "--format", "{{.Config.Image}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
