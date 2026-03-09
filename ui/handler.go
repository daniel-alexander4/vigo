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
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ComplianceData(baseInterval time.Duration) CompliancePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	NodesData(baseInterval time.Duration, statusFilter string) NodesPageData
	NodeDetailData(nodeID uuid.UUID, baseInterval time.Duration) (NodeDetailPageData, error)
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
	mux.HandleFunc("GET /nodes", h.Nodes)
	mux.HandleFunc("GET /nodes/{id}", h.NodeDetail)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /compliance", h.Compliance)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /ui/fragments/nodes-table", h.NodesTable)
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
	TotalNodes     int
	ActiveNodes    int
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

// ComplianceRow is a non-compliant node displayed in the table.
type ComplianceRow struct {
	Hostname      string
	Status        string
	LastSeen      time.Time
	LastRunStatus string
}

// DashboardStatsFragmentData holds data for the stats fragment (HTMX auto-refresh).
type DashboardStatsFragmentData struct {
	TotalNodes     int
	ActiveNodes    int
	RecentFailures int
	DriftRate      float64
}

// NodeRow is a single node displayed in the nodes table.
type NodeRow struct {
	ID         uuid.UUID
	Hostname   string
	IPAddress  string
	EnrolledAt time.Time
	LastSeen   time.Time
	Status     string
}

// NodesPageData holds data for the nodes list page.
type NodesPageData struct {
	PageData
	Nodes        []NodeRow
	StatusFilter string
	NodeCount    int
}

