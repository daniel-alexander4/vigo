// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/ui"
)

// RiskHandler serves fleet and per-envoy risk scoring endpoints.
type RiskHandler struct {
	fleetIndex   *fleet.FleetIndex
	queries      *db.Queries
	baseInterval time.Duration
	version      string
	orgName      string
	logoPath     string
}

// NewRiskHandler creates a RiskHandler.
func NewRiskHandler(fleetIndex *fleet.FleetIndex, queries *db.Queries, baseInterval time.Duration, version string) *RiskHandler {
	return &RiskHandler{
		fleetIndex:   fleetIndex,
		queries:      queries,
		baseInterval: baseInterval,
		version:      version,
	}
}

// SetBranding wires branding.org_name and branding.logo_path for the
// insurance attestation report header. Empty org_name falls back to a
// generic label; empty or unreadable logo_path omits the logo silently.
func (h *RiskHandler) SetBranding(cfg config.BrandingSection) {
	h.orgName = cfg.OrgName
	h.logoPath = cfg.LogoPath
}

// RegisterRoutes mounts risk endpoints. Fleet + per-envoy risk
// scores and the cyber-insurance attestation export are all
// fleet-shaped data — fleet.read is the right gate. The
// insurance.{html,pdf} variants are operator-facing artifacts that
// derive from the same per-envoy scoring; keeping them on
// fleet.read avoids forking the gate by output format.
func (h *RiskHandler) RegisterRoutes(mux *http.ServeMux) {
	read := vigoauth.RequirePermission(vigoauth.PermFleetRead)
	mux.Handle("GET /api/v1/risk", read(http.HandlerFunc(h.handleFleetRisk)))
	mux.Handle("GET /api/v1/risk/history", read(http.HandlerFunc(h.handleRiskHistory)))
	mux.Handle("GET /api/v1/risk/insurance", read(http.HandlerFunc(h.handleInsuranceExport)))
	mux.Handle("GET /api/v1/risk/insurance.html", read(http.HandlerFunc(h.handleInsuranceExportHTML)))
	mux.Handle("GET /api/v1/risk/insurance.pdf", read(http.HandlerFunc(h.handleInsuranceExportPDF)))
	mux.Handle("GET /api/v1/risk/{envoyID}", read(http.HandlerFunc(h.handleEnvoyRisk)))
}

// handleFleetRisk returns the fleet-wide risk summary.
func (h *RiskHandler) handleFleetRisk(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}

	inputs := envoyInputs(h.fleetIndex.AllEnvoys())
	fr := risk.ScoreFleet(inputs, h.baseInterval, 10)
	fr.Envoys = nil // keep response concise

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fr)
}

// handleEnvoyRisk returns the risk score breakdown for a single envoy.
func (h *RiskHandler) handleEnvoyRisk(w http.ResponseWriter, r *http.Request) {
	if h.fleetIndex == nil {
		http.Error(w, `{"error":"fleet index not available"}`, http.StatusServiceUnavailable)
		return
	}

	envoyID := r.PathValue("envoyID")
	es := h.fleetIndex.GetEnvoy(envoyID)
	if es == nil {
		http.Error(w, `{"error":"envoy not found"}`, http.StatusNotFound)
		return
	}

	er := risk.ScoreEnvoy(envoyToInput(*es), h.baseInterval)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(er)
}

// handleRiskHistory returns daily risk posture snapshots.
func (h *RiskHandler) handleRiskHistory(w http.ResponseWriter, r *http.Request) {
	days := int64(90)
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.ParseInt(d, 10, 64); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}

	rows, err := h.queries.ListRiskPostureHistory(context.Background(), days)
	if err != nil {
		http.Error(w, `{"error":"failed to query risk history"}`, http.StatusInternalServerError)
		return
	}

	type historyEntry struct {
		Date         string         `json:"date"`
		AvgScore     int64          `json:"avg_score"`
		MaxScore     int64          `json:"max_score"`
		Distribution map[string]int64 `json:"distribution"`
		ScoredEnvoys int64          `json:"scored_envoys"`
		TotalEnvoys  int64          `json:"total_envoys"`
	}

	entries := make([]historyEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, historyEntry{
			Date:     row.SnapshotDate,
			AvgScore: row.AvgScore,
			MaxScore: row.MaxScore,
			Distribution: map[string]int64{
				"low":      row.DistLow,
				"medium":   row.DistMedium,
				"high":     row.DistHigh,
				"critical": row.DistCritical,
			},
			ScoredEnvoys: row.ScoredEnvoys,
			TotalEnvoys:  row.TotalEnvoys,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"history": entries})
}

