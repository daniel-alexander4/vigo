// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// configTreeNode is one entry (file or directory) under .live/.
type configTreeNode struct {
	Name     string
	IsDir    bool
	Kind     string // file-type classification; empty for directories
	Path     string // slash-joined rel path from .live root; files only (drives the viewer hx-get)
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
			if relPath != "" {
				child.Path = relPath + "/" + name
			} else {
				child.Path = name
			}
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
		return "hostcrate"
	}
	if strings.HasSuffix(name, ".vgo") {
		for _, p := range strings.Split(relPath, "/") {
			switch p {
			case "configcrates":
				return "configcrate"
			case "usercrates":
				return "usercrate"
			}
		}
		return "vgo"
	}
	return "other"
}

// maxConfigFileView caps how much of a file the viewer will render into
// a <pre>. Published config files are small; the cap guards against an
// unexpectedly large file under .live/ blowing up the response.
const maxConfigFileView = 2 << 20 // 2 MB

// resolveLiveFile validates a request-supplied relative path against the
// published-config root and returns the absolute path of the regular file
// to read. It mirrors the secrets-provider guard (server/secrets/local.go):
// Clean+HasPrefix to reject `..`/absolute escapes, then an EvalSymlinks
// re-check so a symlink inside .live/ can't redirect the read outside it.
// Symlinks are rejected outright, matching walkConfigTree which skips them.
func resolveLiveFile(liveDir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path required")
	}
	full := filepath.Clean(filepath.Join(liveDir, filepath.FromSlash(rel)))
	base := filepath.Clean(liveDir)
	if !strings.HasPrefix(full, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes config root")
	}
	// Reject symlinks (the tree never lists them) before resolving.
	if fi, err := os.Lstat(full); err != nil {
		return "", err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink not permitted")
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	if resolved != resolvedBase && !strings.HasPrefix(resolved, resolvedBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path resolves outside config root")
	}
	return resolved, nil
}

// ConfigTreeFileFragmentData drives the "config-tree-file-fragment" define.
type ConfigTreeFileFragmentData struct {
	Path     string
	Content  string
	NotFound bool
	TooLarge bool
	Binary   bool
	Size     int64
	Locale   string
}

// ConfigTreeFileFragment serves the raw contents of one published config
// file under .live/ for the side-panel viewer on /fleet/config-tree. The
// published .vgo source carries `secret:` path references, not resolved
// values, so no secret material is exposed. Auth matches the page: the
// global middleware authenticates; there is no finer per-file gate.
func (h *Handler) ConfigTreeFileFragment(w http.ResponseWriter, r *http.Request) {
	data := ConfigTreeFileFragmentData{
		Path:   r.URL.Query().Get("path"),
		Locale: h.localeFromRequest(r),
	}

	if h.configStore == nil {
		data.NotFound = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}

	full, err := resolveLiveFile(h.configStore.Dir(), data.Path)
	if err != nil {
		data.NotFound = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}

	fi, err := os.Stat(full)
	if err != nil {
		data.NotFound = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}
	data.Size = fi.Size()
	if fi.Size() > maxConfigFileView {
		data.TooLarge = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		data.NotFound = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}
	if !utf8.Valid(raw) {
		data.Binary = true
		h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
		return
	}
	data.Content = string(raw)
	h.renderer.RenderFragment(w, "config-tree", "config-tree-file-fragment", data)
}
