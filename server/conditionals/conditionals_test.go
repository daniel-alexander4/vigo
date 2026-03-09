package conditionals

import (
	"testing"
)

// --- IsConditionalVar tests ---

func TestIsConditionalVar_WithTraitKey(t *testing.T) {
	v := map[string]any{
		"nail": "os.distro",
		"in":    []any{"debian", "ubuntu"},
	}
	if !IsConditionalVar(v) {
		t.Error("expected map with 'nail' key to be a conditional var")
	}
}

func TestIsConditionalVar_PlainString(t *testing.T) {
	if IsConditionalVar("hello") {
		t.Error("expected plain string to not be a conditional var")
	}
}

func TestIsConditionalVar_MapWithoutTrait(t *testing.T) {
	v := map[string]any{
		"name": "foo",
	}
	if IsConditionalVar(v) {
		t.Error("expected map without 'nail' key to not be a conditional var")
	}
}

func TestIsConditionalVar_Nil(t *testing.T) {
	if IsConditionalVar(nil) {
		t.Error("expected nil to not be a conditional var")
	}
}

// --- ParseConditionalVar tests ---

func TestParseConditionalVar_InOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "os.distro",
		"in":    []any{"debian", "ubuntu"},
	}
	cv, err := ParseConditionalVar("is_debian", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Nail != "os.distro" {
		t.Errorf("expected nail 'os.distro', got %q", cv.Nail)
	}
	if cv.Operator != "in" {
		t.Errorf("expected operator 'in', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_NotInOperator(t *testing.T) {
	raw := map[string]any{
		"nail":  "os.family",
		"not_in": []any{"windows"},
	}
	cv, err := ParseConditionalVar("not_windows", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "not_in" {
		t.Errorf("expected operator 'not_in', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_GtOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	}
	cv, err := ParseConditionalVar("high_mem", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "gt" {
		t.Errorf("expected operator 'gt', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_LtOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "hardware.cpu_count",
		"lt":    100,
	}
	cv, err := ParseConditionalVar("low_cpu", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "lt" {
		t.Errorf("expected operator 'lt', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_GteOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "hardware.memory_mb",
		"gte":   4096,
	}
	cv, err := ParseConditionalVar("gte_mem", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "gte" {
		t.Errorf("expected operator 'gte', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_LteOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "hardware.memory_mb",
		"lte":   8192,
	}
	cv, err := ParseConditionalVar("lte_mem", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "lte" {
		t.Errorf("expected operator 'lte', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_RegexOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "os.distro",
		"regex": "^ubuntu",
	}
	cv, err := ParseConditionalVar("is_ubuntu", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cv.Operator != "regex" {
		t.Errorf("expected operator 'regex', got %q", cv.Operator)
	}
}

func TestParseConditionalVar_InvalidRegex(t *testing.T) {
	raw := map[string]any{
		"nail": "os.distro",
		"regex": "[invalid",
	}
	_, err := ParseConditionalVar("bad_regex", raw)
	if err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
}

func TestParseConditionalVar_MissingTrait(t *testing.T) {
	raw := map[string]any{
		"in": []any{"debian"},
	}
	_, err := ParseConditionalVar("no_nail", raw)
	if err == nil {
		t.Error("expected error for missing nail field, got nil")
	}
}

func TestParseConditionalVar_NoOperator(t *testing.T) {
	raw := map[string]any{
		"nail": "os.distro",
	}
	_, err := ParseConditionalVar("no_op", raw)
	if err == nil {
		t.Error("expected error for missing operator, got nil")
	}
}

func TestParseConditionalVar_MultipleOperators(t *testing.T) {
	raw := map[string]any{
		"nail":  "os.distro",
		"in":     []any{"debian"},
		"not_in": []any{"ubuntu"},
	}
	_, err := ParseConditionalVar("multi_op", raw)
	if err == nil {
		t.Error("expected error for multiple operators, got nil")
	}
}

func TestParseConditionalVar_InvalidOperatorName(t *testing.T) {
	raw := map[string]any{
		"nail":   "os.distro",
		"equals": "debian",
	}
	_, err := ParseConditionalVar("bad_op", raw)
	if err == nil {
		t.Error("expected error for invalid operator name, got nil")
	}
}

// --- Evaluate tests: only operator ---

func TestEvaluate_Only_Match(t *testing.T) {
	cv := mustParse(t, "is_debian", map[string]any{
		"nail": "os.distro",
		"in": []any{"debian", "ubuntu"},
	})
	nails := map[string]any{
		"os": map[string]any{"distro": "debian"},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected in:[debian, ubuntu] to match nail 'debian'")
	}
}

func TestEvaluate_Only_NoMatch(t *testing.T) {
	cv := mustParse(t, "is_debian", map[string]any{
		"nail": "os.distro",
		"in": []any{"debian", "ubuntu"},
	})
	nails := map[string]any{
		"os": map[string]any{"distro": "centos"},
	}
	if Evaluate(cv, nails) {
		t.Error("expected in:[debian, ubuntu] to NOT match nail 'centos'")
	}
}

// --- Evaluate tests: not operator ---

func TestEvaluate_Not_Match(t *testing.T) {
	cv := mustParse(t, "not_windows", map[string]any{
		"nail": "os.family",
		"not_in": []any{"windows"},
	})
	nails := map[string]any{
		"os": map[string]any{"family": "linux"},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected not_in:[windows] to be true when nail is 'linux'")
	}
}

func TestEvaluate_Not_NoMatch(t *testing.T) {
	cv := mustParse(t, "not_windows", map[string]any{
		"nail": "os.family",
		"not_in": []any{"windows"},
	})
	nails := map[string]any{
		"os": map[string]any{"family": "windows"},
	}
	if Evaluate(cv, nails) {
		t.Error("expected not_in:[windows] to be false when nail is 'windows'")
	}
}

// --- Evaluate tests: gt operator ---

func TestEvaluate_Gt_True(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 8192},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected gt:4096 to be true when nail is 8192")
	}
}

func TestEvaluate_Gt_Equal(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 4096},
	}
	if Evaluate(cv, nails) {
		t.Error("expected gt:4096 to be false when nail is 4096 (strict greater than)")
	}
}

func TestEvaluate_Gt_False(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 2048},
	}
	if Evaluate(cv, nails) {
		t.Error("expected gt:4096 to be false when nail is 2048")
	}
}

// --- Evaluate tests: lt operator ---

func TestEvaluate_Lt_True(t *testing.T) {
	cv := mustParse(t, "low_cpu", map[string]any{
		"nail": "hardware.cpu_count",
		"lt":    100,
	})
	nails := map[string]any{
		"hardware": map[string]any{"cpu_count": 4},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected lt:100 to be true when nail is 4")
	}
}

func TestEvaluate_Lt_False(t *testing.T) {
	cv := mustParse(t, "low_cpu", map[string]any{
		"nail": "hardware.cpu_count",
		"lt":    100,
	})
	nails := map[string]any{
		"hardware": map[string]any{"cpu_count": 200},
	}
	if Evaluate(cv, nails) {
		t.Error("expected lt:100 to be false when nail is 200")
	}
}

// --- Evaluate tests: gte operator ---

func TestEvaluate_Gte_GreaterThan(t *testing.T) {
	cv := mustParse(t, "gte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gte":   4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 8192},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected gte:4096 to be true when nail is 8192")
	}
}

func TestEvaluate_Gte_Equal(t *testing.T) {
	cv := mustParse(t, "gte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gte":   4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 4096},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected gte:4096 to be true when nail is 4096")
	}
}

func TestEvaluate_Gte_LessThan(t *testing.T) {
	cv := mustParse(t, "gte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gte":   4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 2048},
	}
	if Evaluate(cv, nails) {
		t.Error("expected gte:4096 to be false when nail is 2048")
	}
}

// --- Evaluate tests: lte operator ---

func TestEvaluate_Lte_LessThan(t *testing.T) {
	cv := mustParse(t, "lte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"lte":   8192,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 4096},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected lte:8192 to be true when nail is 4096")
	}
}

func TestEvaluate_Lte_Equal(t *testing.T) {
	cv := mustParse(t, "lte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"lte":   8192,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 8192},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected lte:8192 to be true when nail is 8192")
	}
}

func TestEvaluate_Lte_GreaterThan(t *testing.T) {
	cv := mustParse(t, "lte_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"lte":   8192,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": 16384},
	}
	if Evaluate(cv, nails) {
		t.Error("expected lte:8192 to be false when nail is 16384")
	}
}

// --- Evaluate tests: match operator ---

func TestEvaluate_Match_True(t *testing.T) {
	cv := mustParse(t, "is_ubuntu", map[string]any{
		"nail": "os.distro",
		"regex": "^ubuntu",
	})
	nails := map[string]any{
		"os": map[string]any{"distro": "ubuntu-22.04"},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected regex:'^ubuntu' to be true for 'ubuntu-22.04'")
	}
}

func TestEvaluate_Match_False(t *testing.T) {
	cv := mustParse(t, "is_ubuntu", map[string]any{
		"nail": "os.distro",
		"regex": "^ubuntu",
	})
	nails := map[string]any{
		"os": map[string]any{"distro": "debian-12"},
	}
	if Evaluate(cv, nails) {
		t.Error("expected regex:'^ubuntu' to be false for 'debian-12'")
	}
}

// --- Evaluate edge cases ---

func TestEvaluate_MissingTrait_In(t *testing.T) {
	cv := mustParse(t, "is_debian", map[string]any{
		"nail": "os.distro",
		"in":    []any{"debian"},
	})
	nails := map[string]any{}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is missing (in operator)")
	}
}

func TestEvaluate_MissingTrait_NotIn(t *testing.T) {
	cv := mustParse(t, "not_windows", map[string]any{
		"nail":  "os.family",
		"not_in": []any{"windows"},
	})
	nails := map[string]any{}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is missing (not_in operator)")
	}
}

