// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Vigo public website — static marketing pages.
// Embeds all content at build time for single-binary deployment.
package main

import (
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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

// contentDir is the path to the content directory for top-level marketing pages.
var contentDir = ""

// contentFiles maps URL slugs to markdown files relative to the content directory.
// Pages with custom templates (differentiators, pricing, migration) are not in this map.
var contentFiles = map[string]string{
	"overview":              "overview/overview.md",
	"comparison":            "overview/technical-comparison.md",
	"performance-1m":        "overview/performance-1m.md",
	"performance-15s":       "overview/performance-15s.md",
	"performance-30s":       "overview/performance-30s.md",
	"composition-patterns":  "overview/composition-patterns.md",
	"license":               "LICENSE",
	"terms":                 "COMMERCIAL-TERMS.md",
}

// contentTitles overrides the auto-generated title for specific slugs.
var contentTitles = map[string]string{
	"license":              "License",
	"terms":                "Commercial Terms",
	"performance-1m":       "Performance Analysis (1 min)",
	"performance-15s":      "Performance Analysis (15 sec)",
	"performance-30s":      "Performance Analysis (30 sec)",
	"composition-patterns": "Composition Patterns",
}

// --- Types ---

type pageData struct {
	Title     string
	ActiveNav string
	Content   template.HTML
}

type statsPageData struct {
	Title     string
	ActiveNav string
	Stats     StatsData
}

// --- Main ---

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "443"
	}

	if contentDir == "" {
		contentDir = os.Getenv("VIGO_CONTENT_DIR")
	}
	if contentDir == "" {
		contentDir = ".."
	}

	// Open stats database.
	statsDir := os.Getenv("VIGO_STATS_DIR")
	if statsDir == "" {
		statsDir = "/tmp"
	}
	statsStore, err := NewStatsStore(filepath.Join(statsDir, "site-stats.db"))
	if err != nil {
		log.Fatalf("opening stats db: %v", err)
	}

	// Run hourly aggregation.
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			statsStore.Aggregate()
		}
	}()

	// Stats auth credentials from env.
	statsUser := os.Getenv("VIGO_STATS_USER")
	statsPass := os.Getenv("VIGO_STATS_PASS")
	if statsUser == "" {
		statsUser = "admin"
	}
	if statsPass == "" {
		statsPass = "vigo-stats"
		log.Println("WARNING: using default stats password — set VIGO_STATS_USER and VIGO_STATS_PASS")
	}

	// Template functions for stats bar charts.
	funcMap := template.FuncMap{
		"barChart": func(pages []PathStat) template.HTML {
			items := make([]barItem, len(pages))
			for i, p := range pages {
				items[i] = barItem{Label: p.Path, Value: p.Views, Pct: p.Pct}
			}
			return barChartSVG(items, 700, 10)
		},
		"barChartRef": func(refs []ReferrerStat) template.HTML {
			items := make([]barItem, len(refs))
			for i, r := range refs {
				items[i] = barItem{Label: r.Referrer, Value: r.Views, Pct: r.Pct}
			}
			return barChartSVG(items, 700, 10)
		},
		"barChartUA": func(cats []UACategory) template.HTML {
			items := make([]barItem, len(cats))
			for i, c := range cats {
				items[i] = barItem{Label: c.Category, Value: c.Count, Pct: c.Pct}
			}
			return barChartSVG(items, 700, 10)
		},
	}

	// Parse templates. Each page template includes base.html for the layout.
	// We parse each page separately to avoid {{define "content"}} conflicts.
	parsePage := func(name string) *template.Template {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
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
	statsTmpl := parsePage("stats.html")

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
			Title:     "Vigo — Distributed State Enforcement",
			ActiveNav: "home",
		})
	})

	// Top-level content pages.
	for slug, filename := range contentFiles {
		s, f := slug, filename
		mux.HandleFunc("/"+s, func(w http.ResponseWriter, r *http.Request) {
			raw, err := os.ReadFile(filepath.Join(contentDir, f))
			if err != nil {
				http.Error(w, "page not found", 404)
				return
			}
			html := renderMarkdown(string(raw))
			title := contentTitles[s]
			if title == "" {
				title = strings.Title(strings.ReplaceAll(s, "-", " "))
			}
			pageTmpl.ExecuteTemplate(w, "base", pageData{
				Title:     title,
				ActiveNav: s,
				Content:   template.HTML(html),
			})
		})
	}

	// Why Vigo — custom template with styled differentiator cards.
	mux.HandleFunc("/differentiators", func(w http.ResponseWriter, r *http.Request) {
		diffTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     "Why Vigo",
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

	// Stats page (behind basic auth).
	mux.HandleFunc("/stats", basicAuth(func(w http.ResponseWriter, r *http.Request) {
		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
				days = n
			}
		}
		data := statsPageData{
			Title:     "Site Traffic",
			ActiveNav: "",
			Stats:     statsStore.Query(days),
		}
		statsTmpl.ExecuteTemplate(w, "base", data)
	}, statsUser, statsPass))

	// Wrap mux with stats middleware.
	handler := statsMiddleware(mux, statsStore)

	// Generate self-signed TLS cert for local HTTPS.
	tlsCert, tlsKey, err := generateSelfSignedCert()
	if err != nil {
		log.Fatalf("generating TLS cert: %v", err)
	}

	log.Printf("vigo site listening on https://localhost:%s", port)
	log.Fatal(http.ListenAndServeTLS(":"+port, tlsCert, tlsKey, handler))
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
		Subject:      pkix.Name{Organization: []string{"Vigo Dev"}},
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

	certFile, err := os.CreateTemp("", "vigo-site-cert-*.pem")
	if err != nil {
		return "", "", err
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyFile, err := os.CreateTemp("", "vigo-site-key-*.pem")
	if err != nil {
		return "", "", err
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()

	return certFile.Name(), keyFile.Name(), nil
}

// --- Markdown ---

func renderMarkdown(md string) string {
	// Rewrite relative .md links to website URL paths before rendering.
	// "performance-1m.md" → "/performance-1m"
	// "../concepts/composition-patterns.md" → "/composition-patterns"
	md = rewriteMarkdownLinks(md)

	gm := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Table))
	var buf strings.Builder
	if err := gm.Convert([]byte(md), &buf); err != nil {
		return "<p>Error rendering markdown</p>"
	}
	return buf.String()
}

