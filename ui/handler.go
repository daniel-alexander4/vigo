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
	"github.com/isogov/isogov/server/compliance"
	"github.com/isogov/isogov/server/config"
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
	version      string
}

// NewHandler creates a UI handler wired to real database queries.
func NewHandler(queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, caFile, agentDir, version string) (*Handler, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}

	return &Handler{
		renderer:     renderer,
		dataSource:   &dbDataSource{queries: queries, configStore: configStore},
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
	Run     RunRow
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

// Clients renders the clients list page with optional status filter.
func (h *Handler) Clients(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.ClientsData(h.baseInterval, statusFilter)
	data.Version = h.version
	h.renderer.Render(w, "clients", data)
}

// ClientDetail renders the client detail page with runs, facts, and drift history.
func (h *Handler) ClientDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	clientID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid client ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.ClientDetailData(clientID, h.baseInterval)
	if err != nil {
		http.Error(w, "client not found", http.StatusNotFound)
		return
	}
	data.Version = h.version
	h.renderer.Render(w, "client-detail", data)
}

// Runs renders the runs list page with optional status filter.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.RunsData(statusFilter)
	data.Version = h.version
	h.renderer.Render(w, "runs", data)
}

// RunDetail renders the run detail page with per-module results.
func (h *Handler) RunDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	runID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid run ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.RunDetailData(runID)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	data.Version = h.version
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
	data.Version = h.version
	h.renderer.Render(w, "config", data)
}

// Plans renders the plan detail page with color-coded actions.
func (h *Handler) Plans(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	planID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid plan ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.PlanDetailData(planID)
	if err != nil {
		http.Error(w, "plan not found", http.StatusNotFound)
		return
	}
	data.Version = h.version
	h.renderer.Render(w, "plans", data)
}

// ClientsTable renders the clients table fragment for HTMX polling.
func (h *Handler) ClientsTable(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.ClientsData(h.baseInterval, statusFilter)
	h.renderer.RenderFragment(w, "clients", "clients-table", data)
}

// --- Production data source ---

type dbDataSource struct {
	queries     *db.Queries
	configStore *config.ConfigStore
}

// clientComplianceInfo computes compliance status for a client using its latest run.
func (ds *dbDataSource) clientComplianceInfo(ctx context.Context, c db.Client, baseInterval time.Duration) (status string, runInfo *compliance.RunInfo) {
	lastSeen := time.Time{}
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	latestRuns, err := ds.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
		ClientID: c.ID,
		Limit:    1,
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
	clients, err := ds.queries.ListClients(ctx)
	if err != nil {
		slog.Error("failed to list clients", "error", err)
		return DashboardPageData{PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"}}
	}

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

		status, _ := ds.clientComplianceInfo(ctx, c, baseInterval)
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

	allRuns, err := ds.queries.ListAllRuns(ctx, 10)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
	}

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

		status, _ := ds.clientComplianceInfo(ctx, c, baseInterval)
		statuses = append(statuses, status)

		if status != "compliant" {
			lastRunStatus := ""
			latestRuns, err := ds.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
				ClientID: c.ID,
				Limit:    1,
			})
			if err == nil && len(latestRuns) > 0 {
				lastRunStatus = latestRuns[0].Status
			}
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
	ctx := context.Background()
	clients, err := ds.queries.ListClients(ctx)
	if err != nil {
		slog.Error("failed to list clients", "error", err)
		return ClientsPageData{PageData: PageData{Title: "Clients", ActiveNav: "clients"}}
	}

	var rows []ClientRow
	for _, c := range clients {
		if c.Revoked {
			continue
		}
		status, _ := ds.clientComplianceInfo(ctx, c, baseInterval)

		if statusFilter != "" && status != statusFilter {
			continue
		}

		lastSeen := time.Time{}
		if c.LastSeen.Valid {
			lastSeen = c.LastSeen.Time
		}

		rows = append(rows, ClientRow{
			ID:         c.ID,
			Hostname:   c.Hostname,
			IPAddress:  c.IpAddress,
			EnrolledAt: c.EnrolledAt,
			LastSeen:   lastSeen,
			Status:     status,
		})
	}

	return ClientsPageData{
		PageData:     PageData{Title: "Clients", ActiveNav: "clients"},
		Clients:      rows,
		StatusFilter: statusFilter,
		ClientCount:  len(rows),
	}
}

