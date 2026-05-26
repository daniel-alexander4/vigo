// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
)

// EnvoyTreeNode is one directory node in the envoy-tree view. Each node
// carries the envoys whose matched hostcrate's DirPath equals this node's
// path; nested directories live in Children. The root node has Path="".
//
// Rollup counts (Total / Converged / Degraded / Failed / NoData) cover
// this node's envoys plus every descendant — collapsed branches still
// surface their state. DefaultOpen is true for the root and for any
// subtree containing a non-converged envoy (triage-first).
type EnvoyTreeNode struct {
	Path     string // directory path relative to .live/ ("" for root, "dc1/web" for nested)
	Name     string // basename for display ("dc1", "web"); the root displays as ".live"
	Envoys   []EnvoyRow
	Children []*EnvoyTreeNode

	// Rollup counts over this node + all descendants. Populated by
	// computeRollups after the tree is fully built.
	Total          int
	CountConverged int
	CountDegraded  int
	CountFailed    int
	CountNoData    int

	// DefaultOpen drives the <details open> attribute on first render.
	// True when this node or any descendant has a non-converged envoy
	// (triage state surfaces by default); also true for the root.
	DefaultOpen bool

	// EnvoyOverflow is the number of envoys at this node that were
	// dropped from the rendered list to keep the DOM bounded.
	// `len(Envoys) + EnvoyOverflow == (envoys before truncation)`.
	// Template renders a "+ N more" link when EnvoyOverflow > 0.
	EnvoyOverflow int
}

// envoyTreeRenderCap is the maximum envoys rendered per directory node.
// Above this, the template shows "+ N more" pointing at the flat list.
// Keeps the DOM bounded when the (unmatched) bucket or a busy hostcrate
// dir holds thousands of envoys at fleet scale.
const envoyTreeRenderCap = 50

// EnvoyTreePageData drives templates/pages/envoys-tree.html. Carries the
// same chip-filter state the flat envoys page uses so the UX is uniform
// between the two views (ADR-032 § "Filter UX"). Slice 2 adds 7 axes
// (env/tag/role/os/arch/module/compliance) on top of the original 4.
type EnvoyTreePageData struct {
	PageData
	Root             *EnvoyTreeNode
	Unmatched        *EnvoyTreeNode // synthetic node for envoys with no matched hostcrate; nil when empty
	Missing          bool           // .live/ doesn't exist (no published config yet)
	TotalEnvoys      int            // total in the fleet (pre-filter)
	MatchingTotal    int            // surviving the chip filters
	ConvFilter       string
	DriftFilter      string
	ReachFilter      string
	Search           string
	EnvFilter        string
	TagFilter        string
	RoleFilter       string
	OSFilter         string
	ArchFilter       string
	ModuleFilter     string
	ComplianceFilter string
	// Filter drawer state — mirrored from the EnvoysPageData built by
	// EnvoysData so the tree view shares the same pill enumeration and
	// active-count logic.
	FilterAxes        []FilterAxis
	ActiveFilterCount int
	BasePath          string // "/envoys/tree" — page path the drawer pills anchor against
	// Conv/drift/reach counts for the read-only chip bar above the tree.
	// Same source-of-truth as the flat /envoys page.
	CountConverged, CountDegraded, CountFailed, CountNoData int
	CountChanged, CountDiverged                             int
	CountOffline, CountObserving                            int
	OfflineThresholdLabel                                   string
}

