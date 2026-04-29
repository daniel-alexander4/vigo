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
			return nil
		}
		// Catch a non-canonical filename that looks like a claims-only
		// file (top-level compliance: only, no resources:/name:/vars: —
		// i.e., not a module def). Operators sometimes name them
		// `complaince.vgo` or `usercrates-compliance.vgo`; without this
		// check the file silently isn't loaded as claims.
		if filepath.Ext(base) == ".vgo" && looksLikeClaimsOnly(path) {
			problems = append(problems, fmt.Sprintf(
				"stockpile/%s: looks like a claims-only file (top-level compliance: with no resources/name/vars) but is not named compliance.vgo. "+
					"Rename to compliance.vgo so directory-level claim inheritance loads it.",
				relSlash))
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
			return nil
		}
		// Catch a non-canonical filename carrying a top-level waivers:
		// block — the loader globs for the exact name `waivers.vgo`, so
		// stray filenames silently don't load. Reject with a rename hint.
		if filepath.Ext(base) == ".vgo" && hasTopLevelYAMLKey(path, "waivers") {
			problems = append(problems, fmt.Sprintf(
				"scaffolding/%s: top-level waivers: block in a non-waivers.vgo file. "+
					"Rename to waivers.vgo (the loader only globs that exact filename).",
				relSlash))
		}
		return nil
	})
	return problems
}

// looksLikeClaimsOnly returns true when a YAML file has a top-level
// `compliance:` key and lacks any of the keys typical of a module
// definition (`resources:`, `name:`, `vars:`). Used by the stockpile
// layout check to flag misnamed compliance.vgo files — the loader
// globs the exact filename, so a typo silently drops the claims.
func looksLikeClaimsOnly(path string) bool {
	if !hasTopLevelYAMLKey(path, "compliance") {
		return false
	}
	for _, k := range []string{"resources", "name", "vars"} {
		if hasTopLevelYAMLKey(path, k) {
			return false
		}
	}
	return true
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