func (ds *dbDataSource) ClientDetailData(clientID uuid.UUID, baseInterval time.Duration) (ClientDetailPageData, error) {
	ctx := context.Background()
	c, err := ds.queries.GetClientByID(ctx, clientID)
	if err != nil {
		return ClientDetailPageData{}, fmt.Errorf("client not found: %w", err)
	}

	lastSeen := time.Time{}
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	status, _ := ds.clientComplianceInfo(ctx, c, baseInterval)

	client := ClientRow{
		ID:         c.ID,
		Hostname:   c.Hostname,
		IPAddress:  c.IpAddress,
		EnrolledAt: c.EnrolledAt,
		LastSeen:   lastSeen,
		Status:     status,
	}

	// Fetch last 20 runs with results
	dbRuns, err := ds.queries.ListRunsByClient(ctx, db.ListRunsByClientParams{
		ClientID: clientID,
		Limit:    20,
	})
	if err != nil {
		slog.Error("failed to list runs for client", "error", err)
	}

	var runs []ClientDetailRunRow
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
				Changed:      res.Changed,
				ErrorMessage: errMsg,
				DurationMs:   res.DurationMs,
			})
		}

		runs = append(runs, ClientDetailRunRow{
			RunRow: RunRow{
				ID:           r.ID,
				Hostname:     c.Hostname,
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
	cf, err := ds.queries.GetLatestClientFacts(ctx, clientID)
	if err == nil {
		_ = json.Unmarshal(cf.Facts, &facts)
	}

	return ClientDetailPageData{
		PageData:     PageData{Title: "Client: " + c.Hostname, ActiveNav: "clients"},
		Client:       client,
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

	// Build client hostname lookup to avoid N+1
	clients, err := ds.queries.ListClients(ctx)
	if err != nil {
		slog.Error("failed to list clients for hostname lookup", "error", err)
	}
	clientMap := make(map[uuid.UUID]string, len(clients))
	for _, c := range clients {
		clientMap[c.ID] = c.Hostname
	}

	var rows []RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		rows = append(rows, RunRow{
			ID:           r.ID,
			Hostname:     clientMap[r.ClientID],
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

func (ds *dbDataSource) RunDetailData(runID uuid.UUID) (RunDetailPageData, error) {
	ctx := context.Background()
	r, err := ds.queries.GetRunByID(ctx, runID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("run not found: %w", err)
	}

	c, err := ds.queries.GetClientByID(ctx, r.ClientID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("client not found: %w", err)
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
			Changed:      res.Changed,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
	}

	return RunDetailPageData{
		PageData: PageData{Title: "Run Detail", ActiveNav: "runs"},
		Run: RunRow{
			ID:           r.ID,
			Hostname:     c.Hostname,
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
		PageData: PageData{Title: "Config: " + hostname, ActiveNav: "clients"},
		Hostname: hostname,
		RawYAML:  yamlStr,
	}, nil
}

func (ds *dbDataSource) PlanDetailData(planID uuid.UUID) (PlanDetailPageData, error) {
	ctx := context.Background()
	p, err := ds.queries.GetPlanByID(ctx, planID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("plan not found: %w", err)
	}

	c, err := ds.queries.GetClientByID(ctx, p.ClientID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("client not found: %w", err)
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
		PageData:    PageData{Title: "Plan Detail", ActiveNav: "clients"},
		PlanID:      p.ID,
		Hostname:    c.Hostname,
		CreatedAt:   p.CreatedAt,
		Results:     results,
		TotalCount:  len(results),
		CreateCount: createCount,
		UpdateCount: updateCount,
		DeleteCount: deleteCount,
		SkipCount:   skipCount,
	}, nil
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