func TestEvaluate_MissingTrait_Gt(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is missing (gt operator)")
	}
}

func TestEvaluate_MissingTrait_Regex(t *testing.T) {
	cv := mustParse(t, "is_ubuntu", map[string]any{
		"nail": "os.distro",
		"regex": "^ubuntu",
	})
	nails := map[string]any{}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is missing (regex operator)")
	}
}

func TestEvaluate_NonNumericTrait_Gt(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": "not-a-number"},
	}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is non-numeric with gt operator")
	}
}

func TestEvaluate_NonNumericTrait_Lt(t *testing.T) {
	cv := mustParse(t, "low_cpu", map[string]any{
		"nail": "hardware.cpu_count",
		"lt":    100,
	})
	nails := map[string]any{
		"hardware": map[string]any{"cpu_count": "not-a-number"},
	}
	if Evaluate(cv, nails) {
		t.Error("expected false when nail is non-numeric with lt operator")
	}
}

func TestEvaluate_FloatTrait_Gt(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": float64(8192)},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected gt:4096 to be true when nail is float64(8192)")
	}
}

func TestEvaluate_StringNumericTrait_Gt(t *testing.T) {
	cv := mustParse(t, "high_mem", map[string]any{
		"nail": "hardware.memory_mb",
		"gt":    4096,
	})
	nails := map[string]any{
		"hardware": map[string]any{"memory_mb": "8192"},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected gt:4096 to be true when nail is string '8192'")
	}
}

