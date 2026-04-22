// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/validate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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
	if ext != ".vgo" && ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("unsupported file type %q (expected .vgo, .yaml, or .yml)", ext)
	}

	// Phase 1: YAML syntax check — catch malformed YAML before anything else.
	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Phase 2: detect file type — envoys file vs module file.
	// A envoys file has a top-level "envoys:" key; a role file has "modules:" but no "resources:".
	var probe map[string]any
	yaml.Unmarshal(data, &probe)

	if _, isNodes := probe["envoys"]; isNodes {
		// Envoys file — validate each entry has a "match" field.
		var cfg config.EnvoysConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing envoys file: %w", err)
		}
		var findings []validate.Finding
		for i, entry := range cfg.Envoys {
			if entry.Match == "" {
				findings = append(findings, validate.Finding{
					Severity: validate.SeverityError,
					Module:   filepath.Base(path),
					Message:  fmt.Sprintf("entry %d: missing required 'match' field", i),
				})
			}
		}
		if len(cfg.Envoys) == 0 {
			findings = append(findings, validate.Finding{
				Severity: validate.SeverityWarning,
				Module:   filepath.Base(path),
				Message:  "no envoy entries defined",
			})
		}
		return findings, nil
	}

	// Phase 2b: structural parse as module via the tolerant envoy parser.
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