// EnvoyTree renders the envoy-tree navigation view at /envoys/tree.
// Walks the .live/ directory hierarchy (same shape as /fleet/config-tree)
// and groups the FleetIndex envoys under the directory of the hostcrate
// that matched them. Per ADR-032: coexist with the flat /envoys view,
// reuse the chip filter pattern, click-through to /envoys/{id} for detail.
func (h *Handler) EnvoyTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data := EnvoyTreePageData{
		PageData:         PageData{Title: "Envoys (tree)", ActiveNav: "envoys"},
		ConvFilter:       q.Get("conv"),
		DriftFilter:      q.Get("drift"),
		ReachFilter:      q.Get("reach"),
		Search:           q.Get("search"),
		EnvFilter:        q.Get("env"),
		TagFilter:        q.Get("tag"),
		RoleFilter:       q.Get("role"),
		OSFilter:         q.Get("os"),
		ArchFilter:       q.Get("arch"),
		ModuleFilter:     q.Get("module"),
		ComplianceFilter: q.Get("compliance"),
	}

	if h.configStore == nil {
		data.Missing = true
		h.setCommonWithRequest(&data.PageData, r)
		h.renderer.Render(w, "envoys-tree", data)
		return
	}

	liveDir := h.configStore.Dir()
	if _, err := os.Stat(liveDir); err != nil {
		data.Missing = true
		h.setCommonWithRequest(&data.PageData, r)
		h.renderer.Render(w, "envoys-tree", data)
		return
	}

	// Unpaginated filtered envoy list — chip filters apply, sort doesn't
	// matter at this stage (the tree builder sorts envoys within each
	// directory node alphabetically). Limit=0 returns all matching rows.
	envoyData := h.dataSource.EnvoysData(
		h.baseInterval, h.offlineThreshold,
		data.ConvFilter, data.DriftFilter, data.ReachFilter, data.Search,
		data.EnvFilter, data.TagFilter, data.RoleFilter, data.OSFilter,
		data.ArchFilter, data.ModuleFilter, data.ComplianceFilter,
		"", "", 0, 0,
	)
	data.TotalEnvoys = envoyData.TotalEnvoys
	data.MatchingTotal = envoyData.MatchingTotal
	data.FilterAxes = envoyData.FilterAxes
	data.ActiveFilterCount = envoyData.ActiveFilterCount
	data.BasePath = "/envoys/tree"
	data.CountConverged = envoyData.CountConverged
	data.CountDegraded = envoyData.CountDegraded
	data.CountFailed = envoyData.CountFailed
	data.CountNoData = envoyData.CountNoData
	data.CountChanged = envoyData.CountChanged
	data.CountDiverged = envoyData.CountDiverged
	data.CountOffline = envoyData.CountOffline
	data.CountObserving = envoyData.CountObserving
	data.OfflineThresholdLabel = envoyData.OfflineThresholdLabel

	// Per-envoy DirPath lookup via the existing MatchDirPaths SoT.
	// MatchDirPaths returns pattern → DirPath; we look up each envoy's
	// matched pattern via ConfigStore.Match and join.
	dirByPattern := h.configStore.MatchDirPaths()
	envoysByDir := make(map[string][]EnvoyRow)
	var unmatched []EnvoyRow
	for _, e := range envoyData.Envoys {
		pattern, _ := h.configStore.Match(e.Hostname)
		if pattern == "" {
			unmatched = append(unmatched, e)
			continue
		}
		dir, ok := dirByPattern[pattern]
		if !ok {
			unmatched = append(unmatched, e)
			continue
		}
		// MatchDirPaths maps "" → "(root)" for root-level entries. The
		// envoy-tree builder uses "" for the root path; translate back.
		if dir == "(root)" {
			dir = ""
		}
		envoysByDir[dir] = append(envoysByDir[dir], e)
	}

	// Build the set of hostcrate-bearing directories + every ancestor
	// up to root. Walk-time pruning uses this set to skip directories
	// that aren't on a path to a hostcrate (modules/, usercrates/,
	// templates/, tasks/, workflows/, …). The tree mirrors the
	// hostcrate hierarchy only, not the entire stockpile layout.
	keptDirs := hostcrateDirsAndAncestors(dirByPattern)

	// Walk .live/ to build the directory skeleton, then inject envoys at
	// each node from envoysByDir. Sort each node's envoy list by hostname
	// for deterministic rendering.
	root, err := buildEnvoyTree(liveDir, envoysByDir, keptDirs)
	if err != nil {
		slog.Error("walking .live for envoys-tree", "path", liveDir, "error", err)
		http.Error(w, "envoys tree unavailable", http.StatusInternalServerError)
		return
	}

	// Collapse the synthetic ".live/" root when there's exactly one
	// hostcrate-bearing child and no root-level envoys — operators see
	// "alexander4/" at the top instead of a noise wrapper.
	if len(root.Children) == 1 && len(root.Envoys) == 0 {
		root = root.Children[0]
	}
	data.Root = root

	// ADR-032 slice 2: when any chip is non-empty, prune branches that
	// have no surviving envoys (filter-active = "show me only what's
	// relevant"). Baseline (no filter) keeps the full hierarchy so
	// operators can navigate the entire config tree.
	if data.anyFilterActive() {
		pruneEmptyBranches(data.Root)
	}

	if len(unmatched) > 0 {
		sort.Slice(unmatched, func(i, j int) bool { return unmatched[i].Hostname < unmatched[j].Hostname })
		// (unmatched) is a debug bucket (envoys with no matching
		// hostcrate). At loadtest scale this can hold thousands of
		// rows — never auto-open it; the operator clicks in when
		// investigating. computeRollups still wires up counts so the
		// collapsed summary surfaces "N envoys" + status breakdown.
		data.Unmatched = &EnvoyTreeNode{Name: "(unmatched)", Envoys: unmatched}
		computeRollups(data.Unmatched)
		data.Unmatched.DefaultOpen = false
	}

	// Roll up envoy counts (per-status) and compute default-open
	// triage state after the tree is final.
	computeRollups(data.Root)
	data.Root.DefaultOpen = true // root always opens

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoys-tree", data)
}

// hostcrateDirsAndAncestors returns the set of directory paths that
// either contain a hostcrate or sit on the path to one. Keys use the
// same "" → root, "a/b" → nested convention as the rest of the walker.
// The walker uses this set to skip every directory that isn't load-
// bearing for envoy placement (modules/, usercrates/, templates/, …).
func hostcrateDirsAndAncestors(dirByPattern map[string]string) map[string]bool {
	out := map[string]bool{"": true}
	for _, dir := range dirByPattern {
		if dir == "(root)" {
			continue // root already in the set
		}
		for d := dir; d != ""; {
			out[d] = true
			idx := strings.LastIndex(d, "/")
			if idx < 0 {
				break
			}
			d = d[:idx]
		}
	}
	return out
}

