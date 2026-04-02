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
	"sort"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/config"

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
		"statusColor":    statusColor,
		"sparklineSVG":      SparklineSVG,
		"sparklineSVGColor": SparklineSVGColor,
		"formatTime":     formatTime,
		"highlightYAML":  highlightYAML,
		"truncUUID":      truncUUID,
		"sub":            func(a, b int) int { return a - b },
		"dict":           dict,
		"pct":            pct,
		"truncCommand":   truncCommand,
		"tracePathTree":  tracePathTree,
		"traceGreenBg":   traceGreenBg,
		"traceGreenBord": traceGreenBord,
		"traceGreenText": traceGreenText,
		"isLastIndex":    func(i, length int) bool { return i == length-1 },
		"inSlice": func(needle string, haystack []string) bool {
			for _, s := range haystack {
				if s == needle {
					return true
				}
			}
			return false
		},
		"maskSecret": func(v any) string {
			s := fmt.Sprintf("%v", v)
			if strings.HasPrefix(s, "secret:") {
				return "secret:****"
			}
			return s
		},
		"sortedKeys": func(m map[string][]string) []string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		},
		"joinStrings": func(ss []string, sep string) string {
			return strings.Join(ss, sep)
		},
		"groupWaiversByFile": groupWaiversByFile,
		"filterConds": func(conds []config.ConditionalTraceEntry, side string) []config.ConditionalTraceEntry {
			var out []config.ConditionalTraceEntry
			for _, c := range conds {
				if c.EvalSide == side {
					out = append(out, c)
				}
			}
			return out
		},
		"strContains": strings.Contains,
		"fwSlug":      fwSlug,
		"fwTotal": func(args ...int) int {
			total := 0
			for _, v := range args {
				total += v
			}
			return total
		},
		"fwPct": func(part, total int) float64 {
			if total == 0 {
				return 0
			}
			return float64(part) / float64(total) * 100
		},
		"cssClass": func(s string) string {
			r := strings.NewReplacer(" ", "-", ".", "-", "(", "", ")", "", "/", "-", ":", "-", ",", "")
			return r.Replace(strings.ToLower(s))
		},
		"riskColor": func(score int) string {
			switch {
			case score >= 70:
				return "danger"
			case score >= 40:
				return "warning"
			case score >= 20:
				return "brand"
			default:
				return "success"
			}
		},
		"lifecycleClass": func(state string) string {
			switch state {
			case "available":
				return "brand"
			case "commissioned", "active":
				return "converged"
			case "decommissioning":
				return "relapsed"
			case "retired":
				return "no-data"
			default:
				return "no-data"
			}
		},
		"verificationClass": func(result string) string {
			switch result {
			case "verified":
				return "converged"
			case "unverifiable":
				return "relapsed"
			case "failed":
				return "failed"
			default:
				return "no-data"
			}
		},
	}
}

