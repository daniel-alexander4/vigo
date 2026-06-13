// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/httpsec"
	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
)

// Renderer holds parsed templates and renders them to HTTP responses.
type Renderer struct {
	pages map[string]*template.Template
}

// isCRUDAction reports whether an action_taken value is one of the three
// state-mutating verbs (created/updated/deleted). Used by the run-detail
// Action column to decide whether a Diverged drift bucket should repaint
// the pill — non-CRUD outcomes (settled/skipped/blocked/failed) keep
// their own semantics and aren't repainted by drift.
func isCRUDAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "create", "created", "update", "updated", "delete", "deleted":
		return true
	}
	return false
}

// funcMap returns the template FuncMap used across all templates.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"T":                     i18n.CatalogT,
		"tSafe":                 i18n.CatalogTSafe,
		"timeAgoL":              timeAgoL,
		"tEnum":                 tEnum,
		"healthSummaryL":        healthSummaryL,
		"statusColor":           statusColor,
		"sparklineSVG":          SparklineSVG,
		"sparklineSVGColor":     SparklineSVGColor,
		"sparklineSVGBounded":   SparklineSVGBounded,
		"formatTime":            formatTime,
		"formatUnixTime":        formatUnixTime,
		"truncGitSha":           truncGitSha,
		"timeAgo":               timeAgo,
		"sortHeader":            sortHeader,
		"pctAccent":             pctAccent,
		"pctAccentInv":          pctAccentInv,
		"failureBadgeClass":     failureBadgeClass,
		"highlightYAML":         highlightYAML,
		"truncUUID":             truncUUID,
		"labelDisplayName":      labelDisplayName,
		"sub":                   func(a, b int) int { return a - b },
		"dict":                  dict,
		"pct":                   pct,
		"truncCommand":          truncCommand,
		"cveURL":                cveURL,
		"debianCVEURL":          debianCVEURL,
		"lynisControlURL":       lynisControlURL,
		"secAIQuestion":         securityAIQuestion,
		"cachedCVEAIQ":          cachedCVEAIQuestion,
		"trivyAIQ":              trivyAIQuestion,
		"debsecanAIQ":           debsecanAIQuestion,
		"lynisWarnAIQ":          lynisWarningAIQuestion,
		"lynisSugAIQ":           lynisSuggestionAIQuestion,
		"rkhunterAIQ":           rkhunterAIQuestion,
		"chkrootkitAIQ":         chkrootkitAIQuestion,
		"clamavAIQ":             clamavAIQuestion,
		"aideAIQ":               aideAIQuestion,
		"defenderAIQ":           defenderAIQuestion,
		"scapAIQ":               scapAIQuestion,
		"ddgLink":               ddgLink,
		"trivySevCounts":        trivySevCounts,
		"debsecanUrgencyCounts": debsecanUrgencyCounts,
		"secSeverityClass": func(sev string) string {
			switch cve.Classify(sev) {
			case cve.SevCritical:
				return "sec-card--critical"
			case cve.SevHigh:
				return "sec-card--high"
			case cve.SevMedium:
				return "sec-card--medium"
			case cve.SevLow:
				return "sec-card--low"
			default:
				return "sec-card--info"
			}
		},
		"secSeverityLabel": func(sev string) string {
			if sev == "" {
				return "Unassigned"
			}
			return strings.ToUpper(sev[:1]) + strings.ToLower(sev[1:])
		},
		"secSeverityNorm": func(sev string) string {
			return cve.Norm(sev)
		},
		// convergenceBadgeClass maps the (failure-axis status, drift-axis
		// level) pair to the `.badge--*` variant that paints the
		// convergence ratio cell. Routing every per-row badge through the
		// same classification the pill counts above the table use keeps
		// the row colors and the pill counts in sync — no chance of one
		// saying "Diverged 1" while the matching row renders identically
		// to its non-Diverged neighbors.
		//
		// Drift overrides status: Diverged renders Peach regardless of
		// the failure-axis bucket. That's the visual link an operator
		// needs to spot the diverged envoy in the table; Status (Failed,
		// Degraded, Converged) would otherwise win and Diverged would be
		// invisible.
		//
		// Per-run badges carry the envoy's *current* drift label too
		// (RunRow.EnvoyDriftLabel, populated at row build time via the
		// FleetIndex), so every run row of a Diverged envoy paints Peach
		// — operator can trace the envoy header pill down to its
		// contributing run rows at a glance. Drift is a present-tense
		// classification across the UI (chip counts, header pills, fleet
		// stats); per-run badges follow the same rule. Pass the empty
		// string only when no envoy context exists (no current call
		// site does — kept as the no-op fallback).
		"convergenceBadgeClass": func(status, drift string) string {
			if convergence.ClassifyDrift(drift) == convergence.DriftDiverged {
				return "badge--diverged"
			}
			switch convergence.ClassifyStatus(status) {
			case convergence.StatusConverged:
				return "badge--converged"
			case convergence.StatusDegraded:
				return "badge--degraded"
			case convergence.StatusFailed:
				return "badge--failed"
			}
			return "badge--unknown"
		},
		// convergenceRatio renders the canonical badge text used on every
		// run/envoy convergence indicator across the UI:
		//   "<changed>/<failed>/<total>"
		// e.g. "3/1/172". The bare slash form keeps the badge compact;
		// the title attribute (convergenceHover) carries the labeled
		// "X changed · Y failed of Z configcrates" form for operators who
		// need the legend. Badge color comes from convergenceBadgeClass
		// against the envoy/run's bucket — never the raw failed/total
		// ratio, so the table colors agree with the pill counts.
		"convergenceRatio": func(changed, failed, total int64) string {
			return fmt.Sprintf("%d/%d/%d", changed, failed, total)
		},
		// convergenceHover renders the verbose labeled form for the title
		// attribute on the badge — operator hovers and gets a sentence
		// instead of a numeric trio.
		"convergenceHover": func(changed, failed, total int64) string {
			return fmt.Sprintf("%d changed · %d failed of %d configcrates", changed, failed, total)
		},
		// actionCSSClass maps the richer per-resource action_taken
		// vocabulary the agent emits (create/update/delete plus
		// skipped/settled/"already in desired state"/blocked/failed)
		// to badge variants, so each row's Action column carries a
		// distinct color. We deliberately don't route through
		// convergence.ClassifyAction here — that function collapses
		// every non-CRUD outcome to noop for drift-counting, which
		// erases the visual distinction between "skipped because
		// when:false" and "settled because state already matched."
		//
		// driftLabel is the row's per-module drift bucket (one of "none"
		// / "changed" / "diverged" / ""). When the module is Diverged AND
		// the row's action is a CRUD verb, the cell repaints peach so
		// the operator can spot the runs that are part of a persistent
		// drift pattern. Failed and non-CRUD outcomes keep their own
		// semantic colors — drift severity doesn't override "this thing
		// is on fire" (red) or "agent skipped this on purpose" (muted).
		"actionCSSClass": func(action, driftLabel string) string {
			if convergence.ClassifyDrift(driftLabel) == convergence.DriftDiverged && isCRUDAction(action) {
				return "diverged"
			}
			switch strings.ToLower(strings.TrimSpace(action)) {
			case "create", "created":
				return "success"
			case "update", "updated":
				return "info"
			case "delete", "deleted":
				return "danger"
			case "settled", "already in desired state", "ok", "noop", "none":
				return "mauve"
			case "skipped":
				return "muted"
			case "blocked":
				return "warning"
			case "failed":
				return "danger"
			default:
				return "muted"
			}
		},
		// actionLabel returns the text the run-detail Action pill renders.
		// For CRUD verbs on a Diverged module the label flips to "Diverged"
		// — the verb (Updated/Created/Deleted) is replaced because in the
		// Diverged case the drift signal is what the operator needs to
		// see first. For all other rows the raw action_taken string is
		// preserved, so non-CRUD outcomes (settled/skipped/blocked/
		// failed) carry their normal labels.
		"actionLabel": func(action, driftLabel string) string {
			if convergence.ClassifyDrift(driftLabel) == convergence.DriftDiverged && isCRUDAction(action) {
				return "Diverged"
			}
			return action
		},
		"tracePathTree":       tracePathTree,
		"traceGreenBg":        traceGreenBg,
		"traceGreenBord":      traceGreenBord,
		"traceGreenText":      traceGreenText,
		"traceGreenBordClass": traceGreenBordClass,
		"traceGreenTextClass": traceGreenTextClass,
		"wbucket":             wbucket,
		"pctClass":            pctClass,
		"pctClassBg":          pctClassBg,
		"pctClassInv":         pctClassInv,
		"ccClass":             ccClass,
		"isLastIndex":         func(i, length int) bool { return i == length-1 },
		"inSlice": func(needle string, haystack []string) bool {
			for _, s := range haystack {
				if s == needle {
					return true
				}
			}
			return false
		},
		"maskSecret": func(v any) string {
			s := fmt.Sprintf("%v", v)
			if strings.HasPrefix(s, "secret:") {
				return "secret:****"
			}
			return s
		},
		"sortedKeys": func(m map[string][]string) []string {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return keys
		},
		"joinStrings": func(ss []string, sep string) string {
			return strings.Join(ss, sep)
		},
		"groupWaiversByFile": groupWaiversByFile,
		"traceUsercrates":    traceUsercrates,
		"traceModuleLevels":  traceModuleLevels,
		"filterConds": func(conds []config.ConditionalTraceEntry, side string) []config.ConditionalTraceEntry {
			var out []config.ConditionalTraceEntry
			for _, c := range conds {
				if c.EvalSide == side {
					out = append(out, c)
				}
			}
			return out
		},
		"strContains": strings.Contains,
		"fwSlug":      fwSlug,
		"fwTotal": func(args ...int) int {
			total := 0
			for _, v := range args {
				total += v
			}
			return total
		},
		"fwPct": func(part, total int) float64 {
			if total == 0 {
				return 0
			}
			return float64(part) / float64(total) * 100
		},
		// controlNature maps a control's Scope tag to a human-readable
		// nature label for the framework report table. enforced and
		// attested are both Technical (Vigo applies or measures);
		// documented is Process (operator-uploaded artifact);
		// not_applicable is N/A. Unknown scopes fall back to "—".
		"controlNature": func(scope string) string {
			switch scope {
			case "enforced", "attested":
				return "Technical"
			case "documented":
				return "Process"
			case "not_applicable":
				return "N/A"
			default:
				return "—"
			}
		},
		"controlNatureClass": func(scope string) string {
			switch scope {
			case "enforced", "attested":
				return "badge--info"
			case "documented":
				return "badge--pending"
			case "not_applicable":
				return "badge--unknown"
			default:
				return "badge--unknown"
			}
		},
		"cssClass": func(s string) string {
			r := strings.NewReplacer(" ", "-", ".", "-", "(", "", ")", "", "/", "-", ":", "-", ",", "")
			return r.Replace(strings.ToLower(s))
		},
		"riskColor": func(score int) string {
			switch risk.Classify(score) {
			case risk.LevelCritical:
				return "danger"
			case risk.LevelHigh:
				return "warning"
			case risk.LevelMedium:
				return "brand"
			default:
				return "success"
			}
		},
		"lifecycleClass": func(state string) string {
			switch state {
			case "available":
				return "brand"
			case "commissioned", "active":
				return "converged"
			case "decommissioning":
				return "relapsed"
			case "retired":
				return "no-data"
			default:
				return "no-data"
			}
		},
		"humanDuration": func(start, end time.Time) string {
			if start.IsZero() || end.IsZero() {
				return ""
			}
			d := end.Sub(start)
			if d < 0 {
				return ""
			}
			switch {
			case d < time.Second:
				return fmt.Sprintf("%dms", d.Milliseconds())
			case d < time.Minute:
				return fmt.Sprintf("%ds", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
			default:
				return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
			}
		},
		"humanMs": func(ms int64) string {
			d := time.Duration(ms) * time.Millisecond
			switch {
			case d < time.Second:
				return fmt.Sprintf("%dms", ms)
			case d < time.Minute:
				return fmt.Sprintf("%ds", int(d.Seconds()))
			case d < time.Hour:
				return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
			default:
				return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
			}
		},
		"swarmStatusClass": func(status string) string {
			switch status {
			case "source":
				// Green — the seeder has the blob by construction.
				// Distinct label rendered in the template; the pill
				// class shares converged's green palette so the row
				// reads "done" at a glance alongside complete.
				return "converged"
			case "complete":
				return "converged"
			case "cached":
				return "cached"
			case "downloading":
				return "relapsed"
			case "pending":
				return "no-data"
			case "skipped":
				return "failed"
			case "revoking":
				// Yellow: seed is revoked, envoy still has the blob.
				return "relapsed"
			case "revoked":
				// Green: envoy has successfully evicted the blob.
				return "converged"
			default:
				return "no-data"
			}
		},
		"swarmFileStatusClass": func(status string) string {
			switch status {
			case "complete", "source", "live":
				// Green for anything that reads as "present and healthy."
				// Source is the seeder (has by construction); live
				// covers materialized gitback mirrors and per-user
				// longdrawer/lockbox files.
				return "converged"
			case "cached":
				return "cached"
			case "distributing", "downloading", "stale":
				// Yellow — in flight or past the stale threshold.
				return "relapsed"
			case "revoked", "revoking":
				return "no-data"
			default:
				return "no-data"
			}
		},
		"sourceColor": func(source string, colors map[string]string) string {
			if c, ok := colors[source]; ok {
				return ccClass(c)
			}
			return ccClass("#a0aec0")
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"add": func(a, b int) int {
			return a + b
		},
		"verificationClass": func(result string) string {
			switch result {
			case "verified":
				return "converged"
			case "unverifiable":
				return "relapsed"
			case "failed":
				return "failed"
			default:
				return "no-data"
			}
		},
		"osLabel": func(os string) string {
			switch os {
			case "darwin":
				return "macOS"
			case "linux":
				return "Linux"
			case "windows":
				return "Windows"
			case "freebsd":
				return "FreeBSD"
			case "openbsd":
				return "OpenBSD"
			case "netbsd":
				return "NetBSD"
			case "illumos":
				return "illumos"
			default:
				return os
			}
		},
		"vizStatusColor":     VizStatusColor,
		"vizActionColor":     VizActionColor,
		"vizComplianceColor": VizComplianceColor,
		"donutArc": func(cx, cy, r, startAngle, endAngle float64) string {
			return DonutArc(cx, cy, r, startAngle, endAngle)
		},
		// deref unwraps a *bool into a bool so templates can branch on
		// the value without Go template's nil-is-not-false quirk.
		// Generic helper for any `*bool` JSON field where `nil` should
		// render as `false`.
		"deref": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
		},
		// humanBytes renders a byte count as a compact string
		// ("1.6 KiB", "12.1 MiB"). Mirrors ui/payload.go::humanSize for
		// template-only call sites that can't reuse the Go helper.
		"humanBytes": humanSize,
		// humanBytesU is the uint64 variant — curator catalog entries
		// carry sizes as uint64 (protobuf wire compatibility); the
		// existing humanBytes signature expects int64 and would error at
		// template-execute time on a uint64 arg. Same body otherwise.
		"humanBytesU": func(n uint64) string {
			if n > 1<<62 {
				n = 1 << 62 // clamp absurd inputs so the int64 conversion never wraps
			}
			return humanSize(int64(n))
		},
		// severityColor maps a fleetpressure.Severity to the existing
		// stat-card-- color modifier so the dashboard "Agent Health"
		// rollup uses the same palette as the rest of the dashboard.
		"severityColor": func(s fleetpressure.Severity) string {
			switch s {
			case fleetpressure.Fail:
				return "danger"
			case fleetpressure.Warn:
				return "warning"
			case fleetpressure.OK:
				return "success"
			default:
				return "brand"
			}
		},
		// severityLabel renders a fleetpressure.Severity as the
		// operator-facing word shown in /agent-health cells. Unknown
		// collapses to a dash so missing data doesn't look healthy.
		"severityLabel": func(s fleetpressure.Severity) string {
			switch s {
			case fleetpressure.Fail:
				return "Failing"
			case fleetpressure.Warn:
				return "Degraded"
			case fleetpressure.OK:
				return "Healthy"
			default:
				return "—"
			}
		},
		// severityTextClass maps a fleetpressure.Severity to a text-*
		// utility class so inline labels match the column-card palette.
		"severityTextClass": func(s fleetpressure.Severity) string {
			switch s {
			case fleetpressure.Fail:
				return "text-danger"
			case fleetpressure.Warn:
				return "text-warning"
			case fleetpressure.OK:
				return "text-success"
			default:
				return "text-muted"
			}
		},
		// healthSummary renders the one-line per-axis breakdown for the
		// agent-health composite — used by the /envoys row hover and
		// the per-envoy footprint status card. Routes through the same
		// helper handler.go uses to populate EnvoyRow.HealthSummary so
		// both surfaces phrase the breakdown identically.
		"healthSummary": HealthSummary,
		// severityPillClass maps a fleetpressure.Severity to the
		// status-pill / badge class used in table cells (e.g. the
		// /envoys "Agent Health" column). Unknown collapses to the
		// neutral badge so missing data doesn't look healthy.
		"severityPillClass": func(s fleetpressure.Severity) string {
			switch s {
			case fleetpressure.Fail:
				return "status-pill status-pill--failed"
			case fleetpressure.Warn:
				return "status-pill status-pill--degraded"
			case fleetpressure.OK:
				return "status-pill status-pill--converged"
			default:
				return "badge badge--unknown"
			}
		},
		// metricPillClass maps a fleetpressure.Severity to a status-pill
		// class for inline metric values on the per-envoy Agent Health
		// card. Unknown collapses to status-pill--no-data (neutral
		// surface tint) so a missing metric reads as data-absent rather
		// than an opinion. Always returns a status-pill class (never
		// falls through to .badge) so every metric pill in the grid
		// has identical geometry.
		"metricPillClass": func(s fleetpressure.Severity) string {
			switch s {
			case fleetpressure.Fail:
				return "status-pill status-pill--failed"
			case fleetpressure.Warn:
				return "status-pill status-pill--degraded"
			case fleetpressure.OK:
				return "status-pill status-pill--converged"
			default:
				return "status-pill status-pill--no-data"
			}
		},
		// humanFDPct renders a host_self.fd_pct float with one decimal
		// or "—" for the unset case. Templates can't easily distinguish
		// PercentMetric.Present from a real zero, so this helper
		// branches and formats in one go.
		"humanFDPct": func(p fleetpressure.PercentMetric) string {
			if !p.Present {
				return "—"
			}
			return fmt.Sprintf("%.1f%%", p.Value)
		},
		// humanMetric renders a fleetpressure.Metric value or "—"
		// when absent. Centralizes the missing-trait dash so every
		// row in the agent-footprint table renders consistently.
		"humanMetric": func(m fleetpressure.Metric) string {
			if !m.Present {
				return "—"
			}
			return fmt.Sprintf("%d", m.Value)
		},
		// humanRSS renders a byte count compactly when present, else "—".
		"humanRSS": func(m fleetpressure.Metric) string {
			if !m.Present {
				return "—"
			}
			return humanSize(int64(m.Value))
		},
		// humanUptime renders an uptime in seconds as days/hours/minutes
		// or "—" when the trait is missing. Uses the largest unit so an
		// envoy that's been up two days reads "2d", not "172800s".
		"humanUptime": func(m fleetpressure.Metric) string {
			if !m.Present {
				return "—"
			}
			s := m.Value
			switch {
			case s < 60:
				return fmt.Sprintf("%ds", s)
			case s < 3600:
				return fmt.Sprintf("%dm", s/60)
			case s < 86400:
				return fmt.Sprintf("%dh", s/3600)
			default:
				return fmt.Sprintf("%dd", s/86400)
			}
		},
		// truncCol shortens a string to fit in a fixed-width table column
		// without breaking layout when an operator publishes an artifact
		// with a verbose `target` glob or recipients list. Adds an
		// ellipsis to signal truncation; max <= 1 is a no-op (degenerate).
		"truncCol": func(s string, max int) string {
			if max <= 1 || len(s) <= max {
				return s
			}
			return s[:max-1] + "…"
		},
		// div is integer division for templates. Used by the curator
		// detail page to drop millis from the catalog entry's
		// `issued_at` before handing to formatUnixTime (which expects
		// seconds). One-line helper rather than another timestamp
		// formatter — keeps the render layer thin.
		"div": func(a, b int64) int64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		// curatorArtifactID computes sha256(founder_puddle_pubkey ‖ name)
		// for a CatalogEntry from the template. Mirrors the agent's
		// catalog::artifact_id_for; cheap (one sha256 per row) and
		// keeps the entry shape self-contained so the template doesn't
		// need an artifact_id field bolted on. Returns "" on a bad
		// pubkey — degenerate row will show "" in the column.
		"curatorArtifactID": func(e curatormesh.CatalogEntry) string {
			id, _ := curatormesh.ArtifactID(e)
			return id
		},
		// curatorPlatformSummary collapses an entry's versions ×
		// platforms into a sorted, deduped "os/arch,os/arch" string
		// for the list view's PLATFORMS column. Truncated for display
		// when the union grows large.
		"curatorPlatformSummary": func(e curatormesh.CatalogEntry) string {
			if len(e.Body.Versions) == 0 {
				return "—"
			}
			seen := make(map[string]struct{})
			for _, av := range e.Body.Versions {
				for _, p := range av.Platforms {
					seen[p.OS+"/"+p.Arch] = struct{}{}
				}
			}
			out := make([]string, 0, len(seen))
			for k := range seen {
				out = append(out, k)
			}
			sort.Strings(out)
			joined := strings.Join(out, ",")
			if len(joined) > 28 {
				return joined[:27] + "…"
			}
			return joined
		},
		// curatorIsBlocked tests whether an artifact_id (hex) appears in
		// the blocked-list slice. Linear scan — list is small (most fleets
		// will have zero blocks, a serious incident only adds a few).
		"curatorIsBlocked": func(aid string, blocked []db.CuratorBlockedArtifact) bool {
			low := strings.ToLower(aid)
			for _, b := range blocked {
				if strings.EqualFold(b.ArtifactID, low) {
					return true
				}
			}
			return false
		},
		// curatorOrphanBlocks returns block-table entries whose
		// artifact_id has no live catalog row. Lets the operator see
		// in-effect blocks that survive publisher rm.
		"curatorOrphanBlocks": func(cat *curatormesh.Catalog, blocked []db.CuratorBlockedArtifact) []db.CuratorBlockedArtifact {
			if cat == nil || len(blocked) == 0 {
				return nil
			}
			live := make(map[string]struct{}, len(cat.Artifacts))
			for i := range cat.Artifacts {
				if aid, err := curatormesh.ArtifactID(cat.Artifacts[i]); err == nil {
					live[strings.ToLower(aid)] = struct{}{}
				}
			}
			var out []db.CuratorBlockedArtifact
			for _, b := range blocked {
				if _, ok := live[strings.ToLower(b.ArtifactID)]; !ok {
					out = append(out, b)
				}
			}
			return out
		},
		// poolqMsgID derives sha256(canonical_bytes) for a Message —
		// reproduces what the agent's `Message::msg_id_hex` does so the
		// detail page can show a stable short id per message without
		// the trait collector having to carry it on the wire. Pure;
		// errors render as the empty string (degenerate row).
		"poolqMsgID": func(m poolqmesh.Message) string {
			id, _ := poolqmesh.MsgIDFor(m.Body)
			return id
		},
		// poolqIsBlocked: linear scan for the topics-list template's
		// per-row status badge. Same shape curatorIsBlocked has.
		"poolqIsBlocked": func(topicID string, blocked []db.PoolqBlockedTopic) bool {
			low := strings.ToLower(topicID)
			for _, b := range blocked {
				if strings.EqualFold(b.TopicID, low) {
					return true
				}
			}
			return false
		},
		// poolqOrphanBlocks returns block-table entries whose topic_id
		// has no live snapshot row — typically the founder scrubbed
		// `~/.vigo-poolq/<topic_id>/` directly. Lets the operator see
		// in-effect blocks that survive a founder-side rm.
		"poolqOrphanBlocks": func(snap *poolqmesh.Snapshot, blocked []db.PoolqBlockedTopic) []db.PoolqBlockedTopic {
			if snap == nil || len(blocked) == 0 {
				return nil
			}
			live := make(map[string]struct{}, len(snap.Topics))
			for i := range snap.Topics {
				live[strings.ToLower(snap.Topics[i].TopicIDHex)] = struct{}{}
			}
			var out []db.PoolqBlockedTopic
			for _, b := range blocked {
				if _, ok := live[strings.ToLower(b.TopicID)]; !ok {
					out = append(out, b)
				}
			}
			return out
		},
	}
}

