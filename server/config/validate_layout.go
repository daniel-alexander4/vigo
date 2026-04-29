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

// validateStockpileLayout enforces filename-and-content rules for the
// stockpile tree at load time. Intentional duplicate of
// server/publish.validateStockpileLayout — config/ can't import publish/
// (would be a cycle) and the rule set is small enough that a direct
// copy is cheaper than introducing a shared package.
//
// Publish runs the same check pre-publish; this load-time mirror
// guarantees a hand-edited .live/ directory with a misplaced or
// mistitled file refuses the reload instead of loading broken config.
//
// Rules enforced (single-tree, post-0.33.0 layout):
//
//   - compliance.vgo with a top-level waivers: block — mixed-keys
//     rejection; waivers belong in waivers.vgo.
//   - waivers.vgo with a top-level compliance: block — mixed-keys
//     rejection; claims belong in compliance.vgo.
//   - Any non-waivers.vgo file with a top-level waivers: block —
//     stray-filename rejection; the loader globs the exact filename.
//   - Any "claims-only" file (top-level compliance: with no
//     resources:/name:/vars:) not named compliance.vgo —
//     stray-filename rejection. Module definitions carrying their own
//     compliance: block are not affected.
func validateStockpileLayout(stockpileDir string) error {
	if _, err := os.Stat(stockpileDir); err != nil {
		// stockpile doesn't exist yet (fresh install); nothing to check.
		return nil
	}

	problems := scanStockpileLayout(stockpileDir)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("stockpile layout violations:\n  - %s", strings.Join(problems, "\n  - "))
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

		if base == "compliance.vgo" {
			if hasTopLevelYAMLKey(path, "waivers") {
				problems = append(problems, fmt.Sprintf(
					"stockpile/%s: waivers: block in compliance.vgo. "+
						"Move the waivers: entries to waivers.vgo "+
						"(compliance.vgo holds only compliance: claims).",
					relSlash))
			}
			return nil
		}
		if base == "waivers.vgo" {
			if hasTopLevelYAMLKey(path, "compliance") {
				problems = append(problems, fmt.Sprintf(
					"stockpile/%s: compliance: block in waivers.vgo. "+
						"Move the compliance: claims to compliance.vgo "+
						"(waivers.vgo holds only waivers: entries).",
					relSlash))
			}
			return nil
		}
		// Stray waivers.vgo: any non-waivers.vgo file with a top-level
		// waivers: block silently isn't loaded (loader globs the exact
		// filename). Reject with a rename hint.
		if filepath.Ext(base) == ".vgo" && hasTopLevelYAMLKey(path, "waivers") {
			problems = append(problems, fmt.Sprintf(
				"stockpile/%s: top-level waivers: block in a non-waivers.vgo file. "+
					"Rename to waivers.vgo (the loader only globs that exact filename).",
				relSlash))
			return nil
		}
		// Stray compliance.vgo: a non-canonical filename that looks
		// like a claims-only file (top-level compliance: only, no
		// resources:/name:/vars:). Module definitions carrying their
		// own compliance: block — common in stockpile/modules — are
		// not flagged because they have resources:/name:.
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