// InsuranceReport is the cyber insurance attestation report.
type InsuranceReport struct {
	ReportID       string         `json:"report_id"`
	Organization   string         `json:"organization"`
	PreparedBy     string         `json:"prepared_by"`
	GeneratedAt    string         `json:"generated_at"`
	GeneratedBy    string         `json:"generated_by"`
	PeriodStart    string         `json:"period_start"`
	PeriodEnd      string         `json:"period_end"`
	CurrentPosture risk.FleetRisk `json:"current_posture"`
	Trend          []TrendEntry   `json:"trend"`
	FleetSummary   InsuranceFleet `json:"fleet_summary"`

	// LogoDataURI is the org logo encoded as a data: URI, inlined into
	// the HTML/PDF cover block. Omitted from JSON (machine consumers
	// don't need the image, and it bloats the payload).
	LogoDataURI string `json:"-"`
}

// TrendEntry is a single day in the risk trend.
type TrendEntry struct {
	Date         string `json:"date"`
	AvgScore     int64  `json:"avg_score"`
	MaxScore     int64  `json:"max_score"`
	Critical     int64  `json:"critical"`
	High         int64  `json:"high"`
	ScoredEnvoys int64  `json:"scored_envoys"`
}

// InsuranceFleet summarizes fleet composition.
type InsuranceFleet struct {
	TotalEnvoys int            `json:"total_envoys"`
	OSFamily    map[string]int `json:"os_family"`
}

func (h *RiskHandler) buildInsuranceReport(preparedBy string) (*InsuranceReport, error) {
	now := time.Now().UTC()
	periodStart := now.AddDate(0, 0, -90)

	// Current live posture.
	inputs := envoyInputs(h.fleetIndex.AllEnvoys())
	fr := risk.ScoreFleet(inputs, h.baseInterval, 10)
	fr.Envoys = nil

	// 90-day trend from DB.
	rows, err := h.queries.ListRiskPostureHistory(context.Background(), 90)
	if err != nil {
		return nil, fmt.Errorf("querying risk history: %w", err)
	}

	trend := make([]TrendEntry, 0, len(rows))
	for _, row := range rows {
		trend = append(trend, TrendEntry{
			Date:         row.SnapshotDate,
			AvgScore:     row.AvgScore,
			MaxScore:     row.MaxScore,
			Critical:     row.DistCritical,
			High:         row.DistHigh,
			ScoredEnvoys: row.ScoredEnvoys,
		})
	}

	// Fleet composition from traits.
	osFamily := make(map[string]int)
	for _, es := range h.fleetIndex.AllEnvoys() {
		if es.Revoked {
			continue
		}
		osf := es.Traits["os.family"]
		if osf == "" {
			osf = "unknown"
		}
		osFamily[osf]++
	}

	org := h.orgName
	if org == "" {
		org = "Vigo Managed Fleet"
	}
	if preparedBy == "" {
		preparedBy = "Administrator"
	}

	return &InsuranceReport{
		ReportID:       newReportID(now),
		Organization:   org,
		PreparedBy:     preparedBy,
		GeneratedAt:    now.Format(time.RFC3339),
		GeneratedBy:    "Vigo " + h.version,
		PeriodStart:    periodStart.Format("2006-01-02"),
		PeriodEnd:      now.Format("2006-01-02"),
		CurrentPosture: fr,
		Trend:          trend,
		FleetSummary: InsuranceFleet{
			TotalEnvoys: len(inputs),
			OSFamily:    osFamily,
		},
		LogoDataURI: loadLogoDataURI(h.logoPath),
	}, nil
}

