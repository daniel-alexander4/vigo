// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/traits"
	"github.com/google/uuid"
)

// ── Cached-trait summary (rendered on drawer open; no agent dispatch) ────────

// SecurityDrawerCached is the server-rendered summary the operator sees
// instantly when the security drawer opens. Everything here is sourced from
// the envoy's last check-in traits via FleetIndex — no agent query required.
// Each non-nil scanner field becomes a tab in the drawer.
type SecurityDrawerCached struct {
	// Unique is the canonical per-host CVE count — distinct CVE IDs per
	// severity, deduplicated across every scanner that ran. Same numbers
	// the /security per-host table shows; rendered atop the CVEs tab.
	Unique *UniqueCVECounts
	// Trivy / Debsecan are raw per-scanner package-match counts (a CVE in
	// N packages counts N times; the scanners aren't cross-deduped) — run
	// larger than Unique by that multiplicity; shown as scanner-diagnostic
	// tabs, never aggregated.
	Trivy      *TrivyCached
	Debsecan   *DebsecanCached
	Lynis      *LynisCached
	Chkrootkit *RootkitScannerCached // nil when chkrootkit didn't report
	Rkhunter   *RootkitScannerCached // nil when rkhunter didn't report
	ClamAV     *ClamAVCached
	AIDE       *AIDECached
	CVEs       []CachedCVE
	// CVEsTruncated is true when the agent's MAX_CVES wire cap dropped
	// entries — len(CVEs) is then less than Unique.Total(). The count in
	// Unique stays exact; only the listed CVEs are the top-N by severity.
	CVEsTruncated bool
}

// UniqueCVECounts is the operator-meaningful CVE count for one host:
// distinct CVE IDs per severity, deduplicated across every scanner that
// ran (Trivy + debsecan + Windows-updates), highest-severity-wins. Sourced
// from security_scan.unique_{critical,high,medium,low}.
type UniqueCVECounts struct {
	Critical, High, Medium, Low int
}

// Total returns the total distinct CVE count across all severities.
func (u UniqueCVECounts) Total() int { return u.Critical + u.High + u.Medium + u.Low }

type TrivyCached struct {
	Critical, High, Medium, Low, Total int
	LastScan                           string
}

// DebsecanCached mirrors TrivyCached for the Debian Security Analyzer.
// debsecan classifies by Debian "urgency" (high/medium/low/unassigned) —
// it has no "critical" tier. Total is the raw package-level match count.
type DebsecanCached struct {
	High, Medium, Low, Unassigned, Total int
	LastScan                             string
}

type LynisCached struct {
	Score       int
	Warnings    int
	Suggestions int
}

// RootkitScannerCached is one rootkit scanner's summary (chkrootkit or
// rkhunter). Issues is that scanner's count of suspicious findings —
// chkrootkit reports it as "infected", rkhunter as "warnings"; non-zero
// when not clean.
type RootkitScannerCached struct {
	Clean  bool
	Issues int
}

type ClamAVCached struct {
	Clean    bool
	Infected int
}

type AIDECached struct {
	Clean                  bool
	Added, Removed, Changed int
}

// CachedCVE is one row from `security_scan.cves.N.{id,severity,packages,title}`
// flattened into a displayable struct. The JS used to build this map inline;
// moving it server-side makes the drawer HTML complete on first render.
// Title is the NVD short title (may be empty — debsecan carries no titles).
type CachedCVE struct {
	ID       string
	Severity string
	Packages int
	Title    string
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
	// Certs are this host's certificates expiring or expired within 30 days
	// (via DataSource.EnvoyCertDetails) — feeds the always-present "Certs"
	// tab. Empty means none expiring (the pane self-reports that).
	Certs    []CertDetail
	Detail   *SecurityScanDetail   // only populated on Live=true
	Error    string                // set when dispatch timed out or the envoy was offline
	// RkhunterRows / ChkrootkitRows are normalized versions of Detail's
	// polymorphic JSON — populated so templates can range over structs.
	RkhunterRows   []RkhunterRow
	ChkrootkitRows []ChkrootkitRow
	// Sections is the ordered tab list (CVEs rollup, then scanners, then
	// Certs) — see drawerSections. Drives the tab strip; the first entry
	// is the default-visible pane (via CSS :target fallback).
	Sections []SecuritySection
}

