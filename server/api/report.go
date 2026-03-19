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
	mux.HandleFunc("GET /api/v1/report/hitrust", h.handleHITRUSTReport)
	mux.HandleFunc("GET /api/v1/report/hitrust.html", h.handleHITRUSTReportHTML)
	mux.HandleFunc("GET /api/v1/report/soc2", h.handleSOC2Report)
	mux.HandleFunc("GET /api/v1/report/soc2.html", h.handleSOC2ReportHTML)
	mux.HandleFunc("GET /api/v1/report/pci", h.handlePCIReport)
	mux.HandleFunc("GET /api/v1/report/pci.html", h.handlePCIReportHTML)
	mux.HandleFunc("GET /api/v1/report/cis", h.handleCISReport)
	mux.HandleFunc("GET /api/v1/report/cis.html", h.handleCISReportHTML)
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

func (h *ReportHandler) buildReport(r *http.Request, title string, controls []ControlFinding) (*ComplianceReport, error) {
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
		Title:       title,
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

func (h *ReportHandler) hipaaControls(r *http.Request) []ControlFinding {
	var auditStatus AuditChainStatus
	if v, err := audit.Verify(r.Context(), h.queries); err == nil {
		auditStatus.Valid = v.Valid
		auditStatus.EntryCount = v.EntryCount
		auditStatus.Error = v.Error
	}
	return h.buildControlFindings(h.fleetIndex.ComplianceSummary(), auditStatus)
}

func (h *ReportHandler) handleComplianceReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob HIPAA Compliance Report", "hobnob-compliance",
		h.hipaaControls(r), false)
}

func (h *ReportHandler) handleComplianceReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob HIPAA Compliance Report", "hobnob-compliance",
		h.hipaaControls(r), true)
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

// ---------- HITRUST CSF ----------

