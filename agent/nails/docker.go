package nails

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// DockerCollector gathers Docker/container runtime nails.
// Returns installed status, version, running container count and names.
type DockerCollector struct{}

func (c *DockerCollector) Name() string { return "docker" }

func (c *DockerCollector) Collect(ctx context.Context) (map[string]any, error) {
	runner := &cmdrun.Default{}

	// Check if docker is available.
	out, err := runner.Run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return map[string]any{
			"installed": false,
		}, nil
	}

	result := map[string]any{
		"installed": true,
		"version":   strings.TrimSpace(string(out)),
	}

	// Get running containers.
	out, err = runner.Run(ctx, "docker", "ps", "--format", "{{json .Names}}")
	if err == nil {
		var containers []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var name string
			if json.Unmarshal([]byte(line), &name) == nil {
				containers = append(containers, name)
			} else {
				containers = append(containers, line)
			}
		}
		if containers == nil {
			containers = []string{}
		}
		result["running_count"] = len(containers)
		result["running"] = containers
	}

	// Get container runtime info.
	out, err = runner.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}|{{.StorageDriver}}|{{.CgroupDriver}}")
	if err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
		if len(parts) == 3 {
			result["storage_driver"] = parts[1]
			result["cgroup_driver"] = parts[2]
		}
	}

	return result, nil
}
