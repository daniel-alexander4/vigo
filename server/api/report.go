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
	"sync"
	"time"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	fwreg "git.alexander4.io/vigo/server/frameworks"
	"git.alexander4.io/vigo/ui"
)

// ReportHandler generates compliance reports in HTML and JSON formats.
type ReportHandler struct {
	queries        *db.Queries
	fleetIndex     *fleet.FleetIndex
	configStore    *config.ConfigStore
	version        string
	docsResolver   DocsResolver  // nil-safe; flips documented-scope controls to covered when set
	auditWriterRef auditRecorder // nil-safe; records bundle-download events

	// auditStatusCache memoises audit.Verify results for the hot-path
	// score/report builders. The verify reads + rehashes every
	// audit_entries row — O(N), seconds at fleet scale — so it must
	// never run synchronously on a request or publish. Serve-stale-
	// while-revalidate (see cachedAuditStatus): the cold first access
	// verifies once; afterwards callers always get the cached value and
	// an expired entry triggers a single background re-verify. The
	// on-demand /api/v1/audit/verify REST endpoint (server/api/audit.go)
	// is unaffected — it calls audit.VerifyFull directly for a fresh result.
	auditStatusCache auditStatusCache

	// auditVerifier owns the incremental audit-chain verify the cache above
	// drives (and that the boot/daily re-verify re-anchors). Set by
	// NewReportHandler; nil only in bare struct-literal test handlers, where
	// verifyAuditChain falls back to a stateless full verify.
	auditVerifier *audit.Verifier
}

// auditStatusCacheTTL bounds how stale a cached audit-chain status can
// be before the next access serves the cached value and kicks a
// background re-verify. Five minutes is long enough to amortise across
// a snapshotter loop or a burst of score requests but short enough that
// tampering surfaces within an operationally tight window.
const auditStatusCacheTTL = 5 * time.Minute

type auditStatusCache struct {
	mu         sync.Mutex
	status     AuditChainStatus
	cachedAt   time.Time
	refreshing bool // a background re-verify is in flight (dedup)
}

// store records a fresh verify result. Caller must hold c.mu.
func (c *auditStatusCache) store(v *audit.VerifyResult) {
	c.status = AuditChainStatus{Valid: v.Valid, EntryCount: v.EntryCount, FirstBroken: v.FirstBroken, Error: v.Error}
	c.cachedAt = time.Now()
}

// cachedAuditStatus returns the cached audit-chain status without ever
// blocking the caller on the O(N) verify. Within auditStatusCacheTTL it
// returns the memoised value; otherwise (cold OR expired) it returns the
// current value immediately and kicks a single background re-verify
// (serve-stale-while-revalidate). It never walks the chain on the request
// path — on a large audit_log a cold full walk is tens of seconds (~25 s
// at ~2.8 M entries), which used to hang the first dashboard/compliance/
// executive load after a restart until the async boot warm-up anchored the
// Verifier. On cold, the "current value" is the zero status — the
// not-yet-verified sentinel (see AuditChainStatus): badges render it as
// "verifying…", and it flips to the real verdict once the background verify
// stores. The ctx is unused (the refresh detaches its own bounded context,
// since it outlives the request); kept for call-site symmetry with
// AuditStatus. The /api/v1/audit/verify endpoint remains a fresh full check.
func (h *ReportHandler) cachedAuditStatus(ctx context.Context) AuditChainStatus {
	_ = ctx
	c := &h.auditStatusCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cachedAt.IsZero() && time.Since(c.cachedAt) < auditStatusCacheTTL {
		return c.status
	}
	// Cold (not yet verified) or expired: serve the current value now,
	// refresh in the background. Single-flight via `refreshing`. At boot
	// the kicked refresh does the full walk + anchors the Verifier off the
	// hot path; the daily runAuditReverify may also walk once concurrently
	// (a negligible one-time boot cost — both off the request path).
	if !c.refreshing {
		c.refreshing = true
		daemon.SafeGo("audit-status-refresh", h.refreshAuditStatusAsync)
	}
	return c.status
}

// AuditStatus returns the cached audit-chain verification status
// (serve-stale-while-revalidate). It's the read the UI's ambient
// audit-status badges use, so they share this single cached SoT instead
// of each running an O(N) VerifyFull on page/fragment load. The explicit
// Verify-Chain button and the /api/v1/audit/verify endpoint stay fresh
// full walks.
func (h *ReportHandler) AuditStatus(ctx context.Context) AuditChainStatus {
	return h.cachedAuditStatus(ctx)
}

// refreshAuditStatusAsync re-runs audit.Verify off the hot path and
// updates the cache. The O(N) verify runs WITHOUT the cache lock held —
// so concurrent readers keep getting the stale value instantly — then
// the lock is taken only to store the result and clear the in-flight
// flag. Uses a detached, bounded context since it outlives the request
// that triggered it.
func (h *ReportHandler) refreshAuditStatusAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	v, err := h.verifyAuditChain(ctx)
	c := &h.auditStatusCache
	c.mu.Lock()
	if err == nil {
		c.store(v)
	}
	c.refreshing = false
	c.mu.Unlock()
}

// verifyAuditChain runs the incremental audit-chain verify via the handler's
// Verifier. Bare struct-literal handlers (some tests) have no Verifier and
// fall back to a stateless full verify.
func (h *ReportHandler) verifyAuditChain(ctx context.Context) (*audit.VerifyResult, error) {
	if h.auditVerifier != nil {
		return h.auditVerifier.Verify(ctx)
	}
	return audit.VerifyFull(ctx, h.queries)
}

// AuditVerifier exposes the handler's audit-chain Verifier so the server's
// boot warm-up + daily re-verify can re-anchor the same checkpoint.
func (h *ReportHandler) AuditVerifier() *audit.Verifier { return h.auditVerifier }

// DocsResolver answers questions about operator-uploaded compliance
// documentation for a framework/control pair. Implemented by
// *compliancedocs.Store; kept as an interface in the api package so
// nothing here imports the store package directly (avoids cycle risk).
//
// ActiveDocCountsByControl is the signal that flips a documented-scope
// control out of missing in partition logic — its per-control count is
// stamped onto finding.DocCount once per framework, and partition reads
// DocCount directly (no per-control re-query). ListByControl + Get are
// used by the framework report's "Supporting Documentation" section
// (Phase E) and the audit-bundle export (Phase D) — both need
// per-control metadata, and the bundle additionally needs the
// decrypted plaintext.
type DocsResolver interface {
	CountActiveDocsForControl(ctx context.Context, framework, controlID string) (int, error)
	ActiveDocCountsByControl(ctx context.Context, framework string) (map[string]int, error)
	ListByControl(ctx context.Context, framework, controlID string) ([]db.ComplianceDoc, error)
	Get(ctx context.Context, id string) ([]byte, db.ComplianceDoc, error)
}

// SetDocsResolver wires the compliance-docs store. Optional — when
// nil, partition logic falls back to "documented controls always
// missing" (Phase B behavior).
func (h *ReportHandler) SetDocsResolver(d DocsResolver) { h.docsResolver = d }

