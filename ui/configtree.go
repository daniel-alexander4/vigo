// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// configTreeNode is one entry (file or directory) under .live/.
type configTreeNode struct {
	Name     string
	IsDir    bool
	Kind     string // file-type classification; empty for directories
	Children []*configTreeNode
}

// ConfigTreePageData drives templates/pages/config-tree.html.
type ConfigTreePageData struct {
	PageData
	LiveDir string
	Root    *configTreeNode
	Missing bool
}

// ConfigTree renders the published config tree at /fleet/config-tree.
// Walks .live/ on every request — the directory is small and bounded,
// and operators expect to see the current published state, not a cached
// snapshot from boot.
func (h *Handler) ConfigTree(w http.ResponseWriter, r *http.Request) {
	data := ConfigTreePageData{
		PageData: PageData{Title: "Config Tree", ActiveNav: "config-tree"},
	}

	if h.configStore == nil {
		data.Missing = true
		h.setCommonWithRequest(&data.PageData, r)
		h.renderer.Render(w, "config-tree", data)
		return
	}

	// ConfigStore.Dir() returns the active content root — on a running
	// server that is already /srv/vigo/stacks/.live (see contentDir
	// comment in server/config/loader.go). Don't re-append ".live".
	liveDir := h.configStore.Dir()
	data.LiveDir = liveDir

	if _, err := os.Stat(liveDir); err != nil {
		data.Missing = true
	} else {
		root, err := buildConfigTree(liveDir)
		if err != nil {
			slog.Error("walking .live for config-tree", "path", liveDir, "error", err)
			http.Error(w, "config tree unavailable", http.StatusInternalServerError)
			return
		}
		data.Root = root
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "config-tree", data)
}

func buildConfigTree(liveDir string) (*configTreeNode, error) {
	root := &configTreeNode{Name: ".live", IsDir: true}
	if err := walkConfigTree(liveDir, root, ""); err != nil {
		return nil, err
	}
	return root, nil
}

func walkConfigTree(dir string, node *configTreeNode, relPath string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// Directories first, then files; alphabetical within each group.
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return entries[i].Name() < entries[j].Name()
	})
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := &configTreeNode{Name: name}
		if e.IsDir() {
			child.IsDir = true
			subRel := name
			if relPath != "" {
				subRel = relPath + "/" + name
			}
			if err := walkConfigTree(filepath.Join(dir, name), child, subRel); err != nil {
				return err
			}
		} else {
			child.Kind = classifyConfigFile(name, relPath)
		}
		node.Children = append(node.Children, child)
	}
	return nil
}

// classifyConfigFile returns a file-kind label used to color-code the leaf.
// relPath is the directory chain from .live/ to the file's parent (slash-joined).
func classifyConfigFile(name, relPath string) string {
	switch name {
	case "common.vgo":
		return "common"
	case "environments.vgo":
		return "environments"
	case "roles.vgo":
		return "roles"
	case "waivers.vgo":
		return "waivers"
	case "envoys.vgo":
		return "matchfile"
	}
	if strings.HasSuffix(name, ".vgo") {
		for _, p := range strings.Split(relPath, "/") {
			switch p {
			case "modules":
				return "module"
			case "usercrates":
				return "usercrate"
			}
		}
		return "vgo"
	}
	return "other"
}
