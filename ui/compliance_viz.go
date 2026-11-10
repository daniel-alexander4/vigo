// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"net/http"
	"sort"

	"git.alexander4.io/vigo/server/config"
	fwreg "git.alexander4.io/vigo/server/frameworks"
	"git.alexander4.io/vigo/server/uifmt"
)

// complianceReportTitles maps a compliance report URL slug → {page title,
// slug}. Derived from the framework registry (server/frameworks), the
// single source of truth, plus the "cis" aggregation — a UI-only roll-up
// view across the three CIS benchmarks with no standard key or builder of
// its own. Shared by ComplianceFrameworkViz and ComplianceFramework.
func complianceReportTitles() map[string][2]string {
	m := make(map[string][2]string, len(fwreg.All)+1)
	for _, f := range fwreg.All {
		m[f.Slug] = [2]string{f.ReportTitle, f.Slug}
	}
	m["cis"] = [2]string{"CIS Benchmark Compliance Report", "cis"}
	return m
}

// ComplianceVizPageData extends the base report page with visualization data.
type ComplianceVizPageData struct {
	PageData
	FrameworkTitle string
	FrameworkSlug  string
	// Report data (accessed by template as .Report.Summary, .Report.Controls, etc.)
	Report any
	// Viz-specific fields.
	HeatmapRows    []ComplianceHeatmapRow
	HeatmapColumns []string // control IDs
	HeatmapColMax  int
	DonutSegments  []DonutSegment
	VizCapped      bool
	VizTotal       int
}

// ComplianceHeatmapRow is one envoy's row in the compliance heatmap.
type ComplianceHeatmapRow struct {
	Hostname    string
	CoveragePct float64
	Cells       []ComplianceHeatmapCell
}

// ComplianceHeatmapCell is one control's status for an envoy.
type ComplianceHeatmapCell struct {
	Color string
}

// ComplianceFrameworkViz renders the compliance framework viz page.
func (h *Handler) ComplianceFrameworkViz(w http.ResponseWriter, r *http.Request) {
	fw := r.PathValue("framework")

	frameworks := complianceReportTitles()

	info, ok := frameworks[fw]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if standardKey, hasKey := config.StandardKeyForSlug[fw]; hasKey {
		if h.configStore != nil && !h.configStore.IsStandardEnabled(standardKey) {
			http.NotFound(w, r)
			return
		}
	}

	// Load report data upfront (unlike the table view which lazy-loads via HTMX).
	var reportData any
	if h.reportBuilder != nil && h.reportBuilder.Framework != nil {
		var err error
		reportData, err = h.reportBuilder.Framework(r, info[1])
		if err != nil {
			http.Error(w, "failed to load report data", http.StatusInternalServerError)
			return
		}
	}

	data := ComplianceVizPageData{
		PageData:       PageData{Title: info[0], ActiveNav: "compliance"},
		FrameworkTitle: info[0],
		FrameworkSlug:  info[1],
		Report:         reportData,
	}

	// Build visualizations from the report data via JSON round-trip
	// (report type is any — avoids import cycle with server/api).
	data.buildFromReport(reportData)

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance-viz", data)
}

// buildFromReport extracts visualization data from the report via JSON round-trip.
func (data *ComplianceVizPageData) buildFromReport(report any) {
	if report == nil {
		return
	}

	// JSON round-trip to access report fields without importing server/api.
	b, err := json.Marshal(report)
	if err != nil {
		return
	}

	var parsed struct {
		Summary struct {
			TotalControls   int     `json:"total_controls"`
			CoveragePercent float64 `json:"coverage_percent"`
			Satisfied       int     `json:"satisfied"`
			Partial         int     `json:"partial"`
			NotSatisfied    int     `json:"not_satisfied"`
		} `json:"summary"`
		Controls []struct {
			ControlID    string  `json:"control_id"`
			FleetPercent float64 `json:"fleet_percent"`
		} `json:"controls"`
		Fleet []struct {
			Hostname        string  `json:"hostname"`
			CoveragePercent float64 `json:"coverage_percent"`
			CoveredControls int     `json:"covered_controls"`
			TotalControls   int     `json:"total_controls"`
		} `json:"fleet"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return
	}

	// Donut: satisfied / partial / not_satisfied.
	data.DonutSegments = BuildDonutSegments([]DonutSegment{
		{Label: "Satisfied", Count: parsed.Summary.Satisfied, Color: uifmt.HexGreen},
		{Label: "Partial", Count: parsed.Summary.Partial, Color: uifmt.HexYellow},
		{Label: "Not Satisfied", Count: parsed.Summary.NotSatisfied, Color: uifmt.HexRed},
	})

	// Heatmap: envoys (rows) × controls (columns).
	// Cell color based on the control's fleet-wide coverage for all envoys that have coverage,
	// and the envoy's overall coverage determines row highlight.
	controlIDs := make([]string, len(parsed.Controls))
	controlCoverage := make(map[string]float64, len(parsed.Controls))
	for i, c := range parsed.Controls {
		controlIDs[i] = c.ControlID
		controlCoverage[c.ControlID] = c.FleetPercent
	}
	data.HeatmapColumns = controlIDs

	// Sort fleet by coverage (worst first).
	fleet := parsed.Fleet
	sort.Slice(fleet, func(i, j int) bool {
		return fleet[i].CoveragePercent < fleet[j].CoveragePercent
	})

	// Cap to maxVizItems.
	data.VizTotal = len(fleet)
	if len(fleet) > maxVizItems {
		fleet = fleet[:maxVizItems]
		data.VizCapped = true
	}

	maxCols := len(controlIDs)
	if maxCols > 200 {
		maxCols = 200
	}
	data.HeatmapColMax = maxCols

	// Bucket controls if there are more than maxCols.
	bucketSize := 1
	if len(controlIDs) > maxCols {
		bucketSize = (len(controlIDs) + maxCols - 1) / maxCols
	}

	for _, node := range fleet {
		row := ComplianceHeatmapRow{
			Hostname:    node.Hostname,
			CoveragePct: node.CoveragePercent,
		}

		// Build cells: for each bucket of controls, color by the worst coverage
		// in that bucket, scaled by this envoy's overall coverage.
		for b := 0; b < maxCols; b++ {
			start := b * bucketSize
			end := start + bucketSize
			if end > len(controlIDs) {
				end = len(controlIDs)
			}
			if start >= len(controlIDs) {
				break
			}

			// Average fleet coverage across controls in this bucket.
			var avgPct float64
			count := 0
			for i := start; i < end; i++ {
				avgPct += controlCoverage[controlIDs[i]]
				count++
			}
			if count > 0 {
				avgPct /= float64(count)
			}

			// Scale by this envoy's own coverage ratio.
			envoyScale := node.CoveragePercent / 100.0
			effectivePct := avgPct * envoyScale

			color := uifmt.HexSurface1 // not covered
			if effectivePct >= 95 {
				color = uifmt.HexGreen // satisfied
			} else if effectivePct >= 50 {
				color = uifmt.HexYellow // partial
			} else if effectivePct > 0 {
				color = uifmt.HexRed // not satisfied
			}

			row.Cells = append(row.Cells, ComplianceHeatmapCell{Color: color})
		}

		data.HeatmapRows = append(data.HeatmapRows, row)
	}
}
