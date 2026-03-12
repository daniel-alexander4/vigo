// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"sync"
)

// VolatileCollectors returns collectors whose data changes frequently
// (every second or minute) and must be collected on every cycle.
func VolatileCollectors() []Collector {
	return []Collector{
		&TimeCollector{},
		&UptimeCollector{},
		&LoadAvgCollector{},
		&MountpointsCollector{},
		&FilesystemCollector{},
		&UsersCollector{},
		&MemoryCollector{},
		&SwapCollector{},
	}
}

// StableCollectors returns collectors whose data rarely changes (OS info,
// hardware, network interfaces, packages, etc.) and can be cached across
// multiple check-in cycles.
func StableCollectors() []Collector {
	return []Collector{
		&OSCollector{},
		&HardwareCollector{},
		&NetworkCollector{},
		&PackageCollector{},
		&IdentityCollector{},
		&VirtualCollector{},
		&ProcessorsCollector{},
		&DMICollector{},
		&CloudCollector{},
		&KernelCollector{},
		&BlockDevicesCollector{},
		&DockerCollector{},
		&SystemdCollector{},
		&SELinuxCollector{},
		&TimezoneCollector{},
		&LocaleCollector{},
		&GPUCollector{},
	}
}

// CachedCollectors wraps volatile and stable collector sets, caching stable
// results for a configurable number of cycles. This avoids running expensive
// collectors (especially PackageCollector at 100-300ms) on every check-in.
type CachedCollectors struct {
	volatile []Collector
	stable   []Collector

	mu       sync.Mutex
	cached   map[string]any // cached stable collector results
	cycle    int
	interval int // run stable collectors every N cycles
}

// NewCachedCollectors creates a CachedCollectors that runs stable collectors
// every stableInterval cycles. On cycle 0 (first call), all collectors run.
func NewCachedCollectors(stableInterval int) *CachedCollectors {
	if stableInterval < 1 {
		stableInterval = 1
	}
	return &CachedCollectors{
		volatile: VolatileCollectors(),
		stable:   StableCollectors(),
		interval: stableInterval,
	}
}

// CollectAll gathers traits from all collectors in parallel. Volatile collectors
// run every call. Stable collectors run on the first call and then every N
// calls (where N = stableInterval), reusing cached results on other cycles.
func (cc *CachedCollectors) CollectAll(ctx context.Context) (map[string]any, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Determine whether stable collectors need to run this cycle.
	runStable := cc.cached == nil || cc.cycle%cc.interval == 0
	cc.cycle++

	// Collect from all active collector sets in parallel.
	var collectors []Collector
	collectors = append(collectors, cc.volatile...)
	if runStable {
		collectors = append(collectors, cc.stable...)
	}

	collected, err := CollectAll(ctx, collectors)
	if err != nil {
		return nil, err
	}

	// Update the stable cache with fresh results.
	if runStable {
		cc.cached = make(map[string]any)
		for _, c := range cc.stable {
			if v, ok := collected[c.Name()]; ok {
				cc.cached[c.Name()] = v
			}
		}
	}

	// Start with volatile results, then merge cached stable results.
	result := make(map[string]any, len(cc.volatile)+len(cc.stable))
	for k, v := range collected {
		result[k] = v
	}
	for k, v := range cc.cached {
		if _, exists := result[k]; !exists {
			result[k] = v
		}
	}

	return result, nil
}
