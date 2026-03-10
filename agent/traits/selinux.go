package traits

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// SELinuxCollector gathers SELinux traits: enabled, mode, policy type.
type SELinuxCollector struct{}

func (c *SELinuxCollector) Name() string { return "selinux" }

func (c *SELinuxCollector) Collect(ctx context.Context) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return map[string]any{"enabled": false}, nil
	}

	// Quick check: /etc/selinux/config exists?
	if _, err := os.Stat("/etc/selinux/config"); os.IsNotExist(err) {
		return map[string]any{"enabled": false}, nil
	}

	runner := &cmdrun.Default{}

	// Try getenforce.
	out, err := runner.Run(ctx, "getenforce")
	if err != nil {
		return map[string]any{"enabled": false}, nil
	}

	mode := strings.TrimSpace(string(out))
	enabled := !strings.EqualFold(mode, "disabled")

	result := map[string]any{
		"enabled": enabled,
		"mode":    strings.ToLower(mode),
	}

	// Get policy type from sestatus.
	out, err = runner.Run(ctx, "sestatus")
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "Loaded policy name:") || strings.HasPrefix(line, "Policy from config file:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					result["policy"] = strings.TrimSpace(parts[1])
					break
				}
			}
		}
	}

	return result, nil
}
