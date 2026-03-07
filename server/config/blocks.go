package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FlattenResourceNodes takes raw YAML nodes from a module's resources list
// and flattens any conditional_block entries into plain ResourceConfig items
// with block-level conditionals merged onto each child resource.
func FlattenResourceNodes(nodes []*yaml.Node) ([]ResourceConfig, error) {
	return nil, fmt.Errorf("not implemented")
}