// fwSlug maps compliance framework display names to URL slugs.
func fwSlug(name string) string {
	slugs := map[string]string{
		"HIPAA":             "compliance",
		"HITRUST CSF":       "hitrust",
		"SOC 2":             "soc2",
		"PCI DSS v4.0":      "pci",
		"CIS Benchmarks":    "cis",
		"NIST 800-53":       "nist",
		"ISO 27001":         "iso27001",
		"GDPR":              "gdpr",
		"NERC CIP":          "nerccip",
		"Cyber Essentials+": "cyberessentials",
		"IEC 62443":         "iec62443",
		"SOX":               "sox",
		"FINRA":             "finra",
		"MiFID II":          "mifid2",
		"CCSS":               "ccss",
		"FDA 21 CFR Part 11": "fda21cfr11",
		"Gaming Commission":  "gaming",
		"CIS Windows":        "ciswindows",
		"CIS Ubuntu":         "cisubuntu",
		"CIS RHEL":           "cisrhel",
	}
	if s, ok := slugs[name]; ok {
		return s
	}
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
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

// RenderDocs is removed — docs pages now use the regular base template via Render().

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

// statusColor maps a convergence status string to a CSS color class name.
func statusColor(status string) string {
	switch status {
	case "converged":
		return "success"
	case "relapsed", "stale":
		return "warning"
	case "diverged":
		return "danger"
	case "failing":
		return "danger"
	default:
		return "muted"
	}
}

// formatTime renders a time.Time as "2006-01-02 15:04" in the server's local timezone.
func formatTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
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

// traceGreenRamp returns the interpolation factor for the green ramp,
// matching the CLI's greenRamp: light green (#c6f6d5) to dark green (#14532d),
// with a minimum of 5 effective levels so shallow traces stay light.
func traceGreenRamp(depth, total int) float64 {
	if total <= 1 {
		return 0
	}
	effective := total
	if effective < 5 {
		effective = 5
	}
	return float64(depth) / float64(effective-1)
}

// traceGreenBg returns an rgb() background color for a trace level.
// Returns template.CSS so html/template doesn't escape it in style attributes.
func traceGreenBg(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	r := int(240 - t*200)
	g := int(253 - t*170)
	b := int(230 - t*180)
	return template.CSS(fmt.Sprintf("rgb(%d,%d,%d)", r, g, b))
}

// traceGreenBord returns an rgb() border color for a trace level.
// Range: medium green (#5bb370) → dark green (#1a5c30) — readable on light grey.
func traceGreenBord(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	r := int(91 - t*65)
	g := int(179 - t*87)
	b := int(112 - t*64)
	return template.CSS(fmt.Sprintf("rgb(%d,%d,%d)", r, g, b))
}

// traceGreenText returns an rgb() text color for a trace tree level.
// Range: medium green (#3a8a50) → dark green (#1a5030) — high contrast on light grey.
func traceGreenText(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	r := int(58 - t*32)
	g := int(138 - t*58)
	b := int(80 - t*32)
	return template.CSS(fmt.Sprintf("rgb(%d,%d,%d)", r, g, b))
}

// waiverFileGroup is a waiver file with its entries, for template rendering.
type waiverFileGroup struct {
	File    string
	Entries []config.WaiverTraceEntry
}

// groupWaiversByFile groups waiver trace entries by source file for rendering.
func groupWaiversByFile(waivers []config.WaiverTraceEntry) []waiverFileGroup {
	var groups []waiverFileGroup
	idx := make(map[string]int)
	for _, w := range waivers {
		src := w.SourceFile
		if src == "" {
			src = "waivers.vgo"
		}
		if i, ok := idx[src]; ok {
			groups[i].Entries = append(groups[i].Entries, w)
		} else {
			idx[src] = len(groups)
			groups = append(groups, waiverFileGroup{File: src, Entries: []config.WaiverTraceEntry{w}})
		}
	}
	return groups
}

// tracePathTree renders the path inheritance tree as nested HTML with directory
// indentation and proper tree connectors, matching the CLI's renderInheritanceTree.
// Shows "● .live" as root, groups files by directory, includes waiver files.
func tracePathTree(levels []config.InheritanceLevel, waiverFiles []string) template.HTML {
	if len(levels) == 0 {
		return ""
	}

	type dirLevel struct {
		dir      string
		files    []string
		hasChild bool
	}

	// Collect files per directory from inheritance levels.
	dirFiles := make(map[string][]string)
	var dirOrder []string
	dirSeen := make(map[string]bool)
	for _, lvl := range levels {
		if !dirSeen[lvl.Dir] {
			dirSeen[lvl.Dir] = true
			dirOrder = append(dirOrder, lvl.Dir)
		}
		dirFiles[lvl.Dir] = append(dirFiles[lvl.Dir], lvl.File)
	}

	// Add waiver files at their directory level (after common.vgo if present).
	for _, wf := range waiverFiles {
		parts := strings.Split(wf, "/")
		dir := ""
		if len(parts) > 1 {
			dir = strings.Join(parts[:len(parts)-1], "/")
		}
		file := parts[len(parts)-1]
		existing := dirFiles[dir]
		inserted := false
		for i, f := range existing {
			if f == "common.vgo" {
				tail := make([]string, len(existing[i+1:]))
				copy(tail, existing[i+1:])
				dirFiles[dir] = append(existing[:i+1], append([]string{file}, tail...)...)
				inserted = true
				break
			}
		}
		if !inserted {
			dirFiles[dir] = append(existing, file)
		}
		if !dirSeen[dir] {
			dirSeen[dir] = true
			dirOrder = append(dirOrder, dir)
		}
	}

	// Build dirLevels with hasChild flag.
	var dirLevels []dirLevel
	for i, dir := range dirOrder {
		dl := dirLevel{dir: dir, files: dirFiles[dir]}
		if i < len(dirOrder)-1 {
			dl.hasChild = true
		}
		dirLevels = append(dirLevels, dl)
	}

	total := len(dirLevels)
	var b strings.Builder
	indent := func(depth int) string {
		return strings.Repeat("&nbsp;&nbsp;&nbsp;&nbsp;", depth)
	}

	for i, dl := range dirLevels {
		color := traceGreenText(i, total)

		depth := 0
		if dl.dir != "" {
			depth = len(strings.Split(dl.dir, "/"))
		}

		// Print directory name (except root which gets ● stockpile).
		if dl.dir != "" {
			parts := strings.Split(dl.dir, "/")
			dirName := parts[len(parts)-1]
			parentIndent := indent(depth - 1)
			fmt.Fprintf(&b, `<div style="color:%s;font-weight:700;">%s└── %s</div>`, color, parentIndent, template.HTMLEscapeString(dirName))
		}

		// Print root header.
		if i == 0 && dl.dir == "" {
			fmt.Fprintf(&b, `<div style="color:%s;font-weight:700;">● .live</div>`, color)
		}

		// Print files at this level.
		ind := indent(depth)
		for j, file := range dl.files {
			isLast := j == len(dl.files)-1 && !dl.hasChild
			connector := "├── "
			if isLast {
				connector = "└── "
			}
			fmt.Fprintf(&b, `<div style="color:%s;font-weight:700;">%s%s%s</div>`, color, ind, connector, template.HTMLEscapeString(file))
		}

		// Vertical connector to next level.
		if dl.hasChild {
			fmt.Fprintf(&b, `<div style="color:%s;">%s│</div>`, color, ind)
		}
	}

	return template.HTML(b.String())
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
