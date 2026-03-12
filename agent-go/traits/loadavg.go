// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"

	"github.com/shirou/gopsutil/v4/load"
)

// LoadAvgCollector gathers system load averages (1, 5, and 15 minute).
type LoadAvgCollector struct{}

func (c *LoadAvgCollector) Name() string { return "loadavg" }

func (c *LoadAvgCollector) Collect(_ context.Context) (map[string]any, error) {
	avg, err := load.Avg()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"load1":  avg.Load1,
		"load5":  avg.Load5,
		"load15": avg.Load15,
	}, nil
}
