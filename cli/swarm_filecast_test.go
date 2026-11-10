// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestFilecastDistribute_RequiresTarget pins the guardrail that
// `vigocli swarm filecast distribute` never falls back to a fleet-wide
// push: an empty --target is a hard error (with examples), checked
// before any file is opened or any request is made.
func TestFilecastDistribute_RequiresTarget(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("target", "", "")

	err := runFilecastDistribute(cmd, []string{"/path/never/opened"})
	if err == nil {
		t.Fatal("expected an error when --target is empty")
	}
	if !strings.Contains(err.Error(), "--target is required") {
		t.Errorf("error should mention --target is required; got: %v", err)
	}
}
