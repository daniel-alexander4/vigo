// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/certexpiry"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/traits"
	"git.alexander4.io/vigo/ui"
)

// DataSource implements ui.DataSource backed by a FleetIndex for real-time
// queries and DB for historical data (runs, plans, convergence history).
// envoyRowsCacheTTL bounds how stale the memoized /envoys full-fleet row
// build may be. Kept below the 5s minimum UI poll interval so the page is
// never less fresh than its own poll cadence, while still collapsing the
// stats+table poll fragments (plus multi-tab and scroll load-more) within a
// tick into a single full-fleet build — the build is O(enrolled) and churns
// ~tens of MB of transient rows at large fleets, so doing it once per window
// instead of per request removes avoidable GC load.
const envoyRowsCacheTTL = 2 * time.Second

type DataSource struct {
	fleet       *FleetIndex
	queries     *db.Queries
	runStore    db.RunStore
	configStore *config.ConfigStore

	// envoyRowsCache memoizes the full-fleet envoy row build (SnapshotEnvoys +
	// the per-row Match/EnvoyFrameworks pass) shared across all /envoys
	// consumers. The cached slice is read-only: callers filter into a fresh
	// slice (filterEnvoyRows always copies) and sort that, never the shared
	// rows. Rebuilt under the mutex, so concurrent callers single-flight the
	// build rather than each rebuilding the fleet.
	envoyRowsCache struct {
		mu      sync.Mutex
		rows    []ui.EnvoyRow
		builtAt time.Time
	}

	// expectedAgentVersion resolves the per-(os,arch) agent version the
	// server would auto-update an envoy to, given its reported os.family
	// and os.arch traits. Returns "" when no binary is published for that
	// arch. Wired post-construction from the bootstrap handler (see
	// SetExpectedAgentVersionFunc); nil until then (no expected version,
	// so the version surface reads as "can't verify" → yellow).
	expectedAgentVersion func(family, arch string) string
}

// NewDataSource creates a DataSource backed by the given FleetIndex.
func NewDataSource(fleet *FleetIndex, queries *db.Queries, runStore db.RunStore, configStore *config.ConfigStore) *DataSource {
	return &DataSource{
		fleet:       fleet,
		queries:     queries,
		runStore:    runStore,
		configStore: configStore,
	}
}

// SetExpectedAgentVersionFunc wires the per-(os,arch) expected-agent-version
// resolver (backed by the bootstrap dist-tree version map). Called once at
// gateway construction, after the bootstrap handler exists.
func (ds *DataSource) SetExpectedAgentVersionFunc(f func(family, arch string) string) {
	ds.expectedAgentVersion = f
}

// agentVersionPair returns the envoy's reported agent version
// (column-first, `identity.agent_version`-trait fallback — matching the
// envoys-list precedence) and the expected per-(os,arch) version. Either
// may be ""; the fleetpressure currency rule (EnvoyPressure.AgentVersionCurrent)
// treats an empty value as "can't verify" → yellow.
func (ds *DataSource) agentVersionPair(reported string, traits map[string]string) (current, expected string) {
	current = reported
	if current == "" {
		current = traits["identity.agent_version"]
	}
	if ds.expectedAgentVersion != nil {
		expected = ds.expectedAgentVersion(traits["os.family"], traits["os.arch"])
	}
	return current, expected
}

// envoyRow converts one live FleetIndex EnvoyState into a
// freshness-stamped ui.EnvoyRow. This is the single canonical
// EnvoyState→EnvoyRow mapping — the envoys page, the dashboard
// worst-list, and the envoy detail header all build their rows here so a
// field added to EnvoyRow can't silently go missing on one surface.
// EnvoyDriftLabel returns the envoy's current drift-axis label ("none" /
// "changed" / "diverged") or "" if the envoy isn't in the FleetIndex.
// Cheap O(1) FleetIndex lookup; used by the dashboard recent-runs
// widget to stamp RunRow.EnvoyDriftLabel per row so the convergence
// badge can paint orange when the envoy is currently Diverged.
func (ds *DataSource) EnvoyDriftLabel(envoyID string) string {
	if ts := ds.fleet.GetEnvoy(envoyID); ts != nil {
		return ts.Drift.String()
	}
	return ""
}

func (ds *DataSource) envoyRow(ts *EnvoyState, baseInterval, offlineThreshold time.Duration) ui.EnvoyRow {
	failurePct := convergence.FailurePct(ts.LastRunFailedCount, ts.LastRunResourceCount)
	observedCycle := ts.ObservedCycle()
	offline := ui.EnvoyStale(ts.LastSeen, baseInterval, offlineThreshold, observedCycle)
	// ADR-032 slice 2 axes sourced from EnvoyState — Tags is already a
	// slice; OS/Arch live in the flat-trait map. Roles/Configcrates/
	// Environment/ComplianceFrameworks need a ConfigStore lookup and
	// get stamped in envoyRows (one pass for the whole snapshot).
	var tags []string
	if len(ts.Tags) > 0 {
		tags = append(tags, ts.Tags...)
	}
	return ui.EnvoyRow{
		ID:             ts.ID,
		Hostname:       ts.Hostname,
		IPAddress:      ts.IP,
		EnrolledAt:     ts.EnrolledAt,
		LastSeen:       ts.LastSeen,
		Offline:        offline,
		LastRunAt:      ts.LastRunAt,
		LastRunID:      ts.LastRunID,
		Status:         ts.ConvergenceStatus,
		Drift:          ts.Drift,
		DriftLabel:     ts.Drift.String(),
		FailurePct:     failurePct,
		ResourceCount:  ts.LastRunResourceCount,
		FailedCount:    ts.LastRunFailedCount,
		ChangedCount:   ts.LastRunChangedCount,
		ObserveOnly:    ts.ObserveOnly,
		StaleThreshold: freshness.StaleThresholdFromCycle(baseInterval, observedCycle, offlineThreshold),
		AgentVersion:   ts.AgentVersion,
		Tags:           tags,
		OS:             ts.Traits["os.distro"],
		Arch:           ts.Traits["arch"],
	}
}

// envoyRows builds the full freshness-stamped EnvoyRow set from the live
// FleetIndex snapshot — the shared basis for the envoys page and the
// dashboard worst-list.
//
// After the per-row build, enriches each row with ConfigStore-derived
// ADR-032 slice-2 axes (Environment, Roles, Configcrates, ComplianceFrameworks)
// in one pass so the row is the canonical SoT for every envoys-view chip.
// Uses ConfigStore.Match (shared, read-only) rather than MatchClone to
// keep allocation churn flat at 10k-envoy scale.
func (ds *DataSource) envoyRows(baseInterval, offlineThreshold time.Duration) []ui.EnvoyRow {
	states := ds.fleet.SnapshotEnvoys()
	rows := make([]ui.EnvoyRow, 0, len(states))
	for i := range states {
		rows = append(rows, ds.envoyRow(&states[i], baseInterval, offlineThreshold))
	}
	if ds.configStore == nil {
		return rows
	}
	for i := range rows {
		_, cfg := ds.configStore.Match(rows[i].Hostname)
		if cfg != nil {
			rows[i].Environment = cfg.Environment
			if len(cfg.Roles) > 0 {
				rows[i].Roles = append(rows[i].Roles, cfg.Roles...)
			}
			if len(cfg.Configcrates) > 0 {
				configcrateNames := make([]string, 0, len(cfg.Configcrates))
				for _, m := range cfg.Configcrates {
					configcrateNames = append(configcrateNames, m.Name)
				}
				rows[i].Configcrates = configcrateNames
			}
		}
		rows[i].ComplianceFrameworks = ds.configStore.EnvoyFrameworks(rows[i].Hostname)
	}
	return rows
}

// cachedEnvoyRows returns the memoized full-fleet row build, rebuilding it
// when the cache is older than envoyRowsCacheTTL. The returned slice is
// shared and read-only — callers must not sort or mutate it in place
// (EnvoysData filters into a fresh slice via filterEnvoyRows, then sorts that).
// The build runs under the mutex so simultaneous callers (the stats + table
// poll fragments, multiple tabs, scroll load-more) collapse to one build per
// window instead of a thundering herd of full-fleet rebuilds.
func (ds *DataSource) cachedEnvoyRows(baseInterval, offlineThreshold time.Duration) []ui.EnvoyRow {
	ds.envoyRowsCache.mu.Lock()
	defer ds.envoyRowsCache.mu.Unlock()
	if ds.envoyRowsCache.rows != nil && time.Since(ds.envoyRowsCache.builtAt) < envoyRowsCacheTTL {
		return ds.envoyRowsCache.rows
	}
	rows := ds.envoyRows(baseInterval, offlineThreshold)
	ds.envoyRowsCache.rows = rows
	ds.envoyRowsCache.builtAt = time.Now()
	return rows
}

