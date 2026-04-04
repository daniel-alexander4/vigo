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
	"fmt"
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

// publicDocSections are the doc directories served on the public website.
// Everything else (operations, CLI reference, traits, UI, connwaer, sandgorgon)
// is only available inside the authenticated Vigo server container.
var publicDocSections = map[string]bool{
	"concepts":    true,
	"compliance":  true,
	"getting-started": true,
	"examples":    true,
	"security":    true,
}

// publicDocSubsections allows specific subdirectories within reference/.
var publicDocSubsections = map[string]bool{
	"reference/executors": true,
	"reference/platforms": true,
}

// contentFiles maps URL slugs to markdown files relative to the content directory.
// Pages with custom templates (differentiators, pricing, migration) are not in this map.
var contentFiles = map[string]string{
	"comparison":            "overview/technical-comparison.md",
	"performance-1m":        "overview/performance-1m.md",
	"performance-15s":       "overview/performance-15s.md",
	"performance-30s":       "overview/performance-30s.md",
	"composition-patterns":  "overview/composition-patterns.md",
	"license":               "LICENSE",
	"terms":                 "COMMERCIAL-TERMS.md",
	"migrate/puppet":        "migration/puppet.md",
	"migrate/ansible":       "migration/ansible.md",
	"migrate/chef":          "migration/chef.md",
	"migrate/salt":          "migration/salt.md",
	"migrate/cfengine":      "migration/cfengine.md",
}

// contentTitles overrides the auto-generated title for specific slugs.
var contentTitles = map[string]string{
	"license":              "License",
	"terms":                "Commercial Terms",
	"performance-1m":       "Performance Analysis (1 min)",
	"performance-15s":      "Performance Analysis (15 sec)",
	"performance-30s":      "Performance Analysis (30 sec)",
	"composition-patterns": "Composition Patterns",
	"migrate/puppet":      "Migrating from Puppet",
	"migrate/ansible":     "Migrating from Ansible",
	"migrate/chef":        "Migrating from Chef",
	"migrate/salt":        "Migrating from Salt",
	"migrate/cfengine":    "Migrating from CFEngine",
}

// --- Types ---

type pageData struct {
	Title       string
	Description string
	ActiveNav   string
	Content     template.HTML
	Canonical   string // absolute URL path, e.g. "/pricing"
}

type statsPageData struct {
	Title       string
	Description string
	ActiveNav   string
	Stats       StatsData
	Canonical   string
}

