// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"os"
	"runtime"
	"strings"
)

// DMICollector gathers BIOS/DMI traits from /sys/class/dmi/id/ on Linux.
// On non-Linux platforms, returns an empty map.
type DMICollector struct{}

func (c *DMICollector) Name() string { return "dmi" }

func (c *DMICollector) Collect(_ context.Context) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return map[string]any{}, nil
	}

	fields := map[string]string{
		"bios_vendor":    "/sys/class/dmi/id/bios_vendor",
		"bios_version":   "/sys/class/dmi/id/bios_version",
		"bios_date":      "/sys/class/dmi/id/bios_date",
		"board_vendor":   "/sys/class/dmi/id/board_vendor",
		"board_name":     "/sys/class/dmi/id/board_name",
		"product_name":   "/sys/class/dmi/id/product_name",
		"product_vendor": "/sys/class/dmi/id/sys_vendor",
		"chassis_type":   "/sys/class/dmi/id/chassis_type",
	}

	result := make(map[string]any)
	for key, path := range fields {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		val := strings.TrimSpace(string(data))
		if val != "" {
			result[key] = val
		}
	}

	return result, nil
}
