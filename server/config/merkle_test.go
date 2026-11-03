// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
)

func TestMerkleRootDeterministic(t *testing.T) {
	configcrates := []Configcrate{
		{Name: "a", ContentHash: "aaa"},
		{Name: "b", ContentHash: "bbb"},
	}
	vars := map[string]any{"key": "val"}
	varOrder := []string{"key"}

	root1 := MerkleRoot(configcrates, vars, varOrder)
	root2 := MerkleRoot(configcrates, vars, varOrder)
	if root1 != root2 {
		t.Fatalf("non-deterministic: %s != %s", root1, root2)
	}
	if root1 == "" {
		t.Fatal("root is empty")
	}
	// SHA256 hex = 64 chars
	if len(root1) != 64 {
		t.Fatalf("unexpected length: %d", len(root1))
	}
}

func TestMerkleRootChangesOnConfigcrateChange(t *testing.T) {
	vars := map[string]any{"k": "v"}
	varOrder := []string{"k"}

	before := MerkleRoot([]Configcrate{
		{Name: "a", ContentHash: "aaa"},
		{Name: "b", ContentHash: "bbb"},
	}, vars, varOrder)

	after := MerkleRoot([]Configcrate{
		{Name: "a", ContentHash: "aaa"},
		{Name: "b", ContentHash: "ccc"}, // changed
	}, vars, varOrder)

	if before == after {
		t.Fatal("root did not change when module hash changed")
	}
}

func TestMerkleRootStableWhenUnchanged(t *testing.T) {
	configcrates := []Configcrate{
		{Name: "x", ContentHash: "xxx"},
		{Name: "y", ContentHash: "yyy"},
		{Name: "z", ContentHash: "zzz"},
	}
	vars := map[string]any{"a": 1, "b": "two"}
	varOrder := []string{"a", "b"}

	root1 := MerkleRoot(configcrates, vars, varOrder)
	root2 := MerkleRoot(configcrates, vars, varOrder)
	if root1 != root2 {
		t.Fatalf("root changed without any input change: %s != %s", root1, root2)
	}
}

func TestMerkleRootChangesOnVarsChange(t *testing.T) {
	configcrates := []Configcrate{
		{Name: "a", ContentHash: "aaa"},
	}

	root1 := MerkleRoot(configcrates, map[string]any{"k": "v1"}, []string{"k"})
	root2 := MerkleRoot(configcrates, map[string]any{"k": "v2"}, []string{"k"})

	if root1 == root2 {
		t.Fatal("root did not change when vars changed")
	}
}

func TestMerkleRootVarsOnlyDiffers(t *testing.T) {
	configcrates := []Configcrate{
		{Name: "a", ContentHash: "aaa"},
	}
	varOrder := []string{"k"}

	withVars := MerkleRoot(configcrates, map[string]any{"k": "v"}, varOrder)
	noVars := MerkleRoot(configcrates, nil, nil)

	if withVars == noVars {
		t.Fatal("root is same with and without vars")
	}
}

func TestMerkleRootEmptyInputs(t *testing.T) {
	root := MerkleRoot(nil, nil, nil)
	if root == "" {
		t.Fatal("root is empty for nil inputs")
	}
	if len(root) != 64 {
		t.Fatalf("unexpected length: %d", len(root))
	}
}

func TestMerkleRootSingleConfigcrate(t *testing.T) {
	root := MerkleRoot([]Configcrate{
		{Name: "only", ContentHash: "abc123"},
	}, nil, nil)
	if root == "" || len(root) != 64 {
		t.Fatalf("bad root for single module: %q", root)
	}
}

func TestMerkleRootOddConfigcrateCount(t *testing.T) {
	// Odd number of modules — the tree must handle the unpaired node.
	configcrates := []Configcrate{
		{Name: "a", ContentHash: "aaa"},
		{Name: "b", ContentHash: "bbb"},
		{Name: "c", ContentHash: "ccc"},
	}
	root := MerkleRoot(configcrates, nil, nil)
	if root == "" || len(root) != 64 {
		t.Fatalf("bad root for odd module count: %q", root)
	}
}

func TestMerkleRootConfigcrateOrderMatters(t *testing.T) {
	vars := map[string]any{"k": "v"}
	varOrder := []string{"k"}

	root1 := MerkleRoot([]Configcrate{
		{Name: "a", ContentHash: "aaa"},
		{Name: "b", ContentHash: "bbb"},
	}, vars, varOrder)

	root2 := MerkleRoot([]Configcrate{
		{Name: "b", ContentHash: "bbb"},
		{Name: "a", ContentHash: "aaa"},
	}, vars, varOrder)

	if root1 == root2 {
		t.Fatal("root is same despite different module order — order should matter (DAG order)")
	}
}
