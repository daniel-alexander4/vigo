// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/ui"
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

// frameworkDef describes one framework's metadata and control builder.
// Returned by (*ReportHandler).frameworkDef for both BuildFrameworkReport
// and ComputeEnvoyFrameworkStatus, so the per-slug registry lives in one
// place.
type frameworkDef struct {
	title    string
	name     string // short framework name (e.g., "HIPAA")
	controls func() []ControlFinding
}

// frameworkDef returns the framework metadata + control-catalog builder
// for a slug. The auditStatus parameter is no longer threaded into the
// builders — framework reports score envoy state, not the Vigo server's
// own audit chain. The server's audit-chain integrity is surfaced
// separately on /platform/compliance via server/compliance.AppControls.
func (h *ReportHandler) frameworkDef(_ AuditChainStatus, slug string) (frameworkDef, bool) {
	fws := map[string]frameworkDef{
		"compliance":      {"HIPAA Compliance Report", "HIPAA", h.buildHIPAAFindings},
		"hitrust":         {"HITRUST CSF Compliance Report", "HITRUST CSF", h.buildHITRUSTFindings},
		"soc2":            {"SOC 2 Trust Services Criteria Report", "SOC 2", h.buildSOC2Findings},
		"pci":             {"PCI DSS v4.0 Compliance Report", "PCI DSS v4.0", h.buildPCIDSSFindings},
		"nist":            {"NIST 800-53 Compliance Report", "NIST 800-53", h.buildNIST80053Findings},
		"iso27001":        {"ISO 27001 Compliance Report", "ISO 27001", h.buildISO27001Findings},
		"gdpr":            {"GDPR Compliance Report", "GDPR", h.buildGDPRFindings},
		"nerccip":         {"NERC CIP Compliance Report", "NERC CIP", h.buildNERCCIPFindings},
		"cyberessentials": {"Cyber Essentials+ Compliance Report", "Cyber Essentials+", h.buildCyberEssentialsPlusFindings},
		"iec62443":        {"IEC 62443 Industrial Cybersecurity Report", "IEC 62443", h.buildIEC62443Findings},
		"sox":             {"SOX IT General Controls Report", "SOX", h.buildSOXFindings},
		"finra":           {"FINRA Compliance Report", "FINRA", h.buildFINRAFindings},
		"mifid2":          {"MiFID II RTS 7 Compliance Report", "MiFID II", h.buildMiFID2Findings},
		"ccss":            {"CCSS Cryptocurrency Security Report", "CCSS", h.buildCCSSFindings},
		"fda21cfr11":      {"FDA 21 CFR Part 11 Compliance Report", "FDA 21 CFR Part 11", h.buildFDA21CFR11Findings},
		"gaming":          {"Gaming Commission Compliance Report", "Gaming Commission", h.buildGamingFindings},
		"ciswindows":      {"CIS Windows Server Benchmark Report", "CIS Windows", h.buildCISWindowsFindings},
		"cisubuntu":       {"CIS Ubuntu Linux Benchmark Report", "CIS Ubuntu", h.buildCISUbuntuFindings},
		"cisrhel":         {"CIS RHEL Benchmark Report", "CIS RHEL", h.buildCISRHELFindings},
		"abacyber":        {"ABA Cybersecurity Compliance Report", "ABA Cybersecurity", h.buildABACyberFindings},
		"nydfscyber":      {"NY DFS Cybersecurity (23 NYCRR 500) Report", "NY DFS Cyber", h.buildNYDFSCyberFindings},
	}
	fw, ok := fws[slug]
	return fw, ok
}

