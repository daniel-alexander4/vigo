// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/uifmt"
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

// Catppuccin Mocha 24-bit ANSI escape sequences, built from the canonical
// hex constants in server/uifmt. uifmt owns the color values (data); this
// file owns the terminal rendering of those values (CLI-specific).
const (
	catReset  = "\033[0m"
	catBold   = "\033[1m"
	catStrike = "\033[9m"
)

// ansi converts a "#rrggbb" hex string into a 24-bit ANSI foreground
// escape sequence. Panics on malformed hex — input is always a uifmt
// constant, so a panic at init time is the right failure mode.
func ansi(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		panic("uifmt hex must be #rrggbb: " + hex)
	}
	r, err1 := strconv.ParseUint(hex[1:3], 16, 8)
	g, err2 := strconv.ParseUint(hex[3:5], 16, 8)
	b, err3 := strconv.ParseUint(hex[5:7], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		panic("uifmt hex parse failed: " + hex)
	}
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

var (
	catRed      = ansi(uifmt.HexRed)
	catPeach    = ansi(uifmt.HexPeach)
	catYellow   = ansi(uifmt.HexYellow)
	catGreen    = ansi(uifmt.HexGreen)
	catTeal     = ansi(uifmt.HexTeal)
	catSky      = ansi(uifmt.HexSky)
	catSapphire = ansi(uifmt.HexSapphire)
	catBlue     = ansi(uifmt.HexBlue)
	catLavender = ansi(uifmt.HexLavender)
	catPink     = ansi(uifmt.HexPink)
	catOverlay2 = ansi(uifmt.HexOverlay2)
	catOverlay1 = ansi(uifmt.HexOverlay1)
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

// header wraps a table header in bold Sapphire.
func header(s string) string {
	if !useColor {
		return s
	}
	return catBold + catSapphire + s + catReset
}

// colorStatus applies Catppuccin colors to compliance/run status strings.
// Convergence buckets dispatch through convergence.ClassifyStatus so the
// vocabulary stays consistent with the SoT; non-convergence status strings
// (run lifecycle, generic ANSI vocab, reachability) keep their local
// fallthroughs since they are not owned by server/convergence.
func colorStatus(s string) string {
	switch convergence.ClassifyStatus(s) {
	case convergence.StatusConverged:
		return green(s)
	case convergence.StatusDegraded:
		return yellow(s)
	case convergence.StatusFailed:
		return red(s)
	}
	switch strings.ToLower(s) {
	case "ok", "success", "complete", "healthy", "present", "running":
		return green(s)
	case "offline", "warn", "warning", "pending", "dispatched":
		return yellow(s)
	case "fail", "error", "unreachable", "timeout", "rejected", "diverged":
		return red(s)
	case "changed":
		return yellow(s)
	case "no data", "skipped", "skip":
		return dim(s)
	default:
		return s
	}
}

// formatTimestamp formats an RFC3339 or ISO8601 timestamp to short form.
// Parsing is shared via uifmt.ParseTimestamp; the output layout is
// CLI-specific.
func formatTimestamp(ts string) string {
	if ts == "" {
		return "-"
	}
	if t, err := uifmt.ParseTimestamp(ts); err == nil {
		return t.Format("2006-01-02 15:04")
	}
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

// confirmAction asks the operator to confirm a destructive operation.
// It returns (true, nil) to proceed, (false, nil) when the operator
// declines interactively, and (false, err) when confirmation is
// impossible — a non-TTY stdin with no --yes — so scripts fail loudly
// instead of silently treating the unanswered prompt as "no" (which read
// as a successful no-op). Pass --yes (registered via addConfirmFlag) to
// proceed non-interactively.
func confirmAction(cmd *cobra.Command, msg string) (bool, error) {
	if cmd != nil {
		if f := cmd.Flags().Lookup("yes"); f != nil && f.Value.String() == "true" {
			return true, nil
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("refusing to prompt for confirmation: stdin is not a terminal — re-run with --yes to proceed non-interactively")
	}
	fmt.Printf("%s [y/N] ", msg)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.TrimSpace(strings.ToLower(answer))
	return a == "y" || a == "yes", nil
}
