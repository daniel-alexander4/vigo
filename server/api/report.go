// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/audit"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
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

// connwaerControlStatus checks if Connwaer verification data is available for a
// capability across the fleet. Returns (status, evidence) suitable for a ControlFinding.
// When Connwaer is deployed and passing, upgrades the control to "satisfied".
// When partially deployed, returns "partial". When absent, returns upsell text.
func (h *ReportHandler) connwaerControlStatus(capability, controlTitle string) (string, string) {
	traitKey := "connwaer." + capability + ".status"
	passing := h.fleetIndex.ListByTrait(traitKey, "pass")
	allEnvoys := h.fleetIndex.AllEnvoys()
	total := 0
	for _, e := range allEnvoys {
		if !e.Revoked {
			total++
		}
	}

	if total == 0 {
		return "not_satisfied", fmt.Sprintf(
			"REQUIRES ACTIVE VERIFICATION: %s needs automated testing beyond configuration management. "+
				"Vigo Connwaer performs this verification. Learn more at https://vigo.dev/connwaer", controlTitle)
	}

	coverage := float64(len(passing)) / float64(total) * 100

	if coverage >= 100 {
		return "satisfied", fmt.Sprintf(
			"VERIFIED by Connwaer: %d/%d nodes pass %s verification",
			len(passing), total, capability)
	}
	if coverage > 0 {
		return "partial", fmt.Sprintf(
			"VERIFIED by Connwaer on %.0f%% of fleet (%d/%d nodes). "+
				"Remaining nodes need Connwaer deployment.",
			coverage, len(passing), total)
	}

	return "not_satisfied", fmt.Sprintf(
		"REQUIRES ACTIVE VERIFICATION: %s needs automated testing beyond configuration management. "+
			"Vigo Connwaer performs this verification. Learn more at https://vigo.dev/connwaer", controlTitle)
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
	mux.HandleFunc("GET /api/v1/report/nist", h.handleNISTReport)
	mux.HandleFunc("GET /api/v1/report/nist.html", h.handleNISTReportHTML)
	mux.HandleFunc("GET /api/v1/report/iso27001", h.handleISO27001Report)
	mux.HandleFunc("GET /api/v1/report/iso27001.html", h.handleISO27001ReportHTML)
	mux.HandleFunc("GET /api/v1/report/gdpr", h.handleGDPRReport)
	mux.HandleFunc("GET /api/v1/report/gdpr.html", h.handleGDPRReportHTML)
	mux.HandleFunc("GET /api/v1/report/nerccip", h.handleNERCCIPReport)
	mux.HandleFunc("GET /api/v1/report/nerccip.html", h.handleNERCCIPReportHTML)
	mux.HandleFunc("GET /api/v1/report/cyberessentials", h.handleCyberEssentialsReport)
	mux.HandleFunc("GET /api/v1/report/cyberessentials.html", h.handleCyberEssentialsReportHTML)
	mux.HandleFunc("GET /api/v1/report/iec62443", h.handleIEC62443Report)
	mux.HandleFunc("GET /api/v1/report/iec62443.html", h.handleIEC62443ReportHTML)
	mux.HandleFunc("GET /api/v1/report/executive", h.handleExecutiveReport)
	mux.HandleFunc("GET /api/v1/report/executive.html", h.handleExecutiveReportHTML)
	mux.HandleFunc("GET /api/v1/report/cisubuntu", h.handleCISUbuntuReport)
	mux.HandleFunc("GET /api/v1/report/cisubuntu.html", h.handleCISUbuntuReportHTML)
	mux.HandleFunc("GET /api/v1/report/cisrhel", h.handleCISRHELReport)
	mux.HandleFunc("GET /api/v1/report/cisrhel.html", h.handleCISRHELReportHTML)
	mux.HandleFunc("GET /api/v1/report/abacyber", h.handleABACyberReport)
	mux.HandleFunc("GET /api/v1/report/abacyber.html", h.handleABACyberReportHTML)
	mux.HandleFunc("GET /api/v1/report/nydfscyber", h.handleNYDFSCyberReport)
	mux.HandleFunc("GET /api/v1/report/nydfscyber.html", h.handleNYDFSCyberReportHTML)
}

// BuildFrameworkReport builds a compliance report for the given framework slug.
// Returns nil if the slug is unknown. Exported for use by the UI handler.
func (h *ReportHandler) BuildFrameworkReport(r *http.Request, slug string) (*ComplianceReport, error) {
	type fwDef struct {
		title    string
		name     string // short framework name (e.g., "HIPAA")
		controls func() []ControlFinding
	}
	as := h.auditStatusFromRequest(r)
	fws := map[string]fwDef{
		"compliance":      {"HIPAA Compliance Report", "HIPAA", func() []ControlFinding { return h.hipaaControls(r) }},
		"hitrust":         {"HITRUST CSF Compliance Report", "HITRUST CSF", func() []ControlFinding { return h.buildHITRUSTFindings(as) }},
		"soc2":            {"SOC 2 Trust Services Criteria Report", "SOC 2", func() []ControlFinding { return h.buildSOC2Findings(as) }},
		"pci":             {"PCI DSS v4.0 Compliance Report", "PCI DSS v4.0", func() []ControlFinding { return h.buildPCIDSSFindings(as) }},
		"nist":            {"NIST 800-53 Compliance Report", "NIST 800-53", func() []ControlFinding { return h.buildNIST80053Findings(as) }},
		"iso27001":        {"ISO 27001 Compliance Report", "ISO 27001", func() []ControlFinding { return h.buildISO27001Findings(as) }},
		"gdpr":            {"GDPR Compliance Report", "GDPR", func() []ControlFinding { return h.buildGDPRFindings(as) }},
		"nerccip":         {"NERC CIP Compliance Report", "NERC CIP", func() []ControlFinding { return h.buildNERCCIPFindings(as) }},
		"cyberessentials": {"Cyber Essentials+ Compliance Report", "Cyber Essentials+", func() []ControlFinding { return h.buildCyberEssentialsPlusFindings(as) }},
		"iec62443":        {"IEC 62443 Industrial Cybersecurity Report", "IEC 62443", func() []ControlFinding { return h.buildIEC62443Findings(as) }},
		"sox":             {"SOX IT General Controls Report", "SOX", func() []ControlFinding { return h.buildSOXFindings(as) }},
		"finra":           {"FINRA Compliance Report", "FINRA", func() []ControlFinding { return h.buildFINRAFindings(as) }},
		"mifid2":          {"MiFID II RTS 7 Compliance Report", "MiFID II", func() []ControlFinding { return h.buildMiFID2Findings(as) }},
		"ccss":            {"CCSS Cryptocurrency Security Report", "CCSS", func() []ControlFinding { return h.buildCCSSFindings(as) }},
		"fda21cfr11":      {"FDA 21 CFR Part 11 Compliance Report", "FDA 21 CFR Part 11", func() []ControlFinding { return h.buildFDA21CFR11Findings(as) }},
		"gaming":          {"Gaming Commission Compliance Report", "Gaming Commission", func() []ControlFinding { return h.buildGamingFindings(as) }},
		"ciswindows":      {"CIS Windows Server Benchmark Report", "CIS Windows", func() []ControlFinding { return h.buildCISWindowsFindings(as) }},
		"cisubuntu":       {"CIS Ubuntu Linux Benchmark Report", "CIS Ubuntu", func() []ControlFinding { return h.buildCISUbuntuFindings(as) }},
		"cisrhel":         {"CIS RHEL Benchmark Report", "CIS RHEL", func() []ControlFinding { return h.buildCISRHELFindings(as) }},
		"abacyber":        {"ABA Cybersecurity Compliance Report", "ABA Cybersecurity", func() []ControlFinding { return h.buildABACyberFindings(as) }},
		"nydfscyber":      {"NY DFS Cybersecurity (23 NYCRR 500) Report", "NY DFS Cyber", func() []ControlFinding { return h.buildNYDFSCyberFindings(as) }},
	}
	fw, ok := fws[slug]
	if !ok {
		return nil, fmt.Errorf("unknown framework: %s", slug)
	}
	findings := fw.controls()

	// Apply waivers: mark waived controls regardless of current status.
	// A control can be both satisfied (by enforcement) and waived (by policy).
	// When waived AND not satisfied, the waiver promotes it for scoring.
	// When waived AND already satisfied, the waiver badge still shows.
	// Read from config-file waivers (waivers.vgo) via ConfigStore.
	if standardKey, ok := config.StandardKeyForSlug[slug]; ok {
		if fileWaivers := h.configStore.Waivers(standardKey); len(fileWaivers) > 0 {
			for i := range findings {
				if w, waived := fileWaivers[findings[i].ControlID]; waived {
					if findings[i].Status != "satisfied" {
						findings[i].Status = "waived"
					}
					findings[i].Evidence += " [WAIVED: " + w.Reason + "]"
				}
			}
		}
	}
	// Also check DB-managed waivers (API-created).
	if waivers, err := h.queries.ListComplianceWaiversByFramework(r.Context(), slug); err == nil {
		for i := range findings {
			for _, w := range waivers {
				if w.ControlID == findings[i].ControlID {
					if findings[i].Status != "satisfied" && findings[i].Status != "waived" {
						findings[i].Status = "waived"
					}
					if !strings.Contains(findings[i].Evidence, "[WAIVED:") {
						findings[i].Evidence += " [WAIVED: " + w.Reason + "]"
					}
					break
				}
			}
		}
	}

	report, err := h.BuildReport(r, fw.title, findings)
	if err != nil {
		return nil, err
	}
	report.Slug = slug
	report.FrameworkName = fw.name

	// Enrich fleet nodes with per-standard coverage.
	// Use the findings function's control count as the denominator (not the
	// compliance index count, which only includes controls that have module tags).
	frameworkTotalControls := len(findings)
	// Build a set of valid control IDs from the findings — only controls in
	// this set count toward coverage (module tags for unknown IDs are ignored).
	findingControls := make(map[string]bool, frameworkTotalControls)
	for _, f := range findings {
		findingControls[f.ControlID] = true
	}
	if standardKey, ok := config.StandardKeyForSlug[slug]; ok {
		// Collect per-envoy module sets for per-control coverage calculation.
		type envoyModules struct {
			hostname string
			modules  map[string]bool
		}
		var activeEnvoys []envoyModules

		for i := range report.Fleet {
			node := &report.Fleet[i]
			// Always reset to framework-correct values (overrides trace-based data from BuildReport).
			node.TotalControls = frameworkTotalControls
			node.CoveredControls = 0
			node.MissingCount = frameworkTotalControls
			node.CoveragePercent = 0

			coverage := h.configStore.EnvoyStandardCoverage(node.Hostname, standardKey)
			if coverage != nil {
				// Only count controls that exist in the findings list.
				covered := 0
				for _, cc := range coverage.CoveredControls {
					if findingControls[cc.ControlID] {
						covered++
					}
				}
				node.CoveredControls = covered
				node.MissingCount = frameworkTotalControls - covered
				if frameworkTotalControls > 0 {
					node.CoveragePercent = float64(covered) / float64(frameworkTotalControls) * 100
				}

				mods := make(map[string]bool)
				for _, cc := range coverage.CoveredControls {
					for _, m := range cc.Modules {
						mods[m] = true
					}
				}
				activeEnvoys = append(activeEnvoys, envoyModules{hostname: node.Hostname, modules: mods})
			} else {
				// No modules tagged for this standard — envoy still counts for fleet average at 0%.
				activeEnvoys = append(activeEnvoys, envoyModules{hostname: node.Hostname, modules: nil})
			}
		}

		// Compute per-control fleet coverage: what % of active envoys have
		// modules covering each control?
		controlIndex := h.configStore.ComplianceControlIndex(standardKey)
		fleetTotal := len(activeEnvoys)
		if fleetTotal > 0 {
			for i := range report.Controls {
				cid := report.Controls[i].ControlID
				modulesForControl := controlIndex[cid]
				if len(modulesForControl) == 0 {
					report.Controls[i].FleetTotal = fleetTotal
					continue
				}
				covered := 0
				for _, em := range activeEnvoys {
					for _, mod := range modulesForControl {
						if em.modules[mod] {
							covered++
							break
						}
					}
				}
				report.Controls[i].FleetCovered = covered
				report.Controls[i].FleetTotal = fleetTotal
				report.Controls[i].FleetPercent = math.Round(float64(covered)/float64(fleetTotal)*1000) / 10
			}
		}
	}

	// Recompute fleet-average summary from the corrected per-node data.
	var fleetCoverageSum float64
	fleetCoverageCount := 0
	for _, n := range report.Fleet {
		if n.TotalControls > 0 {
			fleetCoverageSum += n.CoveragePercent
			fleetCoverageCount++
		}
	}
	if fleetCoverageCount > 0 && frameworkTotalControls > 0 {
		fleetPct := math.Round(fleetCoverageSum/float64(fleetCoverageCount)*10) / 10
		report.Summary.CoveragePercent = fleetPct
		avgCovered := fleetPct / 100 * float64(frameworkTotalControls)
		report.Summary.Satisfied = int(math.Round(avgCovered))
		report.Summary.NotSatisfied = frameworkTotalControls - report.Summary.Satisfied
		report.Summary.Partial = 0
		report.Summary.TotalControls = frameworkTotalControls
	}

	return report, nil
}

// ComplianceReport is the JSON format for a compliance report.
type ComplianceReport struct {
	Title         string                  `json:"title"`
	FrameworkName string                  `json:"framework_name,omitempty"`
	Slug          string                  `json:"slug,omitempty"`
	GeneratedAt string                  `json:"generated_at"`
	GeneratedBy string                  `json:"generated_by"`
	Version     string                  `json:"version"`
	Summary     ComplianceReportSummary `json:"summary"`
	Controls    []ControlFinding        `json:"controls"`
	Fleet       []FleetNodeStatus       `json:"fleet"`
	AuditChain  AuditChainStatus        `json:"audit_chain"`
}

// ComplianceReportSummary is the top-level compliance summary.
type ComplianceReportSummary struct {
	TotalNodes         int     `json:"total_nodes"`
	Satisfied          int     `json:"satisfied"`
	Partial            int     `json:"partial"`
	NotSatisfied       int     `json:"not_satisfied"`
	TotalControls      int     `json:"total_controls"`
	CoveragePercent    float64 `json:"coverage_percent"`    // fleet-average module coverage
	ConvergencePercent float64 `json:"convergence_percent"`
	// Convergence breakdown (kept for JSON API consumers).
	Converged   int `json:"converged"`
	Relapsed    int `json:"relapsed"`
	Diverged    int `json:"diverged"`
	Errors      int `json:"errors"`
	LostContact int `json:"lost_contact"`
	NoData      int `json:"no_data"`
}

// ControlFinding maps a compliance control to its implementation status.
type ControlFinding struct {
	ControlID      string  `json:"control_id"`
	Title          string  `json:"title"`
	Status         string  `json:"status"` // "satisfied", "partial", "not_satisfied"
	Evidence       string  `json:"evidence"`
	FleetPercent   float64 `json:"fleet_percent"`    // percentage of fleet with modules covering this control
	FleetCovered   int     `json:"fleet_covered"`    // envoys with coverage
	FleetTotal     int     `json:"fleet_total"`      // total active envoys
}

// FleetNodeStatus is a per-node compliance entry.
type FleetNodeStatus struct {
	Hostname        string  `json:"hostname"`
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	LastSeen        string  `json:"last_seen"`
	OS              string  `json:"os,omitempty"`
	IP              string  `json:"ip,omitempty"`
	CoveragePercent float64 `json:"coverage_percent,omitempty"`
	CoveredControls int     `json:"covered_controls,omitempty"`
	TotalControls   int     `json:"total_controls,omitempty"`
	MissingCount    int     `json:"missing_count,omitempty"`
}

// AuditChainStatus is the integrity status of the audit chain.
type AuditChainStatus struct {
	Valid      bool  `json:"valid"`
	EntryCount int64 `json:"entry_count"`
	Error      string `json:"error,omitempty"`
}

// BuildReport constructs a compliance report with the given title and controls.
// Exported for use by the UI handler's HTMX fragment endpoints.
func (h *ReportHandler) BuildReport(r *http.Request, title string, controls []ControlFinding) (*ComplianceReport, error) {
	now := time.Now().UTC()

	// Fleet summary.
	summary := h.fleetIndex.ConvergenceSummary()
	total := summary.Total
	pct := float64(0)
	if total > 0 {
		pct = float64(summary.Converged) / float64(total) * 100
	}

	// Audit chain verification.
	var auditStatus AuditChainStatus
	verifyResult, err := audit.Verify(r.Context(), h.queries)
	if err == nil {
		auditStatus.Valid = verifyResult.Valid
		auditStatus.EntryCount = verifyResult.EntryCount
		auditStatus.Error = verifyResult.Error
	}

	// Build a set of control IDs for this framework to measure coverage.
	controlSet := make(map[string]bool, len(controls))
	for _, c := range controls {
		controlSet[c.ControlID] = true
	}
	totalControls := len(controlSet)

	// Per-node status with module and standard compliance.
	var nodes []FleetNodeStatus
	for _, t := range h.fleetIndex.AllEnvoys() {
		if t.Revoked {
			continue
		}
		node := FleetNodeStatus{
			Hostname: t.Hostname,
			ID:       t.ID,
			Status:   t.ConvergenceStatus,
			LastSeen: t.LastSeen.Format(time.RFC3339),
			IP:       t.IP,
		}
		if family, ok := t.Traits["os.family"]; ok {
			node.OS = family
		}

		// Standard compliance: which framework controls does this envoy
		// cover via its assigned modules' compliance: tags?
		if totalControls > 0 && h.configStore != nil {
			trace := h.configStore.Trace(t.Hostname)
			if trace.Matched {
				covered := make(map[string]bool)
				for _, ct := range trace.ComplianceTags {
					for _, ids := range ct.Frameworks {
						for _, id := range ids {
							if controlSet[id] {
								covered[id] = true
							}
						}
					}
				}
				node.CoveredControls = len(covered)
				node.TotalControls = totalControls
				node.MissingCount = totalControls - len(covered)
				if totalControls > 0 {
					node.CoveragePercent = float64(len(covered)) / float64(totalControls) * 100
				}
			}
		}

		nodes = append(nodes, node)
	}

	// Count control statuses from findings.
	satisfied, partial, notSatisfied := 0, 0, 0
	for _, c := range controls {
		switch c.Status {
		case "satisfied":
			satisfied++
		case "partial":
			partial++
		default:
			notSatisfied++
		}
	}

	// Fleet-average coverage is computed in BuildFrameworkReport after
	// per-node data is overwritten with standard-specific values.
	// Defaults here use the findings-based counts as a fallback.
	fleetCoveragePct := float64(0)

	return &ComplianceReport{
		Title:       title,
		GeneratedAt: now.Format(time.RFC3339),
		GeneratedBy: "Vigo " + h.version,
		Version:     h.version,
		Summary: ComplianceReportSummary{
			TotalNodes:         int(total),
			Satisfied:          satisfied,
			Partial:            partial,
			NotSatisfied:       notSatisfied,
			TotalControls:      totalControls,
			CoveragePercent:    fleetCoveragePct,
			Converged:          int(summary.Converged),
			Relapsed:           int(summary.Relapsed),
			Diverged:           int(summary.Diverged),
			Errors:             int(summary.Errors),
			LostContact:        int(summary.LostContact),
			NoData:             int(summary.NoData),
			ConvergencePercent: pct,
		},
		Controls:   controls,
		Fleet:      nodes,
		AuditChain: auditStatus,
	}, nil
}

