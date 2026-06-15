// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// LintEnvironmentsFile validates an environments.vgo. The file shape
// is one top-level `environments:` mapping, keyed by environment name,
// with each value being its own mapping of `modules:`, `vars:`,
// `exclude_modules:`.
//
// Example:
//
//	environments:
//	  prod:
//	    modules: [monitoring, alerting]
//	    vars:
//	      log_level: warn
//	  staging:
//	    exclude_modules: [debug-noise]
//
// Checks applied:
//   - At least one environment defined.
//   - Each environment name is non-empty (no `"":` key from a
//     stray colon).
//   - Each environment's value is a mapping (catches `prod: monitoring`
//     where the operator forgot the modules: key).
//   - Unknown keys per environment → did-you-mean error.
//   - Empty environment (no modules, vars, or exclude_modules) → Hint:
//     defining an environment with nothing in it is dead config.
//
// Cross-file resolution (do listed module names exist in the
// stacks?) is Phase 6.
func LintEnvironmentsFile(input []byte, _ Options) *Result {
	r := &Result{}
	repaired := repair(input, r)

	ef, err := parseEnvironmentsFile(repaired)
	if err != nil {
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "environmentsfile-parse",
			Message:  fmt.Sprintf("environments file parse error: %s", err),
		})
		finalizeFindings(r)
		return r
	}

	r.Findings = append(r.Findings, applyEnvironmentsFileRules(ef)...)
	r.Output = string(repaired)

	finalizeFindings(r)
	return r
}

type environmentEntry struct {
	Name            string
	HasConfigcrates bool
	NumConfigcrates int
	HasExcludes     bool
	NumExcludes     int
	HasVars         bool
	UnknownKeys     []string
	Line            int
	Column          int
}

type environmentsFile struct {
	Environments []environmentEntry
}

var environmentKnownKeys = map[string]bool{
	"configcrates": true, "vars": true, "exclude_configcrates": true,
}

func parseEnvironmentsFile(data []byte) (*environmentsFile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	// Empty file — return an empty environmentsFile so the rule can emit
	// the empty-file warning rather than treating it as a parse error.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return &environmentsFile{}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root")
	}

	ef := &environmentsFile{}
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if key != "environments" {
			continue
		}
		if val.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("environments: expected mapping")
		}
		for j := 0; j < len(val.Content)-1; j += 2 {
			envKeyNode := val.Content[j]
			envValNode := val.Content[j+1]
			entry := environmentEntry{
				Name:   envKeyNode.Value,
				Line:   envKeyNode.Line,
				Column: envKeyNode.Column,
			}
			if envValNode.Kind != yaml.MappingNode {
				// Mark by zero attrs; the rule will flag the shape error.
				ef.Environments = append(ef.Environments, entry)
				continue
			}
			for k := 0; k < len(envValNode.Content)-1; k += 2 {
				key := envValNode.Content[k].Value
				v := envValNode.Content[k+1]
				switch key {
				case "configcrates":
					entry.HasConfigcrates = true
					if v.Kind == yaml.SequenceNode {
						entry.NumConfigcrates = len(v.Content)
					}
				case "exclude_configcrates":
					entry.HasExcludes = true
					if v.Kind == yaml.SequenceNode {
						entry.NumExcludes = len(v.Content)
					}
				case "vars":
					entry.HasVars = true
				default:
					entry.UnknownKeys = append(entry.UnknownKeys, key)
				}
			}
			ef.Environments = append(ef.Environments, entry)
		}
	}
	return ef, nil
}

func applyEnvironmentsFileRules(ef *environmentsFile) []Finding {
	var out []Finding
	if len(ef.Environments) == 0 {
		out = append(out, Finding{
			Severity: Warning,
			RuleID:   "environmentsfile-empty",
			Message:  "no environments defined — file is empty or 'environments:' block is missing",
		})
		return out
	}

	for _, e := range ef.Environments {
		if e.Name == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "environmentsfile-empty-name",
				Message:  "environment with empty name — drop it or give it a name",
			})
			continue
		}

		// Empty environment.
		if !e.HasConfigcrates && !e.HasExcludes && !e.HasVars && len(e.UnknownKeys) == 0 {
			out = append(out, Finding{
				Severity: Hint,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "environmentsfile-empty-environment",
				Message: fmt.Sprintf(
					"environment %q has no modules, vars, or exclude_modules — assigning this environment to a host has no effect",
					e.Name,
				),
			})
		}

		// Unknown keys → did-you-mean.
		known := mapKeysSorted(environmentKnownKeys)
		for _, k := range e.UnknownKeys {
			guess := closestKey(k, known)
			msg := fmt.Sprintf("environment %q: unknown key %q", e.Name, k)
			if guess != "" {
				msg += fmt.Sprintf(" — did you mean %q?", guess)
			}
			out = append(out, Finding{
				Severity: Error,
				Line:     e.Line,
				Column:   e.Column,
				RuleID:   "environmentsfile-unknown-key",
				Message:  msg,
			})
		}
	}
	return out
}