// SecuritySection is one tab in the security drawer.
type SecuritySection struct {
	Slug  string // "cves", "trivy", "lynis", "certs", …
	Label string // "CVEs", "Trivy", "Lynis", "Certs", …
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
	hostname, flat, ok := h.dataSource.SecurityScanContext(envoyID)
	if !ok {
		data := SecurityDrawerData{EnvoyID: envoyID, Hostname: hostname}
		h.renderer.RenderFragment(w, "security", "security-drawer-empty", data)
		return
	}

	data := SecurityDrawerData{
		EnvoyID:  envoyID,
		Hostname: hostname,
		Cached:   buildSecurityCached(flat),
		Timeout:  securityLiveTimeoutSec,
	}
	// Certs aren't part of the security_scan trait — pull this host's
	// expiring/expired certs separately for the always-present Certs tab.
	// nil (no cert_lifecycle rows / none expiring) → the tab shows "none".
	data.Certs, _, _ = h.dataSource.EnvoyCertDetails(envoyID, "")

	if r.URL.Query().Get("live") == "true" {
		if h.traitQuery == nil {
			data.Error = "live query dispatcher not configured"
		} else {
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
				}
			}
		}
	}

	// One tab list for the whole drawer: cached scanners before the live
	// fetch, live scanners after (data.Detail is nil unless the dispatch
	// succeeded), plus the CVEs rollup and the Certs tab.
	data.Sections = drawerSections(data.Cached, data.Detail)
	h.renderer.RenderFragment(w, "security", "security-drawer-fragment", data)
}

// securityLiveTimeoutSec is the wait window for the live dispatch. Matches
// the existing SecurityPage.QueryTimeoutSec used by the legacy client flow.
const securityLiveTimeoutSec = 90

// SecurityCertsPageData drives the dedicated /security/certs/{id} page —
// the per-envoy expiring-certificate inventory. Rendered as a full page
// rather than an inline expansion on /security because the parent
// security-status fragment auto-refreshes every PollInterval and would
// wipe any injected child content on the next tick. The dedicated page
// survives page reloads cleanly and can be bookmarked or linked from
// other surfaces (Scrier notes, run output, etc.).
type SecurityCertsPageData struct {
	PageData
	EnvoyID      string
	Hostname     string
	Bucket       string       // "expired" | "7d" | "30d" | "" (all <30d)
	Certs        []CertDetail // filtered subset when Bucket is set
	AllCerts     []CertDetail // unfiltered <30d set used for the stat cards
	Expired      int
	ExpiringIn7  int
	ExpiringIn30 int
}

