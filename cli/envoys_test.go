// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
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
	result := formatTraitValue("ubuntu")
	if result != "ubuntu" {
		t.Errorf("expected ubuntu, got %q", result)
	}
}

func TestTraitsFormatValue_Number(t *testing.T) {
	result := formatTraitValue(float64(42))
	if result != "42" {
		t.Errorf("expected 42, got %q", result)
	}
}

func TestTraitsFormatValue_Bool(t *testing.T) {
	result := formatTraitValue(true)
	if result != "true" {
		t.Errorf("expected true, got %q", result)
	}
}

func TestTraitsFormatValue_Map(t *testing.T) {
	m := map[string]any{"distro": "ubuntu", "version": "22.04"}
	result := formatTraitValue(m)

	// Summary format: "key=value, key=value". Map iteration order isn't
	// stable, so assert presence of each k=v pair independently rather than
	// a fixed ordering.
	for _, want := range []string{"distro=ubuntu", "version=22.04"} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in summary, got %q", want, result)
		}
	}
	if !strings.Contains(result, ", ") {
		t.Errorf("expected %q separator in summary, got %q", ", ", result)
	}
}