func (h *ReportHandler) buildControlFindings(summary fleet.ConvergenceCounters, auditStatus AuditChainStatus) []ControlFinding {
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
		{ControlID: "164.308(a)(6)", Title: "Security Incident Procedures", Status: "partial", Evidence: "PARTIAL: Vigo provides an incident response template with 5 phases. Organization must adopt and customize its own IR plan."},
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
	return h.buildControlFindings(h.fleetIndex.ConvergenceSummary(), auditStatus)
}

func (h *ReportHandler) handleComplianceReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HIPAA Compliance Report", "vigo-compliance",
		h.hipaaControls(r), false)
}

func (h *ReportHandler) handleComplianceReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HIPAA Compliance Report", "vigo-compliance",
		h.hipaaControls(r), true)
}

func renderComplianceHTML(w http.ResponseWriter, r *ComplianceReport) {
	// Gauge color.
	gaugeColor := "#27ae60" // green
	if r.Summary.ConvergencePercent < 80 {
		gaugeColor = "#f39c12" // yellow
	}
	if r.Summary.ConvergencePercent < 50 {
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
  .badge-errors, .badge-relapsed, .badge-diverged, .badge-lost-contact, .badge-no-data { background: #f8d7da; color: #721c24; }
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
  <div class="summary-card"><div class="value" style="color:#27ae60">%d</div><div class="label">Converged</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Errors</div></div>
  <div class="summary-card"><div class="value" style="color:#f39c12">%d</div><div class="label">Relapsed</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Diverged</div></div>
  <div class="summary-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Lost Contact</div></div>
</div>
`, gaugeColor, r.Summary.ConvergencePercent, r.Summary.TotalNodes, r.Summary.Converged, r.Summary.Errors, r.Summary.Relapsed, r.Summary.Diverged, r.Summary.LostContact)

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
			if n.Status != "converged" {
				badgeClass = "badge-" + strings.ReplaceAll(n.Status, " ", "-")
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td><span class="badge %s">%s</span></td><td>%s</td><td>%s</td><td>%s</td></tr>
`, html.EscapeString(n.Hostname), badgeClass, html.EscapeString(n.Status), html.EscapeString(n.OS), html.EscapeString(n.IP), n.LastSeen)
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	fmt.Fprintf(w, `<div class="footer">Vigo Compliance Report &mdash; Generated %s &mdash; %s</div>
</body></html>`, r.GeneratedAt, html.EscapeString(r.GeneratedBy))
}

// ---------- HITRUST CSF ----------

// buildHITRUSTFindings returns HITRUST CSF control findings mapped to Vigo capabilities.
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
		{ControlID: "01.n", Title: "Network Connection Control", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Firewall executor enforces host-based firewall rules (iptables, nftables, pf, Windows Firewall). Coverage depends on module assignment."},
		{ControlID: "01.v", Title: "Information Access Restriction", Status: "satisfied", Evidence: "File executor enforces permissions, ownership, and mode on all managed files"},

		// Domain 06 — Compliance
		{ControlID: "06.c", Title: "Protection of Organizational Records", Status: "satisfied", Evidence: "Encrypted backups (AES-256-GCM), SHA-256 integrity verification, file snapshots in DB"},
		{ControlID: "06.g", Title: "Compliance with Security Policies", Status: "satisfied", Evidence: "Continuous drift detection with 5-level compliance status, fleet-wide dashboards"},
		{ControlID: "06.h", Title: "Technical Compliance Checking", Status: "satisfied", Evidence: "Automated convergence checks on every check-in, relapsed/diverged detection (2/3-run threshold)"},
		{ControlID: "06.i", Title: "Information Systems Audit Controls", Status: statusForAudit(auditStatus), Evidence: auditEvidence},

		// Domain 07 — Asset Management
		{ControlID: "07.a", Title: "Inventory of Assets", Status: "satisfied", Evidence: "25 trait collectors auto-discover hardware, OS, network, packages; FleetIndex provides real-time inventory"},

		// Domain 09 — Communications and Operations Management
		{ControlID: "09.a", Title: "Documented Operating Procedures", Status: "satisfied", Evidence: "Config-as-code: YAML modules define desired state, version-controlled, auditable"},
		{ControlID: "09.b", Title: "Change Management", Status: "satisfied", Evidence: "Stage → publish workflow, git-tracked changes, modlint validation, tamper-evident audit trail"},
		{ControlID: "09.c", Title: "Segregation of Duties", Status: "satisfied", Evidence: "RBAC with admin/viewer roles + fine-grained permissions, change approval workflow (two-person rule)"},
		{ControlID: "09.d", Title: "Separation of Environments", Status: "satisfied", Evidence: "Environment-specific configs via environment_overrides, conditional variables per environment"},
		{ControlID: "09.i", Title: "Protection Against Malicious Code", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package executor enforces AV/EDR packages, service executor ensures they're running. Coverage depends on module assignment."},
		{ControlID: "09.k", Title: "Back-up", Status: "satisfied", Evidence: "Litestream continuous WAL replication, on-demand snapshots, AES-256-GCM encrypted backups"},
		{ControlID: "09.l", Title: "Network Security Management", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Firewall executor (iptables/nftables/pf/Windows Firewall). mTLS enforced on all management traffic. Firewall coverage depends on module assignment."},
		{ControlID: "09.q", Title: "Audit Logging", Status: statusForAudit(auditStatus), Evidence: "SHA-256 hash chain audit trail: auth, user, token, config, push, workflow, emergency, remediation events"},
		{ControlID: "09.s", Title: "Protection of Log Information", Status: "satisfied", Evidence: "Audit chain is tamper-evident (SHA-256 hash chain), immutable once written"},
		{ControlID: "09.v", Title: "Clock Synchronization", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package/service executors enforce NTP/chrony. Coverage depends on module assignment."},

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
		{ControlID: "12.c", Title: "Developing and Implementing BCPs", Status: "partial", Evidence: "PARTIAL: Config-as-code enables rapid infrastructure rebuild. Litestream provides continuous backup. Organization must develop and implement its own BCP — Vigo provides supporting infrastructure, not the plan itself."},

		// Domain 16 — Endpoint Protection
		{ControlID: "16.a", Title: "Endpoint Protection", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package/service executors enforce AV/EDR/firewall. Coverage depends on module assignment."},
	}
}

// serveFrameworkReport handles JSON and HTML report generation for any framework.
func (h *ReportHandler) serveFrameworkReport(w http.ResponseWriter, r *http.Request, title, filePrefix string, controls []ControlFinding, asHTML bool) {
	// Guard: reject disabled frameworks. Extract slug from path /api/v1/report/{slug}[.html].
	if h.configStore != nil {
		slug := r.URL.Path
		if idx := strings.LastIndexByte(slug, '/'); idx >= 0 {
			slug = slug[idx+1:]
		}
		slug = strings.TrimSuffix(slug, ".html")
		if standardKey, ok := config.StandardKeyForSlug[slug]; ok {
			if !h.configStore.IsStandardEnabled(standardKey) {
				if asHTML {
					http.Error(w, "This compliance framework is not enabled.", http.StatusNotFound)
				} else {
					writeJSONError(w, http.StatusNotFound, "framework not enabled")
				}
				return
			}
		}
	}

	report, err := h.BuildReport(r, title, controls)
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
	h.serveFrameworkReport(w, r, "Vigo HITRUST CSF Compliance Report", "vigo-hitrust",
		h.buildHITRUSTFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleHITRUSTReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HITRUST CSF Compliance Report", "vigo-hitrust",
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
		{ControlID: "CC2.1", Title: "Internal Communication of Objectives", Status: "partial", Evidence: "PARTIAL: Config-as-code documents technical desired state. Communicating security objectives to personnel is an organizational process."},
		{ControlID: "CC2.2", Title: "Internal Communication of Policies", Status: "partial", Evidence: "PARTIAL: YAML modules define executable technical policy. Communicating broader security policies to personnel is an organizational process."},

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
		{ControlID: "CC6.7", Title: "Management of Credentials", Status: "satisfied", Evidence: "12-char complexity, bcrypt hashing, AES-256-GCM encrypted storage, API tokens (vgot_ prefix)"},
		{ControlID: "CC6.8", Title: "Prevention of Unauthorized Software", Status: "satisfied", Evidence: "Package executor enforces versions; repository executor controls sources"},

		// CC7 — System Operations
		{ControlID: "CC7.1", Title: "Detection of Security Events", Status: "satisfied", Evidence: "Continuous drift detection, compliance monitoring, failed login tracking with IP"},
		{ControlID: "CC7.2", Title: "Monitoring for Anomalies", Status: "satisfied", Evidence: "Relapsed/diverged detection (2/3-run threshold), compliance threshold alerts, stale node detection"},
		{ControlID: "CC7.3", Title: "Evaluation of Security Events", Status: "satisfied", Evidence: "Tamper-evident audit trail with event classification; SIEM export for external analysis"},
		{ControlID: "CC7.4", Title: "Response to Security Events", Status: "partial", Evidence: "PARTIAL: Vigo provides incident response templates, emergency access procedure, and automated remediation workflow. Organization must adopt and customize its own IR plan."},
		{ControlID: "CC7.5", Title: "Recovery from Security Events", Status: "partial", Evidence: "PARTIAL: Encrypted backups and config-as-code enable rapid rebuild. Actual recovery capability requires DR testing — Vigo provides infrastructure, not proven recovery."},

		// CC8 — Change Management
		{ControlID: "CC8.1", Title: "Change Management Process", Status: "satisfied", Evidence: "Stage → live separation, git-tracked changes, modlint validation, change approval workflow"},

		// CC9 — Risk Mitigation
		{ControlID: "CC9.1", Title: "Risk Mitigation Activities", Status: "satisfied", Evidence: "ENFORCED (when configured): Automated remediation workflow, blast radius limits, auto-rollback on compliance drop. Effectiveness depends on module coverage."},

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
	h.serveFrameworkReport(w, r, "Vigo SOC 2 Trust Services Criteria Report", "vigo-soc2",
		h.buildSOC2Findings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleSOC2ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo SOC 2 Trust Services Criteria Report", "vigo-soc2",
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
		{ControlID: "1.2.1", Title: "Network Security Controls Configured", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Firewall executor enforces iptables/nftables/pf/Windows Firewall rules. Coverage depends on module assignment."},
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
		{ControlID: "5.2.1", Title: "Anti-Malware Deployed", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package/service executors enforce AV deployment. Coverage depends on module assignment."},
		{ControlID: "5.2.2", Title: "Anti-Malware Performs Scans", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Service executor ensures AV is running; security_scan trait collector verifies scan results. Coverage depends on module assignment."},
		{ControlID: "5.3.1", Title: "Anti-Malware Mechanisms Active", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Drift detection alerts if AV service stops; automatic reconvergence restarts it. Coverage depends on module assignment."},

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
		{ControlID: "8.6.1", Title: "System/Application Accounts Managed", Status: "satisfied", Evidence: "API tokens use vgot_ prefix, bcrypt-hashed at rest, audited creation/revocation"},

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
		{ControlID: "11.5.1.1", Title: "FIM Alerts on Unauthorized Changes", Status: "satisfied", Evidence: "Webhook + SMTP notifications on compliance drift; relapsed/diverged detection (2/3-run threshold)"},

		// Req 12 — Security Policies
		{ControlID: "12.1.1", Title: "Security Policy Established", Status: "partial", Evidence: "PARTIAL: Config-as-code defines executable technical policy. Organization must establish a broader security policy covering governance, personnel, and physical controls."},
		{ControlID: "12.3.1", Title: "Risk Assessment Performed", Status: "partial", Evidence: "Compliance reports provide risk evidence; formal risk assessment is organizational"},
		{ControlID: "12.10.1", Title: "Incident Response Plan", Status: "partial", Evidence: "PARTIAL: Vigo provides an IR template with 5 phases and emergency access procedure. Organization must adopt and customize its own IR plan."},
	}
}

func (h *ReportHandler) handlePCIReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo PCI DSS v4.0 Compliance Report", "vigo-pci",
		h.buildPCIDSSFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handlePCIReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo PCI DSS v4.0 Compliance Report", "vigo-pci",
		h.buildPCIDSSFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- CIS Benchmarks ----------

// ---------- NIST 800-53 ----------

func (h *ReportHandler) buildNIST80053Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// AC — Access Control
		{ControlID: "AC-2", Title: "Account Management", Status: "satisfied", Evidence: "Per-user accounts with UUIDs, RBAC roles, admin approval for creation, audited lifecycle"},
		{ControlID: "AC-3", Title: "Access Enforcement", Status: "satisfied", Evidence: "13 fine-grained permissions, RequirePermission() middleware on all endpoints"},
		{ControlID: "AC-6", Title: "Least Privilege", Status: "satisfied", Evidence: "Role-based defaults (admin/viewer), custom permission sets per user"},
		{ControlID: "AC-7", Title: "Unsuccessful Logon Attempts", Status: "satisfied", Evidence: "auth.login_failed audit events with IP; PAM faillock via CIS/PCI modules"},
		{ControlID: "AC-8", Title: "System Use Notification", Status: "satisfied", Evidence: "SSH banner via cis-auth module; login page branding"},
		{ControlID: "AC-11", Title: "Session Lock", Status: "satisfied", Evidence: "15-minute idle session timeout; shell TMOUT via CIS modules"},
		{ControlID: "AC-12", Title: "Session Termination", Status: "satisfied", Evidence: "24-hour absolute session lifetime; idle timeout configurable"},
		{ControlID: "AC-17", Title: "Remote Access", Status: "satisfied", Evidence: "mTLS on all remote access; SSH hardening via modules; TOTP MFA available"},

		// AU — Audit and Accountability
		{ControlID: "AU-2", Title: "Audit Events", Status: "satisfied", Evidence: "15+ event types: auth, user, token, config, push, workflow, emergency, remediation, envoy"},
		{ControlID: "AU-3", Title: "Content of Audit Records", Status: "satisfied", Evidence: "Each entry: timestamp, event type, actor, summary, payload, prev_hash, hash"},
		{ControlID: "AU-6", Title: "Audit Review, Analysis, Reporting", Status: "satisfied", Evidence: "CLI audit list/verify, compliance dashboards, SIEM export, scheduled reports"},
		{ControlID: "AU-8", Title: "Time Stamps", Status: "satisfied", Evidence: "UTC RFC3339 timestamps; NTP/chrony enforcement via modules"},
		{ControlID: "AU-9", Title: "Protection of Audit Information", Status: statusForAudit(auditStatus), Evidence: "SHA-256 hash chain — " + auditEvidence},
		{ControlID: "AU-10", Title: "Non-repudiation", Status: "satisfied", Evidence: "ED25519 signatures on agent communication; tamper-evident audit chain"},
		{ControlID: "AU-11", Title: "Audit Record Retention", Status: "satisfied", Evidence: "Litestream continuous backup; encrypted snapshots; configurable retention"},
		{ControlID: "AU-12", Title: "Audit Generation", Status: "satisfied", Evidence: "Automatic audit recording on all security-relevant events; no opt-out"},

		// CA — Assessment, Authorization, Monitoring
		{ControlID: "CA-7", Title: "Continuous Monitoring", Status: "satisfied", Evidence: "Agent check-in every 5 min (configurable); compliance monitor every 60s; drift alerts"},

		// CM — Configuration Management
		{ControlID: "CM-2", Title: "Baseline Configuration", Status: "satisfied", Evidence: "YAML modules define desired state; version-controlled in git"},
		{ControlID: "CM-3", Title: "Configuration Change Control", Status: "satisfied", Evidence: "Stage → publish workflow, modlint validation, change approval, audit trail"},
		{ControlID: "CM-5", Title: "Access Restrictions for Change", Status: "satisfied", Evidence: "config.publish permission required; change approval workflow (two-person rule)"},
		{ControlID: "CM-6", Title: "Configuration Settings", Status: "satisfied", Evidence: "73 idempotent executors enforce settings across all platforms"},
		{ControlID: "CM-7", Title: "Least Functionality", Status: "satisfied", Evidence: "CIS modules disable unnecessary services, remove unused packages"},
		{ControlID: "CM-8", Title: "System Component Inventory", Status: "satisfied", Evidence: "25 trait collectors auto-discover hardware, OS, network, packages; FleetIndex"},
		{ControlID: "CM-11", Title: "User-Installed Software", Status: "satisfied", Evidence: "Package executor enforces approved versions; repository control"},

		// CP — Contingency Planning
		{ControlID: "CP-9", Title: "System Backup", Status: "satisfied", Evidence: "Litestream continuous WAL replication; AES-256-GCM encrypted snapshots; SHA-256 integrity"},
		{ControlID: "CP-10", Title: "System Recovery and Reconstitution", Status: "satisfied", Evidence: "Config-as-code rapid rebuild; disaster recovery documented; backup restore tested"},

		// IA — Identification and Authentication
		{ControlID: "IA-2", Title: "Identification and Authentication", Status: "satisfied", Evidence: "Basic (bcrypt), OIDC, isowebauth; unique user IDs; TOTP MFA"},
		{ControlID: "IA-5", Title: "Authenticator Management", Status: "satisfied", Evidence: "12-char complexity, bcrypt hashing, AES-256-GCM storage, API tokens (vgot_ prefix)"},
		{ControlID: "IA-8", Title: "Identification and Authentication (Non-Org)", Status: "satisfied", Evidence: "Agent mTLS certificates; ED25519 signature verification on all agent traffic"},

		// IR — Incident Response
		{ControlID: "IR-4", Title: "Incident Handling", Status: "partial", Evidence: "PARTIAL: Vigo provides an IR template with 5 phases and emergency access procedure. Organization must adopt and customize its own IR plan."},
		{ControlID: "IR-5", Title: "Incident Monitoring", Status: "satisfied", Evidence: "Webhook + SMTP alerts; compliance threshold monitoring; audit trail"},
		{ControlID: "IR-6", Title: "Incident Reporting", Status: "satisfied", Evidence: "Audit events for all security incidents; SIEM export for external correlation"},

		// SC — System and Communications Protection
		{ControlID: "SC-7", Title: "Boundary Protection", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Firewall executor enforces network segmentation. mTLS enforced on all management traffic. Firewall coverage depends on module assignment."},
		{ControlID: "SC-8", Title: "Transmission Confidentiality and Integrity", Status: "satisfied", Evidence: "mTLS (TLS 1.3) on all gRPC and REST traffic; no plaintext transport"},
		{ControlID: "SC-12", Title: "Cryptographic Key Establishment", Status: "satisfied", Evidence: "Auto-generated master key; Argon2id derivation; ED25519 agent keys"},
		{ControlID: "SC-13", Title: "Cryptographic Protection", Status: "satisfied", Evidence: "AES-256-GCM, TLS 1.3, ED25519, bcrypt, SHA-256 — FIPS-grade algorithms"},
		{ControlID: "SC-28", Title: "Protection of Information at Rest", Status: "satisfied", Evidence: "AES-256-GCM for secrets and backups; LUKS recommended for database"},

		// SI — System and Information Integrity
		{ControlID: "SI-2", Title: "Flaw Remediation", Status: "satisfied", Evidence: "CVE scanning via security_scan trait; automated remediation workflow"},
		{ControlID: "SI-3", Title: "Malicious Code Protection", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): AV/EDR enforcement via package/service executors. Coverage depends on module assignment."},
		{ControlID: "SI-4", Title: "System Monitoring", Status: "satisfied", Evidence: "Continuous drift detection; compliance dashboards; Prometheus metrics"},
		{ControlID: "SI-7", Title: "Software, Firmware, and Information Integrity", Status: "satisfied", Evidence: "Drift detection = FIM; file snapshots; ED25519 bundle signatures"},
	}
}

func (h *ReportHandler) handleNISTReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NIST 800-53 Compliance Report", "vigo-nist800-53",
		h.buildNIST80053Findings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleNISTReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NIST 800-53 Compliance Report", "vigo-nist800-53",
		h.buildNIST80053Findings(h.auditStatusFromRequest(r)), true)
}

// ---------- ISO 27001 ----------

func (h *ReportHandler) buildISO27001Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// A.5 — Information Security Policies
		{ControlID: "A.5.1", Title: "Management Direction for InfoSec", Status: "partial", Evidence: "PARTIAL: Config-as-code defines executable technical policy. Management direction for information security is an organizational responsibility."},

		// A.6 — Organization of Information Security
		{ControlID: "A.6.1", Title: "Internal Organization", Status: "satisfied", Evidence: "RBAC with 13 permissions; segregation of duties; change approval workflow"},

		// A.8 — Asset Management
		{ControlID: "A.8.1", Title: "Responsibility for Assets", Status: "satisfied", Evidence: "25 trait collectors auto-discover assets; FleetIndex provides real-time inventory"},
		{ControlID: "A.8.2", Title: "Information Classification", Status: "partial", Evidence: "secret: prefix classifies sensitive values; organizational classification policy required"},

		// A.9 — Access Control
		{ControlID: "A.9.1.2", Title: "Access to Networks and Services", Status: "satisfied", Evidence: "mTLS on all management traffic; firewall executor enforces network access"},
		{ControlID: "A.9.2.1", Title: "User Registration and De-registration", Status: "satisfied", Evidence: "Per-user accounts with UUIDs; audited creation/deletion; cascade delete on decommission"},
		{ControlID: "A.9.2.3", Title: "Management of Privileged Access", Status: "satisfied", Evidence: "13 fine-grained permissions; admin/viewer roles; least privilege defaults"},
		{ControlID: "A.9.3.1", Title: "Use of Secret Authentication Information", Status: "satisfied", Evidence: "12-char complexity, bcrypt hashing, AES-256-GCM storage; TOTP MFA available"},
		{ControlID: "A.9.4.1", Title: "Information Access Restriction", Status: "satisfied", Evidence: "RequirePermission() middleware; file executor enforces filesystem permissions"},
		{ControlID: "A.9.4.2", Title: "Secure Log-on Procedures", Status: "satisfied", Evidence: "Failed login auditing with IP; account lockout via PAM; session timeout"},
		{ControlID: "A.9.4.3", Title: "Password Management System", Status: "satisfied", Evidence: "Bcrypt hashing, complexity enforcement, secrets provider encryption"},

		// A.10 — Cryptography
		{ControlID: "A.10.1.1", Title: "Policy on Use of Cryptographic Controls", Status: "satisfied", Evidence: "AES-256-GCM, TLS 1.3, ED25519, bcrypt, Argon2id — enforced by default"},
		{ControlID: "A.10.1.2", Title: "Key Management", Status: "satisfied", Evidence: "Auto-generated master key; secret rotation via watcher; certificate lifecycle"},

		// A.12 — Operations Security
		{ControlID: "A.12.1.2", Title: "Change Management", Status: "satisfied", Evidence: "Stage → publish, modlint validation, change approval, blast radius limits, audit trail"},
		{ControlID: "A.12.1.4", Title: "Separation of Environments", Status: "satisfied", Evidence: "environment_overrides, conditional variables per environment"},
		{ControlID: "A.12.2.1", Title: "Controls Against Malware", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package/service executors enforce AV/EDR; security scanning trait collector. Coverage depends on module assignment."},
		{ControlID: "A.12.3.1", Title: "Information Backup", Status: "satisfied", Evidence: "Litestream continuous backup; AES-256-GCM encrypted snapshots; SHA-256 integrity"},
		{ControlID: "A.12.4.1", Title: "Event Logging", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "A.12.4.3", Title: "Administrator and Operator Logs", Status: "satisfied", Evidence: "All admin actions audited: user/token CRUD, config publish, push, workflow"},
		{ControlID: "A.12.4.4", Title: "Clock Synchronisation", Status: "satisfied", Evidence: "NTP/chrony enforcement via modules; UTC RFC3339 timestamps"},
		{ControlID: "A.12.5.1", Title: "Installation of Software on Operational Systems", Status: "satisfied", Evidence: "Package executor enforces approved versions; repository control"},
		{ControlID: "A.12.6.1", Title: "Management of Technical Vulnerabilities", Status: "satisfied", Evidence: "CVE scanning via security_scan trait; automated remediation workflow"},

		// A.13 — Communications Security
		{ControlID: "A.13.1.1", Title: "Network Controls", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Firewall executor enforces network segmentation. mTLS enforced on all management traffic. Firewall coverage depends on module assignment."},
		{ControlID: "A.13.2.1", Title: "Information Transfer Policies", Status: "satisfied", Evidence: "All data in transit encrypted via mTLS (TLS 1.3); no plaintext transport"},

		// A.14 — System Acquisition, Development, Maintenance
		{ControlID: "A.14.1.2", Title: "Securing Application Services", Status: "satisfied", Evidence: "mTLS on all services; secret: prefix for sensitive config values"},
		{ControlID: "A.14.2.2", Title: "System Change Control Procedures", Status: "satisfied", Evidence: "Stage/live separation, git history, modlint, change approval, auto-rollback"},

		// A.16 — Information Security Incident Management
		{ControlID: "A.16.1.2", Title: "Reporting Information Security Events", Status: "satisfied", Evidence: "Webhook + SMTP alerts; compliance threshold monitoring; emergency access procedure"},
		{ControlID: "A.16.1.5", Title: "Response to Information Security Incidents", Status: "satisfied", Evidence: "Documented 5-phase incident response plan; remediation workflow"},
		{ControlID: "A.16.1.7", Title: "Collection of Evidence", Status: "satisfied", Evidence: "Run history, file snapshots, compliance history, tamper-evident audit chain"},

		// A.17 — Business Continuity
		{ControlID: "A.17.1.1", Title: "Planning Information Security Continuity", Status: "partial", Evidence: "PARTIAL: Config-as-code enables rapid infrastructure rebuild. Organization must plan its own information security continuity — Vigo provides supporting infrastructure, not the plan itself."},

		// A.18 — Compliance
		{ControlID: "A.18.1.3", Title: "Protection of Records", Status: "satisfied", Evidence: "Encrypted backups; tamper-evident audit chain; file snapshots"},
		{ControlID: "A.18.2.2", Title: "Compliance with Security Policies", Status: "satisfied", Evidence: "Continuous drift detection; 5-level compliance status; fleet dashboards"},
		{ControlID: "A.18.2.3", Title: "Technical Compliance Review", Status: "satisfied", Evidence: "Automated convergence checks on every agent check-in; CIS benchmark modules"},
	}
}

func (h *ReportHandler) handleISO27001Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ISO 27001 Compliance Report", "vigo-iso27001",
		h.buildISO27001Findings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleISO27001ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ISO 27001 Compliance Report", "vigo-iso27001",
		h.buildISO27001Findings(h.auditStatusFromRequest(r)), true)
}

// ---------- GDPR ----------

func (h *ReportHandler) buildGDPRFindings(_ AuditChainStatus) []ControlFinding {
	return []ControlFinding{
		// Article 5 — Principles
		{ControlID: "Art.5(1)(f)", Title: "Integrity and Confidentiality", Status: "satisfied", Evidence: "AES-256-GCM encryption, mTLS transport, ED25519 signatures, tamper-evident audit"},

		// Article 25 — Data Protection by Design
		{ControlID: "Art.25(1)", Title: "Data Protection by Design", Status: "satisfied", Evidence: "secret: prefix prevents plaintext secrets; encryption by default; least-privilege RBAC"},
		{ControlID: "Art.25(2)", Title: "Data Protection by Default", Status: "satisfied", Evidence: "Minimal data collection; traits auto-discovered, not user-provided PII"},

		// Article 30 — Records of Processing
		{ControlID: "Art.30(1)", Title: "Records of Processing Activities", Status: "satisfied", Evidence: "Tamper-evident audit trail records all data processing activities with timestamps"},

		// Article 32 — Security of Processing
		{ControlID: "Art.32(1)(a)", Title: "Encryption", Status: "satisfied", Evidence: "AES-256-GCM at rest, mTLS (TLS 1.3) in transit, Argon2id key derivation"},
		{ControlID: "Art.32(1)(b)", Title: "Confidentiality, Integrity, Availability", Status: "satisfied", Evidence: "RBAC access control, hash chain integrity, Litestream continuous backup"},
		{ControlID: "Art.32(1)(c)", Title: "Ability to Restore Availability", Status: "satisfied", Evidence: "Encrypted backups, disaster recovery procedures, config-as-code rapid rebuild"},
		{ControlID: "Art.32(1)(d)", Title: "Testing Security Measures", Status: "satisfied", Evidence: "Continuous compliance monitoring; audit chain verification; CIS benchmark modules"},
		{ControlID: "Art.32(2)", Title: "Risk Assessment for Processing", Status: "partial", Evidence: "Compliance reports provide risk evidence; formal DPIA is organizational"},

		// Article 33 — Breach Notification
		{ControlID: "Art.33(1)", Title: "Notification to Supervisory Authority", Status: "partial", Evidence: "Tamper-evident audit provides breach evidence; 72-hour notification is organizational"},
		{ControlID: "Art.33(3)", Title: "Content of Notification", Status: "satisfied", Evidence: "Audit trail captures: nature of breach, categories affected, consequences, measures taken"},

		// Article 35 — Data Protection Impact Assessment
		{ControlID: "Art.35(1)", Title: "Data Protection Impact Assessment", Status: "partial", Evidence: "Compliance reports + fleet status provide input for DPIA; formal assessment is organizational"},

		// Article 17 — Right to Erasure
		{ControlID: "Art.17(1)", Title: "Right to Erasure", Status: "satisfied", Evidence: "Cascade delete removes all envoy data; documented decommissioning procedure"},
	}
}

func (h *ReportHandler) handleGDPRReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo GDPR Compliance Report", "vigo-gdpr",
		h.buildGDPRFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleGDPRReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo GDPR Compliance Report", "vigo-gdpr",
		h.buildGDPRFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- NERC CIP ----------

func (h *ReportHandler) buildNERCCIPFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// CIP-002 — BES Cyber System Categorization
		{ControlID: "CIP-002-5.1", Title: "BES Cyber System Identification", Status: "satisfied", Evidence: "25 trait collectors auto-discover system inventory; FleetIndex asset registry"},

		// CIP-003 — Security Management Controls
		{ControlID: "CIP-003-8-R1", Title: "Cyber Security Policies", Status: "satisfied", Evidence: "Config-as-code modules define and enforce security policies; version-controlled"},
		{ControlID: "CIP-003-8-R2", Title: "Cyber Security Plans", Status: "partial", Evidence: "Technical controls automated; organizational planning is separate"},

		// CIP-004 — Personnel and Training
		{ControlID: "CIP-004-6-R4", Title: "Access Management Program", Status: "satisfied", Evidence: "RBAC with 13 permissions; per-user accounts; audited lifecycle"},
		{ControlID: "CIP-004-6-R5", Title: "Access Revocation", Status: "satisfied", Evidence: "User deletion with cascade; token revocation; envoy revocation; all audited"},

		// CIP-005 — Electronic Security Perimeters
		{ControlID: "CIP-005-7-R1", Title: "Electronic Security Perimeter", Status: "satisfied", Evidence: "Firewall executor enforces network boundaries; mTLS on management traffic"},
		{ControlID: "CIP-005-7-R2", Title: "Remote Access Management", Status: "satisfied", Evidence: "mTLS required; SSH hardening via modules; TOTP MFA; encrypted tunnels"},

		// CIP-007 — Systems Security Management
		{ControlID: "CIP-007-6-R1", Title: "Ports and Services", Status: "satisfied", Evidence: "CIS/PCI modules disable unnecessary services; firewall restricts ports"},
		{ControlID: "CIP-007-6-R2", Title: "Security Patch Management", Status: "satisfied", Evidence: "Package executor enforces versions; push system enables fleet-wide patching"},
		{ControlID: "CIP-007-6-R3", Title: "Malicious Code Prevention", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): AV/EDR enforcement via package/service executors; security scanning. Coverage depends on module assignment."},
		{ControlID: "CIP-007-6-R4", Title: "Security Event Monitoring", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "CIP-007-6-R5", Title: "System Access Control", Status: "satisfied", Evidence: "Authentication required; RBAC; password complexity; session timeout; MFA"},

		// CIP-009 — Recovery Plans
		{ControlID: "CIP-009-6-R1", Title: "Recovery Plan Specifications", Status: "satisfied", Evidence: "Disaster recovery documented; config-as-code rapid rebuild; encrypted backups"},
		{ControlID: "CIP-009-6-R2", Title: "Recovery Plan Implementation and Testing", Status: "partial", Evidence: "PARTIAL: Litestream continuous replication and remediation workflow provide recovery infrastructure. Actual recovery plan testing requires external validation through DR exercises."},

		// CIP-010 — Configuration Change Management
		{ControlID: "CIP-010-3-R1", Title: "Configuration Change Management", Status: "satisfied", Evidence: "Stage → publish workflow; modlint validation; change approval; blast radius limits"},
		{ControlID: "CIP-010-3-R2", Title: "Configuration Monitoring", Status: "satisfied", Evidence: "Continuous drift detection on every check-in; compliance alerts; FIM"},

		// CIP-011 — Information Protection
		{ControlID: "CIP-011-2-R1", Title: "Information Protection", Status: "satisfied", Evidence: "AES-256-GCM for secrets and backups; secret: prefix; no plaintext secrets"},
		{ControlID: "CIP-011-2-R2", Title: "BES Cyber Asset Reuse and Disposal", Status: "satisfied", Evidence: "Documented decommissioning with cascade delete and secure media disposal"},
	}
}

func (h *ReportHandler) handleNERCCIPReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NERC CIP Compliance Report", "vigo-nerccip",
		h.buildNERCCIPFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleNERCCIPReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NERC CIP Compliance Report", "vigo-nerccip",
		h.buildNERCCIPFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- Cyber Essentials+ ----------

func (h *ReportHandler) buildCyberEssentialsPlusFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Theme 1 — Firewalls
		{ControlID: "CE-FW-1", Title: "Boundary Firewalls", Status: "satisfied", Evidence: "Firewall executor enforces iptables/nftables rules; mTLS on all management traffic"},
		{ControlID: "CE-FW-2", Title: "Firewall Rule Review", Status: "satisfied", Evidence: "Firewall rules declared in YAML modules; continuous drift detection reverts unauthorized changes"},
		{ControlID: "CE-FW-3", Title: "Default Deny", Status: "satisfied", Evidence: "PCI/CIS hardening modules enforce default-deny policies; INPUT/FORWARD DROP by default"},
		{ControlID: "CE-FW-4", Title: "Host-Based Firewalls", Status: "satisfied", Evidence: "Per-host firewall rules via firewall executor; portable across iptables, nftables, pf, Windows Firewall"},
		{ControlID: "CE-FW-5", Title: "Administrative Interface Protection", Status: "satisfied", Evidence: "Admin UI on TLS only; mTLS for agent communication; no plaintext management interfaces"},

		// Theme 2 — Secure Configuration
		{ControlID: "CE-SC-1", Title: "Remove Unnecessary Software", Status: "satisfied", Evidence: "Package executor enforces absent state; CIS modules remove unused packages"},
		{ControlID: "CE-SC-2", Title: "Disable Unnecessary Accounts", Status: "satisfied", Evidence: "User executor manages accounts; system-accounts module disables default/guest accounts"},
		{ControlID: "CE-SC-3", Title: "Change Default Passwords", Status: "satisfied", Evidence: "Secrets provider injects unique credentials; no default passwords in config"},
		{ControlID: "CE-SC-4", Title: "Disable Autorun", Status: "satisfied", Evidence: "CIS hardening modules disable USB autorun/automount; Windows modules disable AutoPlay"},
		{ControlID: "CE-SC-5", Title: "Secure Configuration Baselines", Status: "satisfied", Evidence: "YAML modules define desired state; continuous enforcement on every check-in cycle"},
		{ControlID: "CE-SC-6", Title: "Device Lockdown", Status: "satisfied", Evidence: "Screen lock timeout via exec/registry modules; login banner enforcement"},

		// Theme 3 — Security Update Management
		{ControlID: "CE-SU-1", Title: "Software Licensing", Status: "partial", Evidence: "PARTIAL: Package executor tracks installed versions; trait collectors report software inventory. License compliance verification (ensuring valid licenses for all software) is an organizational process."},
		{ControlID: "CE-SU-2", Title: "Patch Within 14 Days", Status: "satisfied", Evidence: "Package executor enforces latest/specific versions; push system for fleet-wide patching"},
		{ControlID: "CE-SU-3", Title: "Unsupported Software Removal", Status: "satisfied", Evidence: "Package executor can enforce absent state for EOL software; inventory traits flag OS version"},
		{ControlID: "CE-SU-4", Title: "Automatic Update Configuration", Status: "satisfied", Evidence: "Unattended-upgrades/dnf-automatic modules; Windows Update executor for auto-patching"},

		// Theme 4 — User Access Control
		{ControlID: "CE-AC-1", Title: "User Account Management", Status: "satisfied", Evidence: "RBAC with admin/viewer roles + 13 fine-grained permissions; per-user accounts with unique UUIDs"},
		{ControlID: "CE-AC-2", Title: "Least Privilege", Status: "satisfied", Evidence: "Fine-grained RBAC; admin access restricted; viewer role is read-only; no shared accounts"},
		{ControlID: "CE-AC-3", Title: "Authentication Requirements", Status: "satisfied", Evidence: "Password complexity via bcrypt; session timeout; TOTP MFA support; OIDC/SSO"},
		{ControlID: "CE-AC-4", Title: "Multi-Factor Authentication", Status: "satisfied", Evidence: "TOTP MFA for web UI; OIDC SSO with MFA delegation; isowebauth SSH key signing"},
		{ControlID: "CE-AC-5", Title: "Account Lockout", Status: "satisfied", Evidence: "Brute-force protection via session management; API token rate limiting"},
		{ControlID: "CE-AC-6", Title: "Privileged Access Management", Status: "satisfied", Evidence: "Admin role gated by RBAC; peer auth for local-only CLI; all admin actions audited"},

		// Theme 5 — Malware Protection
		{ControlID: "CE-MW-1", Title: "Anti-Malware Software", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): AV/EDR enforcement via package/service executors. Coverage depends on module assignment."},
		{ControlID: "CE-MW-2", Title: "Anti-Malware Configuration", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): AV service state enforced continuously; configuration drift detected and corrected. Coverage depends on module assignment."},
		{ControlID: "CE-MW-3", Title: "Anti-Malware Updates", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Package executor ensures AV signature databases stay current; push for emergency updates. Coverage depends on module assignment."},
		{ControlID: "CE-MW-4", Title: "Application Allow-Listing", Status: "partial", Evidence: "Package executor controls installed software; full application whitelisting is OS-level (AppLocker/fapolicyd)"},
		{ControlID: "CE-MW-5", Title: "Sandboxing and Code Execution Prevention", Status: "partial", Evidence: "Kernel hardening modules (ASLR, NX, seccomp); full sandboxing depends on application-level controls"},

		// Supplementary — Monitoring and Logging (CE+ only)
		{ControlID: "CE-ML-1", Title: "Security Event Logging", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "CE-ML-2", Title: "Log Retention", Status: "satisfied", Evidence: "Audit log with tamper-evident hash chain; configurable retention; Litestream backup"},
		{ControlID: "CE-ML-3", Title: "Incident Detection", Status: "satisfied", Evidence: "Continuous compliance monitoring; drift alerts; SMTP/webhook notifications on anomalies"},
	}
}

func (h *ReportHandler) handleCyberEssentialsReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo Cyber Essentials+ Compliance Report", "vigo-cyberessentials",
		h.buildCyberEssentialsPlusFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleCyberEssentialsReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo Cyber Essentials+ Compliance Report", "vigo-cyberessentials",
		h.buildCyberEssentialsPlusFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- IEC 62443 Industrial Cybersecurity ----------

// buildIEC62443Findings returns IEC 62443 foundational requirement findings.
// Maps to security levels SL 1-4 across zones and conduits for industrial
// IT infrastructure (MES servers, historians, jump hosts).
func (h *ReportHandler) buildIEC62443Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// FR 1 — Identification and Authentication Control (IAC)
		{ControlID: "SR 1.1", Title: "Human User Identification and Authentication", Status: "satisfied", Evidence: "Authentication required on all access; RBAC with admin/viewer roles; session management with SCS"},
		{ControlID: "SR 1.2", Title: "Software Process Identification and Authentication", Status: "satisfied", Evidence: "mTLS for all gRPC agent communication; signature-verified check-ins; one-time enrollment tokens"},
		{ControlID: "SR 1.3", Title: "Account Management", Status: "satisfied", Evidence: "User store with bcrypt passwords; API tokens with expiry; OIDC/SSO integration; key-based SSH auth modules"},
		{ControlID: "SR 1.5", Title: "Authenticator Management", Status: "satisfied", Evidence: "Secrets provider with pluggable backends; bcrypt-hashed tokens at rest; password complexity enforcement via PAM modules"},
		{ControlID: "SR 1.7", Title: "Strength of Password-Based Authentication", Status: "satisfied", Evidence: "pwquality enforcement modules; configurable min length, complexity, aging via vars"},
		{ControlID: "SR 1.8", Title: "Public Key Infrastructure Certificates", Status: "satisfied", Evidence: "Built-in CA for agent enrollment; CSR signing; mTLS certificate management"},
		{ControlID: "SR 1.9", Title: "Strength of Public Key Authentication", Status: "satisfied", Evidence: "Ed25519/ECDSA key generation; SSH hardening modules enforce strong crypto algorithms"},
		{ControlID: "SR 1.10", Title: "Authenticator Feedback", Status: "satisfied", Evidence: "Account lockout via faillock modules; brute-force protection; session timeout enforcement"},

		// FR 2 — Use Control (UC)
		{ControlID: "SR 2.1", Title: "Authorization Enforcement", Status: "satisfied", Evidence: "RBAC with admin/viewer roles; fine-grained permissions; su restriction via PAM modules"},
		{ControlID: "SR 2.2", Title: "Wireless Use Control", Status: "satisfied", Evidence: "Wi-Fi disable module (rfkill/nmcli); USB storage blacklist; removable media control"},
		{ControlID: "SR 2.8", Title: "Auditable Events", Status: statusForAudit(auditStatus), Evidence: "Comprehensive auditd rules for auth, authorization, network, time, and privilege events"},
		{ControlID: "SR 2.9", Title: "Audit Storage Capacity", Status: "satisfied", Evidence: "Log rotation with configurable retention; disk space monitoring with alerting"},
		{ControlID: "SR 2.10", Title: "Response to Audit Processing Failures", Status: "satisfied", Evidence: "auditd configured to halt on disk full/error; space_left alerts via syslog"},
		{ControlID: "SR 2.11", Title: "Timestamps", Status: "satisfied", Evidence: "NTP synchronization via chrony/systemd-timesyncd modules; time change auditing"},
		{ControlID: "SR 2.12", Title: "Non-Repudiation", Status: statusForAudit(auditStatus), Evidence: auditEvidence},

		// FR 3 — System Integrity (SI)
		{ControlID: "SR 3.1", Title: "Communication Integrity", Status: "satisfied", Evidence: "All agent communication over mTLS; TLS 1.2+ enforcement; strong cipher suites"},
		{ControlID: "SR 3.2", Title: "Malicious Code Protection", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): ClamAV enforcement modules; scheduled full-system scans; signature database updates. Coverage depends on module assignment."},
		{ControlID: "SR 3.3", Title: "Security Functionality Verification", Status: "satisfied", Evidence: "Systemd service verification on boot; continuous compliance monitoring; service state enforcement"},
		{ControlID: "SR 3.4", Title: "Software and Information Integrity", Status: "satisfied", Evidence: "AIDE file integrity monitoring modules; daily integrity checks; kernel module blacklisting"},
		{ControlID: "SR 3.5", Title: "Input Validation", Status: "satisfied", Evidence: "USB storage blacklist; removable media control; kernel module loading audit"},
		{ControlID: "SR 3.8", Title: "Session Integrity", Status: "satisfied", Evidence: "Automatic session timeout (TMOUT); SSH ClientAliveInterval; login grace time limits"},
		{ControlID: "SR 3.9", Title: "Protection of Audit Information", Status: statusForAudit(auditStatus), Evidence: "Tamper-evident hash chain; append-only log attributes; immutable audit rules (-e 2)"},

		// FR 4 — Data Confidentiality (DC)
		{ControlID: "SR 4.1", Title: "Information Confidentiality", Status: "satisfied", Evidence: "Secrets never in plaintext; file permissions enforcement; shadow file hardening"},
		{ControlID: "SR 4.3", Title: "Use of Cryptography", Status: "satisfied", Evidence: "TLS 1.2+ minimum; strong cipher enforcement in SSH and OpenSSL configs; mTLS everywhere"},

		// FR 5 — Restricted Data Flow (RDF)
		{ControlID: "SR 5.1", Title: "Network Segmentation", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): Zone-based nftables firewall modules; default-deny policies; per-zone CIDR allowlists via vars. Coverage depends on module assignment."},
		{ControlID: "SR 5.2", Title: "Zone Boundary Protection", Status: "satisfied", Evidence: "IP forwarding disabled; source routing rejected; ICMP redirect blocking; anti-spoofing (rp_filter)"},
		{ControlID: "SR 5.3", Title: "General Purpose Person-to-Person Communication Restrictions", Status: "satisfied", Evidence: "AllowTcpForwarding/X11Forwarding disabled; agent forwarding blocked; strict SSH config"},
		{ControlID: "SR 5.4", Title: "Application Partitioning", Status: "satisfied", Evidence: "Unnecessary services disabled (avahi, cups, NFS, rpcbind); telnet/rsh removed"},

		// FR 6 — Timely Response to Events (TRE)
		{ControlID: "SR 6.1", Title: "Audit Log Accessibility", Status: statusForAudit(auditStatus), Evidence: auditEvidence},
		{ControlID: "SR 6.2", Title: "Continuous Monitoring", Status: "satisfied", Evidence: "15-second convergence cycle; real-time drift detection; SMTP/webhook alerting; compliance sparklines"},

		// FR 7 — Resource Availability (RA)
		{ControlID: "SR 7.1", Title: "Denial of Service Protection", Status: "satisfied", Evidence: "SYN flood protection; connection tracking limits; resource limits (nproc, nofile, core dumps)"},
		{ControlID: "SR 7.2", Title: "Resource Management", Status: "satisfied", Evidence: "Disk space monitoring with threshold alerting; log rotation; backup retention policies"},
		{ControlID: "SR 7.3", Title: "Control System Backup", Status: "satisfied", Evidence: "Automated daily backup of critical configs; compressed archives; configurable retention"},
		{ControlID: "SR 7.4", Title: "Control System Recovery and Reconstitution", Status: "satisfied", Evidence: "Backup retention policies; recovery verification script; Litestream continuous DB replication"},
		{ControlID: "SR 7.6", Title: "Network and Security Configuration Settings", Status: "satisfied", Evidence: "YAML-defined desired state; idempotent enforcement; automatic drift correction every check-in"},
		{ControlID: "SR 7.7", Title: "Least Functionality", Status: "satisfied", Evidence: "Unnecessary packages removed (telnet, rsh); unnecessary services disabled; USB storage blacklisted"},
		{ControlID: "SR 7.8", Title: "Control System Component Inventory", Status: "satisfied", Evidence: "25 trait collectors report OS, hardware, network, packages; fleet-wide inventory via live query"},
	}
}

