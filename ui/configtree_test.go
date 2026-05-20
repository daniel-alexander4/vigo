// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyConfigFile(t *testing.T) {
	cases := []struct {
		name    string
		relPath string
		want    string
	}{
		{"common.vgo", "", "common"},
		{"common.vgo", "alexander4/us-west1", "common"},
		{"environments.vgo", "", "environments"},
		{"roles.vgo", "scaffolding", "roles"},
		{"waivers.vgo", "alexander4/us-west1", "waivers"},
		{"envoys.vgo", "alexander4", "matchfile"},
		{"docker.vgo", "stockpile/modules/system", "module"},
		{"docker-windows.vgo", "stockpile/modules/system", "module"},
		{"alice.vgo", "stockpile/usercrates", "usercrate"},
		{"some-task.vgo", "stockpile/tasks", "vgo"},
		{"some-workflow.vgo", "stockpile/workflows", "vgo"},
		{"hosts.j2", "stockpile/templates", "other"},
		{"README.md", "", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.relPath+"/"+tc.name, func(t *testing.T) {
			if got := classifyConfigFile(tc.name, tc.relPath); got != tc.want {
				t.Errorf("classifyConfigFile(%q, %q) = %q, want %q", tc.name, tc.relPath, got, tc.want)
			}
		})
	}
}

func TestBuildConfigTree(t *testing.T) {
	root := t.TempDir()
	mkdirs := []string{
		"alexander4/us-west1/us-west1-a",
		"alexander4/us-east1/us-east1-a",
		"stockpile/modules/system",
		"stockpile/usercrates",
	}
	for _, d := range mkdirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		"common.vgo",
		"environments.vgo",
		"alexander4/common.vgo",
		"alexander4/us-west1/common.vgo",
		"alexander4/us-west1/us-west1-a/danlap.vgo",
		"alexander4/us-west1/waivers.vgo",
		"alexander4/us-east1/us-east1-a/plex.vgo",
		"stockpile/modules/system/docker.vgo",
		"stockpile/usercrates/alice.vgo",
		".hidden.vgo", // dotfile — must be skipped
	}
	for _, f := range files {
		path := filepath.Join(root, f)
		if err := os.WriteFile(path, []byte("# fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := buildConfigTree(root)
	if err != nil {
		t.Fatalf("buildConfigTree: %v", err)
	}
	if tree.Name != ".live" || !tree.IsDir {
		t.Errorf("root: got %+v", tree)
	}

	// Top-level: directories before files; .hidden.vgo skipped.
	got := childNames(tree)
	want := []string{"alexander4/", "stockpile/", "common.vgo", "environments.vgo"}
	if !equalSlice(got, want) {
		t.Errorf("top-level children:\n  got  %v\n  want %v", got, want)
	}

	// Spot-check classification.
	if k := find(tree, "common.vgo").Kind; k != "common" {
		t.Errorf("common.vgo kind = %q, want common", k)
	}
	docker := findPath(tree, "stockpile", "modules", "system", "docker.vgo")
	if docker == nil || docker.Kind != "module" {
		t.Errorf("docker.vgo: got %+v, want kind=module", docker)
	}
	alice := findPath(tree, "stockpile", "usercrates", "alice.vgo")
	if alice == nil || alice.Kind != "usercrate" {
		t.Errorf("alice.vgo: got %+v, want kind=usercrate", alice)
	}
	waivers := findPath(tree, "alexander4", "us-west1", "waivers.vgo")
	if waivers == nil || waivers.Kind != "waivers" {
		t.Errorf("waivers.vgo: got %+v, want kind=waivers", waivers)
	}
}

func TestBuildConfigTree_MissingDir(t *testing.T) {
	if _, err := buildConfigTree(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

func childNames(n *configTreeNode) []string {
	out := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		if c.IsDir {
			out = append(out, c.Name+"/")
		} else {
			out = append(out, c.Name)
		}
	}
	return out
}

func find(n *configTreeNode, name string) *configTreeNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func findPath(n *configTreeNode, parts ...string) *configTreeNode {
	cur := n
	for _, p := range parts {
		cur = find(cur, p)
		if cur == nil {
			return nil
		}
	}
	return cur
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