func (ds *DataSource) DashboardData(baseInterval, offlineThreshold time.Duration) ui.DashboardPageData {
	summary := ds.fleet.ConvergenceSummary()

	driftRate := float64(0)
	if summary.Total > 0 {
		driftRate = float64(summary.Diverged) / float64(summary.Total) * 100
	}
	// Divide by the ACTIVE fleet (total − offline), same as every other
	// convergence-% surface — via the canonical helper (CLAUDE.md single-SoT).
	// This site previously divided by raw Total and was the lone outlier.
	convPct := convergence.ConvergedPct(int64(summary.Converged), int64(summary.Total)-int64(summary.Offline))

	// Fleet table: worst 50 envoys, triage-ordered. Also tally Observing
	// (observe-only) here in the same walk so the dashboard filter-bar
	// has the field without a separate iteration. Shares the /envoys
	// full-fleet row memo (cachedEnvoyRows) — same build, same poll churn.
	// The cache is shared read-only, so copy before the in-place sort.
	all := ds.cachedEnvoyRows(baseInterval, offlineThreshold)
	observing := 0
	for _, e := range all {
		if e.ObserveOnly {
			observing++
		}
	}
	worstRows := append([]ui.EnvoyRow(nil), all...)
	sortByWorstStatus(worstRows)
	capped := len(worstRows) > 50
	worst := worstRows
	if capped {
		worst = worstRows[:50]
	}
	fleetRows := make([]ui.FleetRow, 0, len(worst))
	for _, e := range worst {
		row := ui.FleetRow{
			ID:            e.ID,
			Hostname:      e.Hostname,
			IP:            e.IPAddress,
			LastSeen:      e.LastSeen,
			Status:        e.Status,
			Drift:         e.Drift,
			DriftLabel:    e.Drift.String(),
			LastRunID:     e.LastRunID,
			FailurePct:    e.FailurePct,
			ChangedCount:  e.ChangedCount,
			FailedCount:   e.FailedCount,
			ResourceCount: e.ResourceCount,
		}
		// Agent Health column: route through the same fleetpressure SoT
		// the /envoys table and /agent-health page consume. ok=false
		// (envoy missing from the pressure index) leaves HealthSeverity
		// at the zero value (Unknown), which the template renders as the
		// neutral "—" pill — same shape as the /envoys row.
		if p, ok := ds.EnvoyPressure(e.ID); ok {
			row.HealthSeverity = p.Health
			if p.Health != fleetpressure.Unknown {
				row.HealthSummary = ui.HealthSummary(p)
				pCopy := p
				row.Pressure = &pCopy
			}
		}
		fleetRows = append(fleetRows, row)
	}

	data := ui.DashboardPageData{
		PageData:         ui.PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalEnvoys:      summary.Total,
		ActiveEnvoys:     summary.Total - summary.Offline,
		RecentFailures:   summary.Failed,
		DriftRate:        driftRate,
		ConvergencePct:   convPct,
		Converged:        summary.Converged,
		Degraded:         summary.Degraded,
		Diverged:         summary.Diverged,
		CompFailed:       summary.Failed,
		Offline:          summary.Offline,
		NoData:           summary.NoData,
		Changed:          summary.Changed,
		Observing:        observing,
		OfflineThreshold: freshness.StaleThresholdFromCycle(baseInterval, 0, offlineThreshold),
		FleetRows:        fleetRows,
	}
	data.FleetRowsCapped = capped
	if capped {
		data.FleetRowsRemaining = summary.Total - 50
	}
	return data
}

func (ds *DataSource) ConvergenceData(baseInterval, offlineThreshold time.Duration) ui.ConvergencePageData {
	summary := ds.fleet.ConvergenceSummary()
	total := int64(summary.Total)

	// Stale envoys are excluded from the convergence percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	convPct := convergence.ConvergedPct(int64(summary.Converged), total-int64(summary.Offline))

	// Gather non-converged envoys.
	var notConverged []ui.ConvergenceRow
	allEnvoys := ds.fleet.AllEnvoys()
	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}
		if ts.ConvergenceStatus != "converged" {
			notConverged = append(notConverged, ui.ConvergenceRow{
				ID:            ts.ID,
				Hostname:      ts.Hostname,
				Status:        ts.ConvergenceStatus,
				LastSeen:      ts.LastSeen,
				LastRunStatus: ts.LastRunStatus,
			})
		}
	}

	// Sparkline from FleetIndex ring buffer (in-memory, no DB query).
	// Falls back to DB convergence_history if the ring buffer is empty.
	snapshots := ds.fleet.ConvergenceSparkline()
	var sparkData []float64
	if len(snapshots) > 0 {
		sparkData = make([]float64, 0, len(snapshots))
		for _, s := range snapshots {
			active := s.Counters.Total - s.Counters.Offline
			if active > 0 {
				sparkData = append(sparkData, convergence.ConvergedPct(int64(s.Counters.Converged), int64(active)))
			}
		}
	} else {
		ctx := context.Background()
		history, err := ds.queries.ListConvergenceHistory(ctx, 30)
		if err != nil {
			slog.Error("failed to list convergence history", "error", err)
		}
		sparkData = make([]float64, 0, len(history))
		for i := len(history) - 1; i >= 0; i-- {
			h := history[i]
			active := h.Total - h.Offline
			if active > 0 {
				sparkData = append(sparkData, convergence.ConvergedPct(h.Converged, active))
			}
		}
	}

	return ui.ConvergencePageData{
		ConvergencePct: convPct,
		Total:          total,
		Converged:      int64(summary.Converged),
		Degraded:       int64(summary.Degraded),
		Failed:         int64(summary.Failed),
		NoData:         int64(summary.NoData),
		Changed:        int64(summary.Changed),
		Diverged:       int64(summary.Diverged),
		Offline:        int64(summary.Offline),
		SparklineData:  sparkData,
		NonConverged:   notConverged,
	}
}

func (ds *DataSource) DashboardStatsData(baseInterval, offlineThreshold time.Duration) ui.DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval, offlineThreshold)
	return ui.DashboardStatsFragmentData{
		TotalEnvoys:    d.TotalEnvoys,
		ActiveEnvoys:   d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
		ConvergencePct: d.ConvergencePct,
		Converged:      d.Converged,
		Degraded:       d.Degraded,
		Diverged:       d.Diverged,
		CompFailed:     d.CompFailed,
		Offline:        d.Offline,
	}
}

// EnvoysData builds the envoys-list page data from the live FleetIndex
// snapshot — the single source of truth for both the table rows and the
// filter-pill counts above them. ADR-032 slice 2 added 7 free-text
// chip axes (env, tag, role, os, arch, module, compliance); each
// accepts comma-separated values for OR-within-axis semantics; AND
// across axes (the existing convFilter/driftFilter/reachFilter behavior).
func (ds *DataSource) EnvoysData(baseInterval, offlineThreshold time.Duration, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter, sortCol, sortDir string, offset, limit int) ui.EnvoysPageData {
	all := ds.cachedEnvoyRows(baseInterval, offlineThreshold)

	// Fleet-wide Module Health + observe-only count over the full set,
	// before filtering — these are fleet totals, not page totals.
	var fleetFailed, fleetTotal int64
	var observing int
	for _, e := range all {
		fleetFailed += e.FailedCount
		fleetTotal += e.ResourceCount
		if e.ObserveOnly {
			observing++
		}
	}

	filtered := filterEnvoyRows(all, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter)
	total := len(filtered)
	filterAxes, activeFilterCount := BuildEnvoyFilterAxes(filtered, ds.fleet.ConvergenceSummary(), observing, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter)
	sortEnvoyRows(filtered, sortCol, sortDir)

	if offset < 0 {
		offset = 0
	}
	var page []ui.EnvoyRow
	var hasMore bool
	if offset < total {
		end := total
		if limit > 0 && offset+limit < total {
			end = offset + limit
			hasMore = true
		}
		page = filtered[offset:end]
	}

	counts := ds.fleet.ConvergenceSummary()
	data := ui.EnvoysPageData{
		PageData:          ui.PageData{Title: "Envoys", ActiveNav: "envoys"},
		Envoys:            page,
		EnvFilter:         envFilter,
		TagFilter:         tagFilter,
		RoleFilter:        roleFilter,
		OSFilter:          osFilter,
		ArchFilter:        archFilter,
		ConfigcrateFilter: configcrateFilter,
		ComplianceFilter:  complianceFilter,
		ConvFilter:        convFilter,
		DriftFilter:       driftFilter,
		ReachFilter:       reachFilter,
		Search:            search,
		SortColumn:        sortCol,
		SortDir:           sortDir,
		EnvoyCount:        len(page),
		MatchingTotal:     total,
		HasMore:           hasMore,
		Offset:            offset,
		Limit:             limit,
		NextOffset:        offset + len(page),
		TotalEnvoys:       counts.Total,
		CountConverged:    counts.Converged,
		CountDegraded:     counts.Degraded,
		CountFailed:       counts.Failed,
		CountNoData:       counts.NoData,
		CountChanged:      counts.Changed,
		CountDiverged:     counts.Diverged,
		CountOffline:      counts.Offline,
		CountObserving:    observing,

		FleetFailedConfigcrates: fleetFailed,
		FleetTotalConfigcrates:  fleetTotal,
		FilterAxes:              filterAxes,
		ActiveFilterCount:       activeFilterCount,
		BasePath:                "/envoys",
	}
	data.ActiveEnvoys = data.TotalEnvoys - data.CountOffline
	if data.TotalEnvoys > 0 {
		data.DriftRate = float64(data.CountDiverged) / float64(data.TotalEnvoys) * 100
	}
	if data.FleetTotalConfigcrates > 0 {
		data.ConfigcrateHealthPct = float64(data.FleetFailedConfigcrates) / float64(data.FleetTotalConfigcrates) * 100
	}
	data.OfflineThreshold = freshness.StaleThresholdFromCycle(baseInterval, 0, offlineThreshold)
	return data
}