func (h *ReportHandler) buildSOXFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Change Management — ENFORCED
		{ControlID: "ITGC-CM-1", Title: "Change Authorization", Status: "satisfied", Evidence: "Config publish requires explicit operator action; approval workflow available; " + auditEvidence},
		{ControlID: "ITGC-CM-2", Title: "Change Documentation", Status: "satisfied", Evidence: "Every config change recorded in tamper-evident audit chain with timestamp, operator, and diff"},
		{ControlID: "ITGC-CM-3", Title: "Change Testing", Status: "satisfied", Evidence: "vigocli config publish --dry-run validates changes before deployment; vigocli test runs config tests"},
		{ControlID: "ITGC-CM-4", Title: "Change Reversibility", Status: "satisfied", Evidence: "Full config rollback via vigocli config rollback; blast-radius monitoring with auto-rollback"},
		// Access Controls — ENFORCED
		{ControlID: "ITGC-AC-1", Title: "Authentication Enforcement", Status: "satisfied", Evidence: "SSH key-only authentication enforced; password login disabled; root login disabled"},
		{ControlID: "ITGC-AC-2", Title: "Authorization Controls", Status: "satisfied", Evidence: "sudo restricted to named groups; RBAC with admin/viewer roles"},
		{ControlID: "ITGC-AC-3", Title: "Account Management", Status: "satisfied", Evidence: "Shared/default accounts disabled; guest account removed; user state enforced"},
		{ControlID: "ITGC-AC-4", Title: "Password Policy", Status: "satisfied", Evidence: "PAM password complexity enforced; sudo timeout configured"},
		{ControlID: "ITGC-AC-5", Title: "Privileged Access Monitoring", Status: "satisfied", Evidence: "Audit trail records all administrative actions; emergency access logged"},
		// System Integrity — ENFORCED
		{ControlID: "ITGC-SI-1", Title: "File Integrity Monitoring", Status: "satisfied", Evidence: "AIDE monitors critical system files; changes detected and reported via traits"},
		{ControlID: "ITGC-SI-2", Title: "Continuous Enforcement", Status: "satisfied", Evidence: "15-second convergence interval; drift auto-remediated; " + auditEvidence},
		{ControlID: "ITGC-SI-3", Title: "System Baseline", Status: "satisfied", Evidence: "Desired state defined in YAML modules; any deviation detected and corrected within check-in interval"},
	}
}

