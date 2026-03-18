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

// ---------------------------------------------------------------------------
// Color helpers — centralized ANSI output for all CLI commands.
// Respects NO_COLOR env var (https://no-color.org) and non-terminal stdout.
// ---------------------------------------------------------------------------

var useColor = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

func cReset() string {
	if !useColor {
		return ""
	}
	return "\033[0m"
}
func cBold() string {
	if !useColor {
		return ""
	}
	return "\033[1m"
}
func cDim() string {
	if !useColor {
		return ""
	}
	return "\033[2m"
}
func cRed() string {
	if !useColor {
		return ""
	}
	return "\033[31m"
}
func cGreen() string {
	if !useColor {
		return ""
	}
	return "\033[32m"
}
func cYellow() string {
	if !useColor {
		return ""
	}
	return "\033[33m"
}
func cCyan() string {
	if !useColor {
		return ""
	}
	return "\033[36m"
}

// bold wraps text in bold.
func bold(s string) string { return cBold() + s + cReset() }

// dim wraps text in dim.
func dim(s string) string { return cDim() + s + cReset() }

// green wraps text in green.
func green(s string) string { return cGreen() + s + cReset() }

// red wraps text in red.
func red(s string) string { return cRed() + s + cReset() }

// yellow wraps text in yellow.
func yellow(s string) string { return cYellow() + s + cReset() }

// cyan wraps text in cyan.
func cyan(s string) string { return cCyan() + s + cReset() }

// header wraps a table header in bold cyan.
func header(s string) string { return cBold() + cCyan() + s + cReset() }

// colorStatus applies color to compliance/run status strings.
func colorStatus(s string) string {
	switch strings.ToLower(s) {
	case "compliant", "ok", "success", "complete", "healthy", "present", "running":
		return green(s)
	case "drifted", "warn", "warning", "degraded", "pending", "dispatched":
		return yellow(s)
	case "failing", "fail", "failed", "error", "unreachable", "timeout", "rejected":
		return red(s)
	case "stale", "unknown", "offline", "skipped", "skip":
		return dim(s)
	default:
		return s
	}
}

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
