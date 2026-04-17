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
		"timeAgo":        timeAgo,
		"sortHeader":     sortHeader,
		"pctColor":       pctColor,
		"pctColorInv":    pctColorInv,
		"pctColorBg":     pctColorBg,
		"pctColorBgInv":  pctColorBgInv,
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
		"humanDuration": func(start, end time.Time) string {
				if start.IsZero() || end.IsZero() {
					return ""
				}
				d := end.Sub(start)
				if d < 0 {
					return ""
				}
				switch {
				case d < time.Second:
					return fmt.Sprintf("%dms", d.Milliseconds())
				case d < time.Minute:
					return fmt.Sprintf("%ds", int(d.Seconds()))
				case d < time.Hour:
					return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
				default:
					return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
				}
			},
			"humanMs": func(ms int64) string {
				d := time.Duration(ms) * time.Millisecond
				switch {
				case d < time.Second:
					return fmt.Sprintf("%dms", ms)
				case d < time.Minute:
					return fmt.Sprintf("%ds", int(d.Seconds()))
				case d < time.Hour:
					return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
				default:
					return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
				}
			},
			"swarmStatusClass": func(status string) string {
			switch status {
			case "source":
				// Green — the seeder has the blob by construction.
				// Distinct label rendered in the template; the pill
				// class shares converged's green palette so the row
				// reads "done" at a glance alongside complete.
				return "converged"
			case "complete":
				return "converged"
			case "cached":
				return "cached"
			case "downloading":
				return "relapsed"
			case "pending":
				return "no-data"
			case "skipped":
				return "failed"
			case "revoking":
				// Yellow: seed is revoked, envoy still has the blob.
				return "relapsed"
			case "revoked":
				// Green: envoy has successfully evicted the blob.
				return "converged"
			default:
				return "no-data"
			}
		},
		"swarmFileStatusClass": func(status string) string {
			switch status {
			case "complete":
				return "converged"
			case "distributing":
				return "relapsed"
			case "revoked":
				return "no-data"
			default:
				return "no-data"
			}
		},
		"bucketColor": func(status string) string {
			switch status {
			case "complete":
				return "#a6e3a1" // Catppuccin Green
			case "downloading":
				return "#f9e2af" // Catppuccin Yellow
			case "skipped":
				return "#f38ba8" // Catppuccin Red
			default: // missing
				return "#45475a" // Catppuccin Surface1
			}
		},
		"sourceColor": func(source string, colors map[string]string) string {
			if c, ok := colors[source]; ok {
				return c
			}
			return "#a0aec0"
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"add": func(a, b int) int {
			return a + b
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
		"osLabel": func(os string) string {
			switch os {
			case "darwin":
				return "macOS"
			case "linux":
				return "Linux"
			case "windows":
				return "Windows"
			case "freebsd":
				return "FreeBSD"
			case "openbsd":
				return "OpenBSD"
			case "netbsd":
				return "NetBSD"
			case "illumos":
				return "illumos"
			default:
				return os
			}
		},
		"vizStatusColor":     VizStatusColor,
		"vizActionColor":     VizActionColor,
		"vizComplianceColor": VizComplianceColor,
		"donutArc": func(cx, cy, r, startAngle, endAngle float64) string {
			return DonutArc(cx, cy, r, startAngle, endAngle)
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
		"CIS Ubuntu":                "cisubuntu",
		"CIS RHEL":                  "cisrhel",
		"ABA Cybersecurity":         "abacyber",
		"NY DFS Cyber (23 NYCRR 500)": "nydfscyber",
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
	case "degraded":
		return "warning"
	case "diverged":
		return "danger"
	case "failed":
		return "danger"
	default:
		return "muted"
	}
}

// formatTime renders a time.Time as "2006-01-02 15:04" in the server's local timezone.
func formatTime(t time.Time) string {
	local := t.Local()
	// Show seconds when the timestamp is less than 60 seconds old so operators
	// can see sub-minute freshness on fast check-in intervals.
	if time.Since(local) < 60*time.Second {
		return local.Format("2006-01-02 15:04:05")
	}
	return local.Format("2006-01-02 15:04")
}

// timeAgo renders a time.Time as a human-readable relative string ("2m ago", "3h ago").
// Returns "-" for zero times. Used in table columns where recency matters more than
// exact timestamps (the absolute time is shown via title tooltip in the template).
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < 30*time.Second:
		return "just now"
	case d < 90*time.Second:
		return "1m ago"
	case d < 60*time.Minute:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 90*time.Minute:
		return "1h ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// toFloat64 converts any numeric type to float64 for template functions.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	default:
		return 0
	}
}

// pctColor returns an HSL color string for a 0-100 percentage where 0% = green
// and 100% = red. Used for failure ratios, risk scores, and drift rates.
// Hue interpolates linearly: 120° (green) at 0% → 0° (red) at 100%.
// Accepts int, int64, float64, or any numeric type.
func pctColor(v any) template.CSS {
	pct := toFloat64(v)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	hue := 120.0 * (1.0 - pct/100.0)
	return template.CSS(fmt.Sprintf("hsl(%.0f, 72%%, 40%%)", hue))
}

// pctColorInv is the inverse: 0% = red, 100% = green. Used for convergence
// percentage, compliance coverage, and other "higher is better" metrics.
func pctColorInv(v any) template.CSS {
	return pctColor(100.0 - toFloat64(v))
}

// pctColorBg returns a light HSL background color for a 0-100 percentage.
// Same hue mapping as pctColor but with high lightness for table row tinting.
func pctColorBg(v any) template.CSS {
	pct := toFloat64(v)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	hue := 120.0 * (1.0 - pct/100.0)
	return template.CSS(fmt.Sprintf("hsl(%.0f, 60%%, 95%%)", hue))
}

// pctColorBgInv returns a light background: 0% = red bg, 100% = green bg.
func pctColorBgInv(v any) template.CSS {
	return pctColorBg(100.0 - toFloat64(v))
}

// sortHeader renders a sortable <th> element. label is the display text, col is
// the sort key, activeCol and activeDir are the current sort state, and baseURL
// is the page URL with existing query params (to preserve filters).
func sortHeader(label, col, activeCol, activeDir, baseURL string) template.HTML {
	nextDir := "asc"
	arrow := "&#x25B4;&#x25BE;" // up+down (unsorted)
	active := ""
	if col == activeCol {
		active = " sort-active"
		if activeDir == "asc" {
			nextDir = "desc"
			arrow = "&#x25B4;" // up arrow
		} else {
			nextDir = "asc"
			arrow = "&#x25BE;" // down arrow
		}
	}

	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}
	href := fmt.Sprintf("%s%ssort=%s&dir=%s", baseURL, sep, col, nextDir)

	return template.HTML(fmt.Sprintf(
		`<th class="sortable%s"><a href="%s" style="color:inherit;text-decoration:none;">%s <span class="sort-arrow">%s</span></a></th>`,
		active, template.HTMLEscapeString(href), template.HTMLEscapeString(label), arrow,
	))
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
			src = "compliance.vgo"
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
