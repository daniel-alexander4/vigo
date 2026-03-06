package facts

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// HardwareCollector gathers hardware facts: CPU count, memory in MB,
// and disk usage per mount point in GB.
type HardwareCollector struct{}

// Name returns "hardware".
func (c *HardwareCollector) Name() string { return "hardware" }

// Collect returns hardware facts using gopsutil cpu, mem, and disk packages.
func (c *HardwareCollector) Collect(_ context.Context) (map[string]any, error) {
	cpuCount, err := cpu.Counts(true) // logical CPUs
	if err != nil {
		return nil, err
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	memoryMB := memInfo.Total / 1024 / 1024

	partitions, err := disk.Partitions(false) // physical only
	if err != nil {
		return nil, err
	}

	diskGB := make(map[string]uint64)
	for _, p := range partitions {
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		diskGB[p.Mountpoint] = usage.Total / 1024 / 1024 / 1024
	}

	return map[string]any{
		"cpu_count": cpuCount,
		"memory_mb": memoryMB,
		"disk_gb":   diskGB,
	}, nil
}