// buildHITRUSTFindings returns HITRUST CSF control findings mapped to Hobnob capabilities.
func (h *ReportHandler) buildHITRUSTFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Domain 01 — Access Control
		{ControlID: "01.b", Title: "User Registration", Status: "satisfied", Evidence: "Per-user accounts with unique UUIDs, RBAC roles, admin/viewer + 13 fine-grained permissions"},
		{ControlID: "01.c", Title: "Privilege Management", Status: "satisfied", Evidence: "Fine-grained RBAC: 13 permissions (fleet.read, config.publish, users.manage, etc.), role-based defaults"},
		{ControlID: "01.d", Title: "User Password Management", Status: "satisfied", Evidence: "12-char minimum, complexity rules (upper/lower/digit/special), bcrypt hashing, secrets provider encryption"},
		{ControlID: "01.i", Title: "Policy on Use of Network Services", Status: "satisfied", Evidence: "mTLS enforced on all gRPC/REST traffic, no plaintext transport"},
		{ControlID: "01.j", Title: "User Authentication for External Connections", Status: "satisfied", Evidence: "mTLS certificates, ED25519 agent signatures, TOTP MFA available"},
		{ControlID: "01.n", Title: "Network Connection Control", Status: "satisfied", Evidence: "Firewall executor enforces host-based firewall rules (iptables, nftables, pf, Windows Firewall)"},
		{ControlID: "01.v", Title: "Information Access Restriction", Status: "satisfied", Evidence: "File executor enforces permissions, ownership, and mode on all managed files"},

		// Domain 06 — Compliance
		{ControlID: "06.c", Title: "Protection of Organizational Records", Status: "satisfied", Evidence: "Encrypted backups (AES-256-GCM), SHA-256 integrity verification, file snapshots in DB"},
		{ControlID: "06.g", Title: "Compliance with Security Policies", Status: "satisfied", Evidence: "Continuous drift detection with 5-level compliance status, fleet-wide dashboards"},
		{ControlID: "06.h", Title: "Technical Compliance Checking", Status: "satisfied", Evidence: "Automated convergence checks on every agent check-in, recurring drift detection (3-run threshold)"},
		{ControlID: "06.i", Title: "Information Systems Audit Controls", Status: statusForAudit(auditStatus), Evidence: auditEvidence},

		// Domain 07 — Asset Management
		{ControlID: "07.a", Title: "Inventory of Assets", Status: "satisfied", Evidence: "25 trait collectors auto-discover hardware, OS, network, packages; FleetIndex provides real-time inventory"},

		// Domain 09 — Communications and Operations Management
		{ControlID: "09.a", Title: "Documented Operating Procedures", Status: "satisfied", Evidence: "Config-as-code: YAML modules define desired state, version-controlled, auditable"},
		{ControlID: "09.b", Title: "Change Management", Status: "satisfied", Evidence: "Stage → publish workflow, git-tracked changes, modlint validation, tamper-evident audit trail"},
		{ControlID: "09.c", Title: "Segregation of Duties", Status: "satisfied", Evidence: "RBAC with admin/viewer roles + fine-grained permissions, change approval workflow (two-person rule)"},
		{ControlID: "09.d", Title: "Separation of Environments", Status: "satisfied", Evidence: "Environment-specific configs via environment_overrides, conditional variables per environment"},
		{ControlID: "09.i", Title: "Protection Against Malicious Code", Status: "satisfied", Evidence: "Package executor enforces AV/EDR packages, service executor ensures they're running"},
		{ControlID: "09.k", Title: "Back-up", Status: "satisfied", Evidence: "Litestream continuous WAL replication, on-demand snapshots, AES-256-GCM encrypted backups"},
		{ControlID: "09.l", Title: "Network Security Management", Status: "satisfied", Evidence: "Firewall executor (iptables/nftables/pf/Windows), mTLS on all management traffic"},
		{ControlID: "09.q", Title: "Audit Logging", Status: statusForAudit(auditStatus), Evidence: "SHA-256 hash chain audit trail: auth, user, token, config, push, workflow, emergency, remediation events"},
		{ControlID: "09.s", Title: "Protection of Log Information", Status: "satisfied", Evidence: "Audit chain is tamper-evident (SHA-256 hash chain), immutable once written"},
		{ControlID: "09.v", Title: "Clock Synchronization", Status: "satisfied", Evidence: "Package/service executors can enforce NTP/chrony across fleet"},

		// Domain 10 — Systems Acquisition, Development, Maintenance
		{ControlID: "10.f", Title: "Cryptographic Controls", Status: "satisfied", Evidence: "AES-256-GCM secrets, mTLS (TLS 1.3), ED25519 signatures, Argon2id key derivation"},
		{ControlID: "10.g", Title: "Key Management", Status: "satisfied", Evidence: "Auto-generated master key, secret rotation via watcher, certificate deployment and renewal"},
		{ControlID: "10.h", Title: "Control of Operational Software", Status: "satisfied", Evidence: "Package executor enforces versions, repository executor controls sources"},
		{ControlID: "10.k", Title: "Change Control Procedures", Status: "satisfied", Evidence: "Stage/live separation, modlint validation, change approval, blast radius limits, auto-rollback"},
		{ControlID: "10.l", Title: "Technical Review After OS Changes", Status: "satisfied", Evidence: "Automatic reconvergence detects drift after OS updates on next check-in"},
		{ControlID: "10.m", Title: "Restrictions on Software Changes", Status: "satisfied", Evidence: "Package pinning prevents unauthorized version changes, repository enforcement controls sources"},

		// Domain 11 — Incident Management
		{ControlID: "11.a", Title: "Reporting Security Events", Status: "satisfied", Evidence: "Webhook + SMTP notifications for compliance drift, threshold breaches, stale nodes"},
		{ControlID: "11.e", Title: "Collection of Evidence", Status: "satisfied", Evidence: "Run history, file snapshots, compliance history, tamper-evident audit chain"},

		// Domain 12 — Business Continuity
		{ControlID: "12.c", Title: "Developing and Implementing BCPs", Status: "satisfied", Evidence: "Config-as-code enables rapid rebuild; backup + restore tested; disaster recovery documented"},

		// Domain 16 — Endpoint Protection
		{ControlID: "16.a", Title: "Endpoint Protection", Status: "satisfied", Evidence: "Package/service executors enforce AV/EDR/firewall on all managed endpoints"},
	}
}

