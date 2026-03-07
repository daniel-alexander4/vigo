package grpc

import (
	"testing"

	"github.com/isogov/isogov/server/conditionals"
	"github.com/isogov/isogov/server/config"
)

func TestEvaluateConditionalVarsMatch(t *testing.T) {
	cfg := &config.ClientConfig{
		Vars:     map[string]any{"port": "8080"},
		VarOrder: []string{"port"},
		ConditionalVars: []conditionals.ConditionalVar{
			{
				Name:     "is_debian",
				Trait:    "os.distro",
				Operator: "only",
				Operand:  []any{"debian", "ubuntu"},
			},
		},
	}

	facts := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}

	vars := map[string]any{"port": "8080"}
	varOrder := []string{"port"}

	vars, varOrder = evaluateConditionalVars(cfg, facts, vars, varOrder)

	// Should set is_debian to "true" since ubuntu is in the only list.
	val, ok := vars["is_debian"]
	if !ok {
		t.Fatal("expected 'is_debian' in vars map")
	}
	if val != "true" {
		t.Errorf("is_debian = %q, want 'true'", val)
	}

	// Regular var should still be present.
	if vars["port"] != "8080" {
		t.Errorf("port = %v, want '8080'", vars["port"])
	}

	// is_debian should be in varOrder.
	found := false
	for _, v := range varOrder {
		if v == "is_debian" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'is_debian' in varOrder")
	}
}

func TestEvaluateConditionalVarsNoMatch(t *testing.T) {
	cfg := &config.ClientConfig{
		ConditionalVars: []conditionals.ConditionalVar{
			{
				Name:     "is_debian",
				Trait:    "os.distro",
				Operator: "only",
				Operand:  []any{"debian", "ubuntu"},
			},
		},
	}

	facts := map[string]any{
		"os": map[string]any{"distro": "centos"},
	}

	vars := make(map[string]any)
	varOrder := []string{}

	vars, varOrder = evaluateConditionalVars(cfg, facts, vars, varOrder)

	val, ok := vars["is_debian"]
	if !ok {
		t.Fatal("expected 'is_debian' in vars map")
	}
	if val != "false" {
		t.Errorf("is_debian = %q, want 'false'", val)
	}
}

func TestEvaluateConditionalVarsMultiple(t *testing.T) {
	cfg := &config.ClientConfig{
		ConditionalVars: []conditionals.ConditionalVar{
			{
				Name:     "is_debian",
				Trait:    "os.distro",
				Operator: "only",
				Operand:  []any{"debian", "ubuntu"},
			},
			{
				Name:     "has_enough_mem",
				Trait:    "hardware.memory_mb",
				Operator: "gte",
				Operand:  float64(4096),
			},
		},
	}

	facts := map[string]any{
		"os":       map[string]any{"distro": "ubuntu"},
		"hardware": map[string]any{"memory_mb": float64(8192)},
	}

	vars := make(map[string]any)
	varOrder := []string{}

	vars, varOrder = evaluateConditionalVars(cfg, facts, vars, varOrder)

	if vars["is_debian"] != "true" {
		t.Errorf("is_debian = %q, want 'true'", vars["is_debian"])
	}
	if vars["has_enough_mem"] != "true" {
		t.Errorf("has_enough_mem = %q, want 'true'", vars["has_enough_mem"])
	}

	// Both should be in varOrder.
	if len(varOrder) != 2 {
		t.Errorf("len(varOrder) = %d, want 2", len(varOrder))
	}
}

func TestConfigToProtoModulesGoIfAllPass(t *testing.T) {
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "nginx",
				Resources: []config.ResourceConfig{
					{
						Name:        "install-nginx",
						Type:        "package",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"name": "nginx"},
					},
				},
			},
		},
	}

	// is_debian=true -> resource included
	vars := map[string]any{"is_debian": "true"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(modules[0].Resources))
	}

	// is_debian=false -> resource excluded, module excluded
	vars = map[string]any{"is_debian": "false"}
	modules, err = configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 0 {
		t.Errorf("expected 0 modules (resource filtered), got %d", len(modules))
	}
}

