// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"git.alexander4.io/vigo/server/modlint"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate <file.vgo> [file2.vgo ...]",
	Short: "Validate .vgo files for YAML syntax, cross-references, and idempotency guards",
	Long: `Validate one or more .vgo files.

Checks performed:
  1. YAML syntax correctness (valid YAML, proper indentation, no tabs, etc.)
  2. Structure (required fields: name, resources with name + type)
  3. Idempotency (exec must have creates/onlyif/unless guard)

Exit code 0 = all checks pass, 1 = errors found.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hasErrors := false

		for _, path := range args {
			result, err := validateFile(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
				hasErrors = true
				continue
			}

			if len(result.Findings) == 0 {
				fmt.Printf("%s: OK\n", path)
				continue
			}

			for _, f := range result.Findings {
				fmt.Println(f.String())
			}
			if !result.OK() {
				hasErrors = true
			}
		}

		if hasErrors {
			return fmt.Errorf("validation failed")
		}
		return nil
	},
}

// validateFile lints a single .vgo file through modlint — the same
// file-kind dispatch the publish path uses, so `config validate` predicts
// `config publish` exactly (no second validation source of truth).
// IsUsercrate is derived from the path inside LintFile.
func validateFile(path string) (*modlint.Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(path)
	if ext != ".vgo" && ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("unsupported file type %q (expected .vgo, .yaml, or .yml)", ext)
	}

	return modlint.LintFile(path, data, modlint.Options{}), nil
}

func init() {
	configCmd.AddCommand(validateCmd)
}
