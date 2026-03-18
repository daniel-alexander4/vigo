// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseModuleFile parses raw YAML bytes into a ModuleDefinition, using the
// same tolerant node-based parsing as the config loader. This handles cases
// where resources have duplicate YAML keys (e.g., "name" as both resource
// identifier and type-specific attribute) by manually walking the YAML nodes.
func ParseModuleFile(data []byte) (*ModuleDefinition, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, formatYAMLError(data, err)
	}

	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}

	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root, got %v", root.Kind)
	}

	var def ModuleDefinition
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]

		switch key {
		case "name":
			def.Name = val.Value
		case "depends_on":
			if err := val.Decode(&def.DependsOn); err != nil {
				return nil, fmt.Errorf("decoding %s: %w", key, err)
			}
		case "before":
			if err := val.Decode(&def.Before); err != nil {
				return nil, fmt.Errorf("decoding before: %w", err)
			}
		case "defaults":
			if err := val.Decode(&def.Defaults); err != nil {
				return nil, fmt.Errorf("decoding defaults: %w", err)
			}
		case "vars":
			if err := val.Decode(&def.Vars); err != nil {
				return nil, fmt.Errorf("decoding vars: %w", err)
			}
		case "resources":
			if val.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("resources: expected sequence")
			}
			resources, err := parseResourceNodes(val.Content)
			if err != nil {
				return nil, fmt.Errorf("parsing resources: %w", err)
			}
			def.Resources = resources
		}
	}

	applyDefaults(def.Resources, def.Defaults)
	return &def, nil
}

// parseResourceNodes walks resource YAML nodes manually, extracting known
// fields by hand and dumping everything else into Attributes. This avoids
// the duplicate-key error from yaml.Decode when a resource has both
// "name: resource-id" and "name: attr-value".
func parseResourceNodes(nodes []*yaml.Node) ([]ResourceConfig, error) {
	var result []ResourceConfig

	for _, node := range nodes {
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("expected mapping node for resource")
		}

		// Check for conditional_block.
		blockIdx := -1
		for i := 0; i < len(node.Content)-1; i += 2 {
			if node.Content[i].Value == "conditional_block" {
				blockIdx = i
				break
			}
		}

		if blockIdx >= 0 {
			var block ConditionalBlock
			if err := node.Content[blockIdx+1].Decode(&block); err != nil {
				return nil, fmt.Errorf("decoding conditional_block: %w", err)
			}
			if len(block.Resources) == 0 {
				return nil, fmt.Errorf("conditional_block has no resources")
			}
			for i := range block.Resources {
				mergeConditionals(&block, &block.Resources[i])
				result = append(result, block.Resources[i])
			}
			continue
		}

		res, err := parseOneResource(node)
		if err != nil {
			return nil, err
		}
		result = append(result, res)
	}

	return result, nil
}

// parseOneResource extracts a ResourceConfig from a YAML mapping node.
// Duplicate keys are rejected — use type-specific attributes instead
// (e.g., "package:", "service:", "username:", "repository:").
func parseOneResource(node *yaml.Node) (ResourceConfig, error) {
	var res ResourceConfig
	res.Attributes = make(map[string]any)
	seen := make(map[string]bool)

	for i := 0; i < len(node.Content)-1; i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]

		if seen[key] {
			return res, fmt.Errorf("resource %q: duplicate key %q (line %d)", res.Name, key, node.Content[i].Line)
		}
		seen[key] = true

		switch {
		case key == "name":
			res.Name = val.Value
		case key == "type":
			res.Type = val.Value
		case key == "when":
			res.When = val.Value
		case key == "notify":
			if err := val.Decode(&res.Notify); err != nil {
				return res, fmt.Errorf("decoding notify: %w", err)
			}
		case key == "subscribes":
			if err := val.Decode(&res.Subscribes); err != nil {
				return res, fmt.Errorf("decoding subscribes: %w", err)
			}
		case key == "watch_secret":
			if err := val.Decode(&res.WatchSecret); err != nil {
				return res, fmt.Errorf("decoding watch_secret: %w", err)
			}
		case key == "depends_on":
			if err := val.Decode(&res.DependsOn); err != nil {
				return res, fmt.Errorf("decoding depends_on: %w", err)
			}
		case key == "before":
			if err := val.Decode(&res.Before); err != nil {
				return res, fmt.Errorf("decoding before: %w", err)
			}
		case key == "foreach":
			res.ForEach = val.Value
		case key == "case":
			res.Case = val.Value
		case key == "match":
			var m map[string]map[string]any
			if err := val.Decode(&m); err != nil {
				return res, fmt.Errorf("decoding match: %w", err)
			}
			res.CaseMatch = m
		default:
			var v any
			if err := val.Decode(&v); err != nil {
				return res, fmt.Errorf("decoding attribute %q: %w", key, err)
			}
			res.Attributes[key] = v
		}
	}

	// Validate case/match pairing.
	if res.Case != "" && res.CaseMatch == nil {
		return res, fmt.Errorf("resource %q: 'case' requires 'match'", res.Name)
	}
	if res.CaseMatch != nil && res.Case == "" {
		return res, fmt.Errorf("resource %q: 'match' requires 'case'", res.Name)
	}
	if res.CaseMatch != nil {
		if _, ok := res.CaseMatch["default"]; !ok {
			return res, fmt.Errorf("resource %q: 'match' must include a 'default' branch", res.Name)
		}
	}

	return res, nil
}

// formatYAMLError extracts the line number from a YAML parse error and
// includes the offending line content for easier debugging.
func formatYAMLError(data []byte, err error) error {
	msg := err.Error()

	// Extract line number from "yaml: line N: ..."
	var lineNum int
	if _, scanErr := fmt.Sscanf(msg, "yaml: line %d:", &lineNum); scanErr == nil && lineNum > 0 {
		lines := strings.Split(string(data), "\n")
		if lineNum <= len(lines) {
			line := lines[lineNum-1]
			// Show the line with visible whitespace indicators.
			visible := strings.ReplaceAll(line, "\t", "→TAB")
			hint := ""
			if strings.HasPrefix(line, "\t") {
				hint = " (tabs are not allowed in YAML — use spaces)"
			} else if lineNum > 1 {
				// Check if indentation looks wrong relative to context.
				prev := lines[lineNum-2]
				if len(line) > 0 && len(prev) > 0 && line[0] == ' ' && prev[0] != ' ' {
					hint = " (this line is indented but the previous line is not — check alignment)"
				}
			}
			return fmt.Errorf("line %d: %s\n  %d | %s%s", lineNum, strings.TrimPrefix(msg, fmt.Sprintf("yaml: line %d: ", lineNum)), lineNum, visible, hint)
		}
	}

	return fmt.Errorf("YAML: %w", err)
}

// structuralFields are resource keys that should not be set via defaults.
var structuralFields = map[string]bool{
	"name": true, "type": true, "when": true,
	"notify": true, "subscribes": true, "watch_secret": true,
	"depends_on": true, "before": true,
	"foreach": true, "case": true, "match": true,
}

// applyDefaults merges module-level defaults into each resource's Attributes.
// Only sets keys that are not already present and not structural fields.
func applyDefaults(resources []ResourceConfig, defaults map[string]any) {
	if len(defaults) == 0 {
		return
	}
	for i := range resources {
		if resources[i].Attributes == nil {
			resources[i].Attributes = make(map[string]any)
		}
		for k, v := range defaults {
			if structuralFields[k] {
				continue
			}
			if _, exists := resources[i].Attributes[k]; !exists {
				resources[i].Attributes[k] = v
			}
		}
	}
}
