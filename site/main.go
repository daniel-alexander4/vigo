// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Hobnob public website — static marketing + documentation browser.
// Embeds all content at build time for single-binary deployment.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"html/template"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

// docsDir is the path to the docs directory. Embedded at build time via -ldflags
// or read from HOBNOB_DOCS_DIR env var (defaults to ../docs for local dev).
var docsDir = ""

// contentFiles maps URL slugs to markdown files relative to the docs directory.
// Pages with custom templates (differentiators, pricing, migration) are not in this map.
var contentFiles = map[string]string{
	"overview":   "overview/overview.md",
	"fact-sheet": "overview/fact-sheet.md",
	"comparison": "overview/technical-comparison.md",
}

// --- Types ---

type pageData struct {
	Title       string
	ActiveNav   string
	Content     template.HTML
	NavTree     []navSection
	CurrentPath string
	Breadcrumbs []crumb
}

type navSection struct {
	Title string
	Path  string
	Pages []navPage
}

type navPage struct {
	Title string
	Path  string
	Text  string // plain text for search
}

type crumb struct {
	Title string
	Path  string
}

// --- Main ---

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "443"
	}

	if docsDir == "" {
		docsDir = os.Getenv("HOBNOB_DOCS_DIR")
	}
	if docsDir == "" {
		docsDir = "../docs"
	}

	// Parse templates. Each page template includes base.html for the layout.
	// We parse each page separately to avoid {{define "content"}} conflicts.
	parsePage := func(name string) *template.Template {
		t, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			log.Fatalf("parsing template %s: %v", name, err)
		}
		return t
	}
	landingTmpl := parsePage("landing.html")
	pageTmpl := parsePage("page.html")
	diffTmpl := parsePage("differentiators.html")
	pricingTmpl := parsePage("pricing.html")
	migrationTmpl := parsePage("migration.html")
	docsTmpl := parsePage("docs.html")

	// Content dir for top-level pages (OVERVIEW.md, etc.).
	contentDir := os.Getenv("HOBNOB_CONTENT_DIR")
	if contentDir == "" {
		contentDir = filepath.Join(docsDir, "..")
	}

	// Build docs nav tree.
	nav := buildNav(docsDir)

	// Routes.
	mux := http.NewServeMux()

	// Static assets.
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Landing page.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		landingTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Hobnob — Distributed State Enforcement",
			ActiveNav: "home",
		})
	})

	// Top-level content pages.
	for slug, filename := range contentFiles {
		s, f := slug, filename
		mux.HandleFunc("/"+s, func(w http.ResponseWriter, r *http.Request) {
			raw, err := os.ReadFile(filepath.Join(docsDir, f))
			if err != nil {
				http.Error(w, "page not found", 404)
				return
			}
			html := renderMarkdown(string(raw))
			pageTmpl.ExecuteTemplate(w, "base", pageData{
				Title:     strings.Title(strings.ReplaceAll(s, "-", " ")),
				ActiveNav: s,
				Content:   template.HTML(html),
			})
		})
	}

	// Why Hobnob — custom template with styled differentiator cards.
	mux.HandleFunc("/differentiators", func(w http.ResponseWriter, r *http.Request) {
		diffTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Why Hobnob",
			ActiveNav: "differentiators",
		})
	})

	// Pricing page — custom template with cards and support table.
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		pricingTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Pricing",
			ActiveNav: "pricing",
		})
	})

	// Migration guides landing page.
	mux.HandleFunc("/migration", func(w http.ResponseWriter, r *http.Request) {
		migrationTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Migration Guides",
			ActiveNav: "migration",
		})
	})

	// Docs browser.
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := os.ReadFile(filepath.Join(docsDir, "_index.md"))
		html := rewriteDocsLinks(renderMarkdown(string(raw)), "")
		docsTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Documentation",
			ActiveNav: "docs",
			Content:   template.HTML(html),
			NavTree:   nav,
		})
	})

	mux.HandleFunc("/docs/", func(w http.ResponseWriter, r *http.Request) {
		pagePath := strings.TrimPrefix(r.URL.Path, "/docs/")
		pagePath = strings.TrimSuffix(pagePath, ".md")

		// Serve static assets (SVG, PNG, JSON) directly.
		ext := filepath.Ext(pagePath)
		if ext != "" && ext != ".md" {
			filePath := filepath.Clean(filepath.Join(docsDir, pagePath))
			if strings.Contains(filePath, "..") {
				http.Error(w, "invalid path", 400)
				return
			}
			http.ServeFile(w, r, filePath)
			return
		}

		// Try as file, then as directory index.
		var raw []byte
		var err error
		var baseDir string
		mdPath := filepath.Join(docsDir, pagePath+".md")
		if raw, err = os.ReadFile(mdPath); err != nil {
			mdPath = filepath.Join(docsDir, pagePath, "_index.md")
			if raw, err = os.ReadFile(mdPath); err != nil {
				http.NotFound(w, r)
				return
			}
			baseDir = pagePath
		} else {
			baseDir = path.Dir(pagePath)
		}

		html := rewriteDocsLinks(renderMarkdown(string(raw)), baseDir)

		// Build breadcrumbs.
		parts := strings.Split(pagePath, "/")
		var breadcrumbs []crumb
		currentSection := ""
		for i, part := range parts {
			title := strings.ReplaceAll(part, "-", " ")
			title = strings.ReplaceAll(title, "_", " ")
			if i == 0 {
				currentSection = part
				for _, sec := range nav {
					if sec.Path == part {
						title = sec.Title
						break
					}
				}
			}
			if i < len(parts)-1 {
				breadcrumbs = append(breadcrumbs, crumb{Title: title, Path: strings.Join(parts[:i+1], "/")})
			} else {
				breadcrumbs = append(breadcrumbs, crumb{Title: title})
			}
		}

		docsTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Docs — " + breadcrumbs[len(breadcrumbs)-1].Title,
			ActiveNav:   "docs",
			Content:     template.HTML(html),
			NavTree:     nav,
			CurrentPath: pagePath,
			Breadcrumbs: breadcrumbs,
		})
		_ = currentSection
	})

	// Generate self-signed TLS cert for local HTTPS.
	tlsCert, tlsKey, err := generateSelfSignedCert()
	if err != nil {
		log.Fatalf("generating TLS cert: %v", err)
	}

	log.Printf("hobnob site listening on https://localhost:%s", port)
	log.Fatal(http.ListenAndServeTLS(":"+port, tlsCert, tlsKey, mux))
}

