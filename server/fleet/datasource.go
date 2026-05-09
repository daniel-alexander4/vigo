// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/traits"
	"git.alexander4.io/vigo/ui"
)

// DataSource implements ui.DataSource backed by a FleetIndex for real-time
// queries and DB for historical data (runs, plans, convergence history).
type DataSource struct {
	fleet       *FleetIndex
	queries     *db.Queries
	runStore    db.RunStore
	configStore *config.ConfigStore
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

func (ds *DataSource) DashboardData(baseInterval time.Duration) ui.DashboardPageData {
	allEnvoys := ds.fleet.AllEnvoys()

	totalEnvoys := len(allEnvoys)
	activeCount := 0
	driftCount := 0
	failureCount := 0

	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}
		if !ts.LastSeen.IsZero() && time.Since(ts.LastSeen) < time.Hour {
			activeCount++
		}
		// Failure axis: contributes to failureCount.
		switch convergence.ClassifyStatus(ts.ConvergenceStatus) {
		case convergence.StatusFailed, convergence.StatusDegraded:
			failureCount++
		}
		// Drift axis: contributes to driftCount when modules keep
		// mutating across recent runs (subset of Changed).
		if ts.Drift == convergence.DriftDiverged {
			driftCount++
		}
	}

	driftRate := float64(0)
	if totalEnvoys > 0 {
		driftRate = float64(driftCount) / float64(totalEnvoys) * 100
	}

	// Recent runs from ring buffer.
	recentFleetRuns := ds.fleet.RecentRuns(10)
	recentRuns := make([]ui.RunRow, 0, len(recentFleetRuns))
	for _, r := range recentFleetRuns {
		recentRuns = append(recentRuns, ui.RunRow{
			ID:           r.RunID,
			Hostname:     r.Hostname,
			StartedAt:    r.FinishedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	// If ring buffer is empty, fall back to DB.
	if len(recentRuns) == 0 {
		ctx := context.Background()
		dbRuns, err := ds.queries.ListAllRuns(ctx, 10)
		if err == nil {
			envoyMap := make(map[string]string, len(allEnvoys))
			for _, ts := range allEnvoys {
				envoyMap[ts.ID] = ts.Hostname
			}
			for _, r := range dbRuns {
				recentRuns = append(recentRuns, ui.RunRow{
					ID:           r.ID,
					Hostname:     envoyMap[r.EnvoyID],
					StartedAt:    r.StartedAt,
					Status:       r.Status,
					ModuleCount:  r.ModuleCount,
					ChangedCount: r.ChangedCount,
					FailedCount:  r.FailedCount,
				})
			}
		}
	}

	fleetRows := make([]ui.FleetRow, 0, len(allEnvoys))
	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}
		fleetRows = append(fleetRows, ui.FleetRow{
			ID:        ts.ID,
			Hostname:  ts.Hostname,
			IP:        ts.IP,
			LastSeen:  ts.LastSeen,
			Status:    ts.ConvergenceStatus,
			LastRunID: ts.LastRunID,
		})
	}

	return ui.DashboardPageData{
		PageData:       ui.PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalEnvoys:   totalEnvoys,
		ActiveEnvoys:  activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
		FleetRows:      fleetRows,
	}
}

