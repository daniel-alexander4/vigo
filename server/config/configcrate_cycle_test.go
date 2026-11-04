// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigcrateDependencyCycleRejected: configcrate-level depends_on /
// before edges that form a cycle are a load error naming the whole cycle.
func TestConfigcrateDependencyCycleRejected(t *testing.T) {
	dir := setupDir(t)
	// a depends_on b, b depends_on c, and a before: [c] (== c depends_on a)
	// closes a → b → c → a.
	writeFile(t, filepath.Join(dir, "configcrates", "a.vgo"), "name: a\ndepends_on: [b]\nbefore: [c]\nresources: []\n")
	writeFile(t, filepath.Join(dir, "configcrates", "b.vgo"), "name: b\ndepends_on: [c]\nresources: []\n")
	writeFile(t, filepath.Join(dir, "configcrates", "c.vgo"), "name: c\nresources: []\n")
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), "envoys:\n  - match: \"web1\"\n    configcrates: [a, b, c]\n")

	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("NewConfigStore err = %v, want a configcrate dependency cycle error", err)
	}
	if !strings.Contains(err.Error(), "a → b → c → a") {
		t.Errorf("cycle error should show the whole cycle a → b → c → a: %v", err)
	}
}

// TestConfigcrateDependencyAcyclicLoads: the same crates without the
// closing edge load fine.
func TestConfigcrateDependencyAcyclicLoads(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "a.vgo"), "name: a\ndepends_on: [b]\nresources: []\n")
	writeFile(t, filepath.Join(dir, "configcrates", "b.vgo"), "name: b\nresources: []\n")
	writeFile(t, filepath.Join(dir, "configcrates", "c.vgo"), "name: c\nbefore: [a]\nresources: []\n")
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), "envoys:\n  - match: \"web1\"\n    configcrates: [a, b, c]\n")
	if _, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true); err != nil {
		t.Fatalf("acyclic crates rejected: %v", err)
	}
}