// SecurityCerts renders the dedicated /security/certs/{id} page listing
// one envoy's certificates expiring within 30 days. Query param `bucket`
// narrows to one of expired / 7d / 30d; absent means show all three.
func (h *Handler) SecurityCerts(w http.ResponseWriter, r *http.Request) {
	envoyID := r.PathValue("id")
	if _, err := uuid.Parse(envoyID); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	switch bucket {
	case "", "expired", "7d", "30d":
	default:
		http.Error(w, "invalid bucket", http.StatusBadRequest)
		return
	}

	all, hostname, ok := h.dataSource.EnvoyCertDetails(envoyID, "")
	if !ok {
		http.NotFound(w, r)
		return
	}

	var expired, in7, in30 int
	for _, c := range all {
		switch {
		case c.DaysRemaining < 0:
			expired++
		case c.DaysRemaining < 7:
			in7++
		case c.DaysRemaining < 30:
			in30++
		}
	}

	// Apply bucket filter for the displayed table; stat-card counts use `all`.
	var shown []CertDetail
	if bucket == "" {
		shown = all
	} else {
		for _, c := range all {
			switch bucket {
			case "expired":
				if c.DaysRemaining < 0 {
					shown = append(shown, c)
				}
			case "7d":
				if c.DaysRemaining >= 0 && c.DaysRemaining < 7 {
					shown = append(shown, c)
				}
			case "30d":
				if c.DaysRemaining >= 7 && c.DaysRemaining < 30 {
					shown = append(shown, c)
				}
			}
		}
	}

	data := SecurityCertsPageData{
		PageData:     PageData{Title: "Certificates — " + hostname, ActiveNav: "security"},
		EnvoyID:      envoyID,
		Hostname:     hostname,
		Bucket:       bucket,
		Certs:        shown,
		AllCerts:     all,
		Expired:      expired,
		ExpiringIn7:  in7,
		ExpiringIn30: in30,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "security-certs", data)
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// buildSecurityCached projects an envoy's flat security_scan.* traits into
// the drawer's instant-render summary. Trait keys come from server/traits so
// the drawer reads the same keys the /security page and the agent write. The
// parameter is `flat` (not `traits`) so it doesn't shadow the package.
func buildSecurityCached(flat map[string]string) SecurityDrawerCached {
	out := SecurityDrawerCached{}

	getInt := func(k string) int {
		v, _ := strconv.Atoi(flat[k])
		return v
	}
	has := func(k string) bool {
		_, ok := flat[k]
		return ok
	}

	// Canonical CVE count — distinct CVE IDs per severity, cross-scanner
	// deduped. Present whenever any CVE scanner ran (the agent always emits
	// unique_* once security_scan has any data). Same numbers the /security
	// per-host table shows; the drawer leads with it.
	if has(traits.UniqueCritical) || has(traits.UniqueHigh) || has(traits.UniqueMedium) || has(traits.UniqueLow) {
		out.Unique = &UniqueCVECounts{
			Critical: getInt(traits.UniqueCritical),
			High:     getInt(traits.UniqueHigh),
			Medium:   getInt(traits.UniqueMedium),
			Low:      getInt(traits.UniqueLow),
		}
	}
	if has(traits.TrivyCritical) || has(traits.TrivyHigh) {
		out.Trivy = &TrivyCached{
			Critical: getInt(traits.TrivyCritical),
			High:     getInt(traits.TrivyHigh),
			Medium:   getInt(traits.TrivyMedium),
			Low:      getInt(traits.TrivyLow),
			Total:    getInt(traits.TrivyTotal),
			LastScan: flat[traits.TrivyLastScan],
		}
	}
	if has(traits.DebsecanHigh) || has(traits.DebsecanMedium) || has(traits.DebsecanLow) || has(traits.DebsecanUnassigned) {
		out.Debsecan = &DebsecanCached{
			High:       getInt(traits.DebsecanHigh),
			Medium:     getInt(traits.DebsecanMedium),
			Low:        getInt(traits.DebsecanLow),
			Unassigned: getInt(traits.DebsecanUnassigned),
			Total:      getInt(traits.DebsecanTotal),
			LastScan:   flat[traits.DebsecanLastScan],
		}
	}
	if has(traits.LynisScore) {
		out.Lynis = &LynisCached{
			Score:       getInt(traits.LynisScore),
			Warnings:    getInt(traits.LynisWarnings),
			Suggestions: getInt(traits.LynisSuggestions),
		}
	}
	if has(traits.ChkrootkitClean) {
		out.Chkrootkit = &RootkitScannerCached{
			Clean:  flat[traits.ChkrootkitClean] != "false",
			Issues: getInt(traits.ChkrootkitInfected),
		}
	}
	if has(traits.RkhunterClean) {
		out.Rkhunter = &RootkitScannerCached{
			Clean:  flat[traits.RkhunterClean] != "false",
			Issues: getInt(traits.RkhunterWarnings),
		}
	}
	if has(traits.ClamavClean) {
		out.ClamAV = &ClamAVCached{
			Clean:    flat[traits.ClamavClean] == "true",
			Infected: getInt(traits.ClamavInfected),
		}
	}
	if has(traits.AideClean) {
		out.AIDE = &AIDECached{
			Clean:   flat[traits.AideClean] == "true",
			Added:   getInt(traits.AideAdded),
			Removed: getInt(traits.AideRemoved),
			Changed: getInt(traits.AideChanged),
		}
	}
	out.CVEs = extractCachedCVEs(flat)
	// The unified CVE list rode the wire capped at the agent's MAX_CVES;
	// the unique_* counts did not. A shortfall means the listed CVEs are
	// only the top-N by severity (the long tail dropped, all Low).
	if out.Unique != nil && out.Unique.Total() > len(out.CVEs) {
		out.CVEsTruncated = true
	}
	return out
}

// extractCachedCVEs walks security_scan.cves.N.{id,severity,packages,title}
// keys and collapses them into a displayable slice. Ordered by severity
// (critical → high → medium → low → unassigned), then CVE ID.
func extractCachedCVEs(flat map[string]string) []CachedCVE {
	grouped := map[string]*CachedCVE{}
	for k, v := range flat {
		if !strings.HasPrefix(k, traits.PrefixCVEs) {
			continue
		}
		// security_scan.cves.<N>.<field>
		parts := strings.SplitN(strings.TrimPrefix(k, traits.PrefixCVEs), ".", 2)
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
		case "title":
			grouped[idx].Title = v
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

// drawerSections returns the ordered tab list for the security drawer.
// Before the live fetch (detail == nil) it lists scanners that have a cached
// summary; after a successful fetch it lists scanners present in the live
// detail (one dispatch covers every scanner). The cross-scanner "CVEs"
// rollup leads when there is any CVE data, and "Certs" always trails (the
// pane self-reports when nothing is expiring). The first entry is the
// default-visible pane when no tab is targeted via URL fragment.
func drawerSections(cached SecurityDrawerCached, detail *SecurityScanDetail) []SecuritySection {
	var out []SecuritySection
	if cached.Unique != nil || len(cached.CVEs) > 0 {
		out = append(out, SecuritySection{"cves", "CVEs"})
	}
	live := detail != nil
	scanners := []struct {
		slug, label      string
		cachedOK, liveOK bool
	}{
		{"trivy", "Trivy", cached.Trivy != nil, live && detail.Trivy != nil},
		{"debsecan", "debsecan", cached.Debsecan != nil, live && detail.Debsecan != nil},
		{"lynis", "Lynis", cached.Lynis != nil, live && detail.Lynis != nil},
		{"chkrootkit", "chkrootkit", cached.Chkrootkit != nil, live && detail.Chkrootkit != nil},
		{"rkhunter", "rkhunter", cached.Rkhunter != nil, live && detail.Rkhunter != nil},
		{"clamav", "ClamAV", cached.ClamAV != nil, live && detail.ClamAV != nil},
		{"aide", "AIDE", cached.AIDE != nil, live && detail.AIDE != nil},
		{"defender", "Windows Defender", false, live && detail.Defender != nil},
		{"scap", "SCAP/STIG", false, live && detail.SCAP != nil},
	}
	for _, s := range scanners {
		if (live && s.liveOK) || (!live && s.cachedOK) {
			out = append(out, SecuritySection{s.slug, s.label})
		}
	}
	return append(out, SecuritySection{"certs", "Certs"})
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

// securityAIQuestion builds the structured finding line consumed by the
// AI assistant. Format: `<host> · <finding>`. The system prompt at
// server/ai/prompt.go has a "Finding Investigations" section that recognizes
// this `<host> · <kind>` shape and replies with Risk / Remediation / Waiver /
// Investigate sections — so the per-button prompts stay terse and the
// response shape lives in one place.
func securityAIQuestion(host, finding string) string {
	return fmt.Sprintf("%s · %s", host, finding)
}

// ── Per-section AI-question helpers ─────────────────────────────────────────
// Each emits a single structured line: `<host> · <kind>[severity]: <details>`.
// The system prompt does the heavy lifting on the response shape. CVE-bearing
// findings append the canonical record URL so the assistant can cite it.

// cachedCVEAIQuestion is the Ask-AI line for a cached-summary CVE card. The
// cached list is deduped across scanners (trivy / debsecan / windows-updates),
// so the kind is the generic "cve", not "trivy":
//
//	<host> · cve <ID> (<sev>)[, <N> packages][ — <title>] (<cveURL>)
func cachedCVEAIQuestion(host string, c CachedCVE) string {
	s := fmt.Sprintf("%s · cve %s (%s)", host, c.ID, c.Severity)
	if c.Packages > 1 {
		s += fmt.Sprintf(", %d packages", c.Packages)
	}
	if c.Title != "" {
		s += " — " + c.Title
	}
	return s + " (" + cveURL(c.ID) + ")"
}

func trivyAIQuestion(host string, v TrivyVuln) string {
	pkg := v.Package
	if v.Installed != "" {
		pkg += " " + v.Installed
		if v.Fixed != "" {
			pkg += "→" + v.Fixed
		}
	}
	s := fmt.Sprintf("%s · trivy %s (%s): %s", host, v.CVEID, v.Severity, pkg)
	if v.Title != "" {
		s += " — " + v.Title
	}
	return s + " (" + cveURL(v.CVEID) + ")"
}

func debsecanAIQuestion(host string, v DebsecanVuln) string {
	return fmt.Sprintf("%s · debsecan %s (%s): %s (%s)", host, v.CVEID, v.Urgency, v.Package, debianCVEURL(v.CVEID))
}

func lynisWarningAIQuestion(host string, e LynisEntry) string {
	return fmt.Sprintf("%s · lynis-warn %s: %s", host, e.ID, e.Text)
}

func lynisSuggestionAIQuestion(host string, e LynisEntry) string {
	return fmt.Sprintf("%s · lynis-sug %s: %s", host, e.ID, e.Text)
}

func rkhunterAIQuestion(host, file string) string {
	return fmt.Sprintf("%s · rkhunter: %s flagged", host, file)
}

func chkrootkitAIQuestion(host, check, files string) string {
	if files != "" {
		return fmt.Sprintf("%s · chkrootkit %s (suspect: %s)", host, check, files)
	}
	return fmt.Sprintf("%s · chkrootkit %s", host, check)
}

func clamavAIQuestion(host string, f ClamAVFinding) string {
	return fmt.Sprintf("%s · clamav %s in %s", host, f.Signature, f.Path)
}

func aideAIQuestion(host, action, file string) string {
	return fmt.Sprintf("%s · aide %s: %s", host, action, file)
}

func defenderAIQuestion(host string, threats int) string {
	return fmt.Sprintf("%s · defender: %d threats", host, threats)
}

func scapAIQuestion(host string, d *SCAPDetail) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%s · scap %q: %d fail (%d high)", host, d.Profile, d.Fail, d.HighFail)
}

// ddgLink builds a DuckDuckGo search URL for a free-text query — used by raw-
// finding cards (rkhunter file, chkrootkit check, ClamAV signature, AIDE file)
// so an operator can pivot from "is this suspicious?" to web context in one
// click. Pre-0.30.9 affordance restored.
func ddgLink(text string) string {
	return "https://duckduckgo.com/?q=" + url.QueryEscape(text)
}

// SecSeverityBucket is one entry in a severity-filter bar — emitted only when
// Count > 0 so the bar doesn't render empty buckets.
type SecSeverityBucket struct {
	Sev   string // "critical" / "high" / "medium" / "low" / "unassigned" — used as data-sev for the JS filter
	Label string // capitalized form for the button label
	Count int
}

// trivySevCounts groups vulnerabilities by Severity and returns ordered, non-
// zero buckets (critical → high → medium → low → unassigned). Total is the
// sum across buckets — used by the "All (N)" button.
func trivySevCounts(vulns []TrivyVuln) []SecSeverityBucket {
	counts := make(map[string]int)
	for _, v := range vulns {
		counts[normSev(v.Severity)]++
	}
	return rankBuckets(counts)
}

// debsecanUrgencyCounts is the debsecan analog — debsecan reports urgency
// rather than severity, but the bucket vocabulary is the same.
func debsecanUrgencyCounts(vulns []DebsecanVuln) []SecSeverityBucket {
	counts := make(map[string]int)
	for _, v := range vulns {
		counts[normSev(v.Urgency)]++
	}
	return rankBuckets(counts)
}

func normSev(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "critical", "high", "medium", "low":
		return s
	default:
		return "unassigned"
	}
}

func rankBuckets(counts map[string]int) []SecSeverityBucket {
	order := []struct{ sev, label string }{
		{"critical", "Critical"},
		{"high", "High"},
		{"medium", "Medium"},
		{"low", "Low"},
		{"unassigned", "Unassigned"},
	}
	out := make([]SecSeverityBucket, 0, len(order))
	for _, o := range order {
		if n := counts[o.sev]; n > 0 {
			out = append(out, SecSeverityBucket{Sev: o.sev, Label: o.label, Count: n})
		}
	}
	return out
}