// serveFrameworkReport handles JSON and HTML report generation for any framework.
func (h *ReportHandler) serveFrameworkReport(w http.ResponseWriter, r *http.Request, title, filePrefix string, controls []ControlFinding, asHTML bool) {
	report, err := h.buildReport(r, title, controls)
	if err != nil {
		if asHTML {
			http.Error(w, "failed to build report: "+err.Error(), http.StatusInternalServerError)
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to build report: "+err.Error())
		}
		return
	}
	if asHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Query().Get("download") == "1" {
			filename := fmt.Sprintf("%s-%s.html", filePrefix, time.Now().Format("2006-01-02"))
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		}
		renderComplianceHTML(w, report)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

// auditStatusFromRequest verifies the audit chain and returns the status.
func (h *ReportHandler) auditStatusFromRequest(r *http.Request) AuditChainStatus {
	var s AuditChainStatus
	if v, err := audit.Verify(r.Context(), h.queries); err == nil {
		s.Valid = v.Valid
		s.EntryCount = v.EntryCount
		s.Error = v.Error
	}
	return s
}

func (h *ReportHandler) handleHITRUSTReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob HITRUST CSF Compliance Report", "hobnob-hitrust",
		h.buildHITRUSTFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleHITRUSTReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob HITRUST CSF Compliance Report", "hobnob-hitrust",
		h.buildHITRUSTFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- SOC 2 Trust Services Criteria ----------

// buildSOC2Findings returns SOC 2 Trust Services Criteria findings.
func (h *ReportHandler) buildSOC2Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// CC1 — Control Environment
		{ControlID: "CC1.1", Title: "COSO Principle 1: Integrity and Ethics", Status: "partial", Evidence: "Tamper-evident audit trail enforces accountability; organizational ethics policy required"},
		{ControlID: "CC1.2", Title: "COSO Principle 2: Board Oversight", Status: "partial", Evidence: "Compliance dashboards provide oversight visibility; board governance is organizational"},
		{ControlID: "CC1.3", Title: "COSO Principle 3: Management Structure", Status: "satisfied", Evidence: "RBAC with 13 fine-grained permissions, admin/viewer roles, segregation of duties"},

		// CC2 — Communication and Information
		{ControlID: "CC2.1", Title: "Internal Communication of Objectives", Status: "satisfied", Evidence: "Config-as-code documents desired state; drift detection identifies deviations"},
		{ControlID: "CC2.2", Title: "Internal Communication of Policies", Status: "satisfied", Evidence: "YAML modules are the executable policy; version-controlled, auditable"},

		// CC3 — Risk Assessment
		{ControlID: "CC3.1", Title: "Risk Identification and Analysis", Status: "satisfied", Evidence: "Continuous drift detection, 5-level compliance status, fleet-wide dashboards"},
		{ControlID: "CC3.2", Title: "Fraud Risk Assessment", Status: "partial", Evidence: "Tamper-evident audit trail detects unauthorized changes; fraud risk assessment is organizational"},
		{ControlID: "CC3.3", Title: "Change Risk Assessment", Status: "satisfied", Evidence: "Blast radius limits, compliance threshold monitoring, auto-rollback on regression"},

		// CC4 — Monitoring Activities
		{ControlID: "CC4.1", Title: "Ongoing and Separate Evaluations", Status: "satisfied", Evidence: "Continuous convergence checks on every agent check-in; compliance monitor runs every 60s"},
		{ControlID: "CC4.2", Title: "Communication of Deficiencies", Status: "satisfied", Evidence: "Webhook + SMTP alerts on compliance threshold breaches, drift detection, stale nodes"},

		// CC5 — Control Activities
		{ControlID: "CC5.1", Title: "Selection of Control Activities", Status: "satisfied", Evidence: "Idempotent resource executors enforce desired state; 73 built-in executor types"},
		{ControlID: "CC5.2", Title: "Technology General Controls", Status: "satisfied", Evidence: "mTLS, ED25519 signatures, AES-256-GCM encryption, bcrypt password hashing"},
		{ControlID: "CC5.3", Title: "Deployment of Control Activities", Status: "satisfied", Evidence: "Stage → publish workflow with modlint validation, change approval, audit trail"},

		// CC6 — Logical and Physical Access Controls
		{ControlID: "CC6.1", Title: "Logical Access Security", Status: "satisfied", Evidence: "Authentication required (basic/OIDC/isowebauth), TOTP MFA, 15-min idle timeout"},
		{ControlID: "CC6.2", Title: "User Registration and Authorization", Status: "satisfied", Evidence: "Per-user accounts with unique UUIDs, role-based defaults, admin approval required"},
		{ControlID: "CC6.3", Title: "Role-Based Access and Least Privilege", Status: "satisfied", Evidence: "13 fine-grained permissions: fleet.read, config.publish, users.manage, etc."},
		{ControlID: "CC6.6", Title: "Restriction of Access to System Assets", Status: "satisfied", Evidence: "File executor enforces permissions/ownership; firewall executor enforces network access"},
		{ControlID: "CC6.7", Title: "Management of Credentials", Status: "satisfied", Evidence: "12-char complexity, bcrypt hashing, AES-256-GCM encrypted storage, API tokens (hbt_ prefix)"},
		{ControlID: "CC6.8", Title: "Prevention of Unauthorized Software", Status: "satisfied", Evidence: "Package executor enforces versions; repository executor controls sources"},

		// CC7 — System Operations
		{ControlID: "CC7.1", Title: "Detection of Security Events", Status: "satisfied", Evidence: "Continuous drift detection, compliance monitoring, failed login tracking with IP"},
		{ControlID: "CC7.2", Title: "Monitoring for Anomalies", Status: "satisfied", Evidence: "Recurring drift detection (3-run threshold), compliance threshold alerts, stale node detection"},
		{ControlID: "CC7.3", Title: "Evaluation of Security Events", Status: "satisfied", Evidence: "Tamper-evident audit trail with event classification; SIEM export for external analysis"},
		{ControlID: "CC7.4", Title: "Response to Security Events", Status: "satisfied", Evidence: "Documented incident response plan; emergency access procedure; remediation workflow"},
		{ControlID: "CC7.5", Title: "Recovery from Security Events", Status: "satisfied", Evidence: "Encrypted backups, disaster recovery procedures, config-as-code rapid rebuild"},

		// CC8 — Change Management
		{ControlID: "CC8.1", Title: "Change Management Process", Status: "satisfied", Evidence: "Stage → live separation, git-tracked changes, modlint validation, change approval workflow"},

		// CC9 — Risk Mitigation
		{ControlID: "CC9.1", Title: "Risk Mitigation Activities", Status: "satisfied", Evidence: "Automated remediation workflow, blast radius limits, auto-rollback on compliance drop"},

		// A1 — Availability
		{ControlID: "A1.1", Title: "Availability Commitments", Status: "satisfied", Evidence: "Litestream continuous backup, disaster recovery docs, peer replication for HA"},
		{ControlID: "A1.2", Title: "Environmental Protections", Status: "partial", Evidence: "Agent offline convergence (cached policy); physical protections are organizational"},

		// C1 — Confidentiality
		{ControlID: "C1.1", Title: "Identification of Confidential Information", Status: "satisfied", Evidence: "secret: prefix identifies sensitive values; never appear in logs, config, or DB plaintext"},
		{ControlID: "C1.2", Title: "Disposal of Confidential Information", Status: "satisfied", Evidence: "Documented decommissioning procedures with cascade delete and secure media disposal"},

		// PI1 — Processing Integrity
		{ControlID: "PI1.1", Title: "Data Processing Accuracy", Status: "satisfied", Evidence: "Idempotent executors check before acting; ED25519 signatures verify data integrity"},
		{ControlID: "PI1.2", Title: "Monitoring of Processing", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
	}
}

