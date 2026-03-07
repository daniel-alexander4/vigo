package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestFlattenPlainResourcesPassThrough verifies that plain resources without
// blocks pass through unchanged (Test 1).
func TestFlattenPlainResourcesPassThrough(t *testing.T) {
	input := `
- name: a
  type: file
  targetpath: /etc/a
- name: b
  type: package
  package: nginx
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len = %d, want 2", len(resources))
	}
	if resources[0].Name != "a" || resources[0].Type != "file" {
		t.Errorf("resources[0] = {%q, %q}, want {a, file}", resources[0].Name, resources[0].Type)
	}
	if resources[1].Name != "b" || resources[1].Type != "package" {
		t.Errorf("resources[1] = {%q, %q}, want {b, package}", resources[1].Name, resources[1].Type)
	}
}

// TestFlattenBlockMergesConditionals verifies that a single block with
// go_if_all_pass flattens into child resources with block conditionals
// merged (Test 2).
func TestFlattenBlockMergesConditionals(t *testing.T) {
	input := `
- conditional_block:
    go_if_all_pass: is_debian
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
      - name: r2
        type: exec
        command: "apt-get update"
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len = %d, want 2", len(resources))
	}
	if resources[0].Name != "r1" || resources[0].Type != "file" {
		t.Errorf("resources[0] = {%q, %q}, want {r1, file}", resources[0].Name, resources[0].Type)
	}
	assertSliceEqual(t, "r1.GoIfAllPass", resources[0].GoIfAllPass, []string{"is_debian"})
	if resources[1].Name != "r2" || resources[1].Type != "exec" {
		t.Errorf("resources[1] = {%q, %q}, want {r2, exec}", resources[1].Name, resources[1].Type)
	}
	assertSliceEqual(t, "r2.GoIfAllPass", resources[1].GoIfAllPass, []string{"is_debian"})
}

// TestFlattenBlockMergesDifferentConditionals verifies that block-level
// conditionals merge with resource-level conditionals on different fields
// (COND-12, Test 3).
func TestFlattenBlockMergesDifferentConditionals(t *testing.T) {
	input := `
- conditional_block:
    go_if_all_pass: is_debian
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
        skip_if_any_pass: is_container
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("len = %d, want 1", len(resources))
	}
	assertSliceEqual(t, "r1.GoIfAllPass", resources[0].GoIfAllPass, []string{"is_debian"})
	assertSliceEqual(t, "r1.SkipIfAnyPass", resources[0].SkipIfAnyPass, []string{"is_container"})
}

// TestFlattenBlockSameFieldConcatenated verifies that the same conditional
// field on block and resource are concatenated (Test 4).
func TestFlattenBlockSameFieldConcatenated(t *testing.T) {
	input := `
- conditional_block:
    go_if_all_pass: is_debian
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
        go_if_all_pass: has_enough_mem
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("len = %d, want 1", len(resources))
	}
	assertSliceEqual(t, "r1.GoIfAllPass", resources[0].GoIfAllPass, []string{"is_debian", "has_enough_mem"})
}

// TestFlattenMixedResourcesAndBlocksOrdering verifies that mixed plain
// resources and blocks maintain ordering (Test 5).
func TestFlattenMixedResourcesAndBlocksOrdering(t *testing.T) {
	input := `
- name: a
  type: file
  targetpath: /a
- conditional_block:
    resources:
      - name: b
        type: file
        targetpath: /b
      - name: c
        type: file
        targetpath: /c
- name: d
  type: file
  targetpath: /d
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 4 {
		t.Fatalf("len = %d, want 4", len(resources))
	}
	names := make([]string, len(resources))
	for i, r := range resources {
		names[i] = r.Name
	}
	expected := []string{"a", "b", "c", "d"}
	assertSliceEqual(t, "names", names, expected)
}

// TestFlattenDependsOnAcrossBlockBoundaryOutsideToInside verifies that
// depends_on from outside a block to inside works (COND-13, Test 6).
func TestFlattenDependsOnAcrossBlockBoundaryOutsideToInside(t *testing.T) {
	input := `
- name: outside
  type: file
  targetpath: /outside
  depends_on: [inside]
- conditional_block:
    go_if_all_pass: x
    resources:
      - name: inside
        type: exec
        command: "echo hello"
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len = %d, want 2", len(resources))
	}
	if resources[0].Name != "outside" {
		t.Errorf("resources[0].Name = %q, want outside", resources[0].Name)
	}
	assertSliceEqual(t, "outside.DependsOn", resources[0].DependsOn, []string{"inside"})
	if resources[1].Name != "inside" {
		t.Errorf("resources[1].Name = %q, want inside", resources[1].Name)
	}
	assertSliceEqual(t, "inside.GoIfAllPass", resources[1].GoIfAllPass, []string{"x"})
}