// --- Dot-path resolution tests ---

func TestEvaluate_DotPath_Nested(t *testing.T) {
	cv := mustParse(t, "deep", map[string]any{
		"nail": "a.b.c",
		"in": []any{"found"},
	})
	nails := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "found",
			},
		},
	}
	if !Evaluate(cv, nails) {
		t.Error("expected dot-path 'a.b.c' to resolve to 'found'")
	}
}

func TestEvaluate_DotPath_MissingIntermediate(t *testing.T) {
	cv := mustParse(t, "deep", map[string]any{
		"nail": "a.b.c",
		"in": []any{"found"},
	})
	nails := map[string]any{
		"a": map[string]any{
			"x": "other",
		},
	}
	if Evaluate(cv, nails) {
		t.Error("expected false when intermediate path segment is missing")
	}
}

func TestEvaluate_DotPath_TopLevel(t *testing.T) {
	cv := mustParse(t, "hostname_check", map[string]any{
		"nail": "hostname",
		"in": []any{"web01"},
	})
	nails := map[string]any{
		"hostname": "web01",
	}
	if !Evaluate(cv, nails) {
		t.Error("expected top-level nail 'hostname' to resolve")
	}
}

func TestEvaluate_Only_NumericTraitAsString(t *testing.T) {
	cv := mustParse(t, "check", map[string]any{
		"nail": "count",
		"in": []any{"5"},
	})
	nails := map[string]any{
		"count": 5,
	}
	// only uses string comparison: fmt.Sprintf of 5 is "5"
	if !Evaluate(cv, nails) {
		t.Error("expected only:['5'] to match nail integer 5 via string coercion")
	}
}

// --- Helper ---

func mustParse(t *testing.T, name string, raw map[string]any) ConditionalVar {
	t.Helper()
	cv, err := ParseConditionalVar(name, raw)
	if err != nil {
		t.Fatalf("mustParse failed: %v", err)
	}
	return cv
}
