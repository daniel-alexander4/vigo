// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"testing"

	"git.alexander4.io/vigo/server/conditionals"
	"gopkg.in/yaml.v3"
)

// conditionalsParse parses a when expression via the canonical parser.
func conditionalsParse(when string) (conditionals.WhenExpr, error) {
	return conditionals.ParseWhenExpr(when)
}

// conditionalsEvalFuncs fully evaluates an expression via the canonical
// parser, resolving the server-side builtins against traits (mirroring what
// EvalModuleWhenServerSide resolves) and treating var refs as false.
func conditionalsEvalFuncs(expr conditionals.WhenExpr, traits map[string]any) (bool, error) {
	return conditionals.EvalWhenExprWithFuncs(expr,
		func(string) bool { return false },
		func(name string, args []string) (bool, error) {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			return evalTraitFunc(name, arg, traits), nil
		},
	)
}

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
	mod := Configcrate{Name: "basic"}
	if !ShouldIncludeModule(mod, map[string]any{}) {
		t.Error("expected true when no when expression set")
	}
}

func TestShouldIncludeModule_WhenFalse(t *testing.T) {
	mod := Configcrate{Name: "nginx", When: "is_debian"}
	vars := map[string]any{"is_debian": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when when var is false")
	}
}

func TestShouldIncludeModule_WhenTrue(t *testing.T) {
	mod := Configcrate{Name: "nginx", When: "is_debian"}
	vars := map[string]any{"is_debian": "true"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when when var is true")
	}
}

func TestShouldIncludeModule_WhenNotTrue(t *testing.T) {
	mod := Configcrate{Name: "desktop-tools", When: "not is_container"}
	vars := map[string]any{"is_container": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when not condition triggers")
	}
}

func TestShouldIncludeModule_WhenNotFalse(t *testing.T) {
	mod := Configcrate{Name: "desktop-tools", When: "not is_container"}
	vars := map[string]any{"is_container": "false"}
	if !ShouldIncludeModule(mod, vars) {
		t.Error("expected true when not condition doesn't trigger")
	}
}

func TestShouldIncludeModule_WhenOrNoneTrue(t *testing.T) {
	mod := Configcrate{Name: "tool", When: "is_debian or is_ubuntu"}
	vars := map[string]any{"is_debian": "false", "is_ubuntu": "false"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when no OR vars are true")
	}
}

func TestShouldIncludeModule_WhenAndNotAll(t *testing.T) {
	mod := Configcrate{Name: "tool", When: "not (is_legacy and is_deprecated)"}
	vars := map[string]any{"is_legacy": "true", "is_deprecated": "true"}
	if ShouldIncludeModule(mod, vars) {
		t.Error("expected false when all conditions in NOT(AND) are true")
	}
}

// --- ConfigcrateRef YAML unmarshal tests ---

func TestConfigcrateRef_PlainString(t *testing.T) {
	yamlData := `configcrates: [nginx]`
	var out struct {
		Configcrates []ConfigcrateRef `yaml:"configcrates"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(out.Configcrates))
	}
	if out.Configcrates[0].Name != "nginx" {
		t.Errorf("Name = %q, want nginx", out.Configcrates[0].Name)
	}
	if out.Configcrates[0].When != "" {
		t.Errorf("When = %q, want empty", out.Configcrates[0].When)
	}
}

func TestConfigcrateRef_ObjectWithWhen(t *testing.T) {
	yamlData := `
configcrates:
  - name: nginx
    when: "is_debian"
`
	var out struct {
		Configcrates []ConfigcrateRef `yaml:"configcrates"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(out.Configcrates))
	}
	if out.Configcrates[0].Name != "nginx" {
		t.Errorf("Name = %q, want nginx", out.Configcrates[0].Name)
	}
	if out.Configcrates[0].When != "is_debian" {
		t.Errorf("When = %q, want is_debian", out.Configcrates[0].When)
	}
}