// filterEnvoyRows returns the subset of rows matching the given axis
// filters. Filter semantics (AND across axes; empty axis = match all):
//   - convFilter: "" / "converged" / "degraded" / "failed" / "no data" (enum, single value)
//   - driftFilter: "" / "changed" / "diverged" (Diverged ⊆ Changed;
//     clicking Changed includes Diverged envoys)
//   - reachFilter: "" / "offline" / "observing"
//
// search and the 7 ADR-032 slice-2 axes (envFilter, tagFilter,
// roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter)
// are case-insensitive substring matches. Each accepts comma-separated
// values for OR-within-axis: `?env=prod,staging` matches either; AND
// holds across axes.
func filterEnvoyRows(rows []ui.EnvoyRow, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter string) []ui.EnvoyRow {
	searchLower := splitCommaLower(search)
	envLower := splitCommaLower(envFilter)
	tagLower := splitCommaLower(tagFilter)
	roleLower := splitCommaLower(roleFilter)
	osLower := splitCommaLower(osFilter)
	archLower := splitCommaLower(archFilter)
	configcrateLower := splitCommaLower(configcrateFilter)
	complianceLower := splitCommaLower(complianceFilter)
	out := make([]ui.EnvoyRow, 0, len(rows))
	for _, e := range rows {
		if matchesEnvoyFilter(e, convFilter, driftFilter, reachFilter, searchLower, envLower, tagLower, roleLower, osLower, archLower, configcrateLower, complianceLower) {
			out = append(out, e)
		}
	}
	return out
}

// splitCommaLower splits an OR-within-axis chip value into trimmed,
// lower-cased substring tokens. Empty/whitespace tokens are dropped so
// `?env=prod,,staging` is equivalent to `?env=prod,staging`.
func splitCommaLower(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// anySubstringMatch returns true if any value in needles is a substring
// of haystack (case-insensitive; haystack pre-lowered by caller). Empty
// needles slice means the axis is not filtering — pass-through true.
func anySubstringMatch(haystackLower string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	for _, n := range needles {
		if strings.Contains(haystackLower, n) {
			return true
		}
	}
	return false
}

// anySubstringMatchInSlice returns true if any needle is a substring of
// any value in haystacks (each value lowered on the fly). Empty needles
// = pass-through; empty haystacks with non-empty needles = no match.
func anySubstringMatchInSlice(haystacks, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	for _, h := range haystacks {
		hl := strings.ToLower(h)
		for _, n := range needles {
			if strings.Contains(hl, n) {
				return true
			}
		}
	}
	return false
}

// envoyFilterAxisMaxPerAxis caps each free-text axis at 50 pills, sorted
// by count desc. Currently-selected values that fall outside the cap are
// always preserved so the operator can toggle them off. The 50 is a
// glance-vs-clutter tradeoff; the axis section also exposes an in-pill
// search input so high-cardinality cases (modules at fleet scale) stay
// navigable past the cap.
const envoyFilterAxisMaxPerAxis = 50

// BuildEnvoyFilterAxes is the envoys-page wrapper around EnumerateFilterAxes:
// declares the 10 axes the drawer renders (env / tag / role / os / arch /
// module / compliance free-text, conv / drift / reach enum), wires each
// axis to the matching EnvoyRow field, and returns the axes + the count
// of axes with at least one active selection (drives the "Filter (N
// active)" button label). Conv/drift/reach counts are sourced from the
// fleet's pre-computed ConvergenceSummary so they match the read-only
// chip bar above the table; "observing" comes from the caller because
// it's not part of ConvergenceCounters.
func BuildEnvoyFilterAxes(rows []ui.EnvoyRow, counts ConvergenceCounters, observing int, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter string) ([]ui.FilterAxis, int) {
	baseQS := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			baseQS[k] = v
		}
	}
	put("conv", convFilter)
	put("drift", driftFilter)
	put("reach", reachFilter)
	put("search", search)
	put("env", envFilter)
	put("tag", tagFilter)
	put("role", roleFilter)
	put("os", osFilter)
	put("arch", archFilter)
	put("configcrate", configcrateFilter)
	put("compliance", complianceFilter)

	specs := []FilterAxisSpec{
		{
			Key:       "conv",
			Label:     "Failure",
			Selected:  convFilter,
			EnumOrder: []string{"converged", "degraded", "no data", "failed"},
			EnumCount: map[string]int{
				"converged": counts.Converged,
				"degraded":  counts.Degraded,
				"no data":   counts.NoData,
				"failed":    counts.Failed,
			},
		},
		{
			Key:       "drift",
			Label:     "Drift",
			Selected:  driftFilter,
			EnumOrder: []string{"changed", "diverged"},
			EnumCount: map[string]int{
				"changed":  counts.Changed,
				"diverged": counts.Diverged,
			},
		},
		{
			Key:       "reach",
			Label:     "Reach",
			Selected:  reachFilter,
			EnumOrder: []string{"offline", "observing"},
			EnumCount: map[string]int{
				"offline":   counts.Offline,
				"observing": observing,
			},
		},
		{Key: "env", Label: "Env", Selected: envFilter, Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.Environment) }},
		{Key: "tag", Label: "Tag", Selected: tagFilter, Extract: func(r ui.EnvoyRow) []string { return r.Tags }},
		{Key: "role", Label: "Role", Selected: roleFilter, Extract: func(r ui.EnvoyRow) []string { return r.Roles }},
		{Key: "os", Label: "OS", Selected: osFilter, Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.OS) }},
		{Key: "arch", Label: "Arch", Selected: archFilter, Extract: func(r ui.EnvoyRow) []string { return strOrNil(r.Arch) }},
		{Key: "configcrate", Label: "Configcrate", Selected: configcrateFilter, Extract: func(r ui.EnvoyRow) []string { return r.Configcrates }},
		{Key: "compliance", Label: "Compliance", Selected: complianceFilter, Extract: func(r ui.EnvoyRow) []string { return r.ComplianceFrameworks }},
	}

	axes := EnumerateFilterAxes(baseQS, rows, specs, envoyFilterAxisMaxPerAxis)

	active := 0
	for _, s := range specs {
		if s.Selected != "" {
			active++
		}
	}
	if search != "" {
		active++
	}
	return axes, active
}

// strOrNil returns []string{v} when v is non-empty, nil otherwise — lets
// the per-row scalar fields (Environment, OS, Arch) reuse the same
// Extract func shape that slice fields use without a separate code path.
func strOrNil(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

// FilterAxisSpec defines one axis fed into EnumerateFilterAxes. Free-text
// axes (Extract != nil) are multi-select (toggle within a CSV value);
// enum axes (EnumOrder != nil) are single-select (set or clear).
type FilterAxisSpec struct {
	Key       string                     // query string param ("env", "conv", etc.)
	Label     string                     // pill section heading
	Selected  string                     // current CSV value from the query string (raw)
	Extract   func(ui.EnvoyRow) []string // free-text axes: per-row values
	EnumOrder []string                   // enum axes: fixed pill order
	EnumCount map[string]int             // enum axes: pre-computed counts per enum value
}

// EnumerateFilterAxes builds the per-axis pill data for the envoy filter
// drawer. Free-text axis pills are enumerated from rows (the filtered
// set, so pills narrow as filters are applied) and capped at maxPerAxis
// by count desc — but currently-selected values are always retained so
// the user can always toggle them off. Each choice's URL is a path-
// relative query string ("?env=prod"); the template prepends the page's
// base path so /envoys and /envoys/tree can share the same axes data.
// Unrelated query parameters in baseQS are preserved verbatim.
func EnumerateFilterAxes(baseQS map[string]string, rows []ui.EnvoyRow, specs []FilterAxisSpec, maxPerAxis int) []ui.FilterAxis {
	axes := make([]ui.FilterAxis, 0, len(specs))
	for _, spec := range specs {
		isEnum := spec.EnumOrder != nil
		selectedSet := map[string]bool{}
		for _, v := range splitCommaLower(spec.Selected) {
			selectedSet[v] = true
		}

		type kv struct {
			Value string
			Count int
		}
		var ranked []kv
		if isEnum {
			ranked = make([]kv, 0, len(spec.EnumOrder))
			for _, v := range spec.EnumOrder {
				ranked = append(ranked, kv{Value: v, Count: spec.EnumCount[v]})
			}
		} else {
			// Bucket values case-insensitively, keep the first casing seen
			// so pills render the way operator data actually spells them.
			counts := make(map[string]int)
			display := make(map[string]string)
			for v := range selectedSet {
				display[v] = v
				counts[v] = 0
			}
			for _, r := range rows {
				for _, v := range spec.Extract(r) {
					if v == "" {
						continue
					}
					k := strings.ToLower(v)
					if _, seen := display[k]; !seen {
						display[k] = v
					}
					counts[k]++
				}
			}
			ranked = make([]kv, 0, len(counts))
			for k, c := range counts {
				ranked = append(ranked, kv{Value: display[k], Count: c})
			}
			sort.Slice(ranked, func(i, j int) bool {
				if ranked[i].Count != ranked[j].Count {
					return ranked[i].Count > ranked[j].Count
				}
				return ranked[i].Value < ranked[j].Value
			})
			if maxPerAxis > 0 && len(ranked) > maxPerAxis {
				kept := ranked[:maxPerAxis]
				for _, r := range ranked[maxPerAxis:] {
					if selectedSet[strings.ToLower(r.Value)] {
						kept = append(kept, r)
					}
				}
				ranked = kept
			}
		}

		choices := make([]ui.FilterChoice, 0, len(ranked))
		for _, r := range ranked {
			selected := selectedSet[strings.ToLower(r.Value)]
			choices = append(choices, ui.FilterChoice{
				Value:    r.Value,
				Count:    r.Count,
				Selected: selected,
				URL:      buildFilterURL(baseQS, spec.Key, isEnum, spec.Selected, r.Value, selected),
			})
		}
		axes = append(axes, ui.FilterAxis{
			Key:     spec.Key,
			Label:   spec.Label,
			IsEnum:  isEnum,
			Choices: choices,
		})
	}
	return axes
}

// buildFilterURL composes the path-relative toggle query string for
// clicking a pill (e.g. "?env=prod,staging"). Multi-select axes add/
// remove pillValue within the existing CSV; single-select enum axes set
// or clear. Returns "" when toggling clears every filter (so the
// template can fall through to the page's base path). Unrelated keys in
// baseQS are preserved. Commas in axis values stay literal — the OR-
// within-axis convention depends on them being legible in the URL.
func buildFilterURL(baseQS map[string]string, key string, isEnum bool, currentValue, pillValue string, isSelected bool) string {
	q := make(map[string]string, len(baseQS))
	for k, v := range baseQS {
		if v != "" && k != key {
			q[k] = v
		}
	}
	if isEnum {
		if !isSelected {
			q[key] = pillValue
		}
	} else {
		current := splitCommaLower(currentValue)
		pillLower := strings.ToLower(pillValue)
		next := make([]string, 0, len(current)+1)
		found := false
		for _, v := range current {
			if v == pillLower {
				found = true
				continue
			}
			next = append(next, v)
		}
		if !found {
			next = append(next, pillLower)
		}
		if len(next) > 0 {
			q[key] = strings.Join(next, ",")
		}
	}
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sep := "?"
	for _, k := range keys {
		sb.WriteString(sep)
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(urlQueryEscapeKeepingCommas(q[k]))
		sep = "&"
	}
	return sb.String()
}

// urlQueryEscapeKeepingCommas percent-encodes everything net/url's
// QueryEscape would, except commas — the OR-within-axis convention
// depends on them being legible in the URL.
func urlQueryEscapeKeepingCommas(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == ',':
			sb.WriteByte(c)
		case c == ' ':
			sb.WriteByte('+')
		default:
			const hex = "0123456789ABCDEF"
			sb.WriteByte('%')
			sb.WriteByte(hex[c>>4])
			sb.WriteByte(hex[c&0x0F])
		}
	}
	return sb.String()
}

