// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"

	"github.com/shirou/gopsutil/v4/disk"
)

// FilesystemCollector gathers disk usage traits for all mounted filesystems.
type FilesystemCollector struct{}

func (c *FilesystemCollector) Name() string { return "filesystem" }

func (c *FilesystemCollector) Collect(_ context.Context) (map[string]any, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, err
	}

	fs := make(map[string]any)
	for _, p := range partitions {
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		fs[p.Mountpoint] = map[string]any{
			"total":   usage.Total,
			"used":    usage.Used,
			"free":    usage.Free,
			"percent": usage.UsedPercent,
		}
	}

	return fs, nil
}
