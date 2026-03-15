// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"fmt"
	"os"
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

// confirmAction prompts the user for confirmation before a destructive operation.
// Returns true if the user types "y" or "yes". Skips the prompt if --force is set.
func confirmAction(msg string) bool {
	fmt.Printf("%s [y/N] ", msg)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	return strings.TrimSpace(strings.ToLower(answer)) == "y" || strings.TrimSpace(strings.ToLower(answer)) == "yes"
}