func matchesEnvoyFilter(e ui.EnvoyRow, convFilter, driftFilter, reachFilter string, searchLower, envLower, tagLower, roleLower, osLower, archLower, configcrateLower, complianceLower []string) bool {
	if convFilter != "" && e.Status != convFilter {
		return false
	}
	switch driftFilter {
	case "":
		// no drift filter
	case "changed":
		// Diverged ⊆ Changed; both buckets satisfy the Changed filter.
		if e.Drift != convergence.DriftChanged && e.Drift != convergence.DriftDiverged {
			return false
		}
	case "diverged":
		if e.Drift != convergence.DriftDiverged {
			return false
		}
	default:
		// unknown drift filter — fall through, match nothing safely
		return false
	}
	switch reachFilter {
	case "":
		// no reach filter
	case "offline":
		if !e.Offline {
			return false
		}
	case "observing":
		if !e.ObserveOnly {
			return false
		}
	}
	if !anySubstringMatch(strings.ToLower(e.Hostname), searchLower) {
		return false
	}
	if !anySubstringMatch(strings.ToLower(e.Environment), envLower) {
		return false
	}
	if !anySubstringMatchInSlice(e.Tags, tagLower) {
		return false
	}
	if !anySubstringMatchInSlice(e.Roles, roleLower) {
		return false
	}
	if !anySubstringMatch(strings.ToLower(e.OS), osLower) {
		return false
	}
	if !anySubstringMatch(strings.ToLower(e.Arch), archLower) {
		return false
	}
	if !anySubstringMatchInSlice(e.Configcrates, configcrateLower) {
		return false
	}
	if !anySubstringMatchInSlice(e.ComplianceFrameworks, complianceLower) {
		return false
	}
	return true
}

// statusPriority returns a sort key: lower = worse. Sorts on the
// failure axis only — drift is reflected in the dedicated drift pills,
// not in row sort order. Diverged envoys with zero failures still sort
// near the bottom because they're functionally healthy from the
// "is anything broken right now" perspective.
func statusPriority(s string) int {
	switch convergence.ClassifyStatus(s) {
	case convergence.StatusFailed:
		return 0
	case convergence.StatusDegraded:
		return 1
	case convergence.StatusNoData:
		return 2
	case convergence.StatusConverged:
		return 3
	default:
		return 4
	}
}

func sortByWorstStatus(rows []ui.EnvoyRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return worstStatusLess(rows[i], rows[j])
	})
}

// worstStatusLess orders rows by triage priority: worst convergence
// status first, then highest failure percentage, then hostname for
// stable tiebreaking. Used both by the dashboard worst-list and by the
// envoys list when the operator picks "Worst first" — the same
// comparator keeps the two surfaces consistent.
func worstStatusLess(a, b ui.EnvoyRow) bool {
	pa, pb := statusPriority(a.Status), statusPriority(b.Status)
	if pa != pb {
		return pa < pb
	}
	if a.FailurePct != b.FailurePct {
		return a.FailurePct > b.FailurePct
	}
	return a.Hostname < b.Hostname
}

// sortEnvoyRows reorders rows in place per the operator's sort choice.
// The "status" column means triage-first when ascending — that is the
// default landing experience for /envoys at scale, where the
// alphabetically-first envoy is rarely the one the operator came to
// fix. "last_run" defaults to newest-first when descending; "hostname"
// / "ip" / "last_seen" follow the obvious lexicographic / temporal
// order.
func sortEnvoyRows(rows []ui.EnvoyRow, col, dir string) {
	switch col {
	case "", "status":
		sortByWorstStatus(rows)
		if dir == "desc" {
			reverseEnvoyRows(rows)
		}
	case "hostname":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Hostname < rows[j].Hostname })
		if dir == "desc" {
			reverseEnvoyRows(rows)
		}
	case "ip":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].IPAddress < rows[j].IPAddress })
		if dir == "desc" {
			reverseEnvoyRows(rows)
		}
	case "last_seen":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeen.Before(rows[j].LastSeen) })
		if dir != "asc" {
			reverseEnvoyRows(rows)
		}
	case "last_run":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastRunAt.Before(rows[j].LastRunAt) })
		if dir != "asc" {
			reverseEnvoyRows(rows)
		}
	default:
		sortByWorstStatus(rows)
	}
}

func reverseEnvoyRows(rows []ui.EnvoyRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func (ds *DataSource) EnvoyDetailData(envoyID string, baseInterval, offlineThreshold time.Duration) (ui.EnvoyDetailPageData, error) {
	// FleetIndex for header (shell only — runs and facts loaded via fragments).
	ts := ds.fleet.GetEnvoy(envoyID)
	if ts == nil {
		ctx := context.Background()
		envoy, err := ds.queries.GetEnvoyByID(ctx, envoyID)
		if err != nil {
			return ui.EnvoyDetailPageData{}, err
		}
		lastSeen := time.Time{}
		if envoy.LastSeen.Valid {
			lastSeen = envoy.LastSeen.Time
		}
		var recent []convergence.RunInfo
		runs, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{EnvoyID: envoyID, Limit: 1})
		if err == nil && len(runs) > 0 {
			recent = []convergence.RunInfo{{Status: runs[0].Status, ChangedCount: runs[0].ChangedCount, FailedCount: runs[0].FailedCount, ResourceCount: runs[0].ResourceCount}}
		}
		convStatus := convergence.ComputeConvergence(recent)
		ts = &EnvoyState{
			ID:                envoy.ID,
			Hostname:          envoy.Hostname,
			IP:                envoy.IpAddress,
			EnrolledAt:        envoy.EnrolledAt,
			LastSeen:          lastSeen,
			ConvergenceStatus: convStatus,
		}
		// Carry the last run's counts onto the synthetic state so the
		// detail header pill renders the real convergence ratio rather
		// than "no run data" for an envoy that has dropped out of the
		// in-memory FleetIndex (revoked, or pre-rehydration). Drift stays
		// None — it needs run history this single-run fetch doesn't have.
		if len(runs) > 0 {
			ts.LastRunID = runs[0].ID
			ts.LastRunAt = runs[0].StartedAt
			ts.LastRunResourceCount = runs[0].ResourceCount
			ts.LastRunChangedCount = runs[0].ChangedCount
			ts.LastRunFailedCount = runs[0].FailedCount
		}
	}

	return ui.EnvoyDetailPageData{
		PageData:   ui.PageData{Title: "Envoy: " + ts.Hostname, ActiveNav: "envoys"},
		Envoy:      ds.envoyRow(ts, baseInterval, offlineThreshold),
		UptimeSecs: fleetpressure.UptimeMetric(ts.healthTraits()),
	}, nil
}

