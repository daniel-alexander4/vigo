// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
)

// TestReadTokenInput: set-token must accept the token off argv — from a
// no-echo prompt on a terminal or from stdin on a pipe — so it never has to
// sit in shell history or the process list.
func TestReadTokenInput(t *testing.T) {
	noPrompt := func() ([]byte, error) {
		t.Fatal("prompted when it should not have")
		return nil, nil
	}

	got, err := readTokenInput(nil, strings.NewReader("vgot_piped\nignored\n"), false, noPrompt)
	if err != nil || got != "vgot_piped" {
		t.Fatalf("stdin: got %q, %v", got, err)
	}

	prompted := false
	got, err = readTokenInput(nil, strings.NewReader(""), true, func() ([]byte, error) {
		prompted = true
		return []byte("  vgot_typed  "), nil
	})
	if err != nil || got != "vgot_typed" || !prompted {
		t.Fatalf("tty prompt: got %q, %v, prompted=%v", got, err, prompted)
	}

	got, err = readTokenInput([]string{"vgot_arg"}, strings.NewReader("vgot_piped\n"), false, noPrompt)
	if err != nil || got != "vgot_arg" {
		t.Fatalf("arg: got %q, %v", got, err)
	}

	if _, err := readTokenInput(nil, strings.NewReader("\n"), false, noPrompt); err == nil {
		t.Fatal("empty stdin must be refused")
	}

	// The command itself must accept zero arguments.
	if err := authSetTokenCmd.Args(authSetTokenCmd, nil); err != nil {
		t.Fatalf("set-token with no argument rejected: %v", err)
	}
}
