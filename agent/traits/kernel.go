package traits

import (
	"context"
	"os"
	"runtime"
	"strings"
)

// KernelCollector gathers kernel traits: full version string, release, and hostname.
type KernelCollector struct{}

func (c *KernelCollector) Name() string { return "kernel" }

func (c *KernelCollector) Collect(_ context.Context) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return map[string]any{}, nil
	}

	result := make(map[string]any)

	if data, err := os.ReadFile("/proc/version"); err == nil {
		result["version"] = strings.TrimSpace(string(data))
	}

	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		result["release"] = strings.TrimSpace(string(data))
	}

	if data, err := os.ReadFile("/proc/sys/kernel/hostname"); err == nil {
		result["hostname"] = strings.TrimSpace(string(data))
	}

	return result, nil
}