func (ds *DataSource) EnvoyRunsData(envoyID string) (ui.EnvoyRunsFragmentData, error) {
	ts := ds.fleet.GetEnvoy(envoyID)
	hostname := envoyID
	driftLabel := ""
	if ts != nil {
		hostname = ts.Hostname
		driftLabel = ts.Drift.String()
	}

	ctx := context.Background()
	dbRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: envoyID,
		Limit:   20,
	})
	if err != nil {
		slog.Error("failed to list runs for envoy", "error", err)
		return ui.EnvoyRunsFragmentData{}, nil
	}

	// Batch-fetch all run results in one query.
	runIDs := make([]string, len(dbRuns))
	for i, r := range dbRuns {
		runIDs[i] = r.ID
	}
	allResults, err := ds.queries.ListRunResultsByRunIDs(ctx, runIDs)
	if err != nil {
		slog.Error("failed to batch-fetch run results", "error", err)
	}

	resultsByRun := make(map[string][]ui.RunResultRow, len(dbRuns))
	for _, res := range allResults {
		errMsg := ""
		if res.ErrorMessage.Valid {
			errMsg = res.ErrorMessage.String
		}
		resultsByRun[res.RunID] = append(resultsByRun[res.RunID], ui.RunResultRow{
			ConfigcrateName: res.ConfigcrateName,
			ResourceType:    res.ResourceType,
			ResourceName:    res.ResourceName,
			ActionTaken:     res.ActionTaken,
			Changed:         res.Changed != 0,
			ErrorMessage:    errMsg,
			DurationMs:      res.DurationMs,
		})
	}

	var runs []ui.EnvoyDetailRunRow
	for _, r := range dbRuns {
		runs = append(runs, ui.EnvoyDetailRunRow{
			RunRow: ui.RunRow{
				ID:            r.ID,
				EnvoyID:       envoyID,
				Hostname:      hostname,
				StartedAt:     r.StartedAt,
				Status:        r.Status,
				ResourceCount: r.ResourceCount,
				ChangedCount:  r.ChangedCount,
				FailedCount:   r.FailedCount,
				Outcome: convergence.ComputeStatus(convergence.RunInfo{
					Status:        r.Status,
					ChangedCount:  r.ChangedCount,
					FailedCount:   r.FailedCount,
					ResourceCount: r.ResourceCount,
				}).String(),
				EnvoyDriftLabel: driftLabel,
			},
			Results: resultsByRun[r.ID],
		})
	}

	return ui.EnvoyRunsFragmentData{Runs: runs}, nil
}

func (ds *DataSource) EnvoyTraitsData(envoyID string) (ui.EnvoyTraitsFragmentData, error) {
	ctx := context.Background()
	cf, err := ds.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		return ui.EnvoyTraitsFragmentData{}, nil
	}
	return ui.BuildTraitsFragment(cf.Traits, cf.CollectedAt), nil
}

func (ds *DataSource) RunsData(statusFilter string) ui.RunsPageData {
	ctx := context.Background()
	allRuns, err := ds.queries.ListAllRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return ui.RunsPageData{PageData: ui.PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	// Look up hostnames individually from FleetIndex (O(1) per run) instead
	// of copying the entire envoys map.
	var rows []ui.RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		hostname := r.EnvoyID
		driftLabel := ""
		if ts := ds.fleet.GetEnvoy(r.EnvoyID); ts != nil {
			hostname = ts.Hostname
			driftLabel = ts.Drift.String()
		}
		rows = append(rows, ui.RunRow{
			ID:            r.ID,
			EnvoyID:       r.EnvoyID,
			Hostname:      hostname,
			StartedAt:     r.StartedAt,
			Status:        r.Status,
			ResourceCount: r.ResourceCount,
			ChangedCount:  r.ChangedCount,
			FailedCount:   r.FailedCount,
			FailurePct:    convergence.FailurePct(r.FailedCount, r.ResourceCount),
			Outcome: convergence.ComputeStatus(convergence.RunInfo{
				Status:        r.Status,
				ChangedCount:  r.ChangedCount,
				FailedCount:   r.FailedCount,
				ResourceCount: r.ResourceCount,
			}).String(),
			EnvoyDriftLabel: driftLabel,
		})
	}

	return ui.RunsPageData{
		PageData:     ui.PageData{Title: "Runs", ActiveNav: "runs"},
		Runs:         rows,
		StatusFilter: statusFilter,
		RunCount:     len(rows),
	}
}

func (ds *DataSource) RunDetailData(runID string) (ui.RunDetailPageData, error) {
	ctx := context.Background()
	r, err := ds.queries.GetRunByID(ctx, runID)
	if err != nil {
		return ui.RunDetailPageData{}, err
	}

	// Get hostname from FleetIndex.
	hostname := r.EnvoyID
	envoyID := r.EnvoyID
	driftLabel := ""
	ts := ds.fleet.GetEnvoy(r.EnvoyID)
	if ts != nil {
		hostname = ts.Hostname
		driftLabel = ts.Drift.String()
	} else {
		envoy, err := ds.queries.GetEnvoyByID(ctx, r.EnvoyID)
		if err == nil {
			hostname = envoy.Hostname
		}
	}

	dbResults, err := ds.queries.ListRunResults(ctx, runID)
	if err != nil {
		return ui.RunDetailPageData{}, err
	}

	var results []ui.RunResultRow
	for _, res := range dbResults {
		errMsg := ""
		if res.ErrorMessage.Valid {
			errMsg = res.ErrorMessage.String
		}
		results = append(results, ui.RunResultRow{
			ConfigcrateName: res.ConfigcrateName,
			ResourceType:    res.ResourceType,
			ResourceName:    res.ResourceName,
			ActionTaken:     res.ActionTaken,
			Changed:         res.Changed != 0,
			ErrorMessage:    errMsg,
			DurationMs:      res.DurationMs,
		})
	}

	return ui.RunDetailPageData{
		PageData: ui.PageData{Title: "Run Detail", ActiveNav: "runs"},
		EnvoyID:  envoyID,
		Run: ui.RunRow{
			ID:              r.ID,
			EnvoyID:         envoyID,
			Hostname:        hostname,
			StartedAt:       r.StartedAt,
			Status:          r.Status,
			Trigger:         r.Trigger,
			ResourceCount:   r.ResourceCount,
			ChangedCount:    r.ChangedCount,
			FailedCount:     r.FailedCount,
			EnvoyDriftLabel: driftLabel,
		},
		Results: results,
	}, nil
}

func (ds *DataSource) ConfigData(hostname string) (ui.ConfigPageData, error) {
	if ds.configStore == nil {
		return ui.ConfigPageData{}, nil
	}

	yamlStr, err := ds.configStore.EffectiveConfigYAML(hostname)
	if err != nil {
		return ui.ConfigPageData{}, err
	}

	return ui.ConfigPageData{
		PageData: ui.PageData{Title: "Config: " + hostname, ActiveNav: "envoys"},
		Hostname: hostname,
		RawYAML:  yamlStr,
	}, nil
}

func (ds *DataSource) PlanDetailData(planID string) (ui.PlanDetailPageData, error) {
	ctx := context.Background()
	p, err := ds.queries.GetPlanByID(ctx, planID)
	if err != nil {
		return ui.PlanDetailPageData{}, err
	}

	// Get hostname from FleetIndex or DB.
	hostname := p.EnvoyID
	ts := ds.fleet.GetEnvoy(p.EnvoyID)
	if ts != nil {
		hostname = ts.Hostname
	} else {
		envoy, err := ds.queries.GetEnvoyByID(ctx, p.EnvoyID)
		if err == nil {
			hostname = envoy.Hostname
		}
	}

	dbResults, err := ds.queries.ListPlanResults(ctx, planID)
	if err != nil {
		return ui.PlanDetailPageData{}, err
	}

	var results []ui.PlanResultRow
	createCount, updateCount, deleteCount, skipCount := 0, 0, 0, 0
	for _, r := range dbResults {
		color := planActionColor(r.PlannedAction)
		results = append(results, ui.PlanResultRow{
			ConfigcrateName: r.ConfigcrateName,
			ResourceType:    r.ResourceType,
			ResourceName:    r.ResourceName,
			PlannedAction:   r.PlannedAction,
			ActionColor:     color,
		})
		switch convergence.ClassifyAction(r.PlannedAction) {
		case convergence.ActionCreate:
			createCount++
		case convergence.ActionUpdate:
			updateCount++
		case convergence.ActionDelete:
			deleteCount++
		default:
			skipCount++
		}
	}

	return ui.PlanDetailPageData{
		PageData:    ui.PageData{Title: "Plan Detail", ActiveNav: "envoys"},
		PlanID:      p.ID,
		EnvoyID:     p.EnvoyID,
		Hostname:    hostname,
		CreatedAt:   p.CreatedAt,
		Results:     results,
		TotalCount:  len(results),
		CreateCount: createCount,
		UpdateCount: updateCount,
		DeleteCount: deleteCount,
		SkipCount:   skipCount,
	}, nil
}

