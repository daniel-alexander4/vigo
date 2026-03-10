package traits

import (
	"context"

	"github.com/shirou/gopsutil/v4/mem"
)

// MemoryCollector gathers detailed memory and swap traits.
type MemoryCollector struct{}

func (c *MemoryCollector) Name() string { return "memory" }

func (c *MemoryCollector) Collect(_ context.Context) (map[string]any, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	s, err := mem.SwapMemory()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"total":        v.Total,
		"available":    v.Available,
		"used":         v.Used,
		"free":         v.Free,
		"percent":      v.UsedPercent,
		"swap_total":   s.Total,
		"swap_used":    s.Used,
		"swap_free":    s.Free,
		"swap_percent": s.UsedPercent,
	}, nil
}