// fwSlug maps compliance framework display names to URL slugs.
func fwSlug(name string) string {
	slugs := map[string]string{
		"HIPAA":                       "compliance",
		"HITRUST CSF":                 "hitrust",
		"SOC 2":                       "soc2",
		"PCI DSS v4.0":                "pci",
		"CIS Benchmarks":              "cis",
		"NIST 800-53":                 "nist",
		"ISO 27001":                   "iso27001",
		"GDPR":                        "gdpr",
		"NERC CIP":                    "nerccip",
		"Cyber Essentials+":           "cyberessentials",
		"IEC 62443":                   "iec62443",
		"SOX":                         "sox",
		"FINRA":                       "finra",
		"MiFID II":                    "mifid2",
		"CCSS":                        "ccss",
		"FDA 21 CFR Part 11":          "fda21cfr11",
		"Gaming Commission":           "gaming",
		"CIS Windows":                 "ciswindows",
		"CIS Ubuntu":                  "cisubuntu",
		"CIS RHEL":                    "cisrhel",
		"ABA Cybersecurity":           "abacyber",
		"NY DFS Cyber (23 NYCRR 500)": "nydfscyber",
	}
	if s, ok := slugs[name]; ok {
		return s
	}
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// NewRenderer parses all embedded templates and returns a ready Renderer.
// Each page template is parsed independently on top of a cloned base+partials
// template set, avoiding the name collision pitfall.
func NewRenderer() (*Renderer, error) {
	// Parse base layout and all partials into a shared template set.
	base, err := template.New("").Funcs(funcMap()).ParseFS(Assets,
		"templates/base.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parsing base templates: %w", err)
	}

	// Discover page templates.
	pageFiles, err := fs.Glob(Assets, "templates/pages/*.html")
	if err != nil {
		return nil, fmt.Errorf("globbing page templates: %w", err)
	}

	pages := make(map[string]*template.Template, len(pageFiles))
	for _, pf := range pageFiles {
		// Clone the base template set for each page.
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning base for %s: %w", pf, err)
		}

		// Parse the page template on top of the cloned base.
		_, err = clone.ParseFS(Assets, pf)
		if err != nil {
			return nil, fmt.Errorf("parsing page %s: %w", pf, err)
		}

		// Extract page name from path: "templates/pages/dashboard.html" -> "dashboard"
		name := strings.TrimPrefix(pf, "templates/pages/")
		name = strings.TrimSuffix(name, ".html")
		pages[name] = clone
	}

	return &Renderer{pages: pages}, nil
}

