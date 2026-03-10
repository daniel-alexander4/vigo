package main

import (
	"testing"

	"github.com/hobnob/hobnob/agent/traits"
)

// --- traverseDotPath tests ---

func TestTraversDotPath_NestedKey(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro":  "ubuntu",
			"version": "22.04",
		},
	}

	val, ok := traverseDotPath(data, "os.distro")
	if !ok {
		t.Fatal("expected ok=true for os.distro")
	}
	if val != "ubuntu" {
		t.Errorf("got %v, want ubuntu", val)
	}
}

func TestTraversDotPath_TopLevelKey(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
			"arch":   "amd64",
		},
	}

	val, ok := traverseDotPath(data, "os")
	if !ok {
		t.Fatal("expected ok=true for os")
	}
	sub, isMap := val.(map[string]any)
	if !isMap {
		t.Fatalf("expected map, got %T", val)
	}
	if sub["distro"] != "ubuntu" {
		t.Errorf("got %v, want ubuntu", sub["distro"])
	}
}

func TestTraversDotPath_NonexistentPath(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}

	val, ok := traverseDotPath(data, "nonexistent.path")
	if ok {
		t.Errorf("expected ok=false, got val=%v", val)
	}
}

func TestTraversDotPath_EmptyPath(t *testing.T) {
	data := map[string]any{"os": map[string]any{"distro": "ubuntu"}}

	val, ok := traverseDotPath(data, "")
	if ok {
		t.Errorf("expected ok=false for empty path, got val=%v", val)
	}
}

func TestTraversDotPath_DeepNested(t *testing.T) {
	data := map[string]any{
		"hardware": map[string]any{
			"disk_gb": map[string]any{
				"/": uint64(100),
			},
		},
	}

	val, ok := traverseDotPath(data, "hardware.disk_gb./")
	if !ok {
		t.Fatal("expected ok=true for hardware.disk_gb./")
	}
	if val != uint64(100) {
		t.Errorf("got %v, want 100", val)
	}
}

// --- filterCollectors tests ---

func TestFilterCollectors_SingleMatch(t *testing.T) {
	all := traits.DefaultCollectors()

	filtered, err := filterCollectors(all, []string{"os"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 collector, got %d", len(filtered))
	}
	if filtered[0].Name() != "os" {
		t.Errorf("expected os collector, got %s", filtered[0].Name())
	}
}

func TestFilterCollectors_MultipleMatch(t *testing.T) {
	all := traits.DefaultCollectors()

	filtered, err := filterCollectors(all, []string{"os", "hardware"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 collectors, got %d", len(filtered))
	}

	names := map[string]bool{}
	for _, c := range filtered {
		names[c.Name()] = true
	}
	if !names["os"] || !names["hardware"] {
		t.Errorf("expected os and hardware, got %v", names)
	}
}

func TestFilterCollectors_Nonexistent(t *testing.T) {
	all := traits.DefaultCollectors()

	_, err := filterCollectors(all, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown collector name")
	}
}

func TestFilterCollectors_MixedValidInvalid(t *testing.T) {
	all := traits.DefaultCollectors()

	_, err := filterCollectors(all, []string{"os", "nonexistent"})
	if err == nil {
		t.Fatal("expected error when any collector name is unknown")
	}
}

// --- formatValue tests ---

func TestFormatValue_String(t *testing.T) {
	result := formatValue("hello")
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestFormatValue_Int(t *testing.T) {
	result := formatValue(42)
	if result != "42" {
		t.Errorf("got %q, want %q", result, "42")
	}
}

func TestFormatValue_Bool(t *testing.T) {
	result := formatValue(true)
	if result != "true" {
		t.Errorf("got %q, want %q", result, "true")
	}
}

func TestFormatValue_Map(t *testing.T) {
	m := map[string]any{"key": "val"}
	result := formatValue(m)
	// Should be valid pretty JSON
	if result == "" {
		t.Fatal("expected non-empty output for map")
	}
	if result[0] != '{' {
		t.Errorf("expected JSON object, got %q", result)
	}
}

func TestFormatValue_Slice(t *testing.T) {
	s := []string{"a", "b"}
	result := formatValue(s)
	if result == "" {
		t.Fatal("expected non-empty output for slice")
	}
	if result[0] != '[' {
		t.Errorf("expected JSON array, got %q", result)
	}
}
