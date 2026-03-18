// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/ui"
)

// DataSource implements ui.DataSource backed by a FleetIndex for real-time
// queries and DB for historical data (runs, plans, compliance history).
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
	allThralls := ds.fleet.AllThralls()

	totalThralls := len(allThralls)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}
		if !ts.LastSeen.IsZero() && time.Since(ts.LastSeen) < time.Hour {
			activeCount++
		}
		switch ts.ComplianceStatus {
		case "drifted":
			driftedCount++
		case "failing":
			failureCount++
		}
	}

	driftRate := float64(0)
	if totalThralls > 0 {
		driftRate = float64(driftedCount) / float64(totalThralls) * 100
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
			thrallMap := make(map[string]string, len(allThralls))
			for _, ts := range allThralls {
				thrallMap[ts.ID] = ts.Hostname
			}
			for _, r := range dbRuns {
				recentRuns = append(recentRuns, ui.RunRow{
					ID:           r.ID,
					Hostname:     thrallMap[r.ThrallID],
					StartedAt:    r.StartedAt,
					Status:       r.Status,
					ModuleCount:  r.ModuleCount,
					ChangedCount: r.ChangedCount,
					FailedCount:  r.FailedCount,
				})
			}
		}
	}

	fleetRows := make([]ui.FleetRow, 0, len(allThralls))
	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}
		fleetRows = append(fleetRows, ui.FleetRow{
			ID:       ts.ID,
			Hostname: ts.Hostname,
			IP:       ts.IP,
			LastSeen: ts.LastSeen,
			Status:   ts.ComplianceStatus,
		})
	}

	return ui.DashboardPageData{
		PageData:       ui.PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalThralls:   totalThralls,
		ActiveThralls:  activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
		FleetRows:      fleetRows,
	}
}

func (ds *DataSource) ComplianceData(baseInterval time.Duration) ui.CompliancePageData {
	summary := ds.fleet.ComplianceSummary()
	total := int64(summary.Total)

	// Stale thralls are excluded from the compliance percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := total - int64(summary.LostContact)
	compPct := float64(0)
	if activeTotal > 0 {
		compPct = float64(summary.Compliant) / float64(activeTotal) * 100
	}

	// Gather non-compliant thralls.
	var nonCompliant []ui.ComplianceRow
	allThralls := ds.fleet.AllThralls()
	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}
		if ts.ComplianceStatus != "compliant" {
			nonCompliant = append(nonCompliant, ui.ComplianceRow{
				Hostname:      ts.Hostname,
				Status:        ts.ComplianceStatus,
				LastSeen:      ts.LastSeen,
				LastRunStatus: ts.LastRunStatus,
			})
		}
	}

	// Sparkline from FleetIndex ring buffer (in-memory, no DB query).
	// Falls back to DB compliance_history if the ring buffer is empty.
	snapshots := ds.fleet.ComplianceSparkline()
	var sparkData []float64
	if len(snapshots) > 0 {
		sparkData = make([]float64, 0, len(snapshots))
		for _, s := range snapshots {
			active := s.Counters.Total - s.Counters.LostContact
			if active > 0 {
				sparkData = append(sparkData, float64(s.Counters.Compliant)/float64(active)*100)
			}
		}
	} else {
		ctx := context.Background()
		history, err := ds.queries.ListComplianceHistory(ctx, 30)
		if err != nil {
			slog.Error("failed to list compliance history", "error", err)
		}
		sparkData = make([]float64, 0, len(history))
		for i := len(history) - 1; i >= 0; i-- {
			h := history[i]
			active := h.Total - h.Stale
			if active > 0 {
				sparkData = append(sparkData, float64(h.Compliant)/float64(active)*100)
			}
		}
	}

	return ui.CompliancePageData{
		PageData:      ui.PageData{Title: "Compliance", ActiveNav: "compliance"},
		CompliancePct: compPct,
		Total:         total,
		Compliant:     int64(summary.Compliant),
		RecurringDrift: int64(summary.RecurringDrift),
		Errors:         int64(summary.Errors),
		LostContact:    int64(summary.LostContact),
		NoData:         int64(summary.NoData),
		SparklineData: sparkData,
		NonCompliant:  nonCompliant,
	}
}

func (ds *DataSource) DashboardStatsData(baseInterval time.Duration) ui.DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval)
	return ui.DashboardStatsFragmentData{
		TotalThralls:   d.TotalThralls,
		ActiveThralls:  d.ActiveThralls,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *DataSource) ThrallsData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) ui.ThrallsPageData {
	thralls, hasMore := ds.fleet.PaginatedThralls(afterHostname, limit, statusFilter)

	rows := make([]ui.ThrallRow, 0, len(thralls))
	for _, ts := range thralls {
		rows = append(rows, ui.ThrallRow{
			ID:         ts.ID,
			Hostname:   ts.Hostname,
			IPAddress:  ts.IP,
			EnrolledAt: ts.EnrolledAt,
			LastSeen:   ts.LastSeen,
			Status:     ts.ComplianceStatus,
		})
	}

	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor = rows[len(rows)-1].Hostname
	}

	return ui.ThrallsPageData{
		PageData:     ui.PageData{Title: "Thralls", ActiveNav: "thralls"},
		Thralls:      rows,
		StatusFilter: statusFilter,
		ThrallCount:  len(rows),
		HasMore:      hasMore,
		NextCursor:   nextCursor,
	}
}

