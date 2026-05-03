// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"math"
	"sort"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/uifmt"
)

// Maximum envoys/items shown in viz visualizations for performance.
const maxVizItems = 50

// VizColorPalette — 14 distinct Catppuccin Mocha accent colors for data
// series. Ordered so that adjacent indices are visually distinct, which
// matters for the hash-based peer-visual assignment in swarm_viz.go
// (two envoys that hash to adjacent slots should still look different).
var VizColorPalette = []string{
	uifmt.HexBlue,
	uifmt.HexGreen,
	uifmt.HexRed,
	uifmt.HexYellow,
	uifmt.HexMauve,
	uifmt.HexPeach,
	uifmt.HexTeal,
	uifmt.HexPink,
	uifmt.HexSapphire,
	uifmt.HexMaroon,
	uifmt.HexLavender,
	uifmt.HexSky,
	uifmt.HexRosewater,
	uifmt.HexFlamingo,
}

// VizShapePalette — 5 SVG symbol names for peer shape assignment.
// Used alongside VizColorPalette in the swarm viz to distinguish envoys
// when color alone runs out. d3.symbolCircle/Square/Triangle/Diamond/Star
// are the backing D3 types; the strings here match the JS-side switch.
var VizShapePalette = []string{
	"circle",
	"square",
	"triangle",
	"diamond",
	"star",
}

// VizDashPalette — 4 SVG stroke-dasharray patterns for peer line styles.
// Used in the swarm viz timeline alongside color to distinguish envoys.
// Empty string is solid (no dashes).
var VizDashPalette = []string{
	"",         // solid
	"8,4",      // dashed
	"2,3",      // dotted
	"8,3,2,3",  // dash-dot
}

// VizStatusColor maps convergence/envoy status to Catppuccin Mocha colors.
// Convergence buckets dispatch through convergence.ClassifyStatus; the
// remaining lifecycle / reachability vocabulary stays local since it lives
// outside the convergence SoT (see CLAUDE.md "Convergence vs Compliance").
func VizStatusColor(status string) string {
	switch convergence.ClassifyStatus(status) {
	case convergence.StatusConverged:
		return uifmt.HexGreen
	case convergence.StatusRelapsed, convergence.StatusDegraded:
		return uifmt.HexYellow
	case convergence.StatusDiverged:
		return uifmt.HexPeach
	case convergence.StatusFailed:
		return uifmt.HexRed
	}
	switch status {
	case "complete", "success":
		return uifmt.HexGreen
	case "downloading":
		return uifmt.HexYellow
	case "offline", "skipped":
		return uifmt.HexOverlay0
	default: // pending, no data
		return uifmt.HexSurface1
	}
}

// VizActionColor maps run result actions to Catppuccin Mocha colors.
func VizActionColor(action string) string {
	switch convergence.ClassifyAction(action) {
	case convergence.ActionCreate:
		return uifmt.HexGreen
	case convergence.ActionUpdate:
		return uifmt.HexBlue
	case convergence.ActionDelete:
		return uifmt.HexRed
	default: // noop, skip
		return uifmt.HexSurface1
	}
}

// VizComplianceColor maps compliance control status to Catppuccin Mocha colors.
func VizComplianceColor(status string) string {
	switch status {
	case "satisfied":
		return uifmt.HexGreen
	case "partial":
		return uifmt.HexYellow
	case "not_satisfied":
		return uifmt.HexRed
	default: // not applicable
		return uifmt.HexSurface1
	}
}

// AssignColors maps a sorted list of names to deterministic palette colors.
func AssignColors(names []string) map[string]string {
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	colors := make(map[string]string, len(sorted))
	for i, name := range sorted {
		colors[name] = VizColorPalette[i%len(VizColorPalette)]
	}
	return colors
}

// DonutSegment represents one slice of a donut chart.
type DonutSegment struct {
	Label      string
	Count      int
	Color      string
	StartAngle float64 // degrees, 0 = top (12 o'clock)
	EndAngle   float64
}

// BuildDonutSegments creates donut segments from label/count/color triples.
// Filters out zero-count entries. Returns segments with computed angles.
func BuildDonutSegments(entries []DonutSegment) []DonutSegment {
	total := 0
	for _, e := range entries {
		total += e.Count
	}
	if total == 0 {
		return nil
	}

	var segments []DonutSegment
	angle := 0.0
	for _, e := range entries {
		if e.Count == 0 {
			continue
		}
		sweep := float64(e.Count) / float64(total) * 360.0
		segments = append(segments, DonutSegment{
			Label:      e.Label,
			Count:      e.Count,
			Color:      e.Color,
			StartAngle: angle,
			EndAngle:   angle + sweep,
		})
		angle += sweep
	}
	return segments
}

// DonutArc computes an SVG path for an arc segment of a donut/pie chart.
// Angles are in degrees, 0 = top (12 o'clock), clockwise.
func DonutArc(cx, cy, r, startAngle, endAngle float64) string {
	// Handle full circle (single segment = 360°).
	if endAngle-startAngle >= 359.99 {
		return fmt.Sprintf("M %.1f %.1f A %.1f %.1f 0 1 1 %.1f %.1f A %.1f %.1f 0 1 1 %.1f %.1f Z",
			cx, cy-r, r, r, cx, cy+r, r, r, cx, cy-r)
	}

	startRad := (startAngle - 90) * math.Pi / 180
	endRad := (endAngle - 90) * math.Pi / 180

	x1 := cx + r*math.Cos(startRad)
	y1 := cy + r*math.Sin(startRad)
	x2 := cx + r*math.Cos(endRad)
	y2 := cy + r*math.Sin(endRad)

	largeArc := 0
	if endAngle-startAngle > 180 {
		largeArc = 1
	}

	return fmt.Sprintf("M %.1f %.1f L %.1f %.1f A %.1f %.1f 0 %d 1 %.1f %.1f Z",
		cx, cy, x1, y1, r, r, largeArc, x2, y2)
}