func planActionColor(action string) string {
	switch convergence.ClassifyAction(action) {
	case convergence.ActionCreate:
		return "success"
	case convergence.ActionUpdate:
		return "info"
	case convergence.ActionDelete:
		return "danger"
	default:
		return "muted"
	}
}

// SecurityScanData aggregates security scan traits across all envoys.
func (ds *DataSource) SecurityScanData() ui.SecurityScanPageData {
	allEnvoys := ds.fleet.AllEnvoys()
	var data ui.SecurityScanPageData
	data.TotalHosts = int64(len(allEnvoys))

	for _, ts := range allEnvoys {
		if ts.Revoked || ts.Traits == nil {
			continue
		}

		hasScan := false
		row := ui.SecurityHostRow{ID: ts.ID, Hostname: ts.Hostname}

		// Trivy ran iff it reported a summary (`security_scan.trivy.total`
		// is always present then, even at zero findings). Per-host CVE
		// counts shown to operators are the cross-scanner deduped `unique_*`
		// set below — Trivy's raw per-package match counts are surfaced
		// only in the per-host drawer, not aggregated here.
		if _, ok := ts.Traits[traits.TrivyTotal]; ok {
			hasScan = true
		}
		if v, ok := ts.Traits[traits.LynisScore]; ok {
			hasScan = true
			row.LynisScore = parseInt64(v)
			data.LynisAvgScore += row.LynisScore
		}
		if v, ok := ts.Traits[traits.LynisWarnings]; ok {
			row.LynisWarnings = parseInt64(v)
			data.LynisWarnings += row.LynisWarnings
		}
		if v, ok := ts.Traits[traits.LynisSuggestions]; ok {
			row.LynisSuggestions = parseInt64(v)
		}

		// SCAP / STIG compliance scan — fleet counts only (scanned, clean,
		// high-severity failures). No averaged score: cross-profile averages
		// are meaningless. The scan ran iff it reported a profile.
		if _, ok := ts.Traits[traits.ScapProfile]; ok {
			hasScan = true
			data.ScapScanned++
			if ts.Traits[traits.ScapClean] == "true" {
				data.ScapClean++
			}
			if parseInt64(ts.Traits[traits.ScapHighFail]) > 0 {
				data.ScapHighFail++
			}
		}

		chkClean := ts.Traits[traits.ChkrootkitClean] != "false"
		rkhClean := ts.Traits[traits.RkhunterClean] != "false"
		if _, ok := ts.Traits[traits.ChkrootkitClean]; ok {
			hasScan = true
			row.ChkrootkitInfected = parseInt64(ts.Traits[traits.ChkrootkitInfected])
		}
		if _, ok := ts.Traits[traits.RkhunterClean]; ok {
			hasScan = true
			row.RkhunterWarnings = parseInt64(ts.Traits[traits.RkhunterWarnings])
		}
		row.RootkitClean = chkClean && rkhClean
		if row.RootkitClean {
			data.RootkitClean++
		} else {
			data.RootkitWarning++
		}

		clamClean := ts.Traits[traits.ClamavClean] != "false"
		if _, ok := ts.Traits[traits.ClamavClean]; ok {
			hasScan = true
			row.ClamavInfected = parseInt64(ts.Traits[traits.ClamavInfected])
		}
		row.ClamavClean = clamClean
		if clamClean {
			data.ClamavClean++
		} else {
			data.ClamavInfected++
		}

		aideClean := ts.Traits[traits.AideClean] != "false"
		if _, ok := ts.Traits[traits.AideClean]; ok {
			hasScan = true
			row.AideAdded = parseInt64(ts.Traits[traits.AideAdded])
			row.AideRemoved = parseInt64(ts.Traits[traits.AideRemoved])
			row.AideChanged = parseInt64(ts.Traits[traits.AideChanged])
		}
		row.AideClean = aideClean
		if aideClean {
			data.AideClean++
		} else {
			data.AideChanged++
		}

		if v, ok := ts.Traits[traits.TrivyLastScan]; ok {
			row.LastScan = v
		}

		// Per-host unique CVE counts from agent-authoritative traits.
		row.UniqueCritical = parseInt64(ts.Traits[traits.UniqueCritical])
		row.UniqueHigh = parseInt64(ts.Traits[traits.UniqueHigh])
		row.UniqueMedium = parseInt64(ts.Traits[traits.UniqueMedium])
		row.UniqueLow = parseInt64(ts.Traits[traits.UniqueLow])
		if row.UniqueCritical > 0 || row.UniqueHigh > 0 || row.UniqueMedium > 0 || row.UniqueLow > 0 {
			hasScan = true
		}

		// Collect per-host CVEs from the unified cross-scanner list.
		row.CVEs = collectCVEEntries(ts.Traits)

		// Truncation flag: sum of agent-authoritative per-severity counts
		// (computed from the FULL deduped set) versus the length of the
		// truncated cves list that actually rode the wire. Mismatch means
		// the agent's MAX_CVES cap dropped entries — fleet-wide dedup
		// can't see those, so the fleet UniqueCVEs stat may undercount.
		uniqueSum := row.UniqueCritical + row.UniqueHigh + row.UniqueMedium + row.UniqueLow
		if uniqueSum > int64(len(row.CVEs)) {
			row.Truncated = true
			data.AnyTruncated = true
		}

		if hasScan {
			data.ScannedHosts++
			data.PerHost = append(data.PerHost, row)
		}
	}

	if data.ScannedHosts > 0 {
		data.LynisAvgScore = data.LynisAvgScore / data.ScannedHosts
	}

	// Deduplicate fleet-wide CVEs by CVE ID. When the same CVE is reported
	// by multiple hosts with different severities (typical when one host's
	// agent saw it from both trivy + debsecan and merged to the higher
	// rank, while another host's agent only saw it from one scanner), the
	// fleet bucket takes the highest severity across all reporting hosts.
	// Two-pass: first build a map of CVE-ID → (max-rank, original string,
	// packages); then emit one CVEEntry per ID with the max-severity
	// reporter's original casing preserved.
	type cveAgg struct {
		level    cve.SeverityLevel
		severity string // original casing from the host that reported the max
		packages int64  // packages from the max-severity reporter
	}
	cveSet := make(map[string]*cveAgg)
	for _, row := range data.PerHost {
		for _, entry := range row.CVEs {
			lvl := cve.Classify(entry.Severity)
			cur, ok := cveSet[entry.CVEID]
			if !ok || lvl > cur.level {
				cveSet[entry.CVEID] = &cveAgg{level: lvl, severity: entry.Severity, packages: entry.Packages}
			}
		}
	}
	for cveID, agg := range cveSet {
		data.CVEs = append(data.CVEs, ui.CVEEntry{CVEID: cveID, Severity: agg.severity, Packages: agg.packages})
		switch agg.level {
		case cve.SevCritical:
			data.UniqueCritical++
		case cve.SevHigh:
			data.UniqueHigh++
		case cve.SevMedium:
			data.UniqueMedium++
		case cve.SevLow:
			data.UniqueLow++
		}
	}
	data.UniqueCVEs = int64(len(cveSet))

	return data
}

// SecuritySummary composes this bolt's Security Posture column data into a
// federatable rollup (ADR-037 slice 2): the deduped unique-CVE set (from
// SecurityScanData's fleet-wide dedup) plus the additive cert-expiry,
// running-CVE, and rootkit counts. In spanner mode the viewer fans this
// out to peer bolts and unions the CVE sets; standalone it is just this
// fleet's posture. Three fleet walks (security/cert/running-CVE) — same
// work the local dashboard already does, bundled; off the hot path.
func (ds *DataSource) SecuritySummary() ui.SecuritySummary {
	sec := ds.SecurityScanData()
	cert := ds.CertExpirySummary()
	run := ds.RunningCVESummary()

	cves := make([]ui.CVEIdent, 0, len(sec.CVEs))
	for _, c := range sec.CVEs {
		cves = append(cves, ui.CVEIdent{ID: c.CVEID, Severity: c.Severity})
	}
	return ui.SecuritySummary{
		UniqueCVEs:              cves,
		CertsExpiringSoon:       cert.ExpiringSoon,
		CertsExpired:            cert.Expired,
		CertsExpiringIn7:        cert.ExpiringIn7,
		CertsExpiringIn30:       cert.ExpiringIn30,
		CertHostsAffected:       cert.HostsAffected,
		RootkitWarnings:         int(sec.RootkitWarning),
		RunningCVEsTotal:        run.Total,
		RunningCVEsCritical:     run.Critical,
		RunningCVEsHigh:         run.High,
		RunningCVEsMedium:       run.Medium,
		RunningCVEsLow:          run.Low,
		RunningCVEHostsAffected: run.HostsAffected,
	}
}

// collectCVEEntries extracts structured CVE entries from the unified
// security_scan.cves.N.{id,severity,packages} flattened traits.
func collectCVEEntries(traitMap map[string]string) []ui.CVEEntry {
	type partial struct {
		id       string
		severity string
		packages int64
	}
	byIndex := make(map[string]*partial)

	for k, v := range traitMap {
		if !strings.HasPrefix(k, traits.PrefixCVEs) {
			continue
		}
		rest := k[len(traits.PrefixCVEs):]
		idx, field, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		p := byIndex[idx]
		if p == nil {
			p = &partial{}
			byIndex[idx] = p
		}
		switch field {
		case traits.CVEFieldID:
			p.id = v
		case traits.CVEFieldSeverity:
			p.severity = v
		case traits.CVEFieldPackages:
			p.packages = parseInt64(v)
		}
	}

	entries := make([]ui.CVEEntry, 0, len(byIndex))
	for _, p := range byIndex {
		if p.id != "" {
			entries = append(entries, ui.CVEEntry{
				CVEID:    p.id,
				Severity: p.severity,
				Packages: p.packages,
			})
		}
	}
	return entries
}