// loadLogoDataURI resolves the attestation cover logo and returns it as a
// base64 data: URI. The logo is inlined (not URL-referenced) so the generated
// HTML and PDF are self-contained when emailed or printed.
//
// Resolution order (first match wins):
//  1. branding.logo_path from server.yaml (if set and readable)
//  2. /srv/vigo/branding/logo.svg — conventional operator drop-in location
//  3. Embedded Vigo default logo (ui/static/img/logo.svg)
//
// Returns "" only if all three sources fail — which shouldn't happen in
// practice since (3) is compiled into the binary. The attestation will omit
// the cover logo block cleanly in that pathological case.
func loadLogoDataURI(configuredPath string) string {
	// Step 1: operator-configured path.
	if configuredPath != "" {
		if uri, err := logoFromDisk(configuredPath); err == nil {
			return uri
		} else {
			slog.Debug("insurance report: branding.logo_path unavailable, trying default drop-in",
				"path", configuredPath, "error", err)
		}
	}

	// Step 2: conventional drop-in path — admins can put a logo here
	// without editing server.yaml.
	const defaultLogoPath = "/srv/vigo/branding/logo.svg"
	if uri, err := logoFromDisk(defaultLogoPath); err == nil {
		return uri
	} else if !os.IsNotExist(err) {
		// Log only if the file exists but is unreadable; silent when
		// it simply doesn't exist (the common case).
		slog.Debug("insurance report: default logo drop-in unavailable, falling back to embedded",
			"path", defaultLogoPath, "error", err)
	}

	// Step 3: embedded Vigo default. Always present because it's in the
	// binary.
	if uri, err := logoFromEmbed("static/img/logo.svg"); err == nil {
		return uri
	} else {
		slog.Warn("insurance report: embedded default logo unavailable — proceeding without logo",
			"error", err)
	}
	return ""
}

