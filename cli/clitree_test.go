// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// These tests walk the LIVE rootCmd cobra tree — every command and
// subcommand wired in via the package's init() functions — and assert the
// structural invariants a regex over cli/*.go source can't reach. The
// source-text audits in test/audit/ (TestEveryCLICommandHasDocPage,
// TestCLICommandCountConsistent) only see top-level rootCmd.AddCommand
// names; a missing Short on `spanner bolt invite`, a dead leaf three levels
// deep, or a usage template that panics all slip past them. Importing
// rootCmd directly (this file is package main) closes that gap.

// walkCommands visits c and every descendant, skipping cobra's
// auto-generated `help` command and any Hidden command (e.g. __complete) —
// neither is operator-authored surface we own.
func walkCommands(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		if sub.Name() == "help" || sub.Hidden {
			continue
		}
		walkCommands(sub, fn)
	}
}

// TestCLITreeWellFormed asserts every command in the tree is structurally
// complete: it has a one-line Short, it isn't a dead leaf (a command with no
// subcommands must be runnable), and its usage template renders without
// panicking. A new subcommand that ships missing any of these turns this red.
func TestCLITreeWellFormed(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		path := c.CommandPath()

		if strings.TrimSpace(c.Short) == "" {
			t.Errorf("%q: empty Short — every command needs a one-line description", path)
		}

		// A leaf (no subcommands) must do something; a parent without a Run
		// is fine — cobra prints its help. A leaf with neither is a command
		// that exists but can't act.
		if !c.HasSubCommands() && c.Run == nil && c.RunE == nil {
			t.Errorf("%q: leaf command has neither Run nor RunE — dead command", path)
		}

		// Exercise the usage/help template across the whole tree. A bad
		// template directive or annotation panics here, not in CI-by-luck.
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		if strings.TrimSpace(c.UsageString()) == "" {
			t.Errorf("%q: empty usage string", path)
		}
	})
}

// unquotedGlobIndex returns the byte index of the first shell-active glob char
// (* or ?) in s that sits outside any single- or double-quoted span, or -1 if
// none. Quote state resets per call, so callers scan one Example line at a time.
func unquotedGlobIndex(s string) int {
	var inSingle, inDouble bool
	for i, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '*', '?':
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}

// TestCLIExamplesShellSafe enforces the copy-paste contract: an operator pastes
// an Example line straight into a shell, so any glob in it (`*.web.*`,
// `os.distro=ubuntu*`) must be quoted — otherwise the shell expands it against
// the cwd and vigocli never sees the pattern the help text promised. The two
// globs in the tree today (the SQL COUNT(*) and task --target "*.web.*") are
// already quoted; this keeps the next one honest.
func TestCLIExamplesShellSafe(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c.Example == "" {
			return
		}
		for _, line := range strings.Split(c.Example, "\n") {
			if i := unquotedGlobIndex(line); i >= 0 {
				t.Errorf("%q: Example has an unquoted shell glob (%q) — quote it so a pasted line reaches vigocli instead of being expanded by the shell:\n    %s",
					c.CommandPath(), string(line[i]), strings.TrimSpace(line))
			}
		}
	})
}

// TestCLINoDuplicateSubcommands guards against two files registering the same
// subcommand name under one parent — the second silently shadows the first in
// cobra's lookup, a class of bug that compiles clean and only shows up as a
// command "not doing what it says."
func TestCLINoDuplicateSubcommands(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		seen := map[string]string{} // name -> first Use seen
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Hidden {
				continue
			}
			name := sub.Name()
			if prior, dup := seen[name]; dup {
				t.Errorf("%q: subcommand %q registered twice (first: %q, second: %q)",
					c.CommandPath(), name, prior, sub.Use)
			}
			seen[name] = sub.Use
		}
	})
}

// exampleCommandLines returns the lines of a command's Example and Long text
// that an operator would paste: those that start with "vigocli ". In Long,
// only indented lines count — a column-0 line is prose ("vigocli communicates
// with…"), an indented one is an example block.
func exampleCommandLines(c *cobra.Command) []string {
	var out []string
	for _, line := range strings.Split(c.Example, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "vigocli ") {
			out = append(out, l)
		}
	}
	for _, line := range strings.Split(c.Long, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "vigocli ") && l != line && strings.TrimLeft(line, " \t") == l {
			out = append(out, l)
		}
	}
	return out
}

// TestCLIExampleLinesPasteClean extends the copy-paste contract to the shape
// of each example line: it must name a command that exists and can run, and
// any description sharing the line must sit behind a `#`. A description run
// on after the command ("…  Show the framework->controls map") is pasted as
// extra arguments, and its `>` creates a file named after the next word.
func TestCLIExampleLinesPasteClean(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		for _, line := range exampleCommandLines(c) {
			cmdPart := line
			if i := strings.Index(cmdPart, "#"); i >= 0 {
				cmdPart = cmdPart[:i]
			}
			cmdPart = strings.TrimRight(cmdPart, " ")
			if strings.Contains(cmdPart, "  ") {
				t.Errorf("%q: example line carries text after a double space without a `#` — a paste passes it as arguments:\n    %s", c.CommandPath(), line)
			}
			var words []string
			for _, w := range strings.Fields(cmdPart)[1:] {
				if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "|><&;$'\"") {
					break
				}
				words = append(words, w)
			}
			// A group with nothing after it prints its help, which is a
			// fine thing to paste; a group followed by more words means a
			// subcommand that does not exist.
			found, rest, err := rootCmd.Find(words)
			if err != nil || found == nil || (!found.Runnable() && len(rest) > 0) {
				name := "<none>"
				if found != nil {
					name = found.CommandPath()
				}
				t.Errorf("%q: example line does not reach a runnable command (resolved %s, err %v):\n    %s", c.CommandPath(), name, err, line)
			}
		}
	})
}
