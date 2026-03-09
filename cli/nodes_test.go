package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTraitsTraverseDotPath_NestedValue(t *testing.T) {
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
		t.Errorf("expected ubuntu, got %v", val)
	}
}

func TestTraitsTraverseDotPath_TopLevelMap(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
		},
	}

	val, ok := traverseDotPath(data, "os")
	if !ok {
		t.Fatal("expected ok=true for os")
	}
	m, isMap := val.(map[string]any)
	if !isMap {
		t.Fatalf("expected map, got %T", val)
	}
	if m["distro"] != "ubuntu" {
		t.Errorf("expected distro=ubuntu, got %v", m["distro"])
	}
}

func TestTraitsTraverseDotPath_BadPath(t *testing.T) {
	data := map[string]any{
		"os": map[string]any{
			"distro": "ubuntu",
		},
	}

	_, ok := traverseDotPath(data, "bad.path")
	if ok {
		t.Error("expected ok=false for bad.path")
	}
}

func TestTraitsFormatValue_String(t *testing.T) {
	result := formatNailValue("ubuntu")
	if result != "ubuntu" {
		t.Errorf("expected ubuntu, got %q", result)
	}
}

func TestTraitsFormatValue_Number(t *testing.T) {
	result := formatNailValue(float64(42))
	if result != "42" {
		t.Errorf("expected 42, got %q", result)
	}
}

func TestTraitsFormatValue_Bool(t *testing.T) {
	result := formatNailValue(true)
	if result != "true" {
		t.Errorf("expected true, got %q", result)
	}
}

func TestTraitsFormatValue_Map(t *testing.T) {
	m := map[string]any{"distro": "ubuntu", "version": "22.04"}
	result := formatNailValue(m)

	// Should be valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got error: %v", err)
	}

	// Should be pretty-printed (indented)
	if !strings.Contains(result, "\n") {
		t.Error("expected pretty-printed JSON with newlines")
	}
}