// logoFromDisk reads a logo from a filesystem path, validates it, and returns
// a data: URI. Returns error on any failure (missing, unsupported extension,
// too large, read error).
func logoFromDisk(path string) (string, error) {
	// 2 MiB cap — logos are typically a few KB to ~100 KB. A larger file
	// either isn't a logo or bloats every PDF render for no visual gain.
	const maxLogoBytes = 2 * 1024 * 1024

	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxLogoBytes {
		return "", fmt.Errorf("logo exceeds 2 MiB cap (%d bytes)", info.Size())
	}
	mime := mimeFromExt(path)
	if mime == "" {
		return "", fmt.Errorf("unsupported logo extension: %s", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// logoFromEmbed reads a logo from the ui.Assets embedded filesystem.
// assetPath is relative to the embed root (e.g. "static/img/logo.svg").
func logoFromEmbed(assetPath string) (string, error) {
	data, err := ui.Assets.ReadFile(assetPath)
	if err != nil {
		return "", err
	}
	mime := mimeFromExt(assetPath)
	if mime == "" {
		return "", fmt.Errorf("unsupported embedded logo extension: %s", filepath.Ext(assetPath))
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// mimeFromExt maps a file extension to its image MIME type. Returns "" for
// unknown extensions so callers can reject the file cleanly.
func mimeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".svg":
		return "image/svg+xml"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

// newReportID builds an attestation document identifier of the form
// VIGO-INS-<YYYYMMDD>-<6-char-hex>. The hex suffix is drawn from
// crypto/rand so each download is uniquely traceable; the date prefix
// makes the ID human-skimmable and sortable by generation date.
func newReportID(at time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("VIGO-INS-%s-%s",
		at.Format("20060102"),
		strings.ToUpper(hex.EncodeToString(b[:])))
}

// currentUsername returns the authenticated user's display-name-or-username
// from the request context, falling back to "" when no user is attached
// (e.g., peer-auth requests from the CLI or localhost).
func currentUsername(r *http.Request) string {
	u := vigoauth.UserFromContext(r.Context())
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

func (h *RiskHandler) handleInsuranceExport(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildInsuranceReport(currentUsername(r))
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vigo-cyber-insurance-%s.json"`, time.Now().Format("2006-01-02")))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

func (h *RiskHandler) handleInsuranceExportHTML(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildInsuranceReport(currentUsername(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cyber-insurance-attestation-%s.html"`, time.Now().Format("2006-01-02")))
	}

	renderInsuranceHTML(w, report)
}

// handleInsuranceExportPDF generates a native PDF of the attestation via
// WeasyPrint. Same content as the HTML endpoint — the HTML is rendered to a
// buffer and piped through WeasyPrint's stdin/stdout mode. Surfaces a 503
// with a human-readable fallback hint when WeasyPrint is unavailable (dev
// environments); 500 for other rendering failures.
func (h *RiskHandler) handleInsuranceExportPDF(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildInsuranceReport(currentUsername(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var htmlBuf bytes.Buffer
	renderInsuranceHTML(&htmlBuf, report)

	pdf, err := RenderHTMLToPDF(r.Context(), htmlBuf.Bytes())
	if err != nil {
		if errors.Is(err, ErrWeasyPrintUnavailable) {
			slog.Warn("insurance PDF unavailable: weasyprint not installed")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			if encErr := json.NewEncoder(w).Encode(map[string]string{
				"error":    "pdf generation unavailable on this server",
				"fallback": "/api/v1/risk/insurance.html",
			}); encErr != nil {
				slog.Debug("risk insurance 503 response encode failed", "error", encErr)
			}
			return
		}
		http.Error(w, "failed to render PDF", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cyber-insurance-attestation-%s.pdf"`, time.Now().Format("2006-01-02")))
	_, _ = w.Write(pdf)
}

// logoImg renders the cover-block <img> tag for an org logo, or "" when no
// logo is configured. The source is a self-contained data: URI so the
// document renders identically whether opened in a browser, a mail client,
// or printed from WeasyPrint.
func logoImg(report *InsuranceReport) string {
	if report.LogoDataURI == "" {
		return ""
	}
	return fmt.Sprintf(`<img class="cover__logo" src="%s" alt="%s logo">`,
		report.LogoDataURI, html.EscapeString(report.Organization))
}

// renderInsuranceHTML writes the self-contained attestation document. The
// page is theme-aware (follows the viewer's prefers-color-scheme) on screen
// and forced to black-on-white when printed or saved as PDF, so the
// attachment-delivered version always looks like a formal document
// regardless of how it's being viewed.
//
// Takes io.Writer so the same renderer can target an http.ResponseWriter
// (HTML endpoint) or a bytes.Buffer that then feeds WeasyPrint (PDF endpoint).
func renderInsuranceHTML(w io.Writer, report *InsuranceReport) {
	esc := html.EscapeString
	org := esc(report.Organization)
	preparedBy := esc(report.PreparedBy)
	reportID := esc(report.ReportID)
	periodStart := esc(report.PeriodStart)
	periodEnd := esc(report.PeriodEnd)
	generatedBy := esc(report.GeneratedBy)
	generatedAt := esc(report.GeneratedAt)
	asOf := esc(report.PeriodEnd)

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Cyber Insurance Risk Attestation — %s</title>
<style>
  :root {
    --doc-bg: #ffffff;
    --doc-fg: #1a1a2e;
    --doc-muted: #6c757d;
    --doc-border: #dee2e6;
    --doc-surface: #f8f9fa;
    --doc-rule: #1a1a2e;
    --doc-signature-line: #1a1a2e;
    --doc-critical: #c0392b;
    --doc-high: #d68910;
    --doc-medium: #2471a3;
    --doc-low: #1e8449;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --doc-bg: #1e1e2e;
      --doc-fg: #cdd6f4;
      --doc-muted: #9399a8;
      --doc-border: #45475a;
      --doc-surface: #313244;
      --doc-rule: #cdd6f4;
      --doc-signature-line: #cdd6f4;
      --doc-critical: #f38ba8;
      --doc-high: #fab387;
      --doc-medium: #89b4fa;
      --doc-low: #a6e3a1;
    }
  }
  @media print {
    :root {
      --doc-bg: #ffffff !important;
      --doc-fg: #000000 !important;
      --doc-muted: #555555 !important;
      --doc-border: #888888 !important;
      --doc-surface: #ffffff !important;
      --doc-rule: #000000 !important;
      --doc-signature-line: #000000 !important;
      --doc-critical: #8b1a1a !important;
      --doc-high: #8a5a00 !important;
      --doc-medium: #1b3a5c !important;
      --doc-low: #1b5e2b !important;
    }
    @page { size: letter; margin: 0.75in; }
    body { padding: 0 !important; }
    /* Keep section headings glued to the content that follows them.
       break-after is the modern property; page-break-after is the
       legacy alias. Both are specified for wider renderer compatibility. */
    h2 { page-break-after: avoid; break-after: avoid; }
    h2 + table, h2 + p, h2 + .methodology { page-break-before: avoid; break-before: avoid; }
    /* Don't apply page-break-inside: avoid to <table> itself — tables
       longer than a page (90-day risk trend) get shoved to the next
       page when the rule can't be honored, which orphans the preceding
       <h2> on the page before. Instead protect only individual rows
       and the header row, letting long tables break cleanly. */
    thead { break-inside: avoid; page-break-inside: avoid; }
    tr { break-inside: avoid; page-break-inside: avoid; }
    .signatory { page-break-inside: avoid; break-inside: avoid; }
  }
  html, body { background: var(--doc-bg); color: var(--doc-fg); }
  body {
    font-family: Georgia, 'Times New Roman', serif;
    margin: 0;
    padding: 3rem 2.5rem;
    line-height: 1.55;
    max-width: 920px;
    margin: 0 auto;
  }
  .cover {
    border-top: 3px double var(--doc-rule);
    border-bottom: 3px double var(--doc-rule);
    padding: 1.75rem 0;
    margin-bottom: 2.25rem;
    text-align: center;
  }
  .cover__logo {
    display: block;
    margin: 0 auto 1rem;
    max-height: 64px;
    max-width: 240px;
    height: auto;
    width: auto;
  }
  @media print {
    .cover__logo { max-height: 56px; }
  }
  .cover__eyebrow {
    font-size: 0.75rem;
    font-weight: 600;
    letter-spacing: 0.22em;
    text-transform: uppercase;
    color: var(--doc-muted);
    margin-bottom: 0.5rem;
  }
  .cover__title {
    font-size: 2rem;
    font-weight: 700;
    letter-spacing: 0.02em;
    margin: 0 0 0.35rem;
  }
  .cover__org {
    font-size: 1.15rem;
    font-style: italic;
    color: var(--doc-fg);
    margin: 0;
  }
  .meta-block {
    display: grid;
    grid-template-columns: max-content 1fr;
    column-gap: 1.25rem;
    row-gap: 0.35rem;
    font-size: 0.92rem;
    margin-bottom: 2.25rem;
  }
  .meta-block dt { font-weight: 700; color: var(--doc-muted); text-transform: uppercase; letter-spacing: 0.05em; font-size: 0.78rem; align-self: center; }
  .meta-block dd { margin: 0; font-family: 'Courier New', Courier, monospace; font-size: 0.92rem; }
  .statement {
    font-size: 1rem;
    margin-bottom: 2.25rem;
    padding: 1rem 1.25rem;
    border-left: 3px solid var(--doc-rule);
    background: var(--doc-surface);
  }
  h2 {
    font-family: Georgia, 'Times New Roman', serif;
    font-size: 1.1rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    margin: 2rem 0 0.85rem;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid var(--doc-rule);
  }
  h2 .section-num {
    display: inline-block;
    min-width: 1.8rem;
    color: var(--doc-muted);
    font-weight: 400;
  }
  .cards { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 0.6rem; margin: 0 0 1.75rem; }
  .card { border: 1px solid var(--doc-border); padding: 0.9rem 0.5rem; text-align: center; background: var(--doc-bg); }
  .card .value { font-size: 1.55rem; font-weight: 700; line-height: 1.15; font-family: Georgia, serif; }
  .card .label { font-size: 0.72rem; margin-top: 0.25rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--doc-muted); }
  table { width: 100%%; border-collapse: collapse; margin-bottom: 1.75rem; font-size: 0.92rem; }
  th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid var(--doc-border); }
  th { font-weight: 700; font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--doc-muted); border-bottom: 2px solid var(--doc-rule); }
  tr:last-child td { border-bottom: 2px solid var(--doc-rule); }
  .level-critical { color: var(--doc-critical); font-weight: 700; }
  .level-high { color: var(--doc-high); font-weight: 700; }
  .level-medium { color: var(--doc-medium); }
  .level-low { color: var(--doc-low); }
  .signatory {
    margin: 3rem 0 2rem;
    padding: 1.5rem 0;
    border-top: 2px solid var(--doc-rule);
  }
  .signatory h2 { border-bottom: none; margin-top: 0; }
  .signatory__intro { margin: 0 0 1.5rem; font-size: 0.95rem; }
  .sig-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 2.5rem 2rem; margin-top: 1.75rem; }
  .sig-field { display: flex; flex-direction: column; }
  .sig-field__line { border-bottom: 1.5px solid var(--doc-signature-line); height: 1.75rem; }
  .sig-field__label { margin-top: 0.35rem; font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--doc-muted); }
  .methodology { margin-top: 2rem; }
  .methodology h2 { margin-top: 0; }
  .methodology p { font-size: 0.9rem; margin: 0 0 0.75rem; }
  .legend { display: flex; flex-wrap: wrap; gap: 1.25rem; font-size: 0.85rem; margin: 0.5rem 0 1rem; }
  .legend span strong { font-family: Georgia, serif; }
  .doc-footer {
    margin-top: 2rem;
    padding-top: 1rem;
    border-top: 1px solid var(--doc-border);
    font-size: 0.75rem;
    color: var(--doc-muted);
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .doc-footer .right { text-align: right; }
</style>
</head>
<body>

<section class="cover">
  %s
  <div class="cover__eyebrow">Cyber Insurance</div>
  <h1 class="cover__title">Risk Attestation</h1>
  <p class="cover__org">Prepared on behalf of %s</p>
</section>

<dl class="meta-block">
  <dt>Report ID</dt>            <dd>%s</dd>
  <dt>Attestation Period</dt>   <dd>%s &nbsp;&mdash;&nbsp; %s</dd>
  <dt>As of</dt>                <dd>%s</dd>
  <dt>Prepared by</dt>          <dd>%s</dd>
  <dt>Platform</dt>             <dd>%s</dd>
</dl>

<section class="statement">
This attestation summarizes the cybersecurity risk posture of the
information technology assets managed by <strong>%s</strong> as of
<strong>%s</strong>, as measured by the Vigo fleet management platform. It
reflects vulnerability, configuration, compliance, and integrity telemetry
collected continuously from each managed host and aggregated over the
preceding 90 days. The data presented has not been altered or summarized
outside of the methodology described herein.
</section>
`,
		org,
		logoImg(report),
		org, // cover paragraph
		reportID, periodStart, periodEnd, asOf, preparedBy, generatedBy,
		org, asOf,
	)

	// Section 1: Current posture
	fmt.Fprintf(w, `<h2><span class="section-num">1.</span> Current Risk Posture</h2>
<div class="cards">
  <div class="card"><div class="value">%d</div><div class="label">Average Score</div></div>
  <div class="card"><div class="value">%d</div><div class="label">Max Score</div></div>
  <div class="card"><div class="value level-critical">%d</div><div class="label">Critical</div></div>
  <div class="card"><div class="value level-high">%d</div><div class="label">High</div></div>
  <div class="card"><div class="value level-medium">%d</div><div class="label">Medium</div></div>
  <div class="card"><div class="value level-low">%d</div><div class="label">Low</div></div>
</div>
`,
		int(report.CurrentPosture.AvgScore),
		report.CurrentPosture.MaxScore,
		report.CurrentPosture.Distribution[risk.LevelCritical],
		report.CurrentPosture.Distribution[risk.LevelHigh],
		report.CurrentPosture.Distribution[risk.LevelMedium],
		report.CurrentPosture.Distribution[risk.LevelLow])

	// Section 2: Top risks
	if len(report.CurrentPosture.TopRisks) > 0 {
		fmt.Fprint(w, `<h2><span class="section-num">2.</span> Highest Risk Assets</h2>
<table>
<thead><tr><th>Hostname</th><th>Score</th><th>Level</th></tr></thead>
<tbody>
`)
		for _, tr := range report.CurrentPosture.TopRisks {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td class="level-%s">%s</td></tr>
`, esc(tr.Hostname), tr.Score, esc(string(tr.Level)), esc(string(tr.Level)))
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Section 3: 90-day trend
	if len(report.Trend) > 0 {
		fmt.Fprint(w, `<h2><span class="section-num">3.</span> 90-Day Risk Trend</h2>
<table>
<thead><tr><th>Date</th><th>Avg Score</th><th>Max Score</th><th>Critical</th><th>High</th><th>Hosts</th></tr></thead>
<tbody>
`)
		for _, t := range report.Trend {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>
`, esc(t.Date), t.AvgScore, t.MaxScore, t.Critical, t.High, t.ScoredEnvoys)
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Section 4: Fleet composition
	fmt.Fprintf(w, `<h2><span class="section-num">4.</span> Fleet Composition</h2>
<p>Total managed hosts: <strong>%d</strong></p>
`, report.FleetSummary.TotalEnvoys)

	if len(report.FleetSummary.OSFamily) > 0 {
		fmt.Fprint(w, `<table>
<thead><tr><th>OS Family</th><th>Hosts</th></tr></thead>
<tbody>
`)
		for osf, count := range report.FleetSummary.OSFamily {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td></tr>
`, esc(osf), count)
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Section 5: Methodology + legend
	fmt.Fprint(w, `<section class="methodology">
<h2><span class="section-num">5.</span> Methodology</h2>
<p>Risk scores are computed per-host from: vulnerability scan results (CVE counts by severity), system hardening scores (Lynis), rootkit and malware detection (chkrootkit, rkhunter, ClamAV), file integrity monitoring (AIDE), unexpected open network ports, configuration convergence status, and host connectivity. Scores are integer values on a 0&ndash;100 scale, where 0 represents the lowest observed risk and 100 the highest.</p>
<p><strong>Risk levels:</strong></p>
<div class="legend">
  <span><strong class="level-low">Low</strong> &mdash; score 0&ndash;19</span>
  <span><strong class="level-medium">Medium</strong> &mdash; score 20&ndash;39</span>
  <span><strong class="level-high">High</strong> &mdash; score 40&ndash;69</span>
  <span><strong class="level-critical">Critical</strong> &mdash; score 70&ndash;100</span>
</div>
<p>This report was generated automatically by the Vigo platform and reflects the state of the managed fleet at the time of generation. Telemetry older than 24 hours from any individual host is excluded from the current posture summary. Historical trend data is drawn from daily snapshots retained in the Vigo audit record.</p>
</section>
`)

	// Signatory
	fmt.Fprint(w, `<section class="signatory">
<h2>Attestation</h2>
<p class="signatory__intro">I, the undersigned, attest that to the best of my knowledge the information contained in this report is a true and accurate reflection of the cybersecurity risk posture of the managed fleet identified above as of the date shown.</p>
<div class="sig-grid">
  <div class="sig-field"><div class="sig-field__line"></div><div class="sig-field__label">Signature</div></div>
  <div class="sig-field"><div class="sig-field__line"></div><div class="sig-field__label">Date</div></div>
  <div class="sig-field"><div class="sig-field__line"></div><div class="sig-field__label">Name (Printed)</div></div>
  <div class="sig-field"><div class="sig-field__line"></div><div class="sig-field__label">Title</div></div>
</div>
</section>
`)

	// Footer with audit metadata
	fmt.Fprintf(w, `<footer class="doc-footer">
  <div>Document ID: <strong>%s</strong></div>
  <div class="right">Generated %s by %s</div>
</footer>
</body></html>`, reportID, generatedAt, generatedBy)
}

// envoyInputs converts a slice of fleet.EnvoyState to risk.EnvoyInput.
func envoyInputs(states []fleet.EnvoyState) []risk.EnvoyInput {
	inputs := make([]risk.EnvoyInput, len(states))
	for i, es := range states {
		inputs[i] = envoyToInput(es)
	}
	return inputs
}

func envoyToInput(es fleet.EnvoyState) risk.EnvoyInput {
	return risk.EnvoyInput{
		ID:                es.ID,
		Hostname:          es.Hostname,
		Revoked:           es.Revoked,
		LastSeen:          es.LastSeen,
		ConvergenceStatus: es.ConvergenceStatus,
		Traits:            es.Traits,
	}
}

// riskLevelClass returns a CSS class for the risk level.
func riskLevelClass(level risk.Level) string {
	return "level-" + strings.ToLower(string(level))
}
