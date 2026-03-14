// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"fmt"
	"html/template"
	"strings"
)

// SparklineSVG generates an inline SVG sparkline from data points.
// width and height are in pixels. values should be percentages (0-100).
// Returns empty string for empty or single-element data.
func SparklineSVG(values []float64, width, height int) template.HTML {
	if len(values) < 2 {
		return ""
	}

	n := len(values)
	stepX := float64(width) / float64(n-1)

	var points []string
	for i, v := range values {
		x := float64(i) * stepX
		// Invert Y because SVG Y-axis goes downward
		y := float64(height) - (v / 100.0 * float64(height))
		points = append(points, fmt.Sprintf("%.1f,%.1f", x, y))
	}

	path := fmt.Sprintf("M%s", strings.Join(points, " L"))

	svg := fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" class="sparkline" preserveAspectRatio="none">`+
			`<path d="%s" fill="none" stroke="var(--color-success)" stroke-width="1.5"/>`+
			`</svg>`,
		width, height, path,
	)

	return template.HTML(svg)
}
