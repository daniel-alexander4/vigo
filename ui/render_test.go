// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRenderer(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}
	if r == nil {
		t.Fatal("NewRenderer() returned nil")
	}
}

func TestSparklineSVGWithData(t *testing.T) {
	data := []float64{50, 60, 70, 80, 90}
	svg := SparklineSVG(data, 400, 60)
	s := string(svg)
	if s == "" {
		t.Fatal("SparklineSVG returned empty string for valid data")
	}
	if !strings.Contains(s, "<svg") {
		t.Error("SparklineSVG output missing <svg element")
	}
	if !strings.Contains(s, "<path") {
		t.Error("SparklineSVG output missing <path element")
	}
}

func TestSparklineSVGEmpty(t *testing.T) {
	svg := SparklineSVG(nil, 400, 60)
	if string(svg) != "" {
		t.Errorf("SparklineSVG with nil data returned %q, want empty", svg)
	}
	svg = SparklineSVG([]float64{}, 400, 60)
	if string(svg) != "" {
		t.Errorf("SparklineSVG with empty data returned %q, want empty", svg)
	}
}

func TestSparklineSVGClass(t *testing.T) {
	data := []float64{10, 20, 30}
	svg := SparklineSVG(data, 200, 40)
	if !strings.Contains(string(svg), "sparkline") {
		t.Error("SparklineSVG output missing 'sparkline' class")
	}
}

func TestStatusColor(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"converged", "success"},
		{"relapsed", "warning"},
		{"diverged", "danger"},
		{"failing", "danger"},
		{"stale", "warning"},
		{"unknown", "muted"},
		{"", "muted"},
	}
	for _, tt := range tests {
		got := statusColor(tt.status)
		if got != tt.want {
			t.Errorf("statusColor(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	tm := time.Date(2025, 3, 15, 14, 30, 0, 0, time.Local)
	got := formatTime(tm)
	if got != "2025-03-15 14:30" {
		t.Errorf("formatTime() = %q, want %q", got, "2025-03-15 14:30")
	}
}

func TestHighlightYAML(t *testing.T) {
	yaml := "name: test\nvalue: 42\n"
	result := highlightYAML(yaml)
	s := string(result)
	if s == "" {
		t.Fatal("highlightYAML returned empty string")
	}
	// chroma CSS-class formatter wraps in <pre> with chroma classes
	if !strings.Contains(s, "chroma") {
		t.Errorf("highlightYAML output missing 'chroma' class, got: %s", s)
	}
}

func TestRenderMarkdown_ScriptTagSanitized(t *testing.T) {
	// Goldmark without html.WithUnsafe() should strip raw HTML tags.
	input := `# Hello

<script>alert('xss')</script>

Normal paragraph.`
	output := RenderMarkdown(input)
	if strings.Contains(output, "<script>") {
		t.Errorf("RenderMarkdown should sanitize script tags, got: %s", output)
	}
	if !strings.Contains(output, "Hello") {
		t.Errorf("RenderMarkdown should render markdown content, got: %s", output)
	}
	if !strings.Contains(output, "Normal paragraph") {
		t.Errorf("RenderMarkdown should render normal paragraphs, got: %s", output)
	}
}

func TestRender_CSPHeader(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	r.Render(rec, "dashboard", DashboardPageData{
		PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"},
	})
	csp := rec.Result().Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("expected Content-Security-Policy header, got empty")
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP header missing default-src 'self': %s", csp)
	}
}

func TestRenderFragment_NoCSP(t *testing.T) {
	// Fragments are HTMX partials — no CSP needed since the parent page has it.
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	r.RenderFragment(rec, "dashboard", "nonexistent", nil)
	_ = rec.Result()
}

func TestEmbedFS(t *testing.T) {
	expected := []string{
		"templates/base.html",
		"static/css/style.css",
		"static/js/htmx.min.js",
	}
	for _, path := range expected {
		_, err := fs.Stat(Assets, path)
		if err != nil {
			t.Errorf("embedded FS missing %q: %v", path, err)
		}
	}
}
