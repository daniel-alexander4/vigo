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
// Color helpers — Catppuccin Mocha palette with 24-bit RGB.
// All colors are explicit RGB values so output looks identical regardless of
// terminal theme. Respects NO_COLOR (https://no-color.org) and non-terminal stdout.
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

// Catppuccin Mocha RGB escape sequences.
const (
	catReset  = "\033[0m"
	catBold   = "\033[1m"
	catStrike = "\033[9m"

	// Core palette — 24-bit RGB, not affected by terminal color schemes.
	catRed       = "\033[38;2;243;139;168m" // #f38ba8  Red
	catMaroon    = "\033[38;2;235;160;172m" // #eba0ac  Maroon
	catPeach     = "\033[38;2;250;179;135m" // #fab387  Peach
	catYellow    = "\033[38;2;249;226;175m" // #f9e2af  Yellow
	catGreen     = "\033[38;2;166;227;161m" // #a6e3a1  Green
	catTeal      = "\033[38;2;148;226;213m" // #94e2d5  Teal
	catSky       = "\033[38;2;137;220;235m" // #89dceb  Sky
	catSapphire  = "\033[38;2;116;199;236m" // #74c7ec  Sapphire
	catBlue      = "\033[38;2;137;180;250m" // #89b4fa  Blue
	catLavender  = "\033[38;2;180;190;254m" // #b4befe  Lavender
	catMauve     = "\033[38;2;203;166;247m" // #cba6f7  Mauve
	catPink      = "\033[38;2;245;194;231m" // #f5c2e7  Pink
	catFlamingo  = "\033[38;2;242;205;205m" // #f2cdcd  Flamingo
	catRosewater = "\033[38;2;245;224;220m" // #f5e0dc  Rosewater
	catText      = "\033[38;2;205;214;244m" // #cdd6f4  Text
	catSubtext1  = "\033[38;2;186;194;222m" // #bac2de  Subtext1
	catSubtext0  = "\033[38;2;166;173;200m" // #a6adc8  Subtext0
	catOverlay2  = "\033[38;2;147;153;178m" // #9399b2  Overlay2
	catOverlay1  = "\033[38;2;127;132;156m" // #7f849c  Overlay1
	catOverlay0  = "\033[38;2;108;112;134m" // #6c7086  Overlay0
)

func cReset() string {
	if !useColor {
		return ""
	}
	return catReset
}
func cBold() string {
	if !useColor {
		return ""
	}
	return catBold
}

// bold wraps text in bold.
func bold(s string) string { return cBold() + s + cReset() }

// dim wraps text in Overlay1 — visible on dark backgrounds unlike ANSI dim.
func dim(s string) string {
	if !useColor {
		return s
	}
	return catOverlay1 + s + catReset
}

// green wraps text in Catppuccin Green.
func green(s string) string {
	if !useColor {
		return s
	}
	return catGreen + s + catReset
}

// red wraps text in Catppuccin Red.
func red(s string) string {
	if !useColor {
		return s
	}
	return catRed + s + catReset
}

// yellow wraps text in Catppuccin Yellow.
func yellow(s string) string {
	if !useColor {
		return s
	}
	return catYellow + s + catReset
}

// cyan wraps text in Catppuccin Sky.
func cyan(s string) string {
	if !useColor {
		return s
	}
	return catSky + s + catReset
}

// blue wraps text in Catppuccin Blue.
func blue(s string) string {
	if !useColor {
		return s
	}
	return catBlue + s + catReset
}

// strike wraps text in strikethrough red (for excluded items).
func strike(s string) string {
	if !useColor {
		return s
	}
	return catStrike + catRed + s + catReset
}

// amber wraps text in Catppuccin Peach — used for module labels.
func amber(s string) string {
	if !useColor {
		return s
	}
	return catPeach + s + catReset
}

// slateBlue wraps text in Catppuccin Lavender — used for var labels.
func slateBlue(s string) string {
	if !useColor {
		return s
	}
	return catLavender + s + catReset
}

// magenta wraps text in Catppuccin Pink — used for match labels.
func magenta(s string) string {
	if !useColor {
		return s
	}
	return catPink + s + catReset
}

// medGray wraps text in Catppuccin Overlay2 — secondary/annotation text.
func medGray(s string) string {
	if !useColor {
		return s
	}
	return catOverlay2 + s + catReset
}

// teal wraps text in Catppuccin Teal — brighter, high-contrast accent.
func teal(s string) string {
	if !useColor {
		return s
	}
	return catTeal + s + catReset
}

// mauve wraps text in Catppuccin Mauve — used for purple accents.
func mauve(s string) string {
	if !useColor {
		return s
	}
	return catMauve + s + catReset
}

// sapphire wraps text in Catppuccin Sapphire — used for informational accents.
func sapphire(s string) string {
	if !useColor {
		return s
	}
	return catSapphire + s + catReset
}

// header wraps a table header in bold Sapphire.
func header(s string) string {
	if !useColor {
		return s
	}
	return catBold + catSapphire + s + catReset
}

// colorStatus applies Catppuccin colors to compliance/run status strings.
func colorStatus(s string) string {
	switch strings.ToLower(s) {
	case "converged", "ok", "success", "complete", "healthy", "present", "running":
		return green(s)
	case "relapsed", "lost contact", "warn", "warning", "degraded", "pending", "dispatched":
		return yellow(s)
	case "diverged", "errors", "fail", "failed", "error", "unreachable", "timeout", "rejected":
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

// requireArgs returns a cobra.PositionalArgs validator that returns a
// descriptive error when not enough args are provided. Cobra prints
// usage automatically (SilenceUsage is false during arg validation).
func requireArgs(min int, noun string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min {
			return fmt.Errorf("requires %s", noun)
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
