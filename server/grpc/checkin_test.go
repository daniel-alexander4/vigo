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
