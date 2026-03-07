package ui

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/isogov/isogov/server/compliance"
	"github.com/isogov/isogov/server/db"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ComplianceData(baseInterval time.Duration) CompliancePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	ClientsData(baseInterval time.Duration, statusFilter string) ClientsPageData
	ClientDetailData(clientID uuid.UUID, baseInterval time.Duration) (ClientDetailPageData, error)
	RunsData(statusFilter string) RunsPageData
	RunDetailData(runID uuid.UUID) (RunDetailPageData, error)
	ConfigData(hostname string) (ConfigPageData, error)
	PlanDetailData(planID uuid.UUID) (PlanDetailPageData, error)
}

// Handler serves all UI pages.
type Handler struct {
	renderer     *Renderer
	dataSource   DataSource
	baseInterval time.Duration
	serverAddr   string
	caFile       string
	agentDir     string
	version string
}

// NewHandler creates a UI handler wired to real database queries.
func NewHandler(queries *db.Queries, baseInterval time.Duration, serverAddr, caFile, agentDir, version string) (*Handler, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}

	return &Handler{
		renderer:     renderer,
		dataSource:   &dbDataSource{queries: queries},
		baseInterval: baseInterval,
		serverAddr:   serverAddr,
		caFile:       caFile,
		agentDir:     agentDir,
		version:      version,
	}, nil
}

// RegisterRoutes mounts all UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Static assets
	staticFS, err := fs.Sub(Assets, "static")
	if err != nil {
		slog.Error("failed to create static sub-FS", "error", err)
		return
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	// Page routes
	mux.HandleFunc("GET /{$}", h.Dashboard)
	mux.HandleFunc("GET /clients", h.Clients)
	mux.HandleFunc("GET /clients/{id}", h.ClientDetail)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /compliance", h.Compliance)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /ui/fragments/clients-table", h.ClientsTable)
}

// --- Page data types ---

// PageData holds common fields for all page templates.
type PageData struct {
	Title     string
	ActiveNav string
	Version   string
}

// DashboardPageData holds data for the dashboard page.
type DashboardPageData struct {
	PageData
	TotalClients   int
	ActiveClients  int
	RecentFailures int
	DriftRate      float64
	RecentRuns     []RunRow
}

// RunRow is a single run displayed in a table.
type RunRow struct {
	ID           uuid.UUID
	Hostname     string
	StartedAt    time.Time
	Status       string
	ModuleCount  int32
	ChangedCount int32
	FailedCount  int32
}

// CompliancePageData holds data for the compliance page.
type CompliancePageData struct {
	PageData
	CompliancePct float64
	Total         int32
	Compliant     int32
	Drifted       int32
	Failing       int32
	Stale         int32
	Unknown       int32
	SparklineData []float64
	NonCompliant  []ComplianceRow
}

// ComplianceRow is a non-compliant client displayed in the table.
type ComplianceRow struct {
	Hostname      string
	Status        string
	LastSeen      time.Time
	LastRunStatus string
}

// DashboardStatsFragmentData holds data for the stats fragment (HTMX auto-refresh).
type DashboardStatsFragmentData struct {
	TotalClients   int
	ActiveClients  int
	RecentFailures int
	DriftRate      float64
}

// ClientRow is a single client displayed in the clients table.
type ClientRow struct {
	ID         uuid.UUID
	Hostname   string
	IPAddress  string
	EnrolledAt time.Time
	LastSeen   time.Time
	Status     string
}

// ClientsPageData holds data for the clients list page.
type ClientsPageData struct {
	PageData
	Clients      []ClientRow
	StatusFilter string
	ClientCount  int
}

// ClientDetailRunRow is a run with its per-module results for the client detail page.
type ClientDetailRunRow struct {
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
	DurationMs   int32
}

// ClientDetailPageData holds data for the client detail page.
type ClientDetailPageData struct {
	PageData
	Client       ClientRow
	Runs         []ClientDetailRunRow
	Facts        map[string]any
	DriftHistory []DriftPoint
}

// DriftPoint represents a single data point in the drift history timeline.
type DriftPoint struct {
	StartedAt    time.Time
	Status       string
	ChangedCount int32
	FailedCount  int32
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
	Run      RunRow
	Results  []RunResultRow
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
	PlanID      uuid.UUID
	Hostname    string
	CreatedAt   time.Time
	Results     []PlanResultRow
	TotalCount  int
	CreateCount int
	UpdateCount int
	DeleteCount int
	SkipCount   int
}

// --- Handlers ---

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	data.Version = h.version
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
	data.Version = h.version
	h.renderer.Render(w, "compliance", data)
}

// Clients renders a placeholder clients page (implemented in Plan 03).
func (h *Handler) Clients(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Clients",
		ActiveNav: "clients",
		Version:   h.version,
	})
}

// ClientDetail renders a placeholder client detail page.
func (h *Handler) ClientDetail(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Client Detail",
		ActiveNav: "clients",
		Version:   h.version,
	})
}

// Runs renders a placeholder runs page.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Runs",
		ActiveNav: "runs",
		Version:   h.version,
	})
}

// RunDetail renders a placeholder run detail page.
func (h *Handler) RunDetail(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Run Detail",
		ActiveNav: "runs",
		Version:   h.version,
	})
}

// Config renders a placeholder config viewer page.
func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Config Viewer",
		ActiveNav: "clients",
		Version:   h.version,
	})
}

// Plans renders a placeholder plans page.
func (h *Handler) Plans(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "coming-soon", PageData{
		Title:     "Plans",
		ActiveNav: "clients",
		Version:   h.version,
	})
}