func (h *ReportHandler) buildFINRAFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Rule 4370 Infrastructure — ENFORCED
		{ControlID: "Rule-4370-1", Title: "Critical Infrastructure Monitoring", Status: "satisfied", Evidence: "All infrastructure services monitored via 15-second convergence; Prometheus metrics exposed"},
		{ControlID: "Rule-4370-2", Title: "Infrastructure Documentation", Status: "satisfied", Evidence: "25 trait collectors provide automated system inventory; fleet query for real-time state"},
		{ControlID: "Rule-4370-3", Title: "Service Availability", Status: "satisfied", Evidence: "Critical services enforced running/enabled; drift auto-remediated; " + auditEvidence},
		// BCP — MONITORED (requires external validation)
		{ControlID: "BCP-1", Title: "Backup Configuration", Status: "partial", Evidence: "MONITORED: Vigo verifies backup service configurations exist. Actual recovery capability requires external DR testing."},
		{ControlID: "BCP-2", Title: "Recovery Procedures", Status: "partial", Evidence: "MONITORED: Vigo can verify DR systems are configured. Recovery time objectives require external validation through actual failover tests."},
		{ControlID: "BCP-3", Title: "BCP Testing", Status: "not_satisfied", Evidence: "REQUIRES EXTERNAL VALIDATION: BCP testing requires actual disaster recovery exercises. Vigo can monitor that test schedules exist but cannot perform DR tests."},
		// WORM — MONITORED (requires external validation)
		{ControlID: "SEC-17a4-1", Title: "WORM Storage Configuration", Status: "partial", Evidence: "MONITORED: Vigo monitors that archival services are running and configured. WORM storage compliance requires a dedicated archival platform (Smarsh, Veritas, S3 Object Lock)."},
		{ControlID: "SEC-17a4-2", Title: "Retention Policy Enforcement", Status: "partial", Evidence: "MONITORED: Vigo verifies retention configuration files exist. Actual write-once-read-many enforcement is a property of the storage platform, not Vigo."},
	}
}

func (h *ReportHandler) buildMiFID2Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// System Resilience — ENFORCED
		{ControlID: "RTS7-Art5-1", Title: "Continuous System Monitoring", Status: "satisfied", Evidence: "15-second convergence provides continuous monitoring; offline convergence ensures enforcement during outages"},
		{ControlID: "RTS7-Art5-2", Title: "System Availability", Status: "satisfied", Evidence: "Critical services enforced running/enabled; automatic remediation on drift; " + auditEvidence},
		{ControlID: "RTS7-Art5-3", Title: "Incident Detection", Status: "satisfied", Evidence: "Security alerts fire on rootkit, malware, integrity breach, critical CVE, hardening score drop"},
		// Change Management — ENFORCED
		{ControlID: "RTS7-Art9-1", Title: "Change Documentation", Status: "satisfied", Evidence: "Every change recorded in tamper-evident audit chain; config publish history with diff"},
		{ControlID: "RTS7-Art9-2", Title: "Change Authorization", Status: "satisfied", Evidence: "Config publish requires operator action; approval workflow available; rollback capability"},
		{ControlID: "RTS7-Art9-3", Title: "Audit Trail Integrity", Status: "satisfied", Evidence: "Hash-chained audit log; " + auditEvidence},
		// Capacity & Kill Switch — MONITORED (requires external validation)
		{ControlID: "RTS7-Art11-1", Title: "Capacity Monitoring", Status: "partial", Evidence: "MONITORED: Vigo collects CPU, memory, disk, and network traits. Load/stress testing to prove peak capacity handling requires external tools."},
		{ControlID: "RTS7-Art11-2", Title: "Capacity Planning", Status: "partial", Evidence: "MONITORED: Trait data supports capacity analysis. Capacity planning decisions and stress test validation are external processes."},
		{ControlID: "RTS7-Art14-1", Title: "Kill Switch Mechanism", Status: "partial", Evidence: "MONITORED: Vigo verifies kill switch software is installed and configured. Sub-millisecond trading halt requires dedicated trading platform integration — Vigo cannot be the kill switch itself."},
	}
}

func (h *ReportHandler) buildCCSSFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Key Storage — ENFORCED
		{ControlID: "CCSS-2.1", Title: "Key Storage Access Controls", Status: "satisfied", Evidence: "Filesystem permissions enforced on key directories (0700, crypto:crypto)"},
		{ControlID: "CCSS-2.2", Title: "Key Backup Protection", Status: "satisfied", Evidence: "Backup directory permissions enforced; access restricted to crypto user"},
		{ControlID: "CCSS-2.3", Title: "HSM Service Availability", Status: "satisfied", Evidence: "HSM daemon (pcscd) enforced running and enabled"},
		{ControlID: "CCSS-2.4", Title: "Key Access Limits", Status: "satisfied", Evidence: "Process and file descriptor limits enforced for crypto user"},
		// Network Security — ENFORCED
		{ControlID: "CCSS-3.1", Title: "SSH Hardening", Status: "satisfied", Evidence: "Key-only authentication, root login disabled, password auth disabled"},
		{ControlID: "CCSS-3.2", Title: "Network Segmentation", Status: "satisfied", Evidence: "Default-deny firewall enforced; only required ports open"},
		// Authorization — ENFORCED
		{ControlID: "CCSS-6.1", Title: "Keyholder Authorization", Status: "satisfied", Evidence: "Access restricted to named crypto user; sudo requires password"},
		{ControlID: "CCSS-6.2", Title: "Keyholder Revocation", Status: "satisfied", Evidence: "User state enforced; revoked users removed automatically"},
		// Audit — ENFORCED
		{ControlID: "CCSS-8.1", Title: "Audit Logging", Status: "satisfied", Evidence: "auditd running; file integrity monitoring via AIDE; " + auditEvidence},
		{ControlID: "CCSS-8.2", Title: "Log Retention", Status: "satisfied", Evidence: "365-day log retention enforced via logrotate"},
		// Data Sanitization — MONITORED
		{ControlID: "CCSS-9.1", Title: "Data Sanitization", Status: "partial", Evidence: "MONITORED: File integrity changes detected. Actual cryptographic key destruction requires HSM-specific procedures — external validation required."},
		// Proof of Reserves — EXTERNAL
		{ControlID: "CCSS-10.1", Title: "Proof of Reserves", Status: "not_satisfied", Evidence: "REQUIRES EXTERNAL VALIDATION: Proof of reserves is a cryptographic accounting procedure performed by auditors. Vigo monitors infrastructure but cannot perform reserve attestation."},
	}
}