// Render executes the base template for the named page with the given data.
func (r *Renderer) Render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := r.pages[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	httpsec.Headers(w)
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		slog.Error("template execution error", "name", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// RenderDocs is removed — docs pages now use the regular base template via Render().

// RenderFragment executes a named template definition (not a full page) for HTMX fragment responses.
func (r *Renderer) RenderFragment(w http.ResponseWriter, page, fragment string, data any) {
	tmpl, ok := r.pages[page]
	if !ok {
		slog.Error("template not found for fragment", "page", page, "fragment", fragment)
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	httpsec.Headers(w)
	if err := tmpl.ExecuteTemplate(w, fragment, data); err != nil {
		slog.Error("fragment execution error", "page", page, "fragment", fragment, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// statusColor maps a failure-axis status string to a CSS color class
// name. Drift is on a separate axis and not consulted here. "stale" is
// reachability rather than convergence and falls through to the
// freshness-derived warning bucket.
func statusColor(status string) string {
	switch convergence.ClassifyStatus(status) {
	case convergence.StatusConverged:
		return "success"
	case convergence.StatusDegraded:
		return "warning"
	case convergence.StatusFailed:
		return "danger"
	}
	if status == "stale" {
		return "warning"
	}
	return "muted"
}

// formatTime renders a time.Time as "2006-01-02 15:04" in the server's local timezone.
func formatTime(t time.Time) string {
	local := t.Local()
	// Show seconds when the timestamp is less than 60 seconds old so operators
	// can see sub-minute freshness on fast check-in intervals.
	if time.Since(local) < 60*time.Second {
		return local.Format("2006-01-02 15:04:05")
	}
	return local.Format("2006-01-02 15:04")
}

// formatUnixTime renders a unix-seconds timestamp using the same
// rules as [formatTime]. Returns "—" for zero/negative values so
// templates don't render "1970-01-01 00:00" for missing data.
func formatUnixTime(secs int64) string {
	if secs <= 0 {
		return "—"
	}
	return formatTime(time.Unix(secs, 0))
}

// timeAgo renders a time.Time as a human-readable relative string ("2m ago", "3h ago").
// Returns "-" for zero times. Used in table columns where recency matters more than
// exact timestamps (the absolute time is shown via title tooltip in the template).
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < 30*time.Second:
		return "just now"
	case d < 90*time.Second:
		return "1m ago"
	case d < 60*time.Minute:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 90*time.Minute:
		return "1h ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// timeAgoL is the locale-aware relative-time helper. English delegates to
// timeAgo (the single source of the English buckets); other locales branch
// here. Relative time carries an interpolated count, which the flat string
// catalog can't express, so the small fixed locale set is branched in code —
// the eventual CLDR generalization (golang.org/x/text/message) would replace
// this. Templates call {{timeAgoL $.Locale .When}}.
func timeAgoL(localeArg any, t time.Time) string {
	locale := i18n.LocaleStr(localeArg)
	if locale != "fr" {
		return timeAgo(t)
	}
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 90*time.Second:
		if d < 30*time.Second {
			return "à l'instant"
		}
		return "il y a 1 min"
	case d < 60*time.Minute:
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	case d < 90*time.Minute:
		return "il y a 1 h"
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d h", int(d.Hours()))
	case d < 48*time.Hour:
		return "hier"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("il y a %d j", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("il y a %d sem", int(d.Hours()/(24*7)))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// tEnum localizes a data-derived enum value (a run outcome, convergence
// status, agent-health severity label, …) by looking up enum.<value> in the
// catalog, normalizing the value to a key token (lowercased, spaces→_). An
// unmapped value passes through unchanged — so Vigo-specific or unexpected
// values, and the English source casing, are preserved. Chrome *literals*
// (headers, buttons, filter-pill labels) use {{T}} instead; tEnum is only for
// values that arrive as data.
func tEnum(localeArg any, value string) string {
	locale := i18n.LocaleStr(localeArg)
	// English (and the default/empty locale) passes through untouched, so a
	// disabled or English deployment renders the exact source value — the
	// enum.* English entries exist only for catalog symmetry, never runtime.
	if value == "" || locale == "" || locale == i18n.DefaultLocaleFallback {
		return value
	}
	key := "enum." + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", "_"))
	if s := i18n.CatalogT(locale, key); s != key {
		return s
	}
	return value
}

// healthSummaryL renders the locale-aware Agent Health pill hover title from
// the row's raw *fleetpressure.EnvoyPressure. nil (no agent-health trait
// reported) yields the empty string so the template falls back to the
// "no traits" message. Templates call {{healthSummaryL $.Locale .Pressure}}.
func healthSummaryL(localeArg any, p *fleetpressure.EnvoyPressure) string {
	if p == nil {
		return ""
	}
	return healthSummaryLocale(i18n.LocaleStr(localeArg), *p)
}

// EnvoyFreshness pairs the canonical freshness predicate with the
// matching human-readable last-seen label. The threshold derives
// per-envoy from freshness.IsStaleFromCycle: 2.5 × max(configured,
// observed) where observed is the agent's smoothed inter-CheckIn
// cadence pulled from FleetIndex. Every surface that classifies an
// envoy as stale routes through this so the label, cell color, pill
// count, doctor check, risk "Lost contact" factor, and stale-notifier
// always agree.
//
// observedCycle = 0 means bootstrap (no agent-reported cadence yet) —
// predicate falls back to configured interval alone.
//
// The label override below exists because timeAgo's "just now" bucket
// is fixed at 30 s. At fast check-in intervals (e.g. 5 s configured →
// 12.5 s threshold under bootstrap) an envoy that's already stale
// would otherwise render as "just now" in the staleness color.
// Printing precise seconds in that window keeps label and color
// consistent.
func EnvoyFreshness(lastSeen time.Time, baseInterval, offlineThreshold, observedCycle time.Duration) (label string, stale bool) {
	if lastSeen.IsZero() {
		return "-", false
	}
	stale = freshness.IsStaleFromCycle(lastSeen, baseInterval, observedCycle, offlineThreshold)
	age := time.Since(lastSeen)
	if stale && age < 30*time.Second {
		secs := int(age.Seconds())
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%ds ago", secs), true
	}
	return timeAgo(lastSeen), stale
}

// toFloat64 converts any numeric type to float64 for template functions.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	default:
		return 0
	}
}

// pctBand maps a 0-100 percentage to a Catppuccin-token band ("success" /
// "warning" / "danger") under the "higher is worse" semantic — used for
// failure ratios, risk scores, and drift rates. Cutoffs:
//   - >50% → danger
//   - >20% → warning
//   - else → success
//
// (The percentage scale matches what the original HSL gradient encoded
// as a smooth red-yellow-green ramp; the band classification is the
// honest 3-state approximation.)
func pctBand(v any) string {
	pct := toFloat64(v)
	switch {
	case pct > 50:
		return "danger"
	case pct > 20:
		return "warning"
	default:
		return "success"
	}
}

// pctBandInv is the inverse: "higher is better" semantic — convergence %,
// compliance coverage, etc. Cutoffs:
//   - <50% → danger
//   - <80% → warning
//   - else → success
func pctBandInv(v any) string {
	pct := toFloat64(v)
	switch {
	case pct < 50:
		return "danger"
	case pct < 80:
		return "warning"
	default:
		return "success"
	}
}

// pctAccent returns the band string ("success" / "warning" / "danger")
// for use in CSS class names like `stat-card--{{pctAccent .X}}`.
// "Higher is worse" semantic.
func pctAccent(v any) string { return pctBand(v) }

// pctAccentInv is the inverse band — "higher is better" semantic.
func pctAccentInv(v any) string { return pctBandInv(v) }

// failureBadgeClass returns the badge class modifier ("badge--compliant"
// / "badge--warning" / "badge--failed") for a `failedCount / totalCount`
// pair. Used by dashboard fleet + activity tables in place of inline
// background/color styling. Token-driven; matches the existing
// `.badge--*` rules in the stylesheet.
func failureBadgeClass(failed, total any) string {
	t := toFloat64(total)
	if t <= 0 {
		return "badge--compliant"
	}
	pct := (toFloat64(failed) / t) * 100.0
	switch {
	case pct == 0:
		return "badge--compliant"
	case pct > 25:
		return "badge--failed"
	default:
		return "badge--warning"
	}
}

// sortHeader renders a sortable <th> element. label is the display text, col is
// the sort key, activeCol and activeDir are the current sort state, and baseURL
// is the page URL with existing query params (to preserve filters).
func sortHeader(label, col, activeCol, activeDir, baseURL string) template.HTML {
	nextDir := "asc"
	arrow := "&#x25B4;&#x25BE;" // up+down (unsorted)
	active := ""
	if col == activeCol {
		active = " sort-active"
		if activeDir == "asc" {
			nextDir = "desc"
			arrow = "&#x25B4;" // up arrow
		} else {
			nextDir = "asc"
			arrow = "&#x25BE;" // down arrow
		}
	}

	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}
	href := fmt.Sprintf("%s%ssort=%s&dir=%s", baseURL, sep, col, nextDir)

	return template.HTML(fmt.Sprintf(
		`<th class="sortable%s"><a href="%s">%s <span class="sort-arrow">%s</span></a></th>`,
		active, template.HTMLEscapeString(href), template.HTMLEscapeString(label), arrow,
	))
}

// highlightYAML applies chroma syntax highlighting to YAML source code.
// Returns HTML with CSS classes for styling (uses monokai style).
func highlightYAML(source string) template.HTML {
	lexer := lexers.Get("yaml")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := chromahtml.New(chromahtml.WithClasses(true))
	style := styles.Get("monokai")

	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(source) + "</pre>")
	}

	return template.HTML(buf.String())
}

// truncCommand returns a command string truncated to 50 chars for display.
func truncCommand(cmd string) string {
	// Show only the first line for multi-line scripts.
	if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
		cmd = cmd[:idx] + "..."
	}
	if len(cmd) > 50 {
		return cmd[:47] + "..."
	}
	return cmd
}

// truncUUID returns the first 8 characters of a UUID string.
func truncUUID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// truncGitSha returns the first 12 characters of a git sha — the
// standard short-sha length operators see in `git log --oneline`.
func truncGitSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// labelDisplayName returns the rightmost colon-delimited segment of a
// swarm blob label — the file name as the user thinks of it. Legacy
// gitback labels take the form `gitback:<host>:<user>:<project>`
// and this helper surfaces just the project tail. Labels without a
// colon (e.g. admin filecast labels like `pc-payload-1777298130`)
// pass through unchanged. The full routing label is intentionally
// not surfaced anywhere user visible — host/user are in the row
// subtitle when relevant.
func labelDisplayName(label string) string {
	if i := strings.LastIndex(label, ":"); i >= 0 && i < len(label)-1 {
		return label[i+1:]
	}
	return label
}

// dict creates a map from key-value pairs for use in templates.
func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs)-1; i += 2 {
		key, ok := pairs[i].(string)
		if ok {
			m[key] = pairs[i+1]
		}
	}
	return m
}

