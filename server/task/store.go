// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package push

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ParamDef defines a single push definition parameter.
type ParamDef struct {
	Type        string `yaml:"type"`        // "string", "int", "bool"
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
	Description string `yaml:"description"`
}

// PushDef is a reusable push definition loaded from a YAML file.
type PushDef struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Timeout     int32               `yaml:"timeout"`
	Target      string              `yaml:"target"`
	Parameters  map[string]ParamDef `yaml:"parameters"`
	Script      string              `yaml:"script"`
}

// Store holds loaded push definitions.
type Store struct {
	mu    sync.RWMutex
	defs  map[string]*PushDef
}

// NewStore creates an empty push store.
func NewStore() *Store {
	return &Store{defs: make(map[string]*PushDef)}
}

// Load reads all .yaml files from a directory and populates the store.
func (s *Store) Load(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no push definitions directory is fine
		}
		return fmt.Errorf("reading push definitions directory: %w", err)
	}

	defs := make(map[string]*PushDef)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		var def PushDef
		if err := yaml.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}

		if def.Name == "" {
			def.Name = strings.TrimSuffix(entry.Name(), ext)
		}
		if def.Script == "" {
			return fmt.Errorf("push definition %q has no script", def.Name)
		}
		if def.Timeout <= 0 {
			def.Timeout = 60
		}

		// Default parameter type to "string".
		for k, p := range def.Parameters {
			if p.Type == "" {
				p.Type = "string"
				def.Parameters[k] = p
			}
		}

		defs[def.Name] = &def
	}

	s.mu.Lock()
	s.defs = defs
	s.mu.Unlock()
	return nil
}

// Get returns a push definition by name.
func (s *Store) Get(name string) (*PushDef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.defs[name]
	return t, ok
}

// List returns all push definition names sorted alphabetically.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.defs))
	for name := range s.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// All returns all push definitions.
func (s *Store) All() []*PushDef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	defs := make([]*PushDef, 0, len(s.defs))
	for _, d := range s.defs {
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// ValidateParams checks that all required parameters are present and no
// unknown parameters are supplied. Returns an error naming invalid params.
func ValidateParams(def *PushDef, params map[string]string) error {
	var missing, unknown []string

	// Check for unknown parameters.
	for k := range params {
		if _, ok := def.Parameters[k]; !ok {
			unknown = append(unknown, k)
		}
	}

	// Check for missing required parameters.
	for k, p := range def.Parameters {
		if p.Required {
			if _, ok := params[k]; !ok {
				if p.Default == "" {
					missing = append(missing, k)
				}
			}
		}
	}

	sort.Strings(missing)
	sort.Strings(unknown)

	if len(missing) > 0 && len(unknown) > 0 {
		return fmt.Errorf("missing required parameters: %s; unknown parameters: %s",
			strings.Join(missing, ", "), strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required parameters: %s", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown parameters: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// BuildEnv converts parameter values into environment variables.
// Parameter names are converted to UPPER_SNAKE_CASE with a PARAM_ prefix.
// Missing optional parameters use their default value.
func BuildEnv(def *PushDef, params map[string]string) map[string]string {
	env := make(map[string]string, len(def.Parameters))
	for k, p := range def.Parameters {
		val, ok := params[k]
		if !ok {
			val = p.Default
		}
		if val != "" {
			envKey := "PARAM_" + strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
			env[envKey] = val
		}
	}
	return env
}