func TestConfigToProtoModulesSkipIfAnyPass(t *testing.T) {
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "linux-tools",
				Resources: []config.ResourceConfig{
					{
						Name:          "install-tool",
						Type:          "package",
						SkipIfAnyPass: config.StringOrSlice{"is_windows"},
						Attributes:    map[string]any{"name": "htop"},
					},
				},
			},
		},
	}

	// is_windows=true -> resource excluded
	vars := map[string]any{"is_windows": "true"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 0 {
		t.Errorf("expected 0 modules (resource skipped), got %d", len(modules))
	}

	// is_windows=false -> resource included
	vars = map[string]any{"is_windows": "false"}
	modules, err = configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(modules[0].Resources))
	}
}

func TestConfigToProtoModulesEmptyModuleExcluded(t *testing.T) {
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "all-skipped",
				Resources: []config.ResourceConfig{
					{
						Name:        "res-a",
						Type:        "file",
						GoIfAllPass: config.StringOrSlice{"never_true"},
						Attributes:  map[string]any{"content": "a"},
					},
					{
						Name:        "res-b",
						Type:        "file",
						GoIfAllPass: config.StringOrSlice{"never_true"},
						Attributes:  map[string]any{"content": "b"},
					},
				},
			},
			{
				Name: "has-resources",
				Resources: []config.ResourceConfig{
					{
						Name:       "res-c",
						Type:       "file",
						Attributes: map[string]any{"content": "c"},
					},
				},
			},
		},
	}

	vars := map[string]any{"never_true": "false"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	// Only "has-resources" should remain
	if len(modules) != 1 {
		t.Fatalf("expected 1 module (empty excluded), got %d", len(modules))
	}
	if modules[0].Name != "has-resources" {
		t.Errorf("expected module 'has-resources', got %q", modules[0].Name)
	}
}

func TestConfigToProtoModulesNoConditionalsPassThrough(t *testing.T) {
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "basic",
				Resources: []config.ResourceConfig{
					{
						Name:       "basic-res",
						Type:       "file",
						Attributes: map[string]any{"content": "hello"},
					},
				},
			},
		},
	}

	// No conditional fields -> resource always included
	modules, err := configToProtoModules(cfg, nil, map[string]any{})
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Name != "basic-res" {
		t.Errorf("expected resource 'basic-res', got %q", modules[0].Resources[0].Name)
	}
}

// --- Conditional Block Check-In Pipeline Tests ---

func TestConditionalBlockAllResourcesIncludedWhenConditionTrue(t *testing.T) {
	// Simulates a module with resources that came from a conditional_block
	// flattening. Block condition go_if_all_pass: [is_debian] is true.
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "debian-tools",
				Resources: []config.ResourceConfig{
					{
						Name:        "install-apt-utils",
						Type:        "package",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"name": "apt-utils"},
					},
					{
						Name:        "apt-update",
						Type:        "exec",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"command": "apt-get update"},
					},
				},
			},
		},
	}

	vars := map[string]any{"is_debian": "true"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Name != "install-apt-utils" {
		t.Errorf("Resources[0].Name = %q, want install-apt-utils", modules[0].Resources[0].Name)
	}
	if modules[0].Resources[1].Name != "apt-update" {
		t.Errorf("Resources[1].Name = %q, want apt-update", modules[0].Resources[1].Name)
	}
}

func TestConditionalBlockAllResourcesExcludedWhenConditionFalse(t *testing.T) {
	// Block condition go_if_all_pass: [is_debian] is false -- all block
	// resources should be excluded, module should be excluded entirely.
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "debian-tools",
				Resources: []config.ResourceConfig{
					{
						Name:        "install-apt-utils",
						Type:        "package",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"name": "apt-utils"},
					},
					{
						Name:        "apt-update",
						Type:        "exec",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"command": "apt-get update"},
					},
				},
			},
		},
	}

	vars := map[string]any{"is_debian": "false"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 0 {
		t.Errorf("expected 0 modules (block condition false), got %d", len(modules))
	}
}