// NodeDetailRunRow is a run with its per-module results for the node detail page.
type NodeDetailRunRow struct {
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

// NodeDetailPageData holds data for the node detail page.
type NodeDetailPageData struct {
	PageData
	Node         NodeRow
	Runs         []NodeDetailRunRow
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

// Nodes renders the nodes list page with optional status filter.
func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.NodesData(h.baseInterval, statusFilter)
	data.Version = h.version
	h.renderer.Render(w, "nodes", data)
}

// NodeDetail renders the node detail page with runs, facts, and drift history.
func (h *Handler) NodeDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	nodeID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.NodeDetailData(nodeID, h.baseInterval)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	data.Version = h.version
	h.renderer.Render(w, "node-detail", data)
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

// NodesTable renders the nodes table fragment for HTMX polling.
func (h *Handler) NodesTable(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.NodesData(h.baseInterval, statusFilter)
	h.renderer.RenderFragment(w, "nodes", "nodes-table", data)
}

// --- Production data source ---

type dbDataSource struct {
	queries     *db.Queries
	configStore *config.ConfigStore
}

// nodeComplianceInfo computes compliance status for a node using its latest run.
func (ds *dbDataSource) nodeComplianceInfo(ctx context.Context, c db.Node, baseInterval time.Duration) (status string, runInfo *compliance.RunInfo) {
	lastSeen := time.Time{}
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	latestRuns, err := ds.queries.ListRunsByNode(ctx, db.ListRunsByNodeParams{
		NodeID: c.ID,
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
	nodes, err := ds.queries.ListNodes(ctx)
	if err != nil {
		slog.Error("failed to list nodes", "error", err)
		return DashboardPageData{PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"}}
	}

	totalNodes := len(nodes)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, n := range nodes {
		if n.Revoked {
			continue
		}
		if n.LastSeen.Valid && time.Since(n.LastSeen.Time) < time.Hour {
			activeCount++
		}

		status, _ := ds.nodeComplianceInfo(ctx, n, baseInterval)
		switch status {
		case "drifted":
			driftedCount++
		case "failing":
			failureCount++
		}
	}

	driftRate := float64(0)
	if totalNodes > 0 {
		driftRate = float64(driftedCount) / float64(totalNodes) * 100
	}

	allRuns, err := ds.queries.ListAllRuns(ctx, 10)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
	}

	nodeMap := make(map[uuid.UUID]string, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n.Hostname
	}

	recentRuns := make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		recentRuns = append(recentRuns, RunRow{
			ID:           r.ID,
			Hostname:     nodeMap[r.NodeID],
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return DashboardPageData{
		PageData:       PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalNodes:     totalNodes,
		ActiveNodes:    activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
	}
}

func (ds *dbDataSource) ComplianceData(baseInterval time.Duration) CompliancePageData {
	ctx := context.Background()
	nodes, err := ds.queries.ListNodes(ctx)
	if err != nil {
		slog.Error("failed to list nodes", "error", err)
		return CompliancePageData{PageData: PageData{Title: "Compliance", ActiveNav: "compliance"}}
	}

	var statuses []string
	var nonCompliant []ComplianceRow

	for _, n := range nodes {
		if n.Revoked {
			continue
		}
		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		status, _ := ds.nodeComplianceInfo(ctx, n, baseInterval)
		statuses = append(statuses, status)

		if status != "compliant" {
			lastRunStatus := ""
			latestRuns, err := ds.queries.ListRunsByNode(ctx, db.ListRunsByNodeParams{
				NodeID: n.ID,
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
		TotalNodes:     d.TotalNodes,
		ActiveNodes:    d.ActiveNodes,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *dbDataSource) NodesData(baseInterval time.Duration, statusFilter string) NodesPageData {
	ctx := context.Background()
	nodes, err := ds.queries.ListNodes(ctx)
	if err != nil {
		slog.Error("failed to list nodes", "error", err)
		return NodesPageData{PageData: PageData{Title: "Nodes", ActiveNav: "nodes"}}
	}

	var rows []NodeRow
	for _, n := range nodes {
		if n.Revoked {
			continue
		}
		status, _ := ds.nodeComplianceInfo(ctx, n, baseInterval)

		if statusFilter != "" && status != statusFilter {
			continue
		}

		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		rows = append(rows, NodeRow{
			ID:         n.ID,
			Hostname:   n.Hostname,
			IPAddress:  n.IpAddress,
			EnrolledAt: n.EnrolledAt,
			LastSeen:   lastSeen,
			Status:     status,
		})
	}

	return NodesPageData{
		PageData:     PageData{Title: "Nodes", ActiveNav: "nodes"},
		Nodes:        rows,
		StatusFilter: statusFilter,
		NodeCount:    len(rows),
	}
}

func (ds *dbDataSource) NodeDetailData(nodeID uuid.UUID, baseInterval time.Duration) (NodeDetailPageData, error) {
	ctx := context.Background()
	node, err := ds.queries.GetNodeByID(ctx, nodeID)
	if err != nil {
		return NodeDetailPageData{}, fmt.Errorf("node not found: %w", err)
	}

	lastSeen := time.Time{}
	if node.LastSeen.Valid {
		lastSeen = node.LastSeen.Time
	}

	status, _ := ds.nodeComplianceInfo(ctx, node, baseInterval)

	nodeRow := NodeRow{
		ID:         node.ID,
		Hostname:   node.Hostname,
		IPAddress:  node.IpAddress,
		EnrolledAt: node.EnrolledAt,
		LastSeen:   lastSeen,
		Status:     status,
	}

	// Fetch last 20 runs with results
	dbRuns, err := ds.queries.ListRunsByNode(ctx, db.ListRunsByNodeParams{
		NodeID: nodeID,
		Limit:  20,
	})
	if err != nil {
		slog.Error("failed to list runs for node", "error", err)
	}

	var runs []NodeDetailRunRow
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

		runs = append(runs, NodeDetailRunRow{
			RunRow: RunRow{
				ID:           r.ID,
				Hostname:     node.Hostname,
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
	cf, err := ds.queries.GetLatestNodeNails(ctx, nodeID)
	if err == nil {
		_ = json.Unmarshal(cf.Nails, &facts)
	}

	return NodeDetailPageData{
		PageData:     PageData{Title: "Node: " + node.Hostname, ActiveNav: "nodes"},
		Node:         nodeRow,
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

	// Build node hostname lookup to avoid N+1
	nodes, err := ds.queries.ListNodes(ctx)
	if err != nil {
		slog.Error("failed to list nodes for hostname lookup", "error", err)
	}
	nodeMap := make(map[uuid.UUID]string, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n.Hostname
	}

	var rows []RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		rows = append(rows, RunRow{
			ID:           r.ID,
			Hostname:     nodeMap[r.NodeID],
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

	node, err := ds.queries.GetNodeByID(ctx, r.NodeID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("node not found: %w", err)
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
			Hostname:     node.Hostname,
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
		PageData: PageData{Title: "Config: " + hostname, ActiveNav: "nodes"},
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

	node, err := ds.queries.GetNodeByID(ctx, p.NodeID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("node not found: %w", err)
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
		PageData:    PageData{Title: "Plan Detail", ActiveNav: "nodes"},
		PlanID:      p.ID,
		Hostname:    node.Hostname,
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
