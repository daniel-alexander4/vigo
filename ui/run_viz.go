// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
)

// RunVizPageData extends RunDetailPageData with waterfall visualization data.
type RunVizPageData struct {
	RunDetailPageData
	WaterfallBars    []WaterfallBar
	WaterfallViewBox string
	MaxDurationMs    int64
	DonutSegments    []DonutSegment
}

// WaterfallBar is one resource's execution bar in the waterfall chart.
type WaterfallBar struct {
	Label        string // "module/resource" truncated
	ModuleName   string
	ResourceName string
	ResourceType string
	ActionTaken  string
	DurationMs   int64
	BarWidth     int    // scaled to max width (600px range)
	Color        string // action color
	Y            int    // vertical position
	ErrorMessage string
}

const waterfallMaxWidth = 600
const waterfallLabelWidth = 220
const waterfallBarHeight = 18
const waterfallBarGap = 2

// RunDetailViz renders the run detail visualization page.
func (h *Handler) RunDetailViz(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid run ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.RunDetailData(idStr)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	vizData := RunVizPageData{RunDetailPageData: data}
	vizData.buildWaterfall()
	vizData.buildDonut()

	h.setCommonWithRequest(&vizData.PageData, r)
	h.renderer.Render(w, "run-viz", vizData)
}

func (vizData *RunVizPageData) buildWaterfall() {
	results := vizData.Results

	// Find max duration for scaling.
	var maxMs int64 = 1
	for _, r := range results {
		if r.DurationMs > maxMs {
			maxMs = r.DurationMs
		}
	}
	vizData.MaxDurationMs = maxMs

	bars := make([]WaterfallBar, len(results))
	for i, r := range results {
		label := r.ModuleName
		if r.ResourceName != "" {
			label += "/" + r.ResourceName
		}
		if len(label) > 35 {
			label = label[:32] + "..."
		}

		barWidth := 0
		if maxMs > 0 && r.DurationMs > 0 {
			barWidth = int(r.DurationMs * waterfallMaxWidth / maxMs)
			if barWidth < 2 {
				barWidth = 2
			}
		}

		color := VizActionColor(r.ActionTaken)
		if r.ErrorMessage != "" {
			color = "#f38ba8" // Red for errors
		}

		bars[i] = WaterfallBar{
			Label:        label,
			ModuleName:   r.ModuleName,
			ResourceName: r.ResourceName,
			ResourceType: r.ResourceType,
			ActionTaken:  r.ActionTaken,
			DurationMs:   r.DurationMs,
			BarWidth:     barWidth,
			Color:        color,
			Y:            i * (waterfallBarHeight + waterfallBarGap),
			ErrorMessage: r.ErrorMessage,
		}
	}

	vizData.WaterfallBars = bars
	totalHeight := len(bars) * (waterfallBarHeight + waterfallBarGap)
	if totalHeight < 40 {
		totalHeight = 40
	}
	vizData.WaterfallViewBox = fmt.Sprintf("0 0 %d %d",
		waterfallLabelWidth+waterfallMaxWidth+80, totalHeight)
}

func (vizData *RunVizPageData) buildDonut() {
	changed := 0
	noop := 0
	failed := 0
	for _, r := range vizData.Results {
		switch {
		case r.ErrorMessage != "":
			failed++
		case r.Changed:
			changed++
		default:
			noop++
		}
	}

	vizData.DonutSegments = BuildDonutSegments([]DonutSegment{
		{Label: "Changed", Count: changed, Color: "#a6e3a1"},
		{Label: "No-op", Count: noop, Color: "#45475a"},
		{Label: "Failed", Count: failed, Color: "#f38ba8"},
	})
}
