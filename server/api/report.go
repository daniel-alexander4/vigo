// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/hobnob/hobnob/server/audit"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
)

// ReportHandler generates compliance reports in HTML and JSON formats.
type ReportHandler struct {
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	configStore *config.ConfigStore
	version     string
}

// NewReportHandler creates a ReportHandler.
func NewReportHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, configStore *config.ConfigStore, version string) *ReportHandler {
	return &ReportHandler{
		queries:     queries,
		fleetIndex:  fleetIndex,
		configStore: configStore,
		version:     version,
	}
}

// RegisterRoutes mounts report endpoints.
func (h *ReportHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/report/compliance", h.handleComplianceReport)
	mux.HandleFunc("GET /api/v1/report/compliance.html", h.handleComplianceReportHTML)
}

// ComplianceReport is the JSON format for a compliance report.
type ComplianceReport struct {
	Title       string                `json:"title"`
	GeneratedAt string                `json:"generated_at"`
	GeneratedBy string                `json:"generated_by"`
	Version     string                `json:"version"`
	Summary     ComplianceReportSummary `json:"summary"`
	Controls    []ControlFinding      `json:"controls"`
	Fleet       []FleetNodeStatus     `json:"fleet"`
	AuditChain  AuditChainStatus      `json:"audit_chain"`
}

// ComplianceReportSummary is the top-level compliance summary.
type ComplianceReportSummary struct {
	TotalNodes      int     `json:"total_nodes"`
	Compliant       int     `json:"compliant"`
	RecurringDrift  int     `json:"recurring_drift"`
	Errors          int     `json:"errors"`
	LostContact     int     `json:"lost_contact"`
	NoData          int     `json:"no_data"`
	CompliancePercent float64 `json:"compliance_percent"`
}

// ControlFinding maps a HIPAA control to its implementation status.
type ControlFinding struct {
	ControlID   string `json:"control_id"`
	Title       string `json:"title"`
	Status      string `json:"status"` // "satisfied", "partial", "not_satisfied"
	Evidence    string `json:"evidence"`
}

