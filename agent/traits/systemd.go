package traits

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// SystemdCollector gathers systemd traits: version, default target, failed units.
type SystemdCollector struct{}

func (c *SystemdCollector) Name() string { return "systemd" }

func (c *SystemdCollector) Collect(ctx context.Context) (map[string]any, error) {
	runner := &cmdrun.Default{}

	// Check if systemctl is available.
	out, err := runner.Run(ctx, "systemctl", "--version")
	if err != nil {
		return map[string]any{
			"available": false,
		}, nil
	}

	result := map[string]any{
		"available": true,
	}

	// Parse version from first line: "systemd 252 (252.22-1~deb12u1)"
	firstLine := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Fields(firstLine)
	if len(fields) >= 2 {
		result["version"] = fields[1]
	}

	// Default target.
	out, err = runner.Run(ctx, "systemctl", "get-default")
	if err == nil {
		result["default_target"] = strings.TrimSpace(string(out))
	}

	// Failed units.
	out, err = runner.Run(ctx, "systemctl", "--no-legend", "--plain", "--state=failed", "list-units")
	if err == nil {
		var failed []string
		scanner := bufio.NewScanner(bytes.NewReader(out))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				failed = append(failed, fields[0])
			}
		}
		if failed == nil {
			failed = []string{}
		}
		result["failed_units"] = failed
		result["failed_count"] = len(failed)
	}

	return result, nil
}
