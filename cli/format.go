// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"strings"
	"time"
)

// formatTimestamp formats an RFC3339 or ISO8601 timestamp to short form.
func formatTimestamp(ts string) string {
	if ts == "" {
		return "-"
	}
	// Try parsing various timestamp formats from grpc-gateway JSON
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	// Return raw if unparseable, trimming any trailing Zs
	return strings.TrimSpace(ts)
}
