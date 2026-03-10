// Package nails provides pluggable nail collectors that gather system
// information from managed nodes. Nails are used in Go templates as
// .Nails.os.family, .Nails.hardware.cpu_count, etc.
package nails

import (
	"context"
	"log/slog"
)

// Collector is the interface for pluggable nail collectors. Each collector
// gathers a category of system nails and returns them as a flat map.
type Collector interface {
	// Name returns the top-level key under which this collector's nails are stored.
	Name() string
	// Collect gathers nails and returns them as a string-keyed map.
	Collect(ctx context.Context) (map[string]any, error)
}

// CollectAll aggregates nails from all provided collectors into a nested map
// keyed by each collector's Name(). If a collector fails, its error is logged
// and collection continues with the remaining collectors.
func CollectAll(ctx context.Context, collectors []Collector) (map[string]any, error) {
	result := make(map[string]any)
	for _, c := range collectors {
		nails, err := c.Collect(ctx)
		if err != nil {
			slog.Warn("nail collector failed",
				"collector", c.Name(),
				"error", err,
			)
			continue
		}
		result[c.Name()] = nails
	}
	return result, nil
}

// DefaultCollectors returns the standard set of nail collectors.
func DefaultCollectors() []Collector {
	return []Collector{
		&OSCollector{},
		&HardwareCollector{},
		&NetworkCollector{},
		&PackageCollector{},
		&TimeCollector{},
		&IdentityCollector{},
		&VirtualCollector{},
		&UptimeCollector{},
		&LoadAvgCollector{},
		&MountpointsCollector{},
		&ProcessorsCollector{},
		&DMICollector{},
		&CloudCollector{},
		&FilesystemCollector{},
		&KernelCollector{},
		&UsersCollector{},
		&MemoryCollector{},
		&BlockDevicesCollector{},
		&DockerCollector{},
		&SystemdCollector{},
		&SELinuxCollector{},
		&TimezoneCollector{},
		&LocaleCollector{},
		&SwapCollector{},
		&GPUCollector{},
	}
}