// buildFrameworkFindings constructs the finding set for a framework and
// applies fleet-wide waivers in place. Waivers come from two sources —
// envoys-tree compliance.vgo (via ConfigStore) and API-created waivers in
// the DB. A waiver promotes a not-satisfied finding to "waived" but never
// overrides a "satisfied" status (already-passing controls keep passing;
// the waiver badge still surfaces in the evidence string).
func (h *ReportHandler) buildFrameworkFindings(ctx context.Context, slug string, fw frameworkDef) []ControlFinding {
	findings := fw.controls()
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
	if waivers, err := h.queries.ListComplianceWaiversByFramework(ctx, slug); err == nil {
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
	return findings
}

// partitionEnvoyFindings buckets each finding for one envoy using the
// single source-of-truth partition rule. Framework reports score envoy
// state only — Vigo's own product-level posture (mTLS, RBAC, audit
// chain, etc.) is surfaced separately via server/compliance.AppControls
// and never inflates these per-envoy buckets.
//
// A finding lands in exactly one bucket, evaluated in priority order:
//
//  1. Module-tagged coverage — the envoy's modules map to this control
//     via .vgo `compliance:` tags.
//  2. Connwaer-trait escalation — Status == "satisfied" set by a
//     fleet-trait check that runs against the envoy itself (e.g.,
//     connwaer.encryption_at_rest.status == "pass" fleet-wide). No
//     framework builder hardcodes this; only trait-driven escalation
//     reaches the satisfied branch.
//  3. Waiver — Status == "waived" after applyWaivers ran. Counted
//     separately from covered per auditor practice (PCI compensating
//     controls, SOC 2 SoA exceptions, ISO 27001 SoA "N/A with
//     justification"); never rolled into covered.
//  4. Partial — Status == "partial" (framework-specific half-credit
//     or partial Connwaer fleet coverage).
//  5. Else missing.
//
// Covered + Waived + Partial + Missing = len(findings).
func partitionEnvoyFindings(findings []ControlFinding, coverage *config.StandardCoverage) (covered, waived, partial, missing int) {
	coveredByModules := make(map[string]bool)
	if coverage != nil {
		for _, cc := range coverage.CoveredControls {
			coveredByModules[cc.ControlID] = true
		}
	}
	for _, f := range findings {
		switch {
		case coveredByModules[f.ControlID]:
			covered++
		case f.Status == "satisfied":
			// Reached only via Connwaer trait-driven escalation today.
			// Framework-builder findings now ship as "not_satisfied" and
			// flip via module coverage above or waivers below.
			covered++
		case f.Status == "waived":
			waived++
		case f.Status == "partial":
			partial++
		default:
			missing++
		}
	}
	return
}

// ComputeEnvoyFrameworkStatus returns per-envoy compliance numbers for one
// framework. Thin wrapper over ComputeFrameworkScore — kept because the
// envoy-detail Compliance tab asks about a single hostname and this is
// the cheapest way to answer.
func (h *ReportHandler) ComputeEnvoyFrameworkStatus(r *http.Request, hostname, slug string) (ui.EnvoyFrameworkStatus, bool) {
	score, ok := h.ComputeFrameworkScore(r, slug)
	if !ok {
		return ui.EnvoyFrameworkStatus{}, false
	}
	if st, present := score.PerEnvoy[hostname]; present {
		return st, true
	}
	// Envoy not in the active fleet index — partition anyway using
	// whatever coverage the config store has. Keeps the drilldown and
	// envoy-detail usable for envoys that are enrolled but have no
	// recent activity.
	var coverage *config.StandardCoverage
	if standardKey, keyOK := config.StandardKeyForSlug[slug]; keyOK {
		coverage = h.configStore.EnvoyStandardCoverage(hostname, standardKey)
	}
	covered, waived, partial, missing := partitionEnvoyFindings(score.Controls, coverage)
	pct := 0.0
	if score.Total > 0 {
		pct = float64(covered) / float64(score.Total) * 100
	}
	return ui.EnvoyFrameworkStatus{
		Covered:         covered,
		Waived:          waived,
		Partial:         partial,
		Missing:         missing,
		Total:           score.Total,
		CoveragePercent: pct,
	}, true
}

// ComputeEnvoyFrameworkDetail returns the per-envoy partition detail for
// one framework: totals plus a per-control status breakdown. Used by the
// /envoys/<id>/compliance/<slug> drilldown so the page agrees with the
// Standards tab card that linked to it.
func (h *ReportHandler) ComputeEnvoyFrameworkDetail(r *http.Request, hostname, slug string) (*ui.EnvoyFrameworkDetail, bool) {
	score, ok := h.ComputeFrameworkScore(r, slug)
	if !ok {
		return nil, false
	}

	standardKey, _ := config.StandardKeyForSlug[slug]
	var coverage *config.StandardCoverage
	if standardKey != "" {
		coverage = h.configStore.EnvoyStandardCoverage(hostname, standardKey)
	}
	coveredByModules := make(map[string][]string)
	if coverage != nil {
		for _, cc := range coverage.CoveredControls {
			coveredByModules[cc.ControlID] = cc.Modules
		}
	}

	rows := make([]ui.ControlStatusRow, 0, len(score.Controls))
	for _, f := range score.Controls {
		row := ui.ControlStatusRow{
			ControlID: f.ControlID,
			Title:     f.Title,
			Evidence:  f.Evidence,
		}
		switch {
		case len(coveredByModules[f.ControlID]) > 0:
			row.Status = "covered"
			row.Modules = coveredByModules[f.ControlID]
		case f.Status == "satisfied":
			// Reached only via Connwaer trait-driven escalation. Framework
			// builders ship every catalog entry as not_satisfied; only a
			// trait check on envoy state can flip it to satisfied here.
			row.Status = "covered"
		case f.Status == "waived":
			row.Status = "waived"
		case f.Status == "partial":
			row.Status = "partial"
		default:
			row.Status = "missing"
			if standardKey != "" {
				row.RecommendedModules = h.configStore.RecommendModulesForControl(standardKey, f.ControlID)
			}
		}
		rows = append(rows, row)
	}

	status, _ := h.ComputeEnvoyFrameworkStatus(r, hostname, slug)
	return &ui.EnvoyFrameworkDetail{
		Status:   status,
		Controls: rows,
	}, true
}

// FrameworkScore is the authoritative compliance score for one framework,
// computed by the single-source-of-truth scorer. Every compliance UI
// surface derives its displayed numbers from this struct — per-envoy fleet
// rows from PerEnvoy, framework-page summary cards and the compliance
// overview cards from Fleet, daily snapshots from Fleet. Direct access to
// PerEnvoy also powers the envoy-detail Standards tab and its drilldown
// page.
type FrameworkScore struct {
	Slug  string
	Name  string
	Title string
	Total int // framework control count (N)

	// Fleet is the macro-averaged aggregate across all active envoys.
	// Covered + Waived + Partial + Missing == Total (forced balance).
	Fleet FrameworkFleetAggregate

	// PerEnvoy is the partition result for every active envoy, keyed by
	// hostname. Covered + Waived + Partial + Missing == Total for each
	// entry.
	PerEnvoy map[string]ui.EnvoyFrameworkStatus

	// Controls is the findings list with waivers applied.
	Controls []ControlFinding
}

// FrameworkFleetAggregate summarizes a framework across all active envoys
// using macro-averaging (mean of per-envoy values). Covered + Waived +
// Partial + Missing == Total by construction.
type FrameworkFleetAggregate struct {
	ActiveEnvoys    int
	Total           int     // framework control count (N)
	Covered         int     // round(mean(per-envoy covered))
	Waived          int     // round(mean(per-envoy waived))
	Partial         int     // round(mean(per-envoy partial))
	Missing         int     // Total - Covered - Waived - Partial (balanced)
	CoveragePercent float64 // mean of per-envoy CoveragePercent, 1-decimal rounded
}

// ComputeFrameworkScore is the single source of truth for framework
// compliance scoring. Every compliance UI surface and the daily snapshot
// job must derive their numbers from this function. Returns ok=false
// when the slug is unknown.
func (h *ReportHandler) ComputeFrameworkScore(r *http.Request, slug string) (*FrameworkScore, bool) {
	return h.computeFrameworkScore(r.Context(), h.auditStatusFromRequest(r), slug)
}

// ComputeFrameworkScoreCtx is the context-only variant for callers without
// an *http.Request (the daily snapshot job in server/main.go). Performs
// its own audit-chain lookup.
func (h *ReportHandler) ComputeFrameworkScoreCtx(ctx context.Context, slug string) (*FrameworkScore, bool) {
	var as AuditChainStatus
	if v, err := audit.Verify(ctx, h.queries); err == nil {
		as.Valid = v.Valid
		as.EntryCount = v.EntryCount
		as.Error = v.Error
	}
	return h.computeFrameworkScore(ctx, as, slug)
}

func (h *ReportHandler) computeFrameworkScore(ctx context.Context, as AuditChainStatus, slug string) (*FrameworkScore, bool) {
	fw, ok := h.frameworkDef(as, slug)
	if !ok {
		return nil, false
	}
	findings := h.buildFrameworkFindings(ctx, slug, fw)
	total := len(findings)

	score := &FrameworkScore{
		Slug:     slug,
		Name:     fw.name,
		Title:    fw.title,
		Total:    total,
		PerEnvoy: make(map[string]ui.EnvoyFrameworkStatus),
		Controls: findings,
	}

	standardKey, hasKey := config.StandardKeyForSlug[slug]

	var activeHostnames []string
	if h.fleetIndex != nil {
		for _, e := range h.fleetIndex.AllEnvoys() {
			if !e.Revoked {
				activeHostnames = append(activeHostnames, e.Hostname)
			}
		}
	}

	for _, hostname := range activeHostnames {
		var coverage *config.StandardCoverage
		if hasKey {
			coverage = h.configStore.EnvoyStandardCoverage(hostname, standardKey)
		}
		covered, waived, partial, missing := partitionEnvoyFindings(findings, coverage)
		pct := 0.0
		if total > 0 {
			pct = float64(covered) / float64(total) * 100
		}
		score.PerEnvoy[hostname] = ui.EnvoyFrameworkStatus{
			Covered:         covered,
			Waived:          waived,
			Partial:         partial,
			Missing:         missing,
			Total:           total,
			CoveragePercent: pct,
		}
	}

	score.Fleet = aggregateFleetPartitions(score.PerEnvoy, total)

	if score.Fleet.CoveragePercent < 0 || score.Fleet.CoveragePercent > 100 {
		slog.Warn("compliance score outside [0,100] — scorer invariant broken",
			"slug", slug, "percent", score.Fleet.CoveragePercent,
			"covered", score.Fleet.Covered, "waived", score.Fleet.Waived,
			"partial", score.Fleet.Partial, "missing", score.Fleet.Missing, "total", total)
	}
	slog.Debug("framework score computed",
		"slug", slug, "envoys", score.Fleet.ActiveEnvoys,
		"covered", score.Fleet.Covered, "waived", score.Fleet.Waived,
		"partial", score.Fleet.Partial, "missing", score.Fleet.Missing,
		"total", total, "score_pct", score.Fleet.CoveragePercent)

	return score, true
}

// aggregateFleetPartitions macro-averages per-envoy partition counts into
// a single fleet aggregate. Buckets are rounded; Missing is forced so
// Covered + Waived + Partial + Missing == Total. CoveragePercent is the
// unweighted mean of per-envoy CoveragePercent, rounded to one decimal.
// Split out for testability — invariants are pure arithmetic that should
// not require a full fleet+configStore fixture.
func aggregateFleetPartitions(perEnvoy map[string]ui.EnvoyFrameworkStatus, total int) FrameworkFleetAggregate {
	agg := FrameworkFleetAggregate{
		ActiveEnvoys: len(perEnvoy),
		Total:        total,
	}
	if agg.ActiveEnvoys == 0 || total == 0 {
		return agg
	}
	var sumCovered, sumWaived, sumPartial, sumPct float64
	for _, st := range perEnvoy {
		sumCovered += float64(st.Covered)
		sumWaived += float64(st.Waived)
		sumPartial += float64(st.Partial)
		sumPct += st.CoveragePercent
	}
	inv := 1.0 / float64(agg.ActiveEnvoys)
	agg.Covered = int(math.Round(sumCovered * inv))
	agg.Waived = int(math.Round(sumWaived * inv))
	agg.Partial = int(math.Round(sumPartial * inv))
	// If rounding pushed the non-missing buckets past Total, trim from the
	// largest so the bars sum to Total and Missing stays ≥ 0.
	if sum := agg.Covered + agg.Waived + agg.Partial; sum > total {
		overshoot := sum - total
		switch {
		case agg.Covered >= agg.Waived && agg.Covered >= agg.Partial:
			agg.Covered -= overshoot
		case agg.Waived >= agg.Partial:
			agg.Waived -= overshoot
		default:
			agg.Partial -= overshoot
		}
	}
	agg.Missing = total - agg.Covered - agg.Waived - agg.Partial
	if agg.Missing < 0 {
		agg.Missing = 0
	}
	agg.CoveragePercent = math.Round(sumPct*inv*10) / 10
	return agg
}

// BuildFrameworkReport builds a compliance report for the given framework slug.
// Returns nil if the slug is unknown. Exported for use by the UI handler.
func (h *ReportHandler) BuildFrameworkReport(r *http.Request, slug string) (*ComplianceReport, error) {
	score, ok := h.ComputeFrameworkScore(r, slug)
	if !ok {
		return nil, fmt.Errorf("unknown framework: %s", slug)
	}
	findings := score.Controls

	report, err := h.BuildReport(r, score.Title, findings)
	if err != nil {
		return nil, err
	}
	report.Slug = slug
	report.FrameworkName = score.Name

	if standardKey, ok := config.StandardKeyForSlug[slug]; ok {
		// Collect per-envoy module sets for per-control coverage calculation.
		type envoyModules struct {
			hostname string
			modules  map[string]bool
		}
		var activeEnvoys []envoyModules

		for i := range report.Fleet {
			node := &report.Fleet[i]
			if st, present := score.PerEnvoy[node.Hostname]; present {
				node.TotalControls = st.Total
				node.CoveredControls = st.Covered
				node.WaivedCount = st.Waived
				node.PartialCount = st.Partial
				node.MissingCount = st.Missing
				node.CoveragePercent = st.CoveragePercent
			}

			coverage := h.configStore.EnvoyStandardCoverage(node.Hostname, standardKey)
			mods := make(map[string]bool)
			if coverage != nil {
				for _, cc := range coverage.CoveredControls {
					for _, m := range cc.Modules {
						mods[m] = true
					}
				}
			}
			activeEnvoys = append(activeEnvoys, envoyModules{hostname: node.Hostname, modules: mods})
		}

		// Compute per-control fleet coverage. Match partitionEnvoyFindings:
		// envoy state alone determines coverage. Vigo's own product-level
		// posture never inflates this number.
		//
		// Rendering semantics:
		//  - satisfied (Connwaer trait escalation only) → 100%
		//  - waived → 100%, painted with the waiver color via the
		//    Status field
		//  - partial → 50%
		//  - else → percentage of active envoys whose assigned modules
		//    carry the matching `compliance:` tag (0% when no module
		//    covers the control — genuinely uncovered)
		controlIndex := h.configStore.ComplianceControlIndex(standardKey)
		fleetTotal := len(activeEnvoys)
		if fleetTotal > 0 {
			for i := range report.Controls {
				cid := report.Controls[i].ControlID
				report.Controls[i].FleetTotal = fleetTotal

				switch report.Controls[i].Status {
				case "satisfied":
					report.Controls[i].FleetCovered = fleetTotal
					report.Controls[i].FleetPercent = 100.0
					continue
				case "waived":
					report.Controls[i].FleetCovered = fleetTotal
					report.Controls[i].FleetPercent = 100.0
					continue
				case "partial":
					report.Controls[i].FleetCovered = fleetTotal / 2
					report.Controls[i].FleetPercent = 50.0
					continue
				}

				// not_satisfied: count how many active envoys have a
				// module tagged for this control. A control with no
				// module tags stays at 0% — it's genuinely uncovered.
				modulesForControl := controlIndex[cid]
				if len(modulesForControl) == 0 {
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
				report.Controls[i].FleetPercent = math.Round(float64(covered)/float64(fleetTotal)*1000) / 10
			}
		}
	}

	// Summary comes straight from the unified scorer's fleet aggregate —
	// macro-averaged per-envoy buckets that balance to Total by construction.
	// Real Partial count, real Waived count (no more forced zeros, no more
	// Satisfied synthesized from avg%×Total).
	if score.Fleet.ActiveEnvoys > 0 {
		report.Summary.CoveragePercent = score.Fleet.CoveragePercent
		report.Summary.Satisfied = score.Fleet.Covered
		report.Summary.Waived = score.Fleet.Waived
		report.Summary.Partial = score.Fleet.Partial
		report.Summary.NotSatisfied = score.Fleet.Missing
		report.Summary.TotalControls = score.Fleet.Total
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
	Fleet       []FleetEnvoyStatus       `json:"fleet"`
	AuditChain  AuditChainStatus        `json:"audit_chain"`
}

// ComplianceReportSummary is the top-level compliance summary.
// Satisfied + Waived + Partial + NotSatisfied == TotalControls by construction
// (the unified scorer balances rounding into Missing).
type ComplianceReportSummary struct {
	TotalNodes         int     `json:"total_nodes"`
	Satisfied          int     `json:"satisfied"`
	Waived             int     `json:"waived"`
	Partial            int     `json:"partial"`
	NotSatisfied       int     `json:"not_satisfied"`
	TotalControls      int     `json:"total_controls"`
	CoveragePercent    float64 `json:"coverage_percent"` // fleet macro-average
	ConvergencePercent float64 `json:"convergence_percent"`
	// Convergence breakdown (kept for JSON API consumers).
	Converged   int `json:"converged"`
	Relapsed    int `json:"relapsed"`
	Diverged    int `json:"diverged"`
	Failed      int `json:"failed"`
	Offline int `json:"offline"`
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

// FleetEnvoyStatus is a per-envoy compliance entry.
type FleetEnvoyStatus struct {
	Hostname        string  `json:"hostname"`
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	LastSeen        string  `json:"last_seen"`
	OS              string  `json:"os,omitempty"`
	IP              string  `json:"ip,omitempty"`
	CoveragePercent float64 `json:"coverage_percent,omitempty"`
	CoveredControls int     `json:"covered_controls,omitempty"`
	// WaivedCount is the number of framework controls addressed by a
	// waiver for this envoy. Kept as its own column per auditor
	// practice (PCI compensating controls, SOC 2 SoA exceptions,
	// ISO 27001 SoA "not applicable"): waivers are NEVER rolled into
	// CoveredControls for coverage reporting. Covered + Waived +
	// Missing = TotalControls.
	WaivedCount     int     `json:"waived_count,omitempty"`
	// PartialCount is the number of framework controls with Status ==
	// "partial" for this envoy — framework-specific half-credit findings
	// (e.g., evidence present but insufficient for full coverage). Kept
	// as its own column so the UI can discriminate partial from missing.
	// Covered + Waived + Partial + Missing = TotalControls.
	PartialCount    int     `json:"partial_count,omitempty"`
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
	var nodes []FleetEnvoyStatus
	for _, t := range h.fleetIndex.AllEnvoys() {
		if t.Revoked {
			continue
		}
		node := FleetEnvoyStatus{
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
	// per-envoy data is overwritten with standard-specific values.
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
			Failed:             int(summary.Failed),
			Offline:        int(summary.Offline),
			NoData:             int(summary.NoData),
			ConvergencePercent: pct,
		},
		Controls:   controls,
		Fleet:      nodes,
		AuditChain: auditStatus,
	}, nil
}

// catalogControl is shorthand for a {ID, Title} pair used by every
// framework's control-catalog builder. Status defaults to
// "not_satisfied" so the partition rule treats unmapped controls as
// missing — coverage flips to "covered" when an envoy's modules carry
// the matching `compliance:` tag, "waived" when a waiver applies, or
// "partial" when a future Connwaer escalation lands. Evidence is left
// empty: the per-control row in the report renders the recommended
// modules for missing controls and the waiver detail for waived ones.
func catalogControl(id, title string) ControlFinding {
	return ControlFinding{ControlID: id, Title: title, Status: "not_satisfied"}
}

// catalog converts a list of {id, title} pairs into the catalog's
// ControlFinding slice. Strictly cosmetic — keeps the per-framework
// builders short and free of repeated struct boilerplate.
func catalog(entries [][2]string) []ControlFinding {
	out := make([]ControlFinding, 0, len(entries))
	for _, e := range entries {
		out = append(out, catalogControl(e[0], e[1]))
	}
	return out
}

// buildHIPAAFindings returns the HIPAA Security Rule control catalog.
// Status of every control reflects envoy state only: covered iff the
// envoy's modules carry the matching `compliance: { hipaa: [...] }`
// tag, waived iff a waiver applies, partial iff a Connwaer escalation
// fires, otherwise missing. Vigo's own architectural posture
// (server-side mTLS, RBAC, audit chain, etc.) lives separately in
// server/compliance.AppControls and never enters this catalog.
func (h *ReportHandler) buildHIPAAFindings() []ControlFinding {
	return catalog([][2]string{
		{"164.312(a)(1)", "Access Control"},
		{"164.312(a)(2)(i)", "Unique User Identification"},
		{"164.312(a)(2)(ii)", "Emergency Access Procedure"},
		{"164.312(a)(2)(iii)", "Automatic Logoff"},
		{"164.312(a)(2)(iv)", "Encryption and Decryption"},
		{"164.312(b)", "Audit Controls"},
		{"164.312(c)(1)", "Integrity"},
		{"164.312(d)", "Person or Entity Authentication"},
		{"164.312(e)(1)", "Transmission Security"},
		{"164.312(e)(2)(ii)", "Encryption (Transmission)"},
		{"164.310(d)(2)(i)", "Disposal"},
		{"164.308(a)(5)(ii)(D)", "Password Management"},
		{"164.308(a)(6)", "Security Incident Procedures"},
		{"164.308(b)", "Business Associate Contracts"},
	})
}

func (h *ReportHandler) handleComplianceReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HIPAA Compliance Report", "vigo-compliance",
		h.buildHIPAAFindings(), false)
}

func (h *ReportHandler) handleComplianceReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HIPAA Compliance Report", "vigo-compliance",
		h.buildHIPAAFindings(), true)
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
  .badge-errors, .badge-relapsed, .badge-diverged, .badge-offline, .badge-no-data { background: #f8d7da; color: #721c24; }
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
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Failed</div></div>
  <div class="summary-card"><div class="value" style="color:#f39c12">%d</div><div class="label">Relapsed</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Diverged</div></div>
  <div class="summary-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Offline</div></div>
</div>
`, gaugeColor, r.Summary.ConvergencePercent, r.Summary.TotalNodes, r.Summary.Converged, r.Summary.Failed, r.Summary.Relapsed, r.Summary.Diverged, r.Summary.Offline)

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
func (h *ReportHandler) buildHITRUSTFindings() []ControlFinding {
	return catalog([][2]string{
		// Domain 01 — Access Control
		{"01.b", "User Registration"},
		{"01.c", "Privilege Management"},
		{"01.d", "User Password Management"},
		{"01.i", "Policy on Use of Network Services"},
		{"01.j", "User Authentication for External Connections"},
		{"01.n", "Network Connection Control"},
		{"01.v", "Information Access Restriction"},
		// Domain 06 — Compliance
		{"06.c", "Protection of Organizational Records"},
		{"06.g", "Compliance with Security Policies"},
		{"06.h", "Technical Compliance Checking"},
		{"06.i", "Information Systems Audit Controls"},
		// Domain 07 — Asset Management
		{"07.a", "Inventory of Assets"},
		// Domain 09 — Communications and Operations Management
		{"09.a", "Documented Operating Procedures"},
		{"09.b", "Change Management"},
		{"09.c", "Segregation of Duties"},
		{"09.d", "Separation of Environments"},
		{"09.i", "Protection Against Malicious Code"},
		{"09.k", "Back-up"},
		{"09.l", "Network Security Management"},
		{"09.q", "Audit Logging"},
		{"09.s", "Protection of Log Information"},
		{"09.v", "Clock Synchronization"},
		// Domain 10 — Systems Acquisition, Development, Maintenance
		{"10.f", "Cryptographic Controls"},
		{"10.g", "Key Management"},
		{"10.h", "Control of Operational Software"},
		{"10.k", "Change Control Procedures"},
		{"10.l", "Technical Review After OS Changes"},
		{"10.m", "Restrictions on Software Changes"},
		// Domain 11 — Incident Management
		{"11.a", "Reporting Security Events"},
		{"11.e", "Collection of Evidence"},
		// Domain 12 — Business Continuity
		{"12.c", "Developing and Implementing BCPs"},
		// Domain 16 — Endpoint Protection
		{"16.a", "Endpoint Protection"},
	})
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
		h.buildHITRUSTFindings(), false)
}

func (h *ReportHandler) handleHITRUSTReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo HITRUST CSF Compliance Report", "vigo-hitrust",
		h.buildHITRUSTFindings(), true)
}

// ---------- SOC 2 Trust Services Criteria ----------

func (h *ReportHandler) buildSOC2Findings() []ControlFinding {
	return catalog([][2]string{
		// CC1 — Control Environment
		{"CC1.1", "COSO Principle 1: Integrity and Ethics"},
		{"CC1.2", "COSO Principle 2: Board Oversight"},
		{"CC1.3", "COSO Principle 3: Management Structure"},
		// CC2 — Communication and Information
		{"CC2.1", "Internal Communication of Objectives"},
		{"CC2.2", "Internal Communication of Policies"},
		// CC3 — Risk Assessment
		{"CC3.1", "Risk Identification and Analysis"},
		{"CC3.2", "Fraud Risk Assessment"},
		{"CC3.3", "Change Risk Assessment"},
		// CC4 — Monitoring Activities
		{"CC4.1", "Ongoing and Separate Evaluations"},
		{"CC4.2", "Communication of Deficiencies"},
		// CC5 — Control Activities
		{"CC5.1", "Selection of Control Activities"},
		{"CC5.2", "Technology General Controls"},
		{"CC5.3", "Deployment of Control Activities"},
		// CC6 — Logical and Physical Access Controls
		{"CC6.1", "Logical Access Security"},
		{"CC6.2", "User Registration and Authorization"},
		{"CC6.3", "Role-Based Access and Least Privilege"},
		{"CC6.6", "Restriction of Access to System Assets"},
		{"CC6.7", "Management of Credentials"},
		{"CC6.8", "Prevention of Unauthorized Software"},
		// CC7 — System Operations
		{"CC7.1", "Detection of Security Events"},
		{"CC7.2", "Monitoring for Anomalies"},
		{"CC7.3", "Evaluation of Security Events"},
		{"CC7.4", "Response to Security Events"},
		{"CC7.5", "Recovery from Security Events"},
		// CC8 — Change Management
		{"CC8.1", "Change Management Process"},
		// CC9 — Risk Mitigation
		{"CC9.1", "Risk Mitigation Activities"},
		// A1 — Availability
		{"A1.1", "Availability Commitments"},
		{"A1.2", "Environmental Protections"},
		// C1 — Confidentiality
		{"C1.1", "Identification of Confidential Information"},
		{"C1.2", "Disposal of Confidential Information"},
		// PI1 — Processing Integrity
		{"PI1.1", "Data Processing Accuracy"},
		{"PI1.2", "Monitoring of Processing"},
	})
}

func (h *ReportHandler) handleSOC2Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo SOC 2 Trust Services Criteria Report", "vigo-soc2",
		h.buildSOC2Findings(), false)
}

func (h *ReportHandler) handleSOC2ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo SOC 2 Trust Services Criteria Report", "vigo-soc2",
		h.buildSOC2Findings(), true)
}

// ---------- PCI DSS v4.0 ----------

func (h *ReportHandler) buildPCIDSSFindings() []ControlFinding {
	return catalog([][2]string{
		// Req 1 — Network Security Controls
		{"1.2.1", "Network Security Controls Configured"},
		{"1.2.5", "Ports, Protocols, Services Restricted"},
		{"1.3.1", "Inbound Traffic Restricted"},
		{"1.4.1", "NSCs Between Trusted and Untrusted"},
		// Req 2 — Secure Configurations
		{"2.2.1", "Configuration Standards Developed"},
		{"2.2.2", "Vendor Defaults Changed"},
		{"2.2.3", "Primary Functions Separated"},
		{"2.2.4", "Unnecessary Services Disabled"},
		{"2.2.5", "Security Parameters Configured"},
		{"2.2.7", "Non-Console Admin Access Encrypted"},
		// Req 3 — Protect Stored Account Data
		{"3.5.1", "Stored Data Encrypted"},
		{"3.5.1.1", "Cryptographic Key Management"},
		// Req 5 — Protect from Malicious Software
		{"5.2.1", "Anti-Malware Deployed"},
		{"5.2.2", "Anti-Malware Performs Scans"},
		{"5.3.1", "Anti-Malware Mechanisms Active"},
		// Req 6 — Develop Secure Systems
		{"6.3.1", "Security Vulnerabilities Identified"},
		{"6.3.3", "Security Patches Installed Promptly"},
		{"6.5.1", "Change Management for Production"},
		// Req 7 — Restrict Access
		{"7.2.1", "Access Control System Defined"},
		{"7.2.2", "Access Based on Job Function"},
		{"7.2.5", "Access Privileges Assigned"},
		// Req 8 — Identify Users
		{"8.2.1", "Unique User IDs Assigned"},
		{"8.2.2", "Group/Shared IDs Not Used"},
		{"8.3.1", "Strong Authentication for All Access"},
		{"8.3.6", "Password Complexity Enforced"},
		{"8.3.9", "MFA for Admin Access"},
		{"8.6.1", "System/Application Accounts Managed"},
		// Req 10 — Log and Monitor
		{"10.2.1", "Audit Logs Enabled"},
		{"10.2.1.1", "User Access Logged"},
		{"10.2.1.2", "Admin Actions Logged"},
		{"10.2.1.3", "Access to Audit Trails Logged"},
		{"10.2.2", "Audit Log Fields Captured"},
		{"10.3.1", "Audit Logs Protected from Modification"},
		{"10.3.2", "Audit Logs Promptly Backed Up"},
		{"10.4.1", "Audit Logs Reviewed Daily"},
		{"10.6.1", "Time Synchronization"},
		// Req 11 — Test Security
		{"11.3.1", "Vulnerability Scans Performed"},
		{"11.5.1", "File Integrity Monitoring"},
		{"11.5.1.1", "FIM Alerts on Unauthorized Changes"},
		// Req 12 — Security Policies
		{"12.1.1", "Security Policy Established"},
		{"12.3.1", "Risk Assessment Performed"},
		{"12.10.1", "Incident Response Plan"},
	})
}

func (h *ReportHandler) handlePCIReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo PCI DSS v4.0 Compliance Report", "vigo-pci",
		h.buildPCIDSSFindings(), false)
}

func (h *ReportHandler) handlePCIReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo PCI DSS v4.0 Compliance Report", "vigo-pci",
		h.buildPCIDSSFindings(), true)
}

// ---------- NIST 800-53 ----------

func (h *ReportHandler) buildNIST80053Findings() []ControlFinding {
	return catalog([][2]string{
		// AC — Access Control
		{"AC-2", "Account Management"},
		{"AC-3", "Access Enforcement"},
		{"AC-6", "Least Privilege"},
		{"AC-7", "Unsuccessful Logon Attempts"},
		{"AC-8", "System Use Notification"},
		{"AC-11", "Session Lock"},
		{"AC-12", "Session Termination"},
		{"AC-17", "Remote Access"},
		// AU — Audit and Accountability
		{"AU-2", "Audit Events"},
		{"AU-3", "Content of Audit Records"},
		{"AU-6", "Audit Review, Analysis, Reporting"},
		{"AU-8", "Time Stamps"},
		{"AU-9", "Protection of Audit Information"},
		{"AU-10", "Non-repudiation"},
		{"AU-11", "Audit Record Retention"},
		{"AU-12", "Audit Generation"},
		// CA — Assessment, Authorization, Monitoring
		{"CA-7", "Continuous Monitoring"},
		// CM — Configuration Management
		{"CM-2", "Baseline Configuration"},
		{"CM-3", "Configuration Change Control"},
		{"CM-5", "Access Restrictions for Change"},
		{"CM-6", "Configuration Settings"},
		{"CM-7", "Least Functionality"},
		{"CM-8", "System Component Inventory"},
		{"CM-11", "User-Installed Software"},
		// CP — Contingency Planning
		{"CP-9", "System Backup"},
		{"CP-10", "System Recovery and Reconstitution"},
		// IA — Identification and Authentication
		{"IA-2", "Identification and Authentication"},
		{"IA-5", "Authenticator Management"},
		{"IA-8", "Identification and Authentication (Non-Org)"},
		// IR — Incident Response
		{"IR-4", "Incident Handling"},
		{"IR-5", "Incident Monitoring"},
		{"IR-6", "Incident Reporting"},
		// SC — System and Communications Protection
		{"SC-7", "Boundary Protection"},
		{"SC-8", "Transmission Confidentiality and Integrity"},
		{"SC-12", "Cryptographic Key Establishment"},
		{"SC-13", "Cryptographic Protection"},
		{"SC-28", "Protection of Information at Rest"},
		// SI — System and Information Integrity
		{"SI-2", "Flaw Remediation"},
		{"SI-3", "Malicious Code Protection"},
		{"SI-4", "System Monitoring"},
		{"SI-7", "Software, Firmware, and Information Integrity"},
	})
}

func (h *ReportHandler) handleNISTReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NIST 800-53 Compliance Report", "vigo-nist800-53",
		h.buildNIST80053Findings(), false)
}

func (h *ReportHandler) handleNISTReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NIST 800-53 Compliance Report", "vigo-nist800-53",
		h.buildNIST80053Findings(), true)
}

// ---------- ISO 27001 ----------

func (h *ReportHandler) buildISO27001Findings() []ControlFinding {
	return catalog([][2]string{
		// A.5 — Information Security Policies
		{"A.5.1", "Management Direction for InfoSec"},
		// A.6 — Organization of Information Security
		{"A.6.1", "Internal Organization"},
		// A.8 — Asset Management
		{"A.8.1", "Responsibility for Assets"},
		{"A.8.2", "Information Classification"},
		// A.9 — Access Control
		{"A.9.1.2", "Access to Networks and Services"},
		{"A.9.2.1", "User Registration and De-registration"},
		{"A.9.2.3", "Management of Privileged Access"},
		{"A.9.3.1", "Use of Secret Authentication Information"},
		{"A.9.4.1", "Information Access Restriction"},
		{"A.9.4.2", "Secure Log-on Procedures"},
		{"A.9.4.3", "Password Management System"},
		// A.10 — Cryptography
		{"A.10.1.1", "Policy on Use of Cryptographic Controls"},
		{"A.10.1.2", "Key Management"},
		// A.12 — Operations Security
		{"A.12.1.2", "Change Management"},
		{"A.12.1.4", "Separation of Environments"},
		{"A.12.2.1", "Controls Against Malware"},
		{"A.12.3.1", "Information Backup"},
		{"A.12.4.1", "Event Logging"},
		{"A.12.4.3", "Administrator and Operator Logs"},
		{"A.12.4.4", "Clock Synchronisation"},
		{"A.12.5.1", "Installation of Software on Operational Systems"},
		{"A.12.6.1", "Management of Technical Vulnerabilities"},
		// A.13 — Communications Security
		{"A.13.1.1", "Network Controls"},
		{"A.13.2.1", "Information Transfer Policies"},
		// A.14 — System Acquisition, Development, Maintenance
		{"A.14.1.2", "Securing Application Services"},
		{"A.14.2.2", "System Change Control Procedures"},
		// A.16 — Information Security Incident Management
		{"A.16.1.2", "Reporting Information Security Events"},
		{"A.16.1.5", "Response to Information Security Incidents"},
		{"A.16.1.7", "Collection of Evidence"},
		// A.17 — Business Continuity
		{"A.17.1.1", "Planning Information Security Continuity"},
		// A.18 — Compliance
		{"A.18.1.3", "Protection of Records"},
		{"A.18.2.2", "Compliance with Security Policies"},
		{"A.18.2.3", "Technical Compliance Review"},
	})
}

func (h *ReportHandler) handleISO27001Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ISO 27001 Compliance Report", "vigo-iso27001",
		h.buildISO27001Findings(), false)
}

func (h *ReportHandler) handleISO27001ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ISO 27001 Compliance Report", "vigo-iso27001",
		h.buildISO27001Findings(), true)
}

// ---------- GDPR ----------

func (h *ReportHandler) buildGDPRFindings() []ControlFinding {
	return catalog([][2]string{
		// Article 5 — Principles
		{"Art.5(1)(f)", "Integrity and Confidentiality"},
		// Article 25 — Data Protection by Design
		{"Art.25(1)", "Data Protection by Design"},
		{"Art.25(2)", "Data Protection by Default"},
		// Article 30 — Records of Processing
		{"Art.30(1)", "Records of Processing Activities"},
		// Article 32 — Security of Processing
		{"Art.32(1)(a)", "Encryption"},
		{"Art.32(1)(b)", "Confidentiality, Integrity, Availability"},
		{"Art.32(1)(c)", "Ability to Restore Availability"},
		{"Art.32(1)(d)", "Testing Security Measures"},
		{"Art.32(2)", "Risk Assessment for Processing"},
		// Article 33 — Breach Notification
		{"Art.33(1)", "Notification to Supervisory Authority"},
		{"Art.33(3)", "Content of Notification"},
		// Article 35 — Data Protection Impact Assessment
		{"Art.35(1)", "Data Protection Impact Assessment"},
		// Article 17 — Right to Erasure
		{"Art.17(1)", "Right to Erasure"},
	})
}

func (h *ReportHandler) handleGDPRReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo GDPR Compliance Report", "vigo-gdpr",
		h.buildGDPRFindings(), false)
}

func (h *ReportHandler) handleGDPRReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo GDPR Compliance Report", "vigo-gdpr",
		h.buildGDPRFindings(), true)
}

// ---------- NERC CIP ----------

func (h *ReportHandler) buildNERCCIPFindings() []ControlFinding {
	return catalog([][2]string{
		// CIP-002 — BES Cyber System Categorization
		{"CIP-002-5.1", "BES Cyber System Identification"},
		// CIP-003 — Security Management Controls
		{"CIP-003-8-R1", "Cyber Security Policies"},
		{"CIP-003-8-R2", "Cyber Security Plans"},
		// CIP-004 — Personnel and Training
		{"CIP-004-6-R4", "Access Management Program"},
		{"CIP-004-6-R5", "Access Revocation"},
		// CIP-005 — Electronic Security Perimeters
		{"CIP-005-7-R1", "Electronic Security Perimeter"},
		{"CIP-005-7-R2", "Remote Access Management"},
		// CIP-007 — Systems Security Management
		{"CIP-007-6-R1", "Ports and Services"},
		{"CIP-007-6-R2", "Security Patch Management"},
		{"CIP-007-6-R3", "Malicious Code Prevention"},
		{"CIP-007-6-R4", "Security Event Monitoring"},
		{"CIP-007-6-R5", "System Access Control"},
		// CIP-009 — Recovery Plans
		{"CIP-009-6-R1", "Recovery Plan Specifications"},
		{"CIP-009-6-R2", "Recovery Plan Implementation and Testing"},
		// CIP-010 — Configuration Change Management
		{"CIP-010-3-R1", "Configuration Change Management"},
		{"CIP-010-3-R2", "Configuration Monitoring"},
		// CIP-011 — Information Protection
		{"CIP-011-2-R1", "Information Protection"},
		{"CIP-011-2-R2", "BES Cyber Asset Reuse and Disposal"},
	})
}

func (h *ReportHandler) handleNERCCIPReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NERC CIP Compliance Report", "vigo-nerccip",
		h.buildNERCCIPFindings(), false)
}

func (h *ReportHandler) handleNERCCIPReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NERC CIP Compliance Report", "vigo-nerccip",
		h.buildNERCCIPFindings(), true)
}

// ---------- Cyber Essentials+ ----------

func (h *ReportHandler) buildCyberEssentialsPlusFindings() []ControlFinding {
	return catalog([][2]string{
		// Theme 1 — Firewalls
		{"CE-FW-1", "Boundary Firewalls"},
		{"CE-FW-2", "Firewall Rule Review"},
		{"CE-FW-3", "Default Deny"},
		{"CE-FW-4", "Host-Based Firewalls"},
		{"CE-FW-5", "Administrative Interface Protection"},
		// Theme 2 — Secure Configuration
		{"CE-SC-1", "Remove Unnecessary Software"},
		{"CE-SC-2", "Disable Unnecessary Accounts"},
		{"CE-SC-3", "Change Default Passwords"},
		{"CE-SC-4", "Disable Autorun"},
		{"CE-SC-5", "Secure Configuration Baselines"},
		{"CE-SC-6", "Device Lockdown"},
		// Theme 3 — Security Update Management
		{"CE-SU-1", "Software Licensing"},
		{"CE-SU-2", "Patch Within 14 Days"},
		{"CE-SU-3", "Unsupported Software Removal"},
		{"CE-SU-4", "Automatic Update Configuration"},
		// Theme 4 — User Access Control
		{"CE-AC-1", "User Account Management"},
		{"CE-AC-2", "Least Privilege"},
		{"CE-AC-3", "Authentication Requirements"},
		{"CE-AC-4", "Multi-Factor Authentication"},
		{"CE-AC-5", "Account Lockout"},
		{"CE-AC-6", "Privileged Access Management"},
		// Theme 5 — Malware Protection
		{"CE-MW-1", "Anti-Malware Software"},
		{"CE-MW-2", "Anti-Malware Configuration"},
		{"CE-MW-3", "Anti-Malware Updates"},
		{"CE-MW-4", "Application Allow-Listing"},
		{"CE-MW-5", "Sandboxing and Code Execution Prevention"},
		// Supplementary — Monitoring and Logging (CE+ only)
		{"CE-ML-1", "Security Event Logging"},
		{"CE-ML-2", "Log Retention"},
		{"CE-ML-3", "Incident Detection"},
	})
}

func (h *ReportHandler) handleCyberEssentialsReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo Cyber Essentials+ Compliance Report", "vigo-cyberessentials",
		h.buildCyberEssentialsPlusFindings(), false)
}

func (h *ReportHandler) handleCyberEssentialsReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo Cyber Essentials+ Compliance Report", "vigo-cyberessentials",
		h.buildCyberEssentialsPlusFindings(), true)
}

// ---------- IEC 62443 Industrial Cybersecurity ----------

// buildIEC62443Findings returns the IEC 62443 control catalog. Maps to
// security levels SL 1-4 across zones and conduits for industrial IT
// infrastructure (MES servers, historians, jump hosts).
func (h *ReportHandler) buildIEC62443Findings() []ControlFinding {
	return catalog([][2]string{
		// FR 1 — Identification and Authentication Control (IAC)
		{"SR 1.1", "Human User Identification and Authentication"},
		{"SR 1.2", "Software Process Identification and Authentication"},
		{"SR 1.3", "Account Management"},
		{"SR 1.5", "Authenticator Management"},
		{"SR 1.7", "Strength of Password-Based Authentication"},
		{"SR 1.8", "Public Key Infrastructure Certificates"},
		{"SR 1.9", "Strength of Public Key Authentication"},
		{"SR 1.10", "Authenticator Feedback"},
		// FR 2 — Use Control (UC)
		{"SR 2.1", "Authorization Enforcement"},
		{"SR 2.2", "Wireless Use Control"},
		{"SR 2.8", "Auditable Events"},
		{"SR 2.9", "Audit Storage Capacity"},
		{"SR 2.10", "Response to Audit Processing Failures"},
		{"SR 2.11", "Timestamps"},
		{"SR 2.12", "Non-Repudiation"},
		// FR 3 — System Integrity (SI)
		{"SR 3.1", "Communication Integrity"},
		{"SR 3.2", "Malicious Code Protection"},
		{"SR 3.3", "Security Functionality Verification"},
		{"SR 3.4", "Software and Information Integrity"},
		{"SR 3.5", "Input Validation"},
		{"SR 3.8", "Session Integrity"},
		{"SR 3.9", "Protection of Audit Information"},
		// FR 4 — Data Confidentiality (DC)
		{"SR 4.1", "Information Confidentiality"},
		{"SR 4.3", "Use of Cryptography"},
		// FR 5 — Restricted Data Flow (RDF)
		{"SR 5.1", "Network Segmentation"},
		{"SR 5.2", "Zone Boundary Protection"},
		{"SR 5.3", "General Purpose Person-to-Person Communication Restrictions"},
		{"SR 5.4", "Application Partitioning"},
		// FR 6 — Timely Response to Events (TRE)
		{"SR 6.1", "Audit Log Accessibility"},
		{"SR 6.2", "Continuous Monitoring"},
		// FR 7 — Resource Availability (RA)
		{"SR 7.1", "Denial of Service Protection"},
		{"SR 7.2", "Resource Management"},
		{"SR 7.3", "Control System Backup"},
		{"SR 7.4", "Control System Recovery and Reconstitution"},
		{"SR 7.6", "Network and Security Configuration Settings"},
		{"SR 7.7", "Least Functionality"},
		{"SR 7.8", "Control System Component Inventory"},
	})
}

func (h *ReportHandler) buildSOXFindings() []ControlFinding {
	return catalog([][2]string{
		// Change Management
		{"ITGC-CM-1", "Change Authorization"},
		{"ITGC-CM-2", "Change Documentation"},
		{"ITGC-CM-3", "Change Testing"},
		{"ITGC-CM-4", "Change Reversibility"},
		// Access Controls
		{"ITGC-AC-1", "Authentication Enforcement"},
		{"ITGC-AC-2", "Authorization Controls"},
		{"ITGC-AC-3", "Account Management"},
		{"ITGC-AC-4", "Password Policy"},
		{"ITGC-AC-5", "Privileged Access Monitoring"},
		// System Integrity
		{"ITGC-SI-1", "File Integrity Monitoring"},
		{"ITGC-SI-2", "Continuous Enforcement"},
		{"ITGC-SI-3", "System Baseline"},
	})
}

func (h *ReportHandler) buildFINRAFindings() []ControlFinding {
	return catalog([][2]string{
		// Rule 4370 Infrastructure
		{"Rule-4370-1", "Critical Infrastructure Monitoring"},
		{"Rule-4370-2", "Infrastructure Documentation"},
		{"Rule-4370-3", "Service Availability"},
		// BCP
		{"BCP-1", "Backup Configuration"},
		{"BCP-2", "Recovery Procedures"},
		{"BCP-3", "BCP Testing"},
		// WORM
		{"SEC-17a4-1", "WORM Storage Configuration"},
		{"SEC-17a4-2", "Retention Policy Enforcement"},
	})
}

func (h *ReportHandler) buildMiFID2Findings() []ControlFinding {
	return catalog([][2]string{
		// System Resilience
		{"RTS7-Art5-1", "Continuous System Monitoring"},
		{"RTS7-Art5-2", "System Availability"},
		{"RTS7-Art5-3", "Incident Detection"},
		// Change Management
		{"RTS7-Art9-1", "Change Documentation"},
		{"RTS7-Art9-2", "Change Authorization"},
		{"RTS7-Art9-3", "Audit Trail Integrity"},
		// Capacity & Kill Switch
		{"RTS7-Art11-1", "Capacity Monitoring"},
		{"RTS7-Art11-2", "Capacity Planning"},
		{"RTS7-Art14-1", "Kill Switch Mechanism"},
	})
}

func (h *ReportHandler) buildCCSSFindings() []ControlFinding {
	return catalog([][2]string{
		// Key Storage
		{"CCSS-2.1", "Key Storage Access Controls"},
		{"CCSS-2.2", "Key Backup Protection"},
		{"CCSS-2.3", "HSM Service Availability"},
		{"CCSS-2.4", "Key Access Limits"},
		// Network Security
		{"CCSS-3.1", "SSH Hardening"},
		{"CCSS-3.2", "Network Segmentation"},
		// Authorization
		{"CCSS-6.1", "Keyholder Authorization"},
		{"CCSS-6.2", "Keyholder Revocation"},
		// Audit
		{"CCSS-8.1", "Audit Logging"},
		{"CCSS-8.2", "Log Retention"},
		// Data Sanitization
		{"CCSS-9.1", "Data Sanitization"},
		// Proof of Reserves
		{"CCSS-10.1", "Proof of Reserves"},
	})
}

func (h *ReportHandler) buildFDA21CFR11Findings() []ControlFinding {
	return catalog([][2]string{
		// Access Controls
		{"11.10-d", "System Access Controls"},
		{"11.10-g", "Authority Checks"},
		{"11.300-a", "Unique User Identification"},
		{"11.300-b", "Password Controls"},
		// Audit Trail
		{"11.10-e", "Audit Trail"},
		{"11.10-e-1", "Audit Trail Integrity"},
		{"11.10-k-2", "Documentation of Changes"},
		// System Integrity
		{"11.10-a", "System Validation"},
		{"11.10-c", "Record Protection"},
		// Electronic Signatures
		{"11.10-h", "Electronic Signatures"},
	})
}

func (h *ReportHandler) buildGamingFindings() []ControlFinding {
	return catalog([][2]string{
		// System Integrity (Nevada, MGA, UKGC)
		{"NGC-TS-1.1", "System Hardening (Nevada)"},
		{"NGC-TS-1.2", "File Integrity (Nevada)"},
		{"MGA-RGR-4.1", "System Security (Malta)"},
		{"MGA-RGR-4.2", "Data Integrity (Malta)"},
		{"UKGC-RTS-4A", "Security Controls (UK)"},
		{"UKGC-RTS-4B", "Change Management (UK)"},
		// Availability
		{"NGC-TS-2.1", "Service Monitoring (Nevada)"},
		{"NGC-TS-2.2", "Recovery Capability (Nevada)"},
		{"MGA-RGR-5.1", "System Availability (Malta)"},
		{"MGA-RGR-5.2", "Disaster Recovery (Malta)"},
		{"UKGC-RTS-5A", "Resilience (UK)"},
		{"UKGC-RTS-5B", "Business Continuity (UK)"},
		// RNG & Gaming Devices
		{"NGC-TS-3.1", "Change Control for Gaming Systems (Nevada)"},
		{"NGC-TS-4.1", "RNG Certification (Nevada)"},
		{"NGC-TS-4.2", "RNG Monitoring (Nevada)"},
		{"MGA-RGR-6.1", "RNG Requirements (Malta)"},
		{"UKGC-RTS-6A", "RNG Testing (UK)"},
	})
}

func (h *ReportHandler) buildCISWindowsFindings() []ControlFinding {
	return catalog([][2]string{
		// Account Policies
		{"1.1.1", "Password History"},
		{"1.1.2", "Maximum Password Age"},
		{"1.1.3", "Minimum Password Age"},
		{"1.1.4", "Minimum Password Length"},
		{"1.1.5", "Password Complexity"},
		{"1.2.1", "Account Lockout Duration"},
		{"1.2.2", "Account Lockout Threshold"},
		{"1.2.3", "Reset Account Lockout Counter"},
		// Security Options
		{"2.3.1.1", "Guest Account Disabled"},
		{"2.3.7.1", "Interactive Logon Message"},
		{"2.3.10.1", "SMB Signing Required (Server)"},
		// Windows Firewall
		{"9.1.1", "Domain Profile Enabled"},
		{"9.1.2", "Domain Default Inbound"},
		{"9.2.1", "Private Profile Enabled"},
		{"9.3.1", "Public Profile Enabled"},
		// Audit Policies
		{"17.1.1", "Audit Credential Validation"},
		{"17.2.1", "Audit Security Group Management"},
		{"17.3.1", "Audit Logon"},
		{"17.5.1", "Audit Process Creation"},
		{"17.9.1", "Audit System Integrity"},
		// Windows Defender
		{"18.9.47.1", "Windows Defender Real-Time Protection"},
	})
}

// ---------- CIS Ubuntu ----------

func (h *ReportHandler) buildCISUbuntuFindings() []ControlFinding {
	return catalog([][2]string{
		// Section 1 — Filesystem
		{"1.1.1.1", "Disable cramfs"},
		{"1.1.1.2", "Disable freevxfs"},
		{"1.1.1.3", "Disable jffs2"},
		{"1.1.1.4", "Disable hfs"},
		{"1.1.1.5", "Disable hfsplus"},
		{"1.1.1.6", "Disable squashfs"},
		{"1.1.1.7", "Disable udf"},
		{"1.1.1.8", "Disable USB Storage"},
		{"1.4.1", "Bootloader Permissions"},
		{"1.5.1", "ASLR Enabled"},
		{"1.5.2", "Restrict ptrace"},
		{"1.5.3", "Core Dumps Restricted"},
		{"1.5.4", "Apport Disabled"},
		// Section 2 — Services
		{"2.1.1", "Time Synchronization"},
		{"2.1.2", "X Window System Removed"},
		{"2.1.3", "Avahi Disabled"},
		{"2.1.4", "CUPS Disabled"},
		{"2.1.5", "DHCP Server Disabled"},
		{"2.1.6", "LDAP Server Disabled"},
		{"2.1.7", "NFS Disabled"},
		{"2.1.8", "DNS Server Disabled"},
		{"2.1.9", "FTP Server Disabled"},
		{"2.1.10", "HTTP Server Disabled"},
		{"2.1.11", "IMAP/POP3 Disabled"},
		{"2.1.12", "Samba Disabled"},
		{"2.1.13", "Squid Disabled"},
		{"2.1.14", "SNMP Disabled"},
		{"2.1.15", "RPC Disabled"},
		{"2.1.16", "rsync Disabled"},
		{"2.1.17", "NIS Removed"},
		{"2.2.1", "rsh Removed"},
		{"2.2.2", "talk Removed"},
		{"2.2.3", "telnet Removed"},
		{"2.2.4", "LDAP Clients Removed"},
		{"2.2.5", "rpcbind Removed"},
		{"2.2.6", "FTP Client Removed"},
		// Section 3 — Network
		{"3.1.1", "Disable DCCP"},
		{"3.2.1", "IP Forwarding Disabled"},
		{"3.2.2", "Packet Redirects Disabled"},
		{"3.2.3", "Source Routed Packets Disabled"},
		{"3.2.4", "ICMP Redirects Disabled"},
		{"3.2.5", "Secure ICMP Redirects Disabled"},
		{"3.2.6", "Suspicious Packets Logged"},
		{"3.2.7", "Broadcast ICMP Ignored"},
		{"3.2.8", "Bogus ICMP Ignored"},
		{"3.2.9", "Reverse Path Filtering"},
		{"3.3.1", "TCP SYN Cookies"},
		{"3.3.2", "IPv6 Router Ads Disabled"},
		{"3.4.1", "UFW Installed"},
		{"3.4.2", "UFW Default Deny"},
		{"3.4.4", "UFW Enabled"},
		// Section 4 — Logging
		{"4.1.1.1", "auditd Installed"},
		{"4.1.1.2", "auditd Enabled"},
		{"4.1.1.3", "Audit at Boot"},
		{"4.1.3", "Date/Time Audit"},
		{"4.1.4", "User/Group Audit"},
		{"4.1.5", "Network Audit"},
		{"4.1.6", "MAC Policy Audit"},
		{"4.1.7", "Login/Logout Audit"},
		{"4.1.8", "Session Audit"},
		{"4.1.9", "Permission Change Audit"},
		{"4.1.10", "Unauthorized Access Audit"},
		{"4.1.12", "Mount Audit"},
		{"4.1.13", "File Deletion Audit"},
		{"4.1.14", "Sudoers Audit"},
		{"4.1.15", "Sudo Log Audit"},
		{"4.1.16", "Kernel Module Audit"},
		{"4.1.17", "Immutable Audit Rules"},
		{"4.2.1.1", "rsyslog Installed"},
		{"4.2.1.2", "rsyslog Enabled"},
		{"4.2.1.3", "rsyslog File Permissions"},
		{"4.2.3", "Log File Permissions"},
		// Section 5 — Access/Auth
		{"5.1.1", "Cron Enabled"},
		{"5.1.2", "Crontab Permissions"},
		{"5.1.8", "Cron Restricted"},
		{"5.2.1", "sshd_config Permissions"},
		{"5.2.5", "SSH LogLevel VERBOSE"},
		{"5.2.6", "SSH X11 Forwarding Off"},
		{"5.2.7", "SSH MaxAuthTries"},
		{"5.2.10", "SSH No Root Login"},
		{"5.2.11", "SSH No Empty Passwords"},
		{"5.2.13", "SSH Strong Ciphers"},
		{"5.2.14", "SSH Strong MACs"},
		{"5.2.15", "SSH Strong KEX"},
		{"5.2.16", "SSH Client Alive"},
		{"5.2.18", "SSH Banner"},
		{"5.2.20", "SSH AllowTcpForwarding Off"},
		{"5.3.1", "PAM pwquality"},
		{"5.3.3", "PAM faillock"},
		{"5.3.4", "Password History"},
		{"5.4.1.1", "Password Aging"},
		{"5.4.2", "System Accounts Locked"},
		{"5.4.4", "Default umask 027"},
		{"5.4.5", "Shell Timeout"},
		// Section 6 — Maintenance
		{"6.1.2", "/etc/passwd Permissions"},
		{"6.1.4", "/etc/shadow Permissions"},
		{"6.1.6", "/etc/group Permissions"},
		{"6.1.8", "/etc/gshadow Permissions"},
		{"6.1.10", "No World-Writable Files"},
		{"6.2.1", "No Empty Passwords"},
		{"6.2.2", "No UID 0 Except Root"},
		{"6.2.5", "Home Directory Permissions"},
	})
}

func (h *ReportHandler) handleCISUbuntuReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS Ubuntu Linux Benchmark Report", "vigo-cis-ubuntu",
		h.buildCISUbuntuFindings(), false)
}

func (h *ReportHandler) handleCISUbuntuReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS Ubuntu Linux Benchmark Report", "vigo-cis-ubuntu",
		h.buildCISUbuntuFindings(), true)
}

// ---------- CIS RHEL ----------

func (h *ReportHandler) buildCISRHELFindings() []ControlFinding {
	return catalog([][2]string{
		// Section 1 — Filesystem
		{"1.1.1.1", "Disable cramfs"},
		{"1.1.1.2", "Disable freevxfs"},
		{"1.1.1.3", "Disable jffs2"},
		{"1.1.1.4", "Disable hfs"},
		{"1.1.1.5", "Disable hfsplus"},
		{"1.1.1.6", "Disable squashfs"},
		{"1.1.1.7", "Disable udf"},
		{"1.1.1.8", "Disable USB Storage"},
		{"1.4.1", "Bootloader Permissions"},
		{"1.5.1", "ASLR Enabled"},
		{"1.5.2", "Restrict ptrace"},
		{"1.5.3", "Core Dumps Restricted"},
		// Section 2 — Services
		{"2.1.1", "Time Synchronization"},
		{"2.1.2", "X Window Removed"},
		{"2.1.3", "Avahi Disabled"},
		{"2.1.4", "CUPS Disabled"},
		{"2.1.5", "DHCP Server Disabled"},
		{"2.1.6", "LDAP Server Disabled"},
		{"2.1.7", "NFS Disabled"},
		{"2.1.8", "DNS Server Disabled"},
		{"2.1.9", "FTP Server Disabled"},
		{"2.1.10", "HTTP Server Disabled"},
		{"2.1.11", "IMAP/POP3 Disabled"},
		{"2.2.1", "ypbind Removed"},
		{"2.2.2", "telnet Removed"},
		{"2.2.3", "rsh Removed"},
		{"2.2.4", "talk Removed"},
		{"2.2.5", "LDAP Clients Removed"},
		// Section 3 — Network
		{"3.1.1", "Disable DCCP"},
		{"3.2.1", "IP Forwarding Disabled"},
		{"3.2.2", "Packet Redirects Disabled"},
		{"3.2.3", "Source Routed Packets Disabled"},
		{"3.2.4", "ICMP Redirects Disabled"},
		{"3.2.5", "Secure ICMP Redirects Disabled"},
		{"3.2.6", "Suspicious Packets Logged"},
		{"3.2.7", "Broadcast ICMP Ignored"},
		{"3.2.8", "Bogus ICMP Ignored"},
		{"3.2.9", "Reverse Path Filtering"},
		{"3.3.1", "TCP SYN Cookies"},
		{"3.3.2", "IPv6 Router Ads Disabled"},
		{"3.4.1.1", "firewalld Installed"},
		{"3.4.1.2", "firewalld Enabled"},
		{"3.4.2.1", "Default Zone Drop"},
		// Section 4 — Logging
		{"4.1.1.1", "audit Installed"},
		{"4.1.1.2", "auditd Enabled"},
		{"4.1.1.3", "Audit at Boot"},
		{"4.1.1.4", "Audit Backlog Limit"},
		{"4.1.3", "Date/Time Audit"},
		{"4.1.4", "User/Group Audit"},
		{"4.1.5", "Network Audit"},
		{"4.1.6", "MAC Policy Audit"},
		{"4.1.7", "Login/Logout Audit"},
		{"4.1.8", "Session Audit"},
		{"4.1.9", "Permission Change Audit"},
		{"4.1.10", "Unauthorized Access Audit"},
		{"4.1.12", "Mount Audit"},
		{"4.1.13", "File Deletion Audit"},
		{"4.1.14", "Sudoers Audit"},
		{"4.1.16", "Kernel Module Audit"},
		{"4.1.17", "Immutable Audit Rules"},
		{"4.2.1.1", "rsyslog Installed"},
		{"4.2.1.2", "rsyslog Enabled"},
		{"4.2.1.3", "rsyslog File Permissions"},
		{"4.2.3", "Log File Permissions"},
		// Section 5 — Access/Auth
		{"5.1.1", "Cron Enabled"},
		{"5.1.2", "Crontab Permissions"},
		{"5.1.8", "Cron Restricted"},
		{"5.2.1", "sshd_config Permissions"},
		{"5.2.5", "SSH LogLevel VERBOSE"},
		{"5.2.6", "SSH X11 Forwarding Off"},
		{"5.2.10", "SSH No Root Login"},
		{"5.2.11", "SSH No Empty Passwords"},
		{"5.2.13", "SSH Strong Ciphers"},
		{"5.2.14", "SSH Strong MACs"},
		{"5.2.15", "SSH Strong KEX"},
		{"5.2.16", "SSH Client Alive"},
		{"5.2.18", "SSH Banner"},
		{"5.3.1", "authselect Profile"},
		{"5.3.2", "Password Quality"},
		{"5.3.3", "Account Lockout"},
		{"5.3.4", "Password History"},
		{"5.4.1", "SELinux Enforcing"},
		{"5.4.3", "SELinux Policy Targeted"},
		{"5.5.1.1", "Password Aging"},
		{"5.5.2", "System Accounts Locked"},
		{"5.5.4", "Default umask 027"},
		{"5.5.5", "Shell Timeout"},
		// Section 6 — Maintenance
		{"6.1.2", "/etc/passwd Permissions"},
		{"6.1.4", "/etc/shadow Permissions"},
		{"6.1.6", "/etc/group Permissions"},
		{"6.1.8", "/etc/gshadow Permissions"},
		{"6.1.10", "No World-Writable Files"},
		{"6.2.1", "No Empty Passwords"},
		{"6.2.2", "No UID 0 Except Root"},
		{"6.2.5", "Home Directory Permissions"},
	})
}

func (h *ReportHandler) handleCISRHELReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS RHEL Benchmark Report", "vigo-cis-rhel",
		h.buildCISRHELFindings(), false)
}

func (h *ReportHandler) handleCISRHELReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo CIS RHEL Benchmark Report", "vigo-cis-rhel",
		h.buildCISRHELFindings(), true)
}

func (h *ReportHandler) handleABACyberReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ABA Cybersecurity Compliance Report", "vigo-aba-cyber",
		h.buildABACyberFindings(), false)
}

func (h *ReportHandler) handleABACyberReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo ABA Cybersecurity Compliance Report", "vigo-aba-cyber",
		h.buildABACyberFindings(), true)
}

func (h *ReportHandler) handleNYDFSCyberReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(), false)
}

func (h *ReportHandler) handleNYDFSCyberReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(), true)
}

// buildABACyberFindings returns the ABA Cybersecurity control catalog,
// combining ABA Formal Opinion 477R, Model Rule 1.1 (technology
// competence), and Model Rule 1.6(c) (confidentiality — reasonable
// efforts).
func (h *ReportHandler) buildABACyberFindings() []ControlFinding {
	return catalog([][2]string{
		// Model Rule 1.1 — Technology Competence
		{"ABA-1.1", "Technology Competence (Model Rule 1.1 Comment 8)"},
		// Model Rule 1.6(c) — Reasonable Efforts
		{"ABA-1.6-1", "Reasonable Efforts — Encryption at Rest"},
		{"ABA-1.6-2", "Reasonable Efforts — Encryption in Transit"},
		{"ABA-1.6-3", "Reasonable Efforts — Access Controls"},
		{"ABA-1.6-4", "Reasonable Efforts — Multi-Factor Authentication"},
		{"ABA-1.6-5", "Reasonable Efforts — Secure Disposal"},
		// Formal Opinion 477R — Specific Requirements
		{"ABA-477R-1", "Nature of Threat Assessment"},
		{"ABA-477R-2", "Client Matter Sensitivity Classification"},
		{"ABA-477R-3", "Secure Communication Channels"},
		{"ABA-477R-4", "Device Security — Endpoints"},
		{"ABA-477R-5", "Device Security — Mobile"},
		{"ABA-477R-6", "Patch Management"},
		{"ABA-477R-7", "Firewall and Network Security"},
		{"ABA-477R-8", "Anti-Malware Protection"},
		{"ABA-477R-9", "Audit Logging"},
		{"ABA-477R-10", "Backup and Recovery"},
		{"ABA-477R-11", "Incident Response Plan"},
		{"ABA-477R-12", "Vendor and Third-Party Oversight"},
	})
}

// buildNYDFSCyberFindings returns the NY DFS 23 NYCRR 500 control
// catalog. Applies to law firms and other entities handling data for
// DFS-regulated financial institutions.
func (h *ReportHandler) buildNYDFSCyberFindings() []ControlFinding {
	return catalog([][2]string{
		{"NYCRR-500.02", "Cybersecurity Program"},
		{"NYCRR-500.03", "Cybersecurity Policy"},
		{"NYCRR-500.05", "Penetration Testing and Vulnerability Assessment"},
		{"NYCRR-500.06", "Audit Trail"},
		{"NYCRR-500.07", "Access Privileges and Management"},
		{"NYCRR-500.08", "Application Security"},
		{"NYCRR-500.09", "Risk Assessment"},
		{"NYCRR-500.10", "Cybersecurity Personnel and Intelligence"},
		{"NYCRR-500.11", "Third-Party Service Provider Security"},
		{"NYCRR-500.12", "Multi-Factor Authentication"},
		{"NYCRR-500.13", "Data Retention and Disposal"},
		{"NYCRR-500.14", "Monitoring and Detection"},
		{"NYCRR-500.15", "Encryption of Nonpublic Information"},
		{"NYCRR-500.16", "Incident Response Plan"},
		{"NYCRR-500.17", "Annual Certification of Compliance"},
	})
}

func (h *ReportHandler) handleIEC62443Report(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo IEC 62443 Industrial Cybersecurity Report", "vigo-iec62443",
		h.buildIEC62443Findings(), false)
}

func (h *ReportHandler) handleIEC62443ReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo IEC 62443 Industrial Cybersecurity Report", "vigo-iec62443",
		h.buildIEC62443Findings(), true)
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
	Failed         int `json:"failed"`
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

// buildFrameworkCoverage computes per-standard compliance scores for every
// enabled framework. Routes through the single-source-of-truth scorer so
// these numbers agree with the per-framework page and the envoy-detail
// Standards tab. Buckets are disjoint (Covered / Waived / Partial / Missing),
// so callers that sum them get sensible totals without waiver double-counting.
func (h *ReportHandler) buildFrameworkCoverage(r *http.Request) []FrameworkSummary {
	if h.configStore == nil {
		return nil
	}

	// Ordered list of (name, slug) pairs so Executive report slug lookups
	// stay stable across enablement changes.
	type fwEntry struct {
		name string
		slug string
	}
	fwDefs := []fwEntry{
		{"HIPAA", "compliance"},
		{"HITRUST CSF", "hitrust"},
		{"SOC 2", "soc2"},
		{"PCI DSS v4.0", "pci"},
		{"NIST 800-53", "nist"},
		{"ISO 27001", "iso27001"},
		{"GDPR", "gdpr"},
		{"NERC CIP", "nerccip"},
		{"Cyber Essentials+", "cyberessentials"},
		{"IEC 62443", "iec62443"},
		{"SOX", "sox"},
		{"FINRA", "finra"},
		{"MiFID II", "mifid2"},
		{"CCSS", "ccss"},
		{"FDA 21 CFR Part 11", "fda21cfr11"},
		{"Gaming Commission", "gaming"},
		{"CIS Windows", "ciswindows"},
		{"CIS Ubuntu", "cisubuntu"},
		{"CIS RHEL", "cisrhel"},
		{"ABA Cybersecurity", "abacyber"},
		{"NY DFS Cyber (23 NYCRR 500)", "nydfscyber"},
	}

	frameworks := make([]FrameworkSummary, 0, len(fwDefs))
	for _, fw := range fwDefs {
		standardKey, hasKey := config.StandardKeyForSlug[fw.slug]
		if !hasKey || !h.configStore.IsStandardEnabled(standardKey) {
			continue
		}
		score, ok := h.ComputeFrameworkScore(r, fw.slug)
		if !ok {
			continue
		}
		frameworks = append(frameworks, FrameworkSummary{
			Name:         fw.name,
			Satisfied:    score.Fleet.Covered,
			Waived:       score.Fleet.Waived,
			Partial:      score.Fleet.Partial,
			NotSatisfied: score.Fleet.Missing,
			ScorePercent: score.Fleet.CoveragePercent,
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

	// Compute overall compliance score as the unweighted mean across all
	// frameworks that have data. This is the true compliance score (not
	// convergence). Unweighted mean, per the ratified rule — every framework
	// carries equal weight regardless of control count.
	compliancePct := float64(0)
	fwCount := 0
	for _, fw := range frameworks {
		total := fw.Satisfied + fw.Waived + fw.Partial + fw.NotSatisfied
		if total > 0 {
			compliancePct += fw.ScorePercent
			fwCount++
		}
	}
	if fwCount > 0 {
		compliancePct = math.Round(compliancePct/float64(fwCount)*10) / 10
	}

	// 30-day trend from compliance_history table. Average across frameworks per day.
	// If the history query fails we log-and-continue with an empty trend — the
	// rest of the executive report is still useful. Discarding the error here
	// previously hid a test-fixture drift bug for weeks.
	compHistory, err := h.queries.ListComplianceHistoryAllFrameworks(r.Context(), 30*20)
	if err != nil {
		slog.Warn("executive report: compliance_history query failed; trend will be empty",
			"error", err)
		compHistory = nil
	}
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
			Failed:         summary.Failed,
			Unreachable:    summary.Offline,
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

	// Priority 2: Nodes with failed convergence (critical).
	if summary.Failed > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "critical",
			Description: fmt.Sprintf("%d node(s) have failed configuration enforcement. These systems may not be in their desired state.", summary.Failed),
		})
	}

	// Priority 3: Unreachable nodes (warning).
	if summary.Offline > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "warning",
			Description: fmt.Sprintf("%d node(s) have not checked in recently and may be unreachable. Their compliance status cannot be verified.", summary.Offline),
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
  <div class="fleet-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Failed</div></div>
  <div class="fleet-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Unreachable</div></div>
  <div class="fleet-card"><div class="value" style="color:#95a5a6">%d</div><div class="label">Pending Data</div></div>
</div>
`, r.Fleet.Total, r.Fleet.FullyConverged, r.Fleet.ConfigChanges, r.Fleet.Failed, r.Fleet.Unreachable, r.Fleet.PendingData)

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