// FleetNodeStatus is a per-node compliance entry.
type FleetNodeStatus struct {
	Hostname   string `json:"hostname"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	LastSeen   string `json:"last_seen"`
	OS         string `json:"os,omitempty"`
	IP         string `json:"ip,omitempty"`
}

// AuditChainStatus is the integrity status of the audit chain.
type AuditChainStatus struct {
	Valid      bool  `json:"valid"`
	EntryCount int64 `json:"entry_count"`
	Error      string `json:"error,omitempty"`
}

func (h *ReportHandler) buildReport(r *http.Request) (*ComplianceReport, error) {
	now := time.Now().UTC()

	// Fleet summary.
	summary := h.fleetIndex.ComplianceSummary()
	total := summary.Total
	pct := float64(0)
	if total > 0 {
		pct = float64(summary.Compliant) / float64(total) * 100
	}

	// Audit chain verification.
	var auditStatus AuditChainStatus
	verifyResult, err := audit.Verify(r.Context(), h.queries)
	if err == nil {
		auditStatus.Valid = verifyResult.Valid
		auditStatus.EntryCount = verifyResult.EntryCount
		auditStatus.Error = verifyResult.Error
	}

	// HIPAA control mappings.
	controls := h.buildControlFindings(summary, auditStatus)

	// Per-node status.
	var nodes []FleetNodeStatus
	for _, t := range h.fleetIndex.AllThralls() {
		if t.Revoked {
			continue
		}
		node := FleetNodeStatus{
			Hostname: t.Hostname,
			ID:       t.ID,
			Status:   t.ComplianceStatus,
			LastSeen: t.LastSeen.Format(time.RFC3339),
			IP:       t.IP,
		}
		if family, ok := t.Traits["os.family"]; ok {
			node.OS = family
		}
		nodes = append(nodes, node)
	}

	return &ComplianceReport{
		Title:       "Hobnob HIPAA Compliance Report",
		GeneratedAt: now.Format(time.RFC3339),
		GeneratedBy: "Hobnob " + h.version,
		Version:     h.version,
		Summary: ComplianceReportSummary{
			TotalNodes:      int(total),
			Compliant:       int(summary.Compliant),
			RecurringDrift:  int(summary.RecurringDrift),
			Errors:          int(summary.Errors),
			LostContact:     int(summary.LostContact),
			NoData:          int(summary.NoData),
			CompliancePercent: pct,
		},
		Controls:   controls,
		Fleet:      nodes,
		AuditChain: auditStatus,
	}, nil
}

func (h *ReportHandler) buildControlFindings(summary fleet.ComplianceCounters, auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		{ControlID: "164.312(a)(1)", Title: "Access Control", Status: "satisfied", Evidence: "Authentication required on all access, RBAC with fine-grained permissions"},
		{ControlID: "164.312(a)(2)(i)", Title: "Unique User Identification", Status: "satisfied", Evidence: "Per-user accounts with unique UUIDs"},
		{ControlID: "164.312(a)(2)(ii)", Title: "Emergency Access Procedure", Status: "satisfied", Evidence: "Localhost trust with audited emergency-access CLI command"},
		{ControlID: "164.312(a)(2)(iii)", Title: "Automatic Logoff", Status: "satisfied", Evidence: "15-minute idle session timeout (configurable)"},
		{ControlID: "164.312(a)(2)(iv)", Title: "Encryption and Decryption", Status: "satisfied", Evidence: "AES-256-GCM for secrets, backup encryption, LUKS recommended for DB"},
		{ControlID: "164.312(b)", Title: "Audit Controls", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "164.312(c)(1)", Title: "Integrity", Status: "satisfied", Evidence: "ED25519 signatures, SHA-256 hash chain tamper detection"},
		{ControlID: "164.312(d)", Title: "Person or Entity Authentication", Status: "satisfied", Evidence: "Basic (bcrypt), OIDC, isowebauth; TOTP MFA available"},
		{ControlID: "164.312(e)(1)", Title: "Transmission Security", Status: "satisfied", Evidence: "mTLS (TLS 1.3) on all gRPC and REST traffic"},
		{ControlID: "164.312(e)(2)(ii)", Title: "Encryption (Transmission)", Status: "satisfied", Evidence: "No plaintext transport — mTLS enforced"},
		{ControlID: "164.310(d)(2)(i)", Title: "Disposal", Status: "satisfied", Evidence: "Documented decommissioning procedures with cascade delete"},
		{ControlID: "164.308(a)(5)(ii)(D)", Title: "Password Management", Status: "satisfied", Evidence: "12-char minimum, complexity rules, bcrypt hashing"},
		{ControlID: "164.308(a)(6)", Title: "Security Incident Procedures", Status: "satisfied", Evidence: "Documented incident response plan with 5 phases"},
		{ControlID: "164.308(b)", Title: "Business Associate Contracts", Status: "partial", Evidence: "BAA template provided — requires execution per deployment"},
	}
}

func statusForAudit(a AuditChainStatus) string {
	if a.Valid {
		return "satisfied"
	}
	return "not_satisfied"
}

func (h *ReportHandler) handleComplianceReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildReport(r)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build report: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

func (h *ReportHandler) handleComplianceReportHTML(w http.ResponseWriter, r *http.Request) {
	report, err := h.buildReport(r)
	if err != nil {
		http.Error(w, "failed to build report: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Determine if download or inline viewing.
	if r.URL.Query().Get("download") == "1" {
		filename := fmt.Sprintf("hobnob-compliance-%s.html", time.Now().Format("2006-01-02"))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	}

	renderComplianceHTML(w, report)
}

func renderComplianceHTML(w http.ResponseWriter, r *ComplianceReport) {
	// Gauge color.
	gaugeColor := "#27ae60" // green
	if r.Summary.CompliancePercent < 80 {
		gaugeColor = "#f39c12" // yellow
	}
	if r.Summary.CompliancePercent < 50 {
		gaugeColor = "#e74c3c" // red
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; padding: 2rem; background: #fff; color: #1a1a2e; line-height: 1.6; }
  .header { display: flex; justify-content: space-between; align-items: center; border-bottom: 2px solid #1a1a2e; padding-bottom: 1rem; margin-bottom: 2rem; }
  .header h1 { margin: 0; font-size: 1.5rem; }
  .header .meta { text-align: right; font-size: 0.85rem; color: #6c757d; }
  .summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
  .summary-card { background: #f8f9fa; border-radius: 8px; padding: 1rem; text-align: center; }
  .summary-card .value { font-size: 2rem; font-weight: bold; }
  .summary-card .label { font-size: 0.8rem; color: #6c757d; text-transform: uppercase; }
  table { width: 100%%; border-collapse: collapse; margin-bottom: 2rem; }
  th, td { padding: 0.6rem 0.8rem; text-align: left; border-bottom: 1px solid #dee2e6; }
  th { background: #f8f9fa; font-weight: 600; font-size: 0.85rem; text-transform: uppercase; color: #6c757d; }
  .badge { display: inline-block; padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
  .badge-satisfied { background: #d4edda; color: #155724; }
  .badge-partial { background: #fff3cd; color: #856404; }
  .badge-not_satisfied { background: #f8d7da; color: #721c24; }
  .badge-compliant { background: #d4edda; color: #155724; }
  .badge-errors, .badge-recurring-drift, .badge-lost-contact, .badge-no-data { background: #f8d7da; color: #721c24; }
  .audit-status { padding: 1rem; border-radius: 8px; margin-bottom: 2rem; }
  .audit-valid { background: #d4edda; }
  .audit-broken { background: #f8d7da; }
  .footer { margin-top: 2rem; padding-top: 1rem; border-top: 1px solid #dee2e6; font-size: 0.8rem; color: #6c757d; text-align: center; }
  @media print { body { padding: 0.5rem; } .header { page-break-after: avoid; } table { page-break-inside: auto; } tr { page-break-inside: avoid; } }
</style>
</head>
<body>
<div class="header">
  <h1>%s</h1>
  <div class="meta">
    Generated: %s<br>
    %s
  </div>
</div>
`, html.EscapeString(r.Title), html.EscapeString(r.Title), r.GeneratedAt, html.EscapeString(r.GeneratedBy))

	// Summary cards.
	fmt.Fprintf(w, `<div class="summary">
  <div class="summary-card"><div class="value" style="color:%s">%.0f%%</div><div class="label">Compliance</div></div>
  <div class="summary-card"><div class="value">%d</div><div class="label">Total Nodes</div></div>
  <div class="summary-card"><div class="value" style="color:#27ae60">%d</div><div class="label">Compliant</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Errors</div></div>
  <div class="summary-card"><div class="value" style="color:#f39c12">%d</div><div class="label">Recurring Drift</div></div>
  <div class="summary-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Lost Contact</div></div>
</div>
`, gaugeColor, r.Summary.CompliancePercent, r.Summary.TotalNodes, r.Summary.Compliant, r.Summary.Errors, r.Summary.RecurringDrift, r.Summary.LostContact)

	// Audit chain status.
	auditClass := "audit-valid"
	auditText := fmt.Sprintf("Audit chain verified — %d entries, integrity intact.", r.AuditChain.EntryCount)
	if !r.AuditChain.Valid {
		auditClass = "audit-broken"
		auditText = fmt.Sprintf("AUDIT CHAIN BROKEN — %s", r.AuditChain.Error)
	}
	fmt.Fprintf(w, `<div class="audit-status %s"><strong>Audit Trail:</strong> %s</div>`, auditClass, html.EscapeString(auditText))

	// Controls table.
	fmt.Fprint(w, `<h2>HIPAA Security Rule Controls</h2>
<table>
<thead><tr><th>Control</th><th>Title</th><th>Status</th><th>Evidence</th></tr></thead>
<tbody>
`)
	for _, c := range r.Controls {
		badgeClass := "badge-" + c.Status
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td><span class="badge %s">%s</span></td><td>%s</td></tr>
`, html.EscapeString(c.ControlID), html.EscapeString(c.Title), badgeClass, html.EscapeString(c.Status), html.EscapeString(c.Evidence))
	}
	fmt.Fprint(w, `</tbody></table>`)

	// Fleet table.
	if len(r.Fleet) > 0 {
		fmt.Fprint(w, `<h2>Fleet Status</h2>
<table>
<thead><tr><th>Hostname</th><th>Status</th><th>OS</th><th>IP</th><th>Last Seen</th></tr></thead>
<tbody>
`)
		for _, n := range r.Fleet {
			badgeClass := "badge-compliant"
			if n.Status != "compliant" {
				badgeClass = "badge-" + strings.ReplaceAll(n.Status, " ", "-")
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td><span class="badge %s">%s</span></td><td>%s</td><td>%s</td><td>%s</td></tr>
`, html.EscapeString(n.Hostname), badgeClass, html.EscapeString(n.Status), html.EscapeString(n.OS), html.EscapeString(n.IP), n.LastSeen)
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	fmt.Fprintf(w, `<div class="footer">Hobnob Compliance Report &mdash; Generated %s &mdash; %s</div>
</body></html>`, r.GeneratedAt, html.EscapeString(r.GeneratedBy))
}
