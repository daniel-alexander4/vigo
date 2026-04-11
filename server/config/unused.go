// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"sort"
)

// UnusedModule describes a module defined but never referenced.
type UnusedModule struct {
	Name       string `json:"name"`
	SourceFile string `json:"source_file"` // stockpile-relative path, e.g. "modules/system/cleanup.vgo"
}

// UnusedMatchFile describes a match file where no pattern matches any enrolled hostname.
type UnusedMatchFile struct {
	Path     string   `json:"path"`     // stockpile-relative path, e.g. "staging/web/nodes.vgo"
	Patterns []string `json:"patterns"` // the match: values in this file
}

// UnusedResult holds all unused modules and match files.
type UnusedResult struct {
	UnusedModules    []UnusedModule    `json:"unused_modules"`
	UnusedMatchFiles []UnusedMatchFile `json:"unused_match_files"`
}

// Unused scans the config tree for modules that are defined but never referenced
// by any role, match entry, common.vgo, or per_env_overrides, and for match files
// whose patterns don't match any enrolled hostname.
func (cs *ConfigStore) Unused(enrolledHostnames []string) *UnusedResult {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := &UnusedResult{}

	// --- Unused modules ---

	// Build the set of all defined module names.
	defined := make(map[string]bool, len(cs.moduleDefs))
	for name := range cs.moduleDefs {
		defined[name] = true
	}

	// Collect all referenced module names.
	referenced := make(map[string]bool)

	// From roles: direct modules, case modules, and single-level includes.
	for _, role := range cs.roleDefs {
		for _, ref := range role.Modules {
			referenced[ref.Name] = true
		}
		for _, c := range role.Case {
			for _, ref := range c.Modules {
				referenced[ref.Name] = true
			}
		}
		for _, incName := range role.Includes {
			if inc, ok := cs.roleDefs[incName]; ok {
				for _, ref := range inc.Modules {
					referenced[ref.Name] = true
				}
			}
		}
	}

	// From envoy entries (post-inheritance — includes common.vgo-inherited modules).
	for _, entry := range cs.envoyEntries {
		for _, ref := range entry.Modules {
			referenced[ref.Name] = true
		}
	}

	// From common.vgo files.
	for _, cc := range cs.commonConfigs {
		for _, ref := range cc.Modules {
			referenced[ref.Name] = true
		}
	}

	// From per_env_overrides.
	for _, envMap := range cs.perEnvOverrides {
		for _, block := range envMap {
			for _, ref := range block.Modules {
				referenced[ref.Name] = true
			}
		}
	}

	// Compute the difference.
	for name := range defined {
		if !referenced[name] {
			result.UnusedModules = append(result.UnusedModules, UnusedModule{
				Name:       name,
				SourceFile: cs.moduleDefs[name].SourceFile,
			})
		}
	}

	sort.Slice(result.UnusedModules, func(i, j int) bool {
		return result.UnusedModules[i].Name < result.UnusedModules[j].Name
	})

	// --- Unused match files ---

	// Build enrolled hostname set for O(1) lookup.
	hostnameSet := make(map[string]bool, len(enrolledHostnames))
	for _, h := range enrolledHostnames {
		hostnameSet[h] = true
	}

	// Group entries by source file.
	type fileInfo struct {
		patterns []string
	}
	fileEntries := make(map[string]*fileInfo)

	for _, entry := range cs.envoyEntries {
		path := entry.SourceFile
		if path == "" {
			path = "nodes.vgo"
		}
		if entry.DirPath != "" {
			path = entry.DirPath + "/" + path
		}

		fi, ok := fileEntries[path]
		if !ok {
			fi = &fileInfo{}
			fileEntries[path] = fi
		}
		fi.patterns = append(fi.patterns, entry.Match)
	}

	// Check each file: does any pattern match any enrolled hostname?
	for path, fi := range fileEntries {
		matched := false
		for _, pattern := range fi.patterns {
			if matched {
				break
			}
			// Exact match first (fast path).
			if hostnameSet[pattern] {
				matched = true
				break
			}
			// Glob match against all hostnames.
			for _, hostname := range enrolledHostnames {
				if ok, _ := filepath.Match(pattern, hostname); ok {
					matched = true
					break
				}
			}
		}
		if !matched {
			result.UnusedMatchFiles = append(result.UnusedMatchFiles, UnusedMatchFile{
				Path:     path,
				Patterns: fi.patterns,
			})
		}
	}

	sort.Slice(result.UnusedMatchFiles, func(i, j int) bool {
		return result.UnusedMatchFiles[i].Path < result.UnusedMatchFiles[j].Path
	})

	return result
}