// computeRollups walks the subtree depth-first and populates Total +
// per-status counts on every node (own envoys + descendants). Returns
// true when this subtree contains a non-converged envoy at any depth —
// the caller wires that into DefaultOpen so triage state is visible on
// first render without expanding every branch.
//
// Rollups are computed BEFORE truncating Envoys to envoyTreeRenderCap so
// the per-node "N envoys" + status breakdown stays accurate even when
// the rendered list is capped.
func computeRollups(node *EnvoyTreeNode) bool {
	hasFailure := false
	for _, e := range node.Envoys {
		node.Total++
		switch e.Status {
		case "converged":
			node.CountConverged++
		case "degraded":
			node.CountDegraded++
			hasFailure = true
		case "failed":
			node.CountFailed++
			hasFailure = true
		default:
			node.CountNoData++
			hasFailure = true
		}
	}
	for _, c := range node.Children {
		childHasFailure := computeRollups(c)
		node.Total += c.Total
		node.CountConverged += c.CountConverged
		node.CountDegraded += c.CountDegraded
		node.CountFailed += c.CountFailed
		node.CountNoData += c.CountNoData
		if childHasFailure {
			hasFailure = true
		}
	}
	if hasFailure {
		node.DefaultOpen = true
	}
	// Cap the rendered envoy list AFTER rollups so totals stay truthful.
	if len(node.Envoys) > envoyTreeRenderCap {
		node.EnvoyOverflow = len(node.Envoys) - envoyTreeRenderCap
		node.Envoys = node.Envoys[:envoyTreeRenderCap]
	}
	return hasFailure
}

// buildEnvoyTree walks the .live/ directory and produces a tree whose
// nodes correspond to directories that hold a hostcrate or sit on the
// path to one. keptDirs is the SoT for that filter; directories not in
// it (modules/, usercrates/, templates/, tasks/, workflows/, …) never
// appear. Envoys are attached at each node from envoysByDir, sorted
// by hostname. Files are ignored — this view groups by directory
// (ADR-032 § "Tree definition").
func buildEnvoyTree(liveDir string, envoysByDir map[string][]EnvoyRow, keptDirs map[string]bool) (*EnvoyTreeNode, error) {
	root := &EnvoyTreeNode{Path: "", Name: ".live"}
	root.Envoys = sortedEnvoys(envoysByDir[""])
	if err := walkEnvoyTree(liveDir, root, "", envoysByDir, keptDirs); err != nil {
		return nil, err
	}
	return root, nil
}

func walkEnvoyTree(absDir string, node *EnvoyTreeNode, relPath string, envoysByDir map[string][]EnvoyRow, keptDirs map[string]bool) error {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		childPath := name
		if relPath != "" {
			childPath = relPath + "/" + name
		}
		// Skip directories that aren't on a path to a hostcrate — the
		// stockpile's modules/, usercrates/, templates/, tasks/, etc.
		// are valid stockpile contents but not navigation surfaces for
		// "which envoys live where."
		if !keptDirs[childPath] {
			continue
		}
		child := &EnvoyTreeNode{
			Path:   childPath,
			Name:   name,
			Envoys: sortedEnvoys(envoysByDir[childPath]),
		}
		if err := walkEnvoyTree(absDir+"/"+name, child, childPath, envoysByDir, keptDirs); err != nil {
			return err
		}
		node.Children = append(node.Children, child)
	}
	return nil
}

// anyFilterActive reports whether any of the 11 chip axes is non-empty.
// Drives the prune-empty-branches behavior on tree (ADR-032 slice 2).
func (d *EnvoyTreePageData) anyFilterActive() bool {
	return d.ConvFilter != "" ||
		d.DriftFilter != "" ||
		d.ReachFilter != "" ||
		d.Search != "" ||
		d.EnvFilter != "" ||
		d.TagFilter != "" ||
		d.RoleFilter != "" ||
		d.OSFilter != "" ||
		d.ArchFilter != "" ||
		d.ModuleFilter != "" ||
		d.ComplianceFilter != ""
}

// pruneEmptyBranches recursively drops Children whose subtree has no
// envoys at any depth. Mutates node in place. Returns true if node
// itself has any surviving content (envoys or non-pruned children) —
// the caller's prune decision keys off the return value of each child.
//
// Only invoked when a filter is active (ADR-032 slice 2). Baseline
// rendering keeps every directory node so operators can navigate the
// full config hierarchy.
func pruneEmptyBranches(node *EnvoyTreeNode) bool {
	if node == nil {
		return false
	}
	kept := node.Children[:0]
	for _, c := range node.Children {
		if pruneEmptyBranches(c) {
			kept = append(kept, c)
		}
	}
	node.Children = kept
	return len(node.Envoys) > 0 || len(node.Children) > 0
}

func sortedEnvoys(rows []EnvoyRow) []EnvoyRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]EnvoyRow, len(rows))
	copy(out, rows)
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}
