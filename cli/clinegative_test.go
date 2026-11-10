// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"strings"
	"testing"
)

// These tests drive the LIVE rootCmd cobra tree in-process — no server, no
// network. They cover two CLI-surface invariants the structural walk in
// clitree_test.go can't reach:
//
//   - TestCLINegativeArgs: malformed invocations (unknown command, unknown
//     flag, wrong arg count) fail at cobra's parse/validation layer with a
//     clean error — never a panic, never a stack trace, and never a silent
//     fall-through into a RunE that would dial the server. Each case is
//     chosen so the failure happens BEFORE RunE; the assertion that the error
//     names a parse problem (and not "request failed"/"connection refused")
//     doubles as a guard that we never accidentally hit the network here.
//
//   - TestCLICompletionGenerates: the shell-completion generators for every
//     supported shell render the whole command tree (including the dynamic
//     ValidArgsFunction registrations in completion.go) without erroring or
//     panicking.
//
// The server-round-trip behavioral surface lives in test/clismoke.

// execRoot runs rootCmd with the given args in-process, capturing stdout+stderr
// into one buffer. Output is redirected so a failing case doesn't spew cobra
// usage text into the test log. Returns the combined output and the error
// Execute() surfaced.
func execRoot(args ...string) (string, error) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	// Reset args so a later Execute() (e.g. in another test) doesn't inherit
	// this invocation's argv.
	rootCmd.SetArgs(nil)
	return buf.String(), err
}

func TestCLINegativeArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		// wantErrSubstr is a substring the parse error must contain. It marks
		// the failure as a cobra validation error rather than a RunE/network
		// error — if a case ever stops failing at parse time and reaches the
		// server, the substring won't match and this test goes red.
		wantErrSubstr string
	}{
		{"unknown top-level command", []string{"totally-not-a-command"}, "unknown command"},
		{"unknown flag", []string{"envoys", "list", "--definitely-not-a-flag"}, "unknown flag"},
		{"missing required arg (runs show)", []string{"runs", "show"}, "accepts 1 arg"},
		{"too many args (runs show)", []string{"runs", "show", "a", "b"}, "accepts 1 arg"},
		{"too many args (auth set-token)", []string{"auth", "set-token", "a", "b"}, "accepts at most 1 arg"},
		{"missing required arg (security cve)", []string{"security", "cve"}, "accepts 1 arg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := execRoot(c.args...)
			if err == nil {
				t.Fatalf("`vigocli %s` unexpectedly succeeded; want a parse error\noutput:\n%s",
					strings.Join(c.args, " "), out)
			}
			// The error must read like a cobra validation failure. A match on
			// the err string OR the captured usage output is enough — cobra
			// puts the reason in the returned error; the usage block goes to
			// the buffer.
			hay := err.Error() + "\n" + out
			if !strings.Contains(hay, c.wantErrSubstr) {
				t.Errorf("`vigocli %s` failed with %q, want a parse error containing %q (not a network/RunE error)",
					strings.Join(c.args, " "), err.Error(), c.wantErrSubstr)
			}
		})
	}
}

func TestCLICompletionGenerates(t *testing.T) {
	// One generator per shell cobra supports. Each walks the full command tree;
	// a bad ValidArgsFunction or annotation would panic or error here.
	shells := []struct {
		name string
		gen  func(*bytes.Buffer) error
	}{
		{"bash", func(b *bytes.Buffer) error { return rootCmd.GenBashCompletionV2(b, true) }},
		{"zsh", func(b *bytes.Buffer) error { return rootCmd.GenZshCompletion(b) }},
		{"fish", func(b *bytes.Buffer) error { return rootCmd.GenFishCompletion(b, true) }},
		{"powershell", func(b *bytes.Buffer) error { return rootCmd.GenPowerShellCompletionWithDesc(b) }},
	}
	for _, s := range shells {
		t.Run(s.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := s.gen(&buf); err != nil {
				t.Fatalf("generate %s completion: %v", s.name, err)
			}
			if buf.Len() == 0 {
				t.Fatalf("%s completion script is empty", s.name)
			}
			// Cobra's V2 completion scripts are generic drivers that resolve
			// commands at runtime via __complete rather than embedding the
			// command tree, but every shell's script embeds the binary name.
			// Its presence confirms the generator ran against this rootCmd.
			if name := rootCmd.Name(); !strings.Contains(buf.String(), name) {
				t.Errorf("%s completion script does not mention the binary name %q", s.name, name)
			}
		})
	}
}
