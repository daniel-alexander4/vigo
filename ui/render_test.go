// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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

func TestEnvoyStale(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		lastSeen     time.Time
		baseInterval time.Duration
		wantStale    bool
	}{
		{
			name:         "zero time is never stale",
			lastSeen:     time.Time{},
			baseInterval: 5 * time.Minute,
			wantStale:    false,
		},
		{
			name:         "default interval, recent: fresh",
			lastSeen:     now.Add(-5 * time.Second),
			baseInterval: 5 * time.Minute,
			wantStale:    false,
		},
		{
			name:         "default interval, mid-window: fresh",
			lastSeen:     now.Add(-5 * time.Minute),
			baseInterval: 5 * time.Minute,
			wantStale:    false,
		},
		{
			name:         "default interval, well past threshold: stale",
			lastSeen:     now.Add(-15 * time.Minute),
			baseInterval: 5 * time.Minute,
			wantStale:    true,
		},
		{
			name:         "5s interval, fresh well below threshold",
			lastSeen:     now.Add(-5 * time.Second),
			baseInterval: 5 * time.Second,
			wantStale:    false,
		},
		{
			// 15s at a 5s interval is past the bare 2.5×=12.5s threshold
			// but under the 30s MinStaleThreshold floor (0.72.7), so the
			// envoy stays fresh — that floor is the whole point at fast
			// cadences: it tolerates missed beats without flapping offline.
			name:         "5s interval, 15s but under 30s floor: still fresh",
			lastSeen:     now.Add(-15 * time.Second),
			baseInterval: 5 * time.Second,
			wantStale:    false,
		},
		{
			name:         "5s interval, stale past 30s floor",
			lastSeen:     now.Add(-45 * time.Second),
			baseInterval: 5 * time.Second,
			wantStale:    true,
		},
		{
			// 11 s is well below the 12.5 s threshold but well above
			// any plausible test-execution wobble — robust under load.
			name:         "5s interval, just below threshold: still fresh",
			lastSeen:     now.Add(-11 * time.Second),
			baseInterval: 5 * time.Second,
			wantStale:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// observedCycle=0 (bootstrap): preserves existing
			// configured-only threshold semantics for these test cases.
			if stale := EnvoyStale(tt.lastSeen, tt.baseInterval, 0, 0); stale != tt.wantStale {
				t.Errorf("stale = %v, want %v", stale, tt.wantStale)
			}
		})
	}
}

func TestFriendlyDurationL(t *testing.T) {
	tests := []struct {
		locale string
		d      time.Duration
		want   string
	}{
		{"", 45 * time.Second, "45s"},
		{"en", 15 * time.Minute, "15m"},
		{"en", 90 * time.Second, "1m30s"},
		{"en", 2 * time.Hour, "2h"},
		{"fr", 45 * time.Second, "45 s"},
		{"fr", 15 * time.Minute, "15 min"},
		{"fr", 90 * time.Second, "1 min 30 s"},
		{"fr", 2 * time.Hour, "2 h"},
		{"fr", 90 * time.Minute, "1 h 30 min"},
		// A locale with no catalog falls back to English — proves a new
		// language needs only catalog entries, no code branch here.
		{"zz", 15 * time.Minute, "15m"},
		// A bundled locale resolves from its catalog (de added 0.76.x).
		{"de", 15 * time.Minute, "15 Min."},
	}
	for _, tt := range tests {
		if got := friendlyDurationL(tt.locale, tt.d); got != tt.want {
			t.Errorf("friendlyDurationL(%q, %v) = %q, want %q", tt.locale, tt.d, got, tt.want)
		}
	}
}

func TestTimeAgoL(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		locale string
		when   time.Time
		want   string
	}{
		{"zero renders dash", "en", time.Time{}, "-"},
		{"en just now", "en", now.Add(-5 * time.Second), "just now"},
		{"en minutes", "en", now.Add(-5 * time.Minute), "5m ago"},
		{"en hours", "en", now.Add(-3 * time.Hour), "3h ago"},
		{"en yesterday", "en", now.Add(-30 * time.Hour), "yesterday"},
		{"fr just now", "fr", now.Add(-5 * time.Second), "à l'instant"},
		{"fr minutes", "fr", now.Add(-5 * time.Minute), "il y a 5 min"},
		{"fr hours", "fr", now.Add(-3 * time.Hour), "il y a 3 h"},
		{"fr yesterday", "fr", now.Add(-30 * time.Hour), "hier"},
		// Unknown locale → English fallback (data-only extensibility).
		{"unknown locale falls back", "zz", now.Add(-5 * time.Minute), "5m ago"},
		// Bundled locale resolves from its catalog (de added 0.76.x).
		{"de minutes", "de", now.Add(-5 * time.Minute), "vor 5 Min."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeAgoL(tt.locale, tt.when); got != tt.want {
				t.Errorf("timeAgoL(%q) = %q, want %q", tt.locale, got, tt.want)
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
	// The de-inline work (static u-* sweep + dynamic class conversion) exists
	// precisely so this holds: no 'unsafe-inline' anywhere, script or style.
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP must not contain 'unsafe-inline': %s", csp)
	}
	if !strings.Contains(csp, "style-src 'self'") {
		t.Errorf("CSP missing locked style-src 'self': %s", csp)
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

// TestTracePathTree_ThemedColor pins that the Path Inheritance tree colors
// itself the way the rest of the trace page does: each line carries the
// .trace-path-line class plus a .tgt-N bucket class. The .tgt-N class sets the
// --tc-* custom-property pair the .trace-path-line rule resolves per theme — so
// the coloring is CSP-safe (no inline style) yet still themed. The bug it
// guards: the gradient is a custom-property pair, not a color, and must reach
// the page as a class, never an inline style= attribute (style-src 'self').
func TestTracePathTree_ThemedColor(t *testing.T) {
	out := string(tracePathTree([]config.InheritanceLevel{
		{Dir: "", File: "common.vgo"},
		{Dir: "alexander4", File: "common.vgo"},
	}, nil))
	if out == "" {
		t.Fatal("tracePathTree returned empty for non-empty levels")
	}
	if !strings.Contains(out, "trace-path-line") {
		t.Errorf("tracePathTree output missing the .trace-path-line class:\n%s", out)
	}
	if !strings.Contains(out, "tgt-") {
		t.Errorf("tracePathTree output missing the .tgt-N gradient bucket class:\n%s", out)
	}
	if strings.Contains(out, "style=") {
		t.Errorf("tracePathTree must not emit an inline style= attribute (CSP style-src 'self'):\n%s", out)
	}
}
