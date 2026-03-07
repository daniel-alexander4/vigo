package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FlattenResourceNodes takes raw YAML nodes from a module's resources list
// and flattens any conditional_block entries into plain ResourceConfig items
// with block-level conditionals merged onto each child resource.
//
// Each node is either a plain resource (decoded as ResourceConfig) or a
// mapping with a single "conditional_block" key whose value is a
// ConditionalBlock. Block-level conditionals are appended to each child
// resource's corresponding fields.
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

		// Merge block-level conditionals onto each child resource.
		for i := range block.Resources {
			mergeConditionals(&block, &block.Resources[i])
			result = append(result, block.Resources[i])
		}
	}

	return result, nil
}

// mergeConditionals appends block-level conditional values to the resource's
// corresponding fields. Block values come first (prepended) so that when
// the same field appears on both block and resource, all conditions are
// required to pass.
func mergeConditionals(block *ConditionalBlock, res *ResourceConfig) {
	res.GoIfAllPass = appendSlices(block.GoIfAllPass, res.GoIfAllPass)
	res.GoIfAnyPass = appendSlices(block.GoIfAnyPass, res.GoIfAnyPass)
	res.SkipIfAllPass = appendSlices(block.SkipIfAllPass, res.SkipIfAllPass)
	res.SkipIfAnyPass = appendSlices(block.SkipIfAnyPass, res.SkipIfAnyPass)
}

// appendSlices returns a combined slice with a's elements before b's.
// Returns nil if both are empty to avoid non-nil empty slices.
func appendSlices(a, b StringOrSlice) StringOrSlice {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	result := make(StringOrSlice, 0, len(a)+len(b))
	result = append(result, a...)
	result = append(result, b...)
	return result
}
