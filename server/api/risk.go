// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/risk"
)

// RiskHandler serves fleet and per-envoy risk scoring endpoints.
type RiskHandler struct {
	fleetIndex   *fleet.FleetIndex
	queries      *db.Queries
	baseInterval time.Duration
	version      string
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

// RegisterRoutes mounts risk endpoints.
func (h *RiskHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/risk", h.handleFleetRisk)
	mux.HandleFunc("GET /api/v1/risk/history", h.handleRiskHistory)
	mux.HandleFunc("GET /api/v1/risk/insurance", h.handleInsuranceExport)
	mux.HandleFunc("GET /api/v1/risk/insurance.html", h.handleInsuranceExportHTML)
	mux.HandleFunc("GET /api/v1/risk/{envoyID}", h.handleEnvoyRisk)
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
	GeneratedAt    string           `json:"generated_at"`
	GeneratedBy    string           `json:"generated_by"`
	PeriodStart    string           `json:"period_start"`
	PeriodEnd      string           `json:"period_end"`
	CurrentPosture risk.FleetRisk   `json:"current_posture"`
	Trend          []TrendEntry     `json:"trend"`
	FleetSummary   InsuranceFleet   `json:"fleet_summary"`
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

func (h *RiskHandler) buildInsuranceReport() (*InsuranceReport, error) {
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

	return &InsuranceReport{
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
	}, nil
}

func (h *RiskHandler) handleInsuranceExport(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildInsuranceReport()
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
	report, err := h.buildInsuranceReport()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vigo-cyber-insurance-%s.html"`, time.Now().Format("2006-01-02")))
	}

	// Risk level color.
	avgColor := "#27ae60"
	if report.CurrentPosture.AvgScore >= 40 {
		avgColor = "#f39c12"
	}
	if report.CurrentPosture.AvgScore >= 70 {
		avgColor = "#e74c3c"
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cyber Insurance Risk Attestation</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; padding: 2rem; background: #fff; color: #1a1a2e; line-height: 1.6; }
  .header { display: flex; justify-content: space-between; align-items: center; border-bottom: 2px solid #1a1a2e; padding-bottom: 1rem; margin-bottom: 2rem; }
  .header h1 { margin: 0; font-size: 1.5rem; }
  .header .meta { text-align: right; font-size: 0.85rem; color: #6c757d; }
  .cards { display: flex; gap: 1.5rem; margin-bottom: 2rem; flex-wrap: wrap; }
  .card { flex: 1; min-width: 140px; background: #f8f9fa; border: 1px solid #dee2e6; border-radius: 8px; padding: 1rem; text-align: center; }
  .card .value { font-size: 2rem; font-weight: 700; }
  .card .label { font-size: 0.85rem; color: #6c757d; }
  table { width: 100%%; border-collapse: collapse; margin-bottom: 2rem; }
  th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid #dee2e6; }
  th { background: #f8f9fa; font-weight: 600; font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .level-critical { color: #e74c3c; font-weight: 600; }
  .level-high { color: #f39c12; font-weight: 600; }
  .level-medium { color: #3498db; }
  .level-low { color: #27ae60; }
  .footer { margin-top: 3rem; padding-top: 1rem; border-top: 1px solid #dee2e6; font-size: 0.8rem; color: #6c757d; }
  @media print { body { padding: 1rem; } .header { page-break-after: avoid; } }
</style>
</head>
<body>
<div class="header">
  <h1>Cyber Insurance Risk Attestation</h1>
  <div class="meta">
    <div>Generated: %s</div>
    <div>Period: %s to %s</div>
    <div>%s</div>
  </div>
</div>
`, report.GeneratedAt[:10], report.PeriodStart, report.PeriodEnd, report.GeneratedBy)

	// Summary cards.
	fmt.Fprintf(w, `<h2>Current Risk Posture</h2>
<div class="cards">
  <div class="card"><div class="value" style="color:%s">%d</div><div class="label">Average Score</div></div>
  <div class="card"><div class="value">%d</div><div class="label">Max Score</div></div>
  <div class="card"><div class="value level-critical">%d</div><div class="label">Critical</div></div>
  <div class="card"><div class="value level-high">%d</div><div class="label">High</div></div>
  <div class="card"><div class="value level-medium">%d</div><div class="label">Medium</div></div>
  <div class="card"><div class="value level-low">%d</div><div class="label">Low</div></div>
</div>
`, avgColor, int(report.CurrentPosture.AvgScore), report.CurrentPosture.MaxScore,
		report.CurrentPosture.Distribution[risk.LevelCritical],
		report.CurrentPosture.Distribution[risk.LevelHigh],
		report.CurrentPosture.Distribution[risk.LevelMedium],
		report.CurrentPosture.Distribution[risk.LevelLow])

	// Top risks table.
	if len(report.CurrentPosture.TopRisks) > 0 {
		fmt.Fprint(w, `<h2>Highest Risk Assets</h2>
<table>
<thead><tr><th>Hostname</th><th>Score</th><th>Level</th></tr></thead>
<tbody>
`)
		for _, tr := range report.CurrentPosture.TopRisks {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td class="level-%s">%s</td></tr>
`, tr.Hostname, tr.Score, tr.Level, tr.Level)
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Trend table.
	if len(report.Trend) > 0 {
		fmt.Fprint(w, `<h2>90-Day Risk Trend</h2>
<table>
<thead><tr><th>Date</th><th>Avg Score</th><th>Max Score</th><th>Critical</th><th>High</th><th>Hosts</th></tr></thead>
<tbody>
`)
		for _, t := range report.Trend {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>
`, t.Date, t.AvgScore, t.MaxScore, t.Critical, t.High, t.ScoredEnvoys)
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Fleet summary.
	fmt.Fprintf(w, `<h2>Fleet Composition</h2>
<p>Total managed hosts: <strong>%d</strong></p>
`, report.FleetSummary.TotalEnvoys)

	if len(report.FleetSummary.OSFamily) > 0 {
		fmt.Fprint(w, `<table>
<thead><tr><th>OS Family</th><th>Count</th></tr></thead>
<tbody>
`)
		for osf, count := range report.FleetSummary.OSFamily {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td></tr>
`, osf, count)
		}
		fmt.Fprint(w, `</tbody></table>
`)
	}

	// Methodology footer.
	fmt.Fprint(w, `<div class="footer">
<h3>Methodology</h3>
<p>Risk scores are computed per-host from: vulnerability scan results (CVE counts by severity),
system hardening scores (Lynis), rootkit/malware detection (chkrootkit, rkhunter, ClamAV),
file integrity monitoring (AIDE), open network ports, configuration convergence status,
and host connectivity. Scores range from 0 (lowest risk) to 100 (highest risk).
Levels: Low (0-19), Medium (20-39), High (40-69), Critical (70-100).</p>
<p>This report was generated automatically by Vigo and reflects the state of the fleet
at the time of generation. It is intended for use in cyber insurance risk assessments.</p>
</div>
</body></html>`)
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
