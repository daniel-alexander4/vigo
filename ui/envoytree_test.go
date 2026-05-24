// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildEnvoyTree owns the join of the .live/ filesystem walk and the
// envoy-by-dirpath grouping (ADR-032 § "Tree definition"). These tests
// exercise it on a synthetic .live/ layout — the production handler
// glues this to ConfigStore.MatchDirPaths + EnvoysData and has the same
// behavior under any production walk.

func TestBuildEnvoyTree_GroupsEnvoysUnderMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "dc1"))
	mustMkdir(t, filepath.Join(root, "dc1", "web"))
	mustMkdir(t, filepath.Join(root, "dc1", "db"))
	mustMkdir(t, filepath.Join(root, "dc2"))

	envoysByDir := map[string][]EnvoyRow{
		"":         {{ID: "r1", Hostname: "root1"}}, // matches root-level hostcrate
		"dc1":      {{ID: "d1", Hostname: "dc1-shared"}},
		"dc1/web":  {{ID: "w1", Hostname: "web02"}, {ID: "w2", Hostname: "web01"}}, // out-of-order to verify sort
		"dc1/db":   {{ID: "b1", Hostname: "db01"}},
		"dc2":      {{ID: "x1", Hostname: "dc2-one"}},
	}

	tree, err := buildEnvoyTree(root, envoysByDir)
	require.NoError(t, err)
	require.NotNil(t, tree)
	assert.Equal(t, ".live", tree.Name)
	assert.Equal(t, "", tree.Path)

	// Root has the root-level envoy.
	require.Len(t, tree.Envoys, 1)
	assert.Equal(t, "root1", tree.Envoys[0].Hostname)

	// Top-level children sorted alphabetically: dc1, dc2.
	require.Len(t, tree.Children, 2)
	dc1 := tree.Children[0]
	dc2 := tree.Children[1]
	assert.Equal(t, "dc1", dc1.Name)
	assert.Equal(t, "dc1", dc1.Path)
	assert.Equal(t, "dc2", dc2.Name)

	// dc1 has its own shared envoy + db, web subchildren (alphabetical).
	require.Len(t, dc1.Envoys, 1)
	assert.Equal(t, "dc1-shared", dc1.Envoys[0].Hostname)
	require.Len(t, dc1.Children, 2)
	db := dc1.Children[0]
	web := dc1.Children[1]
	assert.Equal(t, "db", db.Name)
	assert.Equal(t, "dc1/db", db.Path)
	assert.Equal(t, "web", web.Name)
	assert.Equal(t, "dc1/web", web.Path)

	// web envoys sorted by hostname (input was out-of-order).
	require.Len(t, web.Envoys, 2)
	assert.Equal(t, "web01", web.Envoys[0].Hostname)
	assert.Equal(t, "web02", web.Envoys[1].Hostname)

	// dc1/web and dc1/db have no further subdirs.
	assert.Empty(t, web.Children)
	assert.Empty(t, db.Children)
}

func TestBuildEnvoyTree_EmptyDirectoriesStillRender(t *testing.T) {
	// A directory with no envoys mapping to it still appears in the tree —
	// the operator should be able to navigate the full config hierarchy,
	// not just the populated subset. (Slice 2 will add hide-empty-branches
	// behavior when a chip filter is active.)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "empty-branch"))
	mustMkdir(t, filepath.Join(root, "empty-branch", "deeper"))

	tree, err := buildEnvoyTree(root, map[string][]EnvoyRow{})
	require.NoError(t, err)
	require.Len(t, tree.Children, 1)
	assert.Equal(t, "empty-branch", tree.Children[0].Name)
	assert.Empty(t, tree.Children[0].Envoys)
	require.Len(t, tree.Children[0].Children, 1)
	assert.Equal(t, "deeper", tree.Children[0].Children[0].Name)
}

func TestBuildEnvoyTree_SkipsHiddenAndSymlinks(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".hidden"))
	mustMkdir(t, filepath.Join(root, "visible"))
	// Symlink at root pointing into a real subdir — should be skipped.
	require.NoError(t, os.Symlink(filepath.Join(root, "visible"), filepath.Join(root, "alias")))

	tree, err := buildEnvoyTree(root, map[string][]EnvoyRow{})
	require.NoError(t, err)
	require.Len(t, tree.Children, 1)
	assert.Equal(t, "visible", tree.Children[0].Name)
}

