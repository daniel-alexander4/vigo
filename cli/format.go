// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
func cBlue() string {
	if !useColor {
		return ""
	}
	return "\033[34m"
}
func cCyan() string {
	if !useColor {
		return ""
	}
	return "\033[36m"
}
func cStrike() string {
	if !useColor {
		return ""
	}
	return "\033[9m"
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

// blue wraps text in blue.
func blue(s string) string { return cBlue() + s + cReset() }

// strike wraps text in strikethrough red (for excluded items).
func strike(s string) string { return cStrike() + cRed() + s + cReset() }

// amber wraps text in amber/gold (#d4a017) — used for module labels.
func amber(s string) string {
	if !useColor {
		return s
	}
	return "\033[38;2;212;160;23m" + s + cReset()
}

// slateBlue wraps text in slate blue (#7c9cc5) — used for var labels.
func slateBlue(s string) string {
	if !useColor {
		return s
	}
	return "\033[38;2;124;156;197m" + s + cReset()
}

// magenta wraps text in soft magenta (#c07ec0) — used for match labels.
func magenta(s string) string {
	if !useColor {
		return s
	}
	return "\033[38;2;192;126;192m" + s + cReset()
}

// medGray wraps text in medium gray (#888) — visible on both dark and light backgrounds.
func medGray(s string) string {
	if !useColor {
		return s
	}
	return "\033[38;2;136;136;136m" + s + cReset()
}

// teal wraps text in teal (#2da5a5) — brighter than ANSI cyan, visible on light backgrounds.
func teal(s string) string {
	if !useColor {
		return s
	}
	return "\033[38;2;45;165;165m" + s + cReset()
}

// header wraps a table header in bold cyan.
func header(s string) string { return cBold() + cCyan() + s + cReset() }

// colorStatus applies color to compliance/run status strings.
func colorStatus(s string) string {
	switch strings.ToLower(s) {
	case "converged", "ok", "success", "complete", "healthy", "present", "running":
		return green(s)
	case "recurring drift", "lost contact", "warn", "warning", "degraded", "pending", "dispatched":
		return yellow(s)
	case "errors", "fail", "failed", "error", "unreachable", "timeout", "rejected":
		return red(s)
	case "no data", "offline", "skipped", "skip":
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

// requireArgs returns a cobra.PositionalArgs validator that shows usage + examples
// when not enough args are provided, instead of the default terse error.
func requireArgs(min int, noun string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min {
			msg := fmt.Sprintf("requires %s", noun)
			if cmd.Example != "" {
				return fmt.Errorf("%s\n\nUsage:\n  %s\n\nExamples:\n%s", msg, cmd.UseLine(), cmd.Example)
			}
			return fmt.Errorf("%s\n\nUsage:\n  %s", msg, cmd.UseLine())
		}
		return nil
	}
}

// confirmAction prompts the user for confirmation before a destructive operation.
// Returns true if the user types "y" or "yes". Skips the prompt if --force is set.
func confirmAction(msg string) bool {
	fmt.Printf("%s [y/N] ", msg)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	return strings.TrimSpace(strings.ToLower(answer)) == "y" || strings.TrimSpace(strings.ToLower(answer)) == "yes"
}