// NewReportHandler creates a ReportHandler.
func NewReportHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, configStore *config.ConfigStore, version string) *ReportHandler {
	return &ReportHandler{
		queries:       queries,
		fleetIndex:    fleetIndex,
		configStore:   configStore,
		version:       version,
		auditVerifier: audit.NewVerifier(queries),
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
	mux.HandleFunc("GET /api/v1/report/nydfscyber", h.handleNYDFSCyberReport)
	mux.HandleFunc("GET /api/v1/report/nydfscyber.html", h.handleNYDFSCyberReportHTML)
	h.registerAuditBundleRoute(mux)
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
	// builders is the per-framework control-catalog dispatch — the
	// irreducible behavior surface. The framework's display metadata
	// (title, short name) comes from the registry (server/frameworks), the
	// single source of truth. TestEveryStandardSlugHasABuilder guards that
	// every registry slug has an entry here.
	builders := map[string]func() []ControlFinding{
		"compliance":      h.buildHIPAAFindings,
		"hitrust":         h.buildHITRUSTFindings,
		"soc2":            h.buildSOC2Findings,
		"pci":             h.buildPCIDSSFindings,
		"nist":            h.buildNIST80053Findings,
		"iso27001":        h.buildISO27001Findings,
		"gdpr":            h.buildGDPRFindings,
		"nerccip":         h.buildNERCCIPFindings,
		"cyberessentials": h.buildCyberEssentialsPlusFindings,
		"iec62443":        h.buildIEC62443Findings,
		"sox":             h.buildSOXFindings,
		"finra":           h.buildFINRAFindings,
		"mifid2":          h.buildMiFID2Findings,
		"ccss":            h.buildCCSSFindings,
		"fda21cfr11":      h.buildFDA21CFR11Findings,
		"gamingukgc":      h.buildGamingUKGCFindings,
		"gamingnevada":    h.buildGamingNevadaFindings,
		"gamingmga":       h.buildGamingMGAFindings,
		"ciswindows":      h.buildCISWindowsFindings,
		"cisubuntu":       h.buildCISUbuntuFindings,
		"cisrhel":         h.buildCISRHELFindings,
		"nydfscyber":      h.buildNYDFSCyberFindings,
	}
	build, ok := builders[slug]
	if !ok {
		return frameworkDef{}, false
	}
	meta := fwreg.BySlug[slug]
	return frameworkDef{meta.ReportTitle, meta.ShortName, build}, true
}

// connwaerCapabilityByControl maps (framework slug, control ID) →
// Connwaer capability name. When a control appears here and any envoy
// reports the matching `connwaer.<capability>.status` trait, the
// finding's Status is escalated to "satisfied" or "partial" via
// connwaerControlStatus before waivers run. Module-tag coverage still
// takes priority in partitionEnvoyFindings — this map only fires when a
// control would otherwise be missing.
//
// Mappings derive from connwaer/src/capability/*.rs `controls()`
// declarations, adjusted to the framework builder's actual control IDs
// (some Connwaer-declared IDs predate PCI DSS v4.0 / NIST 800-53 Rev 5
// numbering; this map carries the v4.0 / Rev 5 forms used here).
var connwaerCapabilityByControl = map[string]map[string]string{
	"compliance": { // HIPAA
		"164.312(a)(2)(iii)": "session_timeout",
		"164.312(a)(2)(iv)":  "encryption_at_rest",
	},
	"pci": {
		"3.5.1":  "encryption_at_rest",
		"8.2.8":  "session_timeout",
		"8.3.6":  "password_policy",
		"1.4.1":  "network_segmentation",
		"11.5.2": "integrity_monitoring",
		"10.6.1": "time_sync",
		"10.4.1": "log_review",
	},
	"nist": {
		"AC-4": "network_segmentation",
		"AC-6": "privilege_escalation",
		"AU-8": "time_sync",
		"SI-7": "integrity_monitoring",
		"IA-5": "password_policy",
	},
	"ccss": {
		"CCSS-9.1": "hsm_lifecycle",
	},
	"cyberessentials": {
		"CE-MW-4": "app_allowlisting",
		"CE-MW-5": "sandboxing",
	},
	"finra": {
		"BCP-1":      "backup_restore",
		"BCP-2":      "backup_restore",
		"BCP-3":      "bcp_test",
		"SEC-17a4-1": "worm_verify",
		"SEC-17a4-2": "worm_verify",
	},
	"mifid2": {
		"RTS7-Art11-1": "capacity_monitor",
		"RTS7-Art11-2": "capacity_monitor",
		"RTS7-Art14-1": "kill_switch",
	},
	"gamingmga": {
		"MGA-RGR-5.2": "bcp_test",
	},
	"gamingukgc": {
		"UKGC-RTS-5B": "bcp_test",
	},
}

// applyConnwaerEscalations walks findings for the given framework and
// overrides Status + Evidence on any control that has a Connwaer
// capability mapping with a fleet status worth reporting. Runs before
// waivers so a "satisfied"/"partial" Connwaer escalation is preserved
// through the waiver pass; "not_satisfied" is left as-is so Vigo's
// upsell text doesn't drown out genuine "missing" rendering downstream.
func (h *ReportHandler) applyConnwaerEscalations(slug string, findings []ControlFinding) {
	caps, ok := connwaerCapabilityByControl[slug]
	if !ok {
		return
	}
	for i := range findings {
		capability, mapped := caps[findings[i].ControlID]
		if !mapped {
			continue
		}
		status, evidence := h.connwaerControlStatus(capability, findings[i].Title)
		if status == "not_satisfied" {
			continue
		}
		findings[i].Status = status
		findings[i].Evidence = evidence
	}
}

// buildFrameworkFindings constructs the finding set for a framework,
// applies Connwaer trait-driven escalations, then applies fleet-wide
// waivers in place. Waivers come from the single config source —
// envoys-tree waivers.vgo (via ConfigStore). A waiver promotes a
// not-satisfied finding to "waived" but never overrides a "satisfied"
// status (already-passing controls keep passing; the waiver badge still
// surfaces in the evidence string).
func (h *ReportHandler) buildFrameworkFindings(ctx context.Context, slug string, fw frameworkDef) []ControlFinding {
	_ = ctx // waivers now come from ConfigStore (in-memory); no DB read here
	findings := fw.controls()
	h.applyConnwaerEscalations(slug, findings)
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
//  4. Documented-scope coverage — when the control's Scope is
//     ScopeDocumented and the operator has uploaded at least one
//     document attached to (framework, control_id), the control flips
//     to covered. Without an attached doc, documented-scope controls
//     fall through to missing. This is the only path documented
//     controls take to covered: modules don't apply. Doc presence is
//     read off finding.DocCount, stamped once per framework from the
//     batch ActiveDocCountsByControl query in computeFrameworkScore —
//     no per-control re-query here.
//  5. Partial — Status == "partial" (framework-specific half-credit
//     or partial Connwaer fleet coverage).
//  6. Else missing.
//
// Covered + Waived + Partial + Missing = len(findings).
func partitionEnvoyFindings(findings []ControlFinding, coverage *config.StandardCoverage) (covered, waived, partial, missing int) {
	coveredByConfigcrates := make(map[string]bool)
	if coverage != nil {
		for _, cc := range coverage.CoveredControls {
			coveredByConfigcrates[cc.ControlID] = true
		}
	}
	for _, f := range findings {
		switch {
		case coveredByConfigcrates[f.ControlID]:
			covered++
		case f.Status == "satisfied":
			// Reached only via Connwaer trait-driven escalation today.
			// Framework-builder findings now ship as "not_satisfied" and
			// flip via module coverage above or waivers below.
			covered++
		case f.Status == "waived":
			waived++
		case f.Scope == ScopeDocumented && f.DocCount > 0:
			covered++
		case f.Status == "partial":
			partial++
		default:
			missing++
		}
	}
	return
}

// partitionFindingsForEnvoy is the canonical per-envoy partition entry
// point. It resolves the envoy's module coverage from the config store
// and delegates to partitionEnvoyFindings. Documented-scope coverage is
// read off each finding's DocCount (stamped in computeFrameworkScore),
// so no docs lookup is threaded through here.
func (h *ReportHandler) partitionFindingsForEnvoy(slug, hostname string, findings []ControlFinding) (covered, waived, partial, missing int) {
	var coverage *config.StandardCoverage
	if standardKey, keyOK := config.StandardKeyForSlug[slug]; keyOK {
		coverage = h.configStore.EnvoyStandardCoverage(hostname, standardKey)
	}
	return partitionEnvoyFindings(findings, coverage)
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
	covered, waived, partial, missing := h.partitionFindingsForEnvoy(slug, hostname, score.Controls)
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

	standardKey := config.StandardKeyForSlug[slug]
	var coverage *config.StandardCoverage
	if standardKey != "" {
		coverage = h.configStore.EnvoyStandardCoverage(hostname, standardKey)
	}
	coveredByConfigcrates := make(map[string][]string)
	if coverage != nil {
		for _, cc := range coverage.CoveredControls {
			coveredByConfigcrates[cc.ControlID] = cc.Configcrates
		}
	}

	rows := make([]ui.ControlStatusRow, 0, len(score.Controls))
	for _, f := range score.Controls {
		row := ui.ControlStatusRow{
			ControlID: f.ControlID,
			Title:     f.Title,
			Scope:     f.Scope,
			Evidence:  f.Evidence,
		}
		switch {
		case len(coveredByConfigcrates[f.ControlID]) > 0:
			row.Status = "covered"
			row.Configcrates = coveredByConfigcrates[f.ControlID]
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
			// For documented-scope controls, the recommended remediation is
			// to upload a supporting document (Phase C). Module tags don't
			// apply. For enforced/attested-scope controls, point operators
			// at the modules that would cover this control.
			if standardKey != "" && f.Scope != ScopeDocumented {
				row.RecommendedConfigcrates = h.configStore.RecommendConfigcratesForControl(standardKey, f.ControlID)
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
	return h.computeFrameworkScore(ctx, h.cachedAuditStatus(ctx), slug)
}

func (h *ReportHandler) computeFrameworkScore(ctx context.Context, as AuditChainStatus, slug string) (*FrameworkScore, bool) {
	fw, ok := h.frameworkDef(as, slug)
	if !ok {
		return nil, false
	}
	findings := h.buildFrameworkFindings(ctx, slug, fw)
	total := len(findings)

	standardKey, hasKey := config.StandardKeyForSlug[slug]

	// Stamp per-control attached-doc counts so the framework page can
	// show a "📎 N" badge next to controls with operator-uploaded
	// evidence. One DB call per framework — controls without docs
	// stay at zero and add no cost.
	if h.docsResolver != nil {
		docKey := slug
		if hasKey {
			docKey = standardKey
		}
		if counts, err := h.docsResolver.ActiveDocCountsByControl(ctx, docKey); err == nil {
			for i := range findings {
				if n, ok := counts[findings[i].ControlID]; ok {
					findings[i].DocCount = n
				}
			}
		}
	}

	score := &FrameworkScore{
		Slug:     slug,
		Name:     fw.name,
		Title:    fw.title,
		Total:    total,
		PerEnvoy: make(map[string]ui.EnvoyFrameworkStatus),
		Controls: findings,
	}

	var activeHostnames []string
	if h.fleetIndex != nil {
		for _, e := range h.fleetIndex.AllEnvoys() {
			if !e.Revoked {
				activeHostnames = append(activeHostnames, e.Hostname)
			}
		}
	}

	for _, hostname := range activeHostnames {
		covered, waived, partial, missing := h.partitionFindingsForEnvoy(slug, hostname, findings)
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
		type envoyConfigcrates struct {
			hostname     string
			configcrates map[string]bool
		}
		var activeEnvoys []envoyConfigcrates

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
					for _, m := range cc.Configcrates {
						mods[m] = true
					}
				}
			}
			activeEnvoys = append(activeEnvoys, envoyConfigcrates{hostname: node.Hostname, configcrates: mods})
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
				configcratesForControl := controlIndex[cid]
				if len(configcratesForControl) == 0 {
					continue
				}
				covered := 0
				for _, em := range activeEnvoys {
					for _, mod := range configcratesForControl {
						if em.configcrates[mod] {
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

	// Supporting documentation — populate the report's Documents slice
	// from the docs resolver so the HTML/PDF report and the audit
	// bundle share one source of truth. Best-effort: per-control
	// errors are skipped (the doc store is optional and may be 503
	// under non-local secrets backends).
	if h.docsResolver != nil {
		standardKey, ok := config.StandardKeyForSlug[slug]
		if !ok {
			standardKey = slug
		}
		for _, c := range score.Controls {
			if c.Scope != ScopeDocumented {
				continue
			}
			rows, err := h.docsResolver.ListByControl(r.Context(), standardKey, c.ControlID)
			if err != nil {
				continue
			}
			for _, doc := range rows {
				report.Documents = append(report.Documents, ReportDocAttachment{
					ControlID:  c.ControlID,
					DocID:      doc.ID,
					Filename:   doc.Filename,
					DocType:    doc.DocType,
					MimeType:   doc.MimeType,
					SizeBytes:  doc.SizeBytes,
					SHA256:     doc.Sha256,
					UploadedBy: doc.UploadedBy,
					UploadedAt: doc.UploadedAt.Format(time.RFC3339),
				})
			}
		}
	}

	return report, nil
}

// ComplianceReport is the JSON format for a compliance report.
type ComplianceReport struct {
	// Locale is the resolved per-request locale, stamped by the ui report
	// builder closures so the compliance-report-content / evidence-coverage
	// fragments (which receive this struct as their template dot) can read
	// $.Locale. Not serialized — it's a render-time concern only.
	Locale        string                  `json:"-"`
	Title         string                  `json:"title"`
	FrameworkName string                  `json:"framework_name,omitempty"`
	Slug          string                  `json:"slug,omitempty"`
	GeneratedAt   string                  `json:"generated_at"`
	GeneratedBy   string                  `json:"generated_by"`
	Version       string                  `json:"version"`
	Summary       ComplianceReportSummary `json:"summary"`
	Controls      []ControlFinding        `json:"controls"`
	Fleet         []FleetEnvoyStatus      `json:"fleet"`
	AuditChain    AuditChainStatus        `json:"audit_chain"`
	// Documents lists every operator-uploaded artifact attached to
	// any control in this framework. Renders as a "Supporting
	// Documentation" section in the HTML/PDF report (Phase E) and
	// is the source of truth for the docs/ directory in audit
	// bundles (Phase D).
	Documents []ReportDocAttachment `json:"documents,omitempty"`
}

// ReportDocAttachment is a single doc-to-control row surfaced in the
// PDF/HTML report and the audit bundle.
type ReportDocAttachment struct {
	ControlID  string `json:"control_id"`
	DocID      string `json:"doc_id"`
	Filename   string `json:"filename"`
	DocType    string `json:"doc_type,omitempty"`
	MimeType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	UploadedBy string `json:"uploaded_by"`
	UploadedAt string `json:"uploaded_at"`
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
	// Failure axis (sums to TotalNodes):
	Converged int `json:"converged"`
	Degraded  int `json:"degraded"`
	Failed    int `json:"failed"`
	NoData    int `json:"no_data"`
	// Drift axis (overlapping; Diverged ⊆ Changed):
	Changed  int `json:"changed"`
	Diverged int `json:"diverged"`
	// Reachability:
	Offline int `json:"offline"`
}

// ControlFinding maps a compliance control to its implementation status.
type ControlFinding struct {
	ControlID    string  `json:"control_id"`
	Title        string  `json:"title"`
	Status       string  `json:"status"` // "satisfied", "partial", "not_satisfied", "waived"
	Scope        string  `json:"scope"`  // see ScopeEnforced/Attested/Documented/NotApplicable
	Evidence     string  `json:"evidence"`
	FleetPercent float64 `json:"fleet_percent"` // percentage of fleet with modules covering this control
	FleetCovered int     `json:"fleet_covered"` // envoys with coverage
	FleetTotal   int     `json:"fleet_total"`   // total active envoys
	DocCount     int     `json:"doc_count"`     // number of operator-uploaded docs attached to this control (active, non-deleted)
}

// Control scope classifies whether a framework control is something
// Vigo can enforce/measure on envoys, or whether the operator must
// satisfy it through documentation/process Vigo retains as evidence.
//
//   - ScopeEnforced — Vigo modules apply the technical control
//     (firewall rules, package state, file mode, sysctl, user mgmt,
//     SSH config, AppArmor profiles, etc.). Coverage flips when the
//     envoy's modules carry the matching `compliance:` tag.
//   - ScopeAttested — Vigo collects evidence but doesn't enforce —
//     drift detection, security_scan trait, audit chain integrity,
//     Connwaer fleet checks. Coverage requires the relevant signal
//     to be present and healthy.
//   - ScopeDocumented — operator satisfies via uploaded artifact
//     (BAA, IR plan, risk assessment, training records). Coverage
//     requires at least one valid attached doc (Phase C).
//   - ScopeNotApplicable — control is out of deployment scope (e.g.,
//     HIPAA "Isolating Health Care Clearinghouse Functions" for a
//     non-clearinghouse). Excluded from the framework denominator.
const (
	ScopeEnforced      = "enforced"
	ScopeAttested      = "attested"
	ScopeDocumented    = "documented"
	ScopeNotApplicable = "not_applicable"
)

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
	WaivedCount int `json:"waived_count,omitempty"`
	// PartialCount is the number of framework controls with Status ==
	// "partial" for this envoy — framework-specific half-credit findings
	// (e.g., evidence present but insufficient for full coverage). Kept
	// as its own column so the UI can discriminate partial from missing.
	// Covered + Waived + Partial + Missing = TotalControls.
	PartialCount  int `json:"partial_count,omitempty"`
	TotalControls int `json:"total_controls,omitempty"`
	MissingCount  int `json:"missing_count,omitempty"`
}

// AuditChainStatus is the integrity status of the audit chain.
// AuditChainStatus is the cached audit-chain verdict. Three states, encoded
// in the existing fields (no separate flag):
//   - verified-valid:   Valid=true
//   - verified-broken:  Valid=false AND Error set (FirstBroken points at it)
//   - not-yet-verified: the zero value — Valid=false AND Error=="" (the cold
//     sentinel cachedAuditStatus serves before the first background verify
//     stores). A real broken chain ALWAYS carries Error and an empty log is
//     Valid=true, so Valid=false+empty-Error is unambiguously "verifying".
//     Badge renderers MUST treat this as "verifying…", not "broken".
type AuditChainStatus struct {
	Valid       bool   `json:"valid"`
	EntryCount  int64  `json:"entry_count"`
	FirstBroken int64  `json:"first_broken,omitempty"`
	Error       string `json:"error,omitempty"`
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

	// Audit chain verification — cached at the report-handler layer to
	// avoid re-reading every audit_entries row per render. See
	// cachedAuditStatus on ReportHandler.
	auditStatus := h.cachedAuditStatus(r.Context())

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
			Degraded:           int(summary.Degraded),
			Failed:             int(summary.Failed),
			NoData:             int(summary.NoData),
			Changed:            int(summary.Changed),
			Diverged:           int(summary.Diverged),
			Offline:            int(summary.Offline),
			ConvergencePercent: pct,
		},
		Controls:   controls,
		Fleet:      nodes,
		AuditChain: auditStatus,
	}, nil
}

// catalogControl is shorthand for a {ID, Title, Scope} triple used by
// every framework's control-catalog builder. Status defaults to
// "not_satisfied"; scope is the caller's responsibility (see
// ScopeEnforced / ScopeAttested / ScopeDocumented / ScopeNotApplicable).
//
// The partition rule treats unmapped controls as missing — coverage
// flips to "covered" when an envoy's modules carry the matching
// `compliance:` tag (enforced/attested), or when the documented
// control has at least one attached evidence doc (Phase C),
// "waived" when a waiver applies, or "partial" when a Connwaer
// escalation lands at less than full fleet coverage. Evidence is
// left empty: the per-control row in the report renders the
// recommended modules for missing controls and the waiver detail for
// waived ones.
func catalogControl(id, title, scope string) ControlFinding {
	return ControlFinding{ControlID: id, Title: title, Status: "not_satisfied", Scope: scope}
}

// scopedCatalog converts a list of {id, title} pairs into a catalog
// slice where every entry shares the given scope. Most framework
// builders organize their entries into a small number of scope-uniform
// sections (e.g., all CIS-Ubuntu controls are enforced; HIPAA
// §164.312 Technical Safeguards are mostly enforced; HIPAA §164.308
// Administrative Safeguards are mostly documented), so the scope-per-
// section idiom keeps builders compact.
func scopedCatalog(scope string, entries [][2]string) []ControlFinding {
	out := make([]ControlFinding, 0, len(entries))
	for _, e := range entries {
		out = append(out, catalogControl(e[0], e[1], scope))
	}
	return out
}

// concatCatalogs flattens several scoped sections into one
// ControlFinding slice. Keeps builders that need mixed scopes (HIPAA,
// SOC 2, NIST, ISO 27001) clean: build each section with
// scopedCatalog, then concat.
func concatCatalogs(sections ...[]ControlFinding) []ControlFinding {
	total := 0
	for _, s := range sections {
		total += len(s)
	}
	out := make([]ControlFinding, 0, total)
	for _, s := range sections {
		out = append(out, s...)
	}
	return out
}

// catalog is the legacy single-section helper retained for tests and
// for any framework that's truly scope-uniform. It defaults the
// scope to ScopeEnforced — fine for CIS benchmarks (technical
// hardening), but the wrong default for HIPAA/SOC 2/NIST. Prefer
// scopedCatalog/concatCatalogs in new code.
func catalog(entries [][2]string) []ControlFinding {
	return scopedCatalog(ScopeEnforced, entries)
}

// buildHIPAAFindings returns the HIPAA Security Rule control catalog
// (45 CFR §164.302-318), enumerated in source order. Status of every
// control reflects envoy state only: covered iff the envoy's modules
// carry the matching `compliance: { hipaa: [...] }` tag, waived iff a
// waiver applies, partial iff a Connwaer escalation fires, otherwise
// missing. Vigo's own architectural posture (server-side mTLS, RBAC,
// audit chain, etc.) lives separately in server/compliance.AppControls
// and never enters this catalog.
//
// Scope assignment: §164.308 Administrative + §164.314 Organizational
// + §164.316 Policies/Procedures are predominantly documented (policy
// artifacts the operator produces); §164.310 Physical is a mix
// (mostly documented, a few enforced); §164.312 Technical is
// predominantly enforced (Vigo modules apply the technical control).
// A handful of cross-section overrides handle outliers — e.g.,
// §164.308(a)(5)(ii)(B) Protection from Malicious Software is
// enforced (AV/EDR module) even though the surrounding subsection is
// documented.
func (h *ReportHandler) buildHIPAAFindings() []ControlFinding {
	return concatCatalogs(
		// §164.308 — Administrative Safeguards (mostly documented)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"164.308(a)(1)(i)", "Security Management Process"},
			{"164.308(a)(1)(ii)(A)", "Risk Analysis"},
			{"164.308(a)(1)(ii)(B)", "Risk Management"},
			{"164.308(a)(1)(ii)(C)", "Sanction Policy"},
			{"164.308(a)(2)", "Assigned Security Responsibility"},
			{"164.308(a)(3)(i)", "Workforce Security"},
			{"164.308(a)(3)(ii)(A)", "Authorization and/or Supervision"},
			{"164.308(a)(3)(ii)(B)", "Workforce Clearance Procedure"},
			{"164.308(a)(3)(ii)(C)", "Termination Procedures"},
			{"164.308(a)(4)(i)", "Information Access Management"},
			{"164.308(a)(4)(ii)(A)", "Isolating Health Care Clearinghouse Functions"},
			{"164.308(a)(4)(ii)(B)", "Access Authorization"},
			{"164.308(a)(4)(ii)(C)", "Access Establishment and Modification"},
			{"164.308(a)(5)(i)", "Security Awareness and Training"},
			{"164.308(a)(5)(ii)(A)", "Security Reminders"},
			{"164.308(a)(6)(i)", "Security Incident Procedures"},
			{"164.308(a)(6)(ii)", "Response and Reporting"},
			{"164.308(a)(7)(i)", "Contingency Plan"},
			{"164.308(a)(7)(ii)(B)", "Disaster Recovery Plan"},
			{"164.308(a)(7)(ii)(C)", "Emergency Mode Operation Plan"},
			{"164.308(a)(7)(ii)(D)", "Testing and Revision Procedures"},
			{"164.308(a)(7)(ii)(E)", "Applications and Data Criticality Analysis"},
			{"164.308(a)(8)", "Evaluation"},
			{"164.308(b)(1)", "Business Associate Contracts and Other Arrangements"},
			{"164.308(b)(4)", "Written Contract or Other Arrangement"},
		}),
		// §164.308 — Administrative outliers Vigo can enforce
		scopedCatalog(ScopeEnforced, [][2]string{
			{"164.308(a)(5)(ii)(B)", "Protection from Malicious Software"},
			{"164.308(a)(5)(ii)(D)", "Password Management"},
			{"164.308(a)(7)(ii)(A)", "Data Backup Plan"},
		}),
		// §164.308 — Administrative outliers Vigo can attest to
		scopedCatalog(ScopeAttested, [][2]string{
			{"164.308(a)(1)(ii)(D)", "Information System Activity Review"},
			{"164.308(a)(5)(ii)(C)", "Log-in Monitoring"},
		}),
		// §164.310 — Physical Safeguards (mostly documented)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"164.310(a)(1)", "Facility Access Controls"},
			{"164.310(a)(2)(i)", "Contingency Operations"},
			{"164.310(a)(2)(ii)", "Facility Security Plan"},
			{"164.310(a)(2)(iii)", "Access Control and Validation Procedures"},
			{"164.310(a)(2)(iv)", "Maintenance Records"},
			{"164.310(b)", "Workstation Use"},
			{"164.310(c)", "Workstation Security"},
			{"164.310(d)(1)", "Device and Media Controls"},
			{"164.310(d)(2)(iii)", "Accountability"},
			{"164.310(d)(2)(iv)", "Data Backup and Storage"},
		}),
		// §164.310 — Physical outliers Vigo can enforce (Sandgorgon decom)
		scopedCatalog(ScopeEnforced, [][2]string{
			{"164.310(d)(2)(i)", "Disposal"},
			{"164.310(d)(2)(ii)", "Media Re-use"},
		}),
		// §164.312 — Technical Safeguards (mostly enforced)
		scopedCatalog(ScopeEnforced, [][2]string{
			{"164.312(a)(1)", "Access Control"},
			{"164.312(a)(2)(i)", "Unique User Identification"},
			{"164.312(a)(2)(ii)", "Emergency Access Procedure"},
			{"164.312(a)(2)(iii)", "Automatic Logoff"},
			{"164.312(a)(2)(iv)", "Encryption and Decryption"},
			{"164.312(c)(1)", "Integrity"},
			{"164.312(c)(2)", "Mechanism to Authenticate Electronic Protected Health Information"},
			{"164.312(d)", "Person or Entity Authentication"},
			{"164.312(e)(1)", "Transmission Security"},
			{"164.312(e)(2)(i)", "Integrity Controls"},
			{"164.312(e)(2)(ii)", "Encryption (Transmission)"},
		}),
		// §164.312 — Technical outliers Vigo can attest to
		scopedCatalog(ScopeAttested, [][2]string{
			{"164.312(b)", "Audit Controls"},
		}),
		// §164.314 — Organizational Requirements (all documented — contracts)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"164.314(a)(1)", "Business Associate Contracts or Other Arrangements"},
			{"164.314(a)(2)(i)", "Business Associate Contracts"},
			{"164.314(a)(2)(ii)", "Other Arrangements"},
			{"164.314(a)(2)(iii)", "Business Associate Contracts with Subcontractors"},
			{"164.314(b)(1)", "Requirements for Group Health Plans"},
			{"164.314(b)(2)(i)", "Group Health Plan — Safeguards"},
			{"164.314(b)(2)(ii)", "Group Health Plan — Adequate Separation"},
			{"164.314(b)(2)(iii)", "Group Health Plan — Subcontractor Agreements"},
			{"164.314(b)(2)(iv)", "Group Health Plan — Reporting"},
		}),
		// §164.316 — Policies, Procedures, Documentation (all documented)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"164.316(a)", "Policies and Procedures"},
			{"164.316(b)(1)", "Documentation"},
			{"164.316(b)(2)(i)", "Time Limit"},
			{"164.316(b)(2)(ii)", "Availability"},
			{"164.316(b)(2)(iii)", "Updates"},
		}),
	)
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
  .badge-errors, .badge-diverged, .badge-offline, .badge-no-data { background: #f8d7da; color: #721c24; }
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
  <div class="summary-card"><div class="value" style="color:#f39c12">%d</div><div class="label">Degraded</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Failed</div></div>
  <div class="summary-card"><div class="value" style="color:#e74c3c">%d</div><div class="label">Diverged</div></div>
  <div class="summary-card"><div class="value" style="color:#6c757d">%d</div><div class="label">Offline</div></div>
</div>
`, gaugeColor, r.Summary.ConvergencePercent, r.Summary.TotalNodes, r.Summary.Converged, r.Summary.Degraded, r.Summary.Failed, r.Summary.Diverged, r.Summary.Offline)

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

	// Supporting documentation — operator-uploaded artifacts attached
	// to documented-scope controls. When the report is opened
	// standalone, filenames link to /api/v1/compliance/docs/<id>/download.
	// When the report is bundled (Phase D), the audit-bundle assembler
	// also copies each doc to docs/<id>__<filename> so the PDF
	// references and the on-disk files line up.
	if len(r.Documents) > 0 {
		fmt.Fprint(w, `<h2>Supporting Documentation</h2>
<table>
<thead><tr><th>Control</th><th>Filename</th><th>Type</th><th>MIME</th><th>Size</th><th>SHA-256</th><th>Uploaded</th></tr></thead>
<tbody>
`)
		for _, d := range r.Documents {
			short := d.SHA256
			if len(short) > 12 {
				short = short[:12]
			}
			href := "/api/v1/compliance/docs/" + d.DocID + "/download"
			fmt.Fprintf(w, `<tr><td>%s</td><td><a href="%s">%s</a></td><td>%s</td><td>%s</td><td>%d</td><td><span title="%s">%s</span></td><td>%s</td></tr>
`, html.EscapeString(d.ControlID), html.EscapeString(href), html.EscapeString(d.Filename), html.EscapeString(d.DocType), html.EscapeString(d.MimeType), d.SizeBytes, html.EscapeString(d.SHA256), html.EscapeString(short), html.EscapeString(d.UploadedAt))
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
		// (Endpoint protection lives under 09.i; HITRUST CSF has no
		// category 16 — the old "16.a" entry was an invalid reference.)
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
					writeError(w, http.StatusNotFound, "This compliance framework is not enabled.")
				} else {
					writeError(w, http.StatusNotFound, "framework not enabled")
				}
				return
			}
		}
	}

	report, err := h.BuildReport(r, title, controls)
	if err != nil {
		if asHTML {
			writeError(w, http.StatusInternalServerError, "failed to build report: "+err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "failed to build report: "+err.Error())
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

// auditStatusFromRequest returns the cached audit-chain status. Reads
// from the per-handler 5-minute TTL cache — see cachedAuditStatus on
// ReportHandler.
func (h *ReportHandler) auditStatusFromRequest(r *http.Request) AuditChainStatus {
	return h.cachedAuditStatus(r.Context())
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

// buildSOC2Findings returns the SOC 2 Trust Services Criteria catalog
// (2017 with 2022 revisions). Common Criteria CC1-CC9 plus the
// optional category criteria A1 (Availability), C1 (Confidentiality),
// PI1 (Processing Integrity), and P1-P8 (Privacy). AICPA-licensed: IDs
// + short titles only.
//
// Scope assignment: CC1-CC3, CC9, most P controls, and PI are
// predominantly documented (governance, policy, application-level
// data quality). CC6 access controls and CC8 change management are
// predominantly enforced via Vigo modules. CC4 monitoring and CC7
// detection are attested via Vigo's drift detection + audit chain.
func (h *ReportHandler) buildSOC2Findings() []ControlFinding {
	return concatCatalogs(
		// CC1-CC3, CC9 — Control Environment / Communication / Risk / Risk Mitigation
		scopedCatalog(ScopeDocumented, [][2]string{
			{"CC1.1", "COSO Principle 1: Integrity and Ethics"},
			{"CC1.2", "COSO Principle 2: Board Oversight"},
			{"CC1.3", "COSO Principle 3: Management Structure"},
			{"CC1.4", "COSO Principle 4: Commitment to Competence"},
			{"CC1.5", "COSO Principle 5: Accountability"},
			{"CC2.1", "Internal Information Quality"},
			{"CC2.2", "Internal Communication of Objectives and Responsibilities"},
			{"CC2.3", "External Communication"},
			{"CC3.1", "Specifies Suitable Objectives"},
			{"CC3.2", "Risk Identification and Analysis"},
			{"CC3.3", "Fraud Risk Assessment"},
			{"CC3.4", "Identifies and Assesses Changes"},
			{"CC9.1", "Risk Mitigation Activities"},
			{"CC9.2", "Vendor and Business Partner Risk Management"},
		}),
		// CC4 — Monitoring (Vigo's drift detection + audit chain)
		scopedCatalog(ScopeAttested, [][2]string{
			{"CC4.1", "Ongoing and Separate Evaluations"},
			{"CC4.2", "Communication of Deficiencies"},
		}),
		// CC5 — Control Activities (mixed)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"CC5.1", "Selection and Development of Control Activities"},
			{"CC5.3", "Deployment of Policies and Procedures"},
		}),
		scopedCatalog(ScopeEnforced, [][2]string{
			{"CC5.2", "Technology General Controls"},
		}),
		// CC6 — Logical and Physical Access Controls
		scopedCatalog(ScopeEnforced, [][2]string{
			{"CC6.1", "Logical Access Security Software"},
			{"CC6.2", "User Registration and Authorization"},
			{"CC6.3", "Role-Based Access and Least Privilege"},
			{"CC6.5", "Disposal of Physical Devices"},
			{"CC6.6", "Restriction of Access from Outside System Boundaries"},
			{"CC6.7", "Restriction of Information in Transit and at Rest"},
			{"CC6.8", "Prevention of Unauthorized Software"},
		}),
		scopedCatalog(ScopeDocumented, [][2]string{
			{"CC6.4", "Physical Access to Facilities"},
		}),
		// CC7 — System Operations
		scopedCatalog(ScopeAttested, [][2]string{
			{"CC7.1", "Detection of Configuration Changes and Vulnerabilities"},
			{"CC7.2", "Monitoring System Components for Anomalies"},
		}),
		scopedCatalog(ScopeDocumented, [][2]string{
			{"CC7.3", "Evaluation of Security Events"},
			{"CC7.4", "Response to Identified Security Incidents"},
			{"CC7.5", "Recovery from Identified Security Incidents"},
		}),
		// CC8 — Change Management (vigocli config publish workflow)
		scopedCatalog(ScopeEnforced, [][2]string{
			{"CC8.1", "Change Management Process"},
		}),
		// A — Availability
		scopedCatalog(ScopeEnforced, [][2]string{
			{"A1.1", "Capacity Management"},
		}),
		scopedCatalog(ScopeDocumented, [][2]string{
			{"A1.2", "Environmental Protections, Backup, and Recovery Infrastructure"},
			{"A1.3", "Recovery Plan Testing"},
		}),
		// Scope (compliance-control-scope-policy.md): Security (CC1-CC9) is
		// mandatory and Availability (A1) is the common addition — both kept.
		// Confidentiality (C1), Processing Integrity (PI1), and Privacy (P*)
		// are the rarely-elected optional TSC categories and are excluded;
		// re-add the relevant category here if a customer's report elects it.
	)
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

// buildPCIDSSFindings returns the PCI DSS v4.0 control catalog —
// requirements 1-12 with their full sub-requirement enumeration. PCI
// SSC-licensed: only IDs and short titles are reproduced.
//
// Scope assignment: every "X.1.1 / X.1.2 — security policies and
// operational procedures / roles and responsibilities documented"
// pattern is documented. Req 9 (physical access) is entirely
// documented. Req 12 (governance/policies) is entirely documented.
// Req 11 (test security) is largely attested. The technical
// requirements (1-8 sub-controls, Req 10 audit logging, etc.) are
// mostly enforced with a small documented tail.
func (h *ReportHandler) buildPCIDSSFindings() []ControlFinding {
	return concatCatalogs(
		// Req 1-8, 10 — every "X.1.1 / X.1.2 — security policies and
		// operational procedures / roles documented" entry is a written
		// artifact the operator produces.
		scopedCatalog(ScopeDocumented, [][2]string{
			{"1.1.1", "All security policies and operational procedures for Requirement 1 are documented"},
			{"1.1.2", "Roles and responsibilities for activities in Requirement 1 are documented"},
			{"2.1.1", "All security policies and operational procedures for Requirement 2 are documented"},
			{"2.1.2", "Roles and responsibilities for activities in Requirement 2 are documented"},
			{"3.1.1", "All security policies and operational procedures for Requirement 3 are documented"},
			{"3.1.2", "Roles and responsibilities for activities in Requirement 3 are documented"},
			{"4.1.1", "All security policies and operational procedures for Requirement 4 are documented"},
			{"4.1.2", "Roles and responsibilities for activities in Requirement 4 are documented"},
			{"5.1.1", "All security policies and operational procedures for Requirement 5 are documented"},
			{"5.1.2", "Roles and responsibilities for activities in Requirement 5 are documented"},
			{"5.4.1", "Processes and mechanisms for protecting against phishing attacks"},
			{"6.1.1", "All security policies and operational procedures for Requirement 6 are documented"},
			{"6.1.2", "Roles and responsibilities for activities in Requirement 6 are documented"},
			{"6.2.1", "Bespoke and custom software is developed securely"},
			{"6.2.2", "Software development personnel receive training in secure software development"},
			{"6.2.3", "Bespoke and custom software is reviewed prior to release"},
			{"6.2.3.1", "Code reviews ensure code is developed per secure coding guidelines"},
			{"6.2.4", "Software engineering techniques used to prevent or mitigate common attacks"},
			{"7.1.1", "All security policies and operational procedures for Requirement 7 are documented"},
			{"7.1.2", "Roles and responsibilities for activities in Requirement 7 are documented"},
			{"8.1.1", "All security policies and operational procedures for Requirement 8 are documented"},
			{"8.1.2", "Roles and responsibilities for activities in Requirement 8 are documented"},
			{"3.6.1", "Procedures defined for protecting cryptographic keys"},
			{"3.6.1.1", "Documented description of cryptographic architecture maintained"},
			{"3.6.1.3", "Access to cryptographic keys is restricted to fewest custodians necessary"},
			{"3.7.1", "Key-management policies and procedures defined for generation"},
			{"3.7.2", "Key-management policies for secure distribution of cryptographic keys"},
			{"3.7.3", "Key-management policies for secure storage of cryptographic keys"},
			{"3.7.4", "Key-management policies for cryptographic key cryptoperiod and key changes"},
			{"3.7.5", "Key-management policies for retirement, replacement, or destruction of keys"},
			{"3.7.6", "Manual cleartext key-management operations require split knowledge and dual control"},
			{"3.7.7", "Key-management policies for prevention of unauthorized substitution of keys"},
			{"3.7.8", "Key custodians formally acknowledge their responsibilities in writing"},
			{"3.7.9", "Where used, additional requirements for service provider key management"},
			{"10.1.1", "All security policies and operational procedures for Requirement 10 are documented"},
			{"10.1.2", "Roles and responsibilities for activities in Requirement 10 are documented"},
			// Req 9 — Restrict physical access to cardholder data
			{"9.1.1", "All security policies and operational procedures for Requirement 9 are documented"},
			{"9.1.2", "Roles and responsibilities for activities in Requirement 9 are documented"},
			{"9.2.1", "Appropriate facility entry controls in place to restrict physical access"},
			{"9.2.1.1", "Individual physical access to sensitive areas is monitored with cameras or access control"},
			{"9.2.2", "Physical and/or logical controls implemented to restrict use of publicly accessible network jacks"},
			{"9.2.3", "Physical access to wireless access points, networking equipment, and gateways restricted"},
			{"9.2.4", "Access to consoles in sensitive areas restricted via locking"},
			{"9.3.1", "Procedures implemented for authorizing and managing physical access of personnel to the CDE"},
			{"9.3.1.1", "Physical access to sensitive areas within the CDE for personnel is controlled"},
			{"9.3.2", "Procedures implemented for authorizing and managing visitor access to the CDE"},
			{"9.3.3", "Visitors to sensitive areas are escorted at all times"},
			{"9.3.4", "Visitor logs maintained for visits to sensitive areas"},
			{"9.4.1", "All media with cardholder data physically secured"},
			{"9.4.1.1", "Offline media backups stored in a secure location"},
			{"9.4.1.2", "Security of offline media backup locations reviewed at least every 12 months"},
			{"9.4.2", "All media with CHD classified per sensitivity"},
			{"9.4.3", "Media with CHD sent outside facility secured"},
			{"9.4.4", "Management approval obtained prior to moving media containing CHD outside facility"},
			{"9.4.5", "Inventory logs of all electronic media with CHD maintained"},
			{"9.4.5.1", "Inventories of electronic media with CHD reviewed at least every 12 months"},
			{"9.4.6", "Hard-copy materials with CHD destroyed when no longer needed"},
			{"9.4.7", "Electronic media with CHD destroyed when no longer needed"},
			{"9.5.1", "POI devices that capture payment card data via direct physical interaction are protected"},
			{"9.5.1.1", "Inventory of POI devices is maintained"},
			{"9.5.1.2", "POI device surfaces are periodically inspected for tampering or unauthorized substitution"},
			{"9.5.1.2.1", "Frequency of POI device inspections defined per risk analysis"},
			{"9.5.1.3", "Training provided for personnel in POI environments to be aware of attempted tampering"},
			// Req 12 — Governance / policies / programs
			{"12.1.1", "Overall information security policy established, published, maintained, and disseminated"},
			{"12.1.2", "Information security policy reviewed at least once every 12 months"},
			{"12.1.3", "Security policy clearly defines information security roles and responsibilities"},
			{"12.1.4", "Responsibility for information security formally assigned to a CISO or other knowledgeable executive"},
			{"12.2.1", "Acceptable use policies for end-user technologies documented"},
			{"12.3.1", "Targeted risk analysis performed for each PCI DSS requirement that provides flexibility"},
			{"12.3.2", "Targeted risk analysis performed for each PCI DSS requirement that is met via the customized approach"},
			{"12.3.3", "Cryptographic cipher suites and protocols in use documented and reviewed annually"},
			{"12.3.4", "Hardware and software technologies in use are reviewed annually"},
			{"12.4.1", "Service providers — executive management establishes responsibility for protection of CHD"},
			{"12.4.2", "Service providers — reviews performed at least once every three months to confirm personnel are performing their tasks"},
			{"12.4.2.1", "Service providers — review documented and signed off by responsible personnel"},
			{"12.5.1", "Inventory of system components in scope for PCI DSS maintained"},
			{"12.5.2", "PCI DSS scope is documented and confirmed at least once every 12 months and upon significant change"},
			{"12.5.2.1", "Service providers — PCI DSS scope confirmation performed at least once every six months"},
			{"12.5.3", "Service providers — significant changes to organizational structure result in a documented review of the impact on PCI DSS scope"},
			{"12.6.1", "Formal security awareness program implemented"},
			{"12.6.2", "Security awareness program reviewed at least every 12 months"},
			{"12.6.3", "Personnel receive security awareness training upon hire and at least every 12 months"},
			{"12.6.3.1", "Security awareness training includes specific awareness of threats relevant to job role"},
			{"12.6.3.2", "Security awareness training includes awareness about acceptable use of end-user technologies"},
			{"12.7.1", "Personnel screened prior to hire to minimize risk of attacks from internal sources"},
			{"12.8.1", "Written agreements with TPSPs maintained"},
			{"12.8.2", "Written agreements with TPSPs include acknowledgement of TPSP responsibility for security of CHD"},
			{"12.8.3", "Established process for engaging TPSPs"},
			{"12.8.4", "Program implemented to monitor TPSPs' PCI DSS compliance status"},
			{"12.8.5", "Information maintained about which PCI DSS requirements are managed by each TPSP"},
			{"12.9.1", "Service providers — TPSPs acknowledge in writing they are responsible for security of CHD"},
			{"12.9.2", "Service providers — support customers' requests for information regarding PCI DSS compliance status"},
			{"12.10.1", "Incident response plan exists and is ready to be activated"},
			{"12.10.2", "At least once every 12 months, IR plan is reviewed, tested, and updated as needed"},
			{"12.10.3", "Specific personnel designated to be available 24/7 to respond to suspected or confirmed security incidents"},
			{"12.10.4", "Personnel responsible for responding to incidents trained appropriately"},
			{"12.10.4.1", "Frequency of periodic training defined per risk analysis"},
			{"12.10.5", "IR plan includes monitoring and responding to alerts from security monitoring systems"},
			{"12.10.6", "IR plan modified and evolved per lessons learned"},
			{"12.10.7", "IR procedures are in place to be initiated upon detection of stored PAN where it is not expected"},
		}),
		// Req 11 — Test security of systems and networks regularly
		// (Vigo can attest via security_scan trait + drift detection;
		// pen-test execution and network IDS deployment are
		// operator-driven testing programs.)
		scopedCatalog(ScopeAttested, [][2]string{
			{"11.1.1", "All security policies and operational procedures for Requirement 11 are documented"},
			{"11.1.2", "Roles and responsibilities for activities in Requirement 11 are documented"},
			{"11.2.1", "Authorized and unauthorized wireless access points managed"},
			{"11.2.2", "Inventory of authorized wireless access points maintained, including documented business justification"},
			{"11.3.1", "Internal vulnerability scans performed at least every three months"},
			{"11.3.1.1", "All other applicable vulnerabilities not ranked as high-risk or critical are managed"},
			{"11.3.1.2", "Internal vulnerability scans performed via authenticated scanning"},
			{"11.3.1.3", "Internal vulnerability scans performed after any significant change"},
			{"11.3.2", "External vulnerability scans performed via PCI SSC ASV at least every three months"},
			{"11.3.2.1", "External vulnerability scans performed after any significant change"},
			{"11.4.1", "External and internal penetration testing methodology defined"},
			{"11.4.2", "Internal penetration testing performed at least once every 12 months"},
			{"11.4.3", "External penetration testing performed at least once every 12 months"},
			{"11.4.4", "Exploitable vulnerabilities and security weaknesses found during penetration testing corrected"},
			{"11.4.5", "If segmentation used to reduce CDE scope, penetration testing on segmentation controls performed"},
			{"11.4.6", "Service providers — penetration testing on segmentation controls performed at least every six months"},
			{"11.4.7", "Multi-tenant service providers — support customers for external penetration testing"},
			{"11.5.1", "Network intrusion-detection/prevention techniques used to detect and prevent intrusions"},
			{"11.5.1.1", "Service providers — covert malware communication channels detected, alerted, and prevented"},
			{"11.5.2", "Change-detection mechanism deployed for critical files"},
			{"11.6.1", "Change- and tamper-detection mechanism deployed for payment pages"},
			{"10.4.1", "Audit logs reviewed at least once daily"},
			{"10.4.1.1", "Automated mechanisms used to perform audit log reviews"},
			{"10.4.2", "Logs of all other system components reviewed periodically"},
			{"10.4.2.1", "Frequency of periodic log reviews defined per risk analysis"},
			{"10.4.3", "Exceptions and anomalies identified during the review process addressed"},
			{"10.7.1", "Failures of critical security control systems detected, alerted, and addressed promptly"},
			{"10.7.2", "Service providers — failures of critical security control systems detected, alerted, addressed"},
			{"10.7.3", "Failures of critical security controls responded to promptly"},
		}),
		// Reqs 1-8, 10 (technical) — Vigo modules apply the control.
		scopedCatalog(ScopeEnforced, [][2]string{
			// Req 1 — Install and maintain network security controls
			{"1.2.1", "Configuration standards for NSC rulesets defined and enforced"},
			{"1.2.2", "All changes to network connections and NSC configurations are approved"},
			{"1.2.3", "Accurate network diagram(s) maintained"},
			{"1.2.4", "Accurate data-flow diagram(s) maintained"},
			{"1.2.5", "All services, protocols, and ports allowed are identified, approved, justified"},
			{"1.2.6", "Security features defined for insecure services, protocols, and ports"},
			{"1.2.7", "Configurations of NSCs reviewed at least every six months"},
			{"1.2.8", "NSC configuration files secured from unauthorized access"},
			{"1.3.1", "Inbound traffic to the CDE restricted"},
			{"1.3.2", "Outbound traffic from the CDE restricted"},
			{"1.3.3", "NSCs installed between wireless networks and the CDE"},
			{"1.4.1", "NSCs implemented between trusted and untrusted networks"},
			{"1.4.2", "Inbound traffic from untrusted networks to trusted networks restricted"},
			{"1.4.3", "Anti-spoofing measures implemented to detect and block forged source addresses"},
			{"1.4.4", "System components storing CHD are not directly reachable from untrusted networks"},
			{"1.4.5", "Disclosure of internal IP addresses and routing information limited"},
			{"1.5.1", "Security controls implemented on computing devices that connect to both untrusted networks and the CDE"},
			// Req 2 — Apply secure configurations
			{"2.2.1", "Configuration standards developed, implemented, and maintained"},
			{"2.2.2", "Vendor default accounts managed"},
			{"2.2.3", "Primary functions requiring different security levels are managed"},
			{"2.2.4", "Only necessary services, protocols, daemons, and functions are enabled"},
			{"2.2.5", "If insecure services, protocols, daemons, or functions are present, business justification is documented and additional security features mitigate risk"},
			{"2.2.6", "System security parameters configured to prevent misuse"},
			{"2.2.7", "All non-console administrative access is encrypted with strong cryptography"},
			{"2.3.1", "Wireless vendor defaults are changed at installation"},
			{"2.3.2", "Wireless encryption keys changed when personnel with knowledge leave or change roles"},
			// Req 3 — Protect stored account data
			{"3.2.1", "Account data storage minimized through data-retention and disposal policies"},
			{"3.3.1", "SAD is not retained after authorization"},
			{"3.3.1.1", "Full track data is not retained after authorization"},
			{"3.3.1.2", "Card verification code is not retained after authorization"},
			{"3.3.1.3", "PIN and PIN block are not retained after authorization"},
			{"3.3.2", "SAD encrypted with strong cryptography while stored prior to authorization completion"},
			{"3.3.3", "SAD storage by issuers and companies that support issuing services follow specific requirements"},
			{"3.4.1", "PAN is masked when displayed"},
			{"3.4.2", "When using remote-access technologies, technical controls prevent unauthorized PAN copy/relocation"},
			{"3.5.1", "PAN is rendered unreadable anywhere it is stored"},
			{"3.5.1.1", "Hashes used to render PAN unreadable are keyed cryptographic hashes"},
			{"3.5.1.2", "If disk-level or partition-level encryption used, additional requirements apply"},
			{"3.5.1.3", "If disk-level or partition-level encryption used to render PAN unreadable, it is implemented per defined requirements"},
			{"3.6.1.2", "Secret and private keys for stored data encryption stored in secure forms"},
			{"3.6.1.4", "Cryptographic keys stored in fewest possible locations"},
			// Req 4 — Protect cardholder data with strong cryptography during transmission
			{"4.2.1", "Strong cryptography and security protocols used to safeguard PAN during transmission"},
			{"4.2.1.1", "Inventory of trusted keys and certificates maintained"},
			{"4.2.1.2", "Wireless networks transmitting PAN use strong cryptography"},
			{"4.2.2", "PAN is secured with strong cryptography whenever sent via end-user messaging technologies"},
			// Req 5 — Protect all systems and networks from malicious software
			{"5.2.1", "Anti-malware solution(s) deployed on all system components"},
			{"5.2.2", "Anti-malware solution(s) detect, remove, block, contain malware"},
			{"5.2.3", "Anti-malware solution(s) cover all common types of malware"},
			{"5.2.3.1", "Frequency of evaluations defined for systems not at risk for malware"},
			{"5.3.1", "Anti-malware definitions are kept current via automatic updates"},
			{"5.3.2", "Anti-malware mechanisms perform periodic scans and active/real-time scans"},
			{"5.3.2.1", "Frequency of periodic scans defined per risk analysis"},
			{"5.3.3", "Anti-malware mechanisms scan removable electronic media when in use"},
			{"5.3.4", "Audit logs from anti-malware solutions are enabled and retained per Req 10"},
			{"5.3.5", "Anti-malware mechanisms cannot be disabled or altered by users"},
			// Req 6 — Develop and maintain secure systems and software
			{"6.3.1", "Security vulnerabilities identified and managed"},
			{"6.3.2", "Inventory of bespoke and third-party software components maintained"},
			{"6.3.3", "All system components protected from known vulnerabilities by installing applicable security patches"},
			{"6.4.1", "Public-facing web applications are protected against attacks"},
			{"6.4.2", "Automated technical solution that detects and prevents web-based attacks for public-facing web apps"},
			{"6.4.3", "All payment-page scripts loaded and executed in the consumer's browser are managed"},
			{"6.5.1", "Changes to all system components in production environments follow a process"},
			{"6.5.2", "Upon completion of significant change, all applicable PCI DSS requirements are confirmed"},
			{"6.5.3", "Pre-production environments separated from production environments"},
			{"6.5.4", "Roles and functions separated between production and pre-production environments"},
			{"6.5.5", "Live PANs not used in pre-production environments"},
			{"6.5.6", "Test data and test accounts removed from system components before production release"},
			// Req 7 — Restrict access to system components and CHD by business need to know
			{"7.2.1", "Access control model defines access needs and assignment of access"},
			{"7.2.2", "Access assigned to users based on job classification and function"},
			{"7.2.3", "Required privileges approved by authorized personnel"},
			{"7.2.4", "All user accounts and related access privileges reviewed at least every six months"},
			{"7.2.5", "All application and system accounts and their access privileges are assigned and managed"},
			{"7.2.5.1", "All access by application and system accounts and related access privileges reviewed periodically"},
			{"7.2.6", "All access by application and system accounts to query data is restricted"},
			{"7.3.1", "Access control system in place for system components"},
			{"7.3.2", "Access control system configured to enforce permissions assigned"},
			{"7.3.3", "Access control system has a default 'deny-all' setting"},
			// Req 8 — Identify users and authenticate access to system components
			{"8.2.1", "All users assigned a unique ID before access to system components is allowed"},
			{"8.2.2", "Group, shared, or generic accounts only used as exceptions and managed"},
			{"8.2.3", "Service-provider tech support accounts use unique credentials per customer"},
			{"8.2.4", "Addition, deletion, and modification of user IDs and credentials is managed"},
			{"8.2.5", "Access for terminated users is immediately revoked"},
			{"8.2.6", "Inactive user accounts are removed or disabled within 90 days of inactivity"},
			{"8.2.7", "Accounts used by third parties to access, support, or maintain components are monitored"},
			{"8.2.8", "If user session has been idle more than 15 minutes, user must re-authenticate"},
			{"8.3.1", "All user access to system components for users and admins is authenticated"},
			{"8.3.2", "Strong cryptography used to render all authentication factors unreadable during transmission and storage"},
			{"8.3.3", "User identity verified before modifying any authentication factor"},
			{"8.3.4", "Invalid authentication attempts limited"},
			{"8.3.5", "If passwords/passphrases are used as authentication factors, they are set and reset securely"},
			{"8.3.6", "Passwords/passphrases meet defined complexity"},
			{"8.3.7", "Individuals not allowed to submit a new password/passphrase that is the same as any of the last four"},
			{"8.3.8", "Authentication policies and procedures documented and communicated"},
			{"8.3.9", "If passwords are sole authentication factor for end-user access, change every 90 days OR posture analyzed dynamically"},
			{"8.3.10", "Service providers' additional requirement on customer user passwords"},
			{"8.3.10.1", "Customer user passwords changed at least every 90 days"},
			{"8.3.11", "Where authentication factors are used (tokens, cards), they are assigned, not shared, and physical controls protect them"},
			{"8.4.1", "MFA implemented for all non-console administrative access into the CDE"},
			{"8.4.2", "MFA implemented for all access into the CDE"},
			{"8.4.3", "MFA implemented for all remote network access from outside the entity's network"},
			{"8.5.1", "MFA systems are not susceptible to replay attacks, cannot be bypassed, require at least two factors, and require all factors"},
			{"8.6.1", "Accounts used by systems or applications managed"},
			{"8.6.2", "Passwords/passphrases for application/system accounts are not hard-coded in scripts, config, or source"},
			{"8.6.3", "Passwords/passphrases for application/system accounts are protected against misuse"},
			// Req 9 — Restrict physical access to cardholder data
			// Req 10 — Log and monitor all access to system components and CHD
			{"10.2.1", "Audit logs enabled and active for all system components"},
			{"10.2.1.1", "Audit logs capture all individual user access to CHD"},
			{"10.2.1.2", "Audit logs capture all actions taken by any user with administrative access"},
			{"10.2.1.3", "Audit logs capture all access to audit logs"},
			{"10.2.1.4", "Audit logs capture all invalid logical access attempts"},
			{"10.2.1.5", "Audit logs capture all changes to identification and authentication credentials"},
			{"10.2.1.6", "Audit logs capture initialization, stopping, or pausing of audit logs"},
			{"10.2.1.7", "Audit logs capture all creation and deletion of system-level objects"},
			{"10.2.2", "Audit logs record specific elements for each event"},
			{"10.3.1", "Read access to audit logs limited to those with job-related need"},
			{"10.3.2", "Audit log files protected to prevent modifications"},
			{"10.3.3", "Audit log files promptly backed up to a secure central log server"},
			{"10.3.4", "File integrity monitoring or change-detection mechanisms used on audit logs"},
			{"10.5.1", "Retain audit log history for at least 12 months, with at least 3 months immediately available"},
			{"10.6.1", "System clocks and time synchronized using time-synchronization technology"},
			{"10.6.2", "Systems are configured with the correct, consistent time"},
			{"10.6.3", "Time synchronization settings and data are protected"},
			// Req 11 — Test security of systems and networks regularly
			// Req 12 — Support information security with organizational policies and programs
		}),
	)
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

// buildNIST80053Findings returns the NIST 800-53 Rev 5 Moderate
// baseline catalog (per NIST SP 800-53B). U.S. Government work
// product — public domain — full control titles enumerated verbatim.
//
// Scope assignment: every "X-1 Policy and Procedures" control across
// all families is documented (governance artifact). AT (training),
// CA except CA-7 (assessments), IR mostly (response plans), MA, MP
// except MP-6 (media handling), PE (physical), PL, PM, PS, PT
// (privacy notices), SA, and SR (supply chain) are all documented.
// AC, AU mostly, CM, IA, MP-6, CP-9 family + CP-10, SC, and SI
// mostly are enforced. CA-7, AU-6 family, RA-5 family, SI-4 family,
// SI-5 are attested via Vigo's drift detection / vulnerability
// scanning / system monitoring.
func (h *ReportHandler) buildNIST80053Findings() []ControlFinding {
	return concatCatalogs(
		// Documented — every "X-1 Policy and Procedures", plus AT/CA/
		// IR-mostly/MA/MP-except-6/PE/PL/PM/PS/PT/SA/SR families and
		// CP plans (except CP-9 backups + CP-10 recovery).
		scopedCatalog(ScopeDocumented, [][2]string{
			{"AC-1", "Policy and Procedures"},
			{"AT-1", "Policy and Procedures"},
			{"AT-2", "Literacy Training and Awareness"},
			{"AT-2(2)", "Literacy Training and Awareness | Insider Threat"},
			{"AT-2(3)", "Literacy Training and Awareness | Social Engineering and Mining"},
			{"AT-3", "Role-Based Training"},
			{"AT-4", "Training Records"},
			{"AU-1", "Policy and Procedures"},
			{"CA-1", "Policy and Procedures"},
			{"CA-2", "Control Assessments"},
			{"CA-2(1)", "Control Assessments | Independent Assessors"},
			{"CA-3", "Information Exchange"},
			{"CA-5", "Plan of Action and Milestones"},
			{"CA-6", "Authorization"},
			{"CA-8", "Penetration Testing"},
			{"CA-9", "Internal System Connections"},
			{"CM-1", "Policy and Procedures"},
			{"CM-9", "Configuration Management Plan"},
			{"CM-10", "Software Usage Restrictions"},
			{"CP-1", "Policy and Procedures"},
			{"CP-2", "Contingency Plan"},
			{"CP-2(1)", "Contingency Plan | Coordinate with Related Plans"},
			{"CP-2(3)", "Contingency Plan | Resume Mission and Business Functions"},
			{"CP-2(8)", "Contingency Plan | Identify Critical Assets"},
			{"CP-3", "Contingency Training"},
			{"CP-4", "Contingency Plan Testing"},
			{"CP-4(1)", "Contingency Plan Testing | Coordinate with Related Plans"},
			{"CP-6", "Alternate Storage Site"},
			{"CP-6(1)", "Alternate Storage Site | Separation from Primary Site"},
			{"CP-6(3)", "Alternate Storage Site | Accessibility"},
			{"CP-7", "Alternate Processing Site"},
			{"CP-7(1)", "Alternate Processing Site | Separation from Primary Site"},
			{"CP-7(2)", "Alternate Processing Site | Accessibility"},
			{"CP-7(3)", "Alternate Processing Site | Priority of Service"},
			{"CP-8", "Telecommunications Services"},
			{"CP-8(1)", "Telecommunications Services | Priority of Service Provisions"},
			{"CP-8(2)", "Telecommunications Services | Single Points of Failure"},
			{"IA-1", "Policy and Procedures"},
			{"IR-1", "Policy and Procedures"},
			{"IR-2", "Incident Response Training"},
			{"IR-3", "Incident Response Testing"},
			{"IR-3(2)", "Incident Response Testing | Coordination with Related Plans"},
			{"IR-4", "Incident Handling"},
			{"IR-7", "Incident Response Assistance"},
			{"IR-7(1)", "Incident Response Assistance | Automation Support for Availability of Information"},
			{"IR-8", "Incident Response Plan"},
			{"MA-1", "Policy and Procedures"},
			{"MA-2", "Controlled Maintenance"},
			{"MA-3", "Maintenance Tools"},
			{"MA-3(1)", "Maintenance Tools | Inspect Tools"},
			{"MA-3(2)", "Maintenance Tools | Inspect Media"},
			{"MA-3(3)", "Maintenance Tools | Prevent Unauthorized Removal"},
			{"MA-4", "Nonlocal Maintenance"},
			{"MA-4(2)", "Nonlocal Maintenance | Document Nonlocal Maintenance"},
			{"MA-5", "Maintenance Personnel"},
			{"MA-6", "Timely Maintenance"},
			{"MP-1", "Policy and Procedures"},
			{"MP-2", "Media Access"},
			{"MP-3", "Media Marking"},
			{"MP-4", "Media Storage"},
			{"MP-5", "Media Transport"},
			{"MP-7", "Media Use"},
			{"PE-1", "Policy and Procedures"},
			{"PE-2", "Physical Access Authorizations"},
			{"PE-3", "Physical Access Control"},
			{"PE-4", "Access Control for Transmission"},
			{"PE-5", "Access Control for Output Devices"},
			{"PE-6", "Monitoring Physical Access"},
			{"PE-6(1)", "Monitoring Physical Access | Intrusion Alarms and Surveillance Equipment"},
			{"PE-8", "Visitor Access Records"},
			{"PE-9", "Power Equipment and Cabling"},
			{"PE-10", "Emergency Shutoff"},
			{"PE-11", "Emergency Power"},
			{"PE-12", "Emergency Lighting"},
			{"PE-13", "Fire Protection"},
			{"PE-13(1)", "Fire Protection | Detection Systems — Automatic Activation and Notification"},
			{"PE-14", "Environmental Controls"},
			{"PE-15", "Water Damage Protection"},
			{"PE-16", "Delivery and Removal"},
			{"PE-17", "Alternate Work Site"},
			{"PL-1", "Policy and Procedures"},
			{"PL-2", "System Security and Privacy Plans"},
			{"PL-4", "Rules of Behavior"},
			{"PL-4(1)", "Rules of Behavior | Social Media and External Site/Application Usage Restrictions"},
			{"PL-8", "Security and Privacy Architectures"},
			{"PL-10", "Baseline Selection"},
			{"PL-11", "Baseline Tailoring"},
			{"PM-1", "Information Security Program Plan"},
			{"PM-2", "Information Security Program Leadership Role"},
			{"PM-3", "Information Security and Privacy Resources"},
			{"PM-4", "Plan of Action and Milestones Process"},
			{"PM-5", "System Inventory"},
			{"PM-6", "Measures of Performance"},
			{"PM-7", "Enterprise Architecture"},
			{"PM-8", "Critical Infrastructure Plan"},
			{"PM-9", "Risk Management Strategy"},
			{"PM-10", "Authorization Process"},
			{"PM-11", "Mission and Business Process Definition"},
			{"PM-12", "Insider Threat Program"},
			{"PM-13", "Security and Privacy Workforce"},
			{"PM-14", "Testing, Training, and Monitoring"},
			{"PM-15", "Security and Privacy Groups and Associations"},
			{"PM-16", "Threat Awareness Program"},
			{"PM-17", "Protecting Controlled Unclassified Information on External Systems"},
			{"PM-18", "Privacy Program Plan"},
			{"PM-19", "Privacy Program Leadership Role"},
			{"PM-20", "Dissemination of Privacy Program Information"},
			{"PM-21", "Accounting of Disclosures"},
			{"PM-22", "Personally Identifiable Information Quality Management"},
			{"PM-23", "Data Governance Body"},
			{"PM-24", "Data Integrity Board"},
			{"PM-25", "Minimization of PII Used in Testing, Training, and Research"},
			{"PM-26", "Complaint Management"},
			{"PM-27", "Privacy Reporting"},
			{"PM-28", "Risk Framing"},
			{"PM-29", "Risk Management Program Leadership Roles"},
			{"PM-30", "Supply Chain Risk Management Strategy"},
			{"PM-31", "Continuous Monitoring Strategy"},
			{"PM-32", "Purposing"},
			{"PS-1", "Policy and Procedures"},
			{"PS-2", "Position Risk Designation"},
			{"PS-3", "Personnel Screening"},
			{"PS-4", "Personnel Termination"},
			{"PS-5", "Personnel Transfer"},
			{"PS-6", "Access Agreements"},
			{"PS-7", "External Personnel Security"},
			{"PS-8", "Personnel Sanctions"},
			{"PS-9", "Position Descriptions"},
			{"PT-1", "Policy and Procedures"},
			{"PT-2", "Authority to Process Personally Identifiable Information"},
			{"PT-3", "Personally Identifiable Information Processing Purposes"},
			{"PT-4", "Consent"},
			{"PT-5", "Privacy Notice"},
			{"PT-6", "System of Records Notice"},
			{"RA-1", "Policy and Procedures"},
			{"RA-2", "Security Categorization"},
			{"RA-3", "Risk Assessment"},
			{"RA-3(1)", "Risk Assessment | Supply Chain Risk Assessment"},
			{"RA-7", "Risk Response"},
			{"RA-9", "Criticality Analysis"},
			{"SA-1", "Policy and Procedures"},
			{"SA-2", "Allocation of Resources"},
			{"SA-3", "System Development Life Cycle"},
			{"SA-4", "Acquisition Process"},
			{"SA-4(1)", "Acquisition Process | Functional Properties of Controls"},
			{"SA-4(2)", "Acquisition Process | Design and Implementation Information for Controls"},
			{"SA-4(9)", "Acquisition Process | Functions, Ports, Protocols, and Services in Use"},
			{"SA-4(10)", "Acquisition Process | Use of Approved PIV Products"},
			{"SA-5", "System Documentation"},
			{"SA-8", "Security and Privacy Engineering Principles"},
			{"SA-9", "External System Services"},
			{"SA-9(2)", "External System Services | Identification of Functions, Ports, Protocols, and Services"},
			{"SA-10", "Developer Configuration Management"},
			{"SA-11", "Developer Testing and Evaluation"},
			{"SA-15", "Development Process, Standards, and Tools"},
			{"SA-22", "Unsupported System Components"},
			{"SC-1", "Policy and Procedures"},
			{"SI-1", "Policy and Procedures"},
			{"SR-1", "Policy and Procedures"},
			{"SR-2", "Supply Chain Risk Management Plan"},
			{"SR-2(1)", "Supply Chain Risk Management Plan | Establish SCRM Team"},
			{"SR-3", "Supply Chain Controls and Processes"},
			{"SR-5", "Acquisition Strategies, Tools, and Methods"},
			{"SR-6", "Supplier Assessments and Reviews"},
			{"SR-8", "Notification Agreements"},
			{"SR-10", "Inspection of Systems or Components"},
			{"SR-11", "Component Authenticity"},
			{"SR-11(1)", "Component Authenticity | Anti-counterfeit Training"},
			{"SR-11(2)", "Component Authenticity | Configuration Control for Component Service and Repair"},
			{"SR-12", "Component Disposal"},
		}),
		// Attested — Vigo collects evidence (drift detection, security
		// scan trait, audit chain integrity, system monitoring).
		scopedCatalog(ScopeAttested, [][2]string{
			{"AU-6", "Audit Record Review, Analysis, and Reporting"},
			{"AU-6(1)", "Audit Record Review | Automated Process Integration"},
			{"AU-6(3)", "Audit Record Review | Correlate Audit Record Repositories"},
			{"CA-7", "Continuous Monitoring"},
			{"CA-7(1)", "Continuous Monitoring | Independent Assessment"},
			{"CA-7(4)", "Continuous Monitoring | Risk Monitoring"},
			{"IR-5", "Incident Monitoring"},
			{"IR-6", "Incident Reporting"},
			{"IR-6(1)", "Incident Reporting | Automated Reporting"},
			{"IR-6(3)", "Incident Reporting | Supply Chain Coordination"},
			{"IR-4(1)", "Incident Handling | Automated Incident Handling Processes"},
			{"RA-5", "Vulnerability Monitoring and Scanning"},
			{"RA-5(2)", "Vulnerability Monitoring | Update Vulnerabilities to be Scanned"},
			{"RA-5(11)", "Vulnerability Monitoring | Public Disclosure Program"},
			{"SI-4", "System Monitoring"},
			{"SI-4(2)", "System Monitoring | Automated Tools and Mechanisms for Real-Time Analysis"},
			{"SI-4(4)", "System Monitoring | Inbound and Outbound Communications Traffic"},
			{"SI-4(5)", "System Monitoring | System-Generated Alerts"},
			{"SI-5", "Security Alerts, Advisories, and Directives"},
		}),
		// Enforced — AC, AU (logging), CM, IA, SC, SI mostly + media
		// sanitization + backup family.
		scopedCatalog(ScopeEnforced, [][2]string{
			// AC — Access Control
			{"AC-2", "Account Management"},
			{"AC-2(1)", "Account Management | Automated System Account Management"},
			{"AC-2(2)", "Account Management | Automated Temporary and Emergency Account Management"},
			{"AC-2(3)", "Account Management | Disable Accounts"},
			{"AC-2(4)", "Account Management | Automated Audit Actions"},
			{"AC-2(5)", "Account Management | Inactivity Logout"},
			{"AC-2(13)", "Account Management | Disable Accounts for High-Risk Individuals"},
			{"AC-3", "Access Enforcement"},
			{"AC-4", "Information Flow Enforcement"},
			{"AC-4(4)", "Information Flow Enforcement | Flow Control of Encrypted Information"},
			{"AC-5", "Separation of Duties"},
			{"AC-6", "Least Privilege"},
			{"AC-6(1)", "Least Privilege | Authorize Access to Security Functions"},
			{"AC-6(2)", "Least Privilege | Non-privileged Access for Nonsecurity Functions"},
			{"AC-6(5)", "Least Privilege | Privileged Accounts"},
			{"AC-6(7)", "Least Privilege | Review of User Privileges"},
			{"AC-6(9)", "Least Privilege | Log Use of Privileged Functions"},
			{"AC-6(10)", "Least Privilege | Prohibit Non-privileged Users from Executing Privileged Functions"},
			{"AC-7", "Unsuccessful Logon Attempts"},
			{"AC-8", "System Use Notification"},
			{"AC-11", "Device Lock"},
			{"AC-11(1)", "Device Lock | Pattern-Hiding Displays"},
			{"AC-12", "Session Termination"},
			{"AC-14", "Permitted Actions Without Identification or Authentication"},
			{"AC-17", "Remote Access"},
			{"AC-17(1)", "Remote Access | Monitoring and Control"},
			{"AC-17(2)", "Remote Access | Protection of Confidentiality and Integrity Using Encryption"},
			{"AC-17(3)", "Remote Access | Managed Access Control Points"},
			{"AC-17(4)", "Remote Access | Privileged Commands and Access"},
			{"AC-18", "Wireless Access"},
			{"AC-18(1)", "Wireless Access | Authentication and Encryption"},
			{"AC-18(3)", "Wireless Access | Disable Wireless Networking"},
			{"AC-19", "Access Control for Mobile Devices"},
			{"AC-19(5)", "Access Control for Mobile Devices | Full Device or Container-Based Encryption"},
			{"AC-20", "Use of External Systems"},
			{"AC-20(1)", "Use of External Systems | Limits on Authorized Use"},
			{"AC-20(2)", "Use of External Systems | Portable Storage Devices — Restricted Use"},
			{"AC-21", "Information Sharing"},
			{"AC-22", "Publicly Accessible Content"},
			// AU — Audit and Accountability
			{"AU-2", "Event Logging"},
			{"AU-3", "Content of Audit Records"},
			{"AU-3(1)", "Content of Audit Records | Additional Audit Information"},
			{"AU-4", "Audit Log Storage Capacity"},
			{"AU-5", "Response to Audit Logging Process Failures"},
			{"AU-7", "Audit Record Reduction and Report Generation"},
			{"AU-7(1)", "Audit Record Reduction | Automatic Processing"},
			{"AU-8", "Time Stamps"},
			{"AU-9", "Protection of Audit Information"},
			{"AU-9(4)", "Protection of Audit Information | Access by Subset of Privileged Users"},
			{"AU-11", "Audit Record Retention"},
			{"AU-12", "Audit Record Generation"},
			// CM — Configuration Management
			{"CM-2", "Baseline Configuration"},
			{"CM-2(2)", "Baseline Configuration | Automation Support for Accuracy and Currency"},
			{"CM-2(3)", "Baseline Configuration | Retention of Previous Configurations"},
			{"CM-2(7)", "Baseline Configuration | Configure Systems and Components for High-Risk Areas"},
			{"CM-3", "Configuration Change Control"},
			{"CM-3(2)", "Configuration Change Control | Testing, Validation, and Documentation of Changes"},
			{"CM-3(4)", "Configuration Change Control | Security and Privacy Representatives"},
			{"CM-4", "Impact Analyses"},
			{"CM-4(2)", "Impact Analyses | Verification of Controls"},
			{"CM-5", "Access Restrictions for Change"},
			{"CM-6", "Configuration Settings"},
			{"CM-7", "Least Functionality"},
			{"CM-7(1)", "Least Functionality | Periodic Review"},
			{"CM-7(2)", "Least Functionality | Prevent Program Execution"},
			{"CM-7(5)", "Least Functionality | Authorized Software — Allow-by-Exception"},
			{"CM-8", "System Component Inventory"},
			{"CM-8(1)", "System Component Inventory | Updates During Installation and Removal"},
			{"CM-8(3)", "System Component Inventory | Automated Unauthorized Component Detection"},
			{"CM-11", "User-Installed Software"},
			{"CM-12", "Information Location"},
			{"CP-9", "System Backup"},
			{"CP-9(1)", "System Backup | Testing for Reliability and Integrity"},
			{"CP-9(8)", "System Backup | Cryptographic Protection"},
			{"CP-10", "System Recovery and Reconstitution"},
			{"CP-10(2)", "System Recovery and Reconstitution | Transaction Recovery"},
			// IA — Identification and Authentication
			{"IA-2", "Identification and Authentication (Organizational Users)"},
			{"IA-2(1)", "Identification and Authentication | Multi-Factor Authentication to Privileged Accounts"},
			{"IA-2(2)", "Identification and Authentication | Multi-Factor Authentication to Non-privileged Accounts"},
			{"IA-2(8)", "Identification and Authentication | Access to Accounts — Replay Resistant"},
			{"IA-2(12)", "Identification and Authentication | Acceptance of PIV Credentials"},
			{"IA-3", "Device Identification and Authentication"},
			{"IA-4", "Identifier Management"},
			{"IA-4(4)", "Identifier Management | Identify User Status"},
			{"IA-5", "Authenticator Management"},
			{"IA-5(1)", "Authenticator Management | Password-Based Authentication"},
			{"IA-5(2)", "Authenticator Management | Public Key-Based Authentication"},
			{"IA-5(6)", "Authenticator Management | Protection of Authenticators"},
			{"IA-6", "Authentication Feedback"},
			{"IA-7", "Cryptographic Module Authentication"},
			{"IA-8", "Identification and Authentication (Non-Organizational Users)"},
			{"IA-8(1)", "Identification and Authentication (Non-Org) | Acceptance of PIV Credentials from Other Agencies"},
			{"IA-8(2)", "Identification and Authentication (Non-Org) | Acceptance of External Authenticators"},
			{"IA-8(4)", "Identification and Authentication (Non-Org) | Use of Defined Profiles"},
			{"IA-11", "Re-authentication"},
			{"IA-12", "Identity Proofing"},
			{"IA-12(2)", "Identity Proofing | Identity Evidence"},
			{"IA-12(3)", "Identity Proofing | Identity Evidence Validation and Verification"},
			{"IA-12(5)", "Identity Proofing | Address Confirmation"},
			{"MP-6", "Media Sanitization"},
			// SC — System and Communications Protection
			{"SC-2", "Separation of System and User Functionality"},
			{"SC-4", "Information in Shared System Resources"},
			{"SC-5", "Denial-of-Service Protection"},
			{"SC-7", "Boundary Protection"},
			{"SC-7(3)", "Boundary Protection | Access Points"},
			{"SC-7(4)", "Boundary Protection | External Telecommunications Services"},
			{"SC-7(5)", "Boundary Protection | Deny by Default — Allow by Exception"},
			{"SC-7(7)", "Boundary Protection | Split Tunneling for Remote Devices"},
			{"SC-7(8)", "Boundary Protection | Route Traffic to Authenticated Proxy Servers"},
			{"SC-8", "Transmission Confidentiality and Integrity"},
			{"SC-8(1)", "Transmission Confidentiality and Integrity | Cryptographic Protection"},
			{"SC-10", "Network Disconnect"},
			{"SC-12", "Cryptographic Key Establishment and Management"},
			{"SC-13", "Cryptographic Protection"},
			{"SC-15", "Collaborative Computing Devices and Applications"},
			{"SC-17", "Public Key Infrastructure Certificates"},
			{"SC-18", "Mobile Code"},
			{"SC-20", "Secure Name/Address Resolution Service (Authoritative Source)"},
			{"SC-21", "Secure Name/Address Resolution Service (Recursive or Caching Resolver)"},
			{"SC-22", "Architecture and Provisioning for Name/Address Resolution Service"},
			{"SC-23", "Session Authenticity"},
			{"SC-28", "Protection of Information at Rest"},
			{"SC-28(1)", "Protection of Information at Rest | Cryptographic Protection"},
			{"SC-39", "Process Isolation"},
			// SI — System and Information Integrity
			{"SI-2", "Flaw Remediation"},
			{"SI-2(2)", "Flaw Remediation | Automated Flaw Remediation Status"},
			{"SI-3", "Malicious Code Protection"},
			{"SI-7", "Software, Firmware, and Information Integrity"},
			{"SI-7(1)", "Software Integrity | Integrity Checks"},
			{"SI-7(7)", "Software Integrity | Integration of Detection and Response"},
			{"SI-8", "Spam Protection"},
			{"SI-8(2)", "Spam Protection | Automatic Updates"},
			{"SI-10", "Information Input Validation"},
			{"SI-11", "Error Handling"},
			{"SI-12", "Information Management and Retention"},
			{"SI-16", "Memory Protection"},
		}),
	)
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

// buildISO27001Findings returns the ISO/IEC 27001:2022 Annex A control
// catalog — 93 controls organized into four themes (organizational,
// people, physical, technological). ISO is licensed: only IDs and
// short titles are reproduced here; the descriptive paragraphs from
// the standard are never reproduced.
//
// Scope assignment: A.5 organizational, A.6 people, A.7 physical are
// all documented (governance, HR, contracts, physical security). A.8
// technological is mostly enforced — Vigo modules apply the technical
// control. Logging (A.8.15), monitoring (A.8.16), and SDL items are
// attested or documented.
func (h *ReportHandler) buildISO27001Findings() []ControlFinding {
	return concatCatalogs(
		// A.5 — Organizational controls (governance, contracts, BCP, incidents)
		scopedCatalog(ScopeDocumented, [][2]string{
			{"A.5.1", "Policies for information security"},
			{"A.5.2", "Information security roles and responsibilities"},
			{"A.5.3", "Segregation of duties"},
			{"A.5.4", "Management responsibilities"},
			{"A.5.5", "Contact with authorities"},
			{"A.5.6", "Contact with special interest groups"},
			{"A.5.7", "Threat intelligence"},
			{"A.5.8", "Information security in project management"},
			{"A.5.9", "Inventory of information and other associated assets"},
			{"A.5.10", "Acceptable use of information and other associated assets"},
			{"A.5.11", "Return of assets"},
			{"A.5.12", "Classification of information"},
			{"A.5.13", "Labelling of information"},
			{"A.5.14", "Information transfer"},
			{"A.5.15", "Access control"},
			{"A.5.16", "Identity management"},
			{"A.5.17", "Authentication information"},
			{"A.5.18", "Access rights"},
			{"A.5.19", "Information security in supplier relationships"},
			{"A.5.20", "Addressing information security within supplier agreements"},
			{"A.5.21", "Managing information security in the ICT supply chain"},
			{"A.5.22", "Monitoring, review and change management of supplier services"},
			{"A.5.23", "Information security for use of cloud services"},
			{"A.5.24", "Information security incident management planning and preparation"},
			{"A.5.25", "Assessment and decision on information security events"},
			{"A.5.26", "Response to information security incidents"},
			{"A.5.27", "Learning from information security incidents"},
			{"A.5.28", "Collection of evidence"},
			{"A.5.29", "Information security during disruption"},
			{"A.5.30", "ICT readiness for business continuity"},
			{"A.5.31", "Legal, statutory, regulatory and contractual requirements"},
			{"A.5.32", "Intellectual property rights"},
			{"A.5.33", "Protection of records"},
			{"A.5.34", "Privacy and protection of PII"},
			{"A.5.35", "Independent review of information security"},
			{"A.5.36", "Compliance with policies, rules and standards for information security"},
			{"A.5.37", "Documented operating procedures"},
			// A.6 — People controls (all HR/training/contracts)
			{"A.6.1", "Screening"},
			{"A.6.2", "Terms and conditions of employment"},
			{"A.6.3", "Information security awareness, education and training"},
			{"A.6.4", "Disciplinary process"},
			{"A.6.5", "Responsibilities after termination or change of employment"},
			{"A.6.6", "Confidentiality or non-disclosure agreements"},
			{"A.6.7", "Remote working"},
			{"A.6.8", "Information security event reporting"},
			// A.7 — Physical controls (all physical security)
			{"A.7.1", "Physical security perimeters"},
			{"A.7.2", "Physical entry"},
			{"A.7.3", "Securing offices, rooms and facilities"},
			{"A.7.4", "Physical security monitoring"},
			{"A.7.5", "Protecting against physical and environmental threats"},
			{"A.7.6", "Working in secure areas"},
			{"A.7.7", "Clear desk and clear screen"},
			{"A.7.8", "Equipment siting and protection"},
			{"A.7.9", "Security of assets off-premises"},
			{"A.7.10", "Storage media"},
			{"A.7.11", "Supporting utilities"},
			{"A.7.12", "Cabling security"},
			{"A.7.13", "Equipment maintenance"},
			{"A.7.14", "Secure disposal or re-use of equipment"},
			// A.8 — Technological documented (capacity planning, SDL, secure dev process, redundancy)
			{"A.8.6", "Capacity management"},
			{"A.8.14", "Redundancy of information processing facilities"},
			{"A.8.25", "Secure development life cycle"},
			{"A.8.26", "Application security requirements"},
			{"A.8.27", "Secure system architecture and engineering principles"},
			{"A.8.28", "Secure coding"},
			{"A.8.29", "Security testing in development and acceptance"},
			{"A.8.30", "Outsourced development"},
			{"A.8.33", "Test information"},
			{"A.8.34", "Protection of information systems during audit testing"},
		}),
		// A.8 — Technological attested (logging, monitoring, DLP)
		scopedCatalog(ScopeAttested, [][2]string{
			{"A.8.15", "Logging"},
			{"A.8.16", "Monitoring activities"},
			{"A.8.12", "Data leakage prevention"},
		}),
		// A.8 — Technological enforced (Vigo modules apply)
		scopedCatalog(ScopeEnforced, [][2]string{
			{"A.8.1", "User endpoint devices"},
			{"A.8.2", "Privileged access rights"},
			{"A.8.3", "Information access restriction"},
			{"A.8.4", "Access to source code"},
			{"A.8.5", "Secure authentication"},
			{"A.8.7", "Protection against malware"},
			{"A.8.8", "Management of technical vulnerabilities"},
			{"A.8.9", "Configuration management"},
			{"A.8.10", "Information deletion"},
			{"A.8.11", "Data masking"},
			{"A.8.13", "Information backup"},
			{"A.8.17", "Clock synchronization"},
			{"A.8.18", "Use of privileged utility programs"},
			{"A.8.19", "Installation of software on operational systems"},
			{"A.8.20", "Networks security"},
			{"A.8.21", "Security of network services"},
			{"A.8.22", "Segregation of networks"},
			{"A.8.23", "Web filtering"},
			{"A.8.24", "Use of cryptography"},
			{"A.8.31", "Separation of development, test and production environments"},
			{"A.8.32", "Change management"},
		}),
	)
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

// buildGDPRFindings returns the GDPR (EU Regulation 2016/679) control
// catalog. Enumerates the articles relevant to technical and
// organizational measures, data subject rights, breach handling, DPO,
// and accountability. Public EU text — full titles allowed.
//
// Scope assignment: most GDPR articles are documented (lawful basis,
// consent, data subject rights procedures, DPO designation, transfer
// agreements). Article 32(1)(a)-(c) (encryption, integrity,
// availability, restore) are enforced by Vigo modules. Article 17
// (erasure) is enforced via the cascade-delete decommission flow.
func (h *ReportHandler) buildGDPRFindings() []ControlFinding {
	return concatCatalogs(
		// Articles where Vigo modules apply the technical control.
		scopedCatalog(ScopeEnforced, [][2]string{
			{"Art.5(1)(f)", "Integrity and Confidentiality"},
			{"Art.17", "Right to Erasure (Right to be Forgotten)"},
			{"Art.32(1)(a)", "Pseudonymisation and Encryption"},
			{"Art.32(1)(b)", "Confidentiality, Integrity, Availability, and Resilience"},
			{"Art.32(1)(c)", "Ability to Restore Availability and Access"},
		}),
		// Articles Vigo can attest to via audit log + drift detection.
		scopedCatalog(ScopeAttested, [][2]string{
			{"Art.30(1)", "Records of Processing Activities — Controller"},
			{"Art.30(2)", "Records of Processing Activities — Processor"},
			{"Art.32(1)(d)", "Process for Regular Testing and Evaluation"},
			{"Art.33(5)", "Breach Documentation"},
		}),
		// Articles requiring documented policies / procedures /
		// agreements / data subject response procedures / DPO.
		scopedCatalog(ScopeDocumented, [][2]string{
			{"Art.5(1)(a)", "Lawfulness, Fairness and Transparency"},
			{"Art.5(1)(b)", "Purpose Limitation"},
			{"Art.5(1)(c)", "Data Minimisation"},
			{"Art.5(1)(d)", "Accuracy"},
			{"Art.5(1)(e)", "Storage Limitation"},
			{"Art.5(2)", "Accountability"},
			{"Art.6(1)", "Lawfulness of Processing"},
			{"Art.7", "Conditions for Consent"},
			{"Art.9(1)", "Processing of Special Categories of Personal Data"},
			{"Art.12", "Transparent Information, Communication, and Modalities"},
			{"Art.13", "Information to be Provided where Personal Data are Collected from the Data Subject"},
			{"Art.14", "Information to be Provided where Personal Data are Not Obtained from the Data Subject"},
			{"Art.15", "Right of Access by the Data Subject"},
			{"Art.16", "Right to Rectification"},
			{"Art.18", "Right to Restriction of Processing"},
			{"Art.19", "Notification Obligation regarding Rectification, Erasure, or Restriction"},
			{"Art.20", "Right to Data Portability"},
			{"Art.21", "Right to Object"},
			{"Art.22", "Automated Individual Decision-Making, Including Profiling"},
			{"Art.24", "Responsibility of the Controller"},
			{"Art.25(1)", "Data Protection by Design"},
			{"Art.25(2)", "Data Protection by Default"},
			{"Art.26", "Joint Controllers"},
			{"Art.28", "Processor Obligations and Contracts"},
			{"Art.29", "Processing Under the Authority of Controller or Processor"},
			{"Art.31", "Cooperation with Supervisory Authority"},
			{"Art.32(2)", "Risk Assessment for Processing"},
			{"Art.33(1)", "Notification of Personal Data Breach to Supervisory Authority"},
			{"Art.33(2)", "Processor Notification of Breach to Controller"},
			{"Art.33(3)", "Content of Breach Notification"},
			{"Art.34", "Communication of Personal Data Breach to the Data Subject"},
			{"Art.35", "Data Protection Impact Assessment"},
			{"Art.36", "Prior Consultation with Supervisory Authority"},
			{"Art.37", "Designation of the Data Protection Officer"},
			{"Art.38", "Position of the Data Protection Officer"},
			{"Art.39", "Tasks of the Data Protection Officer"},
			{"Art.44", "General Principle for Transfers"},
			{"Art.46", "Transfers Subject to Appropriate Safeguards"},
		}),
	)
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
		{"CIP-002-5.1a", "BES Cyber System Identification"},
		// CIP-003 — Security Management Controls
		{"CIP-003-9-R1", "Cyber Security Policies"},
		{"CIP-003-9-R2", "Cyber Security Plans"},
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
		{"CIP-010-4-R1", "Configuration Change Management"},
		{"CIP-010-4-R2", "Configuration Monitoring"},
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
		// Cyber Essentials has exactly five technical control themes; the
		// former "Monitoring and Logging" theme was not one of them — CE
		// Plus verifies the same five themes, it does not add a sixth.
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
		// Clock Synchronisation (RTS 25)
		{"RTS25-ClockSync", "Business Clock Synchronisation (UTC)"},
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
		// (Proof of Reserves is a fund-control attestation, not a host
		// configuration control — outside what Vigo can evidence.)
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
		{"11.10-k-2", "Documentation of Changes"},
		// System Integrity
		{"11.10-a", "System Validation"},
		{"11.10-c", "Record Protection"},
		// Electronic Signatures
		{"11.10-h", "Electronic Signatures"},
	})
}

func (h *ReportHandler) buildGamingUKGCFindings() []ControlFinding {
	return catalog([][2]string{
		{"UKGC-RTS-4A", "Security Controls"},
		{"UKGC-RTS-4B", "Change Management"},
		{"UKGC-RTS-5A", "Resilience"},
		{"UKGC-RTS-5B", "Business Continuity"},
	})
}

func (h *ReportHandler) buildGamingNevadaFindings() []ControlFinding {
	return catalog([][2]string{
		{"NGC-TS-1.1", "System Hardening"},
		{"NGC-TS-1.2", "File Integrity"},
		{"NGC-TS-2.1", "Service Monitoring"},
		{"NGC-TS-2.2", "Recovery Capability"},
		{"NGC-TS-3.1", "Change Control for Gaming Systems"},
	})
}

func (h *ReportHandler) buildGamingMGAFindings() []ControlFinding {
	return catalog([][2]string{
		{"MGA-RGR-4.1", "System Security"},
		{"MGA-RGR-4.2", "Data Integrity"},
		{"MGA-RGR-5.1", "System Availability"},
		{"MGA-RGR-5.2", "Disaster Recovery"},
	})
}

// buildCISWindowsFindings returns the CIS Windows Server 2022
// Benchmark Level 1 + Member Server profile catalog. CIS-licensed
// under the CIS Benchmarks End User License Agreement: only IDs and
// short titles are reproduced.
func (h *ReportHandler) buildCISWindowsFindings() []ControlFinding {
	return catalog([][2]string{
		// Section 1 — Account Policies
		{"1.1.1", "Enforce password history"},
		{"1.1.2", "Maximum password age"},
		{"1.1.3", "Minimum password age"},
		{"1.1.4", "Minimum password length"},
		{"1.1.5", "Password must meet complexity requirements"},
		{"1.1.6", "Relax minimum password length limits"},
		{"1.1.7", "Store passwords using reversible encryption"},
		{"1.2.1", "Account lockout duration"},
		{"1.2.2", "Account lockout threshold"},
		{"1.2.3", "Allow Administrator account lockout"},
		{"1.2.4", "Reset account lockout counter after"},
		// Section 2.2 — User Rights Assignment
		{"2.2.1", "Access Credential Manager as a trusted caller"},
		{"2.2.2", "Access this computer from the network"},
		{"2.2.3", "Act as part of the operating system"},
		{"2.2.4", "Adjust memory quotas for a process"},
		{"2.2.5", "Allow log on locally"},
		{"2.2.6", "Allow log on through Remote Desktop Services"},
		{"2.2.7", "Back up files and directories"},
		{"2.2.8", "Change the system time"},
		{"2.2.9", "Change the time zone"},
		{"2.2.10", "Create a pagefile"},
		{"2.2.11", "Create a token object"},
		{"2.2.12", "Create global objects"},
		{"2.2.13", "Create permanent shared objects"},
		{"2.2.14", "Create symbolic links"},
		{"2.2.15", "Debug programs"},
		{"2.2.16", "Deny access to this computer from the network"},
		{"2.2.17", "Deny log on as a batch job"},
		{"2.2.18", "Deny log on as a service"},
		{"2.2.19", "Deny log on locally"},
		{"2.2.20", "Deny log on through Remote Desktop Services"},
		{"2.2.21", "Enable computer and user accounts to be trusted for delegation"},
		{"2.2.22", "Force shutdown from a remote system"},
		{"2.2.23", "Generate security audits"},
		{"2.2.24", "Impersonate a client after authentication"},
		{"2.2.25", "Increase scheduling priority"},
		{"2.2.26", "Load and unload device drivers"},
		{"2.2.27", "Lock pages in memory"},
		{"2.2.28", "Log on as a batch job"},
		{"2.2.29", "Log on as a service"},
		{"2.2.30", "Manage auditing and security log"},
		{"2.2.31", "Modify an object label"},
		{"2.2.32", "Modify firmware environment values"},
		{"2.2.33", "Perform volume maintenance tasks"},
		{"2.2.34", "Profile single process"},
		{"2.2.35", "Profile system performance"},
		{"2.2.36", "Replace a process level token"},
		{"2.2.37", "Restore files and directories"},
		{"2.2.38", "Shut down the system"},
		{"2.2.39", "Take ownership of files or other objects"},
		// Section 2.3 — Security Options
		{"2.3.1.1", "Accounts: Block Microsoft accounts"},
		{"2.3.1.2", "Accounts: Guest account status"},
		{"2.3.1.3", "Accounts: Limit local account use of blank passwords to console logon only"},
		{"2.3.1.4", "Accounts: Rename administrator account"},
		{"2.3.1.5", "Accounts: Rename guest account"},
		{"2.3.2.1", "Audit: Force audit policy subcategory settings to override audit policy category settings"},
		{"2.3.2.2", "Audit: Shut down system immediately if unable to log security audits"},
		{"2.3.4.1", "Devices: Allowed to format and eject removable media"},
		{"2.3.4.2", "Devices: Prevent users from installing printer drivers"},
		{"2.3.6.1", "Domain member: Digitally encrypt or sign secure channel data (always)"},
		{"2.3.6.2", "Domain member: Digitally encrypt secure channel data (when possible)"},
		{"2.3.6.3", "Domain member: Digitally sign secure channel data (when possible)"},
		{"2.3.6.4", "Domain member: Disable machine account password changes"},
		{"2.3.6.5", "Domain member: Maximum machine account password age"},
		{"2.3.6.6", "Domain member: Require strong (Windows 2000 or later) session key"},
		{"2.3.7.1", "Interactive logon: Do not require CTRL+ALT+DEL"},
		{"2.3.7.2", "Interactive logon: Don't display last signed-in"},
		{"2.3.7.3", "Interactive logon: Machine account lockout threshold"},
		{"2.3.7.4", "Interactive logon: Machine inactivity limit"},
		{"2.3.7.5", "Interactive logon: Message text for users attempting to log on"},
		{"2.3.7.6", "Interactive logon: Message title for users attempting to log on"},
		{"2.3.7.7", "Interactive logon: Number of previous logons to cache"},
		{"2.3.7.8", "Interactive logon: Prompt user to change password before expiration"},
		{"2.3.7.9", "Interactive logon: Smart card removal behavior"},
		{"2.3.8.1", "Microsoft network client: Digitally sign communications (always)"},
		{"2.3.8.2", "Microsoft network client: Digitally sign communications (if server agrees)"},
		{"2.3.8.3", "Microsoft network client: Send unencrypted password to third-party SMB servers"},
		{"2.3.9.1", "Microsoft network server: Amount of idle time required before suspending session"},
		{"2.3.9.2", "Microsoft network server: Digitally sign communications (always)"},
		{"2.3.9.3", "Microsoft network server: Digitally sign communications (if client agrees)"},
		{"2.3.9.4", "Microsoft network server: Disconnect clients when logon hours expire"},
		{"2.3.9.5", "Microsoft network server: Server SPN target name validation level"},
		{"2.3.10.1", "Network access: Allow anonymous SID/Name translation"},
		{"2.3.10.2", "Network access: Do not allow anonymous enumeration of SAM accounts"},
		{"2.3.10.3", "Network access: Do not allow anonymous enumeration of SAM accounts and shares"},
		{"2.3.10.4", "Network access: Do not allow storage of passwords and credentials for network authentication"},
		{"2.3.10.5", "Network access: Let Everyone permissions apply to anonymous users"},
		{"2.3.10.6", "Network access: Named Pipes that can be accessed anonymously"},
		{"2.3.10.7", "Network access: Remotely accessible registry paths"},
		{"2.3.10.8", "Network access: Remotely accessible registry paths and sub-paths"},
		{"2.3.10.9", "Network access: Restrict anonymous access to Named Pipes and Shares"},
		{"2.3.10.10", "Network access: Restrict clients allowed to make remote calls to SAM"},
		{"2.3.10.11", "Network access: Shares that can be accessed anonymously"},
		{"2.3.10.12", "Network access: Sharing and security model for local accounts"},
		{"2.3.11.1", "Network security: Allow Local System to use computer identity for NTLM"},
		{"2.3.11.2", "Network security: Allow LocalSystem NULL session fallback"},
		{"2.3.11.3", "Network security: Allow PKU2U authentication requests to this computer to use online identities"},
		{"2.3.11.4", "Network security: Configure encryption types allowed for Kerberos"},
		{"2.3.11.5", "Network security: Do not store LAN Manager hash value on next password change"},
		{"2.3.11.6", "Network security: Force logoff when logon hours expire"},
		{"2.3.11.7", "Network security: LAN Manager authentication level"},
		{"2.3.11.8", "Network security: LDAP client signing requirements"},
		{"2.3.11.9", "Network security: Minimum session security for NTLM SSP based clients"},
		{"2.3.11.10", "Network security: Minimum session security for NTLM SSP based servers"},
		{"2.3.13.1", "Shutdown: Allow system to be shut down without having to log on"},
		{"2.3.15.1", "System objects: Require case insensitivity for non-Windows subsystems"},
		{"2.3.15.2", "System objects: Strengthen default permissions of internal system objects"},
		{"2.3.17.1", "User Account Control: Admin Approval Mode for the Built-in Administrator account"},
		{"2.3.17.2", "User Account Control: Behavior of the elevation prompt for administrators"},
		{"2.3.17.3", "User Account Control: Behavior of the elevation prompt for standard users"},
		{"2.3.17.4", "User Account Control: Detect application installations and prompt for elevation"},
		{"2.3.17.5", "User Account Control: Only elevate UIAccess applications that are installed in secure locations"},
		{"2.3.17.6", "User Account Control: Run all administrators in Admin Approval Mode"},
		{"2.3.17.7", "User Account Control: Switch to the secure desktop when prompting for elevation"},
		{"2.3.17.8", "User Account Control: Virtualize file and registry write failures to per-user locations"},
		// Section 5 — System Services (Member Server)
		{"5.1", "Bluetooth Audio Gateway Service is set to Disabled"},
		{"5.2", "Bluetooth Support Service is set to Disabled"},
		{"5.3", "Computer Browser is set to Disabled or Not Installed"},
		{"5.4", "Downloaded Maps Manager is set to Disabled"},
		{"5.5", "Geolocation Service is set to Disabled"},
		{"5.6", "IIS Admin Service is set to Disabled or Not Installed"},
		{"5.7", "Infrared monitor service is set to Disabled or Not Installed"},
		{"5.8", "Internet Connection Sharing (ICS) is set to Disabled"},
		{"5.9", "Link-Layer Topology Discovery Mapper is set to Disabled"},
		{"5.10", "LxssManager is set to Disabled or Not Installed"},
		{"5.11", "Microsoft FTP Service is set to Disabled or Not Installed"},
		{"5.12", "Microsoft iSCSI Initiator Service is set to Disabled"},
		{"5.13", "OpenSSH SSH Server is set to Disabled or Not Installed"},
		{"5.14", "Peer Name Resolution Protocol is set to Disabled"},
		{"5.15", "Peer Networking Grouping is set to Disabled"},
		{"5.16", "Peer Networking Identity Manager is set to Disabled"},
		{"5.17", "PNRP Machine Name Publication Service is set to Disabled"},
		{"5.18", "Print Spooler is set to Disabled (MS only)"},
		{"5.19", "Problem Reports and Solutions Control Panel Support is set to Disabled"},
		{"5.20", "Remote Access Auto Connection Manager is set to Disabled"},
		{"5.21", "Remote Desktop Configuration is set to Disabled"},
		{"5.22", "Remote Desktop Services is set to Disabled"},
		{"5.23", "Remote Desktop Services UserMode Port Redirector is set to Disabled"},
		{"5.24", "Remote Procedure Call (RPC) Locator is set to Disabled"},
		{"5.25", "Remote Registry is set to Disabled"},
		{"5.26", "Routing and Remote Access is set to Disabled"},
		{"5.27", "Server is not set to Disabled (MS only)"},
		{"5.28", "Simple TCP/IP Services is set to Disabled or Not Installed"},
		{"5.29", "SNMP Service is set to Disabled or Not Installed"},
		{"5.30", "Special Administration Console Helper is set to Disabled"},
		{"5.31", "SSDP Discovery is set to Disabled"},
		{"5.32", "UPnP Device Host is set to Disabled"},
		{"5.33", "Web Management Service is set to Disabled or Not Installed"},
		{"5.34", "Windows Error Reporting Service is set to Disabled"},
		{"5.35", "Windows Event Collector is set to Disabled"},
		{"5.36", "Windows Media Player Network Sharing Service is set to Disabled"},
		{"5.37", "Windows Mobile Hotspot Service is set to Disabled"},
		{"5.38", "Windows Push Notifications System Service is set to Disabled"},
		{"5.39", "Windows PushToInstall Service is set to Disabled"},
		{"5.40", "Windows Remote Management is set to Disabled"},
		{"5.41", "World Wide Web Publishing Service is set to Disabled or Not Installed"},
		{"5.42", "Xbox Accessory Management Service is set to Disabled"},
		{"5.43", "Xbox Live Auth Manager is set to Disabled"},
		{"5.44", "Xbox Live Game Save is set to Disabled"},
		{"5.45", "Xbox Live Networking Service is set to Disabled"},
		// Section 9 — Windows Defender Firewall with Advanced Security
		{"9.1.1", "Windows Firewall: Domain: Firewall state"},
		{"9.1.2", "Windows Firewall: Domain: Inbound connections"},
		{"9.1.3", "Windows Firewall: Domain: Outbound connections"},
		{"9.1.4", "Windows Firewall: Domain: Settings: Display a notification"},
		{"9.1.5", "Windows Firewall: Domain: Logging: Name"},
		{"9.1.6", "Windows Firewall: Domain: Logging: Size limit (KB)"},
		{"9.1.7", "Windows Firewall: Domain: Logging: Log dropped packets"},
		{"9.1.8", "Windows Firewall: Domain: Logging: Log successful connections"},
		{"9.2.1", "Windows Firewall: Private: Firewall state"},
		{"9.2.2", "Windows Firewall: Private: Inbound connections"},
		{"9.2.3", "Windows Firewall: Private: Outbound connections"},
		{"9.2.4", "Windows Firewall: Private: Settings: Display a notification"},
		{"9.2.5", "Windows Firewall: Private: Logging: Name"},
		{"9.2.6", "Windows Firewall: Private: Logging: Size limit (KB)"},
		{"9.2.7", "Windows Firewall: Private: Logging: Log dropped packets"},
		{"9.2.8", "Windows Firewall: Private: Logging: Log successful connections"},
		{"9.3.1", "Windows Firewall: Public: Firewall state"},
		{"9.3.2", "Windows Firewall: Public: Inbound connections"},
		{"9.3.3", "Windows Firewall: Public: Outbound connections"},
		{"9.3.4", "Windows Firewall: Public: Settings: Display a notification"},
		{"9.3.5", "Windows Firewall: Public: Settings: Apply local firewall rules"},
		{"9.3.6", "Windows Firewall: Public: Settings: Apply local connection security rules"},
		{"9.3.7", "Windows Firewall: Public: Logging: Name"},
		{"9.3.8", "Windows Firewall: Public: Logging: Size limit (KB)"},
		{"9.3.9", "Windows Firewall: Public: Logging: Log dropped packets"},
		{"9.3.10", "Windows Firewall: Public: Logging: Log successful connections"},
		// Section 17 — Advanced Audit Policy Configuration
		{"17.1.1", "Audit Credential Validation"},
		{"17.2.1", "Audit Application Group Management"},
		{"17.2.2", "Audit Security Group Management"},
		{"17.2.3", "Audit User Account Management"},
		{"17.3.1", "Audit PNP Activity"},
		{"17.3.2", "Audit Process Creation"},
		{"17.5.1", "Audit Account Lockout"},
		{"17.5.2", "Audit Group Membership"},
		{"17.5.3", "Audit Logoff"},
		{"17.5.4", "Audit Logon"},
		{"17.5.5", "Audit Other Logon/Logoff Events"},
		{"17.5.6", "Audit Special Logon"},
		{"17.6.1", "Audit Detailed File Share"},
		{"17.6.2", "Audit File Share"},
		{"17.6.3", "Audit Other Object Access Events"},
		{"17.6.4", "Audit Removable Storage"},
		{"17.7.1", "Audit Audit Policy Change"},
		{"17.7.2", "Audit Authentication Policy Change"},
		{"17.7.3", "Audit Authorization Policy Change"},
		{"17.7.4", "Audit MPSSVC Rule-Level Policy Change"},
		{"17.7.5", "Audit Other Policy Change Events"},
		{"17.8.1", "Audit Sensitive Privilege Use"},
		{"17.9.1", "Audit IPsec Driver"},
		{"17.9.2", "Audit Other System Events"},
		{"17.9.3", "Audit Security State Change"},
		{"17.9.4", "Audit Security System Extension"},
		{"17.9.5", "Audit System Integrity"},
		// Section 18 — Administrative Templates: Computer
		{"18.1.1.1", "Prevent enabling lock screen camera"},
		{"18.1.1.2", "Prevent enabling lock screen slide show"},
		{"18.1.2.2", "Allow users to enable online speech recognition services"},
		{"18.1.3", "Allow Online Tips"},
		{"18.2.1", "LAPS AdmPwd GPO Extension is installed"},
		{"18.2.2", "Do not allow password expiration time longer than required by policy"},
		{"18.2.3", "Enable Local Admin Password Management"},
		{"18.2.4", "Password Settings: Password Complexity"},
		{"18.2.5", "Password Settings: Password Length"},
		{"18.2.6", "Password Settings: Password Age"},
		{"18.3.1", "Apply UAC restrictions to local accounts on network logons (MS Security Guide)"},
		{"18.3.2", "Configure SMB v1 client driver"},
		{"18.3.3", "Configure SMB v1 server"},
		{"18.3.4", "Enable Structured Exception Handling Overwrite Protection (SEHOP)"},
		{"18.3.5", "Limits print driver installation to Administrators"},
		{"18.3.6", "NetBT NodeType configuration"},
		{"18.3.7", "WDigest Authentication"},
		{"18.4.1", "MSS: AutoAdminLogon"},
		{"18.4.2", "MSS: DisableIPSourceRouting IPv6"},
		{"18.4.3", "MSS: DisableIPSourceRouting IPv4"},
		{"18.4.4", "MSS: DisableSavePassword"},
		{"18.4.5", "MSS: EnableICMPRedirect"},
		{"18.4.6", "MSS: KeepAliveTime"},
		{"18.4.7", "MSS: NoNameReleaseOnDemand"},
		{"18.4.8", "MSS: PerformRouterDiscovery"},
		{"18.4.9", "MSS: SafeDllSearchMode"},
		{"18.4.10", "MSS: ScreenSaverGracePeriod"},
		{"18.4.11", "MSS: TcpMaxDataRetransmissions IPv6"},
		{"18.4.12", "MSS: TcpMaxDataRetransmissions IPv4"},
		{"18.4.13", "MSS: WarningLevel"},
		{"18.5.4.1", "Configure DNS over HTTPS (DoH) name resolution"},
		{"18.5.4.2", "Turn off multicast name resolution"},
		{"18.5.5.1", "Enable Font Providers"},
		{"18.5.8.1", "Enable insecure guest logons"},
		{"18.5.9.1", "Turn on Mapper I/O (LLTDIO) driver"},
		{"18.5.9.2", "Turn on Responder (RSPNDR) driver"},
		{"18.5.10.2", "Turn off Microsoft Peer-to-Peer Networking Services"},
		{"18.5.11.2", "Prohibit installation and configuration of Network Bridge"},
		{"18.5.11.3", "Prohibit use of Internet Connection Sharing on your DNS domain network"},
		{"18.5.11.4", "Require domain users to elevate when setting a network's location"},
		{"18.5.14.1", "Hardened UNC Paths"},
		{"18.5.19.2.1", "Disable IPv6 source routing"},
		{"18.5.20.1", "Configuration of wireless settings using Windows Connect Now"},
		{"18.5.20.2", "Prohibit access of the Windows Connect Now wizards"},
		{"18.5.21.1", "Minimize the number of simultaneous connections to the Internet or a Windows Domain"},
		{"18.5.21.2", "Prohibit connection to non-domain networks when connected to domain authenticated network"},
		{"18.5.23.2.1", "Enable Windows NTP Client"},
		{"18.5.23.2.2", "Enable Windows NTP Server"},
		{"18.6.1", "Allow Print Spooler to accept client connections"},
		{"18.6.2", "Configure Redirection Guard"},
		{"18.6.3", "Configure RPC connection settings: Protocol to use for outgoing RPC connections"},
		{"18.6.4", "Configure RPC connection settings: Use authentication for outgoing RPC connections"},
		{"18.6.5", "Configure RPC listener settings: Protocols to allow for incoming RPC connections"},
		{"18.6.6", "Configure RPC listener settings: Authentication protocol to use for incoming RPC connections"},
		{"18.6.7", "Configure RPC over TCP port"},
		{"18.6.8", "Limits print driver installation to Administrators"},
		{"18.6.9", "Manage processing of Queue-specific files"},
		{"18.6.10", "Point and Print Restrictions: When installing drivers for a new connection"},
		{"18.6.11", "Point and Print Restrictions: When updating drivers for an existing connection"},
		{"18.7.1.1", "Turn off notifications network usage"},
		{"18.8.3.1", "Include command line in process creation events"},
		{"18.8.4.1", "Encryption Oracle Remediation"},
		{"18.8.4.2", "Remote host allows delegation of non-exportable credentials"},
		{"18.8.5.1", "Turn On Virtualization Based Security"},
		{"18.8.5.2", "Select Platform Security Level"},
		{"18.8.5.3", "Virtualization Based Protection of Code Integrity"},
		{"18.8.5.4", "Require UEFI Memory Attributes Table"},
		{"18.8.5.5", "Credential Guard Configuration"},
		{"18.8.5.6", "Secure Launch Configuration"},
		{"18.8.5.7", "Kernel-mode Hardware-enforced Stack Protection"},
		{"18.8.7.1.1", "Prevent device metadata retrieval from the Internet"},
		{"18.8.14.1", "Boot-Start Driver Initialization Policy"},
		{"18.8.21.2", "Configure registry policy processing: Do not apply during periodic background processing"},
		{"18.8.21.3", "Configure registry policy processing: Process even if the Group Policy objects have not changed"},
		{"18.8.21.4", "Continue experiences on this device"},
		{"18.8.21.5", "Turn off background refresh of Group Policy"},
		{"18.8.22.1.1", "Turn off access to the Store"},
		{"18.8.22.1.2", "Turn off downloading of print drivers over HTTP"},
		{"18.8.22.1.3", "Turn off handwriting personalization data sharing"},
		{"18.8.22.1.4", "Turn off handwriting recognition error reporting"},
		{"18.8.22.1.5", "Turn off Internet Connection Wizard if URL connection is referring to Microsoft.com"},
		{"18.8.22.1.6", "Turn off Internet download for Web publishing and online ordering wizards"},
		{"18.8.22.1.7", "Turn off printing over HTTP"},
		{"18.8.22.1.8", "Turn off Registration if URL connection is referring to Microsoft.com"},
		{"18.8.22.1.9", "Turn off Search Companion content file updates"},
		{"18.8.22.1.10", "Turn off the Order Prints picture task"},
		{"18.8.22.1.11", "Turn off the Publish to Web task for files and folders"},
		{"18.8.22.1.12", "Turn off the Windows Messenger Customer Experience Improvement Program"},
		{"18.8.22.1.13", "Turn off Windows Customer Experience Improvement Program"},
		{"18.8.22.1.14", "Turn off Windows Error Reporting"},
		{"18.8.25.1", "Block user from showing account details on sign-in"},
		{"18.8.25.2", "Do not display network selection UI"},
		{"18.8.25.3", "Do not enumerate connected users on domain-joined computers"},
		{"18.8.25.4", "Enumerate local users on domain-joined computers"},
		{"18.8.25.5", "Turn off app notifications on the lock screen"},
		{"18.8.25.6", "Turn off picture password sign-in"},
		{"18.8.25.7", "Turn on convenience PIN sign-in"},
		{"18.8.27.1", "Allow Clipboard synchronization across devices"},
		{"18.8.27.2", "Allow upload of User Activities"},
		{"18.8.28.1", "Allow network connectivity during connected-standby (on battery)"},
		{"18.8.28.2", "Allow network connectivity during connected-standby (plugged in)"},
		{"18.8.28.3", "Require a password when a computer wakes (on battery)"},
		{"18.8.28.4", "Require a password when a computer wakes (plugged in)"},
		{"18.8.31.1", "Configure Offer Remote Assistance"},
		{"18.8.31.2", "Configure Solicited Remote Assistance"},
		{"18.8.32.1", "Enable RPC Endpoint Mapper Client Authentication"},
		{"18.8.32.2", "Restrict Unauthenticated RPC clients"},
		{"18.9.4.1", "Allow a Windows app to share application data between users"},
		{"18.9.4.2", "Prevent non-admin users from installing packaged Windows apps"},
		{"18.9.5.1", "Let Windows apps activate with voice while the system is locked"},
		{"18.9.6.1", "Allow Microsoft accounts to be optional"},
		{"18.9.7.1.1", "Configure default consent"},
		{"18.9.7.2", "Disallow Autoplay for non-volume devices"},
		{"18.9.7.3", "Set the default behavior for AutoRun"},
		{"18.9.7.4", "Turn off Autoplay"},
		{"18.9.10.1.1", "Configure enhanced anti-spoofing"},
		{"18.9.11.1.1", "Allow Use of Camera"},
		{"18.9.13.1", "Turn off Microsoft consumer experiences"},
		{"18.9.14.1", "Require pin for pairing"},
		{"18.9.15.1", "Do not display the password reveal button"},
		{"18.9.15.2", "Enumerate administrator accounts on elevation"},
		{"18.9.15.3", "Prevent the use of security questions for local accounts"},
		{"18.9.16.1", "Allow Diagnostic Data"},
		{"18.9.16.2", "Configure Authenticated Proxy usage for the Connected User Experience and Telemetry service"},
		{"18.9.16.3", "Disable OneSettings Downloads"},
		{"18.9.16.4", "Do not show feedback notifications"},
		{"18.9.16.5", "Enable OneSettings Auditing"},
		{"18.9.16.6", "Limit Diagnostic Log Collection"},
		{"18.9.16.7", "Limit Dump Collection"},
		{"18.9.16.8", "Toggle user control over Insider builds"},
		{"18.9.17.1", "Download Mode"},
		{"18.9.20.1.1", "Application: Control Event Log behavior when the log file reaches its maximum size"},
		{"18.9.20.1.2", "Application: Specify the maximum log file size (KB)"},
		{"18.9.20.2.1", "Security: Control Event Log behavior when the log file reaches its maximum size"},
		{"18.9.20.2.2", "Security: Specify the maximum log file size (KB)"},
		{"18.9.20.3.1", "Setup: Control Event Log behavior when the log file reaches its maximum size"},
		{"18.9.20.3.2", "Setup: Specify the maximum log file size (KB)"},
		{"18.9.20.4.1", "System: Control Event Log behavior when the log file reaches its maximum size"},
		{"18.9.20.4.2", "System: Specify the maximum log file size (KB)"},
		{"18.9.31.2", "Turn off Data Execution Prevention for Explorer"},
		{"18.9.31.3", "Turn off heap termination on corruption"},
		{"18.9.31.4", "Turn off shell protocol protected mode"},
		{"18.9.41.1", "Turn off location"},
		{"18.9.45.1", "Allow Message Service Cloud Sync"},
		{"18.9.46.1", "Block all consumer Microsoft account user authentication"},
		{"18.9.47.1", "Configure local setting override for reporting to Microsoft MAPS"},
		{"18.9.47.2.1", "Configure Attack Surface Reduction rules"},
		{"18.9.47.2.2", "Configure ASR rules"},
		{"18.9.47.3.1", "Prevent users and apps from accessing dangerous websites"},
		{"18.9.47.4.1", "Enable Network Protection"},
		{"18.9.47.5.1", "Scan all downloaded files and attachments"},
		{"18.9.47.5.2", "Turn off real-time protection"},
		{"18.9.47.5.3", "Turn on behavior monitoring"},
		{"18.9.47.5.4", "Turn on script scanning"},
		{"18.9.47.6", "Configure detection for potentially unwanted applications"},
		{"18.9.47.7", "Turn off Microsoft Defender Antivirus"},
		{"18.9.65.2", "Allow Cortana"},
		{"18.9.65.3", "Allow Cortana above lock screen"},
		{"18.9.65.4", "Allow indexing of encrypted files"},
		{"18.9.65.5", "Allow search and Cortana to use location"},
		{"18.9.66.1", "Turn off KMS Client Online AVS Validation"},
		{"18.9.85.1.1", "Manage preview builds"},
		{"18.9.85.1.2", "Select when Preview Builds and Feature Updates are received"},
		{"18.9.85.1.3", "Select when Quality Updates are received"},
		{"18.9.85.1.4", "Set Update Notification Level"},
		{"18.9.85.2.1", "No auto-restart with logged on users for scheduled automatic updates installations"},
		{"18.9.85.2.2", "Configure Automatic Updates"},
		{"18.9.85.2.3", "Configure Automatic Updates: Scheduled install day"},
		{"18.9.85.2.4", "Configure Automatic Updates: Scheduled install time"},
		// Section 19 — Administrative Templates: User
		{"19.1.3.1", "Enable screen saver"},
		{"19.1.3.2", "Force specific screen saver: Screen saver executable name"},
		{"19.1.3.3", "Password protect the screen saver"},
		{"19.1.3.4", "Screen saver timeout"},
		{"19.5.1.1", "Turn off toast notifications on the lock screen"},
		{"19.7.4.1", "Do not preserve zone information in file attachments"},
		{"19.7.4.2", "Notify antivirus programs when opening attachments"},
		{"19.7.7.1", "Configure Windows spotlight on lock screen"},
		{"19.7.7.2", "Do not suggest third-party content in Windows spotlight"},
		{"19.7.7.3", "Do not use diagnostic data for tailored experiences"},
		{"19.7.7.4", "Turn off all Windows spotlight features"},
		{"19.7.8.1", "Prevent users from sharing files within their profile"},
		{"19.7.26.1", "Prevent Codec Download"},
	})
}

// ---------- CIS Ubuntu ----------

// buildCISUbuntuFindings returns the CIS Ubuntu Linux Benchmark
// v2.0.0 Level 1 — Server profile control catalog. CIS-licensed under
// the CIS Benchmarks End User License Agreement: only IDs and short
// titles are reproduced — descriptive text, audit procedures, and
// remediation guidance from the benchmark PDF are never reproduced here.
func (h *ReportHandler) buildCISUbuntuFindings() []ControlFinding {
	return catalog([][2]string{
		// Section 1 — Initial Setup
		{"1.1.1.1", "Ensure cramfs kernel module is not available"},
		{"1.1.1.2", "Ensure freevxfs kernel module is not available"},
		{"1.1.1.3", "Ensure hfs kernel module is not available"},
		{"1.1.1.4", "Ensure hfsplus kernel module is not available"},
		{"1.1.1.5", "Ensure jffs2 kernel module is not available"},
		{"1.1.1.8", "Ensure usb-storage kernel module is not available"},
		{"1.1.2.1.1", "Ensure /tmp is a separate partition"},
		{"1.1.2.1.2", "Ensure nodev option set on /tmp partition"},
		{"1.1.2.1.3", "Ensure nosuid option set on /tmp partition"},
		{"1.1.2.1.4", "Ensure noexec option set on /tmp partition"},
		{"1.1.2.2.1", "Ensure /dev/shm is a separate partition"},
		{"1.1.2.2.2", "Ensure nodev option set on /dev/shm partition"},
		{"1.1.2.2.3", "Ensure nosuid option set on /dev/shm partition"},
		{"1.1.2.2.4", "Ensure noexec option set on /dev/shm partition"},
		{"1.1.2.3.2", "Ensure nodev option set on /home partition"},
		{"1.1.2.3.3", "Ensure nosuid option set on /home partition"},
		{"1.1.2.4.2", "Ensure nodev option set on /var partition"},
		{"1.1.2.4.3", "Ensure nosuid option set on /var partition"},
		{"1.1.2.5.2", "Ensure nodev option set on /var/tmp partition"},
		{"1.1.2.5.3", "Ensure nosuid option set on /var/tmp partition"},
		{"1.1.2.5.4", "Ensure noexec option set on /var/tmp partition"},
		{"1.1.2.6.2", "Ensure nodev option set on /var/log partition"},
		{"1.1.2.6.3", "Ensure nosuid option set on /var/log partition"},
		{"1.1.2.6.4", "Ensure noexec option set on /var/log partition"},
		{"1.1.2.7.2", "Ensure nodev option set on /var/log/audit partition"},
		{"1.1.2.7.3", "Ensure nosuid option set on /var/log/audit partition"},
		{"1.1.2.7.4", "Ensure noexec option set on /var/log/audit partition"},
		{"1.2.1.1", "Ensure GPG keys are configured"},
		{"1.2.1.2", "Ensure package manager repositories are configured"},
		{"1.2.2.1", "Ensure updates, patches, and additional security software are installed"},
		{"1.3.1.1", "Ensure AppArmor is installed"},
		{"1.3.1.2", "Ensure AppArmor is enabled in the bootloader configuration"},
		{"1.3.1.3", "Ensure all AppArmor Profiles are in enforce or complain mode"},
		{"1.4.1", "Ensure bootloader password is set"},
		{"1.4.2", "Ensure access to bootloader config is configured"},
		{"1.5.1", "Ensure address space layout randomization is enabled"},
		{"1.5.2", "Ensure ptrace_scope is restricted"},
		{"1.5.3", "Ensure core dumps are restricted"},
		{"1.5.4", "Ensure prelink is not installed"},
		{"1.5.5", "Ensure Automatic Error Reporting is not enabled"},
		{"1.6.1", "Ensure message of the day is configured properly"},
		{"1.6.2", "Ensure local login warning banner is configured properly"},
		{"1.6.3", "Ensure remote login warning banner is configured properly"},
		{"1.6.4", "Ensure access to /etc/motd is configured"},
		{"1.6.5", "Ensure access to /etc/issue is configured"},
		{"1.6.6", "Ensure access to /etc/issue.net is configured"},
		{"1.7.2", "Ensure GDM login banner is configured"},
		{"1.7.3", "Ensure GDM disable-user-list option is enabled"},
		{"1.7.4", "Ensure GDM screen locks when the user is idle"},
		{"1.7.5", "Ensure GDM screen locks cannot be overridden"},
		{"1.7.6", "Ensure GDM automatic mounting of removable media is disabled"},
		{"1.7.7", "Ensure GDM disabling automatic mounting of removable media is not overridden"},
		{"1.7.8", "Ensure GDM autorun-never is enabled"},
		{"1.7.9", "Ensure GDM autorun-never is not overridden"},
		{"1.7.10", "Ensure XDMCP is not enabled"},
		// Section 2 — Services
		{"2.1.1", "Ensure autofs services are not in use"},
		{"2.1.2", "Ensure avahi daemon services are not in use"},
		{"2.1.3", "Ensure dhcp server services are not in use"},
		{"2.1.4", "Ensure dns server services are not in use"},
		{"2.1.5", "Ensure dnsmasq services are not in use"},
		{"2.1.6", "Ensure ftp server services are not in use"},
		{"2.1.7", "Ensure ldap server services are not in use"},
		{"2.1.8", "Ensure message access server services are not in use"},
		{"2.1.9", "Ensure network file system services are not in use"},
		{"2.1.10", "Ensure nis server services are not in use"},
		{"2.1.11", "Ensure print server services are not in use"},
		{"2.1.12", "Ensure rpcbind services are not in use"},
		{"2.1.13", "Ensure rsync services are not in use"},
		{"2.1.14", "Ensure samba file server services are not in use"},
		{"2.1.15", "Ensure snmp services are not in use"},
		{"2.1.16", "Ensure tftp server services are not in use"},
		{"2.1.17", "Ensure web proxy server services are not in use"},
		{"2.1.18", "Ensure web server services are not in use"},
		{"2.1.19", "Ensure xinetd services are not in use"},
		{"2.1.21", "Ensure mail transfer agent is configured for local-only mode"},
		{"2.1.22", "Ensure only approved services are listening on a network interface"},
		{"2.2.1", "Ensure NIS Client is not installed"},
		{"2.2.2", "Ensure rsh client is not installed"},
		{"2.2.3", "Ensure talk client is not installed"},
		{"2.2.4", "Ensure telnet client is not installed"},
		{"2.2.5", "Ensure ldap client is not installed"},
		{"2.2.6", "Ensure ftp client is not installed"},
		{"2.3.1.1", "Ensure a single time synchronization daemon is in use"},
		{"2.3.2.1", "Ensure systemd-timesyncd configured with authorized timeserver"},
		{"2.3.2.2", "Ensure systemd-timesyncd is enabled and running"},
		{"2.3.3.1", "Ensure chrony is configured with authorized timeserver"},
		{"2.3.3.2", "Ensure chrony is running as user _chrony"},
		{"2.3.3.3", "Ensure chrony is enabled and running"},
		{"2.4.1.1", "Ensure cron daemon is enabled and active"},
		{"2.4.1.2", "Ensure permissions on /etc/crontab are configured"},
		{"2.4.1.3", "Ensure permissions on /etc/cron.hourly are configured"},
		{"2.4.1.4", "Ensure permissions on /etc/cron.daily are configured"},
		{"2.4.1.5", "Ensure permissions on /etc/cron.weekly are configured"},
		{"2.4.1.6", "Ensure permissions on /etc/cron.monthly are configured"},
		{"2.4.1.7", "Ensure permissions on /etc/cron.d are configured"},
		{"2.4.1.8", "Ensure crontab is restricted to authorized users"},
		{"2.4.2.1", "Ensure at is restricted to authorized users"},
		// Section 3 — Network
		{"3.1.1", "Ensure IPv6 status is identified"},
		{"3.1.2", "Ensure wireless interfaces are disabled"},
		{"3.1.3", "Ensure bluetooth services are not in use"},
		{"3.3.1", "Ensure ip forwarding is disabled"},
		{"3.3.2", "Ensure packet redirect sending is disabled"},
		{"3.3.3", "Ensure bogus icmp responses are ignored"},
		{"3.3.4", "Ensure broadcast icmp requests are ignored"},
		{"3.3.5", "Ensure icmp redirects are not accepted"},
		{"3.3.6", "Ensure secure icmp redirects are not accepted"},
		{"3.3.7", "Ensure reverse path filtering is enabled"},
		{"3.3.8", "Ensure source routed packets are not accepted"},
		{"3.3.9", "Ensure suspicious packets are logged"},
		{"3.3.10", "Ensure tcp syn cookies is enabled"},
		{"3.3.11", "Ensure ipv6 router advertisements are not accepted"},
		// Section 4 — Host Based Firewall
		{"4.1.1", "Ensure ufw is installed"},
		{"4.1.2", "Ensure iptables-persistent is not installed with ufw"},
		{"4.1.3", "Ensure ufw service is enabled"},
		{"4.1.4", "Ensure ufw loopback traffic is configured"},
		{"4.1.5", "Ensure ufw outbound connections are configured"},
		{"4.1.6", "Ensure ufw firewall rules exist for all open ports"},
		{"4.1.7", "Ensure ufw default deny firewall policy"},
		{"4.2.1", "Ensure nftables is installed"},
		{"4.2.2", "Ensure ufw is uninstalled or disabled with nftables"},
		{"4.2.3", "Ensure iptables are flushed with nftables"},
		{"4.2.4", "Ensure a nftables table exists"},
		{"4.2.5", "Ensure nftables base chains exist"},
		{"4.2.6", "Ensure nftables loopback traffic is configured"},
		{"4.2.7", "Ensure nftables outbound and established connections are configured"},
		{"4.2.8", "Ensure nftables default deny firewall policy"},
		{"4.2.9", "Ensure nftables service is enabled"},
		{"4.2.10", "Ensure nftables rules are permanent"},
		{"4.3.1.1", "Ensure iptables packages are installed"},
		{"4.3.1.2", "Ensure nftables is not installed with iptables"},
		{"4.3.1.3", "Ensure ufw is uninstalled or disabled with iptables"},
		{"4.3.2.1", "Ensure iptables default deny firewall policy"},
		{"4.3.2.2", "Ensure iptables loopback traffic is configured"},
		{"4.3.2.3", "Ensure iptables outbound and established connections are configured"},
		{"4.3.2.4", "Ensure iptables firewall rules exist for all open ports"},
		{"4.3.3.1", "Ensure ip6tables default deny firewall policy"},
		{"4.3.3.2", "Ensure ip6tables loopback traffic is configured"},
		{"4.3.3.3", "Ensure ip6tables outbound and established connections are configured"},
		{"4.3.3.4", "Ensure ip6tables firewall rules exist for all open ports"},
		// Section 5 — Access, Authentication and Authorization
		{"5.1.1", "Ensure permissions on /etc/ssh/sshd_config are configured"},
		{"5.1.2", "Ensure permissions on SSH private host key files are configured"},
		{"5.1.3", "Ensure permissions on SSH public host key files are configured"},
		{"5.1.4", "Ensure sshd access is configured"},
		{"5.1.5", "Ensure sshd Banner is configured"},
		{"5.1.6", "Ensure sshd Ciphers are configured"},
		{"5.1.7", "Ensure sshd ClientAliveInterval and ClientAliveCountMax are configured"},
		{"5.1.10", "Ensure sshd HostbasedAuthentication is disabled"},
		{"5.1.11", "Ensure sshd IgnoreRhosts is enabled"},
		{"5.1.12", "Ensure sshd KexAlgorithms is configured"},
		{"5.1.13", "Ensure sshd LoginGraceTime is configured"},
		{"5.1.14", "Ensure sshd LogLevel is configured"},
		{"5.1.15", "Ensure sshd MACs are configured"},
		{"5.1.16", "Ensure sshd MaxAuthTries is configured"},
		{"5.1.17", "Ensure sshd MaxSessions is configured"},
		{"5.1.18", "Ensure sshd MaxStartups is configured"},
		{"5.1.19", "Ensure sshd PermitEmptyPasswords is disabled"},
		{"5.1.20", "Ensure sshd PermitRootLogin is disabled"},
		{"5.1.21", "Ensure sshd PermitUserEnvironment is disabled"},
		{"5.1.22", "Ensure sshd UsePAM is enabled"},
		{"5.2.1", "Ensure sudo is installed"},
		{"5.2.2", "Ensure sudo commands use pty"},
		{"5.2.3", "Ensure sudo log file exists"},
		{"5.2.5", "Ensure re-authentication for privilege escalation is not disabled globally"},
		{"5.2.6", "Ensure sudo authentication timeout is configured correctly"},
		{"5.2.7", "Ensure access to the su command is restricted"},
		{"5.3.1.1", "Ensure latest version of pam is installed"},
		{"5.3.1.2", "Ensure libpam-modules is installed"},
		{"5.3.1.3", "Ensure libpam-pwquality is installed"},
		{"5.3.2.1", "Ensure pam_unix module is enabled"},
		{"5.3.2.2", "Ensure pam_faillock module is enabled"},
		{"5.3.2.3", "Ensure pam_pwquality module is enabled"},
		{"5.3.2.4", "Ensure pam_pwhistory module is enabled"},
		{"5.3.3.1.1", "Ensure password failed attempts lockout is configured"},
		{"5.3.3.1.2", "Ensure password unlock time is configured"},
		{"5.3.3.2.1", "Ensure password number of changed characters is configured"},
		{"5.3.3.2.2", "Ensure minimum password length is configured"},
		{"5.3.3.2.3", "Ensure password complexity is configured"},
		{"5.3.3.2.4", "Ensure password same consecutive characters is configured"},
		{"5.3.3.2.5", "Ensure password maximum sequential characters is configured"},
		{"5.3.3.2.6", "Ensure password dictionary check is enabled"},
		{"5.3.3.2.7", "Ensure password quality checking is enforced"},
		{"5.3.3.2.8", "Ensure password quality is enforced for the root user"},
		{"5.3.3.3.1", "Ensure password history remember is configured"},
		{"5.3.3.3.2", "Ensure password history is enforced for the root user"},
		{"5.3.3.3.3", "Ensure pam_pwhistory includes use_authtok"},
		{"5.3.3.4.1", "Ensure pam_unix does not include nullok"},
		{"5.3.3.4.2", "Ensure pam_unix does not include remember"},
		{"5.3.3.4.3", "Ensure pam_unix includes a strong password hashing algorithm"},
		{"5.3.3.4.4", "Ensure pam_unix includes use_authtok"},
		{"5.4.1.1", "Ensure password expiration is configured"},
		{"5.4.1.3", "Ensure password expiration warning days is configured"},
		{"5.4.1.4", "Ensure strong password hashing algorithm is configured"},
		{"5.4.1.5", "Ensure inactive password lock is configured"},
		{"5.4.1.6", "Ensure all users last password change date is in the past"},
		{"5.4.2.1", "Ensure root is the only UID 0 account"},
		{"5.4.2.2", "Ensure root is the only GID 0 account"},
		{"5.4.2.3", "Ensure group root is the only GID 0 group"},
		{"5.4.2.4", "Ensure root password is set"},
		{"5.4.2.5", "Ensure root path integrity"},
		{"5.4.2.6", "Ensure root user umask is configured"},
		{"5.4.2.7", "Ensure system accounts do not have a valid login shell"},
		{"5.4.2.8", "Ensure accounts without a valid login shell are locked"},
		{"5.4.3.2", "Ensure default user shell timeout is configured"},
		{"5.4.3.3", "Ensure default user umask is configured"},
		// Section 6 — Logging and Auditing
		{"6.1.1", "Ensure AIDE is installed"},
		{"6.1.2", "Ensure filesystem integrity is regularly checked"},
		{"6.2.1.1.1", "Ensure journald service is enabled and active"},
		{"6.2.1.1.2", "Ensure journald log file access is configured"},
		{"6.2.1.1.3", "Ensure journald log file rotation is configured"},
		{"6.2.1.1.4", "Ensure journald ForwardToSyslog is disabled"},
		{"6.2.1.1.5", "Ensure journald Storage is configured"},
		{"6.2.1.1.6", "Ensure journald Compress is configured"},
		{"6.2.1.2.1", "Ensure systemd-journal-remote is installed"},
		{"6.2.1.2.2", "Ensure systemd-journal-remote authentication is configured"},
		{"6.2.1.2.3", "Ensure systemd-journal-upload is enabled and active"},
		{"6.2.1.2.4", "Ensure systemd-journal-remote service is not in use"},
		{"6.2.2.1", "Ensure access to all logfiles has been configured"},
		// Section 7 — System Maintenance
		{"7.1.1", "Ensure permissions on /etc/passwd are configured"},
		{"7.1.2", "Ensure permissions on /etc/passwd- are configured"},
		{"7.1.3", "Ensure permissions on /etc/group are configured"},
		{"7.1.4", "Ensure permissions on /etc/group- are configured"},
		{"7.1.5", "Ensure permissions on /etc/shadow are configured"},
		{"7.1.6", "Ensure permissions on /etc/shadow- are configured"},
		{"7.1.7", "Ensure permissions on /etc/gshadow are configured"},
		{"7.1.8", "Ensure permissions on /etc/gshadow- are configured"},
		{"7.1.9", "Ensure permissions on /etc/shells are configured"},
		{"7.1.10", "Ensure permissions on /etc/security/opasswd are configured"},
		{"7.1.11", "Ensure world writable files and directories are secured"},
		{"7.1.12", "Ensure no files or directories without an owner and a group exist"},
		{"7.1.13", "Ensure SUID and SGID files are reviewed"},
		{"7.2.1", "Ensure accounts in /etc/passwd use shadowed passwords"},
		{"7.2.2", "Ensure /etc/shadow password fields are not empty"},
		{"7.2.3", "Ensure all groups in /etc/passwd exist in /etc/group"},
		{"7.2.4", "Ensure shadow group is empty"},
		{"7.2.5", "Ensure no duplicate UIDs exist"},
		{"7.2.6", "Ensure no duplicate GIDs exist"},
		{"7.2.7", "Ensure no duplicate user names exist"},
		{"7.2.8", "Ensure no duplicate group names exist"},
		{"7.2.9", "Ensure local interactive user home directories are configured"},
		{"7.2.10", "Ensure local interactive user dot files access is configured"},
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

// buildCISRHELFindings returns the CIS Red Hat Enterprise Linux 9
// Benchmark v2.0.0 Level 1 — Server profile control catalog. CIS-licensed
// under the CIS Benchmarks End User License Agreement: only IDs and short
// titles are reproduced.
func (h *ReportHandler) buildCISRHELFindings() []ControlFinding {
	return catalog([][2]string{
		// Section 1 — Initial Setup
		{"1.1.1.1", "Ensure cramfs kernel module is not available"},
		{"1.1.1.2", "Ensure freevxfs kernel module is not available"},
		{"1.1.1.3", "Ensure hfs kernel module is not available"},
		{"1.1.1.4", "Ensure hfsplus kernel module is not available"},
		{"1.1.1.5", "Ensure jffs2 kernel module is not available"},
		{"1.1.1.8", "Ensure usb-storage kernel module is not available"},
		{"1.1.1.9", "Ensure unused filesystems kernel modules are not available"},
		{"1.1.2.1.1", "Ensure /tmp is a separate partition"},
		{"1.1.2.1.2", "Ensure nodev option set on /tmp partition"},
		{"1.1.2.1.3", "Ensure nosuid option set on /tmp partition"},
		{"1.1.2.1.4", "Ensure noexec option set on /tmp partition"},
		{"1.1.2.2.1", "Ensure /dev/shm is a separate partition"},
		{"1.1.2.2.2", "Ensure nodev option set on /dev/shm partition"},
		{"1.1.2.2.3", "Ensure nosuid option set on /dev/shm partition"},
		{"1.1.2.2.4", "Ensure noexec option set on /dev/shm partition"},
		{"1.1.2.3.2", "Ensure nodev option set on /home partition"},
		{"1.1.2.3.3", "Ensure nosuid option set on /home partition"},
		{"1.1.2.4.2", "Ensure nodev option set on /var partition"},
		{"1.1.2.4.3", "Ensure nosuid option set on /var partition"},
		{"1.1.2.5.2", "Ensure nodev option set on /var/tmp partition"},
		{"1.1.2.5.3", "Ensure nosuid option set on /var/tmp partition"},
		{"1.1.2.5.4", "Ensure noexec option set on /var/tmp partition"},
		{"1.1.2.6.2", "Ensure nodev option set on /var/log partition"},
		{"1.1.2.6.3", "Ensure nosuid option set on /var/log partition"},
		{"1.1.2.6.4", "Ensure noexec option set on /var/log partition"},
		{"1.1.2.7.2", "Ensure nodev option set on /var/log/audit partition"},
		{"1.1.2.7.3", "Ensure nosuid option set on /var/log/audit partition"},
		{"1.1.2.7.4", "Ensure noexec option set on /var/log/audit partition"},
		{"1.2.1.1", "Ensure GPG keys are configured"},
		{"1.2.1.2", "Ensure gpgcheck is globally activated"},
		{"1.2.1.4", "Ensure package manager repositories are configured"},
		{"1.2.2.1", "Ensure updates, patches, and additional security software are installed"},
		{"1.3.1.1", "Ensure SELinux is installed"},
		{"1.3.1.2", "Ensure SELinux is not disabled in bootloader configuration"},
		{"1.3.1.3", "Ensure SELinux policy is configured"},
		{"1.3.1.4", "Ensure the SELinux mode is not disabled"},
		{"1.3.1.7", "Ensure the MCS Translation Service (mcstrans) is not installed"},
		{"1.3.1.8", "Ensure SETroubleshoot is not installed"},
		{"1.4.1", "Ensure bootloader password is set"},
		{"1.4.2", "Ensure access to bootloader config is configured"},
		{"1.5.1", "Ensure address space layout randomization is enabled"},
		{"1.5.2", "Ensure ptrace_scope is restricted"},
		{"1.5.3", "Ensure core dump backtraces are disabled"},
		{"1.5.4", "Ensure core dump storage is disabled"},
		{"1.6.1", "Ensure system wide crypto policy is not set to legacy"},
		{"1.6.2", "Ensure system wide crypto policy is not set in sshd configuration"},
		{"1.6.3", "Ensure system wide crypto policy disables sha1 hash and signature support"},
		{"1.6.4", "Ensure system wide crypto policy disables macs less than 128 bits"},
		{"1.6.5", "Ensure system wide crypto policy disables cbc for ssh"},
		{"1.6.6", "Ensure system wide crypto policy disables chacha20-poly1305 for ssh"},
		{"1.6.7", "Ensure system wide crypto policy disables EtM for ssh"},
		{"1.7.1", "Ensure message of the day is configured properly"},
		{"1.7.2", "Ensure local login warning banner is configured properly"},
		{"1.7.3", "Ensure remote login warning banner is configured properly"},
		{"1.7.4", "Ensure access to /etc/motd is configured"},
		{"1.7.5", "Ensure access to /etc/issue is configured"},
		{"1.7.6", "Ensure access to /etc/issue.net is configured"},
		{"1.8.2", "Ensure GDM login banner is configured"},
		{"1.8.3", "Ensure GDM disable-user-list option is enabled"},
		{"1.8.4", "Ensure GDM screen locks when the user is idle"},
		{"1.8.5", "Ensure GDM screen locks cannot be overridden"},
		{"1.8.6", "Ensure GDM automatic mounting of removable media is disabled"},
		{"1.8.7", "Ensure GDM disabling automatic mounting of removable media is not overridden"},
		{"1.8.8", "Ensure GDM autorun-never is enabled"},
		{"1.8.9", "Ensure GDM autorun-never is not overridden"},
		{"1.8.10", "Ensure XDMCP is not enabled"},
		// Section 2 — Services
		{"2.1.1", "Ensure autofs services are not in use"},
		{"2.1.2", "Ensure avahi daemon services are not in use"},
		{"2.1.3", "Ensure dhcp server services are not in use"},
		{"2.1.4", "Ensure dns server services are not in use"},
		{"2.1.5", "Ensure dnsmasq services are not in use"},
		{"2.1.6", "Ensure samba file server services are not in use"},
		{"2.1.7", "Ensure ftp server services are not in use"},
		{"2.1.8", "Ensure message access server services are not in use"},
		{"2.1.9", "Ensure network file system services are not in use"},
		{"2.1.10", "Ensure nis server services are not in use"},
		{"2.1.11", "Ensure print server services are not in use"},
		{"2.1.12", "Ensure rpcbind services are not in use"},
		{"2.1.13", "Ensure rsync services are not in use"},
		{"2.1.14", "Ensure snmp services are not in use"},
		{"2.1.15", "Ensure telnet server services are not in use"},
		{"2.1.16", "Ensure tftp server services are not in use"},
		{"2.1.17", "Ensure web proxy server services are not in use"},
		{"2.1.18", "Ensure web server services are not in use"},
		{"2.1.19", "Ensure xinetd services are not in use"},
		{"2.1.21", "Ensure mail transfer agents are configured for local-only mode"},
		{"2.1.22", "Ensure only approved services are listening on a network interface"},
		{"2.2.1", "Ensure ftp client is not installed"},
		{"2.2.3", "Ensure nis client is not installed"},
		{"2.2.4", "Ensure telnet client is not installed"},
		{"2.2.5", "Ensure tftp client is not installed"},
		{"2.3.1", "Ensure time synchronization is in use"},
		{"2.3.2", "Ensure chrony is configured"},
		{"2.3.3", "Ensure chrony is not run as the root user"},
		{"2.4.1.1", "Ensure cron daemon is enabled and active"},
		{"2.4.1.2", "Ensure permissions on /etc/crontab are configured"},
		{"2.4.1.3", "Ensure permissions on /etc/cron.hourly are configured"},
		{"2.4.1.4", "Ensure permissions on /etc/cron.daily are configured"},
		{"2.4.1.5", "Ensure permissions on /etc/cron.weekly are configured"},
		{"2.4.1.6", "Ensure permissions on /etc/cron.monthly are configured"},
		{"2.4.1.7", "Ensure permissions on /etc/cron.d are configured"},
		{"2.4.1.8", "Ensure crontab is restricted to authorized users"},
		{"2.4.2.1", "Ensure at is restricted to authorized users"},
		// Section 3 — Network
		{"3.1.1", "Ensure IPv6 status is identified"},
		{"3.1.2", "Ensure wireless interfaces are disabled"},
		{"3.1.3", "Ensure bluetooth services are not in use"},
		{"3.3.1", "Ensure IP forwarding is disabled"},
		{"3.3.2", "Ensure packet redirect sending is disabled"},
		{"3.3.3", "Ensure bogus icmp responses are ignored"},
		{"3.3.4", "Ensure broadcast icmp requests are ignored"},
		{"3.3.5", "Ensure icmp redirects are not accepted"},
		{"3.3.6", "Ensure secure icmp redirects are not accepted"},
		{"3.3.7", "Ensure reverse path filtering is enabled"},
		{"3.3.8", "Ensure source routed packets are not accepted"},
		{"3.3.9", "Ensure suspicious packets are logged"},
		{"3.3.10", "Ensure tcp syn cookies is enabled"},
		{"3.3.11", "Ensure IPv6 router advertisements are not accepted"},
		// Section 4 — Host Based Firewall
		{"4.1.1", "Ensure nftables is installed"},
		{"4.1.2", "Ensure a single firewall configuration utility is in use"},
		{"4.2.1", "Ensure firewalld drops unnecessary services and ports"},
		{"4.2.2", "Ensure firewalld loopback traffic is configured"},
		{"4.3.1", "Ensure nftables base chains exist"},
		{"4.3.2", "Ensure nftables established connections are configured"},
		{"4.3.3", "Ensure nftables default deny firewall policy"},
		{"4.3.4", "Ensure nftables loopback traffic is configured"},
		// Section 5 — Access, Authentication and Authorization
		{"5.1.1", "Ensure permissions on /etc/ssh/sshd_config are configured"},
		{"5.1.2", "Ensure permissions on SSH private host key files are configured"},
		{"5.1.3", "Ensure permissions on SSH public host key files are configured"},
		{"5.1.4", "Ensure sshd Ciphers are configured"},
		{"5.1.5", "Ensure sshd KexAlgorithms is configured"},
		{"5.1.6", "Ensure sshd MACs are configured"},
		{"5.1.7", "Ensure sshd access is configured"},
		{"5.1.8", "Ensure sshd Banner is configured"},
		{"5.1.9", "Ensure sshd ClientAliveInterval and ClientAliveCountMax are configured"},
		{"5.1.12", "Ensure sshd HostbasedAuthentication is disabled"},
		{"5.1.13", "Ensure sshd IgnoreRhosts is enabled"},
		{"5.1.14", "Ensure sshd LoginGraceTime is configured"},
		{"5.1.15", "Ensure sshd LogLevel is configured"},
		{"5.1.16", "Ensure sshd MaxAuthTries is configured"},
		{"5.1.17", "Ensure sshd MaxStartups is configured"},
		{"5.1.18", "Ensure sshd MaxSessions is configured"},
		{"5.1.19", "Ensure sshd PermitEmptyPasswords is disabled"},
		{"5.1.20", "Ensure sshd PermitRootLogin is disabled"},
		{"5.1.21", "Ensure sshd PermitUserEnvironment is disabled"},
		{"5.1.22", "Ensure sshd UsePAM is enabled"},
		{"5.2.1", "Ensure sudo is installed"},
		{"5.2.2", "Ensure sudo commands use pty"},
		{"5.2.3", "Ensure sudo log file exists"},
		{"5.2.5", "Ensure re-authentication for privilege escalation is not disabled globally"},
		{"5.2.6", "Ensure sudo authentication timeout is configured correctly"},
		{"5.2.7", "Ensure access to the su command is restricted"},
		{"5.3.1.1", "Ensure latest version of pam is installed"},
		{"5.3.1.2", "Ensure latest version of authselect is installed"},
		{"5.3.1.3", "Ensure latest version of libpwquality is installed"},
		{"5.3.2.1", "Ensure active authselect profile includes pam modules"},
		{"5.3.2.2", "Ensure pam_faillock module is enabled"},
		{"5.3.2.3", "Ensure pam_pwquality module is enabled"},
		{"5.3.2.4", "Ensure pam_pwhistory module is enabled"},
		{"5.3.2.5", "Ensure pam_unix module is enabled"},
		{"5.3.3.1.1", "Ensure password failed attempts lockout is configured"},
		{"5.3.3.1.2", "Ensure password unlock time is configured"},
		{"5.3.3.2.1", "Ensure password number of changed characters is configured"},
		{"5.3.3.2.2", "Ensure password length is configured"},
		{"5.3.3.2.3", "Ensure password complexity is configured"},
		{"5.3.3.2.4", "Ensure password same consecutive characters is configured"},
		{"5.3.3.2.5", "Ensure password maximum sequential characters is configured"},
		{"5.3.3.2.6", "Ensure password dictionary check is enabled"},
		{"5.3.3.2.7", "Ensure password quality is enforced for the root user"},
		{"5.3.3.3.1", "Ensure password history remember is configured"},
		{"5.3.3.3.2", "Ensure password history is enforced for the root user"},
		{"5.3.3.3.3", "Ensure pam_pwhistory includes use_authtok"},
		{"5.3.3.4.1", "Ensure pam_unix does not include nullok"},
		{"5.3.3.4.2", "Ensure pam_unix does not include remember"},
		{"5.3.3.4.3", "Ensure pam_unix includes a strong password hashing algorithm"},
		{"5.3.3.4.4", "Ensure pam_unix includes use_authtok"},
		{"5.4.1.1", "Ensure password expiration is configured"},
		{"5.4.1.3", "Ensure password expiration warning days is configured"},
		{"5.4.1.4", "Ensure strong password hashing algorithm is configured"},
		{"5.4.1.5", "Ensure inactive password lock is configured"},
		{"5.4.1.6", "Ensure all users last password change date is in the past"},
		{"5.4.2.1", "Ensure root is the only UID 0 account"},
		{"5.4.2.2", "Ensure root is the only GID 0 account"},
		{"5.4.2.3", "Ensure group root is the only GID 0 group"},
		{"5.4.2.4", "Ensure root account access is controlled"},
		{"5.4.2.5", "Ensure root path integrity"},
		{"5.4.2.6", "Ensure root user umask is configured"},
		{"5.4.2.7", "Ensure system accounts do not have a valid login shell"},
		{"5.4.2.8", "Ensure accounts without a valid login shell are locked"},
		{"5.4.3.2", "Ensure default user shell timeout is configured"},
		{"5.4.3.3", "Ensure default user umask is configured"},
		// Section 6 — Logging and Auditing
		{"6.1.1", "Ensure AIDE is installed"},
		{"6.1.2", "Ensure filesystem integrity is regularly checked"},
		{"6.1.3", "Ensure cryptographic mechanisms are used to protect the integrity of audit tools"},
		{"6.2.1.1", "Ensure journald service is enabled and active"},
		{"6.2.1.2", "Ensure journald log file access is configured"},
		{"6.2.1.3", "Ensure journald log file rotation is configured"},
		{"6.2.1.4", "Ensure only one logging system is in use"},
		{"6.2.2.1.1", "Ensure systemd-journal-remote is installed"},
		{"6.2.2.1.2", "Ensure systemd-journal-upload authentication is configured"},
		{"6.2.2.1.3", "Ensure systemd-journal-upload is enabled and active"},
		{"6.2.2.1.4", "Ensure systemd-journal-remote service is not in use"},
		{"6.2.2.2", "Ensure journald ForwardToSyslog is disabled"},
		{"6.2.2.3", "Ensure journald Compress is configured"},
		{"6.2.2.4", "Ensure journald Storage is configured"},
		{"6.2.3.1", "Ensure rsyslog is installed"},
		{"6.2.3.2", "Ensure rsyslog service is enabled and active"},
		{"6.2.3.3", "Ensure journald is configured to send logs to rsyslog"},
		{"6.2.3.4", "Ensure rsyslog log file creation mode is configured"},
		{"6.2.3.5", "Ensure rsyslog logging is configured"},
		{"6.2.3.6", "Ensure rsyslog is configured to send logs to a remote log host"},
		{"6.2.3.7", "Ensure rsyslog is not configured to receive logs from a remote client"},
		{"6.2.3.8", "Ensure rsyslog logrotate is configured"},
		{"6.2.4.1", "Ensure access to all logfiles has been configured"},
		// Section 7 — System Maintenance
		{"7.1.1", "Ensure permissions on /etc/passwd are configured"},
		{"7.1.2", "Ensure permissions on /etc/passwd- are configured"},
		{"7.1.3", "Ensure permissions on /etc/group are configured"},
		{"7.1.4", "Ensure permissions on /etc/group- are configured"},
		{"7.1.5", "Ensure permissions on /etc/shadow are configured"},
		{"7.1.6", "Ensure permissions on /etc/shadow- are configured"},
		{"7.1.7", "Ensure permissions on /etc/gshadow are configured"},
		{"7.1.8", "Ensure permissions on /etc/gshadow- are configured"},
		{"7.1.9", "Ensure permissions on /etc/shells are configured"},
		{"7.1.10", "Ensure permissions on /etc/security/opasswd are configured"},
		{"7.1.11", "Ensure world writable files and directories are secured"},
		{"7.1.12", "Ensure no files or directories without an owner and a group exist"},
		{"7.1.13", "Ensure SUID and SGID files are reviewed"},
		{"7.2.1", "Ensure accounts in /etc/passwd use shadowed passwords"},
		{"7.2.2", "Ensure /etc/shadow password fields are not empty"},
		{"7.2.3", "Ensure all groups in /etc/passwd exist in /etc/group"},
		{"7.2.4", "Ensure no duplicate UIDs exist"},
		{"7.2.5", "Ensure no duplicate GIDs exist"},
		{"7.2.6", "Ensure no duplicate user names exist"},
		{"7.2.7", "Ensure no duplicate group names exist"},
		{"7.2.8", "Ensure local interactive user home directories are configured"},
		{"7.2.9", "Ensure local interactive user dot files access is configured"},
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

func (h *ReportHandler) handleNYDFSCyberReport(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(), false)
}

func (h *ReportHandler) handleNYDFSCyberReportHTML(w http.ResponseWriter, r *http.Request) {
	h.serveFrameworkReport(w, r, "Vigo NY DFS Cybersecurity (23 NYCRR 500) Report", "vigo-ny-dfs-cyber",
		h.buildNYDFSCyberFindings(), true)
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
	// Locale is the resolved per-request locale, stamped by the ui report
	// builder closures so the compliance-frameworks / compliance-executive-content
	// fragments (which receive this struct as their template dot) can read
	// $.Locale. Not serialized — it's a render-time concern only.
	Locale      string             `json:"-"`
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

	// Framework order + names come from the registry (server/frameworks),
	// the single source of truth, so Executive report lookups stay stable
	// across enablement changes.
	frameworks := make([]FrameworkSummary, 0, len(fwreg.All))
	for _, fw := range fwreg.All {
		if !h.configStore.IsStandardEnabled(fw.StandardKey) {
			continue
		}
		score, ok := h.ComputeFrameworkScore(r, fw.Slug)
		if !ok {
			continue
		}
		frameworks = append(frameworks, FrameworkSummary{
			Name:         fw.ShortName,
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
	dayTotals := make(map[string]struct {
		sum   float64
		count int
	})
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
			ConfigChanges:  summary.Changed,
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

	// Priority 4: Persistent drift (warning).
	if summary.Diverged > 0 {
		issues = append(issues, ExecutiveIssue{
			Severity:    "warning",
			Description: fmt.Sprintf("%d node(s) are experiencing persistent configuration drift. Configcrates keep applying changes across many consecutive runs, indicating an external process may be conflicting or a configcrate may be non-idempotent.", summary.Diverged),
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
		writeError(w, http.StatusInternalServerError, "failed to build executive report: "+err.Error())
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
		writeError(w, http.StatusInternalServerError, "failed to build executive report: "+err.Error())
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

// PersistComplianceSnapshot writes per-framework standards compliance
// scores to the compliance_history table through the unified scorer
// (ComputeFrameworkScoreCtx) so snapshot numbers stay consistent with
// every live UI page. Called daily by the snapshotter loop and on every
// successful config publish via ConfigHandler.snapshotCompliance. The
// (snapshot_date, framework) UPSERT key means multiple invocations on
// the same day overwrite rather than duplicate — the most recent run
// wins, which is what the dashboard summary reads.
func (h *ReportHandler) PersistComplianceSnapshot(ctx context.Context) {
	if h.configStore == nil {
		return
	}
	// Framework set + names come from the registry (server/frameworks), the
	// single source of truth. The snapshot Framework column (fw.ShortName)
	// is part of the (snapshot_date, framework) upsert key, so the registry
	// ShortName must stay stable.
	recorded := 0
	for _, fw := range fwreg.All {
		if !h.configStore.IsStandardEnabled(fw.StandardKey) {
			continue
		}
		score, ok := h.ComputeFrameworkScoreCtx(ctx, fw.Slug)
		if !ok {
			slog.Warn("compliance snapshot: scorer returned not-ok", "framework", fw.ShortName, "slug", fw.Slug)
			continue
		}
		if err := h.queries.UpsertComplianceSnapshot(ctx, db.UpsertComplianceSnapshotParams{
			Framework:     fw.ShortName,
			ScorePercent:  score.Fleet.CoveragePercent,
			Satisfied:     int64(score.Fleet.Covered),
			Waived:        int64(score.Fleet.Waived),
			Partial:       int64(score.Fleet.Partial),
			NotSatisfied:  int64(score.Fleet.Missing),
			TotalControls: int64(score.Total),
		}); err != nil {
			slog.Error("failed to persist compliance snapshot", "framework", fw.ShortName, "error", err)
			continue
		}
		recorded++
	}
	slog.Info("compliance snapshot recorded", "frameworks", recorded)
}
