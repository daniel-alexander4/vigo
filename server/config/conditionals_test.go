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

// --- ModuleRef YAML unmarshal tests ---

func TestModuleRef_PlainString(t *testing.T) {
	yamlData := `modules: [nginx]`
	var out struct {
		Modules []ModuleRef `yaml:"modules"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(out.Modules))
	}
	if out.Modules[0].Name != "nginx" {
		t.Errorf("Name = %q, want nginx", out.Modules[0].Name)
	}
	if len(out.Modules[0].GoIfAllPass) != 0 {
		t.Errorf("GoIfAllPass = %v, want empty", out.Modules[0].GoIfAllPass)
	}
}

func TestModuleRef_ObjectWithGoIfAllPass(t *testing.T) {
	yamlData := `
modules:
  - name: nginx
    go_if_all_pass: is_debian
`
	var out struct {
		Modules []ModuleRef `yaml:"modules"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(out.Modules))
	}
	if out.Modules[0].Name != "nginx" {
		t.Errorf("Name = %q, want nginx", out.Modules[0].Name)
	}
	if len(out.Modules[0].GoIfAllPass) != 1 || out.Modules[0].GoIfAllPass[0] != "is_debian" {
		t.Errorf("GoIfAllPass = %v, want [is_debian]", out.Modules[0].GoIfAllPass)
	}
}

func TestModuleRef_ObjectWithListForm(t *testing.T) {
	yamlData := `
modules:
  - name: nginx
    skip_if_any_pass: [is_container, is_vm]
`
	var out struct {
		Modules []ModuleRef `yaml:"modules"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Modules) != 1 {
		t.Fatalf("len(Modules) = %d, want 1", len(out.Modules))
	}
	if len(out.Modules[0].SkipIfAnyPass) != 2 {
		t.Fatalf("SkipIfAnyPass = %v, want [is_container, is_vm]", out.Modules[0].SkipIfAnyPass)
	}
	if out.Modules[0].SkipIfAnyPass[0] != "is_container" || out.Modules[0].SkipIfAnyPass[1] != "is_vm" {
		t.Errorf("SkipIfAnyPass = %v, want [is_container, is_vm]", out.Modules[0].SkipIfAnyPass)
	}
}

func TestModuleRef_MixedPlainAndObject(t *testing.T) {
	yamlData := `
modules:
  - nginx
  - name: redis
    go_if_all_pass: is_debian
`
	var out struct {
		Modules []ModuleRef `yaml:"modules"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Modules) != 2 {
		t.Fatalf("len(Modules) = %d, want 2", len(out.Modules))
	}
	if out.Modules[0].Name != "nginx" {
		t.Errorf("Modules[0].Name = %q, want nginx", out.Modules[0].Name)
	}
	if len(out.Modules[0].GoIfAllPass) != 0 {
		t.Errorf("Modules[0].GoIfAllPass = %v, want empty", out.Modules[0].GoIfAllPass)
	}
	if out.Modules[1].Name != "redis" {
		t.Errorf("Modules[1].Name = %q, want redis", out.Modules[1].Name)
	}
	if len(out.Modules[1].GoIfAllPass) != 1 || out.Modules[1].GoIfAllPass[0] != "is_debian" {
		t.Errorf("Modules[1].GoIfAllPass = %v, want [is_debian]", out.Modules[1].GoIfAllPass)
	}
}

// --- ShouldIncludeModule tests ---

func TestShouldIncludeModule_NoConditionals(t *testing.T) {
	mod := ModuleConfig{Name: "basic"}
	if !ShouldIncludeModule(mod, map[string]any{}) {
		t.Error("expected true when no conditional fields set")
	}
}

func TestShouldIncludeModule_GoIfAllPassFalse(t *testing.T) {
	mod := ModuleConfig{
		Name:        "nginx",
		GoIfAllPass: StringOrSlice{"is_debian"},
	}
	vars := map[string]any{"is_debian": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when go_if_all_pass var is false")
	}
}

func TestShouldIncludeModule_GoIfAllPassTrue(t *testing.T) {
	mod := ModuleConfig{
		Name:        "nginx",
		GoIfAllPass: StringOrSlice{"is_debian"},
	}
	vars := map[string]any{"is_debian": "true"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when go_if_all_pass var is true")
	}
}

func TestShouldIncludeModule_SkipIfAnyPassTrue(t *testing.T) {
	mod := ModuleConfig{
		Name:          "desktop-tools",
		SkipIfAnyPass: StringOrSlice{"is_container"},
	}
	vars := map[string]any{"is_container": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when skip_if_any_pass var is true")
	}
}

func TestShouldIncludeModule_SkipIfAnyPassFalse(t *testing.T) {
	mod := ModuleConfig{
		Name:          "desktop-tools",
		SkipIfAnyPass: StringOrSlice{"is_container"},
	}
	vars := map[string]any{"is_container": "false"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when skip_if_any_pass var is false")
	}
}

func TestShouldIncludeModule_GoIfAnyPassNoneTrue(t *testing.T) {
	mod := ModuleConfig{
		Name:        "tool",
		GoIfAnyPass: StringOrSlice{"is_debian", "is_ubuntu"},
	}
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when no go_if_any_pass vars are true")
	}
}

func TestShouldIncludeModule_SkipIfAllPassAllTrue(t *testing.T) {
	mod := ModuleConfig{
		Name:          "tool",
		SkipIfAllPass: StringOrSlice{"is_legacy", "is_deprecated"},
	}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when all skip_if_all_pass vars are true")
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
