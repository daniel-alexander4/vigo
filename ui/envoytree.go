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
type EnvoyTreeNode struct {
	Path     string // directory path relative to .live/ ("" for root, "dc1/web" for nested)
	Name     string // basename for display ("dc1", "web"); the root displays as ".live"
	Envoys   []EnvoyRow
	Children []*EnvoyTreeNode
}

// EnvoyTreePageData drives templates/pages/envoys-tree.html. Carries the
// same chip-filter state the flat envoys page uses so the UX is uniform
// between the two views (ADR-032 § "Filter UX").
type EnvoyTreePageData struct {
	PageData
	Root            *EnvoyTreeNode
	Unmatched       *EnvoyTreeNode // synthetic node for envoys with no matched hostcrate; nil when empty
	Missing         bool           // .live/ doesn't exist (no published config yet)
	TotalEnvoys     int            // total in the fleet (pre-filter)
	MatchingTotal   int            // surviving the chip filters
	ConvFilter      string
	DriftFilter     string
	ReachFilter     string
	Search          string
}

// EnvoyTree renders the envoy-tree navigation view at /envoys/tree.
// Walks the .live/ directory hierarchy (same shape as /fleet/config-tree)
// and groups the FleetIndex envoys under the directory of the hostcrate
// that matched them. Per ADR-032: coexist with the flat /envoys view,
// reuse the chip filter pattern, click-through to /envoys/{id} for detail.
func (h *Handler) EnvoyTree(w http.ResponseWriter, r *http.Request) {
	data := EnvoyTreePageData{
		PageData:    PageData{Title: "Envoys (tree)", ActiveNav: "envoys"},
		ConvFilter:  r.URL.Query().Get("conv"),
		DriftFilter: r.URL.Query().Get("drift"),
		ReachFilter: r.URL.Query().Get("reach"),
		Search:      r.URL.Query().Get("search"),
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
		"", "", 0, 0,
	)
	data.TotalEnvoys = envoyData.TotalEnvoys
	data.MatchingTotal = envoyData.MatchingTotal

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

	// Walk .live/ to build the directory skeleton, then inject envoys at
	// each node from envoysByDir. Sort each node's envoy list by hostname
	// for deterministic rendering.
	root, err := buildEnvoyTree(liveDir, envoysByDir)
	if err != nil {
		slog.Error("walking .live for envoys-tree", "path", liveDir, "error", err)
		http.Error(w, "envoys tree unavailable", http.StatusInternalServerError)
		return
	}
	data.Root = root

	if len(unmatched) > 0 {
		sort.Slice(unmatched, func(i, j int) bool { return unmatched[i].Hostname < unmatched[j].Hostname })
		data.Unmatched = &EnvoyTreeNode{Name: "(unmatched)", Envoys: unmatched}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoys-tree", data)
}

// buildEnvoyTree walks the .live/ directory and produces a tree whose
// nodes correspond to directories. At each node, envoys whose matched
// hostcrate has that DirPath are attached (sorted by hostname).
// Subdirectories become Children. Files in .live/ are ignored — this
// view groups by directory, not file (ADR-032 § "Tree definition").
func buildEnvoyTree(liveDir string, envoysByDir map[string][]EnvoyRow) (*EnvoyTreeNode, error) {
	root := &EnvoyTreeNode{Path: "", Name: ".live"}
	root.Envoys = sortedEnvoys(envoysByDir[""])
	if err := walkEnvoyTree(liveDir, root, "", envoysByDir); err != nil {
		return nil, err
	}
	return root, nil
}

func walkEnvoyTree(absDir string, node *EnvoyTreeNode, relPath string, envoysByDir map[string][]EnvoyRow) error {
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
		child := &EnvoyTreeNode{
			Path:   childPath,
			Name:   name,
			Envoys: sortedEnvoys(envoysByDir[childPath]),
		}
		if err := walkEnvoyTree(absDir+"/"+name, child, childPath, envoysByDir); err != nil {
			return err
		}
		node.Children = append(node.Children, child)
	}
	return nil
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
