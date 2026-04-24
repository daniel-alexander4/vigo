// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── Cached-trait summary (rendered on drawer open; no agent dispatch) ────────

// SecurityDrawerCached is the server-rendered summary the operator sees
// instantly when the security drawer opens. Everything here is sourced from
// the envoy's last check-in traits via FleetIndex — no agent query required.
type SecurityDrawerCached struct {
	Trivy   *TrivyCached
	Lynis   *LynisCached
	Rootkit *RootkitCached
	ClamAV  *ClamAVCached
	AIDE    *AIDECached
	CVEs    []CachedCVE
}

type TrivyCached struct {
	Critical, High, Medium, Low, Total int
	LastScan                           string
}

type LynisCached struct {
	Score       int
	Warnings    int
	Suggestions int
}

type RootkitCached struct {
	ChkrootkitClean bool
	RkhunterClean   bool
	BothPresent     bool // one of the two scanners reported at all
}

type ClamAVCached struct {
	Clean    bool
	Infected int
}

type AIDECached struct {
	Clean                  bool
	Added, Removed, Changed int
}

// CachedCVE is one row from `security_scan.cves.N.{id,severity,packages}`
// flattened into a displayable struct. The JS used to build this map inline;
// moving it server-side makes the drawer HTML complete on first render.
type CachedCVE struct {
	ID       string
	Severity string
	Packages int
}

// ── Live-detail types (parsed from the `security_scan_detail` trait) ────────

// SecurityScanDetail is the JSON shape the agent publishes under the
// `security_scan_detail` trait path. Matches the nine scanner sections the
// drawer renders as tabs.
type SecurityScanDetail struct {
	Available bool             `json:"available"`
	Trivy     *TrivyDetail     `json:"trivy,omitempty"`
	Debsecan  *DebsecanDetail  `json:"debsecan,omitempty"`
	Lynis     *LynisDetail     `json:"lynis,omitempty"`
	Rkhunter  *RkhunterDetail  `json:"rkhunter,omitempty"`
	Chkrootkit *ChkrootkitDetail `json:"chkrootkit,omitempty"`
	ClamAV    *ClamAVDetail    `json:"clamav,omitempty"`
	AIDE      *AIDEDetail      `json:"aide,omitempty"`
	Defender  *DefenderDetail  `json:"windows_defender,omitempty"`
	SCAP      *SCAPDetail      `json:"scap,omitempty"`
}

type TrivyDetail struct {
	Vulnerabilities []TrivyVuln `json:"vulnerabilities"`
	LastScan        string      `json:"last_scan"`
	Truncated       bool        `json:"truncated"`
}

type TrivyVuln struct {
	CVEID     string `json:"cve_id"`
	Severity  string `json:"severity"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed"`
	Title     string `json:"title"`
	Others    string `json:"others"`
}

type DebsecanDetail struct {
	Vulnerabilities []DebsecanVuln `json:"vulnerabilities"`
	LastScan        string         `json:"last_scan"`
}

type DebsecanVuln struct {
	CVEID   string `json:"cve_id"`
	Urgency string `json:"urgency"`
	Package string `json:"package"`
}

type LynisDetail struct {
	Score       int           `json:"score"`
	Warnings    []LynisEntry  `json:"warnings"`
	Suggestions []LynisEntry  `json:"suggestions"`
	LastScan    string        `json:"last_scan"`
}

type LynisEntry struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RkhunterDetail — warnings is either a list of objects {file: ...} or
// bare strings. We unmarshal into json.RawMessage items and normalize
// on render.
type RkhunterDetail struct {
	Warnings []json.RawMessage `json:"warnings"`
	LastScan string            `json:"last_scan"`
}

type ChkrootkitDetail struct {
	Infected []json.RawMessage `json:"infected"`
	LastScan string            `json:"last_scan"`
}

type ClamAVDetail struct {
	Findings []ClamAVFinding `json:"findings"`
	LastScan string          `json:"last_scan"`
}