// contentDescriptions provides meta descriptions for content pages.
var contentDescriptions = map[string]string{
	"comparison":           "Head-to-head comparison of Vigo against Puppet, Ansible, Chef, Salt, and CFEngine covering architecture, security, performance, and scalability.",
	"performance-1m":       "Performance analysis of Vigo at 1-minute check-in intervals. Capacity projections, CPU/memory estimates, and bottleneck analysis on commodity hardware.",
	"performance-15s":      "Performance analysis of Vigo at 15-second check-in intervals. 10,000 nodes on a single 4-core server with sub-millisecond check-ins.",
	"performance-30s":      "Performance analysis of Vigo at 30-second check-in intervals. Capacity projections and resource estimates for continuous enforcement.",
	"composition-patterns": "Configuration composition patterns in Vigo — glob matching, roles, modules, resource DAGs, and Go templates for infrastructure as code.",
	"license":              "Vigo proprietary software license. Copyright 2026 Alexander4, LLC. All rights reserved.",
	"terms":                "Vigo commercial terms — pricing tiers, support levels, loyalty discounts, multi-year commitments, and enterprise agreements.",
	"migrate/puppet":       "Migrate from Puppet to Vigo — concept mapping, resource type equivalents, template syntax conversion, and common gotchas.",
	"migrate/ansible":      "Migrate from Ansible to Vigo — push-to-pull shift, Jinja2 to Go templates, playbook conversion, and variable hierarchy mapping.",
	"migrate/chef":         "Migrate from Chef to Vigo — Ruby DSL to YAML, 15 attribute levels to 3, cookbook conversion, and data bag alternatives.",
	"migrate/salt":         "Migrate from Salt to Vigo — Pillar to vars, Grains to Traits, Jinja2 restrictions, and requisite mapping.",
	"migrate/cfengine":     "Migrate from CFEngine to Vigo — promise theory heritage, class system to when: expressions, bundle to module conversion.",
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
	featuresTmpl := parsePage("features.html")
	complianceTmpl := parsePage("compliance.html")
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
			Title:       "Vigo — Distributed State Enforcement",
			Description: "Vigo is a distributed state enforcement engine. One server, one binary agent, seven operating systems, 72 resource types. 10,000 nodes at 15-second check-ins on a single 4-core server.",
			ActiveNav:   "home",
			Canonical:   "/",
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
				Title:       title + " — Vigo",
				Description: contentDescriptions[s],
				ActiveNav:   s,
				Content:     template.HTML(html),
				Canonical:   "/" + s,
			})
		})
	}

	// Features — comprehensive feature overview.
	mux.HandleFunc("/features", func(w http.ResponseWriter, r *http.Request) {
		featuresTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Features — Vigo",
			Description: "Everything Vigo does: 73 resource types, 21 compliance frameworks, browser-based remote access, orchestration, risk scoring, bare metal lifecycle, and AI assistant.",
			ActiveNav:   "features",
			Canonical:   "/features",
		})
	})

	// Compliance — framework list and compliance features.
	mux.HandleFunc("/compliance", func(w http.ResponseWriter, r *http.Request) {
		complianceTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Compliance — Vigo",
			Description: "21 regulatory frameworks enforced through idempotent resources. HIPAA, SOC 2, PCI DSS, NIST, CIS, ISO 27001, and more. Honest compliance claims.",
			ActiveNav:   "features",
			Canonical:   "/compliance",
		})
	})

	// Why Vigo — custom template with styled differentiator cards.
	mux.HandleFunc("/differentiators", func(w http.ResponseWriter, r *http.Request) {
		diffTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Why Vigo — Differentiators",
			Description: "Why choose Vigo over Puppet, Ansible, Chef, or Salt? Simplicity, security, performance, resilience, and transparent pricing.",
			ActiveNav:   "differentiators",
			Canonical:   "/differentiators",
		})
	})

	// Pricing page — custom template with cards and support table.
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		pricingTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Pricing — Vigo",
			Description: "Vigo pricing: free for 25 nodes, $84-$144/node/year for paid tiers. All features included at every tier. No enterprise-only add-ons.",
			ActiveNav:   "pricing",
			Canonical:   "/pricing",
		})
	})

	// Migration guides landing page.
	mux.HandleFunc("/migration", func(w http.ResponseWriter, r *http.Request) {
		migrationTmpl.ExecuteTemplate(w, "base", pageData{
			Title:       "Migration Guides — Vigo",
			Description: "Migrate to Vigo from Puppet, Ansible, Chef, Salt, or CFEngine. Concept mapping, resource equivalents, and step-by-step conversion guides.",
			ActiveNav:   "migration",
			Canonical:   "/migration",
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
			Title:       "Site Traffic — Vigo",
			Description: "Website traffic statistics.",
			ActiveNav:   "",
			Stats:       statsStore.Query(days),
			Canonical:   "/stats",
		}
		statsTmpl.ExecuteTemplate(w, "base", data)
	}, statsUser, statsPass))

	// Docs — public subset of the in-product documentation.
	mux.HandleFunc("/docs/", func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimPrefix(r.URL.Path, "/docs/")
		slug = strings.TrimSuffix(slug, "/")

		// Docs landing page — use the website's curated public index.
		if slug == "" {
			raw, err := os.ReadFile(filepath.Join(contentDir, "website", "docs-index.md"))
			if err != nil {
				http.Error(w, "docs not found", 404)
				return
			}
			html := renderMarkdown(string(raw))
			pageTmpl.ExecuteTemplate(w, "base", pageData{
				Title:       "Documentation — Vigo",
				Description: "Vigo documentation: concepts, getting started, compliance frameworks, executor reference, and example configs.",
				ActiveNav:   "docs",
				Content:     template.HTML(html),
				Canonical:   "/docs/",
			})
			return
		}

		// Check if the section is public.
		parts := strings.SplitN(slug, "/", 3)
		section := parts[0]
		allowed := publicDocSections[section]
		if !allowed && len(parts) >= 2 {
			allowed = publicDocSubsections[section+"/"+parts[1]]
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}

		// Try slug as a directory with _index.md, then as slug.md.
		var raw []byte
		var err error
		filePath := filepath.Join(contentDir, "docs", slug+".md")
		indexPath := filepath.Join(contentDir, "docs", slug, "_index.md")
		if info, statErr := os.Stat(filepath.Join(contentDir, "docs", slug)); statErr == nil && info.IsDir() {
			raw, err = os.ReadFile(indexPath)
			if err != nil {
				http.Error(w, "page not found", 404)
				return
			}
		} else {
			raw, err = os.ReadFile(filePath)
			if err != nil {
				http.Error(w, "page not found", 404)
				return
			}
		}

		content := stripConfidentialFooter(string(raw))
		html := renderMarkdown(content)

		// Extract title from first # heading.
		title := "Documentation"
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimPrefix(line, "# ")
				break
			}
		}

		pageTmpl.ExecuteTemplate(w, "base", pageData{
			Title:     title + " — Vigo Docs",
			ActiveNav: "docs",
			Content:   template.HTML(html),
			Canonical: "/docs/" + slug,
		})
	})

	// Sitemap.
	sitemapDomain := os.Getenv("VIGO_SITE_DOMAIN")
	if sitemapDomain == "" {
		sitemapDomain = "localhost"
	}
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`)
		fmt.Fprintf(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		pages := []string{"/", "/features", "/compliance", "/differentiators", "/comparison", "/pricing", "/migration",
			"/performance-1m", "/performance-15s", "/performance-30s", "/composition-patterns",
			"/license", "/terms",
			"/migrate/puppet", "/migrate/ansible", "/migrate/chef", "/migrate/salt", "/migrate/cfengine",
			"/docs/"}
		for _, p := range pages {
			fmt.Fprintf(w, `<url><loc>https://%s%s</loc></url>`, sitemapDomain, p)
		}
		fmt.Fprintf(w, `</urlset>`)
	})

	// robots.txt.
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /stats\n\nSitemap: https://%s/sitemap.xml\n", sitemapDomain)
	})

	// Wrap mux with stats middleware.
	handler := statsMiddleware(mux, statsStore)

	// When behind a reverse proxy (Caddy), listen on plain HTTP.
	// For local dev without a proxy, generate a self-signed cert.
	if os.Getenv("VIGO_SITE_PLAIN_HTTP") == "1" {
		log.Printf("vigo site listening on http://localhost:%s (plain HTTP — expects reverse proxy)", port)
		log.Fatal(http.ListenAndServe(":"+port, handler))
	} else {
		tlsCert, tlsKey, err := generateSelfSignedCert()
		if err != nil {
			log.Fatalf("generating TLS cert: %v", err)
		}
		log.Printf("vigo site listening on https://localhost:%s (self-signed TLS)", port)
		log.Fatal(http.ListenAndServeTLS(":"+port, tlsCert, tlsKey, handler))
	}
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

// stripConfidentialFooter removes the "Confidential" footer lines that appear
// in container-only docs. These are not appropriate for the public website.
func stripConfidentialFooter(md string) string {
	var lines []string
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, "Confidential -- Alexander4") || strings.Contains(line, "documentation-license") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

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
		"../../concepts/config-format.md",
		"../../concepts/resource-dag.md",
		"../../concepts/templates.md",
		"../../concepts/when-expressions.md",
		"../../concepts/secrets.md",
		"../../concepts/custom-executors.md",
		"../../reference/executors/_index.md",
		"../../examples/nginx-module.md",
		"../../getting-started/first-module.md",
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
