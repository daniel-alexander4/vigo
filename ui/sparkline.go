// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"html/template"
	"math"
	"strings"
)

// SparklineSVG generates an inline SVG sparkline from data points.
// Values are scaled to the actual data range with padding so lines
// never clip at the SVG edges. Includes a translucent area fill.
func SparklineSVG(values []float64, width, height int) template.HTML {
	min, max := dataBounds(values)
	return sparklineSVG(values, width, height, "var(--color-success)", min, max)
}

// SparklineSVGColor generates a sparkline with a custom stroke color.
func SparklineSVGColor(values []float64, width, height int, color string) template.HTML {
	min, max := dataBounds(values)
	return sparklineSVG(values, width, height, color, min, max)
}

// SparklineSVGBounded generates a sparkline with a fixed y-axis range.
// Use for bounded metrics (percents, 0-100 risk scores) where adaptive
// scaling visually amplifies noise: a line wobbling between 98 and 100
// looks dramatic under auto-scale but correctly flat near the top under
// a fixed 0-100 axis. Also makes two sparklines visually comparable.
func SparklineSVGBounded(values []float64, width, height int, color string, yMin, yMax int) template.HTML {
	return sparklineSVG(values, width, height, color, float64(yMin), float64(yMax))
}

func dataBounds(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 1
	}
	min, max := values[0], values[0]
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

func sparklineSVG(values []float64, width, height int, color string, minVal, maxVal float64) template.HTML {
	if len(values) < 2 {
		return ""
	}

	// Ensure a minimum range so flat lines don't sit at the edge.
	dataRange := maxVal - minVal
	if dataRange < 1 {
		dataRange = 1
	}

	// Padding: top and bottom so the line never touches the SVG edge.
	padTop := 6.0
	padBottom := 6.0
	plotH := float64(height) - padTop - padBottom

	n := len(values)
	stepX := float64(width) / float64(n-1)

	// Build polyline points.
	var points []string
	for i, v := range values {
		x := float64(i) * stepX
		// Scale value into the padded plot area.
		normalized := (v - minVal) / dataRange
		y := padTop + plotH - (normalized * plotH)
		points = append(points, fmt.Sprintf("%.1f,%.1f", x, y))
	}

	polyline := strings.Join(points, " ")

	// Area fill: close the path along the bottom edge.
	lastX := float64(n-1) * stepX
	bottomY := padTop + plotH
	areaPath := fmt.Sprintf("M0,%.1f L%s L%.1f,%.1f Z", bottomY, polyline, lastX, bottomY)

	// Y-axis scale labels (min and max).
	labelMin := formatScaleLabel(minVal)
	labelMax := formatScaleLabel(maxVal)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" class="sparkline" preserveAspectRatio="none">`, width, height)

	// Area fill.
	fmt.Fprintf(&b, `<path d="%s" fill="%s" opacity="0.1"/>`, areaPath, color)

	// Line.
	fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5" stroke-linejoin="round"/>`, polyline, color)

	// Scale labels (top-right = max, bottom-right = min).
	fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="9" fill="#6b7280" text-anchor="end" font-family="-apple-system,sans-serif" dominant-baseline="hanging">%s</text>`,
		width-2, padTop, labelMax)
	fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="9" fill="#6b7280" text-anchor="end" font-family="-apple-system,sans-serif">%s</text>`,
		width-2, math.Max(padTop+plotH, float64(height)-2), labelMin)

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// formatScaleLabel formats a number for sparkline Y-axis labels.
func formatScaleLabel(v float64) string {
	if v == float64(int(v)) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
