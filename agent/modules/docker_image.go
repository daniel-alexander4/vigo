// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DockerImageExecutor manages Docker images.
// Params:
//   - image (or name): image name with optional tag (e.g., "nginx:1.25")
//   - state: present (default) or absent
//   - tag: tag to use if not in image name (default: "latest")
//   - force: "true" to always pull even if present (default: "false")
type DockerImageExecutor struct {
	Runner CommandRunner
}

func (e *DockerImageExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *DockerImageExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	image := params["image"]
	if image == "" {
		image = params["name"]
	}
	state := params["state"]
	if state == "" {
		state = "present"
	}
	force := params["force"] == "true"

	// Append tag if not present in image name.
	if !strings.Contains(image, ":") {
		tag := params["tag"]
		if tag == "" {
			tag = "latest"
		}
		image = image + ":" + tag
	}

	result := ResourceResult{
		ResourceType: "docker_image",
		ResourceName: image,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if image == "" || image == ":" || image == ":latest" {
		result.Error = fmt.Errorf("docker_image executor: image or name param is required")
		return result
	}

	switch state {
	case "present":
		return e.ensurePresent(ctx, image, force, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, image, dryRun, result)
	default:
		result.Error = fmt.Errorf("docker_image executor: unknown state %q", state)
		return result
	}
}

func (e *DockerImageExecutor) ensurePresent(ctx context.Context, image string, force, dryRun bool, result ResourceResult) ResourceResult {
	exists := e.imageExists(ctx, image)

	if exists && !force {
		result.Action = ActionNone
		return result
	}

	result.Changed = true
	if exists {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, "docker", "pull", image); err != nil {
		result.Error = fmt.Errorf("docker pull %s: %w: %s", image, err, string(out))
	}

	return result
}

func (e *DockerImageExecutor) ensureAbsent(ctx context.Context, image string, dryRun bool, result ResourceResult) ResourceResult {
	if !e.imageExists(ctx, image) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if out, err := e.runner().Run(ctx, "docker", "rmi", image); err != nil {
		result.Error = fmt.Errorf("docker rmi %s: %w: %s", image, err, string(out))
	}

	return result
}

func (e *DockerImageExecutor) imageExists(ctx context.Context, image string) bool {
	_, err := e.runner().Run(ctx, "docker", "image", "inspect", image)
	return err == nil
}
