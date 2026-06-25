// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestConfirmActionYesFlag: --yes proceeds without prompting.
func TestConfirmActionYesFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	ok, err := confirmAction(cmd, "proceed?")
	if err != nil || !ok {
		t.Fatalf("with --yes: got (%v, %v), want (true, nil)", ok, err)
	}
}

// TestConfirmActionNonTTYErrors guards the regression that started this:
// under `go test` stdin is not a TTY, so without --yes confirmAction must
// fail LOUDLY (non-nil error) rather than returning false-as-cancel —
// which callers turned into a silent exit-0 no-op.
func TestConfirmActionNonTTYErrors(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	ok, err := confirmAction(cmd, "proceed?")
	if err == nil {
		t.Fatalf("non-TTY without --yes: want a loud error, got (%v, nil)", ok)
	}
	if ok {
		t.Fatalf("non-TTY without --yes: ok must be false, got true")
	}
}