// pct calculates a percentage: (numerator / denominator) * 100.
// Returns 0 if denominator is 0.
func pct(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

// traceGreenRamp returns the interpolation factor for the green ramp,
// matching the CLI's greenRamp: light green (#c6f6d5) to dark green (#14532d),
// with a minimum of 5 effective levels so shallow traces stay light.
func traceGreenRamp(depth, total int) float64 {
	if total <= 1 {
		return 0
	}
	effective := total
	if effective < 5 {
		effective = 5
	}
	return float64(depth) / float64(effective-1)
}

// traceGreenBg returns an rgb() background color for a trace level.
// Returns template.CSS so html/template doesn't escape it in style attributes.
func traceGreenBg(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	r := int(240 - t*200)
	g := int(253 - t*170)
	b := int(230 - t*180)
	return template.CSS(fmt.Sprintf("rgb(%d,%d,%d)", r, g, b))
}

// traceGreenBord emits two CSS custom properties — --tb-l for Latte (dark
// green, readable on pale trace bg) and --tb-d for Mocha (light green,
// readable on dark trace bg). The corresponding CSS rule picks the active
// value based on `html.theme-dark`. Emitted as an inline style snippet so
// each depth in the inheritance chain carries its own gradient position.
func traceGreenBord(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	lr, lg, lb := int(91-t*65), int(179-t*87), int(112-t*64)  // Latte: #5bb370 → #1a5c30
	dr, dg, db := int(140+t*26), int(210+t*17), int(140+t*21) // Mocha: #8cd28c → #a6e3a1 (approaches Mocha Green)
	return template.CSS(fmt.Sprintf("--tb-l:rgb(%d,%d,%d);--tb-d:rgb(%d,%d,%d)", lr, lg, lb, dr, dg, db))
}

// traceGreenText mirrors traceGreenBord but for text color. The --tc-l and
// --tc-d custom properties are consumed by CSS rules keyed on theme.
func traceGreenText(depth, total int) template.CSS {
	t := traceGreenRamp(depth, total)
	lr, lg, lb := int(58-t*32), int(138-t*58), int(80-t*32)   // Latte: #3a8a50 → #1a5030
	dr, dg, db := int(120+t*46), int(200+t*27), int(130+t*31) // Mocha: #78c882 → #a6e3a1
	return template.CSS(fmt.Sprintf("--tc-l:rgb(%d,%d,%d);--tc-d:rgb(%d,%d,%d)", lr, lg, lb, dr, dg, db))
}

// traceRampSteps is the number of discrete buckets the continuous green ramp
// is quantized into for de-inlined CSS classes (21 levels: 0..traceRampSteps).
// The .tgb-N / .tgt-N classes in style.css are generated at t = N/traceRampSteps
// using the exact same rgb() formulas as traceGreenBord / traceGreenText.
const traceRampSteps = 20

func traceGreenBucket(depth, total int) int {
	return int(traceGreenRamp(depth, total)*float64(traceRampSteps) + 0.5)
}

// traceGreenBordClass / traceGreenTextClass return the bucketed border / text
// gradient class — the CSP-safe replacement for the traceGreenBord /
// traceGreenText inline style attributes (each class sets the same --tb-* /
// --tc-* custom properties the inline style did).
func traceGreenBordClass(depth, total int) string {
	return "tgb-" + strconv.Itoa(traceGreenBucket(depth, total))
}
func traceGreenTextClass(depth, total int) string {
	return "tgt-" + strconv.Itoa(traceGreenBucket(depth, total))
}

// wbucket rounds a percentage-ish value to an integer 0..100 for the .w-pct-N
// width utility classes (de-inlined progress / coverage / time bars).
func wbucket(v any) int {
	n := int(toFloat64(v) + 0.5)
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// pctClass / pctClassBg / pctClassInv return the .pct-<band> utility class
// carrying the band color (CSP-safe — the stylesheet's .pct-* rules resolve
// it, instead of an inline var(--color-…)).
func pctClass(v any) string    { return "pct-" + pctBand(v) }
func pctClassBg(v any) string  { return "pct-" + pctBand(v) + "-bg" }
func pctClassInv(v any) string { return "pct-" + pctBandInv(v) }

// ccClassColors is the set of viz colors with a generated .cc-<rrggbb> rule in
// style.css: the swarm palette (which also covers the compliance-donut hues)
// plus the neutral fallback. Built once at init.
var ccClassColors = func() map[string]bool {
	m := make(map[string]bool, len(swarmVizColors)+1)
	for _, h := range swarmVizColors {
		m[strings.ToLower(h)] = true
	}
	m["#a0aec0"] = true
	return m
}()

// ccClass maps a "#rrggbb" viz color to its .cc-<rrggbb> custom-property class
// (which sets --cc-rgb). Paired with the .cc-fg / .cc-bg / .cc-bg-aN consumers
// in style.css. Replaces inline background:/color: hex values in the swarm and
// compliance visualizations. A color without a generated class — e.g. an
// operator `viz.color=#customhex` tag override, which strict CSP can't turn
// into a static class — degrades to the neutral fallback rather than an
// unstyled (invisible) element.
func ccClass(hex string) string {
	h := strings.ToLower(hex)
	if len(h) == 7 && h[0] == '#' && ccClassColors[h] {
		return "cc-" + h[1:]
	}
	return "cc-a0aec0"
}

// waiverFileGroup is a waiver file with its entries, for template rendering.
type waiverFileGroup struct {
	File    string
	Entries []config.EnvoyWaiverTraceEntry
}

// usercrateScope is one usercrates/ folder in the inheritance chain along with
// every auto-applied usercrate it contributed. Mirrors the CLI's per-scope
// grouping but shaped for trace-card rendering — one box per scope.
type usercrateScope struct {
	Scope string   // display label, e.g. "production/web" or "(fleet-wide)"
	Path  string   // header path, e.g. "production/web/usercrates/" or "usercrates/"
	Names []string // usercrate names contributed by this scope
}

// traceUsercrates extracts auto-applied usercrate scopes from inheritance
// levels (those tagged File == "usercrates/"), preserving parent→child order.
// Each scope becomes one trace-card so the section visually mirrors the
// other inheritance sections.
func traceUsercrates(levels []config.InheritanceLevel) []usercrateScope {
	var scopes []usercrateScope
	for _, lvl := range levels {
		if lvl.File != "usercrates/" || len(lvl.Configcrates) == 0 {
			continue
		}
		scope := lvl.Dir
		path := "usercrates/"
		if scope == "" {
			scope = "(fleet-wide)"
		} else {
			path = scope + "/usercrates/"
		}
		scopes = append(scopes, usercrateScope{
			Scope: scope,
			Path:  path,
			Names: append([]string(nil), lvl.Configcrates...),
		})
	}
	return scopes
}

// traceModuleLevels filters out usercrate entries from inheritance levels.
// The dedicated Usercrates section renders those separately; without this
// filter the Configcrate Inheritance cards would show duplicate entries.
func traceModuleLevels(levels []config.InheritanceLevel) []config.InheritanceLevel {
	out := make([]config.InheritanceLevel, 0, len(levels))
	for _, lvl := range levels {
		if lvl.File == "usercrates/" {
			continue
		}
		out = append(out, lvl)
	}
	return out
}

// groupWaiversByFile groups waiver trace entries by source file for rendering.
func groupWaiversByFile(waivers []config.EnvoyWaiverTraceEntry) []waiverFileGroup {
	var groups []waiverFileGroup
	idx := make(map[string]int)
	for _, w := range waivers {
		src := w.SourceFile
		if src == "" {
			src = "compliance.vgo"
		}
		if i, ok := idx[src]; ok {
			groups[i].Entries = append(groups[i].Entries, w)
		} else {
			idx[src] = len(groups)
			groups = append(groups, waiverFileGroup{File: src, Entries: []config.EnvoyWaiverTraceEntry{w}})
		}
	}
	return groups
}

// tracePathTree renders the path inheritance tree as nested HTML with directory
// indentation and proper tree connectors, matching the CLI's renderInheritanceTree.
// Shows "● .live" as root, groups files by directory, includes waiver files.
func tracePathTree(levels []config.InheritanceLevel, waiverFiles []string) template.HTML {
	if len(levels) == 0 {
		return ""
	}

	type dirLevel struct {
		dir      string
		files    []string
		hasChild bool
	}

	// Collect files per directory from inheritance levels.
	dirFiles := make(map[string][]string)
	var dirOrder []string
	dirSeen := make(map[string]bool)
	for _, lvl := range levels {
		if !dirSeen[lvl.Dir] {
			dirSeen[lvl.Dir] = true
			dirOrder = append(dirOrder, lvl.Dir)
		}
		dirFiles[lvl.Dir] = append(dirFiles[lvl.Dir], lvl.File)
	}

	// Add waiver files at their directory level (after common.vgo if present).
	for _, wf := range waiverFiles {
		parts := strings.Split(wf, "/")
		dir := ""
		if len(parts) > 1 {
			dir = strings.Join(parts[:len(parts)-1], "/")
		}
		file := parts[len(parts)-1]
		existing := dirFiles[dir]
		inserted := false
		for i, f := range existing {
			if f == "common.vgo" {
				tail := make([]string, len(existing[i+1:]))
				copy(tail, existing[i+1:])
				dirFiles[dir] = append(existing[:i+1], append([]string{file}, tail...)...)
				inserted = true
				break
			}
		}
		if !inserted {
			dirFiles[dir] = append(existing, file)
		}
		if !dirSeen[dir] {
			dirSeen[dir] = true
			dirOrder = append(dirOrder, dir)
		}
	}

	// Build dirLevels with hasChild flag.
	var dirLevels []dirLevel
	for i, dir := range dirOrder {
		dl := dirLevel{dir: dir, files: dirFiles[dir]}
		if i < len(dirOrder)-1 {
			dl.hasChild = true
		}
		dirLevels = append(dirLevels, dl)
	}

	total := len(dirLevels)
	var b strings.Builder
	indent := func(depth int) string {
		return strings.Repeat("&nbsp;&nbsp;&nbsp;&nbsp;", depth)
	}

	for i, dl := range dirLevels {
		// traceGreenTextClass returns the bucketed .tgt-N class; that class sets
		// the --tc-l/--tc-d custom-property pair the .trace-path-line rule reads,
		// the same indirection the trace cards use (CSP-safe — no inline style).
		tcClass := traceGreenTextClass(i, total)

		depth := 0
		if dl.dir != "" {
			depth = len(strings.Split(dl.dir, "/"))
		}

		// Print directory name (except root which gets ● stacks).
		if dl.dir != "" {
			parts := strings.Split(dl.dir, "/")
			dirName := parts[len(parts)-1]
			parentIndent := indent(depth - 1)
			fmt.Fprintf(&b, `<div class="trace-path-line %s">%s└── %s</div>`, tcClass, parentIndent, template.HTMLEscapeString(dirName))
		}

		// Print root header.
		if i == 0 && dl.dir == "" {
			fmt.Fprintf(&b, `<div class="trace-path-line %s">● .live</div>`, tcClass)
		}

		// Print files at this level.
		ind := indent(depth)
		for j, file := range dl.files {
			isLast := j == len(dl.files)-1 && !dl.hasChild
			connector := "├── "
			if isLast {
				connector = "└── "
			}
			fmt.Fprintf(&b, `<div class="trace-path-line %s">%s%s%s</div>`, tcClass, ind, connector, template.HTMLEscapeString(file))
		}

		// Vertical connector to next level.
		if dl.hasChild {
			fmt.Fprintf(&b, `<div class="trace-path-line %s">%s│</div>`, tcClass, ind)
		}
	}

	return template.HTML(b.String())
}

// markdownRenderer is a singleton goldmark instance with chroma syntax highlighting.
// html.WithUnsafe() intentionally omitted — raw HTML in markdown is sanitized
// to prevent XSS (SEC-14), particularly from AI chat responses.
var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(
			highlighting.WithStyle("monokai"),
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		),
	),
)

// RenderMarkdown converts markdown source to HTML with syntax-highlighted code blocks.
func RenderMarkdown(source string) string {
	var buf bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &buf); err != nil {
		return "<pre>" + template.HTMLEscapeString(source) + "</pre>"
	}
	return buf.String()
}