type ClamAVFinding struct {
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

type AIDEDetail struct {
	Clean    bool     `json:"clean"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Changed  []string `json:"changed"`
	LastScan string   `json:"last_scan"`
}

type DefenderDetail struct {
	RealtimeProtection bool `json:"realtime_protection"`
	Enabled            bool `json:"enabled"`
	SignatureAgeDays   int  `json:"signature_age_days"`
	ThreatsDetected    int  `json:"threats_detected"`
	LastScan           string `json:"last_scan"`
}

type SCAPDetail struct {
	Profile       string `json:"profile"`
	Score         int    `json:"score"`
	Pass          int    `json:"pass"`
	Fail          int    `json:"fail"`
	NotApplicable int    `json:"not_applicable"`
	TotalRules    int    `json:"total_rules"`
	HighFail      int    `json:"high_fail"`
	MediumFail    int    `json:"medium_fail"`
	LowFail       int    `json:"low_fail"`
	LastScan      string `json:"last_scan"`
}

// RkhunterRow / ChkrootkitRow are normalized render-time rows. Handlers
// flatten the polymorphic `Warnings`/`Infected` JSON into these so the
// template doesn't have to know about raw JSON shapes.
type RkhunterRow struct {
	File string
}
type ChkrootkitRow struct {
	Check string
	Files string
}

// SecurityDrawerData drives the `security-drawer-fragment` template for both
// the cached-summary open and the live-detail fetch. Live=true means the
// dispatch succeeded and Detail is populated; Live=false means the drawer
// shows only the instant summary with a "Fetch Live Detail" button.
type SecurityDrawerData struct {
	EnvoyID  string
	Hostname string
	Live     bool                  // true when live-dispatch completed successfully
	Timeout  int                   // seconds; exposed to the template for the spinner hint
	Cached   SecurityDrawerCached
	Detail   *SecurityScanDetail   // only populated on Live=true
	Error    string                // set when dispatch timed out or the envoy was offline
	// RkhunterRows / ChkrootkitRows are normalized versions of Detail's
	// polymorphic JSON — populated so templates can range over structs.
	RkhunterRows   []RkhunterRow
	ChkrootkitRows []ChkrootkitRow
	// Sections is the ordered list of scanner slugs that have data for
	// this envoy. Drives the tab strip — the first entry is the default-
	// visible pane (via CSS :target fallback).
	Sections []SecuritySection
}

// SecuritySection is one tab in the live-detail view.
type SecuritySection struct {
	Slug  string // "trivy", "lynis", "rkhunter", …
	Label string // "Trivy CVEs", "Lynis", "rkhunter", …
}

// SecurityEnvoyFragment serves the security drawer body for one envoy.
// Default: renders the cached-traits summary from FleetIndex.
// With `?live=true`: dispatches a live `security_scan_detail` query to the
// agent (blocking, ~60s cap) and renders the full tabbed detail on success.
//
// Called via HTMX from /security page "Scan Details" buttons — replaces the
// former client-side fetch() + DOM render helpers. The entire drawer body
// ships as one HTML swap so screen readers announce the change cleanly via
// the drawer's aria-live region.
func (h *Handler) SecurityEnvoyFragment(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	if _, err := uuid.Parse(envoyID); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	hostname, traits, ok := h.dataSource.SecurityScanContext(envoyID)
	if !ok {
		data := SecurityDrawerData{EnvoyID: envoyID, Hostname: hostname}
		h.renderer.RenderFragment(w, "security", "security-drawer-empty", data)
		return
	}

	data := SecurityDrawerData{
		EnvoyID:  envoyID,
		Hostname: hostname,
		Cached:   buildSecurityCached(traits),
		Timeout:  securityLiveTimeoutSec,
	}

	if r.URL.Query().Get("live") == "true" {
		if h.traitQuery == nil {
			data.Error = "live query dispatcher not configured"
			h.renderer.RenderFragment(w, "security", "security-drawer-fragment", data)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(securityLiveTimeoutSec+5)*time.Second)
		defer cancel()
		value, found, err := h.traitQuery.DispatchTraitQuery(ctx, "security_scan_detail", hostname, time.Duration(securityLiveTimeoutSec)*time.Second)
		switch {
		case err != nil:
			data.Error = "dispatch failed: " + err.Error()
		case !found:
			data.Error = "agent did not respond within " + strconv.Itoa(securityLiveTimeoutSec) + "s — it may not have checked in during the wait window."
		default:
			var detail SecurityScanDetail
			if jerr := json.Unmarshal([]byte(value), &detail); jerr != nil {
				data.Error = "could not parse agent response: " + jerr.Error()
			} else {
				data.Live = true
				data.Detail = &detail
				data.RkhunterRows = flattenRkhunter(detail.Rkhunter)
				data.ChkrootkitRows = flattenChkrootkit(detail.Chkrootkit)
				data.Sections = liveSections(&detail)
			}
		}
	}

	h.renderer.RenderFragment(w, "security", "security-drawer-fragment", data)
}

// securityLiveTimeoutSec is the wait window for the live dispatch. Matches
// the existing SecurityPage.QueryTimeoutSec used by the legacy client flow.
const securityLiveTimeoutSec = 90

// ── Helpers ─────────────────────────────────────────────────────────────────

func buildSecurityCached(traits map[string]string) SecurityDrawerCached {
	out := SecurityDrawerCached{}

	getInt := func(k string) int {
		v, _ := strconv.Atoi(traits[k])
		return v
	}
	has := func(k string) bool {
		_, ok := traits[k]
		return ok
	}

	if has("security_scan.trivy.critical") || has("security_scan.trivy.high") {
		out.Trivy = &TrivyCached{
			Critical: getInt("security_scan.trivy.critical"),
			High:     getInt("security_scan.trivy.high"),
			Medium:   getInt("security_scan.trivy.medium"),
			Low:      getInt("security_scan.trivy.low"),
			Total:    getInt("security_scan.trivy.total"),
			LastScan: traits["security_scan.trivy.last_scan"],
		}
	}
	if has("security_scan.lynis.score") {
		out.Lynis = &LynisCached{
			Score:       getInt("security_scan.lynis.score"),
			Warnings:    getInt("security_scan.lynis.warnings"),
			Suggestions: getInt("security_scan.lynis.suggestions"),
		}
	}
	if has("security_scan.chkrootkit.clean") || has("security_scan.rkhunter.clean") {
		rc := traits["security_scan.chkrootkit.clean"]
		rh := traits["security_scan.rkhunter.clean"]
		out.Rootkit = &RootkitCached{
			BothPresent:     true,
			ChkrootkitClean: rc == "" || rc == "true",
			RkhunterClean:   rh == "" || rh == "true",
		}
	}
	if has("security_scan.clamav.clean") {
		out.ClamAV = &ClamAVCached{
			Clean:    traits["security_scan.clamav.clean"] == "true",
			Infected: getInt("security_scan.clamav.infected"),
		}
	}
	if has("security_scan.aide.clean") {
		out.AIDE = &AIDECached{
			Clean:   traits["security_scan.aide.clean"] == "true",
			Added:   getInt("security_scan.aide.added"),
			Removed: getInt("security_scan.aide.removed"),
			Changed: getInt("security_scan.aide.changed"),
		}
	}
	out.CVEs = extractCachedCVEs(traits)
	return out
}

// extractCachedCVEs walks security_scan.cves.N.{id,severity,packages} keys
// and collapses them into a displayable slice. Ordered by severity
// (critical → high → medium → low → unassigned), then CVE ID.
func extractCachedCVEs(traits map[string]string) []CachedCVE {
	grouped := map[string]*CachedCVE{}
	for k, v := range traits {
		if !strings.HasPrefix(k, "security_scan.cves.") {
			continue
		}
		// security_scan.cves.<N>.<field>
		parts := strings.SplitN(strings.TrimPrefix(k, "security_scan.cves."), ".", 2)
		if len(parts) != 2 {
			continue
		}
		idx, field := parts[0], parts[1]
		if _, ok := grouped[idx]; !ok {
			grouped[idx] = &CachedCVE{}
		}
		switch field {
		case "id":
			grouped[idx].ID = v
		case "severity":
			grouped[idx].Severity = strings.ToLower(v)
		case "packages":
			n, _ := strconv.Atoi(v)
			grouped[idx].Packages = n
		}
	}
	out := make([]CachedCVE, 0, len(grouped))
	for _, c := range grouped {
		if c.ID == "" {
			continue
		}
		out = append(out, *c)
	}
	sevRank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4, "unassigned": 4}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := sevRank[out[i].Severity], sevRank[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// flattenRkhunter normalizes the polymorphic warnings array (strings or
// objects with a "file" field) into a flat slice of rows.
func flattenRkhunter(d *RkhunterDetail) []RkhunterRow {
	if d == nil {
		return nil
	}
	rows := make([]RkhunterRow, 0, len(d.Warnings))
	for _, raw := range d.Warnings {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			rows = append(rows, RkhunterRow{File: s})
			continue
		}
		var obj struct {
			File string `json:"file"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.File != "" {
			rows = append(rows, RkhunterRow{File: obj.File})
			continue
		}
		rows = append(rows, RkhunterRow{File: string(raw)})
	}
	return rows
}

