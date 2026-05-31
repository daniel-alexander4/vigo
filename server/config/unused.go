// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"os"
	"path/filepath"
	"sort"
)

// UnusedModule describes a module defined but never referenced.
type UnusedModule struct {
	Name       string `json:"name"`
	SourceFile string `json:"source_file"` // stockpile-relative path, e.g. "modules/system/cleanup.vgo"
}

// UnusedUsercrate describes a usercrate defined but never referenced.
type UnusedUsercrate struct {
	Name       string `json:"name"`
	SourceFile string `json:"source_file"` // stockpile-relative path, e.g. "usercrates/alice.vgo"
}

// UnusedTemplate describes a template file present under templates/ but
// not referenced by any module's resource source: attribute. Reachability
// is sound because the loader rejects non-literal source: values at load
// time (resolveSourceFiles reads the file by its declared path), so every
// reference is a concrete stockpile-relative path.
type UnusedTemplate struct {
	Path string `json:"path"` // stockpile-relative path, e.g. "templates/nginx/site.tmpl"
}

// UnusedHostcrate describes a hostcrate where no pattern matches any enrolled hostname.
type UnusedHostcrate struct {
	Path     string   `json:"path"`     // stockpile-relative path, e.g. "staging/web/envoys.vgo"
	Patterns []string `json:"patterns"` // the match: values in this file
}

// UnusedResult holds all unused defs and hostcrates.
type UnusedResult struct {
	UnusedModules    []UnusedModule    `json:"unused_modules"`
	UnusedUsercrates []UnusedUsercrate `json:"unused_usercrates"`
	UnusedTemplates  []UnusedTemplate  `json:"unused_templates"`

	UnusedHostcrates []UnusedHostcrate `json:"unused_hostcrates"`
}

// Unused scans the config tree for modules, usercrates, and templates
// that are defined but never reachable through any carrier (role / case
// branch / 1-level include / match-block / common.vgo / environments.vgo),
// plus hostcrates whose patterns don't match any currently enrolled
// hostname. Static analysis — no runtime trait evaluation; the result is
// stable across check-ins and depends only on the published config.
func (cs *ConfigStore) Unused(enrolledHostnames []string) *UnusedResult {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := &UnusedResult{}

	// --- Unused modules + usercrates ---

	// Split definitions by kind. Configcrates and usercrates share cs.moduleDefs
	// but each kind has its own ref set; reporting them separately matches
	// the carrier-field shape (modules: vs usercrates:).
	definedModules := make(map[string]bool)
	definedUsercrates := make(map[string]bool)
	for name, def := range cs.moduleDefs {
		if def.Kind == ConfigcrateKindUsercrate {
			definedUsercrates[name] = true
		} else {
			definedModules[name] = true
		}
	}

	refModules := make(map[string]bool)
	refUsercrates := make(map[string]bool)

	// From roles: direct, case branches, and 1-level includes.
	for _, role := range cs.roleDefs {
		for _, r := range role.Configcrates {
			refModules[r.Name] = true
		}
		for _, r := range role.Usercrates {
			refUsercrates[r.Name] = true
		}
		for _, c := range role.Case {
			for _, r := range c.Configcrates {
				refModules[r.Name] = true
			}
		}
		for _, incName := range role.Includes {
			if inc, ok := cs.roleDefs[incName]; ok {
				for _, r := range inc.Configcrates {
					refModules[r.Name] = true
				}
				for _, r := range inc.Usercrates {
					refUsercrates[r.Name] = true
				}
			}
		}
	}

	// From envoy entries (post-inheritance — includes common.vgo-inherited refs).
	for _, entry := range cs.envoyEntries {
		for _, r := range entry.Configcrates {
			refModules[r.Name] = true
		}
		for _, r := range entry.Usercrates {
			refUsercrates[r.Name] = true
		}
	}

	// From common.vgo files.
	for _, cc := range cs.commonConfigs {
		for _, r := range cc.Configcrates {
			refModules[r.Name] = true
		}
		for _, r := range cc.Usercrates {
			refUsercrates[r.Name] = true
		}
	}

	// From environments.vgo per-env blocks.
	for _, envMap := range cs.perEnvOverrides {
		for _, block := range envMap {
			for _, r := range block.Configcrates {
				refModules[r.Name] = true
			}
			for _, r := range block.Usercrates {
				refUsercrates[r.Name] = true
			}
		}
	}

	for name := range definedModules {
		if !refModules[name] {
			result.UnusedModules = append(result.UnusedModules, UnusedModule{
				Name:       name,
				SourceFile: cs.moduleDefs[name].SourceFile,
			})
		}
	}
	sort.Slice(result.UnusedModules, func(i, j int) bool {
		return result.UnusedModules[i].Name < result.UnusedModules[j].Name
	})

	for name := range definedUsercrates {
		if !refUsercrates[name] {
			result.UnusedUsercrates = append(result.UnusedUsercrates, UnusedUsercrate{
				Name:       name,
				SourceFile: cs.moduleDefs[name].SourceFile,
			})
		}
	}
	sort.Slice(result.UnusedUsercrates, func(i, j int) bool {
		return result.UnusedUsercrates[i].Name < result.UnusedUsercrates[j].Name
	})

	// --- Unused templates ---

	result.UnusedTemplates = cs.unusedTemplates()

	// --- Unused hostcrates ---

	hostnameSet := make(map[string]bool, len(enrolledHostnames))
	for _, h := range enrolledHostnames {
		hostnameSet[h] = true
	}

	type fileInfo struct {
		patterns []string
	}
	fileEntries := make(map[string]*fileInfo)

	for _, entry := range cs.envoyEntries {
		path := entry.SourceFile
		if path == "" {
			path = "envoys.vgo"
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

	for path, fi := range fileEntries {
		matched := false
		for _, pattern := range fi.patterns {
			if matched {
				break
			}
			if hostnameSet[pattern] {
				matched = true
				break
			}
			for _, hostname := range enrolledHostnames {
				if ok, _ := filepath.Match(pattern, hostname); ok {
					matched = true
					break
				}
			}
		}
		if !matched {
			result.UnusedHostcrates = append(result.UnusedHostcrates, UnusedHostcrate{
				Path:     path,
				Patterns: fi.patterns,
			})
		}
	}

	sort.Slice(result.UnusedHostcrates, func(i, j int) bool {
		return result.UnusedHostcrates[i].Path < result.UnusedHostcrates[j].Path
	})

	return result
}

// unusedTemplates lists every file under templates/ that no module
// references via a `source:` attribute. References are captured by
// resolveSourceFiles at load time and stashed on
// ConfigcrateDefinition.TemplateSources; dynamic source paths cannot occur
// because resolveSourceFiles rejects non-literal values (it reads the
// declared path off disk during validation).
func (cs *ConfigStore) unusedTemplates() []UnusedTemplate {
	referenced := make(map[string]bool)
	for _, def := range cs.moduleDefs {
		for _, src := range def.TemplateSources {
			referenced[src] = true
		}
	}

	templatesDir := cs.templatesDir()
	if _, err := os.Stat(templatesDir); err != nil {
		return nil
	}

	var out []UnusedTemplate
	_ = filepath.WalkDir(templatesDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(cs.contentDir(), path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !referenced[rel] {
			out = append(out, UnusedTemplate{Path: rel})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
