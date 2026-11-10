// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
// EvalConfigcrateWhenServerSide resolves) and treating var refs as false.
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

// --- EvalConfigcrateWhenServerSide canonical-parser agreement (B20) ---

// evalCanonical fully evaluates a when: expression through the canonical
// server/conditionals AST parser, resolving the same server-side builtins
// EvalConfigcrateWhenServerSide resolves. It is the oracle these tests check the
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

func TestEvalConfigcrateWhenServerSide_AgreesWithCanonicalParser(t *testing.T) {
	traits := map[string]any{
		"os.family": "debian",
		"os.distro": "ubuntu",
		"os.arch":   "amd64",
	}

	// All builtins are server-resolvable, so EvalConfigcrateWhenServerSide must
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
			include, agentWhen := EvalConfigcrateWhenServerSide(when, traits)
			if agentWhen != "" {
				t.Fatalf("expected fully server-resolved (empty agentWhen), got residual %q", agentWhen)
			}
			want := evalCanonical(t, when, traits)
			if include != want {
				t.Errorf("EvalConfigcrateWhenServerSide(%q) = %v, canonical parser = %v", when, include, want)
			}
		})
	}
}

// TestEvalConfigcrateWhenServerSide_DefersAgentBuiltins pins the preserved
// deferral behavior: an expression with an agent-only builtin that can't be
// short-circuited server-side must be passed through unchanged (include=true,
// non-empty residual) for the agent to finish.
func TestEvalConfigcrateWhenServerSide_DefersAgentBuiltins(t *testing.T) {
	traits := map[string]any{"os.family": "debian"}

	// Pure agent-only builtin: deferred.
	include, agentWhen := EvalConfigcrateWhenServerSide("has_command('docker')", traits)
	if !include || agentWhen == "" {
		t.Errorf("agent-only builtin should defer: include=%v agentWhen=%q", include, agentWhen)
	}

	// AND short-circuit: a server-false part makes the whole expression
	// false without deferring.
	include, agentWhen = EvalConfigcrateWhenServerSide("os_family('rhel') and has_command('docker')", traits)
	if include || agentWhen != "" {
		t.Errorf("server-false AND part should drop without deferral: include=%v agentWhen=%q", include, agentWhen)
	}

	// OR short-circuit: a server-true part makes the whole expression true
	// without deferring.
	include, agentWhen = EvalConfigcrateWhenServerSide("os_family('debian') or has_command('docker')", traits)
	if !include || agentWhen != "" {
		t.Errorf("server-true OR part should include without deferral: include=%v agentWhen=%q", include, agentWhen)
	}

	// Mixed AND where the server part is true: the agent-only remainder is
	// deferred (include=true, residual contains the agent builtin).
	include, agentWhen = EvalConfigcrateWhenServerSide("os_family('debian') and has_command('docker')", traits)
	if !include || agentWhen == "" {
		t.Errorf("server-true AND agent-builtin should defer remainder: include=%v agentWhen=%q", include, agentWhen)
	}
}
