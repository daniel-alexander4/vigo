// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolDefs_Count(t *testing.T) {
	defs := toolDefs()
	assert.Equal(t, 14, len(defs), "expected 14 tool definitions")
}

func TestToolDefs_UniqueNames(t *testing.T) {
	defs := toolDefs()
	seen := make(map[string]bool)
	for _, d := range defs {
		assert.False(t, seen[d.Name], "duplicate tool name: %s", d.Name)
		seen[d.Name] = true
	}
}

func TestToolDefs_HaveDescriptions(t *testing.T) {
	for _, d := range toolDefs() {
		assert.NotEmpty(t, d.Description, "tool %s has no description", d.Name)
	}
}

func TestMatchFilters(t *testing.T) {
	traits := map[string]any{
		"os": map[string]any{
			"distro":  "ubuntu",
			"version": "22.04",
		},
		"hardware": map[string]any{
			"memory_mb": 8192,
		},
	}

	assert.True(t, matchFilters(traits, []filter{{path: "os.distro", value: "ubuntu"}}))
	assert.False(t, matchFilters(traits, []filter{{path: "os.distro", value: "debian"}}))
	assert.True(t, matchFilters(traits, []filter{
		{path: "os.distro", value: "ubuntu"},
		{path: "os.version", value: "22.04"},
	}))
	assert.True(t, matchFilters(traits, nil))
}

func TestGetNestedValue(t *testing.T) {
	m := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
		},
	}

	assert.Equal(t, "ubuntu", getNestedValue(m, "os.distro"))
	assert.Nil(t, getNestedValue(m, "os.missing"))
	assert.Nil(t, getNestedValue(m, "missing.path"))
}

func TestIntArg(t *testing.T) {
	args := map[string]any{
		"limit":   float64(42),
		"count":   10,
		"invalid": "not a number",
	}

	assert.Equal(t, 42, intArg(args, "limit", 0))
	assert.Equal(t, 10, intArg(args, "count", 0))
	assert.Equal(t, 99, intArg(args, "missing", 99))
	assert.Equal(t, 99, intArg(args, "invalid", 99))
}
