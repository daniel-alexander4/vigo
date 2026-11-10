// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"

	"git.alexander4.io/vigo/server/config"
	"github.com/google/uuid"
)

// TraceVizPageData extends EnvoyTracePageData with visualization data.
type TraceVizPageData struct {
	EnvoyTracePageData
	// Inheritance tree (server-rendered SVG).
	TreeLevels  []InheritanceVizLevel
	TreeViewBox string
	// Module dependency DAG (D3 force-directed).
	DependencyJSON template.JS
	HasDeps        bool
}

// InheritanceVizLevel is one level in the inheritance tree SVG.
type InheritanceVizLevel struct {
	Label        string // file path (dir/file)
	Configcrates []string
	Roles        []string
	VarCount     int
	Overrides    int
	Color        string
	Y            int // SVG Y position
	BoxHeight    int
}

// DepNode is a node in the dependency graph (D3).
type DepNode struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "configcrate" or "resource"
}

// DepEdge is an edge in the dependency graph (D3).
type DepEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"` // "depends_on", "before", "notify", "subscribes"
}

// DepGraph is the full dependency graph for D3.
type DepGraph struct {
	Nodes []DepNode `json:"nodes"`
	Edges []DepEdge `json:"edges"`
}

const treeBoxWidth = 420
const treeBoxX = 60
const treeGap = 12 // gap between levels

// EnvoyTraceViz renders the config trace visualization page.
func (h *Handler) EnvoyTraceViz(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	trace := h.configStore.Trace(detail.Envoy.Hostname)

	data := TraceVizPageData{
		EnvoyTracePageData: EnvoyTracePageData{
			PageData: PageData{Title: "Config Trace: " + detail.Envoy.Hostname, ActiveNav: "envoys"},
			Hostname: detail.Envoy.Hostname,
			EnvoyID:  idStr,
			Trace:    trace,
		},
	}

	if trace != nil && trace.Matched {
		data.buildInheritanceTree(trace)
		data.buildDependencyGraph(trace)
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoy-trace-viz", data)
}

func (data *TraceVizPageData) buildInheritanceTree(trace *config.ConfigTrace) {
	levels := trace.Inheritance
	total := len(levels)
	if total == 0 {
		return
	}

	y := 0
	vizLevels := make([]InheritanceVizLevel, total)
	for i, lvl := range levels {
		label := lvl.Dir
		if label != "" {
			label += "/"
		}
		label += lvl.File

		// Content lines: label + modules line + roles line = base height.
		lines := 1 // label
		if len(lvl.Configcrates) > 0 {
			lines++
		}
		if len(lvl.Roles) > 0 {
			lines++
		}
		boxHeight := 8 + lines*16

		// Color gradient: deeper levels get darker green.
		green := 161 + (total-1-i)*10
		if green > 227 {
			green = 227
		}
		color := fmt.Sprintf("rgb(166, %d, 161)", green)

		vizLevels[i] = InheritanceVizLevel{
			Label:        label,
			Configcrates: lvl.Configcrates,
			Roles:        lvl.Roles,
			VarCount:     len(lvl.Vars),
			Overrides:    len(lvl.Overrides),
			Color:        color,
			Y:            y,
			BoxHeight:    boxHeight,
		}
		y += boxHeight + treeGap
	}

	data.TreeLevels = vizLevels
	data.TreeViewBox = fmt.Sprintf("0 0 %d %d", treeBoxX+treeBoxWidth+20, y)
}

func (data *TraceVizPageData) buildDependencyGraph(trace *config.ConfigTrace) {
	deps := trace.Dependencies
	if len(deps) == 0 {
		return
	}

	nodeSet := make(map[string]string) // id → type
	for _, d := range deps {
		if _, ok := nodeSet[d.From]; !ok {
			nodeSet[d.From] = d.Level
		}
		if _, ok := nodeSet[d.To]; !ok {
			nodeSet[d.To] = d.Level
		}
	}

	nodes := make([]DepNode, 0, len(nodeSet))
	for id, typ := range nodeSet {
		nodes = append(nodes, DepNode{ID: id, Type: typ})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	edges := make([]DepEdge, len(deps))
	for i, d := range deps {
		edges[i] = DepEdge{
			Source: d.From,
			Target: d.To,
			Type:   d.Type,
		}
	}

	graph := DepGraph{Nodes: nodes, Edges: edges}
	b, _ := json.Marshal(graph)
	data.DependencyJSON = template.JS(b)
	data.HasDeps = true
}
