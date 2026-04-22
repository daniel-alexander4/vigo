// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validateWorksiteLayout enforces the filename-per-tree rule introduced in
// 0.29.0. Four fatal checks:
//
//  1. waivers.vgo under stockpile/ — waivers belong in scaffolding/.
//  2. compliance.vgo under scaffolding/ — claims belong in stockpile/.
//  3. stockpile/**/compliance.vgo with a waivers: top-level key — move
//     the waivers: block to scaffolding/waivers.vgo.
//  4. scaffolding/**/waivers.vgo with a compliance: top-level key — move
//     the compliance: block to stockpile/compliance.vgo.
//
// All four are combined into a single error so operators see every
// problem in one publish attempt, not one at a time.
func validateWorksiteLayout(worksiteDir string) error {
	var problems []string

	stockpile := filepath.Join(worksiteDir, "stockpile")
	scaffolding := filepath.Join(worksiteDir, "scaffolding")

	if _, err := os.Stat(stockpile); err == nil {
		if errs := scanStockpileLayout(stockpile); len(errs) > 0 {
			problems = append(problems, errs...)
		}
	}
	if _, err := os.Stat(scaffolding); err == nil {
		if errs := scanScaffoldingLayout(scaffolding); len(errs) > 0 {
			problems = append(problems, errs...)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("worksite layout violations:\n  - %s", strings.Join(problems, "\n  - "))
}

// scanStockpileLayout walks the stockpile subtree and rejects any file
// whose filename or key content belongs in scaffolding.
func scanStockpileLayout(stockpileDir string) []string {
	var problems []string
	_ = filepath.WalkDir(stockpileDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
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
			if hasTopLevelKey(path, "waivers") {
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

// scanScaffoldingLayout walks the scaffolding subtree and rejects any
// file whose filename or key content belongs in stockpile.
func scanScaffoldingLayout(scaffoldingDir string) []string {
	var problems []string
	_ = filepath.WalkDir(scaffoldingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
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
			if hasTopLevelKey(path, "compliance") {
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

// hasTopLevelKey returns true if the YAML file at path has the given
// top-level key set to a non-empty value. Used by the layout validator to
// detect waivers: blocks in stockpile compliance.vgo and compliance: blocks
// in scaffolding waivers.vgo.
//
// A parse failure is treated as "no such key" — the schema validator in
// the next pipeline stage catches malformed YAML separately.
func hasTopLevelKey(path, key string) bool {
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
	// Non-null node with at least one entry counts.
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		return len(node.Content) > 0
	}
	return node.Kind != yaml.ScalarNode || node.Value != ""
}