// RiskData computes fleet-wide risk scores from the in-memory fleet index.
func (ds *DataSource) RiskData(baseInterval, offlineThreshold time.Duration) ui.RiskSummaryData {
	allEnvoys := ds.fleet.AllEnvoys()
	inputs := make([]risk.EnvoyInput, len(allEnvoys))
	for i, es := range allEnvoys {
		inputs[i] = risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     ds.fleet.EnvoyCycle(es.ID),
		}
	}
	fr := risk.ScoreFleet(inputs, baseInterval, offlineThreshold, 5)
	var top []ui.RiskEnvoyRow
	for _, er := range fr.TopRisks {
		top = append(top, ui.RiskEnvoyRow{
			EnvoyID:  er.EnvoyID,
			Hostname: er.Hostname,
			Score:    er.Score,
			Level:    string(er.Level),
		})
	}
	// Load 30-day risk sparkline from DB.
	var sparkData []float64
	var sparkMin, sparkMax int
	if ds.queries != nil {
		history, err := ds.queries.ListRiskPostureHistory(context.Background(), 30)
		if err == nil && len(history) > 0 {
			sparkData = make([]float64, 0, len(history))
			sparkMin = int(history[0].AvgScore)
			sparkMax = int(history[0].AvgScore)
			for i := len(history) - 1; i >= 0; i-- {
				v := int(history[i].AvgScore)
				if v < sparkMin {
					sparkMin = v
				}
				if v > sparkMax {
					sparkMax = v
				}
				sparkData = append(sparkData, float64(v))
			}
		}
	}

	return ui.RiskSummaryData{
		AvgScore:      int(fr.AvgScore),
		Critical:      fr.Distribution[risk.LevelCritical],
		High:          fr.Distribution[risk.LevelHigh],
		Medium:        fr.Distribution[risk.LevelMedium],
		Low:           fr.Distribution[risk.LevelLow],
		TopRisks:      top,
		SparklineData: sparkData,
		SparklineMin:  sparkMin,
		SparklineMax:  sparkMax,
	}
}

// RiskSummary composes this bolt's Risk column data into a federatable
// rollup (ADR-037 slice 3): the score sum + scored count (so the viewer
// re-means across bolts) plus the severity distribution. Skips the
// per-envoy top-N and the DB sparkline that RiskData builds — those are
// the /risk page's, not the dashboard column's, and not federated here.
func (ds *DataSource) RiskSummary(baseInterval, offlineThreshold time.Duration) ui.RiskSummary {
	allEnvoys := ds.fleet.AllEnvoys()
	inputs := make([]risk.EnvoyInput, len(allEnvoys))
	for i, es := range allEnvoys {
		inputs[i] = risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     ds.fleet.EnvoyCycle(es.ID),
		}
	}
	fr := risk.ScoreFleet(inputs, baseInterval, offlineThreshold, 0)
	return ui.RiskSummary{
		ScoreSum:    fr.ScoreSum,
		ScoredCount: fr.ScoredEnvoys,
		Critical:    fr.Distribution[risk.LevelCritical],
		High:        fr.Distribution[risk.LevelHigh],
		Medium:      fr.Distribution[risk.LevelMedium],
		Low:         fr.Distribution[risk.LevelLow],
	}
}

func (ds *DataSource) RiskPostureData(baseInterval, offlineThreshold time.Duration) ui.RiskPosturePageData {
	allEnvoys := ds.fleet.AllEnvoys()
	inputs := make([]risk.EnvoyInput, len(allEnvoys))
	for i, es := range allEnvoys {
		inputs[i] = risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
			ObservedCycle:     ds.fleet.EnvoyCycle(es.ID),
		}
	}
	fr := risk.ScoreFleet(inputs, baseInterval, offlineThreshold, len(inputs))

	// Build envoy rows with top factor.
	envoys := make([]ui.RiskEnvoyDetail, 0, len(fr.Envoys))
	factorPoints := make(map[string]int)
	factorCount := make(map[string]int)
	for _, er := range fr.Envoys {
		top := ""
		var factors []ui.RiskFactorSummary
		for _, f := range er.Factors {
			if top == "" {
				top = f.Name
			}
			factors = append(factors, ui.RiskFactorSummary{Name: f.Name, TotalPoints: f.Points})
			factorPoints[f.Name] += f.Points
			factorCount[f.Name]++
		}
		envoys = append(envoys, ui.RiskEnvoyDetail{
			EnvoyID:    er.EnvoyID,
			Hostname:   er.Hostname,
			Score:      er.Score,
			Level:      string(er.Level),
			TopFactor:  top,
			FactorList: factors,
		})
	}

	// Build fleet-wide factor summary sorted by total points.
	var topFactors []ui.RiskFactorSummary
	for name, pts := range factorPoints {
		topFactors = append(topFactors, ui.RiskFactorSummary{
			Name:        name,
			TotalPoints: pts,
			EnvoyCount:  factorCount[name],
		})
	}
	sort.Slice(topFactors, func(i, j int) bool {
		return topFactors[i].TotalPoints > topFactors[j].TotalPoints
	})

	// Load 30-day sparkline with date range and min/max.
	var sparkData []float64
	var sparkMin, sparkMax int
	var sparkStart, sparkEnd string
	var sparkDays int
	if ds.queries != nil {
		history, err := ds.queries.ListRiskPostureHistory(context.Background(), 30)
		if err == nil && len(history) > 0 {
			sparkDays = len(history)
			// History is DESC — index 0 is newest, last is oldest.
			sparkEnd = history[0].SnapshotDate
			sparkStart = history[len(history)-1].SnapshotDate
			sparkMin = int(history[0].AvgScore)
			sparkMax = int(history[0].AvgScore)
			sparkData = make([]float64, 0, len(history))
			for i := len(history) - 1; i >= 0; i-- {
				v := int(history[i].AvgScore)
				if v < sparkMin {
					sparkMin = v
				}
				if v > sparkMax {
					sparkMax = v
				}
				sparkData = append(sparkData, float64(v))
			}
		}
	}

	return ui.RiskPosturePageData{
		AvgScore:       int(fr.AvgScore),
		MaxScore:       fr.MaxScore,
		Critical:       fr.Distribution[risk.LevelCritical],
		High:           fr.Distribution[risk.LevelHigh],
		Medium:         fr.Distribution[risk.LevelMedium],
		Low:            fr.Distribution[risk.LevelLow],
		SparklineData:  sparkData,
		SparklineMin:   sparkMin,
		SparklineMax:   sparkMax,
		SparklineStart: sparkStart,
		SparklineEnd:   sparkEnd,
		SparklineDays:  sparkDays,
		Envoys:         envoys,
		TopFactors:     topFactors,
	}
}

// parseInt64 parses an agent trait count, returning 0 on any malformed value.
// Uses strconv (like its risk/metrics siblings) rather than a hand-rolled
// digit loop that mis-parsed "-5"→5 and "12x"→12 and wrapped on overflow.
func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// FleetPressure aggregates per-envoy `agent_self.*` / `host_self.*` /
// `mountpoints.*` / `repo_health.*` telemetry into the dashboard rollup.
// Revoked envoys are skipped — they're not contributing to the live
// fleet's pressure picture.
func (ds *DataSource) FleetPressure() fleetpressure.FleetSummary {
	sum, _ := fleetpressure.Aggregate(ds.healthSources())
	return sum
}

// FleetPressureDetail returns the rollup plus every per-envoy projection
// in one walk — feeds the `/agent-health` page table without re-walking
// the fleet. Revoked envoys are skipped, same as FleetPressure.
func (ds *DataSource) FleetPressureDetail() (fleetpressure.FleetSummary, []fleetpressure.EnvoyPressure) {
	return fleetpressure.Aggregate(ds.healthSources())
}

// healthSources collects every non-revoked envoy as an EnvoySource for
// the fleetpressure package to digest. Single iteration helper so the
// rollup and the per-envoy detail can share the walk.
func (ds *DataSource) healthSources() []fleetpressure.EnvoySource {
	envoys := ds.fleet.AllEnvoys()
	sources := make([]fleetpressure.EnvoySource, 0, len(envoys))
	for _, ts := range envoys {
		if ts.Revoked {
			continue
		}
		current, expected := ds.agentVersionPair(ts.AgentVersion, ts.Traits)
		sources = append(sources, fleetpressure.EnvoySource{
			ID:                   ts.ID,
			Hostname:             ts.Hostname,
			Traits:               ts.healthTraits(),
			AgentVersion:         current,
			ExpectedAgentVersion: expected,
		})
	}
	return sources
}

