// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

	return ui.DashboardPageData{
		PageData:       ui.PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalThralls:   totalThralls,
		ActiveThralls:  activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
	}
}

func (ds *DataSource) ComplianceData(baseInterval time.Duration) ui.CompliancePageData {
	summary := ds.fleet.ComplianceSummary()
	total := int64(summary.Total)

	compPct := float64(0)
	if total > 0 {
		compPct = float64(summary.Compliant) / float64(total) * 100
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

	// Sparkline from compliance history (DB — historical data).
	ctx := context.Background()
	history, err := ds.queries.ListComplianceHistory(ctx, 30)
	if err != nil {
		slog.Error("failed to list compliance history", "error", err)
	}

	sparkData := make([]float64, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		if h.Total > 0 {
			sparkData = append(sparkData, float64(h.Compliant)/float64(h.Total)*100)
		}
	}

	return ui.CompliancePageData{
		PageData:      ui.PageData{Title: "Compliance", ActiveNav: "compliance"},
		CompliancePct: compPct,
		Total:         total,
		Compliant:     int64(summary.Compliant),
		Drifted:       int64(summary.Drifted),
		Failing:       int64(summary.Failing),
		Stale:         int64(summary.Stale),
		Unknown:       int64(summary.Unknown),
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

func (ds *DataSource) ThrallsData(baseInterval time.Duration, statusFilter string) ui.ThrallsPageData {
	allThralls := ds.fleet.AllThralls()

	var rows []ui.ThrallRow
	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}
		if statusFilter != "" && ts.ComplianceStatus != statusFilter {
			continue
		}
		rows = append(rows, ui.ThrallRow{
			ID:         ts.ID,
			Hostname:   ts.Hostname,
			IPAddress:  ts.IP,
			EnrolledAt: ts.EnrolledAt,
			LastSeen:   ts.LastSeen,
			Status:     ts.ComplianceStatus,
		})
	}

	return ui.ThrallsPageData{
		PageData:     ui.PageData{Title: "Thralls", ActiveNav: "thralls"},
		Thralls:      rows,
		StatusFilter: statusFilter,
		ThrallCount:  len(rows),
	}
}

func (ds *DataSource) ThrallDetailData(thrallID string, baseInterval time.Duration) (ui.ThrallDetailPageData, error) {
	// FleetIndex for header, DB for run history.
	ts := ds.fleet.GetThrall(thrallID)
	if ts == nil {
		// Fallback to DB.
		ctx := context.Background()
		thrall, err := ds.queries.GetThrallByID(ctx, thrallID)
		if err != nil {
			return ui.ThrallDetailPageData{}, err
		}
		lastSeen := time.Time{}
		if thrall.LastSeen.Valid {
			lastSeen = thrall.LastSeen.Time
		}
		// Compute status from DB.
		var ri *compliance.RunInfo
		runs, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{ThrallID: thrallID, Limit: 1})
		if err == nil && len(runs) > 0 {
			ri = &compliance.RunInfo{Status: runs[0].Status, ChangedCount: runs[0].ChangedCount}
		}
		compStatus := compliance.ComputeStatus(ri, lastSeen, baseInterval)
		ts = &ThrallState{
			ID:               thrall.ID,
			Hostname:         thrall.Hostname,
			IP:               thrall.IpAddress,
			EnrolledAt:       thrall.EnrolledAt,
			LastSeen:         lastSeen,
			ComplianceStatus: compStatus,
		}
	}

	thrallRow := ui.ThrallRow{
		ID:         ts.ID,
		Hostname:   ts.Hostname,
		IPAddress:  ts.IP,
		EnrolledAt: ts.EnrolledAt,
		LastSeen:   ts.LastSeen,
		Status:     ts.ComplianceStatus,
	}

	// Run history from DB (historical data stays in SQLite).
	ctx := context.Background()
	dbRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
		ThrallID: thrallID,
		Limit:    20,
	})
	if err != nil {
		slog.Error("failed to list runs for thrall", "error", err)
	}

	var runs []ui.ThrallDetailRunRow
	var driftHistory []ui.DriftPoint
	for _, r := range dbRuns {
		dbResults, err := ds.queries.ListRunResults(ctx, r.ID)
		if err != nil {
			slog.Error("failed to list run results", "run_id", r.ID, "error", err)
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

		runs = append(runs, ui.ThrallDetailRunRow{
			RunRow: ui.RunRow{
				ID:           r.ID,
				Hostname:     ts.Hostname,
				StartedAt:    r.StartedAt,
				Status:       r.Status,
				ModuleCount:  r.ModuleCount,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
			},
			Results: results,
		})

		driftHistory = append(driftHistory, ui.DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	// Traits from FleetIndex or DB.
	facts := make(map[string]any)
	if ts.Traits != nil {
		for k, v := range ts.Traits {
			facts[k] = v
		}
	} else {
		cf, err := ds.queries.GetLatestThrallTraits(ctx, thrallID)
		if err == nil {
			_ = json.Unmarshal([]byte(cf.Traits), &facts)
		}
	}

	return ui.ThrallDetailPageData{
		PageData:     ui.PageData{Title: "Thrall: " + ts.Hostname, ActiveNav: "thralls"},
		Thrall:       thrallRow,
		Runs:         runs,
		Facts:        facts,
		DriftHistory: driftHistory,
	}, nil
}

func (ds *DataSource) RunsData(statusFilter string) ui.RunsPageData {
	ctx := context.Background()
	allRuns, err := ds.queries.ListAllRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return ui.RunsPageData{PageData: ui.PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	// Build hostname lookup from FleetIndex.
	allThralls := ds.fleet.AllThralls()
	thrallMap := make(map[string]string, len(allThralls))
	for _, ts := range allThralls {
		thrallMap[ts.ID] = ts.Hostname
	}

	var rows []ui.RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		rows = append(rows, ui.RunRow{
			ID:           r.ID,
			Hostname:     thrallMap[r.ThrallID],
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
