// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/validate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var validateCmd = &cobra.Command{
	Use:   "validate <file.hnb> [file2.hnb ...]",
	Short: "Check .hnb files for YAML correctness and idempotency",
	Long: `Validate one or more .hnb files.

Checks performed:
  1. YAML syntax correctness (valid YAML, proper indentation, no tabs, etc.)
  2. Structure (required fields: name, resources with name + type)
  3. Idempotency (exec must have creates/onlyif/unless guard)

Exit code 0 = all checks pass, 1 = errors found.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hasErrors := false

		for _, path := range args {
			findings, err := validateFile(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
				hasErrors = true
				continue
			}

			if len(findings) == 0 {
				fmt.Printf("%s: OK\n", path)
				continue
			}

			for _, f := range findings {
				fmt.Println(f.String())
			}

			if validate.HasErrors(findings) {
				hasErrors = true
			}
		}

		if hasErrors {
			return fmt.Errorf("validation failed")
		}
		return nil
	},
}

func validateFile(path string) ([]validate.Finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(path)
	if ext != ".hnb" && ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("unsupported file type %q (expected .hnb, .yaml, or .yml)", ext)
	}

	// Phase 1: YAML syntax check — catch malformed YAML before anything else.
	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Phase 2: detect file type — thralls file vs module file.
	// A thralls file has a top-level "thralls:" key; a role file has "modules:" but no "resources:".
	var probe map[string]any
	yaml.Unmarshal(data, &probe)

	if _, isNodes := probe["thralls"]; isNodes {
		// Thralls file — validate each entry has a "match" field.
		var cfg config.ThrallsConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing thralls file: %w", err)
		}
		var findings []validate.Finding
		for i, entry := range cfg.Thralls {
			if entry.Match == "" {
				findings = append(findings, validate.Finding{
					Severity: validate.SeverityError,
					Module:   filepath.Base(path),
					Message:  fmt.Sprintf("entry %d: missing required 'match' field", i),
				})
			}
		}
		if len(cfg.Thralls) == 0 {
			findings = append(findings, validate.Finding{
				Severity: validate.SeverityWarning,
				Module:   filepath.Base(path),
				Message:  "no thrall entries defined",
			})
		}
		return findings, nil
	}

	// Phase 2b: structural parse as module via the tolerant thrall parser.
	def, err := config.ParseModuleFile(data)
	if err != nil {
		return nil, err
	}

	// Phase 3: structural validation.
	var findings []validate.Finding

	if def.Name == "" {
		findings = append(findings, validate.Finding{
			Severity: validate.SeverityError,
			Module:   filepath.Base(path),
			Message:  "missing required 'name' field",
		})
	}

	moduleName := def.Name
	if moduleName == "" {
		moduleName = filepath.Base(path)
	}

	for _, r := range def.Resources {
		if r.Name == "" {
			findings = append(findings, validate.Finding{
				Severity:     validate.SeverityError,
				Module:       moduleName,
				ResourceType: r.Type,
				Message:      "resource missing 'name' field",
			})
		}
		if r.Type == "" {
			findings = append(findings, validate.Finding{
				Severity:     validate.SeverityError,
				Module:       moduleName,
				ResourceName: r.Name,
				Message:      "resource missing 'type' field",
			})
		}
	}

	// Phase 4: idempotency validation.
	mod := validate.Module{Name: moduleName}
	for _, r := range def.Resources {
		mod.Resources = append(mod.Resources, validate.Resource{
			Name:             r.Name,
			Type:             r.Type,
			Attributes:       r.Attributes,
			Notify:           r.Notify,
			Subscribes:       r.Subscribes,
			WatchSecret: r.WatchSecret,
		})
	}
	findings = append(findings, validate.ValidateModule(mod)...)

	// Deduplicate (structural checks may overlap with validate.ValidateModule).
	return dedup(findings), nil
}

func dedup(findings []validate.Finding) []validate.Finding {
	seen := make(map[string]bool)
	var out []validate.Finding
	for _, f := range findings {
		key := f.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}

func init() {
	configCmd.AddCommand(validateCmd)
}
