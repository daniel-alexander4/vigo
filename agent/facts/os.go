package facts

import (
	"context"
	"runtime"

	"github.com/shirou/gopsutil/v4/host"
)

// OSCollector gathers operating system facts: family, distro, version,
// kernel version, and architecture.
type OSCollector struct{}

// Name returns "os".
func (c *OSCollector) Name() string { return "os" }

// Collect returns OS facts using gopsutil host.Info and runtime.GOARCH.
func (c *OSCollector) Collect(_ context.Context) (map[string]any, error) {
	info, err := host.Info()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"family":  info.PlatformFamily,
		"distro":  info.Platform,
		"version": info.PlatformVersion,
		"kernel":  info.KernelVersion,
		"arch":    runtime.GOARCH,
	}, nil
}
