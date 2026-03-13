// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/ai"
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ComplianceData(baseInterval time.Duration) CompliancePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	ThrallsData(baseInterval time.Duration, statusFilter string) ThrallsPageData
	ThrallDetailData(thrallID string, baseInterval time.Duration) (ThrallDetailPageData, error)
	RunsData(statusFilter string) RunsPageData
	RunDetailData(runID string) (RunDetailPageData, error)
	ConfigData(hostname string) (ConfigPageData, error)
	PlanDetailData(planID string) (PlanDetailPageData, error)
}

// Handler serves all UI pages.
type Handler struct {
	renderer     *Renderer
	dataSource   DataSource
	queries      *db.Queries // direct DB access for task/workflow pages
	baseInterval time.Duration
	serverAddr   string
	caFile       string
	agentDir     string
	version      string
	assistant    *ai.Assistant
	hub          *federation.Hub
}

// NewHandler creates a UI handler wired to real database queries.
// The assistant parameter is optional — pass nil to disable the Ask page.
// The customDS parameter is optional — when provided, it replaces the default
// DB-backed DataSource (used by FleetIndex integration).
func NewHandler(queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, caFile, agentDir, version string, assistant *ai.Assistant, hub *federation.Hub, customDS DataSource) (*Handler, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}

	var ds DataSource
	if customDS != nil {
		ds = customDS
	} else {
		ds = &dbDataSource{queries: queries, configStore: configStore}
	}

	return &Handler{
		renderer:     renderer,
		dataSource:   ds,
		queries:      queries,
		baseInterval: baseInterval,
		serverAddr:   serverAddr,
		caFile:       caFile,
		agentDir:     agentDir,
		version:      version,
		assistant:    assistant,
		hub:          hub,
	}, nil
}

// securityHeaders wraps a handler to add standard security response headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes mounts all UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Static assets
	staticFS, err := fs.Sub(Assets, "static")
	if err != nil {
		slog.Error("failed to create static sub-FS", "error", err)
		return
	}
	mux.Handle("GET /static/", securityHeaders(http.StripPrefix("/static/", http.FileServerFS(staticFS))))

	// Page routes
	mux.HandleFunc("GET /{$}", h.Dashboard)
	mux.HandleFunc("GET /thralls", h.Thralls)
	mux.HandleFunc("GET /thralls/{id}", h.ThrallDetail)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /compliance", h.Compliance)
	mux.HandleFunc("GET /tasks", h.Tasks)
	mux.HandleFunc("GET /tasks/{id}", h.TaskDetail)
	mux.HandleFunc("GET /workflows", h.Workflows)
	mux.HandleFunc("GET /workflows/{id}", h.WorkflowDetail)
	mux.HandleFunc("GET /query", h.Query)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /ui/fragments/thralls-table", h.ThrallsTable)

	// Federation pages (only registered when running as hub)
	if h.hub != nil {
		mux.HandleFunc("GET /federation", h.Federation)
		mux.HandleFunc("GET /federation/spokes/{id}", h.SpokeDetail)
	}

	// AI chat page (only registered when assistant is available)
	if h.assistant != nil {
		mux.HandleFunc("GET /ask", h.AskPage)
		mux.HandleFunc("POST /ui/ask", h.AskStream)
	}
}

// --- Page data types ---

// PageData holds common fields for all page templates.
type PageData struct {
	Title             string
	ActiveNav         string
	Version           string
	AIEnabled         bool
	FederationEnabled bool
}

// DashboardPageData holds data for the dashboard page.
type DashboardPageData struct {
	PageData
	TotalThralls     int
	ActiveThralls    int
	RecentFailures int
	DriftRate      float64
	RecentRuns     []RunRow
}

// RunRow is a single run displayed in a table.
type RunRow struct {
	ID           string
	Hostname     string
	StartedAt    time.Time
	Status       string
	ModuleCount  int64
	ChangedCount int64
	FailedCount  int64
}

// CompliancePageData holds data for the compliance page.
type CompliancePageData struct {
	PageData
	CompliancePct float64
	Total         int64
	Compliant     int64
	Drifted       int64
	Failing       int64
	Stale         int64
	Unknown       int64
	SparklineData []float64
	NonCompliant  []ComplianceRow
}

// ComplianceRow is a non-compliant thrall displayed in the table.
type ComplianceRow struct {
	Hostname      string
	Status        string
	LastSeen      time.Time
	LastRunStatus string
}

// DashboardStatsFragmentData holds data for the stats fragment (HTMX auto-refresh).
type DashboardStatsFragmentData struct {
	TotalThralls     int
	ActiveThralls    int
	RecentFailures int
	DriftRate      float64
}

