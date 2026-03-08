package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// --- EvaluateResourceWhen tests ---

func TestEvaluateResourceWhen_EmptyWhen(t *testing.T) {
	include, agentWhen, err := EvaluateResourceWhen("", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true for empty when expression")
	}
	if agentWhen != "" {
		t.Errorf("expected empty agentWhen, got %q", agentWhen)
	}
}

func TestEvaluateResourceWhen_SimpleVarTrue(t *testing.T) {
	vars := map[string]any{"is_debian": "true"}
	include, agentWhen, err := EvaluateResourceWhen("is_debian", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true when var is true")
	}
	if agentWhen != "" {
		t.Errorf("expected empty agentWhen, got %q", agentWhen)
	}
}

func TestEvaluateResourceWhen_SimpleVarFalse(t *testing.T) {
	vars := map[string]any{"is_debian": "false"}
	include, _, err := EvaluateResourceWhen("is_debian", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false when var is false")
	}
}

func TestEvaluateResourceWhen_AndAllTrue(t *testing.T) {
	vars := map[string]any{"is_debian": "true", "has_mem": "true"}
	include, _, err := EvaluateResourceWhen("is_debian and has_mem", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true when all AND conditions are true")
	}
}

func TestEvaluateResourceWhen_AndOneFalse(t *testing.T) {
	vars := map[string]any{"is_debian": "true", "has_mem": "false"}
	include, _, err := EvaluateResourceWhen("is_debian and has_mem", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false when one AND condition is false")
	}
}

func TestEvaluateResourceWhen_OrOneTrue(t *testing.T) {
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "true"}
	include, _, err := EvaluateResourceWhen("is_debian or is_ubuntu", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true when one OR condition is true")
	}
}

func TestEvaluateResourceWhen_OrAllFalse(t *testing.T) {
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "false"}
	include, _, err := EvaluateResourceWhen("is_debian or is_ubuntu", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false when all OR conditions are false")
	}
}

func TestEvaluateResourceWhen_NotTrue(t *testing.T) {
	vars := map[string]any{"is_windows": "true"}
	include, _, err := EvaluateResourceWhen("not is_windows", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false when NOT of true var")
	}
}

func TestEvaluateResourceWhen_NotFalse(t *testing.T) {
	vars := map[string]any{"is_windows": "false"}
	include, _, err := EvaluateResourceWhen("not is_windows", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true when NOT of false var")
	}
}

func TestEvaluateResourceWhen_CombinedAndNot(t *testing.T) {
	vars := map[string]any{"is_debian": "true", "is_legacy": "true"}
	include, _, err := EvaluateResourceWhen("is_debian and not is_legacy", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false: var passes but NOT condition triggers")
	}
}

func TestEvaluateResourceWhen_CombinedAndNotPass(t *testing.T) {
	vars := map[string]any{"is_debian": "true", "is_legacy": "false"}
	include, _, err := EvaluateResourceWhen("is_debian and not is_legacy", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true: var passes and NOT condition doesn't trigger")
	}
}

func TestEvaluateResourceWhen_UnknownVarTreatedAsFalse(t *testing.T) {
	include, _, err := EvaluateResourceWhen("nonexistent_var", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false: unknown var name should be treated as false")
	}
}

func TestEvaluateResourceWhen_BuiltinPassedToAgent(t *testing.T) {
	vars := map[string]any{"is_debian": "true"}
	include, agentWhen, err := EvaluateResourceWhen("is_debian and file_exists('/etc/foo')", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !include {
		t.Error("expected true (pass to agent)")
	}
	if agentWhen == "" {
		t.Error("expected non-empty agentWhen for builtin expression")
	}
}

func TestEvaluateResourceWhen_BuiltinShortCircuitFalse(t *testing.T) {
	vars := map[string]any{"is_debian": "false"}
	include, agentWhen, err := EvaluateResourceWhen("is_debian and file_exists('/etc/foo')", vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if include {
		t.Error("expected false: var is false, short-circuit AND")
	}
	if agentWhen != "" {
		t.Errorf("expected empty agentWhen after short-circuit, got %q", agentWhen)
	}
}

// --- ShouldIncludeModule tests ---

func TestShouldIncludeModule_NoConditionals(t *testing.T) {
	mod := ModuleConfig{Name: "basic"}
	if !ShouldIncludeModule(mod, map[string]any{}) {
		t.Error("expected true when no when expression set")
	}
}

func TestShouldIncludeModule_WhenFalse(t *testing.T) {
	mod := ModuleConfig{Name: "nginx", When: "is_debian"}
	vars := map[string]any{"is_debian": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when when var is false")
	}
}

func TestShouldIncludeModule_WhenTrue(t *testing.T) {
	mod := ModuleConfig{Name: "nginx", When: "is_debian"}
	vars := map[string]any{"is_debian": "true"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when when var is true")
	}
}

func TestShouldIncludeModule_WhenNotTrue(t *testing.T) {
	mod := ModuleConfig{Name: "desktop-tools", When: "not is_container"}
	vars := map[string]any{"is_container": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when not condition triggers")
	}
}

func TestShouldIncludeModule_WhenNotFalse(t *testing.T) {
	mod := ModuleConfig{Name: "desktop-tools", When: "not is_container"}
	vars := map[string]any{"is_container": "false"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when not condition doesn't trigger")
	}
}

func TestShouldIncludeModule_WhenOrNoneTrue(t *testing.T) {
	mod := ModuleConfig{Name: "tool", When: "is_debian or is_ubuntu"}
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when no OR vars are true")
	}
}

func TestShouldIncludeModule_WhenAndNotAll(t *testing.T) {
	mod := ModuleConfig{Name: "tool", When: "not (is_legacy and is_deprecated)"}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when all conditions in NOT(AND) are true")
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
	if out.Modules[0].When != "" {
		t.Errorf("When = %q, want empty", out.Modules[0].When)
	}
}

func TestModuleRef_ObjectWithWhen(t *testing.T) {
	yamlData := `
modules:
  - name: nginx
    when: "is_debian"
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
	if out.Modules[0].When != "is_debian" {
		t.Errorf("When = %q, want is_debian", out.Modules[0].When)
	}
}

func TestModuleRef_ObjectWithWhenExpression(t *testing.T) {
	yamlData := `
modules:
  - name: nginx
    when: "not is_container and not is_vm"
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
	if out.Modules[0].When != "not is_container and not is_vm" {
		t.Errorf("When = %q, want 'not is_container and not is_vm'", out.Modules[0].When)
	}
}

func TestModuleRef_MixedPlainAndObject(t *testing.T) {
	yamlData := `
modules:
  - nginx
  - name: redis
    when: "is_debian"
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
	if out.Modules[0].When != "" {
		t.Errorf("Modules[0].When = %q, want empty", out.Modules[0].When)
	}
	if out.Modules[1].Name != "redis" {
		t.Errorf("Modules[1].Name = %q, want redis", out.Modules[1].Name)
	}
	if out.Modules[1].When != "is_debian" {
		t.Errorf("Modules[1].When = %q, want is_debian", out.Modules[1].When)
	}
}
