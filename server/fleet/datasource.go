// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/compliance"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/ui"
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
	allEnvoys := ds.fleet.AllEnvoys()

	totalEnvoys := len(allEnvoys)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, ts := range allEnvoys {
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
	if totalEnvoys > 0 {
		driftRate = float64(driftedCount) / float64(totalEnvoys) * 100
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
			ID:       ts.ID,
			Hostname: ts.Hostname,
			IP:       ts.IP,
			LastSeen: ts.LastSeen,
			Status:   ts.ComplianceStatus,
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

func (ds *DataSource) ComplianceData(baseInterval time.Duration) ui.CompliancePageData {
	summary := ds.fleet.ComplianceSummary()
	total := int64(summary.Total)

	// Stale envoys are excluded from the compliance percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := total - int64(summary.LostContact)
	compPct := float64(0)
	if activeTotal > 0 {
		compPct = float64(summary.Compliant) / float64(activeTotal) * 100
	}

	// Gather non-compliant envoys.
	var nonCompliant []ui.ComplianceRow
	allEnvoys := ds.fleet.AllEnvoys()
	for _, ts := range allEnvoys {
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
			active := h.Total - h.LostContact
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
		TotalEnvoys:   d.TotalEnvoys,
		ActiveEnvoys:  d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *DataSource) EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) ui.EnvoysPageData {
	envoys, hasMore := ds.fleet.PaginatedEnvoys(afterHostname, limit, statusFilter)

	rows := make([]ui.EnvoyRow, 0, len(envoys))
	for _, ts := range envoys {
		rows = append(rows, ui.EnvoyRow{
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

	return ui.EnvoysPageData{
		PageData:     ui.PageData{Title: "Envoys", ActiveNav: "envoys"},
		Envoys:      rows,
		StatusFilter: statusFilter,
		EnvoyCount:  len(rows),
		HasMore:      hasMore,
		NextCursor:   nextCursor,
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
		var recent []compliance.RunInfo
		runs, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{EnvoyID: envoyID, Limit: 1})
		if err == nil && len(runs) > 0 {
			recent = []compliance.RunInfo{{Status: runs[0].Status, ChangedCount: runs[0].ChangedCount}}
		}
		compStatus := compliance.ComputeStatus(recent, lastSeen, baseInterval)
		ts = &EnvoyState{
			ID:               envoy.ID,
			Hostname:         envoy.Hostname,
			IP:               envoy.IpAddress,
			EnrolledAt:       envoy.EnrolledAt,
			LastSeen:         lastSeen,
			ComplianceStatus: compStatus,
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
			Status:     ts.ComplianceStatus,
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
	var driftHistory []ui.DriftPoint
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
		driftHistory = append(driftHistory, ui.DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return ui.EnvoyRunsFragmentData{Runs: runs, DriftHistory: driftHistory}, nil
}

func (ds *DataSource) EnvoyTraitsData(envoyID string) (ui.EnvoyTraitsFragmentData, error) {
	ts := ds.fleet.GetEnvoy(envoyID)
	traits := make(map[string]any)
	if ts != nil && ts.Traits != nil {
		for k, v := range ts.Traits {
			traits[k] = v
		}
	} else {
		// DB fallback: unmarshal nested JSON then flatten to dot-separated
		// string values so the template can safely render every value.
		ctx := context.Background()
		cf, err := ds.queries.GetLatestEnvoyTraits(ctx, envoyID)
		if err == nil {
			var nested map[string]any
			if err := json.Unmarshal([]byte(cf.Traits), &nested); err != nil {
				slog.Warn("malformed traits JSON", "envoy_id", envoyID, "error", err)
			} else {
				flat := make(map[string]string)
				flattenJSON("", nested, flat)
				for k, v := range flat {
					traits[k] = v
				}
			}
		}
	}
	return ui.EnvoyTraitsFragmentData{Traits: traits}, nil
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

// SecurityScanData aggregates security scan traits across all envoys.
func (ds *DataSource) SecurityScanData() ui.SecurityScanPageData {
	allEnvoys := ds.fleet.AllEnvoys()
	var data ui.SecurityScanPageData
	data.TotalHosts = int64(len(allEnvoys))

	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}
		if ts.Traits == nil {
			continue
		}

		// Check if this envoy has any security_scan data.
		hasScan := false
		row := ui.SecurityHostRow{ID: ts.ID, Hostname: ts.Hostname}

		if v, ok := ts.Traits["security_scan.trivy.critical"]; ok {
			hasScan = true
			n := parseInt64(v)
			row.TrivyCritical = n
			data.TrivyCritical += n
		}
		if v, ok := ts.Traits["security_scan.trivy.high"]; ok {
			n := parseInt64(v)
			row.TrivyHigh = n
			data.TrivyHigh += n
		}
		if v, ok := ts.Traits["security_scan.trivy.medium"]; ok {
			row.TrivyMedium = parseInt64(v)
			data.TrivyMedium += row.TrivyMedium
		}
		if v, ok := ts.Traits["security_scan.trivy.low"]; ok {
			row.TrivyLow = parseInt64(v)
			data.TrivyLow += row.TrivyLow
		}
		if v, ok := ts.Traits["security_scan.lynis.score"]; ok {
			hasScan = true
			row.LynisScore = parseInt64(v)
			data.LynisAvgScore += row.LynisScore
		}
		if v, ok := ts.Traits["security_scan.lynis.warnings"]; ok {
			row.LynisWarnings = parseInt64(v)
			data.LynisWarnings += row.LynisWarnings
		}
		if v, ok := ts.Traits["security_scan.lynis.suggestions"]; ok {
			row.LynisSuggestions = parseInt64(v)
		}

		// Rootkit: clean if both chkrootkit and rkhunter are clean (or absent).
		chkClean := ts.Traits["security_scan.chkrootkit.clean"] != "false"
		rkhClean := ts.Traits["security_scan.rkhunter.clean"] != "false"
		if _, ok := ts.Traits["security_scan.chkrootkit.clean"]; ok {
			hasScan = true
			row.ChkrootkitInfected = parseInt64(ts.Traits["security_scan.chkrootkit.infected"])
		}
		if _, ok := ts.Traits["security_scan.rkhunter.clean"]; ok {
			hasScan = true
			row.RkhunterWarnings = parseInt64(ts.Traits["security_scan.rkhunter.warnings"])
		}
		row.RootkitClean = chkClean && rkhClean
		if row.RootkitClean {
			data.RootkitClean++
		} else {
			data.RootkitWarning++
		}

		clamClean := ts.Traits["security_scan.clamav.clean"] != "false"
		if _, ok := ts.Traits["security_scan.clamav.clean"]; ok {
			hasScan = true
			row.ClamavInfected = parseInt64(ts.Traits["security_scan.clamav.infected"])
		}
		row.ClamavClean = clamClean
		if clamClean {
			data.ClamavClean++
		} else {
			data.ClamavInfected++
		}

		aideClean := ts.Traits["security_scan.aide.clean"] != "false"
		if _, ok := ts.Traits["security_scan.aide.clean"]; ok {
			hasScan = true
			row.AideAdded = parseInt64(ts.Traits["security_scan.aide.added"])
			row.AideRemoved = parseInt64(ts.Traits["security_scan.aide.removed"])
			row.AideChanged = parseInt64(ts.Traits["security_scan.aide.changed"])
		}
		row.AideClean = aideClean
		if aideClean {
			data.AideClean++
		} else {
			data.AideChanged++
		}

		if v, ok := ts.Traits["security_scan.trivy.last_scan"]; ok {
			row.LastScan = v
		}

		// Collect per-host CVEs (structured: each entry has cve_id, package, others).
		row.CVEs = collectCVEEntries(ts.Traits)

		if hasScan {
			data.ScannedHosts++
			data.PerHost = append(data.PerHost, row)
		}
	}

	// Average Lynis score.
	if data.ScannedHosts > 0 {
		data.LynisAvgScore = data.LynisAvgScore / data.ScannedHosts
	}

	// Deduplicate fleet-wide CVEs by cve_id.
	cveSet := make(map[string]bool)
	for _, row := range data.PerHost {
		for _, entry := range row.CVEs {
			if !cveSet[entry.CVEID] {
				cveSet[entry.CVEID] = true
				data.CVEs = append(data.CVEs, entry)
			}
		}
	}

	return data
}

// collectCVEEntries extracts structured CVE entries from flattened traits.
// Supports both new format (cves.N.cve_id/package/others) and legacy (cves.N = "CVE-...").
func collectCVEEntries(traits map[string]string) []ui.CVEEntry {
	prefixes := []string{
		"security_scan.trivy.cves.",
		"security_scan.debsecan.cves.",
		"security_scan.windows_updates.cves.",
	}

	// Collect by index: prefix+index -> fields.
	type partial struct {
		cveID   string
		pkg     string
		others  int64
	}
	byIndex := make(map[string]*partial) // "prefix.N" -> fields

	for k, v := range traits {
		for _, pfx := range prefixes {
			if !strings.HasPrefix(k, pfx) {
				continue
			}
			rest := k[len(pfx):]
			// New format: "0.cve_id", "0.package", "0.others"
			if idx, field, ok := strings.Cut(rest, "."); ok {
				key := pfx + idx
				p := byIndex[key]
				if p == nil {
					p = &partial{}
					byIndex[key] = p
				}
				switch field {
				case "cve_id":
					p.cveID = v
				case "package":
					p.pkg = v
				case "others":
					p.others = parseInt64(v)
				}
			} else {
				// Legacy format: "0" = "CVE-2024-..."
				byIndex[pfx+rest] = &partial{cveID: v}
			}
		}
	}

	entries := make([]ui.CVEEntry, 0, len(byIndex))
	for _, p := range byIndex {
		if p.cveID != "" {
			entries = append(entries, ui.CVEEntry{
				CVEID:   p.cveID,
				Package: p.pkg,
				Others:  p.others,
			})
		}
	}
	return entries
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

// SecurityScanTraits returns the flattened security_scan.* traits for a single envoy.
func (ds *DataSource) SecurityScanTraits(envoyID string) (map[string]string, bool) {
	ts, ok, _ := ds.fleet.GetEnvoyLite(envoyID)
	if !ok || ts.Traits == nil {
		return nil, false
	}
	result := make(map[string]string)
	for k, v := range ts.Traits {
		if strings.HasPrefix(k, "security_scan.") {
			result[k] = v
		}
	}
	if len(result) == 0 {
		return nil, false
	}
	return result, true
}
