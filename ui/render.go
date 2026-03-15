// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
)

// Renderer holds parsed templates and renders them to HTTP responses.
type Renderer struct {
	pages map[string]*template.Template
}

// funcMap returns the template FuncMap used across all templates.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"statusColor":   statusColor,
		"sparklineSVG":  SparklineSVG,
		"formatTime":    formatTime,
		"highlightYAML": highlightYAML,
		"truncUUID":     truncUUID,
		"sub":           func(a, b int) int { return a - b },
		"dict":          dict,
		"pct":           pct,
		"truncCommand":  truncCommand,
	}
}

// NewRenderer parses all embedded templates and returns a ready Renderer.
// Each page template is parsed independently on top of a cloned base+partials
// template set, avoiding the name collision pitfall.
func NewRenderer() (*Renderer, error) {
	// Parse base layout and all partials into a shared template set.
	base, err := template.New("").Funcs(funcMap()).ParseFS(Assets,
		"templates/base.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsing base templates: %w", err)
	}

	// Discover page templates.
	pageFiles, err := fs.Glob(Assets, "templates/pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("globbing page templates: %w", err)
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, pf := range pageFiles {
		// Clone the base template set for each page.
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning base for %s: %w", pf, err)
		}

		// Parse the page template on top of the cloned base.
		_, err = clone.ParseFS(Assets, pf)
		if err != nil {
			return nil, fmt.Errorf("parsing page %s: %w", pf, err)
		}

		// Extract page name from path: "templates/pages/dashboard.html" -> "dashboard"
		name := strings.TrimPrefix(pf, "templates/pages/")
		name = strings.TrimSuffix(name, ".html")
		pages[name] = clone
	}

	return &Renderer{pages: pages}, nil
}

// Render executes the base template for the named page with the given data.
func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		slog.Error("template execution error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// RenderAdmin executes the admin-base template for admin pages.
func (r *Renderer) RenderAdmin(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
	if err := tmpl.ExecuteTemplate(w, "admin-base", data); err != nil {
		slog.Error("template execution error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// RenderDocs executes the docs-base template for standalone documentation pages.
func (r *Renderer) RenderDocs(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
	if err := tmpl.ExecuteTemplate(w, "docs-base", data); err != nil {
		slog.Error("template execution error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// RenderFragment executes a named template definition (not a full page) for HTMX fragment responses.
func (r *Renderer) RenderFragment(w http.ResponseWriter, page, fragment string, data any) {
	tmpl, ok := r.pages[page]
	if !ok {
		slog.Error("template not found for fragment", "page", page, "fragment", fragment)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := tmpl.ExecuteTemplate(w, fragment, data); err != nil {
		slog.Error("fragment execution error", "page", page, "fragment", fragment, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// statusColor maps a compliance status string to a CSS color class name.
func statusColor(status string) string {
	switch status {
	case "compliant":
		return "success"
	case "drifted", "stale":
		return "warning"
	case "failing":
		return "danger"
	default:
		return "muted"
	}
}

// formatTime renders a time.Time as "2006-01-02 15:04" format.
func formatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}

// highlightYAML applies chroma syntax highlighting to YAML source code.
// Returns HTML with CSS classes for styling (uses monokai style).
func highlightYAML(source string) template.HTML {
	lexer := lexers.Get("yaml")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := chromahtml.New(chromahtml.WithClasses(true))
	style := styles.Get("monokai")

	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
	}

	return template.HTML(buf.String())
}

// truncCommand returns a command string truncated to 50 chars for display.
func truncCommand(cmd string) string {
	// Show only the first line for multi-line scripts.
	if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
		cmd = cmd[:idx] + "..."
	}
	if len(cmd) > 50 {
		return cmd[:47] + "..."
	}
	return cmd
}

// truncUUID returns the first 8 characters of a UUID string.
func truncUUID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// dict creates a map from key-value pairs for use in templates.
func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs)-1; i += 2 {
		key, ok := pairs[i].(string)
		if ok {
			m[key] = pairs[i+1]
		}
	}
	return m
}

// pct calculates a percentage: (numerator / denominator) * 100.
// Returns 0 if denominator is 0.
func pct(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

// markdownRenderer is a singleton goldmark instance with chroma syntax highlighting.
// html.WithUnsafe() intentionally omitted — raw HTML in markdown is sanitized
// to prevent XSS (SEC-14), particularly from AI chat responses.
var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(
			highlighting.WithStyle("monokai"),
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		),
	),
)

// RenderMarkdown converts markdown source to HTML with syntax-highlighted code blocks.
func RenderMarkdown(source string) string {
	var buf bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &buf); err != nil {
		return "<pre>" + template.HTMLEscapeString(source) + "</pre>"
	}
	return buf.String()
}
