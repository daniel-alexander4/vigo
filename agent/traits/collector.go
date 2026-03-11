// Package traits provides pluggable trait collectors that gather system
// information from managed thralls. Traits are used in Go templates as
// .Traits.os.family, .Traits.hardware.cpu_count, etc.
package traits

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// collectorTimeout is the maximum time a single trait collector may run.
const collectorTimeout = 5 * time.Second

// Collector is the interface for pluggable trait collectors. Each collector
// gathers a category of system traits and returns them as a flat map.
type Collector interface {
	// Name returns the top-level key under which this collector's traits are stored.
	Name() string
	// Collect gathers traits and returns them as a string-keyed map.
	Collect(ctx context.Context) (map[string]any, error)
}

// collectorResult holds the output of a single collector goroutine.
type collectorResult struct {
	name  string
	traits map[string]any
}

// CollectAll aggregates traits from all provided collectors in parallel.
// Each collector runs in its own goroutine. If a collector fails, its error
// is logged and collection continues with the remaining collectors.
func CollectAll(ctx context.Context, collectors []Collector) (map[string]any, error) {
	ch := make(chan collectorResult, len(collectors))
	var wg sync.WaitGroup

	for _, c := range collectors {
		wg.Add(1)
		go func(c Collector) {
			defer wg.Done()
			collCtx, cancel := context.WithTimeout(ctx, collectorTimeout)
			defer cancel()
			traits, err := c.Collect(collCtx)
			if err != nil {
				slog.Warn("trait collector failed",
					"collector", c.Name(),
					"error", err,
				)
				return
			}
			ch <- collectorResult{name: c.Name(), traits: traits}
		}(c)
	}

	wg.Wait()
	close(ch)

	result := make(map[string]any, len(collectors))
	for r := range ch {
		result[r.name] = r.traits
	}
	return result, nil
}

// DefaultCollectors returns the standard set of trait collectors.
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