// rewriteMarkdownLinks converts relative .md links to absolute URL paths.
// Handles patterns like: [text](file.md), [text](../dir/file.md)
func rewriteMarkdownLinks(md string) string {
	// Replace ](../anything/file.md) → ](/file)
	// Replace ](file.md) → ](/file)
	result := md
	// Rewrite links that have website routes.
	for old, slug := range map[string]string{
		"performance-1m.md":                   "/performance-1m",
		"performance-15s.md":                  "/performance-15s",
		"performance-30s.md":                  "/performance-30s",
		"../concepts/composition-patterns.md": "/composition-patterns",
	} {
		result = strings.ReplaceAll(result, "("+old+")", "("+slug+")")
	}

	// Remove links to docs-only pages that don't have website routes.
	// Convert [text](file.md) → text (plain text, no link).
	for _, deadLink := range []string{
		"config-format.md",
		"config-format.md#directory-inheritance",
		"resource-language.md",
		"when-expressions.md",
		"templates.md",
		"resource-dag.md",
	} {
		// [Link Text](file.md) → Link Text
		old := "](" + deadLink + ")"
		// Find the opening bracket for this link.
		for {
			idx := strings.Index(result, old)
			if idx < 0 {
				break
			}
			// Walk backwards to find the matching [.
			openBracket := strings.LastIndex(result[:idx], "[")
			if openBracket < 0 {
				break
			}
			linkText := result[openBracket+1 : idx]
			result = result[:openBracket] + linkText + result[idx+len(old):]
		}
	}

	return result
}

func init() {
	// Suppress strings.Title deprecation — good enough for display names.
}
