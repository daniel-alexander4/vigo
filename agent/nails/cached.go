package nails

import (
	"context"
	"log/slog"
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

// CollectAll gathers nails from all collectors. Volatile collectors run every
// call. Stable collectors run on the first call and then every N calls
// (where N = stableInterval), reusing cached results on other cycles.
func (cc *CachedCollectors) CollectAll(ctx context.Context) (map[string]any, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Determine whether stable collectors need to run this cycle.
	runStable := cc.cached == nil || cc.cycle%cc.interval == 0
	cc.cycle++

	result := make(map[string]any)

	// Always run volatile collectors.
	for _, c := range cc.volatile {
		nails, err := c.Collect(ctx)
		if err != nil {
			slog.Warn("nail collector failed", "collector", c.Name(), "error", err)
			continue
		}
		result[c.Name()] = nails
	}

	if runStable {
		// Run stable collectors and cache results.
		cc.cached = make(map[string]any)
		for _, c := range cc.stable {
			nails, err := c.Collect(ctx)
			if err != nil {
				slog.Warn("nail collector failed", "collector", c.Name(), "error", err)
				continue
			}
			cc.cached[c.Name()] = nails
		}
	}

	// Merge cached stable results into the output.
	for k, v := range cc.cached {
		result[k] = v
	}

	return result, nil
}
