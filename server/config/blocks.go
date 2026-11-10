// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

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
