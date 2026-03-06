// Package facts provides pluggable fact collectors that gather system
// information from managed nodes. Facts are used in Go templates as
// .Facts.os.family, .Facts.hardware.cpu_count, etc.
package facts

import "context"

// Collector is the interface for pluggable fact collectors. Each collector
// gathers a category of system facts and returns them as a flat map.
type Collector interface {
	// Name returns the top-level key under which this collector's facts are stored.
	Name() string
	// Collect gathers facts and returns them as a string-keyed map.
	Collect(ctx context.Context) (map[string]any, error)
}

// CollectAll aggregates facts from all provided collectors into a nested map
// keyed by each collector's Name(). If a collector fails, its error is logged
// and collection continues with the remaining collectors.
func CollectAll(ctx context.Context, collectors []Collector) (map[string]any, error) {
	return nil, nil
}

// DefaultCollectors returns the standard set of fact collectors.
func DefaultCollectors() []Collector {
	return nil
}