// EnvoyPressure projects one envoy's flattened traits into the
// per-envoy "Agent footprint" data shape. Returns zero + false when
// the envoy is unknown to the in-memory index (race during
// enrollment, or stale URL).
func (ds *DataSource) EnvoyPressure(envoyID string) (fleetpressure.EnvoyPressure, bool) {
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok {
		return fleetpressure.EnvoyPressure{}, false
	}
	current, expected := ds.agentVersionPair(ts.AgentVersion, ts.Traits)
	return fleetpressure.Compute(fleetpressure.EnvoySource{
		ID:                   ts.ID,
		Hostname:             ts.Hostname,
		Traits:               ts.Traits,
		AgentVersion:         current,
		ExpectedAgentVersion: expected,
	}), true
}

// SecurityScanTraits returns the flattened security_scan.* traits for a single envoy.
func (ds *DataSource) SecurityScanTraits(envoyID string) (map[string]string, bool) {
	_, result, ok := ds.SecurityScanContext(envoyID)
	return result, ok
}

// EnvoyFreshness returns the envoy's last-check-in timestamp and smoothed
// observed cadence. Feeds responsiveness checks on fragment handlers —
// e.g. the security drawer's "live fetch will likely fail" pre-flight
// banner. Bool is false when the envoy is unknown to the fleet index.
func (ds *DataSource) EnvoyFreshness(envoyID string) (time.Time, time.Duration, bool) {
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok {
		return time.Time{}, 0, false
	}
	return ts.LastSeen, ts.ObservedCycle(), true
}

// SecurityScanContext returns the envoy's hostname alongside its cached
// security_scan.* traits. Consumed by the server-rendered security drawer
// fragment which needs the hostname to dispatch a live-detail query.
func (ds *DataSource) SecurityScanContext(envoyID string) (string, map[string]string, bool) {
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok || ts.Traits == nil {
		return "", nil, false
	}
	result := make(map[string]string)
	for k, v := range ts.Traits {
		if strings.HasPrefix(k, traits.PrefixSecurityScan) {
			result[k] = v
		}
	}
	if len(result) == 0 {
		return ts.Hostname, nil, false
	}
	return ts.Hostname, result, true
}

// RunningCVESummary sums the per-envoy `cve_running_correlation.summary.*`
// counts emitted by the agent-side correlation collector. Envoys
// without the trait contribute zero — no error, no weird "missing
// data" display; the dashboard shows the sum of what we know about.
func (ds *DataSource) RunningCVESummary() ui.RunningCVESummary {
	var out ui.RunningCVESummary
	for _, ts := range ds.fleet.AllEnvoys() {
		if ts.Revoked || ts.Traits == nil {
			continue
		}
		total := parseIntTrait(ts.Traits, traits.RunningCVETotal)
		if total == 0 {
			continue
		}
		out.Total += total
		out.Critical += parseIntTrait(ts.Traits, traits.RunningCVECritical)
		out.High += parseIntTrait(ts.Traits, traits.RunningCVEHigh)
		out.Medium += parseIntTrait(ts.Traits, traits.RunningCVEMedium)
		out.Low += parseIntTrait(ts.Traits, traits.RunningCVELow)
		out.HostsAffected++
	}
	return out
}

// parseIntTrait reads a flattened trait value as an integer, returning
// zero when the key is absent or unparseable.
func parseIntTrait(traitMap map[string]string, key string) int {
	v, ok := traitMap[key]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// CertExpirySummary walks every envoy's `cert_lifecycle.certificates.N.not_after`
// timestamps, derives days-remaining as of now (see server/certexpiry — it is
// never a stored trait), and aggregates expired / within-7-days / within-30-days
// counts plus the number of hosts with at least one cert in any of those buckets.
// Runs on each dashboard render via the Security Posture stat card.
func (ds *DataSource) CertExpirySummary() ui.CertExpirySummary {
	const prefix = "cert_lifecycle.certificates."
	const suffix = ".not_after"
	now := time.Now()

	var summary ui.CertExpirySummary
	for _, ts := range ds.fleet.AllEnvoys() {
		if ts.Revoked || ts.Traits == nil {
			continue
		}
		var host certexpiry.Buckets
		for k, v := range ts.Traits {
			if !strings.HasPrefix(k, prefix) || !strings.HasSuffix(k, suffix) {
				continue
			}
			inner := k[len(prefix) : len(k)-len(suffix)]
			if strings.Contains(inner, ".") {
				continue
			}
			if days, ok := certexpiry.DaysRemaining(v, now); ok {
				host.Add(days)
			}
		}
		summary.Expired += host.Expired
		summary.ExpiringIn7 += host.In7
		summary.ExpiringIn30 += host.In30
		if host.Expired+host.In7+host.In30 > 0 {
			summary.HostsAffected++
			summary.PerHost = append(summary.PerHost, ui.CertHostRow{
				ID:           ts.ID,
				Hostname:     ts.Hostname,
				Expired:      host.Expired,
				ExpiringIn7:  host.In7,
				ExpiringIn30: host.In30,
			})
		}
	}
	summary.ExpiringSoon = summary.Expired + summary.ExpiringIn7 + summary.ExpiringIn30
	// Sort most-urgent first: expired > ≤7d > ≤30d, then alphabetically.
	sort.Slice(summary.PerHost, func(i, j int) bool {
		a, b := summary.PerHost[i], summary.PerHost[j]
		if a.Expired != b.Expired {
			return a.Expired > b.Expired
		}
		if a.ExpiringIn7 != b.ExpiringIn7 {
			return a.ExpiringIn7 > b.ExpiringIn7
		}
		if a.ExpiringIn30 != b.ExpiringIn30 {
			return a.ExpiringIn30 > b.ExpiringIn30
		}
		return a.Hostname < b.Hostname
	})
	return summary
}

// EnvoyCertDetails assembles per-certificate rows for one envoy from its
// `cert_lifecycle.certificates.N.*` flattened trait fields. Filters to
// certs with days_remaining < 30 (matches the fleet-panel threshold).
// The `bucket` argument narrows further: "expired" keeps only days<0,
// "7d" keeps 0..6, "30d" keeps 7..29; empty string returns all three.
// Returns the envoy's hostname alongside the rows so the caller can
// render a header without a separate lookup.
func (ds *DataSource) EnvoyCertDetails(envoyID, bucket string) ([]ui.CertDetail, string, bool) {
	// Hostname + existence come from the FleetIndex: a miss is a genuine
	// unknown-envoy 404 (SecurityCerts keys its 404 off the returned ok).
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok {
		return nil, "", false
	}
	// Per-cert detail (path/cn/issuer/is_ca/sans) is no longer held in the
	// in-memory flattened map — Lever 1c keeps only cert_lifecycle.certificates.N
	// .not_after there for the fleet-wide expiry buckets. Read the full nested
	// records from the DB JSON on demand; this is a cold, per-envoy, user-
	// triggered path. An envoy known to the fleet but with no traits row yet
	// (enrolled, never reported) yields an empty list, NOT a 404.
	cf, err := ds.queries.GetLatestEnvoyTraits(context.Background(), envoyID)
	if err != nil {
		return nil, ts.Hostname, true
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cf.Traits), &parsed); err != nil {
		return nil, ts.Hostname, true
	}

	now := time.Now()
	certs := certificatesFromTraits(parsed)
	out := make([]ui.CertDetail, 0, len(certs))
	for _, cm := range certs {
		notAfter := jsonString(cm["not_after"])
		if notAfter == "" {
			notAfter = jsonString(cm["expires"])
		}
		// days-remaining is derived from not_after (never a stored trait —
		// see server/certexpiry); skip certs whose not_after won't parse.
		days, ok := certexpiry.DaysRemaining(notAfter, now)
		if !ok {
			continue
		}
		// Fleet-panel threshold: certs with >= 30 days remaining aren't "expiring soon".
		if days >= certexpiry.SoonThreshold {
			continue
		}
		if bucket == "expired" && days >= 0 {
			continue
		}
		if bucket == "7d" && (days < 0 || days >= certexpiry.UrgentThreshold) {
			continue
		}
		if bucket == "30d" && (days < certexpiry.UrgentThreshold || days >= certexpiry.SoonThreshold) {
			continue
		}
		out = append(out, ui.CertDetail{
			Path:          jsonString(cm["path"]),
			CN:            jsonString(cm["cn"]),
			SANs:          jsonStringSlice(cm["sans"]),
			Issuer:        jsonString(cm["issuer"]),
			NotAfter:      notAfter,
			DaysRemaining: days,
			IsCA:          jsonBool(cm["is_ca"]),
		})
	}
	// Most-urgent first: expired (most-negative) before soonest-expiring.
	sort.Slice(out, func(i, j int) bool {
		return out[i].DaysRemaining < out[j].DaysRemaining
	})
	return out, ts.Hostname, true
}

// certificatesFromTraits returns the cert_lifecycle.certificates[] objects from
// nested traits JSON (one DB row, unmarshaled), or nil if absent/malformed.
func certificatesFromTraits(parsed map[string]any) []map[string]any {
	cl, ok := parsed["cert_lifecycle"].(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := cl["certificates"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, c := range arr {
		if cm, ok := c.(map[string]any); ok {
			out = append(out, cm)
		}
	}
	return out
}

// jsonString / jsonBool / jsonStringSlice coerce values from an unmarshaled
// map[string]any (nested traits JSON), where strings/bools/arrays are native
// types — unlike the flattened map where everything is a string.
func jsonString(v any) string {
	s, _ := v.(string)
	return s
}

func jsonBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func jsonStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
