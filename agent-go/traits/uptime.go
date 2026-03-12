// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/host"
)

// UptimeCollector gathers uptime traits: uptime in seconds and the boot
// timestamp in UTC RFC 3339 format.
type UptimeCollector struct{}

func (c *UptimeCollector) Name() string { return "uptime" }

func (c *UptimeCollector) Collect(_ context.Context) (map[string]any, error) {
	uptimeSec, err := host.Uptime()
	if err != nil {
		return nil, err
	}

	bootTime, err := host.BootTime()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"seconds":   uptimeSec,
		"boot_time": time.Unix(int64(bootTime), 0).UTC().Format(time.RFC3339),
	}, nil
}
