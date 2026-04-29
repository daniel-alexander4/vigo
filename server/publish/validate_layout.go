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

// validateStockpileLayout enforces filename-and-content rules for the
// stockpile tree. Mirrors server/config.validateStockpileLayout —
// publish/ runs this pre-publish; the loader runs it again at reload
// time defensively. The two copies exist because publish imports
// config but not vice versa, and the rule set is small enough that a
// shared package is more friction than the duplication.
//
// Rules enforced (single-tree, post-0.33.0 layout):
//
//   - compliance.vgo with a top-level waivers: block — mixed-keys
//     rejection; waivers belong in waivers.vgo.
//   - waivers.vgo with a top-level compliance: block — mixed-keys
//     rejection; claims belong in compliance.vgo.
//   - Any non-waivers.vgo file with a top-level waivers: block —
//     stray-filename rejection (loader globs the exact filename).
//   - Any "claims-only" file (top-level compliance: with no
//     resources:/name:/vars:) not named compliance.vgo —
//     stray-filename rejection. Module definitions carrying their own
//     compliance: block are not affected.
func validateStockpileLayout(stockpileDir string) error {
	if _, err := os.Stat(stockpileDir); err != nil {
		return nil
	}
	problems := scanStockpileLayout(stockpileDir)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("stockpile layout violations:\n  - %s", strings.Join(problems, "\n  - "))
}

// scanStockpileLayout walks the stockpile subtree and rejects any file
// whose filename or key content violates the canonical-filename rules.
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
			if hasTopLevelKey(path, "waivers") {
				problems = append(problems, fmt.Sprintf(
					"stockpile/%s: waivers: block in compliance.vgo. "+
						"Move the waivers: entries to waivers.vgo "+
						"(compliance.vgo holds only compliance: claims).",
					relSlash))
			}
			return nil
		}
		if base == "waivers.vgo" {
			if hasTopLevelKey(path, "compliance") {
				problems = append(problems, fmt.Sprintf(
					"stockpile/%s: compliance: block in waivers.vgo. "+
						"Move the compliance: claims to compliance.vgo "+
						"(waivers.vgo holds only waivers: entries).",
					relSlash))
			}
			return nil
		}
		if filepath.Ext(base) == ".vgo" && hasTopLevelKey(path, "waivers") {
			problems = append(problems, fmt.Sprintf(
				"stockpile/%s: top-level waivers: block in a non-waivers.vgo file. "+
					"Rename to waivers.vgo (the loader only globs that exact filename).",
				relSlash))
			return nil
		}
		if filepath.Ext(base) == ".vgo" && looksLikeClaimsOnlyFile(path) {
			problems = append(problems, fmt.Sprintf(
				"stockpile/%s: looks like a claims-only file (top-level compliance: with no resources/name/vars) but is not named compliance.vgo. "+
					"Rename to compliance.vgo so directory-level claim inheritance loads it.",
				relSlash))
		}
		return nil
	})
	return problems
}

// looksLikeClaimsOnlyFile returns true when a file has a top-level
// compliance: key and lacks any of resources:/name:/vars: that would
// mark it as a module definition.
func looksLikeClaimsOnlyFile(path string) bool {
	if !hasTopLevelKey(path, "compliance") {
		return false
	}
	for _, k := range []string{"resources", "name", "vars"} {
		if hasTopLevelKey(path, k) {
			return false
		}
	}
	return true
}

// hasTopLevelKey returns true if the YAML file at path has the given
// top-level key set to a non-empty value. A parse failure is treated
// as "no such key" — the schema validator in the next pipeline stage
// catches malformed YAML separately.
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
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		return len(node.Content) > 0
	}
	return node.Kind != yaml.ScalarNode || node.Value != ""
}