// TestFlattenDependsOnFromInsideToOutside verifies that depends_on from
// inside a block to outside resource works (Test 7).
func TestFlattenDependsOnFromInsideToOutside(t *testing.T) {
	input := `
- name: outside
  type: file
  targetpath: /outside
- conditional_block:
    resources:
      - name: inside
        type: exec
        command: "echo hello"
        depends_on: [outside]
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("len = %d, want 2", len(resources))
	}
	if resources[0].Name != "outside" {
		t.Errorf("resources[0].Name = %q, want outside", resources[0].Name)
	}
	if resources[1].Name != "inside" {
		t.Errorf("resources[1].Name = %q, want inside", resources[1].Name)
	}
	assertSliceEqual(t, "inside.DependsOn", resources[1].DependsOn, []string{"outside"})
}

// TestFlattenBlockEmptyResourcesError verifies that a block with no
// resources returns an error (Test 8).
func TestFlattenBlockEmptyResourcesError(t *testing.T) {
	input := `
- conditional_block:
    go_if_all_pass: x
    resources: []
`
	_, err := flattenFromYAML(t, input)
	if err == nil {
		t.Fatal("expected error for empty resources in conditional_block, got nil")
	}
	if !strings.Contains(err.Error(), "conditional_block has no resources") {
		t.Errorf("error = %q, want it to contain 'conditional_block has no resources'", err)
	}
}

// TestFlattenBlockAllFourConditionalFields verifies that all four conditional
// fields on a block are merged onto child resources (Test 9).
func TestFlattenBlockAllFourConditionalFields(t *testing.T) {
	input := `
- conditional_block:
    go_if_all_pass: a
    go_if_any_pass: [b, c]
    skip_if_all_pass: d
    skip_if_any_pass: [e]
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("len = %d, want 1", len(resources))
	}
	r := resources[0]
	assertSliceEqual(t, "GoIfAllPass", r.GoIfAllPass, []string{"a"})
	assertSliceEqual(t, "GoIfAnyPass", r.GoIfAnyPass, []string{"b", "c"})
	assertSliceEqual(t, "SkipIfAllPass", r.SkipIfAllPass, []string{"d"})
	assertSliceEqual(t, "SkipIfAnyPass", r.SkipIfAnyPass, []string{"e"})
}

// TestConditionalBlockYAMLUnmarshalIntegration verifies that a full module
// YAML with conditional_block parses correctly via ModuleDefinition's custom
// UnmarshalYAML (Test 10).
func TestConditionalBlockYAMLUnmarshalIntegration(t *testing.T) {
	moduleYAML := `
name: test-module
resources:
  - name: standalone
    type: package
    package: nginx

  - conditional_block:
      go_if_all_pass: is_debian
      resources:
        - name: apt-config
          type: file
          targetpath: /etc/apt/config
          content: "deb http://..."
        - name: apt-update
          type: exec
          command: "apt-get update"
          depends_on: [apt-config]

  - name: final
    type: service
    service: nginx
    state: running
`
	var def ModuleDefinition
	if err := yaml.Unmarshal([]byte(moduleYAML), &def); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if def.Name != "test-module" {
		t.Errorf("Name = %q, want test-module", def.Name)
	}
	if len(def.Resources) != 4 {
		t.Fatalf("len(Resources) = %d, want 4 (standalone + 2 from block + final)", len(def.Resources))
	}
	// standalone
	if def.Resources[0].Name != "standalone" {
		t.Errorf("Resources[0].Name = %q, want standalone", def.Resources[0].Name)
	}
	// apt-config from block
	if def.Resources[1].Name != "apt-config" {
		t.Errorf("Resources[1].Name = %q, want apt-config", def.Resources[1].Name)
	}
	assertSliceEqual(t, "apt-config.GoIfAllPass", def.Resources[1].GoIfAllPass, []string{"is_debian"})
	// apt-update from block
	if def.Resources[2].Name != "apt-update" {
		t.Errorf("Resources[2].Name = %q, want apt-update", def.Resources[2].Name)
	}
	assertSliceEqual(t, "apt-update.GoIfAllPass", def.Resources[2].GoIfAllPass, []string{"is_debian"})
	assertSliceEqual(t, "apt-update.DependsOn", def.Resources[2].DependsOn, []string{"apt-config"})
	// final
	if def.Resources[3].Name != "final" {
		t.Errorf("Resources[3].Name = %q, want final", def.Resources[3].Name)
	}
}

// --- test helpers ---

// flattenFromYAML parses a YAML sequence of resource nodes and calls
// FlattenResourceNodes.
func flattenFromYAML(t *testing.T, yamlStr string) ([]ResourceConfig, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(yamlStr), &node); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	// node.Content[0] is the sequence node
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.SequenceNode {
		t.Fatal("expected YAML sequence node")
	}
	return FlattenResourceNodes(node.Content[0].Content)
}

func assertSliceEqual(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: len = %d, want %d; got %v", name, len(got), len(want), got)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}