// ClientsTable renders the clients table fragment for HTMX polling.
func (h *Handler) ClientsTable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<div class="coming-soon"><h2>Clients Table</h2><p>Coming soon</p></div>`))
}

// --- Production data source ---

type dbDataSource struct {
	queries *db.Queries
}

func (ds *dbDataSource) DashboardData(baseInterval time.Duration) DashboardPageData {
	ctx := context.Background()
	clients, err := ds.queries.ListClients(ctx)
	if err != nil {
		slog.Error("failed to list clients", "error", err)
		return DashboardPageData{PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"}}
	}

	// Compute stats
	totalClients := len(clients)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, c := range clients {
		if c.Revoked {
			continue
		}
		if c.LastSeen.Valid && time.Since(c.LastSeen.Time) < time.Hour {
			activeCount++
		}

		// Compute compliance status for this client
		lastSeen := time.Time{}
		if c.LastSeen.Valid {
			lastSeen = c.LastSeen.Time
		}

		latestRuns, err := ds.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
			ClientID: c.ID,
			Limit:    1,
		})
		if err != nil {
			continue
		}

		var runInfo *compliance.RunInfo
		if len(latestRuns) > 0 {
			runInfo = &compliance.RunInfo{
				Status:       latestRuns[0].Status,
				ChangedCount: latestRuns[0].ChangedCount,
			}
		}

		status := compliance.ComputeStatus(runInfo, lastSeen, baseInterval)
		switch status {
		case "drifted":
			driftedCount++
		case "failing":
			failureCount++
		}
	}

	driftRate := float64(0)
	if totalClients > 0 {
		driftRate = float64(driftedCount) / float64(totalClients) * 100
	}

	// Recent runs
	allRuns, err := ds.queries.ListAllRuns(ctx, 10)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
	}

	// Build hostname lookup
	clientMap := make(map[uuid.UUID]string, len(clients))
	for _, c := range clients {
		clientMap[c.ID] = c.Hostname
	}

	recentRuns := make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		recentRuns = append(recentRuns, RunRow{
			ID:           r.ID,
			Hostname:     clientMap[r.ClientID],
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return DashboardPageData{
		PageData:       PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalClients:   totalClients,
		ActiveClients:  activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
	}
}

func (ds *dbDataSource) ComplianceData(baseInterval time.Duration) CompliancePageData {
	ctx := context.Background()
	clients, err := ds.queries.ListClients(ctx)
	if err != nil {
		slog.Error("failed to list clients", "error", err)
		return CompliancePageData{PageData: PageData{Title: "Compliance", ActiveNav: "compliance"}}
	}

	var statuses []string
	var nonCompliant []ComplianceRow

	for _, c := range clients {
		if c.Revoked {
			continue
		}
		lastSeen := time.Time{}
		if c.LastSeen.Valid {
			lastSeen = c.LastSeen.Time
		}

		latestRuns, err := ds.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
			ClientID: c.ID,
			Limit:    1,
		})
		if err != nil {
			continue
		}

		var runInfo *compliance.RunInfo
		var lastRunStatus string
		if len(latestRuns) > 0 {
			runInfo = &compliance.RunInfo{
				Status:       latestRuns[0].Status,
				ChangedCount: latestRuns[0].ChangedCount,
			}
			lastRunStatus = latestRuns[0].Status
		}

		status := compliance.ComputeStatus(runInfo, lastSeen, baseInterval)
		statuses = append(statuses, status)

		if status != "compliant" {
			nonCompliant = append(nonCompliant, ComplianceRow{
				Hostname:      c.Hostname,
				Status:        status,
				LastSeen:      lastSeen,
				LastRunStatus: lastRunStatus,
			})
		}
	}

	summary := compliance.Summary(statuses)
	total := int32(len(statuses))
	compPct := float64(0)
	if total > 0 {
		compPct = float64(summary["compliant"]) / float64(total) * 100
	}

	// 30-day compliance history for sparkline
	history, err := ds.queries.ListComplianceHistory(ctx, 30)
	if err != nil {
		slog.Error("failed to list compliance history", "error", err)
	}

	sparkData := make([]float64, 0, len(history))
	// Reverse so oldest is first (ListComplianceHistory returns DESC)
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
		Compliant:     summary["compliant"],
		Drifted:       summary["drifted"],
		Failing:       summary["failing"],
		Stale:         summary["stale"],
		Unknown:       summary["unknown"],
		SparklineData: sparkData,
		NonCompliant:  nonCompliant,
	}
}

func (ds *dbDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalClients:   d.TotalClients,
		ActiveClients:  d.ActiveClients,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *dbDataSource) ClientsData(baseInterval time.Duration, statusFilter string) ClientsPageData {
	return ClientsPageData{PageData: PageData{Title: "Clients", ActiveNav: "clients"}}
}

func (ds *dbDataSource) ClientDetailData(clientID uuid.UUID, baseInterval time.Duration) (ClientDetailPageData, error) {
	return ClientDetailPageData{}, fmt.Errorf("not implemented")
}

func (ds *dbDataSource) RunsData(statusFilter string) RunsPageData {
	return RunsPageData{PageData: PageData{Title: "Runs", ActiveNav: "runs"}}
}

func (ds *dbDataSource) RunDetailData(runID uuid.UUID) (RunDetailPageData, error) {
	return RunDetailPageData{}, fmt.Errorf("not implemented")
}

func (ds *dbDataSource) ConfigData(hostname string) (ConfigPageData, error) {
	return ConfigPageData{}, fmt.Errorf("not implemented")
}

func (ds *dbDataSource) PlanDetailData(planID uuid.UUID) (PlanDetailPageData, error) {
	return PlanDetailPageData{}, fmt.Errorf("not implemented")
}