// generateSelfSignedCert creates a self-signed TLS certificate and key
// in temp files, valid for localhost. Returns paths to cert and key files.
func generateSelfSignedCert() (certPath, keyPath string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Hobnob Dev"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}

	certFile, err := os.CreateTemp("", "hobnob-site-cert-*.pem")
	if err != nil {
		return "", "", err
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyFile, err := os.CreateTemp("", "hobnob-site-key-*.pem")
	if err != nil {
		return "", "", err
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()

	return certFile.Name(), keyFile.Name(), nil
}

// --- Markdown ---

func renderMarkdown(md string) string {
	gm := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Table))
	var buf strings.Builder
	if err := gm.Convert([]byte(md), &buf); err != nil {
		return "<p>Error rendering markdown</p>"
	}
	return buf.String()
}

// --- Docs nav ---

var sectionOrder = []string{
	"getting-started", "concepts", "executors", "traits",
	"cli", "api", "operations", "ui", "ai", "examples", "migration",
}

func buildNav(docsDir string) []navSection {
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return nil
	}

	sectionMap := make(map[string]navSection)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		sectionPath := entry.Name()
		sectionTitle := extractTitle(filepath.Join(docsDir, sectionPath, "_index.md"))
		if sectionTitle == "" {
			sectionTitle = strings.Title(strings.ReplaceAll(sectionPath, "-", " "))
		}

		pageFiles, err := os.ReadDir(filepath.Join(docsDir, sectionPath))
		if err != nil {
			continue
		}

		var pages []navPage
		for _, pf := range pageFiles {
			if pf.IsDir() || !strings.HasSuffix(pf.Name(), ".md") || pf.Name() == "_index.md" {
				continue
			}
			pageName := strings.TrimSuffix(pf.Name(), ".md")
			pageTitle := extractTitle(filepath.Join(docsDir, sectionPath, pf.Name()))
			if pageTitle == "" {
				pageTitle = strings.ReplaceAll(pageName, "-", " ")
				pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
			}

			// Read plain text for search.
			var plainText string
			if raw, err := os.ReadFile(filepath.Join(docsDir, sectionPath, pf.Name())); err == nil {
				plainText = stripMarkdown(string(raw))
			}

			pages = append(pages, navPage{
				Title: pageTitle,
				Path:  sectionPath + "/" + pageName,
				Text:  plainText,
			})
		}

		sort.Slice(pages, func(i, j int) bool { return pages[i].Title < pages[j].Title })
		sectionMap[sectionPath] = navSection{Title: sectionTitle, Path: sectionPath, Pages: pages}
	}

	var nav []navSection
	seen := make(map[string]bool)
	for _, name := range sectionOrder {
		if sec, ok := sectionMap[name]; ok {
			nav = append(nav, sec)
			seen[name] = true
		}
	}
	for name, sec := range sectionMap {
		if !seen[name] {
			nav = append(nav, sec)
		}
	}
	return nav
}

func extractTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

func stripMarkdown(md string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimPrefix(trimmed, "#")
		}
		trimmed = strings.TrimSpace(trimmed)
		if len(trimmed) > 0 && trimmed[0] == '|' && !strings.ContainsAny(trimmed, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
			continue
		}
		trimmed = strings.ReplaceAll(trimmed, "|", " ")
		if trimmed != "" {
			b.WriteString(trimmed)
			b.WriteByte(' ')
		}
	}
	text := b.String()
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func rewriteDocsLinks(html string, baseDir string) string {
	// Rewrite relative .md links in rendered HTML to /docs/ absolute paths.
	var b strings.Builder
	remaining := html
	for {
		i := strings.Index(remaining, `href="`)
		if i < 0 {
			b.WriteString(remaining)
			break
		}
		// Write everything up to and including href="
		b.WriteString(remaining[:i+6])
		remaining = remaining[i+6:]

		// Find the closing quote.
		end := strings.Index(remaining, `"`)
		if end < 0 {
			b.WriteString(remaining)
			break
		}
		href := remaining[:end]

		// Skip absolute URLs, anchors, mailto, etc.
		if href == "" || href[0] == '/' || href[0] == '#' ||
			strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
			b.WriteString(href)
			remaining = remaining[end:]
			continue
		}

		// Split anchor from path.
		anchor := ""
		hrefPath := href
		if ai := strings.IndexByte(hrefPath, '#'); ai >= 0 {
			anchor = hrefPath[ai:]
			hrefPath = hrefPath[:ai]
		}

		// Only rewrite .md links or extensionless links (internal docs).
		if !strings.HasSuffix(hrefPath, ".md") && strings.Contains(hrefPath, ".") {
			b.WriteString(href)
			remaining = remaining[end:]
			continue
		}

		// Resolve relative path against the current doc's directory.
		resolved := path.Clean(path.Join(baseDir, hrefPath))
		resolved = strings.TrimSuffix(resolved, ".md")
		resolved = strings.TrimSuffix(resolved, "/_index")

		b.WriteString("/docs/" + resolved + anchor)
		remaining = remaining[end:]
	}
	return b.String()
}

func init() {
	// Suppress strings.Title deprecation — good enough for display names.
}