func (ds *DataSource) ConvergenceData(baseInterval time.Duration) ui.ConvergencePageData {
	summary := ds.fleet.ConvergenceSummary()
	total := int64(summary.Total)

	// Stale envoys are excluded from the convergence percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := total - int64(summary.Offline)
	convPct := float64(0)
	if activeTotal > 0 {
		convPct = float64(summary.Converged) / float64(activeTotal) * 100
	}

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
				sparkData = append(sparkData, float64(s.Counters.Converged)/float64(active)*100)
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
				sparkData = append(sparkData, float64(h.Converged)/float64(active)*100)
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

func (ds *DataSource) DashboardStatsData(baseInterval time.Duration) ui.DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval)
	return ui.DashboardStatsFragmentData{
		TotalEnvoys:   d.TotalEnvoys,
		ActiveEnvoys:  d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *DataSource) EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int, search string) ui.EnvoysPageData {
	envoys, hasMore := ds.fleet.PaginatedEnvoys(afterHostname, limit, statusFilter, search)

	rows := make([]ui.EnvoyRow, 0, len(envoys))
	for _, ts := range envoys {
		var failurePct float64
		if ts.LastRunModuleCount > 0 {
			failurePct = float64(ts.LastRunFailedCount) / float64(ts.LastRunModuleCount) * 100
		}
		rows = append(rows, ui.EnvoyRow{
			ID:           ts.ID,
			Hostname:     ts.Hostname,
			IPAddress:    ts.IP,
			EnrolledAt:   ts.EnrolledAt,
			LastSeen:     ts.LastSeen,
			LastRunAt:    ts.LastRunAt,
			LastRunID:    ts.LastRunID,
			Status:       ts.ConvergenceStatus,
			FailedCount:  ts.LastRunFailedCount,
			ModuleCount:  ts.LastRunModuleCount,
			FailurePct:   failurePct,
			Offline:      ts.Offline,
			AgentVersion: ts.AgentVersion,
		})
	}

	// Populate filter-bar counts from the FleetIndex's precomputed
	// counters — the same source feeding the row statuses above, so
	// the buttons can never disagree with what's rendered below them.
	counts := ds.fleet.ConvergenceSummary()
	return ui.EnvoysPageData{
		PageData:       ui.PageData{Title: "Envoys", ActiveNav: "envoys"},
		Envoys:         rows,
		ConvFilter:     statusFilter,
		Search:         search,
		EnvoyCount:     len(rows),
		HasMore:        hasMore,
		Limit:          limit,
		NextOffset:     len(rows),
		TotalEnvoys:    counts.Total,
		CountConverged: counts.Converged,
		CountDegraded:  counts.Degraded,
		CountFailed:    counts.Failed,
		CountNoData:    counts.NoData,
		CountChanged:   counts.Changed,
		CountDiverged:  counts.Diverged,
		CountOffline:   counts.Offline,
	}
}

func (ds *DataSource) EnvoyDetailData(envoyID string, baseInterval time.Duration) (ui.EnvoyDetailPageData, error) {
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
			recent = []convergence.RunInfo{{Status: runs[0].Status, ChangedCount: runs[0].ChangedCount}}
		}
		convStatus := convergence.ComputeConvergence(recent)
		ts = &EnvoyState{
			ID:               envoy.ID,
			Hostname:         envoy.Hostname,
			IP:               envoy.IpAddress,
			EnrolledAt:       envoy.EnrolledAt,
			LastSeen:         lastSeen,
			ConvergenceStatus: convStatus,
		}
	}

	return ui.EnvoyDetailPageData{
		PageData: ui.PageData{Title: "Envoy: " + ts.Hostname, ActiveNav: "envoys"},
		Envoy: ui.EnvoyRow{
			ID:         ts.ID,
			Hostname:   ts.Hostname,
			IPAddress:  ts.IP,
			EnrolledAt: ts.EnrolledAt,
			LastSeen:   ts.LastSeen,
			Status:     ts.ConvergenceStatus,
			Offline:    ts.Offline,
		},
	}, nil
}

