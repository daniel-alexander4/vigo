// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
)

// DashboardVizPageData extends DashboardPageData with visualization data.
type DashboardVizPageData struct {
	DashboardPageData
	// Fleet bubble chart (D3).
	FleetBubbleJSON template.JS
	MaxModuleCount  int64
	// Convergence donut.
	ConvergenceDonut []DonutSegment
	// Risk histogram.
	RiskBuckets    []RiskBucket
	RiskBucketMax  int
	VizCapped      bool
	VizTotal       int
}

// FleetBubble is one envoy in the D3 bubble chart.
type FleetBubble struct {
	ID          string  `json:"id"`
	Hostname    string  `json:"hostname"`
	Status      string  `json:"status"`
	ModuleCount int64   `json:"moduleCount"`
	FailurePct  float64 `json:"failurePct"`
	Color       string  `json:"color"`
	Radius      float64 `json:"radius"`
}

// RiskBucket is one bar in the risk histogram.
type RiskBucket struct {
	RangeLabel string
	Count      int
	Color      string
	Height     int // scaled to max
}

// DashboardViz renders the dashboard visualization page.
func (h *Handler) DashboardViz(w http.ResponseWriter, r *http.Request) {
	base := h.dashboardData()

	// Same enrichment as Dashboard handler.
	base.AgentVersion = h.agentVersion
	if base.AgentVersion == "" {
		base.AgentVersion = h.version
	}
	base.AgentPlatforms = h.agentPlatforms()

	sec := h.dataSource.SecurityScanData()
	base.CVECritical = sec.UniqueCritical
	base.CVEHigh = sec.UniqueHigh
	base.UniqueCVEs = sec.UniqueCVEs
	base.HardeningScore = sec.LynisAvgScore
	base.RootkitWarnings = sec.RootkitWarning
	base.MalwareDetected = sec.ClamavInfected
	base.IntegrityIssues = sec.AideChanged

	stds := h.computeStandardsSummary()
	base.StandardsPct = stds.Pct
	base.SatisfiedControls = stds.Satisfied
	base.WaivedControls = stds.Waived
	base.PartialControls = stds.Partial
	base.NotSatisfiedControls = stds.NotSatisfied
	base.TotalControls = stds.Total

	rsk := h.dataSource.RiskData(h.baseInterval)
	base.RiskAvgScore = rsk.AvgScore
	base.RiskCritical = rsk.Critical
	base.RiskHigh = rsk.High
	base.RiskMedium = rsk.Medium
	base.RiskLow = rsk.Low
	base.RiskTopEnvoys = rsk.TopRisks
	base.RiskSparklineData = rsk.SparklineData
	base.RiskSparklineMin = rsk.SparklineMin
	base.RiskSparklineMax = rsk.SparklineMax

	if h.swarmTracker != nil {
		base.SwarmFiles = h.swarmFiles()
	}

	data := DashboardVizPageData{DashboardPageData: base}
	data.buildBubbles()
	data.buildConvergenceDonut()
	data.buildRiskHistogram()

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "dashboard-viz", data)
}

// DashboardVizFleetFragment renders the fleet viz fragment for HTMX auto-refresh.
func (h *Handler) DashboardVizFleetFragment(w http.ResponseWriter, r *http.Request) {
	base := h.dashboardData()

	rsk := h.dataSource.RiskData(h.baseInterval)
	base.RiskTopEnvoys = rsk.TopRisks
	base.RiskAvgScore = rsk.AvgScore
	base.RiskCritical = rsk.Critical
	base.RiskHigh = rsk.High
	base.RiskMedium = rsk.Medium
	base.RiskLow = rsk.Low

	data := DashboardVizPageData{DashboardPageData: base}
	data.buildBubbles()
	data.buildConvergenceDonut()
	data.buildRiskHistogram()

	h.renderer.RenderFragment(w, "dashboard-viz", "dashboard-viz-fleet-fragment", data)
}

func (data *DashboardVizPageData) buildBubbles() {
	rows := data.FleetRows

	// Cap to 50 (worst first — highest failure %).
	data.VizTotal = len(rows)
	if len(rows) > maxVizItems {
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].FailurePct > rows[j].FailurePct
		})
		rows = rows[:maxVizItems]
		data.VizCapped = true
	}

	// Find max module count for radius scaling.
	var maxMod int64 = 1
	for _, r := range rows {
		if r.ModuleCount > maxMod {
			maxMod = r.ModuleCount
		}
	}
	data.MaxModuleCount = maxMod

	bubbles := make([]FleetBubble, len(rows))
	for i, r := range rows {
		radius := 8.0 + float64(r.ModuleCount)/float64(maxMod)*20.0
		bubbles[i] = FleetBubble{
			ID:          r.ID,
			Hostname:    r.Hostname,
			Status:      r.Status,
			ModuleCount: r.ModuleCount,
			FailurePct:  r.FailurePct,
			Color:       VizStatusColor(r.Status),
			Radius:      radius,
		}
	}

	b, _ := json.Marshal(bubbles)
	data.FleetBubbleJSON = template.JS(b)
}

func (data *DashboardVizPageData) buildConvergenceDonut() {
	data.ConvergenceDonut = BuildDonutSegments([]DonutSegment{
		{Label: "Converged", Count: data.Converged, Color: "#a6e3a1"},
		{Label: "Relapsed", Count: data.Relapsed, Color: "#f9e2af"},
		{Label: "Diverged", Count: data.Diverged, Color: "#fab387"},
		{Label: "Failed", Count: data.CompFailed, Color: "#f38ba8"},
		{Label: "Offline", Count: data.Offline, Color: "#6c7086"},
	})
}

func (data *DashboardVizPageData) buildRiskHistogram() {
	// Bucket risk scores into 10-point ranges.
	bucketCounts := make([]int, 10) // 0-9, 10-19, ..., 90-100
	for _, e := range data.RiskTopEnvoys {
		idx := e.Score / 10
		if idx > 9 {
			idx = 9
		}
		bucketCounts[idx]++
	}

	maxCount := 1
	for _, c := range bucketCounts {
		if c > maxCount {
			maxCount = c
		}
	}
	data.RiskBucketMax = maxCount

	maxBarHeight := 120
	buckets := make([]RiskBucket, 10)
	for i := range buckets {
		height := 0
		if maxCount > 0 && bucketCounts[i] > 0 {
			height = bucketCounts[i] * maxBarHeight / maxCount
			if height < 2 {
				height = 2
			}
		}

		color := "#a6e3a1" // green (low risk)
		switch {
		case i >= 7:
			color = "#f38ba8" // red (critical)
		case i >= 4:
			color = "#fab387" // peach (high)
		case i >= 2:
			color = "#f9e2af" // yellow (medium)
		}

		buckets[i] = RiskBucket{
			RangeLabel: fmt.Sprintf("%d-%d", i*10, i*10+9),
			Count:      bucketCounts[i],
			Color:      color,
			Height:     height,
		}
	}
	data.RiskBuckets = buckets
}
