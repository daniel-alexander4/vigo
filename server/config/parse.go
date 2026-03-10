package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ParseModuleFile parses raw YAML bytes into a ModuleDefinition, using the
// same tolerant node-based parsing as the config loader. This handles cases
// where resources have duplicate YAML keys (e.g., "name" as both resource
// identifier and type-specific attribute) by manually walking the YAML nodes.
func ParseModuleFile(data []byte) (*ModuleDefinition, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
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
				return nil, fmt.Errorf("decoding depends_on: %w", err)
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
// (e.g., "package:", "service:", "username:", "repo_name:").
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
		case key == "depends_on" || key == "require":
			if err := val.Decode(&res.DependsOn); err != nil {
				return res, fmt.Errorf("decoding %s: %w", key, err)
			}
		default:
			var v any
			if err := val.Decode(&v); err != nil {
				return res, fmt.Errorf("decoding attribute %q: %w", key, err)
			}
			res.Attributes[key] = v
		}
	}

	return res, nil
}