func (h *ReportHandler) handleSOC2Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob SOC 2 Trust Services Criteria Report", "hobnob-soc2",
		h.buildSOC2Findings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleSOC2ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob SOC 2 Trust Services Criteria Report", "hobnob-soc2",
		h.buildSOC2Findings(h.auditStatusFromRequest(r)), true)
}

// ---------- PCI DSS v4.0 ----------

// buildPCIDSSFindings returns PCI DSS v4.0 control findings.
func (h *ReportHandler) buildPCIDSSFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Req 1 — Network Security Controls
		{ControlID: "1.2.1", Title: "Network Security Controls Configured", Status: "satisfied", Evidence: "Firewall executor enforces iptables/nftables/pf/Windows Firewall rules across fleet"},
		{ControlID: "1.2.5", Title: "Ports, Protocols, Services Restricted", Status: "satisfied", Evidence: "Firewall rules restrict services to only those needed; managed via firewall resources"},
		{ControlID: "1.3.1", Title: "Inbound Traffic Restricted", Status: "satisfied", Evidence: "Firewall executor enforces inbound rules; mTLS required for all management traffic"},
		{ControlID: "1.4.1", Title: "NSCs Between Trusted and Untrusted", Status: "satisfied", Evidence: "Network segmentation enforceable via firewall resources and sysctl net parameters"},

		// Req 2 — Secure Configurations
		{ControlID: "2.2.1", Title: "Configuration Standards Developed", Status: "satisfied", Evidence: "YAML modules define desired state as configuration standards; version-controlled, auditable"},
		{ControlID: "2.2.2", Title: "Vendor Defaults Changed", Status: "satisfied", Evidence: "User executor removes/disables default accounts; file executor enforces custom configs. Example: pci-hardening module"},
		{ControlID: "2.2.3", Title: "Primary Functions Separated", Status: "satisfied", Evidence: "Environment separation via environment_overrides; role-based module assignment"},
		{ControlID: "2.2.4", Title: "Unnecessary Services Disabled", Status: "satisfied", Evidence: "Service executor ensures unused services are stopped and disabled. Example: pci-hardening module"},
		{ControlID: "2.2.5", Title: "Security Parameters Configured", Status: "satisfied", Evidence: "File executor enforces sysctl, SSH, PAM, and audit configs. Example: pci-hardening module"},
		{ControlID: "2.2.7", Title: "Non-Console Admin Access Encrypted", Status: "satisfied", Evidence: "SSH enforced with key-only auth; mTLS on all management traffic; no plaintext admin access"},

		// Req 3 — Protect Stored Account Data
		{ControlID: "3.5.1", Title: "Stored Data Encrypted", Status: "satisfied", Evidence: "AES-256-GCM for secrets; LUKS/dm-crypt recommended for database encryption at rest"},
		{ControlID: "3.5.1.1", Title: "Cryptographic Key Management", Status: "satisfied", Evidence: "Auto-generated master key, Argon2id derivation, secret rotation via watcher"},

		// Req 5 — Protect from Malicious Software
		{ControlID: "5.2.1", Title: "Anti-Malware Deployed", Status: "satisfied", Evidence: "Package/service executors enforce AV deployment (ClamAV, etc.) across fleet"},
		{ControlID: "5.2.2", Title: "Anti-Malware Performs Scans", Status: "satisfied", Evidence: "Service executor ensures AV is running; security_scan trait collector verifies scan results"},
		{ControlID: "5.3.1", Title: "Anti-Malware Mechanisms Active", Status: "satisfied", Evidence: "Drift detection alerts if AV service stops; automatic reconvergence restarts it"},

		// Req 6 — Develop Secure Systems
		{ControlID: "6.3.1", Title: "Security Vulnerabilities Identified", Status: "satisfied", Evidence: "CVE scanning via security_scan trait collector (Trivy, debsecan); NVD links in dashboard"},
		{ControlID: "6.3.3", Title: "Security Patches Installed Promptly", Status: "satisfied", Evidence: "Package executor enforces latest versions; push system enables fleet-wide patching"},
		{ControlID: "6.5.1", Title: "Change Management for Production", Status: "satisfied", Evidence: "Stage → publish workflow, modlint validation, change approval, blast radius limits"},

		// Req 7 — Restrict Access
		{ControlID: "7.2.1", Title: "Access Control System Defined", Status: "satisfied", Evidence: "RBAC with 13 fine-grained permissions; RequirePermission() middleware on all endpoints"},
		{ControlID: "7.2.2", Title: "Access Based on Job Function", Status: "satisfied", Evidence: "Role-based defaults (admin/viewer); custom permission sets per user for least privilege"},
		{ControlID: "7.2.5", Title: "Access Privileges Assigned", Status: "satisfied", Evidence: "Permissions stored per user in DB; admin approval required for user creation"},

		// Req 8 — Identify Users
		{ControlID: "8.2.1", Title: "Unique User IDs Assigned", Status: "satisfied", Evidence: "Per-user accounts with unique UUIDs; shared accounts prohibited by design"},
		{ControlID: "8.2.2", Title: "Group/Shared IDs Not Used", Status: "satisfied", Evidence: "Each user has individual credentials; localhost trust is audited separately"},
		{ControlID: "8.3.1", Title: "Strong Authentication for All Access", Status: "satisfied", Evidence: "Basic (bcrypt), OIDC, or isowebauth required; TOTP MFA available"},
		{ControlID: "8.3.6", Title: "Password Complexity Enforced", Status: "satisfied", Evidence: "12-char minimum, uppercase + lowercase + digit + special character required"},
		{ControlID: "8.3.9", Title: "MFA for Admin Access", Status: "satisfied", Evidence: "TOTP two-factor authentication available for all accounts"},
		{ControlID: "8.6.1", Title: "System/Application Accounts Managed", Status: "satisfied", Evidence: "API tokens use hbt_ prefix, bcrypt-hashed at rest, audited creation/revocation"},

		// Req 10 — Log and Monitor
		{ControlID: "10.2.1", Title: "Audit Logs Enabled", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "10.2.1.1", Title: "User Access Logged", Status: "satisfied", Evidence: "auth.login, auth.login_failed, auth.logout events with IP address"},
		{ControlID: "10.2.1.2", Title: "Admin Actions Logged", Status: "satisfied", Evidence: "user.create/delete, token.create/revoke, config.publish, push events in audit chain"},
		{ControlID: "10.2.1.3", Title: "Access to Audit Trails Logged", Status: "satisfied", Evidence: "Audit trail access via authenticated API; tamper-evident hash chain prevents modification"},
		{ControlID: "10.2.2", Title: "Audit Log Fields Captured", Status: "satisfied", Evidence: "Each entry: timestamp, event type, actor, summary, payload (IP), prev_hash, hash"},
		{ControlID: "10.3.1", Title: "Audit Logs Protected from Modification", Status: "satisfied", Evidence: "SHA-256 hash chain — any modification/deletion breaks the chain and is detectable"},
		{ControlID: "10.3.2", Title: "Audit Logs Promptly Backed Up", Status: "satisfied", Evidence: "Litestream continuous WAL replication; on-demand encrypted snapshots"},
		{ControlID: "10.4.1", Title: "Audit Logs Reviewed Daily", Status: "partial", Evidence: "Dashboard and CLI provide review tools; daily review is an operational process"},
		{ControlID: "10.6.1", Title: "Time Synchronization", Status: "satisfied", Evidence: "Package/service executors enforce NTP/chrony; audit timestamps are UTC RFC3339"},

		// Req 11 — Test Security
		{ControlID: "11.3.1", Title: "Vulnerability Scans Performed", Status: "satisfied", Evidence: "security_scan trait collector runs Trivy, Lynis, debsecan; results in dashboard"},
		{ControlID: "11.5.1", Title: "File Integrity Monitoring", Status: "satisfied", Evidence: "Drift detection on every check-in; file snapshots capture previous content; compliance alerts on change"},
		{ControlID: "11.5.1.1", Title: "FIM Alerts on Unauthorized Changes", Status: "satisfied", Evidence: "Webhook + SMTP notifications on compliance drift; recurring drift detection (3-run threshold)"},

		// Req 12 — Security Policies
		{ControlID: "12.1.1", Title: "Security Policy Established", Status: "satisfied", Evidence: "Config-as-code modules are the executable security policy; version-controlled in git"},
		{ControlID: "12.3.1", Title: "Risk Assessment Performed", Status: "partial", Evidence: "Compliance reports provide risk evidence; formal risk assessment is organizational"},
		{ControlID: "12.10.1", Title: "Incident Response Plan", Status: "satisfied", Evidence: "Documented incident response plan with 5 phases; emergency access procedure"},
	}
}