func (h *ReportHandler) buildFDA21CFR11Findings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Access Controls — ENFORCED
		{ControlID: "11.10-d", Title: "System Access Controls", Status: "satisfied", Evidence: "SSH key-only authentication; session timeout (300s); password complexity enforced"},
		{ControlID: "11.10-g", Title: "Authority Checks", Status: "satisfied", Evidence: "RBAC enforced; sudo restricted; shared accounts disabled; guest account removed"},
		{ControlID: "11.300-a", Title: "Unique User Identification", Status: "satisfied", Evidence: "Individual user accounts enforced; shared/generic accounts removed"},
		{ControlID: "11.300-b", Title: "Password Controls", Status: "satisfied", Evidence: "Minimum 12 chars, complexity requirements (upper, lower, digit, special) enforced via PAM"},
		// Audit Trail — ENFORCED
		{ControlID: "11.10-e", Title: "Audit Trail", Status: "satisfied", Evidence: "Infrastructure audit trail via auditd + Vigo tamper-evident chain; 730-day retention; " + auditEvidence},
		{ControlID: "11.10-e-1", Title: "Audit Trail Integrity", Status: "satisfied", Evidence: "Hash-chained audit log prevents undetected modification; " + auditEvidence},
		{ControlID: "11.10-k-2", Title: "Documentation of Changes", Status: "satisfied", Evidence: "Config changes logged with timestamp, operator, and audit rules for privilege/user/group changes"},
		// System Integrity — ENFORCED
		{ControlID: "11.10-a", Title: "System Validation", Status: "partial", Evidence: "MONITORED: File integrity monitoring via AIDE enforced. IQ/OQ/PQ qualification and application-level validation are external processes — Vigo enforces infrastructure integrity only."},
		{ControlID: "11.10-c", Title: "Record Protection", Status: "satisfied", Evidence: "File permissions enforced; integrity monitoring detects unauthorized changes; NTP sync for accurate timestamps"},
		// Electronic Signatures — EXTERNAL
		{ControlID: "11.10-h", Title: "Electronic Signatures", Status: "not_satisfied", Evidence: "REQUIRES EXTERNAL VALIDATION: Electronic signatures are an application-level requirement (LIMS, MES, ERP). Vigo enforces infrastructure security but does not provide Part 11 compliant e-signature capability."},
	}
}

func (h *ReportHandler) buildGamingFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// System Integrity — ENFORCED (Nevada, MGA, UKGC)
		{ControlID: "NGC-TS-1.1", Title: "System Hardening (Nevada)", Status: "satisfied", Evidence: "SSH hardening enforced; root login disabled; key-only auth; auditd running"},
		{ControlID: "NGC-TS-1.2", Title: "File Integrity (Nevada)", Status: "satisfied", Evidence: "AIDE file integrity monitoring; continuous convergence; " + auditEvidence},
		{ControlID: "MGA-RGR-4.1", Title: "System Security (Malta)", Status: "satisfied", Evidence: "Access controls enforced; audit logging active; change management via Vigo"},
		{ControlID: "MGA-RGR-4.2", Title: "Data Integrity (Malta)", Status: "satisfied", Evidence: "File integrity monitoring; tamper-evident audit chain; 15-second convergence"},
		{ControlID: "UKGC-RTS-4A", Title: "Security Controls (UK)", Status: "satisfied", Evidence: "SSH hardening, access controls, audit logging enforced continuously"},
		{ControlID: "UKGC-RTS-4B", Title: "Change Management (UK)", Status: "satisfied", Evidence: "All changes logged in audit chain; config publish with approval workflow available"},
		// Availability — ENFORCED
		{ControlID: "NGC-TS-2.1", Title: "Service Monitoring (Nevada)", Status: "satisfied", Evidence: "Node exporter + NTP + syslog enforced running; 15-second check-in"},
		{ControlID: "NGC-TS-2.2", Title: "Recovery Capability (Nevada)", Status: "satisfied", Evidence: "Offline convergence maintains policy during outages; automatic reconnection"},
		{ControlID: "MGA-RGR-5.1", Title: "System Availability (Malta)", Status: "satisfied", Evidence: "Critical services enforced running/enabled; drift auto-remediated"},
		{ControlID: "MGA-RGR-5.2", Title: "Disaster Recovery (Malta)", Status: "partial", Evidence: "MONITORED: Vigo enforces service availability and can verify backup configs. Actual DR testing is an external validation requirement."},
		{ControlID: "UKGC-RTS-5A", Title: "Resilience (UK)", Status: "satisfied", Evidence: "15-second convergence; offline enforcement; automatic recovery"},
		{ControlID: "UKGC-RTS-5B", Title: "Business Continuity (UK)", Status: "partial", Evidence: "MONITORED: Service enforcement provides operational continuity. Full BCP testing requires external validation."},
		// RNG & Gaming Devices — EXTERNAL
		{ControlID: "NGC-TS-3.1", Title: "Change Control for Gaming Systems (Nevada)", Status: "satisfied", Evidence: "Config change management enforced via Vigo audit chain; all changes documented and reversible"},
		{ControlID: "NGC-TS-4.1", Title: "RNG Certification (Nevada)", Status: "not_satisfied", Evidence: "REQUIRES EXTERNAL VALIDATION: RNG certification requires accredited testing lab (GLI, BMM). Vigo monitors RNG service status but cannot certify randomness."},
		{ControlID: "NGC-TS-4.2", Title: "RNG Monitoring (Nevada)", Status: "partial", Evidence: "MONITORED: Vigo verifies RNG daemon is running and configured. Statistical randomness testing requires GLI/BMM/eCOGRA certification."},
		{ControlID: "MGA-RGR-6.1", Title: "RNG Requirements (Malta)", Status: "partial", Evidence: "MONITORED: RNG service availability monitored. MGA RNG certification requires eCOGRA or equivalent accredited lab."},
		{ControlID: "UKGC-RTS-6A", Title: "RNG Testing (UK)", Status: "not_satisfied", Evidence: "REQUIRES EXTERNAL VALIDATION: UKGC requires independent RNG testing by approved test houses. Vigo monitors infrastructure only."},
	}
}

func (h *ReportHandler) buildCISWindowsFindings(auditStatus AuditChainStatus) []ControlFinding {
	return []ControlFinding{
		// Account Policies — ENFORCED
		{ControlID: "1.1.1", Title: "Password History", Status: "satisfied", Evidence: "24 passwords remembered enforced via local security policy"},
		{ControlID: "1.1.2", Title: "Maximum Password Age", Status: "satisfied", Evidence: "365 days enforced"},
		{ControlID: "1.1.3", Title: "Minimum Password Age", Status: "satisfied", Evidence: "1 day enforced"},
		{ControlID: "1.1.4", Title: "Minimum Password Length", Status: "satisfied", Evidence: "14 characters enforced"},
		{ControlID: "1.1.5", Title: "Password Complexity", Status: "satisfied", Evidence: "Complexity requirements enforced"},
		{ControlID: "1.2.1", Title: "Account Lockout Duration", Status: "satisfied", Evidence: "15 minutes enforced"},
		{ControlID: "1.2.2", Title: "Account Lockout Threshold", Status: "satisfied", Evidence: "5 invalid attempts enforced"},
		{ControlID: "1.2.3", Title: "Reset Account Lockout Counter", Status: "satisfied", Evidence: "15 minutes enforced"},
		// Security Options — ENFORCED
		{ControlID: "2.3.1.1", Title: "Guest Account Disabled", Status: "satisfied", Evidence: "Guest account disabled via security policy"},
		{ControlID: "2.3.7.1", Title: "Interactive Logon Message", Status: "satisfied", Evidence: "Legal notice caption and text enforced"},
		{ControlID: "2.3.10.1", Title: "SMB Signing Required (Server)", Status: "satisfied", Evidence: "RequireSecuritySignature=1 in registry"},
		// Windows Firewall — ENFORCED
		{ControlID: "9.1.1", Title: "Domain Profile Enabled", Status: "satisfied", Evidence: "Windows Firewall domain profile enabled with default inbound block"},
		{ControlID: "9.1.2", Title: "Domain Default Inbound", Status: "satisfied", Evidence: "Default inbound action: block"},
		{ControlID: "9.2.1", Title: "Private Profile Enabled", Status: "satisfied", Evidence: "Windows Firewall private profile enabled"},
		{ControlID: "9.3.1", Title: "Public Profile Enabled", Status: "satisfied", Evidence: "Windows Firewall public profile enabled"},
		// Audit Policies — ENFORCED
		{ControlID: "17.1.1", Title: "Audit Credential Validation", Status: "satisfied", Evidence: "Success and Failure auditing enforced"},
		{ControlID: "17.2.1", Title: "Audit Security Group Management", Status: "satisfied", Evidence: "Success auditing enforced"},
		{ControlID: "17.3.1", Title: "Audit Logon", Status: "satisfied", Evidence: "Success and Failure auditing enforced"},
		{ControlID: "17.5.1", Title: "Audit Process Creation", Status: "satisfied", Evidence: "Success auditing enforced"},
		{ControlID: "17.9.1", Title: "Audit System Integrity", Status: "satisfied", Evidence: "Success and Failure auditing enforced"},
		// Windows Defender — ENFORCED
		{ControlID: "18.9.47.1", Title: "Windows Defender Real-Time Protection", Status: "satisfied", Evidence: "Real-time monitoring and behavior monitoring enforced"},
	}
}

// ---------- CIS Ubuntu ----------