func TestBuildEnvoyTree_IgnoresFilesAtEveryLevel(t *testing.T) {
	// Tree grouping is directory-level only (ADR-032). Files at any level
	// — common.vgo, envoys.vgo, modules/*.vgo — are not rendered; the
	// config-tree page at /fleet/config-tree handles file-level display.
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "dc1"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "common.vgo"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dc1", "envoys.vgo"), []byte(""), 0o644))

	tree, err := buildEnvoyTree(root, map[string][]EnvoyRow{})
	require.NoError(t, err)
	// Root level: only "dc1" (the directory); the common.vgo file is omitted.
	require.Len(t, tree.Children, 1)
	assert.Equal(t, "dc1", tree.Children[0].Name)
	// dc1 level: no children; the envoys.vgo file is omitted.
	assert.Empty(t, tree.Children[0].Children)
}

// pruneEmptyBranches is gated on any-filter-active in the production
// handler; the helper itself is unconditional and recursive. Tests
// pin the canonical contract — node survives iff it has direct envoys
// or at least one surviving child after recursive prune.

func TestPruneEmptyBranches_RetainsBranchesWithSurvivingDescendants(t *testing.T) {
	tree := &EnvoyTreeNode{
		Name: ".live",
		Children: []*EnvoyTreeNode{
			{Name: "dc1", Children: []*EnvoyTreeNode{
				{Name: "web", Envoys: []EnvoyRow{{Hostname: "web01"}}}, // surviving
				{Name: "db"},  // empty leaf — drop
			}},
			{Name: "dc2"}, // empty branch — drop
		},
	}
	kept := pruneEmptyBranches(tree)
	assert.True(t, kept, "root with one surviving great-grandchild should survive")
	require.Len(t, tree.Children, 1, "dc2 (empty) should be pruned; dc1 (has surviving descendant) should stay")
	assert.Equal(t, "dc1", tree.Children[0].Name)
	require.Len(t, tree.Children[0].Children, 1, "dc1/db (empty leaf) should be pruned; dc1/web should stay")
	assert.Equal(t, "web", tree.Children[0].Children[0].Name)
}

func TestPruneEmptyBranches_DropsFullyEmptyTree(t *testing.T) {
	tree := &EnvoyTreeNode{
		Name: ".live",
		Children: []*EnvoyTreeNode{
			{Name: "dc1", Children: []*EnvoyTreeNode{{Name: "web"}, {Name: "db"}}},
			{Name: "dc2"},
		},
	}
	kept := pruneEmptyBranches(tree)
	assert.False(t, kept, "fully-empty tree should not survive")
	assert.Empty(t, tree.Children, "all children pruned")
}

func TestPruneEmptyBranches_KeepsRootEnvoysWithoutChildren(t *testing.T) {
	// An envoy at the root level (matched a root-level hostcrate) should
	// survive even when no subdirectories have content.
	tree := &EnvoyTreeNode{
		Name:   ".live",
		Envoys: []EnvoyRow{{Hostname: "root-host"}},
		Children: []*EnvoyTreeNode{
			{Name: "empty"},
		},
	}
	kept := pruneEmptyBranches(tree)
	assert.True(t, kept)
	assert.Empty(t, tree.Children)
	require.Len(t, tree.Envoys, 1)
}

// EnvoyTreePageData.anyFilterActive — pin the 11-axis check so adding
// or renaming an axis later doesn't silently break the prune trigger.
func TestAnyFilterActive_DetectsEveryAxis(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*EnvoyTreePageData)
	}{
		{"conv", func(d *EnvoyTreePageData) { d.ConvFilter = "converged" }},
		{"drift", func(d *EnvoyTreePageData) { d.DriftFilter = "diverged" }},
		{"reach", func(d *EnvoyTreePageData) { d.ReachFilter = "offline" }},
		{"search", func(d *EnvoyTreePageData) { d.Search = "web" }},
		{"env", func(d *EnvoyTreePageData) { d.EnvFilter = "prod" }},
		{"tag", func(d *EnvoyTreePageData) { d.TagFilter = "canary" }},
		{"role", func(d *EnvoyTreePageData) { d.RoleFilter = "web" }},
		{"os", func(d *EnvoyTreePageData) { d.OSFilter = "ubuntu" }},
		{"arch", func(d *EnvoyTreePageData) { d.ArchFilter = "arm64" }},
		{"module", func(d *EnvoyTreePageData) { d.ModuleFilter = "nginx" }},
		{"compliance", func(d *EnvoyTreePageData) { d.ComplianceFilter = "hipaa" }},
	}
	// Baseline: empty page data → no axis active.
	empty := &EnvoyTreePageData{}
	assert.False(t, empty.anyFilterActive(), "empty data should not report any filter active")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &EnvoyTreePageData{}
			tc.mut(d)
			assert.True(t, d.anyFilterActive(), "%s should mark anyFilterActive true", tc.name)
		})
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