// ThrallRow is a single thrall displayed in the thralls table.
type ThrallRow struct {
	ID         string
	Hostname   string
	IPAddress  string
	EnrolledAt time.Time
	LastSeen   time.Time
	Status     string
}

// ThrallsPageData holds data for the thralls list page.
type ThrallsPageData struct {
	PageData
	Thralls        []ThrallRow
	StatusFilter string
	ThrallCount    int
}

// ThrallDetailRunRow is a run with its per-module results for the thrall detail page.
type ThrallDetailRunRow struct {
	RunRow
	Results []RunResultRow
}

// RunResultRow is a single run result (per-module) displayed in a table.
type RunResultRow struct {
	ModuleName   string
	ResourceType string
	ResourceName string
	ActionTaken  string
	Changed      bool
	ErrorMessage string
	DurationMs   int64
}

// ThrallDetailPageData holds data for the thrall detail page.
type ThrallDetailPageData struct {
	PageData
	Thrall         ThrallRow
	Runs         []ThrallDetailRunRow
	Facts        map[string]any
	DriftHistory []DriftPoint
}

// DriftPoint represents a single data point in the drift history timeline.
type DriftPoint struct {
	StartedAt    time.Time
	Status       string
	ChangedCount int64
	FailedCount  int64
}

// RunsPageData holds data for the runs list page.
type RunsPageData struct {
	PageData
	Runs         []RunRow
	StatusFilter string
	RunCount     int
}

// RunDetailPageData holds data for the run detail page.
type RunDetailPageData struct {
	PageData
	Run     RunRow
	ThrallID  string
	Results []RunResultRow
}

// ConfigPageData holds data for the config viewer page.
type ConfigPageData struct {
	PageData
	Hostname string
	RawYAML  string
}

// PlanResultRow is a single plan result displayed in the plans page.
type PlanResultRow struct {
	ModuleName    string
	ResourceType  string
	ResourceName  string
	PlannedAction string
	ActionColor   string
}

// PlanDetailPageData holds data for the plan detail page.
type PlanDetailPageData struct {
	PageData
	PlanID      string
	ThrallID      string
	Hostname    string
	CreatedAt   time.Time
	Results     []PlanResultRow
	TotalCount  int
	CreateCount int
	UpdateCount int
	DeleteCount int
	SkipCount   int
}

// setCommon applies version, AI-enabled, and federation flags to page data.
func (h *Handler) setCommon(pd *PageData) {
	pd.Version = h.version
	pd.AIEnabled = h.assistant != nil
	pd.FederationEnabled = h.hub != nil
}

// --- Handlers ---

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "dashboard", data)
}

// DashboardStats renders just the stat cards fragment for HTMX polling.
func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardStatsData(h.baseInterval)
	h.renderer.RenderFragment(w, "dashboard", "dashboard-stats", data)
}

// Compliance renders the compliance overview page.
func (h *Handler) Compliance(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.ComplianceData(h.baseInterval)
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "compliance", data)
}

// Thralls renders the thralls list page with optional status filter.
func (h *Handler) Thralls(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.ThrallsData(h.baseInterval, statusFilter)
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "thralls", data)
}

// ThrallDetail renders the thrall detail page with runs, facts, and drift history.
func (h *Handler) ThrallDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid thrall ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.ThrallDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "thrall not found", http.StatusNotFound)
		return
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "thrall-detail", data)
}

// Runs renders the runs list page with optional status filter.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.RunsData(statusFilter)
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "runs", data)
}

// RunDetail renders the run detail page with per-module results.
func (h *Handler) RunDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid run ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.RunDetailData(idStr)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "run-detail", data)
}

// Config renders the config viewer page with syntax-highlighted raw YAML.
func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	data, err := h.dataSource.ConfigData(hostname)
	if err != nil {
		http.Error(w, "config not found", http.StatusNotFound)
		return
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "config", data)
}

// Plans renders the plan detail page with color-coded actions.
func (h *Handler) Plans(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid plan ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.PlanDetailData(idStr)
	if err != nil {
		http.Error(w, "plan not found", http.StatusNotFound)
		return
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "plans", data)
}

// ThrallsTable renders the thralls table fragment for HTMX polling.
func (h *Handler) ThrallsTable(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.ThrallsData(h.baseInterval, statusFilter)
	h.renderer.RenderFragment(w, "thralls", "thralls-table", data)
}

// --- Production data source ---

type dbDataSource struct {
	queries     *db.Queries
	configStore *config.ConfigStore
}