func (h *ReportHandler) buildCISUbuntuFindings(auditStatus AuditChainStatus) []ControlFinding {
	return []ControlFinding{
		// Section 1 — Filesystem
		{ControlID: "1.1.1.1", Title: "Disable cramfs", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists cramfs via modprobe.d"},
		{ControlID: "1.1.1.2", Title: "Disable freevxfs", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists freevxfs via modprobe.d"},
		{ControlID: "1.1.1.3", Title: "Disable jffs2", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists jffs2 via modprobe.d"},
		{ControlID: "1.1.1.4", Title: "Disable hfs", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists hfs via modprobe.d"},
		{ControlID: "1.1.1.5", Title: "Disable hfsplus", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists hfsplus via modprobe.d"},
		{ControlID: "1.1.1.6", Title: "Disable squashfs", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists squashfs via modprobe.d (Level 2)"},
		{ControlID: "1.1.1.7", Title: "Disable udf", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists udf via modprobe.d"},
		{ControlID: "1.1.1.8", Title: "Disable USB Storage", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: blacklists usb-storage via modprobe.d (Level 2)"},
		{ControlID: "1.4.1", Title: "Bootloader Permissions", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: chmod 0400 /boot/grub/grub.cfg"},
		{ControlID: "1.5.1", Title: "ASLR Enabled", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: kernel.randomize_va_space = 2"},
		{ControlID: "1.5.2", Title: "Restrict ptrace", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: kernel.yama.ptrace_scope = 1"},
		{ControlID: "1.5.3", Title: "Core Dumps Restricted", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: hard core 0, fs.suid_dumpable = 0"},
		{ControlID: "1.5.4", Title: "Apport Disabled", Status: "satisfied", Evidence: "cis-ubuntu-filesystem module: apport service stopped and disabled"},

		// Section 2 — Services
		{ControlID: "2.1.1", Title: "Time Synchronization", Status: "satisfied", Evidence: "cis-ubuntu-services module: chrony installed and running"},
		{ControlID: "2.1.2", Title: "X Window System Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: xserver-xorg package absent"},
		{ControlID: "2.1.3", Title: "Avahi Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: avahi-daemon stopped and disabled"},
		{ControlID: "2.1.4", Title: "CUPS Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: cups stopped and disabled"},
		{ControlID: "2.1.5", Title: "DHCP Server Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: isc-dhcp-server stopped and disabled"},
		{ControlID: "2.1.6", Title: "LDAP Server Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: slapd stopped and disabled"},
		{ControlID: "2.1.7", Title: "NFS Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: nfs-kernel-server stopped and disabled"},
		{ControlID: "2.1.8", Title: "DNS Server Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: named stopped and disabled"},
		{ControlID: "2.1.9", Title: "FTP Server Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: vsftpd stopped and disabled"},
		{ControlID: "2.1.10", Title: "HTTP Server Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: apache2 stopped and disabled"},
		{ControlID: "2.1.11", Title: "IMAP/POP3 Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: dovecot stopped and disabled"},
		{ControlID: "2.1.12", Title: "Samba Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: smbd stopped and disabled"},
		{ControlID: "2.1.13", Title: "Squid Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: squid stopped and disabled"},
		{ControlID: "2.1.14", Title: "SNMP Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: snmpd stopped and disabled"},
		{ControlID: "2.1.15", Title: "RPC Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: rpcbind stopped and disabled"},
		{ControlID: "2.1.16", Title: "rsync Disabled", Status: "satisfied", Evidence: "cis-ubuntu-services module: rsync stopped and disabled"},
		{ControlID: "2.1.17", Title: "NIS Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: nis package absent"},
		{ControlID: "2.2.1", Title: "rsh Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: rsh-client package absent"},
		{ControlID: "2.2.2", Title: "talk Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: talk package absent"},
		{ControlID: "2.2.3", Title: "telnet Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: telnet package absent"},
		{ControlID: "2.2.4", Title: "LDAP Clients Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: ldap-utils package absent"},
		{ControlID: "2.2.5", Title: "rpcbind Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: rpcbind package absent"},
		{ControlID: "2.2.6", Title: "FTP Client Removed", Status: "satisfied", Evidence: "cis-ubuntu-services module: ftp package absent"},

		// Section 3 — Network
		{ControlID: "3.1.1", Title: "Disable DCCP", Status: "satisfied", Evidence: "cis-ubuntu-network module: blacklists DCCP, SCTP, RDS, TIPC"},
		{ControlID: "3.2.1", Title: "IP Forwarding Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: net.ipv4.ip_forward = 0"},
		{ControlID: "3.2.2", Title: "Packet Redirects Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: send_redirects = 0"},
		{ControlID: "3.2.3", Title: "Source Routed Packets Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: accept_source_route = 0"},
		{ControlID: "3.2.4", Title: "ICMP Redirects Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: accept_redirects = 0"},
		{ControlID: "3.2.5", Title: "Secure ICMP Redirects Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: secure_redirects = 0"},
		{ControlID: "3.2.6", Title: "Suspicious Packets Logged", Status: "satisfied", Evidence: "cis-ubuntu-network module: log_martians = 1"},
		{ControlID: "3.2.7", Title: "Broadcast ICMP Ignored", Status: "satisfied", Evidence: "cis-ubuntu-network module: icmp_echo_ignore_broadcasts = 1"},
		{ControlID: "3.2.8", Title: "Bogus ICMP Ignored", Status: "satisfied", Evidence: "cis-ubuntu-network module: icmp_ignore_bogus_error_responses = 1"},
		{ControlID: "3.2.9", Title: "Reverse Path Filtering", Status: "satisfied", Evidence: "cis-ubuntu-network module: rp_filter = 1"},
		{ControlID: "3.3.1", Title: "TCP SYN Cookies", Status: "satisfied", Evidence: "cis-ubuntu-network module: tcp_syncookies = 1"},
		{ControlID: "3.3.2", Title: "IPv6 Router Ads Disabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: accept_ra = 0"},
		{ControlID: "3.4.1", Title: "UFW Installed", Status: "satisfied", Evidence: "cis-ubuntu-network module: ufw package present"},
		{ControlID: "3.4.2", Title: "UFW Default Deny", Status: "satisfied", Evidence: "cis-ubuntu-network module: ufw default deny incoming"},
		{ControlID: "3.4.4", Title: "UFW Enabled", Status: "satisfied", Evidence: "cis-ubuntu-network module: ufw enabled after default rules set"},

		// Section 4 — Logging
		{ControlID: "4.1.1.1", Title: "auditd Installed", Status: "satisfied", Evidence: "cis-ubuntu-logging module: auditd + audispd-plugins packages present"},
		{ControlID: "4.1.1.2", Title: "auditd Enabled", Status: "satisfied", Evidence: "cis-ubuntu-logging module: auditd service running and enabled"},
		{ControlID: "4.1.1.3", Title: "Audit at Boot", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit=1 in GRUB_CMDLINE_LINUX"},
		{ControlID: "4.1.3", Title: "Date/Time Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for adjtimex, settimeofday, clock_settime"},
		{ControlID: "4.1.4", Title: "User/Group Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for passwd, shadow, group, gshadow, opasswd"},
		{ControlID: "4.1.5", Title: "Network Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for hostname, hosts, netplan"},
		{ControlID: "4.1.6", Title: "MAC Policy Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for apparmor"},
		{ControlID: "4.1.7", Title: "Login/Logout Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for faillog, lastlog, tallylog"},
		{ControlID: "4.1.8", Title: "Session Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for utmp, wtmp, btmp"},
		{ControlID: "4.1.9", Title: "Permission Change Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for chmod, chown, setxattr"},
		{ControlID: "4.1.10", Title: "Unauthorized Access Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for EACCES/EPERM on file operations"},
		{ControlID: "4.1.12", Title: "Mount Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for mount syscall"},
		{ControlID: "4.1.13", Title: "File Deletion Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for unlink, rename"},
		{ControlID: "4.1.14", Title: "Sudoers Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for /etc/sudoers"},
		{ControlID: "4.1.15", Title: "Sudo Log Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for /var/log/sudo.log"},
		{ControlID: "4.1.16", Title: "Kernel Module Audit", Status: "satisfied", Evidence: "cis-ubuntu-logging module: audit rules for insmod, rmmod, modprobe, init_module"},
		{ControlID: "4.1.17", Title: "Immutable Audit Rules", Status: "satisfied", Evidence: "cis-ubuntu-logging module: -e 2 flag requires reboot to change rules"},
		{ControlID: "4.2.1.1", Title: "rsyslog Installed", Status: "satisfied", Evidence: "cis-ubuntu-logging module: rsyslog package present"},
		{ControlID: "4.2.1.2", Title: "rsyslog Enabled", Status: "satisfied", Evidence: "cis-ubuntu-logging module: rsyslog service running and enabled"},
		{ControlID: "4.2.1.3", Title: "rsyslog File Permissions", Status: "satisfied", Evidence: "cis-ubuntu-logging module: $FileCreateMode 0640"},
		{ControlID: "4.2.3", Title: "Log File Permissions", Status: "satisfied", Evidence: "cis-ubuntu-logging module: removes group-write and other-access from /var/log"},

		// Section 5 — Access/Auth
		{ControlID: "5.1.1", Title: "Cron Enabled", Status: "satisfied", Evidence: "cis-ubuntu-access module: cron service running and enabled"},
		{ControlID: "5.1.2", Title: "Crontab Permissions", Status: "satisfied", Evidence: "cis-ubuntu-access module: /etc/crontab 0600 root:root"},
		{ControlID: "5.1.8", Title: "Cron Restricted", Status: "satisfied", Evidence: "cis-ubuntu-access module: /etc/cron.allow with root only"},
		{ControlID: "5.2.1", Title: "sshd_config Permissions", Status: "satisfied", Evidence: "cis-ubuntu-access module: /etc/ssh/sshd_config 0600 root:root"},
		{ControlID: "5.2.5", Title: "SSH LogLevel VERBOSE", Status: "satisfied", Evidence: "cis-ubuntu-access module: LogLevel VERBOSE in sshd_config.d"},
		{ControlID: "5.2.6", Title: "SSH X11 Forwarding Off", Status: "satisfied", Evidence: "cis-ubuntu-access module: X11Forwarding no"},
		{ControlID: "5.2.7", Title: "SSH MaxAuthTries", Status: "satisfied", Evidence: "cis-ubuntu-access module: MaxAuthTries 4"},
		{ControlID: "5.2.10", Title: "SSH No Root Login", Status: "satisfied", Evidence: "cis-ubuntu-access module: PermitRootLogin no"},
		{ControlID: "5.2.11", Title: "SSH No Empty Passwords", Status: "satisfied", Evidence: "cis-ubuntu-access module: PermitEmptyPasswords no"},
		{ControlID: "5.2.13", Title: "SSH Strong Ciphers", Status: "satisfied", Evidence: "cis-ubuntu-access module: aes256-gcm, chacha20-poly1305, aes256-ctr"},
		{ControlID: "5.2.14", Title: "SSH Strong MACs", Status: "satisfied", Evidence: "cis-ubuntu-access module: hmac-sha2-512-etm, hmac-sha2-256-etm"},
		{ControlID: "5.2.15", Title: "SSH Strong KEX", Status: "satisfied", Evidence: "cis-ubuntu-access module: curve25519-sha256, diffie-hellman-group16-sha512"},
		{ControlID: "5.2.16", Title: "SSH Client Alive", Status: "satisfied", Evidence: "cis-ubuntu-access module: ClientAliveInterval 300, ClientAliveCountMax 3"},
		{ControlID: "5.2.18", Title: "SSH Banner", Status: "satisfied", Evidence: "cis-ubuntu-access module: Banner /etc/issue.net configured"},
		{ControlID: "5.2.20", Title: "SSH AllowTcpForwarding Off", Status: "satisfied", Evidence: "cis-ubuntu-access module: AllowTcpForwarding no"},
		{ControlID: "5.3.1", Title: "PAM pwquality", Status: "satisfied", Evidence: "cis-ubuntu-access module: libpam-pwquality installed, minlen 14, minclass 4"},
		{ControlID: "5.3.3", Title: "PAM faillock", Status: "satisfied", Evidence: "cis-ubuntu-access module: deny 5, unlock_time 900"},
		{ControlID: "5.3.4", Title: "Password History", Status: "satisfied", Evidence: "cis-ubuntu-access module: remember=5 in pam_unix"},
		{ControlID: "5.4.1.1", Title: "Password Aging", Status: "satisfied", Evidence: "cis-ubuntu-access module: PASS_MAX_DAYS 365, PASS_MIN_DAYS 1, PASS_WARN_AGE 7"},
		{ControlID: "5.4.2", Title: "System Accounts Locked", Status: "satisfied", Evidence: "cis-ubuntu-access module: UID < 1000 accounts locked with nologin shell"},
		{ControlID: "5.4.4", Title: "Default umask 027", Status: "satisfied", Evidence: "cis-ubuntu-access module: /etc/profile.d/cis-umask.sh"},
		{ControlID: "5.4.5", Title: "Shell Timeout", Status: "satisfied", Evidence: "cis-ubuntu-access module: TMOUT=900 readonly"},

		// Section 6 — Maintenance
		{ControlID: "6.1.2", Title: "/etc/passwd Permissions", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: 644 root:root"},
		{ControlID: "6.1.4", Title: "/etc/shadow Permissions", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: 640 root:shadow"},
		{ControlID: "6.1.6", Title: "/etc/group Permissions", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: 644 root:root"},
		{ControlID: "6.1.8", Title: "/etc/gshadow Permissions", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: 640 root:shadow"},
		{ControlID: "6.1.10", Title: "No World-Writable Files", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: removes world-writable permission from all files"},
		{ControlID: "6.2.1", Title: "No Empty Passwords", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: locks accounts with empty password fields"},
		{ControlID: "6.2.2", Title: "No UID 0 Except Root", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: verifies only root has UID 0"},
		{ControlID: "6.2.5", Title: "Home Directory Permissions", Status: "satisfied", Evidence: "cis-ubuntu-maintenance module: removes group-write and other-access from home directories"},
	}
}

func (h *ReportHandler) handleCISUbuntuReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS Ubuntu Linux Benchmark Report", "vigo-cis-ubuntu",
		h.buildCISUbuntuFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleCISUbuntuReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS Ubuntu Linux Benchmark Report", "vigo-cis-ubuntu",
		h.buildCISUbuntuFindings(h.auditStatusFromRequest(r)), true)
}

// ---------- CIS RHEL ----------

func (h *ReportHandler) buildCISRHELFindings(auditStatus AuditChainStatus) []ControlFinding {
	return []ControlFinding{
		// Section 1 — Filesystem
		{ControlID: "1.1.1.1", Title: "Disable cramfs", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists cramfs via modprobe.d"},
		{ControlID: "1.1.1.2", Title: "Disable freevxfs", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists freevxfs via modprobe.d"},
		{ControlID: "1.1.1.3", Title: "Disable jffs2", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists jffs2 via modprobe.d"},
		{ControlID: "1.1.1.4", Title: "Disable hfs", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists hfs via modprobe.d"},
		{ControlID: "1.1.1.5", Title: "Disable hfsplus", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists hfsplus via modprobe.d"},
		{ControlID: "1.1.1.6", Title: "Disable squashfs", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists squashfs via modprobe.d (Level 2)"},
		{ControlID: "1.1.1.7", Title: "Disable udf", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists udf via modprobe.d"},
		{ControlID: "1.1.1.8", Title: "Disable USB Storage", Status: "satisfied", Evidence: "cis-rhel-filesystem module: blacklists usb-storage via modprobe.d (Level 2)"},
		{ControlID: "1.4.1", Title: "Bootloader Permissions", Status: "satisfied", Evidence: "cis-rhel-filesystem module: chmod 0400 grub.cfg (grub2 or EFI path)"},
		{ControlID: "1.5.1", Title: "ASLR Enabled", Status: "satisfied", Evidence: "cis-rhel-filesystem module: kernel.randomize_va_space = 2"},
		{ControlID: "1.5.2", Title: "Restrict ptrace", Status: "satisfied", Evidence: "cis-rhel-filesystem module: kernel.yama.ptrace_scope = 1"},
		{ControlID: "1.5.3", Title: "Core Dumps Restricted", Status: "satisfied", Evidence: "cis-rhel-filesystem module: hard core 0, fs.suid_dumpable = 0, systemd coredump Storage=none"},

		// Section 2 — Services
		{ControlID: "2.1.1", Title: "Time Synchronization", Status: "satisfied", Evidence: "cis-rhel-services module: chrony installed and running"},
		{ControlID: "2.1.2", Title: "X Window Removed", Status: "satisfied", Evidence: "cis-rhel-services module: xorg-x11-server-Xorg package absent"},
		{ControlID: "2.1.3", Title: "Avahi Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: avahi-daemon stopped and disabled"},
		{ControlID: "2.1.4", Title: "CUPS Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: cups stopped and disabled"},
		{ControlID: "2.1.5", Title: "DHCP Server Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: dhcpd stopped and disabled"},
		{ControlID: "2.1.6", Title: "LDAP Server Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: slapd stopped and disabled"},
		{ControlID: "2.1.7", Title: "NFS Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: nfs-server stopped and disabled"},
		{ControlID: "2.1.8", Title: "DNS Server Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: named stopped and disabled"},
		{ControlID: "2.1.9", Title: "FTP Server Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: vsftpd stopped and disabled"},
		{ControlID: "2.1.10", Title: "HTTP Server Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: httpd stopped and disabled"},
		{ControlID: "2.1.11", Title: "IMAP/POP3 Disabled", Status: "satisfied", Evidence: "cis-rhel-services module: dovecot stopped and disabled"},
		{ControlID: "2.2.1", Title: "ypbind Removed", Status: "satisfied", Evidence: "cis-rhel-services module: ypbind package absent"},
		{ControlID: "2.2.2", Title: "telnet Removed", Status: "satisfied", Evidence: "cis-rhel-services module: telnet package absent"},
		{ControlID: "2.2.3", Title: "rsh Removed", Status: "satisfied", Evidence: "cis-rhel-services module: rsh package absent"},
		{ControlID: "2.2.4", Title: "talk Removed", Status: "satisfied", Evidence: "cis-rhel-services module: talk package absent"},
		{ControlID: "2.2.5", Title: "LDAP Clients Removed", Status: "satisfied", Evidence: "cis-rhel-services module: openldap-clients package absent"},

		// Section 3 — Network
		{ControlID: "3.1.1", Title: "Disable DCCP", Status: "satisfied", Evidence: "cis-rhel-network module: blacklists DCCP, SCTP, RDS, TIPC"},
		{ControlID: "3.2.1", Title: "IP Forwarding Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: net.ipv4.ip_forward = 0"},
		{ControlID: "3.2.2", Title: "Packet Redirects Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: send_redirects = 0"},
		{ControlID: "3.2.3", Title: "Source Routed Packets Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: accept_source_route = 0"},
		{ControlID: "3.2.4", Title: "ICMP Redirects Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: accept_redirects = 0"},
		{ControlID: "3.2.5", Title: "Secure ICMP Redirects Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: secure_redirects = 0"},
		{ControlID: "3.2.6", Title: "Suspicious Packets Logged", Status: "satisfied", Evidence: "cis-rhel-network module: log_martians = 1"},
		{ControlID: "3.2.7", Title: "Broadcast ICMP Ignored", Status: "satisfied", Evidence: "cis-rhel-network module: icmp_echo_ignore_broadcasts = 1"},
		{ControlID: "3.2.8", Title: "Bogus ICMP Ignored", Status: "satisfied", Evidence: "cis-rhel-network module: icmp_ignore_bogus_error_responses = 1"},
		{ControlID: "3.2.9", Title: "Reverse Path Filtering", Status: "satisfied", Evidence: "cis-rhel-network module: rp_filter = 1"},
		{ControlID: "3.3.1", Title: "TCP SYN Cookies", Status: "satisfied", Evidence: "cis-rhel-network module: tcp_syncookies = 1"},
		{ControlID: "3.3.2", Title: "IPv6 Router Ads Disabled", Status: "satisfied", Evidence: "cis-rhel-network module: accept_ra = 0"},
		{ControlID: "3.4.1.1", Title: "firewalld Installed", Status: "satisfied", Evidence: "cis-rhel-network module: firewalld package present"},
		{ControlID: "3.4.1.2", Title: "firewalld Enabled", Status: "satisfied", Evidence: "cis-rhel-network module: firewalld service running and enabled"},
		{ControlID: "3.4.2.1", Title: "Default Zone Drop", Status: "satisfied", Evidence: "cis-rhel-network module: firewall-cmd --set-default-zone=drop"},

		// Section 4 — Logging
		{ControlID: "4.1.1.1", Title: "audit Installed", Status: "satisfied", Evidence: "cis-rhel-logging module: audit + audit-libs packages present"},
		{ControlID: "4.1.1.2", Title: "auditd Enabled", Status: "satisfied", Evidence: "cis-rhel-logging module: auditd service running and enabled"},
		{ControlID: "4.1.1.3", Title: "Audit at Boot", Status: "satisfied", Evidence: "cis-rhel-logging module: audit=1 in GRUB_CMDLINE_LINUX"},
		{ControlID: "4.1.1.4", Title: "Audit Backlog Limit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit_backlog_limit=8192 in GRUB_CMDLINE_LINUX"},
		{ControlID: "4.1.3", Title: "Date/Time Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for adjtimex, settimeofday, clock_settime"},
		{ControlID: "4.1.4", Title: "User/Group Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for passwd, shadow, group, gshadow, opasswd"},
		{ControlID: "4.1.5", Title: "Network Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for hostname, hosts, sysconfig/network"},
		{ControlID: "4.1.6", Title: "MAC Policy Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for selinux"},
		{ControlID: "4.1.7", Title: "Login/Logout Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for faillock, lastlog"},
		{ControlID: "4.1.8", Title: "Session Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for utmp, wtmp, btmp"},
		{ControlID: "4.1.9", Title: "Permission Change Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for chmod, chown, setxattr"},
		{ControlID: "4.1.10", Title: "Unauthorized Access Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for EACCES/EPERM on file operations"},
		{ControlID: "4.1.12", Title: "Mount Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for mount syscall"},
		{ControlID: "4.1.13", Title: "File Deletion Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for unlink, rename"},
		{ControlID: "4.1.14", Title: "Sudoers Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for /etc/sudoers"},
		{ControlID: "4.1.16", Title: "Kernel Module Audit", Status: "satisfied", Evidence: "cis-rhel-logging module: audit rules for insmod, rmmod, modprobe, init_module"},
		{ControlID: "4.1.17", Title: "Immutable Audit Rules", Status: "satisfied", Evidence: "cis-rhel-logging module: -e 2 flag requires reboot to change rules"},
		{ControlID: "4.2.1.1", Title: "rsyslog Installed", Status: "satisfied", Evidence: "cis-rhel-logging module: rsyslog package present"},
		{ControlID: "4.2.1.2", Title: "rsyslog Enabled", Status: "satisfied", Evidence: "cis-rhel-logging module: rsyslog service running and enabled"},
		{ControlID: "4.2.1.3", Title: "rsyslog File Permissions", Status: "satisfied", Evidence: "cis-rhel-logging module: $FileCreateMode 0640"},
		{ControlID: "4.2.3", Title: "Log File Permissions", Status: "satisfied", Evidence: "cis-rhel-logging module: removes group-write and other-access from /var/log"},

		// Section 5 — Access/Auth
		{ControlID: "5.1.1", Title: "Cron Enabled", Status: "satisfied", Evidence: "cis-rhel-access module: crond service running and enabled"},
		{ControlID: "5.1.2", Title: "Crontab Permissions", Status: "satisfied", Evidence: "cis-rhel-access module: /etc/crontab 0600 root:root"},
		{ControlID: "5.1.8", Title: "Cron Restricted", Status: "satisfied", Evidence: "cis-rhel-access module: /etc/cron.allow with root only"},
		{ControlID: "5.2.1", Title: "sshd_config Permissions", Status: "satisfied", Evidence: "cis-rhel-access module: /etc/ssh/sshd_config 0600 root:root"},
		{ControlID: "5.2.5", Title: "SSH LogLevel VERBOSE", Status: "satisfied", Evidence: "cis-rhel-access module: LogLevel VERBOSE in sshd_config.d"},
		{ControlID: "5.2.6", Title: "SSH X11 Forwarding Off", Status: "satisfied", Evidence: "cis-rhel-access module: X11Forwarding no"},
		{ControlID: "5.2.10", Title: "SSH No Root Login", Status: "satisfied", Evidence: "cis-rhel-access module: PermitRootLogin no"},
		{ControlID: "5.2.11", Title: "SSH No Empty Passwords", Status: "satisfied", Evidence: "cis-rhel-access module: PermitEmptyPasswords no"},
		{ControlID: "5.2.13", Title: "SSH Strong Ciphers", Status: "satisfied", Evidence: "cis-rhel-access module: aes256-gcm, chacha20-poly1305, aes256-ctr"},
		{ControlID: "5.2.14", Title: "SSH Strong MACs", Status: "satisfied", Evidence: "cis-rhel-access module: hmac-sha2-512-etm, hmac-sha2-256-etm"},
		{ControlID: "5.2.15", Title: "SSH Strong KEX", Status: "satisfied", Evidence: "cis-rhel-access module: curve25519-sha256, diffie-hellman-group16-sha512"},
		{ControlID: "5.2.16", Title: "SSH Client Alive", Status: "satisfied", Evidence: "cis-rhel-access module: ClientAliveInterval 300, ClientAliveCountMax 3"},
		{ControlID: "5.2.18", Title: "SSH Banner", Status: "satisfied", Evidence: "cis-rhel-access module: Banner /etc/issue.net configured"},
		{ControlID: "5.3.1", Title: "authselect Profile", Status: "satisfied", Evidence: "cis-rhel-access module: authselect sssd with-faillock with-mkhomedir"},
		{ControlID: "5.3.2", Title: "Password Quality", Status: "satisfied", Evidence: "cis-rhel-access module: minlen 14, minclass 4 in pwquality.conf"},
		{ControlID: "5.3.3", Title: "Account Lockout", Status: "satisfied", Evidence: "cis-rhel-access module: deny 5, unlock_time 900 in faillock.conf"},
		{ControlID: "5.3.4", Title: "Password History", Status: "satisfied", Evidence: "cis-rhel-access module: remember=5 in system-auth"},
		{ControlID: "5.4.1", Title: "SELinux Enforcing", Status: "satisfied", Evidence: "cis-rhel-access module: setenforce 1, SELINUX=enforcing in config"},
		{ControlID: "5.4.3", Title: "SELinux Policy Targeted", Status: "satisfied", Evidence: "cis-rhel-access module: SELINUXTYPE=targeted"},
		{ControlID: "5.5.1.1", Title: "Password Aging", Status: "satisfied", Evidence: "cis-rhel-access module: PASS_MAX_DAYS 365, PASS_MIN_DAYS 1, PASS_WARN_AGE 7"},
		{ControlID: "5.5.2", Title: "System Accounts Locked", Status: "satisfied", Evidence: "cis-rhel-access module: UID < 1000 accounts locked with nologin shell"},
		{ControlID: "5.5.4", Title: "Default umask 027", Status: "satisfied", Evidence: "cis-rhel-access module: /etc/profile.d/cis-umask.sh"},
		{ControlID: "5.5.5", Title: "Shell Timeout", Status: "satisfied", Evidence: "cis-rhel-access module: TMOUT=900 readonly"},

		// Section 6 — Maintenance
		{ControlID: "6.1.2", Title: "/etc/passwd Permissions", Status: "satisfied", Evidence: "cis-rhel-maintenance module: 644 root:root"},
		{ControlID: "6.1.4", Title: "/etc/shadow Permissions", Status: "satisfied", Evidence: "cis-rhel-maintenance module: mode 0 root:root"},
		{ControlID: "6.1.6", Title: "/etc/group Permissions", Status: "satisfied", Evidence: "cis-rhel-maintenance module: 644 root:root"},
		{ControlID: "6.1.8", Title: "/etc/gshadow Permissions", Status: "satisfied", Evidence: "cis-rhel-maintenance module: mode 0 root:root"},
		{ControlID: "6.1.10", Title: "No World-Writable Files", Status: "satisfied", Evidence: "cis-rhel-maintenance module: removes world-writable permission from all files"},
		{ControlID: "6.2.1", Title: "No Empty Passwords", Status: "satisfied", Evidence: "cis-rhel-maintenance module: locks accounts with empty password fields"},
		{ControlID: "6.2.2", Title: "No UID 0 Except Root", Status: "satisfied", Evidence: "cis-rhel-maintenance module: verifies only root has UID 0"},
		{ControlID: "6.2.5", Title: "Home Directory Permissions", Status: "satisfied", Evidence: "cis-rhel-maintenance module: removes group-write and other-access from home directories"},
	}
}

func (h *ReportHandler) handleCISRHELReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS RHEL Benchmark Report", "vigo-cis-rhel",
		h.buildCISRHELFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleCISRHELReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS RHEL Benchmark Report", "vigo-cis-rhel",
		h.buildCISRHELFindings(h.auditStatusFromRequest(r)), true)
}

func (h *ReportHandler) handleABACyberReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ABA Cybersecurity Compliance Report", "vigo-aba-cyber",
		h.buildABACyberFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleABACyberReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ABA Cybersecurity Compliance Report", "vigo-aba-cyber",
		h.buildABACyberFindings(h.auditStatusFromRequest(r)), true)
}

func (h *ReportHandler) handleNYDFSCyberReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleNYDFSCyberReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(h.auditStatusFromRequest(r)), true)
}

// buildABACyberFindings returns controls for ABA Cybersecurity compliance,
// combining ABA Formal Opinion 477R, Model Rule 1.1 (technology competence),
// and Model Rule 1.6(c) (confidentiality — reasonable efforts).
func (h *ReportHandler) buildABACyberFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		// Model Rule 1.1 — Technology Competence
		{ControlID: "ABA-1.1", Title: "Technology Competence (Model Rule 1.1 Comment 8)", Status: "partial", Evidence: "ENFORCED (when modules assigned): infrastructure configuration managed by Vigo ensures technical competence of systems; attorney training on technology is organizational"},

		// Model Rule 1.6(c) — Reasonable Efforts
		{ControlID: "ABA-1.6-1", Title: "Reasonable Efforts — Encryption at Rest", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): disk encryption enforcement via LUKS/BitLocker modules; verified by convergence"},
		{ControlID: "ABA-1.6-2", Title: "Reasonable Efforts — Encryption in Transit", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): TLS enforcement, SSH hardening, plaintext protocol blocking via firewall modules"},
		{ControlID: "ABA-1.6-3", Title: "Reasonable Efforts — Access Controls", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): user account management, sudo restrictions, SSH key-only auth, shared account removal"},
		{ControlID: "ABA-1.6-4", Title: "Reasonable Efforts — Multi-Factor Authentication", Status: "partial", Evidence: "ENFORCED (when modules assigned): PAM MFA modules for system auth; application-level MFA (email, DMS) is external to Vigo"},
		{ControlID: "ABA-1.6-5", Title: "Reasonable Efforts — Secure Disposal", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): secure file deletion, disk wipe enforcement; Sandgorgon decommission with NIST 800-88 certificates"},

		// Formal Opinion 477R — Specific Requirements
		{ControlID: "ABA-477R-1", Title: "Nature of Threat Assessment", Status: "partial", Evidence: "Security scanning (CVE, rootkit, malware detection) provides continuous threat visibility; formal risk assessment methodology is organizational"},
		{ControlID: "ABA-477R-2", Title: "Client Matter Sensitivity Classification", Status: "partial", Evidence: "Compliance tagging and per-node risk scoring available; data classification policy and matter-level tagging is organizational"},
		{ControlID: "ABA-477R-3", Title: "Secure Communication Channels", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): TLS enforcement on all services, SSH hardening, firewall rules blocking unencrypted protocols"},
		{ControlID: "ABA-477R-4", Title: "Device Security — Endpoints", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): endpoint hardening, screen lock timeout, disk encryption, USB restrictions, CIS benchmark enforcement"},
		{ControlID: "ABA-477R-5", Title: "Device Security — Mobile", Status: "partial", Evidence: "ENFORCED (when agent deployed): mobile device hardening where Vigo agent is installed; BYOD policy and MDM for unmanaged devices is external"},
		{ControlID: "ABA-477R-6", Title: "Patch Management", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): package update enforcement, vulnerability scanning via Trivy/debsecan, CVE alerting"},
		{ControlID: "ABA-477R-7", Title: "Firewall and Network Security", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): firewall rule enforcement (ufw/iptables/firewalld), network segmentation, port allowlisting"},
		{ControlID: "ABA-477R-8", Title: "Anti-Malware Protection", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): ClamAV/Windows Defender enforcement, scheduled scanning, malware detection alerting"},
		{ControlID: "ABA-477R-9", Title: "Audit Logging", Status: "satisfied", Evidence: "Tamper-evident audit trail with SHA-256 hash chain; system audit logging (auditd/syslog) enforcement; " + auditEvidence},
		{ControlID: "ABA-477R-10", Title: "Backup and Recovery", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): backup agent enforcement, retention policy management; tested recovery via Connwaer backup_restore capability"},
		{ControlID: "ABA-477R-11", Title: "Incident Response Plan", Status: "partial", Evidence: "Automated incident detection (security alerts, drift detection, workflow triggers); written incident response plan is organizational"},
		{ControlID: "ABA-477R-12", Title: "Vendor and Third-Party Oversight", Status: "partial", Evidence: "Convergence monitoring of vendor-managed infrastructure; vendor contract review and due diligence process is organizational"},
	}
}

// buildNYDFSCyberFindings returns controls for NY DFS 23 NYCRR 500 cybersecurity regulation.
// Applies to law firms and other entities handling data for DFS-regulated financial institutions.
func (h *ReportHandler) buildNYDFSCyberFindings(auditStatus AuditChainStatus) []ControlFinding {
	auditEvidence := fmt.Sprintf("%d entries in tamper-evident hash chain", auditStatus.EntryCount)
	if !auditStatus.Valid {
		auditEvidence += " (CHAIN BROKEN)"
	}

	return []ControlFinding{
		{ControlID: "NYCRR-500.02", Title: "Cybersecurity Program", Status: "partial", Evidence: "Vigo enforces technical controls fleet-wide with continuous convergence; written cybersecurity program document is organizational"},
		{ControlID: "NYCRR-500.03", Title: "Cybersecurity Policy", Status: "partial", Evidence: "ENFORCED (when modules assigned): policy enforcement via configuration modules; written policy document approved by senior officer is organizational"},
		{ControlID: "NYCRR-500.05", Title: "Penetration Testing and Vulnerability Assessment", Status: "partial", Evidence: "Continuous vulnerability scanning (Trivy/debsecan/Windows Updates), CVE alerting, risk scoring; formal annual penetration test is external"},
		{ControlID: "NYCRR-500.06", Title: "Audit Trail", Status: "satisfied", Evidence: "Tamper-evident audit trail with SHA-256 hash chain; configurable retention (default 90 days, 6-year retention supported); " + auditEvidence},
		{ControlID: "NYCRR-500.07", Title: "Access Privileges and Management", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): least-privilege user management, sudo restrictions, SSH key-only auth, shared account removal, periodic access review via trait collection"},
		{ControlID: "NYCRR-500.08", Title: "Application Security", Status: "partial", Evidence: "ENFORCED (when modules assigned): package management, patch enforcement, vulnerability scanning; secure development lifecycle for custom applications is organizational"},
		{ControlID: "NYCRR-500.09", Title: "Risk Assessment", Status: "partial", Evidence: "Per-envoy risk scoring (0-100) from 10+ factors; fleet-wide risk posture dashboard; formal risk assessment methodology and documentation is organizational"},
		{ControlID: "NYCRR-500.10", Title: "Cybersecurity Personnel and Intelligence", Status: "partial", Evidence: "Automated enforcement reduces manual security burden; qualified CISO designation and threat intelligence program is organizational"},
		{ControlID: "NYCRR-500.11", Title: "Third-Party Service Provider Security", Status: "partial", Evidence: "Convergence monitoring of vendor-managed nodes; third-party risk assessment and contractual security requirements are organizational"},
		{ControlID: "NYCRR-500.12", Title: "Multi-Factor Authentication", Status: "partial", Evidence: "ENFORCED (when modules assigned): PAM MFA modules for system-level auth; application-level MFA for remote access and privileged accounts may require external controls"},
		{ControlID: "NYCRR-500.13", Title: "Data Retention and Disposal", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): data retention policies, secure file deletion, disk wipe enforcement; Sandgorgon NIST 800-88 decommission certificates"},
		{ControlID: "NYCRR-500.14", Title: "Monitoring and Detection", Status: "satisfied", Evidence: "Security scanning (CVE, rootkit, malware, integrity), convergence monitoring, drift detection, real-time alerting via webhooks/SMTP/integrations"},
		{ControlID: "NYCRR-500.15", Title: "Encryption of Nonpublic Information", Status: "satisfied", Evidence: "ENFORCED (when modules assigned): disk encryption (LUKS/BitLocker), TLS enforcement on all services, SSH hardening; Connwaer encryption_at_rest verification"},
		{ControlID: "NYCRR-500.16", Title: "Incident Response Plan", Status: "partial", Evidence: "Automated incident detection, security alerting, workflow-driven response triggers; written IRP with roles, communication plan, and recovery procedures is organizational"},
		{ControlID: "NYCRR-500.17", Title: "Annual Certification of Compliance", Status: "partial", Evidence: "Compliance reports, executive summary, and evidence export provide certification evidence; board/senior officer certification filing with DFS is organizational"},
	}
}

func (h *ReportHandler) handleIEC62443Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo IEC 62443 Industrial Cybersecurity Report", "vigo-iec62443",
		h.buildIEC62443Findings(h.auditStatusFromRequest(r)), false)
}

func (h *ReportHandler) handleIEC62443ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo IEC 62443 Industrial Cybersecurity Report", "vigo-iec62443",
		h.buildIEC62443Findings(h.auditStatusFromRequest(r)), true)
}

// ---------- Executive Summary ----------

// ExecutiveReport is the JSON format for the executive summary report.
type ExecutiveReport struct {
	GeneratedAt string             `json:"generated_at"`
	GeneratedBy string             `json:"generated_by"`
	Score       ExecutiveScore     `json:"score"`
	Trend       []TrendPoint       `json:"trend"`
	Frameworks  []FrameworkSummary `json:"frameworks"`
	Fleet       ExecutiveFleet     `json:"fleet"`
	TopIssues   []ExecutiveIssue   `json:"top_issues"`
	AuditChain  AuditChainStatus   `json:"audit_chain"`
}

// ExecutiveScore is the overall compliance score and rating.
type ExecutiveScore struct {
	Percent float64 `json:"percent"`
	Rating  string  `json:"rating"` // "Excellent", "Good", "Needs Attention", "Critical"
}

// TrendPoint is a single day's compliance percentage in the 30-day trend.
type TrendPoint struct {
	Date    string  `json:"date"`
	Percent float64 `json:"percent"`
}

// FrameworkSummary is a single framework's control status counts.
type FrameworkSummary struct {
	Name         string  `json:"name"`
	Satisfied    int     `json:"satisfied"`
	Waived       int     `json:"waived"`
	Partial      int     `json:"partial"`
	NotSatisfied int     `json:"not_satisfied"`
	ScorePercent float64 `json:"score_percent"`
}

// ExecutiveFleet is the fleet summary with friendly labels.
type ExecutiveFleet struct {
	Total          int `json:"total"`
	FullyConverged int `json:"fully_compliant"`
	ConfigChanges  int `json:"config_changes"`
	Errors         int `json:"errors"`
	Unreachable    int `json:"unreachable"`
	PendingData    int `json:"pending_data"`
}

// ExecutiveIssue is a plain-English issue with severity.
type ExecutiveIssue struct {
	Severity    string `json:"severity"` // "critical", "warning", "info"
	Description string `json:"description"`
}

func complianceRating(pct float64) string {
	switch {
	case pct >= 95:
		return "Excellent"
	case pct >= 80:
		return "Good"
	case pct >= 50:
		return "Needs Attention"
	default:
		return "Critical"
	}
}

// buildFrameworkCoverage computes per-standard compliance scores using the
// authoritative findings functions (which correctly distinguish satisfied,
// partial, and not_satisfied). Active waivers boost not_satisfied/partial
// controls to satisfied.
func (h *ReportHandler) buildFrameworkCoverage(r *http.Request) []FrameworkSummary {
	if h.configStore == nil {
		return nil
	}

	as := h.auditStatusFromRequest(r)

	type fwEntry struct {
		name     string
		slug     string
		controls func() []ControlFinding
	}
	fwDefs := []fwEntry{
		{"HIPAA", "compliance", func() []ControlFinding { return h.buildControlFindings(h.fleetIndex.ConvergenceSummary(), as) }},
		{"HITRUST CSF", "hitrust", func() []ControlFinding { return h.buildHITRUSTFindings(as) }},
		{"SOC 2", "soc2", func() []ControlFinding { return h.buildSOC2Findings(as) }},
		{"PCI DSS v4.0", "pci", func() []ControlFinding { return h.buildPCIDSSFindings(as) }},
		{"NIST 800-53", "nist", func() []ControlFinding { return h.buildNIST80053Findings(as) }},
		{"ISO 27001", "iso27001", func() []ControlFinding { return h.buildISO27001Findings(as) }},
		{"GDPR", "gdpr", func() []ControlFinding { return h.buildGDPRFindings(as) }},
		{"NERC CIP", "nerccip", func() []ControlFinding { return h.buildNERCCIPFindings(as) }},
		{"Cyber Essentials+", "cyberessentials", func() []ControlFinding { return h.buildCyberEssentialsPlusFindings(as) }},
		{"IEC 62443", "iec62443", func() []ControlFinding { return h.buildIEC62443Findings(as) }},
		{"SOX", "sox", func() []ControlFinding { return h.buildSOXFindings(as) }},
		{"FINRA", "finra", func() []ControlFinding { return h.buildFINRAFindings(as) }},
		{"MiFID II", "mifid2", func() []ControlFinding { return h.buildMiFID2Findings(as) }},
		{"CCSS", "ccss", func() []ControlFinding { return h.buildCCSSFindings(as) }},
		{"FDA 21 CFR Part 11", "fda21cfr11", func() []ControlFinding { return h.buildFDA21CFR11Findings(as) }},
		{"Gaming Commission", "gaming", func() []ControlFinding { return h.buildGamingFindings(as) }},
		{"CIS Windows", "ciswindows", func() []ControlFinding { return h.buildCISWindowsFindings(as) }},
		{"CIS Ubuntu", "cisubuntu", func() []ControlFinding { return h.buildCISUbuntuFindings(as) }},
		{"CIS RHEL", "cisrhel", func() []ControlFinding { return h.buildCISRHELFindings(as) }},
		{"ABA Cybersecurity", "abacyber", func() []ControlFinding { return h.buildABACyberFindings(as) }},
		{"NY DFS Cyber (23 NYCRR 500)", "nydfscyber", func() []ControlFinding { return h.buildNYDFSCyberFindings(as) }},
	}

	// Filter to enabled frameworks only.
	if h.configStore != nil {
		var filtered []fwEntry
		for _, fw := range fwDefs {
			standardKey := config.StandardKeyForSlug[fw.slug]
			if h.configStore.IsStandardEnabled(standardKey) {
				filtered = append(filtered, fw)
			}
		}
		fwDefs = filtered
	}

	// Load all active waivers: merge config-file waivers + DB waivers.
	waiversByKey := make(map[string]map[string]bool) // standard_key → control_id → true
	if h.configStore != nil {
		for standard, controls := range h.configStore.AllWaivers() {
			if _, ok := waiversByKey[standard]; !ok {
				waiversByKey[standard] = make(map[string]bool)
			}
			for controlID := range controls {
				waiversByKey[standard][controlID] = true
			}
		}
	}
	// Also merge DB-managed waivers (keyed by slug, need to convert).
	if waivers, err := h.queries.ListComplianceWaivers(r.Context()); err == nil {
		for _, w := range waivers {
			stdKey := w.Framework
			if mapped, ok := config.StandardKeyForSlug[w.Framework]; ok {
				stdKey = mapped
			}
			if _, ok := waiversByKey[stdKey]; !ok {
				waiversByKey[stdKey] = make(map[string]bool)
			}
			waiversByKey[stdKey][w.ControlID] = true
		}
	}

	envoys := h.fleetIndex.AllEnvoys()
	var activeHostnames []string
	for _, e := range envoys {
		if !e.Revoked {
			activeHostnames = append(activeHostnames, e.Hostname)
		}
	}

	frameworks := make([]FrameworkSummary, 0, len(fwDefs))
	for _, fw := range fwDefs {
		findings := fw.controls()
		frameworkTotalControls := len(findings)

		// Build a set of valid control IDs from the findings builder.
		// Only controls in this set count toward coverage — module tags
		// referencing unknown control IDs are ignored.
		validControls := make(map[string]bool, frameworkTotalControls)
		for _, f := range findings {
			validControls[f.ControlID] = true
		}

		// Compute fleet-average coverage using module-to-control assignment
		// with the findings count as the denominator.
		standardKey, hasKey := config.StandardKeyForSlug[fw.slug]
		waivedSet := waiversByKey[standardKey]
		var fleetCoverageSum float64
		fleetCoverageCount := 0
		fleetSatisfiedSum := 0
		fleetWaivedSum := 0
		if hasKey && h.configStore != nil && frameworkTotalControls > 0 {
			for _, hostname := range activeHostnames {
				cov := h.configStore.EnvoyStandardCoverage(hostname, standardKey)
				if cov == nil {
					continue
				}
				// Only count controls that exist in the findings list.
				covered := 0
				for _, cc := range cov.CoveredControls {
					if validControls[cc.ControlID] {
						covered++
					}
				}
				// Waivers boost missing controls to covered.
				waived := 0
				if len(waivedSet) > 0 {
					for _, mc := range cov.MissingControls {
						if waivedSet[mc] && validControls[mc] {
							waived++
						}
					}
				}
				total := covered + waived
				pct := float64(total) / float64(frameworkTotalControls) * 100
				fleetCoverageSum += pct
				fleetSatisfiedSum += covered
				fleetWaivedSum += waived
				fleetCoverageCount++
			}
		}

		scorePct := float64(0)
		avgSatisfied := 0
		avgNotSatisfied := frameworkTotalControls
		if fleetCoverageCount > 0 {
			scorePct = math.Round(fleetCoverageSum/float64(fleetCoverageCount)*10) / 10
			avgSatisfied = int(math.Round(float64(fleetSatisfiedSum+fleetWaivedSum) / float64(fleetCoverageCount)))
			avgNotSatisfied = frameworkTotalControls - avgSatisfied
		}

		// Count total active waivers for this framework (regardless of
		// whether the control is covered or missing). This shows operators
		// that waivers exist even when all controls are satisfied.
		totalWaivers := len(waivedSet)

		frameworks = append(frameworks, FrameworkSummary{
			Name:         fw.name,
			Satisfied:    avgSatisfied,
			Waived:       totalWaivers,
			Partial:      0,
			NotSatisfied: avgNotSatisfied,
			ScorePercent: scorePct,
		})
	}
	sort.Slice(frameworks, func(i, j int) bool {
		return frameworks[i].Name < frameworks[j].Name
	})
	return frameworks
}

func frameworkScore(controls []ControlFinding) FrameworkSummary {
	var s FrameworkSummary
	for _, c := range controls {
		switch c.Status {
		case "satisfied":
			s.Satisfied++
		case "waived":
			s.Waived++
		case "partial":
			s.Partial++
		default:
			s.NotSatisfied++
		}
	}
	total := s.Satisfied + s.Waived + s.Partial + s.NotSatisfied
	if total > 0 {
		s.ScorePercent = math.Round(float64(s.Satisfied+s.Waived)/float64(total)*1000) / 10
	}
	return s
}

// BuildExecutiveReport returns the executive summary data for all frameworks.
// Exported for use by the UI handler's HTMX fragment endpoints.
func (h *ReportHandler) BuildExecutiveReport(r *http.Request) (*ExecutiveReport, error) {
	now := time.Now().UTC()
	summary := h.fleetIndex.ConvergenceSummary()

	// Audit chain.
	auditStatus := h.auditStatusFromRequest(r)

	// Framework scores using findings-based scoring (with waivers applied).
	frameworks := h.buildFrameworkCoverage(r)

	// Compute overall compliance score as the average across all frameworks
	// that have data. This is the true compliance score (not convergence).
	compliancePct := float64(0)
	fwCount := 0
	for _, fw := range frameworks {
		total := fw.Satisfied + fw.Partial + fw.NotSatisfied
		if total > 0 {
			compliancePct += fw.ScorePercent
			fwCount++
		}
	}
	if fwCount > 0 {
		compliancePct = math.Round(compliancePct/float64(fwCount)*10) / 10
	}

	// 30-day trend from compliance_history table. Average across frameworks per day.
	compHistory, _ := h.queries.ListComplianceHistoryAllFrameworks(r.Context(), 30*20)
	dayTotals := make(map[string]struct{ sum float64; count int })
	for _, rec := range compHistory {
		e := dayTotals[rec.SnapshotDate]
		e.sum += rec.ScorePercent
		e.count++
		dayTotals[rec.SnapshotDate] = e
	}
	// Collect dates in order (history comes DESC).
	var dates []string
	seen := make(map[string]bool)
	for i := len(compHistory) - 1; i >= 0; i-- {
		d := compHistory[i].SnapshotDate
		if !seen[d] {
			dates = append(dates, d)
			seen[d] = true
		}
	}
	trend := make([]TrendPoint, 0, len(dates))
	for _, d := range dates {
		e := dayTotals[d]
		avg := math.Round(e.sum/float64(e.count)*10) / 10
		trend = append(trend, TrendPoint{Date: d, Percent: avg})
	}

	// Top issues.
	topIssues := h.buildTopIssues(summary, auditStatus, frameworks)

	return &ExecutiveReport{
		GeneratedAt: now.Format(time.RFC3339),
		GeneratedBy: "Vigo " + h.version,
		Score: ExecutiveScore{
			Percent: compliancePct,
			Rating:  complianceRating(compliancePct),
		},
		Trend:      trend,
		Frameworks: frameworks,
		Fleet: ExecutiveFleet{
			Total:          summary.Total,
			FullyConverged: summary.Converged,
			ConfigChanges:  summary.Relapsed + summary.Diverged,
			Errors:         summary.Errors,
			Unreachable:    summary.LostContact,
			PendingData:    summary.NoData,
		},
		TopIssues:  topIssues,
		AuditChain: auditStatus,
	}, nil
}

func (h *ReportHandler) buildTopIssues(summary fleet.ConvergenceCounters, auditStatus AuditChainStatus, frameworks []FrameworkSummary) []ExecutiveIssue {
	var issues []ExecutiveIssue

	// Priority 1: Audit chain broken (critical).
	if !auditStatus.Valid {
		issues = append(issues, ExecutiveIssue{
			Severity:    "critical",
			Description: "The audit trail integrity check has failed. The tamper-evident hash chain is broken, which may indicate unauthorized modification of audit records.",
		})
	}

	// Priority 2: Nodes with errors (critical).
	if summary.Errors > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "critical",
			Description: fmt.Sprintf("%d node(s) are reporting errors during configuration enforcement. These systems may not be in their desired state.", summary.Errors),
		})
	}

	// Priority 3: Unreachable nodes (warning).
	if summary.LostContact > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "warning",
			Description: fmt.Sprintf("%d node(s) have not checked in recently and may be unreachable. Their compliance status cannot be verified.", summary.LostContact),
		})
	}

	// Priority 4: Relapsed/diverged drift (warning).
	if summary.Relapsed+summary.Diverged > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "warning",
			Description: fmt.Sprintf("%d node(s) are experiencing persistent configuration drift (relapsed or diverged). Changes are being reverted repeatedly, indicating an external process may be conflicting.", summary.Relapsed+summary.Diverged),
		})
	}

	// Priority 5: Per-framework not_satisfied controls (warning).
	for _, fw := range frameworks {
		if fw.NotSatisfied > 0 {
			issues = append(issues, ExecutiveIssue{
				Severity:    "warning",
				Description: fmt.Sprintf("%s: %d control(s) are not satisfied and require attention.", fw.Name, fw.NotSatisfied),
			})
		}
	}

	// Priority 6: Pending data (info).
	if summary.NoData > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "info",
			Description: fmt.Sprintf("%d node(s) have enrolled but not yet reported any data. These are new systems awaiting their first check-in.", summary.NoData),
		})
	}

	// Priority 7: Per-framework partial controls (info).
	for _, fw := range frameworks {
		if fw.Partial > 0 {
			issues = append(issues, ExecutiveIssue{
				Severity:    "info",
				Description: fmt.Sprintf("%s: %d control(s) are partially satisfied and may require organizational action.", fw.Name, fw.Partial),
			})
		}
	}

	// Sort by severity (critical > warning > info), stable to preserve priority order within severity.
	severityRank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(issues, func(i, j int) bool {
		return severityRank[issues[i].Severity] < severityRank[issues[j].Severity]
	})

	// Cap at 10.
	if len(issues) > 10 {
		issues = issues[:10]
	}
	return issues
}

func (h *ReportHandler) handleExecutiveReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.BuildExecutiveReport(r)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build executive report: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Get("download") == "1" {
		filename := fmt.Sprintf("vigo-executive-%s.json", time.Now().Format("2006-01-02"))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

func (h *ReportHandler) handleExecutiveReportHTML(w http.ResponseWriter, r *http.Request) {
	report, err := h.BuildExecutiveReport(r)
	if err != nil {
		http.Error(w, "failed to build executive report: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		filename := fmt.Sprintf("vigo-executive-%s.html", time.Now().Format("2006-01-02"))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	}
	renderExecutiveHTML(w, report)
}

func renderExecutiveHTML(w http.ResponseWriter, r *ExecutiveReport) {
	// Score colors.
	scoreColor := "#27ae60"
	switch {
	case r.Score.Percent < 50:
		scoreColor = "#e74c3c"
	case r.Score.Percent < 80:
		scoreColor = "#f39c12"
	case r.Score.Percent < 95:
		scoreColor = "#2ecc71"
	}

	// Donut chart: SVG arc for the score percentage.
	// Circle: cx=60, cy=60, r=50. Circumference = 2*pi*50 ≈ 314.16
	circumference := 2 * math.Pi * 50
	filled := circumference * r.Score.Percent / 100

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Vigo Executive Compliance Summary</title>
<style>
  * { box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; padding: 2rem; background: #fff; color: #1a1a2e; line-height: 1.6; }
  .header { display: flex; justify-content: space-between; align-items: center; border-bottom: 2px solid #1a1a2e; padding-bottom: 1rem; margin-bottom: 2rem; }
  .header h1 { margin: 0; font-size: 1.5rem; }
  .header .meta { text-align: right; font-size: 0.85rem; color: #6c757d; }
  .score-section { display: flex; align-items: center; gap: 2rem; margin-bottom: 2rem; }
  .score-gauge svg { display: block; }
  .score-gauge text { font-family: inherit; }
  .score-info .rating { font-size: 1.5rem; font-weight: 700; margin: 0; }
  .score-info .sublabel { font-size: 0.85rem; color: #6c757d; }
  .fleet-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
  .fleet-card { background: #f8f9fa; border-radius: 8px; padding: 1rem; text-align: center; }
  .fleet-card .value { font-size: 2rem; font-weight: bold; }
  .fleet-card .label { font-size: 0.75rem; color: #6c757d; text-transform: uppercase; letter-spacing: 0.5px; }
  h2 { font-size: 1.1rem; margin: 1.5rem 0 0.75rem; border-bottom: 1px solid #dee2e6; padding-bottom: 0.3rem; }
  .trend-chart { margin-bottom: 2rem; }
  .trend-chart svg { display: block; width: 100%%; max-width: 600px; }
  table { width: 100%%; border-collapse: collapse; margin-bottom: 2rem; }
  th, td { padding: 0.5rem 0.75rem; text-align: left; border-bottom: 1px solid #dee2e6; font-size: 0.9rem; }
  th { background: #f8f9fa; font-weight: 600; font-size: 0.8rem; text-transform: uppercase; color: #6c757d; }
  .bar-container { display: flex; height: 18px; border-radius: 4px; overflow: hidden; min-width: 120px; }
  .bar-green { background: #27ae60; }
  .bar-yellow { background: #f39c12; }
  .bar-red { background: #e74c3c; }
  .issue { padding: 0.5rem 0.75rem; border-left: 4px solid; margin-bottom: 0.5rem; font-size: 0.9rem; }
  .issue-critical { border-color: #e74c3c; background: #fdf0f0; }
  .issue-warning { border-color: #f39c12; background: #fef9f0; }
  .issue-info { border-color: #3498db; background: #f0f6fe; }
  .issue .sev { font-weight: 600; text-transform: uppercase; font-size: 0.7rem; margin-right: 0.5rem; }
  .issue .sev-critical { color: #e74c3c; }
  .issue .sev-warning { color: #f39c12; }
  .issue .sev-info { color: #3498db; }
  .audit-banner { padding: 0.75rem 1rem; border-radius: 8px; margin-bottom: 2rem; font-size: 0.9rem; }
  .audit-pass { background: #d4edda; color: #155724; }
  .audit-fail { background: #f8d7da; color: #721c24; }
  .footer { margin-top: 2rem; padding-top: 1rem; border-top: 1px solid #dee2e6; font-size: 0.8rem; color: #6c757d; text-align: center; }
  @media print {
    body { padding: 0.5rem; font-size: 11px; }
    .header { page-break-after: avoid; }
    .score-section { page-break-after: avoid; }
    .fleet-cards { page-break-after: avoid; }
    table { page-break-inside: auto; }
    tr { page-break-inside: avoid; }
    .issue { page-break-inside: avoid; }
    h2 { page-break-after: avoid; }
  }
</style>
</head>
<body>
<div class="header">
  <h1>Executive Compliance Summary</h1>
  <div class="meta">
    Generated: %s<br>
    %s
  </div>
</div>
`, r.GeneratedAt, html.EscapeString(r.GeneratedBy))

	// 1. Score gauge — SVG donut.
	fmt.Fprintf(w, `<div class="score-section">
  <div class="score-gauge">
    <svg width="120" height="120" viewBox="0 0 120 120">
      <circle cx="60" cy="60" r="50" fill="none" stroke="#e9ecef" stroke-width="10"/>
      <circle cx="60" cy="60" r="50" fill="none" stroke="%s" stroke-width="10"
        stroke-dasharray="%.1f %.1f" stroke-dashoffset="%.1f"
        stroke-linecap="round" transform="rotate(-90 60 60)"/>
      <text x="60" y="55" text-anchor="middle" font-size="22" font-weight="bold" fill="%s">%.0f%%%%</text>
      <text x="60" y="72" text-anchor="middle" font-size="11" fill="#6c757d">compliance</text>
    </svg>
  </div>
  <div class="score-info">
    <p class="rating" style="color:%s">%s</p>
    <p class="sublabel">Overall fleet compliance rating</p>
  </div>
</div>
`, scoreColor, filled, circumference-filled, -circumference/4, scoreColor, r.Score.Percent,
		scoreColor, html.EscapeString(r.Score.Rating))

	// 2. Fleet summary cards.
	fmt.Fprintf(w, `<div class="fleet-cards">
  <div class="fleet-card"><div class="value">%d</div><div class="label">Total Nodes</div></div>
  <div class="fleet-card"><div class="value" style="color:#27ae60">%d</div><div class="label">Fully Converged</div></div>
  <div class="fleet-card"><div class="value" style="color:#f39c12">%d</div><div class="label">Configuration Changes Detected</div></div>
  <div class="fleet-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Errors</div></div>
  <div class="fleet-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Unreachable</div></div>
  <div class="fleet-card"><div class="value" style="color:#95a5a6">%d</div><div class="label">Pending Data</div></div>
</div>
`, r.Fleet.Total, r.Fleet.FullyConverged, r.Fleet.ConfigChanges, r.Fleet.Errors, r.Fleet.Unreachable, r.Fleet.PendingData)

	// 3. 30-day trend — inline SVG area chart.
	if len(r.Trend) > 1 {
		fmt.Fprint(w, `<h2>30-Day Compliance Trend</h2>
<div class="trend-chart">
<svg viewBox="0 0 600 200" xmlns="http://www.w3.org/2000/svg">
  <rect width="600" height="200" fill="#fafafa" rx="4"/>
`)
		// Grid lines at 25%, 50%, 75%, 100%.
		for _, pctLine := range []float64{25, 50, 75, 100} {
			y := 190 - (pctLine/100)*180
			fmt.Fprintf(w, `  <line x1="40" y1="%.0f" x2="590" y2="%.0f" stroke="#e9ecef" stroke-width="1"/>
  <text x="35" y="%.0f" text-anchor="end" font-size="9" fill="#adb5bd">%.0f%%%%</text>
`, y, y, y+3, pctLine)
		}

		n := len(r.Trend)
		xStep := 550.0 / float64(n-1)
		var linePoints, areaPoints strings.Builder
		for i, pt := range r.Trend {
			x := 40 + float64(i)*xStep
			y := 190 - (pt.Percent/100)*180
			fmt.Fprintf(&linePoints, "%.1f,%.1f ", x, y)
			fmt.Fprintf(&areaPoints, "%.1f,%.1f ", x, y)
		}
		lastX := 40 + float64(n-1)*xStep
		fmt.Fprintf(&areaPoints, "%.1f,190 40,190", lastX)

		fmt.Fprintf(w, `  <polygon points="%s" fill="%s" fill-opacity="0.15"/>
  <polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>
`, areaPoints.String(), scoreColor, linePoints.String(), scoreColor)

		fmt.Fprint(w, `</svg>
</div>
`)
	}

	// 4. Framework coverage table with stacked bars.
	fmt.Fprint(w, `<h2>Framework Coverage</h2>
<table>
<thead><tr><th>Framework</th><th>Score</th><th style="width:40%%">Controls</th><th>Satisfied</th><th>Partial</th><th>Not Met</th></tr></thead>
<tbody>
`)
	for _, fw := range r.Frameworks {
		total := fw.Satisfied + fw.Partial + fw.NotSatisfied
		gPct, yPct, rPct := float64(0), float64(0), float64(0)
		if total > 0 {
			gPct = float64(fw.Satisfied) / float64(total) * 100
			yPct = float64(fw.Partial) / float64(total) * 100
			rPct = float64(fw.NotSatisfied) / float64(total) * 100
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%.1f%%%%</td><td><div class="bar-container">`,
			html.EscapeString(fw.Name), fw.ScorePercent)
		if gPct > 0 {
			fmt.Fprintf(w, `<div class="bar-green" style="width:%.1f%%"></div>`, gPct)
		}
		if yPct > 0 {
			fmt.Fprintf(w, `<div class="bar-yellow" style="width:%.1f%%"></div>`, yPct)
		}
		if rPct > 0 {
			fmt.Fprintf(w, `<div class="bar-red" style="width:%.1f%%"></div>`, rPct)
		}
		fmt.Fprintf(w, `</div></td><td>%d</td><td>%d</td><td>%d</td></tr>
`, fw.Satisfied, fw.Partial, fw.NotSatisfied)
	}
	fmt.Fprint(w, `</tbody></table>`)

	// 5. Top issues.
	if len(r.TopIssues) > 0 {
		fmt.Fprint(w, `<h2>Top Issues</h2>
`)
		for _, issue := range r.TopIssues {
			fmt.Fprintf(w, `<div class="issue issue-%s"><span class="sev sev-%s">%s</span>%s</div>
`, issue.Severity, issue.Severity, html.EscapeString(strings.ToUpper(issue.Severity)), html.EscapeString(issue.Description))
		}
	}

	// 6. Audit chain banner.
	auditClass := "audit-pass"
	auditText := fmt.Sprintf("Audit Trail Verified — %d entries, integrity intact.", r.AuditChain.EntryCount)
	if !r.AuditChain.Valid {
		auditClass = "audit-fail"
		auditText = "AUDIT TRAIL INTEGRITY CHECK FAILED"
		if r.AuditChain.Error != "" {
			auditText += " — " + r.AuditChain.Error
		}
	}
	fmt.Fprintf(w, `<div class="audit-banner %s"><strong>Audit Chain:</strong> %s</div>
`, auditClass, html.EscapeString(auditText))

	// Footer.
	fmt.Fprintf(w, `<div class="footer">Vigo Executive Compliance Summary &mdash; Generated %s &mdash; %s</div>
</body></html>`, r.GeneratedAt, html.EscapeString(r.GeneratedBy))
}
