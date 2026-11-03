// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ServerYAMLState is the live on-disk view of server.yaml used by the
// read-only "Server Config" UI. It is re-read from the file every time the UI
// handler runs so the page reflects reality immediately after an operator edits
// the file — the in-memory ServerConfig is loaded once at startup and would
// otherwise look stale until a restart.
//
// Values are sourced from the raw YAML tree and never from the resolved
// ServerConfig, so "secret:" references remain as their literal ref string and
// plaintext secrets cannot leak into the UI.
type ServerYAMLState struct {
	Sections []SectionState
}

// SectionState is one top-level section of server.yaml as it appears on disk.
type SectionState struct {
	Name    string     // yaml key ("server", "database", "swarm", ...)
	Present bool       // key exists in the top-level mapping
	Enabled bool       // Present AND not "empty" AND not explicitly enabled: false
	Node    *yaml.Node // raw value node; nil when Present is false
}

// ReadServerYAMLState parses the raw server.yaml on disk and returns the
// per-section state. The returned state is keyed only by yaml key — it is not
// reconciled against the schema here. Callers join this with
// ServerConfigSchema() to produce the UI card grid.
func ReadServerYAMLState(path string) (*ServerYAMLState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading server config %s: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing server config: %w", err)
	}

	// A document node wraps a single mapping. Guard for empty files.
	var mapping *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		mapping = root.Content[0]
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return &ServerYAMLState{}, nil
	}

	state := &ServerYAMLState{}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		key := mapping.Content[i]
		val := mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		name := key.Value
		present, enabled := evaluatePresence(val)
		node := val
		if !present {
			node = nil
		}
		state.Sections = append(state.Sections, SectionState{
			Name:    name,
			Present: present,
			Enabled: enabled,
			Node:    node,
		})
	}
	return state, nil
}

// Section returns the SectionState for the given yaml name, or nil when the
// section is not present in the raw file.
func (s *ServerYAMLState) Section(name string) *SectionState {
	if s == nil {
		return nil
	}
	for i := range s.Sections {
		if s.Sections[i].Name == name {
			return &s.Sections[i]
		}
	}
	return nil
}

// EnabledCount returns the number of sections that are present and not
// explicitly disabled. Used by the dashboard stat-card.
func (s *ServerYAMLState) EnabledCount() int {
	if s == nil {
		return 0
	}
	n := 0
	for _, sec := range s.Sections {
		if sec.Enabled {
			n++
		}
	}
	return n
}

// evaluatePresence decides whether a yaml value counts as "present" and
// "enabled":
//
//   - An explicit "null" (or a missing value) counts as NOT present.
//   - An empty mapping ("foo: {}") is present but NOT enabled — it looks like
//     a placeholder for defaults.
//   - A present mapping with an "enabled: false" child is present but NOT
//     enabled (explicitly turned off).
//   - Anything else that is a non-empty mapping / sequence / non-empty scalar
//     is present AND enabled.
func evaluatePresence(val *yaml.Node) (present, enabled bool) {
	if val == nil {
		return false, false
	}
	// Explicit null tag.
	if val.Tag == "!!null" {
		return false, false
	}

	switch val.Kind {
	case yaml.MappingNode:
		if len(val.Content) == 0 {
			return true, false
		}
		if childIsFalse(val, "enabled") {
			return true, false
		}
		return true, true
	case yaml.SequenceNode:
		if len(val.Content) == 0 {
			return true, false
		}
		return true, true
	case yaml.ScalarNode:
		if val.Value == "" {
			return false, false
		}
		return true, true
	default:
		return false, false
	}
}

// childIsFalse returns true when the mapping node has a child named `key` whose
// scalar value parses as YAML false. Used to treat "enabled: false" as
// disabled while a missing enabled key stays enabled (the operator set other
// fields instead).
func childIsFalse(mapping *yaml.Node, key string) bool {
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != key {
			continue
		}
		if v.Kind != yaml.ScalarNode {
			return false
		}
		switch v.Value {
		case "false", "False", "FALSE", "no", "No", "NO", "off", "Off", "OFF":
			return true
		}
	}
	return false
}

// MarshalYAML returns the section's live YAML as a pretty-printed string,
// suitable for rendering in a <pre> block on the UI detail page. The output
// is the mapping under the section key (without the section key itself),
// re-serialized from the raw yaml.Node so "secret:" references stay as
// literal ref strings and are never resolved.
//
// Returns "" when the section is absent or has no value node. The caller
// renders "(not configured)" in that case.
func (s *SectionState) MarshalYAML() (string, error) {
	if s == nil || s.Node == nil {
		return "", nil
	}
	out, err := yaml.Marshal(s.Node)
	if err != nil {
		return "", fmt.Errorf("marshaling section %q: %w", s.Name, err)
	}
	return string(out), nil
}

// FieldValue returns the raw YAML scalar value for a child key under this
// section, or "" when the key is absent, null, or non-scalar. Used by the
// section detail page to render "current values" directly from disk without
// ever dereferencing the resolved (secret-substituted) ServerConfig.
func (s *SectionState) FieldValue(key string) string {
	if s == nil || s.Node == nil || s.Node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i < len(s.Node.Content)-1; i += 2 {
		k := s.Node.Content[i]
		v := s.Node.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != key {
			continue
		}
		switch v.Kind {
		case yaml.ScalarNode:
			return v.Value
		case yaml.SequenceNode:
			return renderSequence(v)
		case yaml.MappingNode:
			return "{…}"
		}
	}
	return ""
}

// SubsectionState returns the raw YAML node for a nested subsection (e.g. the
// "slack" child of the "integrations" section), or nil when absent.
func (s *SectionState) SubsectionState(name string) *SectionState {
	if s == nil || s.Node == nil || s.Node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(s.Node.Content)-1; i += 2 {
		k := s.Node.Content[i]
		v := s.Node.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != name {
			continue
		}
		present, enabled := evaluatePresence(v)
		node := v
		if !present {
			node = nil
		}
		return &SectionState{Name: name, Present: present, Enabled: enabled, Node: node}
	}
	return nil
}

// renderSequence returns a compact "[a, b, c]" display of a sequence node.
// For non-trivial element types ("secret:" refs, simple scalars) this stays
// readable and never resolves secret refs.
func renderSequence(seq *yaml.Node) string {
	parts := make([]string, 0, len(seq.Content))
	for _, c := range seq.Content {
		if c.Kind != yaml.ScalarNode {
			parts = append(parts, "…")
			continue
		}
		parts = append(parts, c.Value)
	}
	if len(parts) == 0 {
		return "[]"
	}
	out := "["
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out + "]"
}
