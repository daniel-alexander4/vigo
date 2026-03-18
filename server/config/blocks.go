// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FlattenResourceNodes takes raw YAML nodes from a module's resources list
// and flattens any conditional_block entries into plain ResourceConfig items
// with block-level when expressions merged onto each child resource.
//
// Each node is either a plain resource (decoded as ResourceConfig) or a
// mapping with a single "conditional_block" key whose value is a
// ConditionalBlock. Block-level when is AND-composed with each child
// resource's when.
func FlattenResourceNodes(nodes []*yaml.Node) ([]ResourceConfig, error) {
	var result []ResourceConfig

	for _, node := range nodes {
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("expected mapping node, got %v", node.Kind)
		}

		// Check if this mapping has a "conditional_block" key.
		blockIdx := -1
		for i := 0; i < len(node.Content)-1; i += 2 {
			if node.Content[i].Value == "conditional_block" {
				blockIdx = i
				break
			}
		}

		if blockIdx < 0 {
			// Plain resource -- decode directly.
			var res ResourceConfig
			if err := node.Decode(&res); err != nil {
				return nil, fmt.Errorf("decoding resource: %w", err)
			}
			normalizeResourceAttrs(res.Attributes)
			result = append(result, res)
			continue
		}

		// Conditional block -- decode the value node.
		var block ConditionalBlock
		if err := node.Content[blockIdx+1].Decode(&block); err != nil {
			return nil, fmt.Errorf("decoding conditional_block: %w", err)
		}

		if len(block.Resources) == 0 {
			return nil, fmt.Errorf("conditional_block has no resources")
		}

		// Merge block-level when onto each child resource.
		for i := range block.Resources {
			mergeConditionals(&block, &block.Resources[i])
			normalizeResourceAttrs(block.Resources[i].Attributes)
			result = append(result, block.Resources[i])
		}
	}

	return result, nil
}

// normalizeResourceAttrs fixes YAML parsing quirks in resource attributes:
//   - mode: 0644 (parsed as octal int 420) → string "0644"
//   - targetpath → target_path (canonical form)
func normalizeResourceAttrs(attrs map[string]any) {
	// Coerce integer mode to octal string.
	if v, ok := attrs["mode"]; ok {
		if n, isInt := v.(int); isInt {
			attrs["mode"] = fmt.Sprintf("%04o", n)
		}
	}
	// Normalize targetpath → target_path.
	if v, ok := attrs["targetpath"]; ok {
		if _, hasCanonical := attrs["target_path"]; !hasCanonical {
			attrs["target_path"] = v
		}
		delete(attrs, "targetpath")
	}
}

// mergeConditionals combines block-level and resource-level when expressions.
// If both have when, they are AND-composed: "(block) and (resource)".
// If only one has when, it is used directly.
func mergeConditionals(block *ConditionalBlock, res *ResourceConfig) {
	if block.When == "" {
		return
	}
	if res.When == "" {
		res.When = block.When
		return
	}
	res.When = "(" + block.When + ") and (" + res.When + ")"
}