func (h *ReportHandler) handlePCIReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob PCI DSS v4.0 Compliance Report", "hobnob-pci",
		h.buildPCIDSSFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handlePCIReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob PCI DSS v4.0 Compliance Report", "hobnob-pci",
		h.buildPCIDSSFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- CIS Benchmarks ----------

// buildCISFindings returns CIS Benchmark control findings.
// These map to the enforcement modules in examples/cis-benchmarks/.
func (h *ReportHandler) buildCISFindings(auditStatus AuditChainStatus) []ControlFinding {
	return []ControlFinding{
		// Section 1 — Filesystem
		{ControlID: "1.1.1", Title: "Disable Unused Filesystems", Status: "satisfied", Evidence: "cis-filesystem module: blacklists cramfs, freevxfs, jffs2, hfs, hfsplus, squashfs, udf, usb-storage"},
		{ControlID: "1.4.1", Title: "Bootloader Permissions", Status: "satisfied", Evidence: "cis-filesystem module: chmod 600 /boot/grub/grub.cfg"},
		{ControlID: "1.5.1", Title: "ASLR Enabled", Status: "satisfied", Evidence: "cis-filesystem module: kernel.randomize_va_space = 2"},
		{ControlID: "1.5.3", Title: "Core Dumps Restricted", Status: "satisfied", Evidence: "cis-filesystem module: hard core 0 + fs.suid_dumpable = 0"},

		// Section 2 — Services
		{ControlID: "2.1.1", Title: "Time Synchronization", Status: "satisfied", Evidence: "cis-services module: chrony installed and running"},
		{ControlID: "2.2.x", Title: "Unnecessary Services Disabled", Status: "satisfied", Evidence: "cis-services module: removes/disables avahi, cups, dhcp, ldap, nfs, bind, ftp, http, dovecot, smb, squid, snmp, rpcbind, rsync"},
		{ControlID: "2.3.x", Title: "Unnecessary Clients Removed", Status: "satisfied", Evidence: "cis-services module: removes nis, rsh, talk, telnet, ldap clients"},

		// Section 3 — Network
		{ControlID: "3.2.x", Title: "Network Host Parameters", Status: "satisfied", Evidence: "cis-network module: disable IP forwarding, disable redirects"},
		{ControlID: "3.3.x", Title: "Network Security Parameters", Status: "satisfied", Evidence: "cis-network module: disable source routing, log martians, SYN cookies, reverse path filtering, ignore ICMP broadcasts"},
		{ControlID: "3.4.x", Title: "Uncommon Protocols Disabled", Status: "satisfied", Evidence: "cis-network module: blacklists DCCP, SCTP, RDS, TIPC"},

		// Section 4 — Logging
		{ControlID: "4.1.x", Title: "Audit Logging Configured", Status: "satisfied", Evidence: "cis-logging module: auditd installed, 15+ audit rules (time, identity, network, MAC, login, permissions, access, privileged, delete, modules)"},
		{ControlID: "4.1.immutable", Title: "Audit Rules Immutable", Status: "satisfied", Evidence: "cis-logging module: -e 2 flag requires reboot to change audit rules"},
		{ControlID: "4.2.x", Title: "Syslog Configured", Status: "satisfied", Evidence: "cis-logging module: rsyslog installed, running, file permissions 0640"},

		// Section 5 — Access/Auth
		{ControlID: "5.2.x", Title: "SSH Hardened", Status: "satisfied", Evidence: "cis-auth module: Protocol 2, no root login, no empty passwords, strong ciphers/MACs/KEX, MaxAuthTries 4, AllowGroups, session timeout, banner"},
		{ControlID: "5.4.x", Title: "Password Policy", Status: "satisfied", Evidence: "cis-auth module: PASS_MAX_DAYS 365, PASS_MIN_DAYS 1, PASS_WARN_AGE 7, 14-char minimum (RHEL: pwquality minclass=4)"},
		{ControlID: "5.4.4", Title: "Default umask 027", Status: "satisfied", Evidence: "cis-auth module: /etc/profile.d/cis-umask.sh"},
		{ControlID: "5.4.5", Title: "Shell Timeout", Status: "satisfied", Evidence: "cis-auth module: TMOUT=900 readonly"},
		{ControlID: "5.5.x", Title: "System Accounts Locked", Status: "satisfied", Evidence: "cis-auth module: locks all UID < 1000 accounts (except root) with nologin shell"},

		// Section 6 — Maintenance
		{ControlID: "6.1.x", Title: "System File Permissions", Status: "satisfied", Evidence: "cis-permissions module: passwd 644, shadow 640, group 644, gshadow 640, correct ownership"},
		{ControlID: "6.2.1", Title: "No Empty Passwords", Status: "satisfied", Evidence: "cis-permissions module: locks accounts with empty password fields"},
		{ControlID: "6.2.7", Title: "Home Directory Permissions", Status: "satisfied", Evidence: "cis-permissions module: removes group-write and other-access from home directories"},

		// Windows equivalents
		{ControlID: "W-1.1.x", Title: "Windows Password Policy", Status: "satisfied", Evidence: "cis-account-policies module: 24 history, 365 max age, 14 min length, complexity enabled"},
		{ControlID: "W-1.2.x", Title: "Windows Account Lockout", Status: "satisfied", Evidence: "cis-account-policies module: 5 threshold, 15 min duration, 15 min reset"},
		{ControlID: "W-17.x", Title: "Windows Audit Policy", Status: "satisfied", Evidence: "cis-audit-policy module: credential validation, user mgmt, logon/logoff, process creation, policy change, system integrity"},
		{ControlID: "W-2.3.x", Title: "Windows Security Options", Status: "satisfied", Evidence: "cis-security-options module: disable guest, SMB signing, WDigest disabled, inactivity timeout, firewall enabled"},
	}
}

func (h *ReportHandler) handleCISReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob CIS Benchmark Compliance Report", "hobnob-cis",
		h.buildCISFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleCISReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Hobnob CIS Benchmark Compliance Report", "hobnob-cis",
		h.buildCISFindings(h.auditStatusFromRequest(r)), true)
}
