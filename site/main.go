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
	"overview":   "overview/overview.md",
	"fact-sheet": "overview/fact-sheet.md",
	"comparison": "overview/technical-comparison.md",
}

// --- Types ---

type pageData struct {
	Title     string
	ActiveNav string
	Content   template.HTML
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
			pageTmpl.ExecuteTemplate(w, "base", pageData{
				Title:     strings.Title(strings.ReplaceAll(s, "-", " ")),
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

	// Generate self-signed TLS cert for local HTTPS.
	tlsCert, tlsKey, err := generateSelfSignedCert()
	if err != nil {
		log.Fatalf("generating TLS cert: %v", err)
	}

	log.Printf("vigo site listening on https://localhost:%s", port)
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
	gm := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Table))
	var buf strings.Builder
	if err := gm.Convert([]byte(md), &buf); err != nil {
		return "<p>Error rendering markdown</p>"
	}
	return buf.String()
}

func init() {
	// Suppress strings.Title deprecation — good enough for display names.
}