func (ds *DataSource) ThrallDetailData(thrallID string, baseInterval time.Duration) (ui.ThrallDetailPageData, error) {
	// FleetIndex for header (shell only — runs and facts loaded via fragments).
	ts := ds.fleet.GetThrall(thrallID)
	if ts == nil {
		ctx := context.Background()
		thrall, err := ds.queries.GetThrallByID(ctx, thrallID)
		if err != nil {
			return ui.ThrallDetailPageData{}, err
		}
		lastSeen := time.Time{}
		if thrall.LastSeen.Valid {
			lastSeen = thrall.LastSeen.Time
		}
		var recent []compliance.RunInfo
		runs, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{ThrallID: thrallID, Limit: 1})
		if err == nil && len(runs) > 0 {
			recent = []compliance.RunInfo{{Status: runs[0].Status, ChangedCount: runs[0].ChangedCount}}
		}
		compStatus := compliance.ComputeStatus(recent, lastSeen, baseInterval)
		ts = &ThrallState{
			ID:               thrall.ID,
			Hostname:         thrall.Hostname,
			IP:               thrall.IpAddress,
			EnrolledAt:       thrall.EnrolledAt,
			LastSeen:         lastSeen,
			ComplianceStatus: compStatus,
		}
	}

	return ui.ThrallDetailPageData{
		PageData: ui.PageData{Title: "Thrall: " + ts.Hostname, ActiveNav: "thralls"},
		Thrall: ui.ThrallRow{
			ID:         ts.ID,
			Hostname:   ts.Hostname,
			IPAddress:  ts.IP,
			EnrolledAt: ts.EnrolledAt,
			LastSeen:   ts.LastSeen,
			Status:     ts.ComplianceStatus,
		},
	}, nil
}

func (ds *DataSource) ThrallRunsData(thrallID string) (ui.ThrallRunsFragmentData, error) {
	ts := ds.fleet.GetThrall(thrallID)
	hostname := thrallID
	if ts != nil {
		hostname = ts.Hostname
	}

	ctx := context.Background()
	dbRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
		ThrallID: thrallID,
		Limit:    20,
	})
	if err != nil {
		slog.Error("failed to list runs for thrall", "error", err)
		return ui.ThrallRunsFragmentData{}, nil
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

	var runs []ui.ThrallDetailRunRow
	var driftHistory []ui.DriftPoint
	for _, r := range dbRuns {
		runs = append(runs, ui.ThrallDetailRunRow{
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
		driftHistory = append(driftHistory, ui.DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return ui.ThrallRunsFragmentData{Runs: runs, DriftHistory: driftHistory}, nil
}

func (ds *DataSource) ThrallFactsData(thrallID string) (ui.ThrallFactsFragmentData, error) {
	ts := ds.fleet.GetThrall(thrallID)
	facts := make(map[string]any)
	if ts != nil && ts.Traits != nil {
		for k, v := range ts.Traits {
			facts[k] = v
		}
	} else {
		ctx := context.Background()
		cf, err := ds.queries.GetLatestThrallTraits(ctx, thrallID)
		if err == nil {
			if err := json.Unmarshal([]byte(cf.Traits), &facts); err != nil {
				slog.Warn("malformed traits JSON", "thrall_id", thrallID, "error", err)
			}
		}
	}
	return ui.ThrallFactsFragmentData{Facts: facts}, nil
}

func (ds *DataSource) RunsData(statusFilter string) ui.RunsPageData {
	ctx := context.Background()
	allRuns, err := ds.queries.ListAllRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return ui.RunsPageData{PageData: ui.PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	// Look up hostnames individually from FleetIndex (O(1) per run) instead
	// of copying the entire thralls map.
	var rows []ui.RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		hostname := r.ThrallID
		if ts := ds.fleet.GetThrall(r.ThrallID); ts != nil {
			hostname = ts.Hostname
		}
		rows = append(rows, ui.RunRow{
			ID:           r.ID,
			Hostname:     hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
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
	hostname := r.ThrallID
	thrallID := r.ThrallID
	ts := ds.fleet.GetThrall(r.ThrallID)
	if ts != nil {
		hostname = ts.Hostname
	} else {
		thrall, err := ds.queries.GetThrallByID(ctx, r.ThrallID)
		if err == nil {
			hostname = thrall.Hostname
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
		ThrallID: thrallID,
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
		PageData: ui.PageData{Title: "Config: " + hostname, ActiveNav: "thralls"},
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
	hostname := p.ThrallID
	ts := ds.fleet.GetThrall(p.ThrallID)
	if ts != nil {
		hostname = ts.Hostname
	} else {
		thrall, err := ds.queries.GetThrallByID(ctx, p.ThrallID)
		if err == nil {
			hostname = thrall.Hostname
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
		switch r.PlannedAction {
		case "create":
			createCount++
		case "update":
			updateCount++
		case "delete":
			deleteCount++
		default:
			skipCount++
		}
	}

	return ui.PlanDetailPageData{
		PageData:    ui.PageData{Title: "Plan Detail", ActiveNav: "thralls"},
		PlanID:      p.ID,
		ThrallID:    p.ThrallID,
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
	switch action {
	case "create":
		return "success"
	case "update":
		return "info"
	case "delete":
		return "danger"
	default:
		return "muted"
	}
}
