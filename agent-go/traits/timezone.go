// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// TimezoneCollector gathers timezone traits: name, abbreviation, UTC offset.
type TimezoneCollector struct{}

func (c *TimezoneCollector) Name() string { return "timezone" }

func (c *TimezoneCollector) Collect(_ context.Context) (map[string]any, error) {
	result := make(map[string]any)

	now := time.Now()
	zone, offset := now.Zone()
	result["abbreviation"] = zone
	result["utc_offset_seconds"] = offset

	// Hours offset as string like "+05:30" or "-08:00".
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	if minutes < 0 {
		minutes = -minutes
	}
	result["utc_offset"] = formatOffset(hours, minutes)

	// Try to read the IANA timezone name from /etc/timezone or the symlink.
	if name := readTimezoneFile(); name != "" {
		result["name"] = name
	} else if name := readTimezoneLink(); name != "" {
		result["name"] = name
	} else {
		result["name"] = zone
	}

	return result, nil
}

func readTimezoneFile() string {
	data, err := os.ReadFile("/etc/timezone")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readTimezoneLink() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	// /etc/localtime -> /usr/share/zoneinfo/America/New_York
	const prefix = "zoneinfo/"
	if idx := strings.Index(target, prefix); idx >= 0 {
		return target[idx+len(prefix):]
	}
	return ""
}

func formatOffset(hours, minutes int) string {
	sign := "+"
	if hours < 0 {
		sign = "-"
		hours = -hours
	}
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}