func TestConfigcrateRef_ObjectWithWhenExpression(t *testing.T) {
	yamlData := `
configcrates:
  - name: nginx
    when: "not is_container and not is_vm"
`
	var out struct {
		Configcrates []ConfigcrateRef `yaml:"configcrates"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Configcrates) != 1 {
		t.Fatalf("len(Configcrates) = %d, want 1", len(out.Configcrates))
	}
	if out.Configcrates[0].When != "not is_container and not is_vm" {
		t.Errorf("When = %q, want 'not is_container and not is_vm'", out.Configcrates[0].When)
	}
}

func TestConfigcrateRef_MixedPlainAndObject(t *testing.T) {
	yamlData := `
configcrates:
  - nginx
  - name: redis
    when: "is_debian"
`
	var out struct {
		Configcrates []ConfigcrateRef `yaml:"configcrates"`
	}
	if err := yaml.Unmarshal([]byte(yamlData), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Configcrates) != 2 {
		t.Fatalf("len(Configcrates) = %d, want 2", len(out.Configcrates))
	}
	if out.Configcrates[0].Name != "nginx" {
		t.Errorf("Configcrates[0].Name = %q, want nginx", out.Configcrates[0].Name)
	}
	if out.Configcrates[0].When != "" {
		t.Errorf("Configcrates[0].When = %q, want empty", out.Configcrates[0].When)
	}
	if out.Configcrates[1].Name != "redis" {
		t.Errorf("Configcrates[1].Name = %q, want redis", out.Configcrates[1].Name)
	}
	if out.Configcrates[1].When != "is_debian" {
		t.Errorf("Configcrates[1].When = %q, want is_debian", out.Configcrates[1].When)
	}
}

// --- EvalModuleWhenServerSide canonical-parser agreement (B20) ---

// evalCanonical fully evaluates a when: expression through the canonical
// server/conditionals AST parser, resolving the same server-side builtins
// EvalModuleWhenServerSide resolves. It is the oracle these tests check the
// server-side filter against — proving the filter now shares the canonical
// parser's precedence/parenthesization semantics instead of the old
// string-split evaluator's.
func evalCanonical(t *testing.T, when string, traits map[string]any) bool {
	t.Helper()
	expr, err := conditionalsParse(when)
	if err != nil {
		t.Fatalf("canonical parse of %q failed: %v", when, err)
	}
	result, err := conditionalsEvalFuncs(expr, traits)
	if err != nil {
		t.Fatalf("canonical eval of %q failed: %v", when, err)
	}
	return result
}

func TestEvalModuleWhenServerSide_AgreesWithCanonicalParser(t *testing.T) {
	traits := map[string]any{
		"os.family": "debian",
		"os.distro": "ubuntu",
		"os.arch":   "amd64",
	}

	// All builtins are server-resolvable, so EvalModuleWhenServerSide must
	// return a definite (include, "") and that include must match the
	// canonical parser's full evaluation. Precedence and parens are the
	// cases the old string-split evaluator got wrong.
	cases := []string{
		"os_family('debian')",
		"os_family('rhel')",
		"os_family('debian') and arch('amd64')",
		"os_family('rhel') or arch('amd64')",
		// Precedence: `and` binds tighter than `or`, so this is
		// (rhel and amd64) or ubuntu == false or true == true.
		"os_family('rhel') and arch('amd64') or distro('ubuntu')",
		// Same operands, parenthesized to force the other grouping:
		// rhel and (amd64 or ubuntu) == false and true == false.
		"os_family('rhel') and (arch('amd64') or distro('ubuntu'))",
		"not os_family('rhel')",
		"not (os_family('debian') and arch('arm64'))",
		"(os_family('debian') or os_family('rhel')) and arch('amd64')",
	}

	for _, when := range cases {
		t.Run(when, func(t *testing.T) {
			include, agentWhen := EvalModuleWhenServerSide(when, traits)
			if agentWhen != "" {
				t.Fatalf("expected fully server-resolved (empty agentWhen), got residual %q", agentWhen)
			}
			want := evalCanonical(t, when, traits)
			if include != want {
				t.Errorf("EvalModuleWhenServerSide(%q) = %v, canonical parser = %v", when, include, want)
			}
		})
	}
}

// TestEvalModuleWhenServerSide_DefersAgentBuiltins pins the preserved
// deferral behavior: an expression with an agent-only builtin that can't be
// short-circuited server-side must be passed through unchanged (include=true,
// non-empty residual) for the agent to finish.
func TestEvalModuleWhenServerSide_DefersAgentBuiltins(t *testing.T) {
	traits := map[string]any{"os.family": "debian"}

	// Pure agent-only builtin: deferred.
	include, agentWhen := EvalModuleWhenServerSide("has_command('docker')", traits)
	if !include || agentWhen == "" {
		t.Errorf("agent-only builtin should defer: include=%v agentWhen=%q", include, agentWhen)
	}

	// AND short-circuit: a server-false part makes the whole expression
	// false without deferring.
	include, agentWhen = EvalModuleWhenServerSide("os_family('rhel') and has_command('docker')", traits)
	if include || agentWhen != "" {
		t.Errorf("server-false AND part should drop without deferral: include=%v agentWhen=%q", include, agentWhen)
	}

	// OR short-circuit: a server-true part makes the whole expression true
	// without deferring.
	include, agentWhen = EvalModuleWhenServerSide("os_family('debian') or has_command('docker')", traits)
	if !include || agentWhen != "" {
		t.Errorf("server-true OR part should include without deferral: include=%v agentWhen=%q", include, agentWhen)
	}

	// Mixed AND where the server part is true: the agent-only remainder is
	// deferred (include=true, residual contains the agent builtin).
	include, agentWhen = EvalModuleWhenServerSide("os_family('debian') and has_command('docker')", traits)
	if !include || agentWhen == "" {
		t.Errorf("server-true AND agent-builtin should defer remainder: include=%v agentWhen=%q", include, agentWhen)
	}
}