// flattenChkrootkit normalizes the polymorphic infected array into rows.
func flattenChkrootkit(d *ChkrootkitDetail) []ChkrootkitRow {
	if d == nil {
		return nil
	}
	rows := make([]ChkrootkitRow, 0, len(d.Infected))
	for _, raw := range d.Infected {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			rows = append(rows, ChkrootkitRow{Check: s})
			continue
		}
		var obj struct {
			Check string `json:"check"`
			Files string `json:"files"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil && obj.Check != "" {
			rows = append(rows, ChkrootkitRow{Check: obj.Check, Files: obj.Files})
			continue
		}
		rows = append(rows, ChkrootkitRow{Check: string(raw)})
	}
	return rows
}

// liveSections returns the ordered list of tabs that have data for this
// envoy. The first entry is the default-visible pane when no tab is
// targeted via URL fragment.
func liveSections(d *SecurityScanDetail) []SecuritySection {
	candidates := []SecuritySection{
		{"trivy", "Trivy CVEs"},
		{"debsecan", "Debsecan"},
		{"lynis", "Lynis"},
		{"rkhunter", "rkhunter"},
		{"chkrootkit", "chkrootkit"},
		{"clamav", "ClamAV"},
		{"aide", "AIDE"},
		{"defender", "Windows Defender"},
		{"scap", "SCAP/STIG"},
	}
	out := []SecuritySection{}
	for _, c := range candidates {
		has := false
		switch c.Slug {
		case "trivy":
			has = d.Trivy != nil
		case "debsecan":
			has = d.Debsecan != nil
		case "lynis":
			has = d.Lynis != nil
		case "rkhunter":
			has = d.Rkhunter != nil
		case "chkrootkit":
			has = d.Chkrootkit != nil
		case "clamav":
			has = d.ClamAV != nil
		case "aide":
			has = d.AIDE != nil
		case "defender":
			has = d.Defender != nil
		case "scap":
			has = d.SCAP != nil
		}
		if has {
			out = append(out, c)
		}
	}
	return out
}

// cveURL returns the external CVE record URL for a given CVE ID.
func cveURL(cveID string) string {
	return "https://www.cve.org/CVERecord?id=" + cveID
}

// debianCVEURL returns the Debian security tracker URL for a CVE.
func debianCVEURL(cveID string) string {
	return "https://security-tracker.debian.org/tracker/" + cveID
}

// lynisControlURL returns the Lynis documentation URL for a given control ID.
func lynisControlURL(id string) string {
	return "https://cisofy.com/lynis/controls/" + id + "/"
}

// securityAIQuestion crafts an AI-drawer question matching the phrasing used
// by the previous client-side `aiBtn()` helper, so existing muscle memory
// (and the AI provider's prompt-context signals) doesn't drift.
func securityAIQuestion(host, finding string) string {
	return fmt.Sprintf("On host %s: %s", host, finding)
}