func TestConditionalBlockResourceLevelSkipOverridesBlockPass(t *testing.T) {
	// Block go_if_all_pass: [is_debian] passes, but resource has
	// skip_if_any_pass: [is_container] which also passes -- resource excluded.
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "debian-desktop",
				Resources: []config.ResourceConfig{
					{
						Name:          "desktop-tool",
						Type:          "package",
						GoIfAllPass:   config.StringOrSlice{"is_debian"},
						SkipIfAnyPass: config.StringOrSlice{"is_container"},
						Attributes:    map[string]any{"name": "desktop-tool"},
					},
					{
						Name:        "apt-update",
						Type:        "exec",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						Attributes:  map[string]any{"command": "apt-get update"},
					},
				},
			},
		},
	}

	// Both is_debian and is_container are true: desktop-tool excluded, apt-update included
	vars := map[string]any{"is_debian": "true", "is_container": "true"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource (desktop-tool skipped), got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Name != "apt-update" {
		t.Errorf("expected apt-update, got %q", modules[0].Resources[0].Name)
	}

	// is_debian=true, is_container=false: both resources included
	vars = map[string]any{"is_debian": "true", "is_container": "false"}
	modules, err = configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(modules[0].Resources))
	}
}

func TestConditionalBlockDependsOnFromNonBlockToBlockResource(t *testing.T) {
	// Non-block resource depends_on a block resource. Both included when
	// conditions pass. Verifies depends_on name references work across
	// block boundaries in the proto response.
	cfg := &config.ClientConfig{
		Modules: []config.ModuleConfig{
			{
				Name: "cross-dep",
				Resources: []config.ResourceConfig{
					{
						Name:       "setup-repo",
						Type:       "exec",
						Attributes: map[string]any{"command": "add-repo"},
					},
					{
						Name:        "install-tool",
						Type:        "package",
						GoIfAllPass: config.StringOrSlice{"is_debian"},
						DependsOn:   []string{"setup-repo"},
						Attributes:  map[string]any{"name": "my-tool"},
					},
				},
			},
		},
	}

	vars := map[string]any{"is_debian": "true"}
	modules, err := configToProtoModules(cfg, nil, vars)
	if err != nil {
		t.Fatalf("configToProtoModules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if len(modules[0].Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Name != "setup-repo" {
		t.Errorf("Resources[0].Name = %q, want setup-repo", modules[0].Resources[0].Name)
	}
	if modules[0].Resources[1].Name != "install-tool" {
		t.Errorf("Resources[1].Name = %q, want install-tool", modules[0].Resources[1].Name)
	}
}

func TestEvaluateConditionalVarsBeforeRenderVars(t *testing.T) {
	// Ensure conditional vars are inserted at the beginning of varOrder
	// so regular vars can reference them via Go templates.
	cfg := &config.ClientConfig{
		ConditionalVars: []conditionals.ConditionalVar{
			{
				Name:     "is_ubuntu",
				Trait:    "os.distro",
				Operator: "only",
				Operand:  []any{"ubuntu"},
			},
		},
	}

	facts := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}

	vars := map[string]any{
		"greeting": "ubuntu={{ .Vars.is_ubuntu }}",
	}
	varOrder := []string{"greeting"}

	vars, varOrder = evaluateConditionalVars(cfg, facts, vars, varOrder)

	// is_ubuntu should appear BEFORE greeting in varOrder so that
	// RenderVars can use it in template rendering.
	isUbuntuIdx := -1
	greetingIdx := -1
	for i, v := range varOrder {
		if v == "is_ubuntu" {
			isUbuntuIdx = i
		}
		if v == "greeting" {
			greetingIdx = i
		}
	}

	if isUbuntuIdx == -1 {
		t.Fatal("is_ubuntu not found in varOrder")
	}
	if greetingIdx == -1 {
		t.Fatal("greeting not found in varOrder")
	}
	if isUbuntuIdx >= greetingIdx {
		t.Errorf("is_ubuntu (idx=%d) should appear before greeting (idx=%d) in varOrder",
			isUbuntuIdx, greetingIdx)
	}

	// Verify the var value is set correctly.
	if vars["is_ubuntu"] != "true" {
		t.Errorf("is_ubuntu = %q, want 'true'", vars["is_ubuntu"])
	}
}
