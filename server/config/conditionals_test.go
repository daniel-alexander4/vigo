package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestShouldRunResource_GoIfAllPass_AllTrue(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass: StringOrSlice{"is_debian", "has_mem"},
	}
	vars := map[string]any{"is_debian": "true", "has_mem": "true"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true when all go_if_all_pass conditions are true")
	}
}

func TestShouldRunResource_GoIfAllPass_OneFalse(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass: StringOrSlice{"is_debian", "has_mem"},
	}
	vars := map[string]any{"is_debian": "true", "has_mem": "false"}
	if ShouldRunResource(res, vars) {
		t.Error("expected false when one go_if_all_pass condition is false")
	}
}

func TestShouldRunResource_GoIfAnyPass_OneTrue(t *testing.T) {
	res := ResourceConfig{
		GoIfAnyPass: StringOrSlice{"is_debian", "is_ubuntu"},
	}
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "true"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true when at least one go_if_any_pass condition is true")
	}
}

func TestShouldRunResource_GoIfAnyPass_AllFalse(t *testing.T) {
	res := ResourceConfig{
		GoIfAnyPass: StringOrSlice{"is_debian", "is_ubuntu"},
	}
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "false"}
	if ShouldRunResource(res, vars) {
		t.Error("expected false when all go_if_any_pass conditions are false")
	}
}

func TestShouldRunResource_SkipIfAllPass_AllTrue(t *testing.T) {
	res := ResourceConfig{
		SkipIfAllPass: StringOrSlice{"is_legacy", "is_deprecated"},
	}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "true"}
	if ShouldRunResource(res, vars) {
		t.Error("expected false (skip) when all skip_if_all_pass conditions are true")
	}
}

func TestShouldRunResource_SkipIfAllPass_OneFalse(t *testing.T) {
	res := ResourceConfig{
		SkipIfAllPass: StringOrSlice{"is_legacy", "is_deprecated"},
	}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "false"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true (run) when one skip_if_all_pass condition is false")
	}
}

func TestShouldRunResource_SkipIfAnyPass_AnyTrue(t *testing.T) {
	res := ResourceConfig{
		SkipIfAnyPass: StringOrSlice{"is_legacy", "is_deprecated"},
	}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "false"}
	if ShouldRunResource(res, vars) {
		t.Error("expected false (skip) when any skip_if_any_pass condition is true")
	}
}

func TestShouldRunResource_SkipIfAnyPass_AllFalse(t *testing.T) {
	res := ResourceConfig{
		SkipIfAnyPass: StringOrSlice{"is_legacy", "is_deprecated"},
	}
	vars := map[string]any{"is_legacy": "false", "is_deprecated": "false"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true (run) when all skip_if_any_pass conditions are false")
	}
}

func TestShouldRunResource_NoConditionals(t *testing.T) {
	res := ResourceConfig{}
	vars := map[string]any{"whatever": "true"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true when no conditional fields are set")
	}
}

func TestShouldRunResource_CombinedGoPassButSkipTriggers(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass:   StringOrSlice{"is_debian"},
		SkipIfAnyPass: StringOrSlice{"is_legacy"},
	}
	vars := map[string]any{"is_debian": "true", "is_legacy": "true"}
	if ShouldRunResource(res, vars) {
		t.Error("expected false: go_if_all_pass passes but skip_if_any_pass also triggers")
	}
}

func TestShouldRunResource_CombinedGoPassNoSkip(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass:   StringOrSlice{"is_debian"},
		SkipIfAnyPass: StringOrSlice{"is_legacy"},
	}
	vars := map[string]any{"is_debian": "true", "is_legacy": "false"}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true: go_if_all_pass passes and no skip triggers")
	}
}

func TestShouldRunResource_UnknownVarTreatedAsFalse(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass: StringOrSlice{"nonexistent_var"},
	}
	vars := map[string]any{}
	if ShouldRunResource(res, vars) {
		t.Error("expected false: unknown var name should be treated as false")
	}
}

func TestShouldRunResource_EmptyList(t *testing.T) {
	res := ResourceConfig{
		GoIfAllPass: StringOrSlice{},
	}
	vars := map[string]any{}
	if !ShouldRunResource(res, vars) {
		t.Error("expected true: empty conditional list should be treated as no constraint")
	}
}

// --- StringOrSlice YAML unmarshal tests ---

func TestStringOrSlice_SingleString(t *testing.T) {
	yamlData := `go_if_all_pass: is_debian`
	var out struct {
		GoIfAllPass StringOrSlice `yaml:"go_if_all_pass"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GoIfAllPass) != 1 || out.GoIfAllPass[0] != "is_debian" {
		t.Errorf("got %v, want [is_debian]", out.GoIfAllPass)
	}
}

func TestStringOrSlice_List(t *testing.T) {
	yamlData := `go_if_all_pass: [is_debian, has_mem]`
	var out struct {
		GoIfAllPass StringOrSlice `yaml:"go_if_all_pass"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GoIfAllPass) != 2 || out.GoIfAllPass[0] != "is_debian" || out.GoIfAllPass[1] != "has_mem" {
		t.Errorf("got %v, want [is_debian, has_mem]", out.GoIfAllPass)
	}
}

func TestStringOrSlice_AllFourFields(t *testing.T) {
	yamlData := `
go_if_all_pass: is_debian
go_if_any_pass: [a, b]
skip_if_all_pass: legacy
skip_if_any_pass: [x, y, z]
`
	var out struct {
		GoIfAllPass   StringOrSlice `yaml:"go_if_all_pass"`
		GoIfAnyPass   StringOrSlice `yaml:"go_if_any_pass"`
		SkipIfAllPass StringOrSlice `yaml:"skip_if_all_pass"`
		SkipIfAnyPass StringOrSlice `yaml:"skip_if_any_pass"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GoIfAllPass) != 1 || out.GoIfAllPass[0] != "is_debian" {
		t.Errorf("GoIfAllPass = %v, want [is_debian]", out.GoIfAllPass)
	}
	if len(out.GoIfAnyPass) != 2 {
		t.Errorf("GoIfAnyPass = %v, want [a, b]", out.GoIfAnyPass)
	}
	if len(out.SkipIfAllPass) != 1 || out.SkipIfAllPass[0] != "legacy" {
		t.Errorf("SkipIfAllPass = %v, want [legacy]", out.SkipIfAllPass)
	}
	if len(out.SkipIfAnyPass) != 3 {
		t.Errorf("SkipIfAnyPass = %v, want [x, y, z]", out.SkipIfAnyPass)
	}
}