// thrallComplianceInfo computes compliance status for a thrall using its latest run.
func (ds *dbDataSource) thrallComplianceInfo(ctx context.Context, c db.Thrall, baseInterval time.Duration) (status string, runInfo *compliance.RunInfo) {
	lastSeen := time.Time{}
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	latestRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
		ThrallID: c.ID,
		Limit:  1,
	})
	if err != nil {
		return compliance.ComputeStatus(nil, lastSeen, baseInterval), nil
	}

	if len(latestRuns) > 0 {
		runInfo = &compliance.RunInfo{
			Status:       latestRuns[0].Status,
			ChangedCount: latestRuns[0].ChangedCount,
		}
	}

	return compliance.ComputeStatus(runInfo, lastSeen, baseInterval), runInfo
}

func (ds *dbDataSource) DashboardData(baseInterval time.Duration) DashboardPageData {
	ctx := context.Background()
	thralls, err := ds.queries.ListThralls(ctx)
	if err != nil {
		slog.Error("failed to list thralls", "error", err)
		return DashboardPageData{PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"}}
	}

	totalThralls := len(thralls)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, n := range thralls {
		if n.Revoked != 0 {
			continue
		}
		if n.LastSeen.Valid && time.Since(n.LastSeen.Time) < time.Hour {
			activeCount++
		}

		status, _ := ds.thrallComplianceInfo(ctx, n, baseInterval)
		switch status {
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

	allRuns, err := ds.queries.ListAllRuns(ctx, 10)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
	}

	thrallMap := make(map[string]string, len(thralls))
	for _, n := range thralls {
		thrallMap[n.ID] = n.Hostname
	}

	recentRuns := make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		recentRuns = append(recentRuns, RunRow{
			ID:           r.ID,
			Hostname:     thrallMap[r.ThrallID],
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return DashboardPageData{
		PageData:       PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalThralls:     totalThralls,
		ActiveThralls:    activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
	}
}

func (ds *dbDataSource) ComplianceData(baseInterval time.Duration) CompliancePageData {
	ctx := context.Background()
	thralls, err := ds.queries.ListThralls(ctx)
	if err != nil {
		slog.Error("failed to list thralls", "error", err)
		return CompliancePageData{PageData: PageData{Title: "Compliance", ActiveNav: "compliance"}}
	}

	var statuses []string
	var nonCompliant []ComplianceRow

	for _, n := range thralls {
		if n.Revoked != 0 {
			continue
		}
		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		status, _ := ds.thrallComplianceInfo(ctx, n, baseInterval)
		statuses = append(statuses, status)

		if status != "compliant" {
			lastRunStatus := ""
			latestRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
				ThrallID: n.ID,
				Limit:  1,
			})
			if err == nil && len(latestRuns) > 0 {
				lastRunStatus = latestRuns[0].Status
			}
			nonCompliant = append(nonCompliant, ComplianceRow{
				Hostname:      n.Hostname,
				Status:        status,
				LastSeen:      lastSeen,
				LastRunStatus: lastRunStatus,
			})
		}
	}

	summary := compliance.Summary(statuses)
	total := int64(len(statuses))
	compPct := float64(0)
	if total > 0 {
		compPct = float64(summary["compliant"]) / float64(total) * 100
	}

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

	return CompliancePageData{
		PageData:      PageData{Title: "Compliance", ActiveNav: "compliance"},
		CompliancePct: compPct,
		Total:         total,
		Compliant:     int64(summary["compliant"]),
		Drifted:       int64(summary["drifted"]),
		Failing:       int64(summary["failing"]),
		Stale:         int64(summary["stale"]),
		Unknown:       int64(summary["unknown"]),
		SparklineData: sparkData,
		NonCompliant:  nonCompliant,
	}
}

func (ds *dbDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalThralls:     d.TotalThralls,
		ActiveThralls:    d.ActiveThralls,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *dbDataSource) ThrallsData(baseInterval time.Duration, statusFilter string) ThrallsPageData {
	ctx := context.Background()
	thralls, err := ds.queries.ListThralls(ctx)
	if err != nil {
		slog.Error("failed to list thralls", "error", err)
		return ThrallsPageData{PageData: PageData{Title: "Thralls", ActiveNav: "thralls"}}
	}

	var rows []ThrallRow
	for _, n := range thralls {
		if n.Revoked != 0 {
			continue
		}
		status, _ := ds.thrallComplianceInfo(ctx, n, baseInterval)

		if statusFilter != "" && status != statusFilter {
			continue
		}

		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		rows = append(rows, ThrallRow{
			ID:         n.ID,
			Hostname:   n.Hostname,
			IPAddress:  n.IpAddress,
			EnrolledAt: n.EnrolledAt,
			LastSeen:   lastSeen,
			Status:     status,
		})
	}

	return ThrallsPageData{
		PageData:     PageData{Title: "Thralls", ActiveNav: "thralls"},
		Thralls:        rows,
		StatusFilter: statusFilter,
		ThrallCount:    len(rows),
	}
}

func (ds *dbDataSource) ThrallDetailData(thrallID string, baseInterval time.Duration) (ThrallDetailPageData, error) {
	ctx := context.Background()
	thrall, err := ds.queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		return ThrallDetailPageData{}, fmt.Errorf("thrall not found: %w", err)
	}

	lastSeen := time.Time{}
	if thrall.LastSeen.Valid {
		lastSeen = thrall.LastSeen.Time
	}

	status, _ := ds.thrallComplianceInfo(ctx, thrall, baseInterval)

	thrallRow := ThrallRow{
		ID:         thrall.ID,
		Hostname:   thrall.Hostname,
		IPAddress:  thrall.IpAddress,
		EnrolledAt: thrall.EnrolledAt,
		LastSeen:   lastSeen,
		Status:     status,
	}

	// Fetch last 20 runs with results
	dbRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
		ThrallID: thrallID,
		Limit:  20,
	})
	if err != nil {
		slog.Error("failed to list runs for thrall", "error", err)
	}

	var runs []ThrallDetailRunRow
	var driftHistory []DriftPoint
	for _, r := range dbRuns {
		dbResults, err := ds.queries.ListRunResults(ctx, r.ID)
		if err != nil {
			slog.Error("failed to list run results", "run_id", r.ID, "error", err)
		}

		var results []RunResultRow
		for _, res := range dbResults {
			errMsg := ""
			if res.ErrorMessage.Valid {
				errMsg = res.ErrorMessage.String
			}
			results = append(results, RunResultRow{
				ModuleName:   res.ModuleName,
				ResourceType: res.ResourceType,
				ResourceName: res.ResourceName,
				ActionTaken:  res.ActionTaken,
				Changed:      res.Changed != 0,
				ErrorMessage: errMsg,
				DurationMs:   res.DurationMs,
			})
		}

		runs = append(runs, ThrallDetailRunRow{
			RunRow: RunRow{
				ID:           r.ID,
				Hostname:     thrall.Hostname,
				StartedAt:    r.StartedAt,
				Status:       r.Status,
				ModuleCount:  r.ModuleCount,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
			},
			Results: results,
		})

		driftHistory = append(driftHistory, DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	// Fetch latest facts
	facts := make(map[string]any)
	cf, err := ds.queries.GetLatestThrallTraits(ctx, thrallID)
	if err == nil {
		_ = json.Unmarshal([]byte(cf.Traits), &facts)
	}

	return ThrallDetailPageData{
		PageData:     PageData{Title: "Thrall: " + thrall.Hostname, ActiveNav: "thralls"},
		Thrall:         thrallRow,
		Runs:         runs,
		Facts:        facts,
		DriftHistory: driftHistory,
	}, nil
}

func (ds *dbDataSource) RunsData(statusFilter string) RunsPageData {
	ctx := context.Background()
	allRuns, err := ds.queries.ListAllRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return RunsPageData{PageData: PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	// Build thrall hostname lookup to avoid N+1
	thralls, err := ds.queries.ListThralls(ctx)
	if err != nil {
		slog.Error("failed to list thralls for hostname lookup", "error", err)
	}
	thrallMap := make(map[string]string, len(thralls))
	for _, n := range thralls {
		thrallMap[n.ID] = n.Hostname
	}

	var rows []RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		rows = append(rows, RunRow{
			ID:           r.ID,
			Hostname:     thrallMap[r.ThrallID],
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return RunsPageData{
		PageData:     PageData{Title: "Runs", ActiveNav: "runs"},
		Runs:         rows,
		StatusFilter: statusFilter,
		RunCount:     len(rows),
	}
}

func (ds *dbDataSource) RunDetailData(runID string) (RunDetailPageData, error) {
	ctx := context.Background()
	r, err := ds.queries.GetRunByID(ctx, runID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("run not found: %w", err)
	}

	thrall, err := ds.queries.GetThrallByID(ctx, r.ThrallID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("thrall not found: %w", err)
	}

	dbResults, err := ds.queries.ListRunResults(ctx, runID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("listing run results: %w", err)
	}

	var results []RunResultRow
	for _, res := range dbResults {
		errMsg := ""
		if res.ErrorMessage.Valid {
			errMsg = res.ErrorMessage.String
		}
		results = append(results, RunResultRow{
			ModuleName:   res.ModuleName,
			ResourceType: res.ResourceType,
			ResourceName: res.ResourceName,
			ActionTaken:  res.ActionTaken,
			Changed:      res.Changed != 0,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
	}

	return RunDetailPageData{
		PageData: PageData{Title: "Run Detail", ActiveNav: "runs"},
		ThrallID:   thrall.ID,
		Run: RunRow{
			ID:           r.ID,
			Hostname:     thrall.Hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		},
		Results: results,
	}, nil
}

func (ds *dbDataSource) ConfigData(hostname string) (ConfigPageData, error) {
	if ds.configStore == nil {
		return ConfigPageData{}, fmt.Errorf("no config store")
	}

	yamlStr, err := ds.configStore.EffectiveConfigYAML(hostname)
	if err != nil {
		return ConfigPageData{}, fmt.Errorf("config not found for %s: %w", hostname, err)
	}

	return ConfigPageData{
		PageData: PageData{Title: "Config: " + hostname, ActiveNav: "thralls"},
		Hostname: hostname,
		RawYAML:  yamlStr,
	}, nil
}

func (ds *dbDataSource) PlanDetailData(planID string) (PlanDetailPageData, error) {
	ctx := context.Background()
	p, err := ds.queries.GetPlanByID(ctx, planID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("plan not found: %w", err)
	}

	thrall, err := ds.queries.GetThrallByID(ctx, p.ThrallID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("thrall not found: %w", err)
	}

	dbResults, err := ds.queries.ListPlanResults(ctx, planID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("listing plan results: %w", err)
	}

	var results []PlanResultRow
	createCount, updateCount, deleteCount, skipCount := 0, 0, 0, 0
	for _, r := range dbResults {
		color := planActionColor(r.PlannedAction)
		results = append(results, PlanResultRow{
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

	return PlanDetailPageData{
		PageData:    PageData{Title: "Plan Detail", ActiveNav: "thralls"},
		PlanID:      p.ID,
		ThrallID:      thrall.ID,
		Hostname:    thrall.Hostname,
		CreatedAt:   p.CreatedAt,
		Results:     results,
		TotalCount:  len(results),
		CreateCount: createCount,
		UpdateCount: updateCount,
		DeleteCount: deleteCount,
		SkipCount:   skipCount,
	}, nil
}

// --- Task and Workflow page types ---

// TaskRow is a single task run displayed in the tasks list.
type TaskRow struct {
	ID           string
	Command      string
	Target       string
	Operator     string
	Status       string
	TotalTargets int64
	BatchSize    string
	MaxFailures  string
	CreatedAt    time.Time
}

// TasksPageData holds data for the tasks list page.
type TasksPageData struct {
	PageData
	Tasks        []TaskRow
	StatusFilter string
	TaskCount    int
}

// TaskTargetRow is a per-thrall result in the task detail page.
type TaskTargetRow struct {
	ID         string
	Hostname   string
	Status     string
	ExitCode   string
	Stdout     string
	Stderr     string
	DurationMs string
	ErrorMsg   string
}

// TaskDetailPageData holds data for the task detail page.
type TaskDetailPageData struct {
	PageData
	Task      TaskRow
	Targets   []TaskTargetRow
	IsRunning bool
}

// WorkflowRow is a single workflow run displayed in the workflows list.
type WorkflowRow struct {
	ID            string
	WorkflowName  string
	Status        string
	CurrentStep   int64
	TotalSteps    int64
	Operator      string
	FailureReason string
	CreatedAt     time.Time
}

// WorkflowsPageData holds data for the workflows list page.
type WorkflowsPageData struct {
	PageData
	Workflows      []WorkflowRow
	StatusFilter   string
	WorkflowCount  int
}

// WorkflowStepRow is a step result in the workflow detail page.
type WorkflowStepRow struct {
	StepIndex  int64
	StepName   string
	TaskRunID  string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
}

// WorkflowDetailPageData holds data for the workflow detail page.
type WorkflowDetailPageData struct {
	PageData
	Workflow       WorkflowRow
	Steps          []WorkflowStepRow
	CompletedSteps int
	FailedSteps    int
	IsRunning      bool
}

// QueryPageData holds data for the live query page.
type QueryPageData struct {
	PageData
}

// Tasks renders the task history page.
func (h *Handler) Tasks(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	ctx := r.Context()

	taskRuns, err := h.queries.ListTaskRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list task runs", "error", err)
	}

	var rows []TaskRow
	for _, t := range taskRuns {
		if statusFilter != "" && t.Status != statusFilter {
			continue
		}
		rows = append(rows, taskRowFromDB(t))
	}

	data := TasksPageData{
		PageData:     PageData{Title: "Tasks", ActiveNav: "tasks"},
		Tasks:        rows,
		StatusFilter: statusFilter,
		TaskCount:    len(rows),
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "tasks", data)
}

// TaskDetail renders the task detail page with per-thrall results.
func (h *Handler) TaskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	task, err := h.queries.GetTaskRun(ctx, id)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	targets, err := h.queries.ListTaskRunTargets(ctx, id)
	if err != nil {
		slog.Error("failed to list task run targets", "error", err)
	}

	var tgtRows []TaskTargetRow
	for _, t := range targets {
		row := TaskTargetRow{
			ID:       t.ID,
			Hostname: t.Hostname,
			Status:   t.Status,
		}
		if t.ExitCode.Valid {
			row.ExitCode = fmt.Sprintf("%d", t.ExitCode.Int64)
		}
		if t.Stdout.Valid {
			row.Stdout = t.Stdout.String
		}
		if t.Stderr.Valid {
			row.Stderr = t.Stderr.String
		}
		if t.DurationMs.Valid {
			row.DurationMs = fmt.Sprintf("%d", t.DurationMs.Int64)
		}
		if t.ErrorMsg.Valid {
			row.ErrorMsg = t.ErrorMsg.String
		}
		tgtRows = append(tgtRows, row)
	}

	data := TaskDetailPageData{
		PageData:  PageData{Title: "Task Detail", ActiveNav: "tasks"},
		Task:      taskRowFromDB(task),
		Targets:   tgtRows,
		IsRunning: task.Status == "running" || task.Status == "pending",
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "task-detail", data)
}

// Workflows renders the workflow list page.
func (h *Handler) Workflows(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	ctx := r.Context()

	runs, err := h.queries.ListWorkflowRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list workflow runs", "error", err)
	}

	var rows []WorkflowRow
	for _, wf := range runs {
		if statusFilter != "" && wf.Status != statusFilter {
			continue
		}
		rows = append(rows, workflowRowFromDB(wf))
	}

	data := WorkflowsPageData{
		PageData:      PageData{Title: "Workflows", ActiveNav: "workflows"},
		Workflows:     rows,
		StatusFilter:  statusFilter,
		WorkflowCount: len(rows),
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "workflows", data)
}

// WorkflowDetail renders the workflow detail page with step-level results.
func (h *Handler) WorkflowDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	run, err := h.queries.GetWorkflowRun(ctx, id)
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	steps, err := h.queries.ListWorkflowStepResults(ctx, id)
	if err != nil {
		slog.Error("failed to list workflow step results", "error", err)
	}

	var stepRows []WorkflowStepRow
	completedSteps, failedSteps := 0, 0
	for _, s := range steps {
		row := WorkflowStepRow{
			StepIndex: s.StepIndex,
			StepName:  s.StepName,
			Status:    s.Status,
		}
		if s.TaskRunID.Valid {
			row.TaskRunID = s.TaskRunID.String
		}
		if s.StartedAt.Valid {
			row.StartedAt = s.StartedAt.Time
		}
		if s.FinishedAt.Valid {
			row.FinishedAt = s.FinishedAt.Time
		}
		stepRows = append(stepRows, row)

		switch s.Status {
		case "complete":
			completedSteps++
		case "failed":
			failedSteps++
		}
	}

	wfRow := workflowRowFromDB(run)
	data := WorkflowDetailPageData{
		PageData:       PageData{Title: "Workflow: " + wfRow.WorkflowName, ActiveNav: "workflows"},
		Workflow:       wfRow,
		Steps:          stepRows,
		CompletedSteps: completedSteps,
		FailedSteps:    failedSteps,
		IsRunning:      run.Status == "running" || run.Status == "pending",
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "workflow-detail", data)
}

// Query renders the live query page.
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	data := QueryPageData{
		PageData: PageData{Title: "Live Query", ActiveNav: "query"},
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "query", data)
}

// taskRowFromDB converts a DB TaskRun to a TaskRow.
func taskRowFromDB(t db.TaskRun) TaskRow {
	row := TaskRow{
		ID:           t.ID,
		Command:      t.Command,
		Target:       t.TargetGlob,
		Operator:     t.Operator,
		Status:       t.Status,
		TotalTargets: t.TotalTargets,
		CreatedAt:    t.CreatedAt,
	}
	if t.BatchSize.Valid {
		row.BatchSize = t.BatchSize.String
	}
	if t.MaxFailures.Valid {
		row.MaxFailures = t.MaxFailures.String
	}
	return row
}

// workflowRowFromDB converts a DB WorkflowRun to a WorkflowRow.
func workflowRowFromDB(w db.WorkflowRun) WorkflowRow {
	row := WorkflowRow{
		ID:           w.ID,
		WorkflowName: w.WorkflowName,
		Status:       w.Status,
		CurrentStep:  w.CurrentStep,
		TotalSteps:   w.TotalSteps,
		Operator:     w.Operator,
		CreatedAt:    w.CreatedAt,
	}
	if w.FailureReason.Valid {
		row.FailureReason = w.FailureReason.String
	}
	return row
}

// FederationSpokeRow represents a spoke in the federation UI table.
type FederationSpokeRow struct {
	ID            string
	Addr          string
	Patterns      []string
	Healthy       bool
	ThrallCount   int32
	Compliant     int32
	Drifted       int32
	Failing       int32
	Stale         int32
	Unknown       int32
	CompliancePct float32
	LatencyMs     int64
	Error         string
}

// FederationPageData holds data for the federation page.
type FederationPageData struct {
	PageData
	SpokeCount     int
	HealthyCount   int
	TotalThralls   int32
	TotalCompliant int32
	TotalDrifted   int32
	TotalFailing   int32
	CompliancePct  float32
	Spokes         []FederationSpokeRow
}

// Federation renders the federation hub status page.
func (h *Handler) Federation(w http.ResponseWriter, r *http.Request) {
	statuses := h.hub.SpokeStatuses(r.Context())

	var spokes []FederationSpokeRow
	healthyCount := 0
	var totalThralls, totalCompliant, totalDrifted, totalFailing int32
	for _, s := range statuses {
		spokes = append(spokes, FederationSpokeRow{
			ID:            s.ID,
			Addr:          s.Addr,
			Patterns:      s.Patterns,
			Healthy:       s.Healthy,
			ThrallCount:   s.ThrallCount,
			Compliant:     s.Compliant,
			Drifted:       s.Drifted,
			Failing:       s.Failing,
			Stale:         s.Stale,
			Unknown:       s.Unknown,
			CompliancePct: s.CompliancePct,
			LatencyMs:     s.LatencyMs,
			Error:         s.Error,
		})
		if s.Healthy {
			healthyCount++
		}
		totalThralls += s.ThrallCount
		totalCompliant += s.Compliant
		totalDrifted += s.Drifted
		totalFailing += s.Failing
	}

	var compPct float32
	if totalThralls > 0 {
		compPct = float32(totalCompliant) / float32(totalThralls) * 100
	}

	data := FederationPageData{
		PageData:       PageData{Title: "Federation", ActiveNav: "federation"},
		SpokeCount:     len(spokes),
		HealthyCount:   healthyCount,
		TotalThralls:   totalThralls,
		TotalCompliant: totalCompliant,
		TotalDrifted:   totalDrifted,
		TotalFailing:   totalFailing,
		CompliancePct:  compPct,
		Spokes:         spokes,
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "federation", data)
}

// SpokeDetailPageData holds data for the spoke detail page.
type SpokeDetailPageData struct {
	PageData
	SpokeID       string
	Addr          string
	Healthy       bool
	ThrallCount   int32
	Compliant     int32
	Drifted       int32
	Failing       int32
	Stale         int32
	Unknown       int32
	CompliancePct float32
	LatencyMs     int64
	Thralls       []SpokeDetailThrallRow
	Runs          []SpokeDetailRunRow
}

// SpokeDetailThrallRow is a thrall on a spoke.
type SpokeDetailThrallRow struct {
	ID        string
	Hostname  string
	IPAddress string
	LastSeen  string
	Revoked   bool
}

// SpokeDetailRunRow is a run on a spoke.
type SpokeDetailRunRow struct {
	ID           string
	ThrallID     string
	Status       string
	ModuleCount  int32
	ChangedCount int32
	FailedCount  int32
	StartedAt    string
}

// SpokeDetail renders the spoke detail page with thralls and runs.
func (h *Handler) SpokeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	statuses := h.hub.SpokeStatuses(ctx)
	var spoke *federation.SpokeStatus
	for i := range statuses {
		if statuses[i].ID == id {
			spoke = &statuses[i]
			break
		}
	}
	if spoke == nil {
		http.Error(w, "spoke not found", http.StatusNotFound)
		return
	}

	// Fetch thralls and runs from this spoke.
	thralls, _ := h.hub.SpokeListThralls(ctx, id)
	runs, _ := h.hub.SpokeListRuns(ctx, id, 20)

	var thrallRows []SpokeDetailThrallRow
	for _, t := range thralls {
		row := SpokeDetailThrallRow{
			ID:        t.Id,
			Hostname:  t.Hostname,
			IPAddress: t.IpAddress,
			Revoked:   t.Revoked,
		}
		if t.LastSeen != nil {
			row.LastSeen = t.LastSeen.AsTime().Format("2006-01-02 15:04:05")
		}
		thrallRows = append(thrallRows, row)
	}

	var runRows []SpokeDetailRunRow
	for _, run := range runs {
		row := SpokeDetailRunRow{
			ID:           run.Id,
			ThrallID:     run.ThrallId,
			Status:       run.Status,
			ModuleCount:  run.ModuleCount,
			ChangedCount: run.ChangedCount,
			FailedCount:  run.FailedCount,
		}
		if run.StartedAt != nil {
			row.StartedAt = run.StartedAt.AsTime().Format("2006-01-02 15:04:05")
		}
		runRows = append(runRows, row)
	}

	data := SpokeDetailPageData{
		PageData:      PageData{Title: "Spoke: " + id, ActiveNav: "federation"},
		SpokeID:       spoke.ID,
		Addr:          spoke.Addr,
		Healthy:       spoke.Healthy,
		ThrallCount:   spoke.ThrallCount,
		Compliant:     spoke.Compliant,
		Drifted:       spoke.Drifted,
		Failing:       spoke.Failing,
		Stale:         spoke.Stale,
		Unknown:       spoke.Unknown,
		CompliancePct: spoke.CompliancePct,
		LatencyMs:     spoke.LatencyMs,
		Thralls:       thrallRows,
		Runs:          runRows,
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "spoke-detail", data)
}

// AskDriftRow represents a package drift entry for the ask/security page.
type AskDriftRow struct {
	Hostname         string
	PackageName      string
	InstalledVersion string
	DesiredVersion   string
}

// AskPage renders the AI compliance & security dashboard.
func (h *Handler) AskPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	compData := h.dataSource.ComplianceData(h.baseInterval)

	var driftRows []AskDriftRow
	if h.queries != nil {
		rows, err := h.queries.ListPackageDrift(ctx)
		if err == nil {
			for _, row := range rows {
				installed := ""
				if row.InstalledVersion.Valid {
					installed = row.InstalledVersion.String
				}
				desired := ""
				if row.DesiredVersion.Valid {
					desired = row.DesiredVersion.String
				}
				driftRows = append(driftRows, AskDriftRow{
					Hostname:         row.Hostname,
					PackageName:      row.PackageName,
					InstalledVersion: installed,
					DesiredVersion:   desired,
				})
			}
		}
	}

	data := struct {
		PageData
		CompliancePct float64
		Total         int64
		Compliant     int64
		Drifted       int64
		Failing       int64
		Stale         int64
		Unknown       int64
		DriftCount    int
		DriftRows     []AskDriftRow
	}{
		PageData:      PageData{Title: "Compliance & Security", ActiveNav: "ask"},
		CompliancePct: compData.CompliancePct,
		Total:         compData.Total,
		Compliant:     compData.Compliant,
		Drifted:       compData.Drifted,
		Failing:       compData.Failing,
		Stale:         compData.Stale,
		Unknown:       compData.Unknown,
		DriftCount:    len(driftRows),
		DriftRows:     driftRows,
	}
	h.setCommon(&data.PageData)
	h.renderer.Render(w, "ask", data)
}

// AskStream handles POST /ui/ask — proxies to the AI assistant with server-side markdown rendering.
func (h *Handler) AskStream(w http.ResponseWriter, r *http.Request) {
	if h.assistant == nil {
		http.Error(w, "AI assistant not enabled", http.StatusNotFound)
		return
	}

	var req struct {
		Question string `json:"question"`
		History  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendSSE := func(data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	// Flush headers and send initial event immediately so the browser
	// knows the SSE stream is open before the (potentially slow) LLM call.
	sendSSE(map[string]any{"type": "status", "status": "processing"})

	aiReq := ai.AskRequest{Question: req.Question}
	for _, m := range req.History {
		aiReq.History = append(aiReq.History, ai.HistoryMessage{Role: m.Role, Content: m.Content})
	}

	var rawAnswer string

	err := h.assistant.AskStream(r.Context(), aiReq, func(event ai.SSEEvent) {
		switch event.Type {
		case "delta":
			rawAnswer = event.Text
			sendSSE(map[string]any{"type": "delta", "html": RenderMarkdown(event.Text)})
		case "status":
			sendSSE(map[string]any{"type": "status", "tools": event.ToolsCalled})
		case "done":
			sendSSE(map[string]any{
				"type":        "done",
				"raw_answer":  rawAnswer,
				"tools_used":  event.ToolsUsed,
				"tokens_used": event.TokensUsed,
			})
		case "error":
			sendSSE(map[string]any{"type": "error", "text": event.Text})
		}
	})
	if err != nil {
		sendSSE(map[string]any{"type": "error", "text": err.Error()})
	}
}

// planActionColor maps a plan action to a badge CSS class.
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
