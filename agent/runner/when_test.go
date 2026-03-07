package runner

import (
	"testing"
)

func TestEvaluateWhenEmpty(t *testing.T) {
	result, err := EvaluateWhen("", nil)
	if err != nil {
		t.Fatalf("EvaluateWhen empty: %v", err)
	}
	if !result {
		t.Error("empty expression should return true (always run)")
	}
}

func TestEvaluateWhenMatchingCondition(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
		},
	}

	result, err := EvaluateWhen(`{{ eq .Traits.os.family "linux" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen: %v", err)
	}
	if !result {
		t.Error("expected true for matching condition")
	}
}

func TestEvaluateWhenNonMatchingCondition(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "darwin",
		},
	}

	result, err := EvaluateWhen(`{{ eq .Traits.os.family "linux" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen: %v", err)
	}
	if result {
		t.Error("expected false for non-matching condition")
	}
}

func TestEvaluateWhenMissingFactKey(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
		},
	}

	// Missing key should render as zero value, not "true"
	result, err := EvaluateWhen(`{{ eq .Traits.os.nonexistent "linux" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen with missing key: %v", err)
	}
	if result {
		t.Error("expected false for missing fact key")
	}
}

func TestEvaluateWhenMissingTopLevelFact(t *testing.T) {
	facts := map[string]any{}

	// Completely missing top-level fact
	result, err := EvaluateWhen(`{{ eq .Traits.os "something" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen with missing top-level: %v", err)
	}
	if result {
		t.Error("expected false for missing top-level fact")
	}
}

func TestEvaluateWhenNestedFacts(t *testing.T) {
	facts := map[string]any{
		"hardware": map[string]any{
			"cpu_count": 4,
		},
		"os": map[string]any{
			"family":  "linux",
			"distro":  "ubuntu",
			"version": "22.04",
		},
	}

	result, err := EvaluateWhen(`{{ eq .Traits.os.distro "ubuntu" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen nested: %v", err)
	}
	if !result {
		t.Error("expected true for nested fact match")
	}
}

func TestEvaluateWhenInvalidTemplate(t *testing.T) {
	_, err := EvaluateWhen(`{{ invalid syntax }}`, nil)
	if err == nil {
		t.Error("expected error for invalid template syntax")
	}
}

func TestEvaluateWhenNotEqual(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
		},
	}

	result, err := EvaluateWhen(`{{ ne .Traits.os.family "darwin" }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen ne: %v", err)
	}
	if !result {
		t.Error("expected true for ne with different values")
	}
}

func TestEvaluateWhenWithNilFacts(t *testing.T) {
	result, err := EvaluateWhen("", nil)
	if err != nil {
		t.Fatalf("EvaluateWhen nil facts: %v", err)
	}
	if !result {
		t.Error("empty expression with nil facts should return true")
	}
}

func TestEvaluateWhenAndCondition(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
			"distro": "ubuntu",
		},
	}

	result, err := EvaluateWhen(`{{ and (eq .Traits.os.family "linux") (eq .Traits.os.distro "ubuntu") }}`, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen and: %v", err)
	}
	if !result {
		t.Error("expected true for and condition with both true")
	}
}

func TestEvaluateWhenWhitespaceResult(t *testing.T) {
	// Template with whitespace around the result should still work
	facts := map[string]any{
		"os": map[string]any{
			"family": "linux",
		},
	}

	result, err := EvaluateWhen(`  {{ eq .Traits.os.family "linux" }}  `, facts)
	if err != nil {
		t.Fatalf("EvaluateWhen whitespace: %v", err)
	}
	if !result {
		t.Error("expected true even with whitespace around result")
	}
}

func TestEvaluateWhenFalseString(t *testing.T) {
	// Explicit "false" should be false
	result, err := EvaluateWhen(`{{ eq 1 2 }}`, nil)
	if err != nil {
		t.Fatalf("EvaluateWhen false: %v", err)
	}
	if result {
		t.Error("expected false for eq 1 2")
	}
}