func (ds *DataSource) EnvoyRunsData(envoyID string) (ui.EnvoyRunsFragmentData, error) {
	ts := ds.fleet.GetEnvoy(envoyID)
	hostname := envoyID
	if ts != nil {
		hostname = ts.Hostname
	}

	ctx := context.Background()
	dbRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: envoyID,
		Limit:    20,
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
			ModuleName:   res.ModuleName,
			ResourceType: res.ResourceType,
			ResourceName: res.ResourceName,
			ActionTaken:  res.ActionTaken,
			Changed:      res.Changed != 0,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
	}

	var runs []ui.EnvoyDetailRunRow
	for _, r := range dbRuns {
		runs = append(runs, ui.EnvoyDetailRunRow{
			RunRow: ui.RunRow{
				ID:           r.ID,
				Hostname:     hostname,
				StartedAt:    r.StartedAt,
				Status:       r.Status,
				ModuleCount:  r.ModuleCount,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
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
		if ts := ds.fleet.GetEnvoy(r.EnvoyID); ts != nil {
			hostname = ts.Hostname
		}
		rows = append(rows, ui.RunRow{
			ID:           r.ID,
			EnvoyID:      r.EnvoyID,
			Hostname:     hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
			FailurePct:   convergence.FailurePct(r.FailedCount, r.ModuleCount),
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
	ts := ds.fleet.GetEnvoy(r.EnvoyID)
	if ts != nil {
		hostname = ts.Hostname
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
			ModuleName:   res.ModuleName,
			ResourceType: res.ResourceType,
			ResourceName: res.ResourceName,
			ActionTaken:  res.ActionTaken,
			Changed:      res.Changed != 0,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
	}

	return ui.RunDetailPageData{
		PageData: ui.PageData{Title: "Run Detail", ActiveNav: "runs"},
		EnvoyID: envoyID,
		Run: ui.RunRow{
			ID:           r.ID,
			Hostname:     hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
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
			ModuleName:    r.ModuleName,
			ResourceType:  r.ResourceType,
			ResourceName:  r.ResourceName,
			PlannedAction: r.PlannedAction,
			ActionColor:   color,
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
		EnvoyID:    p.EnvoyID,
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

		if v, ok := ts.Traits[traits.TrivyCritical]; ok {
			hasScan = true
			n := parseInt64(v)
			row.TrivyCritical = n
			data.TrivyCritical += n
		}
		if v, ok := ts.Traits[traits.TrivyHigh]; ok {
			n := parseInt64(v)
			row.TrivyHigh = n
			data.TrivyHigh += n
		}
		if v, ok := ts.Traits[traits.TrivyMedium]; ok {
			row.TrivyMedium = parseInt64(v)
			data.TrivyMedium += row.TrivyMedium
		}
		if v, ok := ts.Traits[traits.TrivyLow]; ok {
			row.TrivyLow = parseInt64(v)
			data.TrivyLow += row.TrivyLow
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
func (ds *DataSource) RiskData(baseInterval time.Duration) ui.RiskSummaryData {
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
	fr := risk.ScoreFleet(inputs, baseInterval, 5)
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

func (ds *DataSource) RiskPostureData(baseInterval time.Duration) ui.RiskPosturePageData {
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
	fr := risk.ScoreFleet(inputs, baseInterval, len(inputs))

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

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}

// FleetPressure aggregates per-envoy `agent_self.*` / `host_self.*`
// telemetry into the dashboard rollup. Revoked envoys are skipped —
// they're not contributing to the live fleet's pressure picture.
func (ds *DataSource) FleetPressure() fleetpressure.FleetSummary {
	envoys := ds.fleet.AllEnvoys()
	sources := make([]fleetpressure.EnvoySource, 0, len(envoys))
	for _, ts := range envoys {
		if ts.Revoked {
			continue
		}
		sources = append(sources, fleetpressure.EnvoySource{
			ID:       ts.ID,
			Hostname: ts.Hostname,
			Traits:   ts.Traits,
		})
	}
	return fleetpressure.Aggregate(sources)
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
	return fleetpressure.Compute(fleetpressure.EnvoySource{
		ID:       ts.ID,
		Hostname: ts.Hostname,
		Traits:   ts.Traits,
	}), true
}

// SecurityScanTraits returns the flattened security_scan.* traits for a single envoy.
func (ds *DataSource) SecurityScanTraits(envoyID string) (map[string]string, bool) {
	_, result, ok := ds.SecurityScanContext(envoyID)
	return result, ok
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

// CertExpirySummary walks every envoy's `cert_lifecycle.certificates.N.days_remaining`
// rows and aggregates expired / within-7-days / within-30-days counts plus
// the number of hosts with at least one cert in any of those buckets.
// Runs on each dashboard render via the Security Posture stat card.
func (ds *DataSource) CertExpirySummary() ui.CertExpirySummary {
	const prefix = "cert_lifecycle.certificates."
	const suffix = ".days_remaining"

	var summary ui.CertExpirySummary
	for _, ts := range ds.fleet.AllEnvoys() {
		if ts.Revoked || ts.Traits == nil {
			continue
		}
		var hostExpired, host7, host30 int
		for k, v := range ts.Traits {
			if !strings.HasPrefix(k, prefix) || !strings.HasSuffix(k, suffix) {
				continue
			}
			inner := k[len(prefix) : len(k)-len(suffix)]
			if strings.Contains(inner, ".") {
				continue
			}
			days, err := strconv.Atoi(v)
			if err != nil {
				continue
			}
			switch {
			case days < 0:
				hostExpired++
			case days < 7:
				host7++
			case days < 30:
				host30++
			}
		}
		summary.Expired += hostExpired
		summary.ExpiringIn7 += host7
		summary.ExpiringIn30 += host30
		if hostExpired+host7+host30 > 0 {
			summary.HostsAffected++
			summary.PerHost = append(summary.PerHost, ui.CertHostRow{
				ID:           ts.ID,
				Hostname:     ts.Hostname,
				Expired:      hostExpired,
				ExpiringIn7:  host7,
				ExpiringIn30: host30,
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
	const prefix = "cert_lifecycle.certificates."
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok || ts.Traits == nil {
		return nil, "", false
	}
	// Group flattened keys by index, then materialize one CertDetail per index.
	type rowBuf struct {
		Path          string
		CN            string
		SANs          []string
		Issuer        string
		NotAfter      string
		DaysRemaining int
		IsCA          bool
		HasDays       bool
	}
	rows := map[string]*rowBuf{}
	getRow := func(idx string) *rowBuf {
		if r, ok := rows[idx]; ok {
			return r
		}
		r := &rowBuf{}
		rows[idx] = r
		return r
	}
	for k, v := range ts.Traits {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		dot := strings.Index(rest, ".")
		if dot < 0 {
			continue
		}
		idx, field := rest[:dot], rest[dot+1:]
		if strings.Contains(idx, ".") {
			continue
		}
		r := getRow(idx)
		switch {
		case field == "path":
			r.Path = v
		case field == "cn":
			r.CN = v
		case field == "issuer":
			r.Issuer = v
		case field == "not_after", field == "expires":
			if r.NotAfter == "" {
				r.NotAfter = v
			}
		case field == "days_remaining":
			if n, err := strconv.Atoi(v); err == nil {
				r.DaysRemaining = n
				r.HasDays = true
			}
		case field == "is_ca":
			r.IsCA = v == "true"
		case strings.HasPrefix(field, "sans."):
			// sans.0, sans.1, … — preserve index order.
			sanIdx, err := strconv.Atoi(strings.TrimPrefix(field, "sans."))
			if err == nil {
				for len(r.SANs) <= sanIdx {
					r.SANs = append(r.SANs, "")
				}
				r.SANs[sanIdx] = v
			}
		}
	}

	out := make([]ui.CertDetail, 0, len(rows))
	for _, r := range rows {
		if !r.HasDays {
			continue
		}
		// Fleet-panel threshold: certs with >= 30 days remaining aren't "expiring soon".
		if r.DaysRemaining >= 30 {
			continue
		}
		if bucket == "expired" && r.DaysRemaining >= 0 {
			continue
		}
		if bucket == "7d" && (r.DaysRemaining < 0 || r.DaysRemaining >= 7) {
			continue
		}
		if bucket == "30d" && (r.DaysRemaining < 7 || r.DaysRemaining >= 30) {
			continue
		}
		// Strip any trailing empty slots in SANs caused by sparse indexes.
		sans := r.SANs
		for len(sans) > 0 && sans[len(sans)-1] == "" {
			sans = sans[:len(sans)-1]
		}
		out = append(out, ui.CertDetail{
			Path:          r.Path,
			CN:            r.CN,
			SANs:          sans,
			Issuer:        r.Issuer,
			NotAfter:      r.NotAfter,
			DaysRemaining: r.DaysRemaining,
			IsCA:          r.IsCA,
		})
	}
	// Most-urgent first: expired (most-negative) before soonest-expiring.
	sort.Slice(out, func(i, j int) bool {
		return out[i].DaysRemaining < out[j].DaysRemaining
	})
	return out, ts.Hostname, true
}
