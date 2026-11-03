// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"sort"
)

// DedupFinding describes a single redundancy in the config tree.
type DedupFinding struct {
	Type          string `json:"type"`           // "configcrate" or "var"
	Name          string `json:"name"`           // module name or var key
	File          string `json:"file"`           // file where the redundancy appears
	Reason        string `json:"reason"`         // human-readable explanation
	InheritedFrom string `json:"inherited_from"` // which ancestor already defines it
}

// DedupResult holds all redundancies found in the config tree.
type DedupResult struct {
	Findings []DedupFinding `json:"findings"`
}

// Dedup scans the config tree for redundant module references and variable
// definitions. A module is redundant if it's already inherited from a parent
// common.vgo. A variable is redundant if a child defines the same key with
// the same value as a parent.
func (cs *ConfigStore) Dedup() *DedupResult {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := &DedupResult{}

	// Expand all role modules for lookup.
	roleConfigcrates := make(map[string]map[string]bool)
	for name, role := range cs.roleDefs {
		mods := make(map[string]bool)
		for _, ref := range role.Configcrates {
			mods[ref.Name] = true
		}
		for _, c := range role.Case {
			for _, ref := range c.Configcrates {
				mods[ref.Name] = true
			}
		}
		for _, incName := range role.Includes {
			if inc, ok := cs.roleDefs[incName]; ok {
				for _, ref := range inc.Configcrates {
					mods[ref.Name] = true
				}
			}
		}
		roleConfigcrates[name] = mods
	}

	// Check each envoy entry for redundant modules and vars.
	for _, entry := range cs.envoyEntries {
		chain := inheritanceChain(entry.DirPath, cs.commonConfigs)

		// Build the set of modules inherited from the chain (common.vgo files).
		inheritedConfigcrates := make(map[string]string) // module name → source file
		inheritedVars := make(map[string]varOrigin)      // var key → origin

		for _, cc := range chain {
			src := "common.vgo"
			if cc.DirPath != "" {
				src = cc.DirPath + "/common.vgo"
			}

			for _, ref := range cc.Configcrates {
				if _, exists := inheritedConfigcrates[ref.Name]; !exists {
					inheritedConfigcrates[ref.Name] = src
				}
			}

			// Configcrates from roles in common.vgo.
			for _, roleRef := range cc.Roles {
				if mods, ok := roleConfigcrates[roleRef.Name]; ok {
					for mod := range mods {
						if _, exists := inheritedConfigcrates[mod]; !exists {
							inheritedConfigcrates[mod] = src + " (role " + roleRef.Name + ")"
						}
					}
				}
			}

			for k, v := range cc.Vars {
				inheritedVars[k] = varOrigin{value: v, source: src}
			}
		}

		entryFile := entry.SourceFile
		if entryFile == "" {
			entryFile = "envoys.vgo"
		}
		if entry.DirPath != "" {
			entryFile = entry.DirPath + "/" + entryFile
		}

		// Check entry's own modules for redundancy.
		for _, ref := range entry.OwnConfigcrates {
			if src, ok := inheritedConfigcrates[ref.Name]; ok {
				result.Findings = append(result.Findings, DedupFinding{
					Type:          "configcrate",
					Name:          ref.Name,
					File:          entryFile,
					Reason:        fmt.Sprintf("configcrate %q already inherited from %s", ref.Name, src),
					InheritedFrom: src,
				})
			}
		}

		// Check if any directly listed module is also provided by a role on the same entry.
		entryRoleConfigcrates := make(map[string]string)
		for _, roleRef := range entry.OwnRoles {
			if mods, ok := roleConfigcrates[roleRef.Name]; ok {
				for mod := range mods {
					entryRoleConfigcrates[mod] = roleRef.Name
				}
			}
		}
		for _, ref := range entry.OwnConfigcrates {
			if roleName, ok := entryRoleConfigcrates[ref.Name]; ok {
				result.Findings = append(result.Findings, DedupFinding{
					Type:          "configcrate",
					Name:          ref.Name,
					File:          entryFile,
					Reason:        fmt.Sprintf("configcrate %q listed directly but already included by role %q", ref.Name, roleName),
					InheritedFrom: "role " + roleName,
				})
			}
		}

		// Check entry's own vars for redundancy (same value as inherited).
		for k, v := range entry.OwnVars {
			if origin, ok := inheritedVars[k]; ok {
				if fmt.Sprintf("%v", v) == fmt.Sprintf("%v", origin.value) {
					result.Findings = append(result.Findings, DedupFinding{
						Type:          "var",
						Name:          k,
						File:          entryFile,
						Reason:        fmt.Sprintf("var %q = %v is identical to value inherited from %s", k, v, origin.source),
						InheritedFrom: origin.source,
					})
				}
			}
		}
	}

	// Check common.vgo files: child common.vgo re-listing a module from parent common.vgo.
	for dirPath, cc := range cs.commonConfigs {
		if dirPath == "" {
			continue // root has no parent
		}
		parentChain := inheritanceChain(dirPath, cs.commonConfigs)
		// Remove the last element (self) from the chain.
		if len(parentChain) > 0 && parentChain[len(parentChain)-1].DirPath == dirPath {
			parentChain = parentChain[:len(parentChain)-1]
		}

		parentConfigcrates := make(map[string]string)
		parentVars := make(map[string]varOrigin)
		for _, parent := range parentChain {
			src := "common.vgo"
			if parent.DirPath != "" {
				src = parent.DirPath + "/common.vgo"
			}
			for _, ref := range parent.Configcrates {
				if _, exists := parentConfigcrates[ref.Name]; !exists {
					parentConfigcrates[ref.Name] = src
				}
			}
			for k, v := range parent.Vars {
				parentVars[k] = varOrigin{value: v, source: src}
			}
		}

		childFile := dirPath + "/common.vgo"

		for _, ref := range cc.Configcrates {
			if src, ok := parentConfigcrates[ref.Name]; ok {
				result.Findings = append(result.Findings, DedupFinding{
					Type:          "configcrate",
					Name:          ref.Name,
					File:          childFile,
					Reason:        fmt.Sprintf("configcrate %q already inherited from %s", ref.Name, src),
					InheritedFrom: src,
				})
			}
		}

		for k, v := range cc.Vars {
			if origin, ok := parentVars[k]; ok {
				if fmt.Sprintf("%v", v) == fmt.Sprintf("%v", origin.value) {
					result.Findings = append(result.Findings, DedupFinding{
						Type:          "var",
						Name:          k,
						File:          childFile,
						Reason:        fmt.Sprintf("var %q = %v is identical to value inherited from %s", k, v, origin.source),
						InheritedFrom: origin.source,
					})
				}
			}
		}
	}

	// Sort findings by file, then type, then name for stable output.
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Type != result.Findings[j].Type {
			return result.Findings[i].Type < result.Findings[j].Type
		}
		return result.Findings[i].Name < result.Findings[j].Name
	})

	return result
}

type varOrigin struct {
	value  any
	source string
}
