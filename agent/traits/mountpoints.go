package traits

import (
	"context"

	"github.com/shirou/gopsutil/v4/disk"
)

// MountpointsCollector gathers per-mount-point traits: device, filesystem type,
// total/used/available in GB, and usage percentage.
type MountpointsCollector struct{}

func (c *MountpointsCollector) Name() string { return "mountpoints" }

func (c *MountpointsCollector) Collect(_ context.Context) (map[string]any, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}

	mounts := make(map[string]any)
	for _, p := range partitions {
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		mounts[p.Mountpoint] = map[string]any{
			"device":      p.Device,
			"fstype":      p.Fstype,
			"total_gb":    usage.Total / 1024 / 1024 / 1024,
			"used_gb":     usage.Used / 1024 / 1024 / 1024,
			"avail_gb":    usage.Free / 1024 / 1024 / 1024,
			"used_pct":    usage.UsedPercent,
			"inode_pct":   usage.InodesUsedPercent,
		}
	}

	return map[string]any{
		"mounts": mounts,
	}, nil
}
