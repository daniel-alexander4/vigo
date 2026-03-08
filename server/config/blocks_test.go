package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func assertWhenEqual(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

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
// when flattens into child resources with block when merged (Test 2).
func TestFlattenBlockMergesConditionals(t *testing.T) {
	input := `
- conditional_block:
    when: "is_debian"
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
	assertWhenEqual(t, "r1.When", resources[0].When, "is_debian")
	if resources[1].Name != "r2" || resources[1].Type != "exec" {
		t.Errorf("resources[1] = {%q, %q}, want {r2, exec}", resources[1].Name, resources[1].Type)
	}
	assertWhenEqual(t, "r2.When", resources[1].When, "is_debian")
}

// TestFlattenBlockMergesDifferentConditionals verifies that block-level
// when merges with resource-level when via AND composition (COND-12, Test 3).
func TestFlattenBlockMergesDifferentConditionals(t *testing.T) {
	input := `
- conditional_block:
    when: "is_debian"
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
        when: "not is_container"
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("len = %d, want 1", len(resources))
	}
	assertWhenEqual(t, "r1.When", resources[0].When, "(is_debian) and (not is_container)")
}

// TestFlattenBlockSameFieldConcatenated verifies that when expressions on
// block and resource are AND-composed (Test 4).
func TestFlattenBlockSameFieldConcatenated(t *testing.T) {
	input := `
- conditional_block:
    when: "is_debian"
    resources:
      - name: r1
        type: file
        targetpath: /etc/r1
        when: "has_enough_mem"
`
	resources, err := flattenFromYAML(t, input)
	if err != nil {
		t.Fatalf("FlattenResourceNodes: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("len = %d, want 1", len(resources))
	}
	assertWhenEqual(t, "r1.When", resources[0].When, "(is_debian) and (has_enough_mem)")
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
    when: "x"
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
	assertWhenEqual(t, "inside.When", resources[1].When, "x")
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
    when: "x"
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

// TestFlattenBlockWhenExpression verifies that a when expression on a block
// is merged onto child resources (Test 9).
func TestFlattenBlockWhenExpression(t *testing.T) {
	input := `
- conditional_block:
    when: "a and b"
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
	assertWhenEqual(t, "When", r.When, "a and b")
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
      when: "is_debian"
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
	assertWhenEqual(t, "apt-config.When", def.Resources[1].When, "is_debian")
	// apt-update from block
	if def.Resources[2].Name != "apt-update" {
		t.Errorf("Resources[2].Name = %q, want apt-update", def.Resources[2].Name)
	}
	assertWhenEqual(t, "apt-update.When", def.Resources[2].When, "is_debian")
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
