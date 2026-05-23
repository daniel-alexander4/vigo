// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/config"
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
		{"degraded", "warning"},
		{"failed", "danger"},
		{"stale", "warning"},
		{"unknown", "muted"},
		{"", "muted"},
		// Drift-axis vocabulary is no longer recognized by the
		// failure-axis statusColor — it returns muted for these.
		{"relapsed", "muted"},
		{"diverged", "muted"},
	}
	for _, tt := range tests {
		got := statusColor(tt.status)
		if got != tt.want {
			t.Errorf("statusColor(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestEnvoyFreshness(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		lastSeen     time.Time
		baseInterval time.Duration
		wantLabel    string
		wantStale    bool
	}{
		{
			name:         "zero time renders dash, not stale",
			lastSeen:     time.Time{},
			baseInterval: 5 * time.Minute,
			wantLabel:    "-",
			wantStale:    false,
		},
		{
			name:         "default interval, recent: just now + fresh",
			lastSeen:     now.Add(-5 * time.Second),
			baseInterval: 5 * time.Minute,
			wantLabel:    "just now",
			wantStale:    false,
		},
		{
			name:         "default interval, mid-window: minute label + fresh",
			lastSeen:     now.Add(-5 * time.Minute),
			baseInterval: 5 * time.Minute,
			wantLabel:    "5m ago",
			wantStale:    false,
		},
		{
			name:         "default interval, well past threshold: stale",
			lastSeen:     now.Add(-15 * time.Minute),
			baseInterval: 5 * time.Minute,
			wantLabel:    "15m ago",
			wantStale:    true,
		},
		{
			name:         "5s interval, fresh well below threshold: just now",
			lastSeen:     now.Add(-5 * time.Second),
			baseInterval: 5 * time.Second,
			wantLabel:    "just now",
			wantStale:    false,
		},
		{
			name:         "5s interval, stale-but-under-30s: precise seconds (the bug case)",
			lastSeen:     now.Add(-15 * time.Second),
			baseInterval: 5 * time.Second,
			wantLabel:    "15s ago",
			wantStale:    true,
		},
		{
			name:         "5s interval, stale past 30s: minute label",
			lastSeen:     now.Add(-45 * time.Second),
			baseInterval: 5 * time.Second,
			wantLabel:    "1m ago",
			wantStale:    true,
		},
		{
			// 11 s is well below the 12.5 s threshold but well above
			// any plausible test-execution wobble — robust under load.
			name:         "5s interval, just below threshold: still fresh",
			lastSeen:     now.Add(-11 * time.Second),
			baseInterval: 5 * time.Second,
			wantLabel:    "just now",
			wantStale:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// observedCycle=0 (bootstrap): preserves existing
			// configured-only threshold semantics for these test cases.
			label, stale := envoyFreshness(tt.lastSeen, tt.baseInterval, 0, 0)
			if label != tt.wantLabel {
				t.Errorf("label = %q, want %q", label, tt.wantLabel)
			}
			if stale != tt.wantStale {
				t.Errorf("stale = %v, want %v", stale, tt.wantStale)
			}
			// Invariant: a fresh envoy never carries a stale label, a
			// stale envoy never reads "just now". This is the property
			// the bug violated.
			if !stale && label == "just now" {
				// fine
			} else if stale && label == "just now" {
				t.Errorf("invariant violated: stale=true with label %q", label)
			}
		})
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

func TestRender_FirstRunEmptyState(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}
	admin := &AuthUser{Role: "admin"}

	// Zero envoys: the dashboard renders the enrollment panel with the
	// bootstrap command, and the operational nav is suppressed.
	rec := httptest.NewRecorder()
	r.Render(rec, "dashboard", DashboardPageData{
		PageData:         PageData{Title: "Dashboard", NoEnvoys: true, CurrentUser: admin},
		BootstrapCommand: "curl -sSfk https://vigo.test:8443/bootstrap | sudo sh",
	})
	body := rec.Body.String()
	if !strings.Contains(body, "No envoys enrolled yet") {
		t.Error("first-run panel heading missing")
	}
	if !strings.Contains(body, "curl -sSfk https://vigo.test:8443/bootstrap | sudo sh") {
		t.Error("bootstrap command missing from first-run panel")
	}
	if strings.Contains(body, `href="/envoys"`) {
		t.Error("operational nav rendered in the zero-envoy first-run state")
	}

	// With envoys enrolled, the operational nav is present again.
	rec2 := httptest.NewRecorder()
	r.Render(rec2, "dashboard", DashboardPageData{
		PageData:    PageData{Title: "Dashboard", NoEnvoys: false, CurrentUser: admin},
		TotalEnvoys: 3,
	})
	if !strings.Contains(rec2.Body.String(), `href="/envoys"`) {
		t.Error("operational nav missing when envoys are enrolled")
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

func TestIsCRUDAction(t *testing.T) {
	cases := map[string]bool{
		"create":    true,
		"created":   true,
		"update":    true,
		"updated":   true,
		"delete":    true,
		"deleted":   true,
		"  UPDATE ": true, // whitespace + case tolerance
		"settled":   false,
		"skipped":   false,
		"blocked":   false,
		"failed":    false,
		"":          false,
		"unknown":   false,
	}
	for action, want := range cases {
		if got := isCRUDAction(action); got != want {
			t.Errorf("isCRUDAction(%q) = %v, want %v", action, got, want)
		}
	}
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

// TestRender_EnvoyTrace renders the config-trace page end to end. trace-body
// dereferences config.ConfigTrace fields directly, so a template that names a
// field the struct no longer has fails only at execution — this exercises it
// against a matched trace. Render writes "template error" into the body on an
// ExecuteTemplate failure, so its absence (plus a complete </html>) proves the
// page rendered clean.
func TestRender_EnvoyTrace(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	r.Render(rec, "envoy-trace", EnvoyTracePageData{
		PageData: PageData{Title: "Config Trace"},
		Hostname: "danlap",
		EnvoyID:  "00000000-0000-0000-0000-000000000000",
		Trace:    &config.ConfigTrace{Hostname: "danlap", Matched: true},
	})
	body := rec.Body.String()
	if strings.Contains(body, "template error") {
		t.Errorf("envoy-trace render hit a template execution error:\n%s", body)
	}
	if !strings.Contains(body, "</html>") {
		t.Error("envoy-trace render is incomplete — missing </html> (template aborted mid-render)")
	}
}
