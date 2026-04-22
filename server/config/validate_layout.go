// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validateWorksiteLayout enforces the filename-per-tree rule at load
// time. Intentional duplicate of server/publish.validateWorksiteLayout —
// config/ can't import publish/ (would be a cycle) and the rule is small
// enough that a direct copy is cheaper than introducing a shared package.
//
// Publish runs the same check pre-publish; this load-time mirror
// guarantees a hand-edited structure/ directory with a misplaced file
// refuses the reload instead of loading broken config.
func validateWorksiteLayout(worksiteDir string) error {
	var problems []string

	stockpile := filepath.Join(worksiteDir, "stockpile")
	scaffolding := filepath.Join(worksiteDir, "scaffolding")

	if _, err := os.Stat(stockpile); err == nil {
		problems = append(problems, scanStockpileLayout(stockpile)...)
	}
	if _, err := os.Stat(scaffolding); err == nil {
		problems = append(problems, scanScaffoldingLayout(scaffolding)...)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("worksite layout violations:\n  - %s", strings.Join(problems, "\n  - "))
}

func scanStockpileLayout(stockpileDir string) []string {
	var problems []string
	_ = filepath.WalkDir(stockpileDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(stockpileDir, path)
		relSlash := filepath.ToSlash(rel)
		base := filepath.Base(path)

		if base == "waivers.vgo" {
			problems = append(problems, fmt.Sprintf(
				"stockpile/%s: waivers live in the scaffolding tree. Move to scaffolding/%s",
				relSlash, relSlash))
			return nil
		}
		if base == "compliance.vgo" {
			if hasTopLevelYAMLKey(path, "waivers") {
				problems = append(problems, fmt.Sprintf(
					"stockpile/%s: waivers: block in a stockpile compliance.vgo. "+
						"Move the waivers: entries to scaffolding/waivers.vgo "+
						"(compliance.vgo in stockpile holds only compliance: claims).",
					relSlash))
			}
		}
		return nil
	})
	return problems
}

func scanScaffoldingLayout(scaffoldingDir string) []string {
	var problems []string
	_ = filepath.WalkDir(scaffoldingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(scaffoldingDir, path)
		relSlash := filepath.ToSlash(rel)
		base := filepath.Base(path)

		if base == "compliance.vgo" {
			problems = append(problems, fmt.Sprintf(
				"scaffolding/%s: compliance claims live in the stockpile tree. Move to stockpile/%s",
				relSlash, relSlash))
			return nil
		}
		if base == "waivers.vgo" {
			if hasTopLevelYAMLKey(path, "compliance") {
				problems = append(problems, fmt.Sprintf(
					"scaffolding/%s: compliance: block in a scaffolding waivers.vgo. "+
						"Move the compliance: claims to stockpile/compliance.vgo "+
						"(waivers.vgo in scaffolding holds only waivers: entries).",
					relSlash))
			}
		}
		return nil
	})
	return problems
}

func hasTopLevelYAMLKey(path, key string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]yaml.Node
	if err := yaml.Unmarshal(data, &m); err != nil {
		return false
	}
	node, ok := m[key]
	if !ok {
		return false
	}
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		return len(node.Content) > 0
	}
	return node.Kind != yaml.ScalarNode || node.Value != ""
}
