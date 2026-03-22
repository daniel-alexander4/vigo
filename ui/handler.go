// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/daniel-alexander4/vigo/server/ai"
	"github.com/daniel-alexander4/vigo/server/audit"
	"github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/peer"
	"github.com/daniel-alexander4/vigo/server/spanner"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ConvergenceData(baseInterval time.Duration) ConvergencePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) EnvoysPageData
	EnvoyDetailData(envoyID string, baseInterval time.Duration) (EnvoyDetailPageData, error)
	EnvoyRunsData(envoyID string) (EnvoyRunsFragmentData, error)
	EnvoyTraitsData(envoyID string) (EnvoyTraitsFragmentData, error)
	RunsData(statusFilter string) RunsPageData
	RunDetailData(runID string) (RunDetailPageData, error)
	ConfigData(hostname string) (ConfigPageData, error)
	PlanDetailData(planID string) (PlanDetailPageData, error)
	SecurityScanData() SecurityScanPageData
	SecurityScanTraits(envoyID string) (map[string]string, bool)
	RiskData(baseInterval time.Duration) RiskSummaryData
}

// ReportBuilder builds compliance reports for HTMX fragment rendering.
// Set via SetReportBuilder after construction to avoid import cycles.
type ReportBuilder struct {
	// Executive returns the executive summary report data (any is *api.ExecutiveReport).
	Executive func(r *http.Request) (any, error)
	// Framework returns a per-framework report (any is *api.ComplianceReport).
	Framework func(r *http.Request, slug string) (any, error)
}

// Handler serves all UI pages.
type Handler struct {
	renderer      *Renderer
	dataSource    DataSource
	queries       *db.Queries // direct DB access for task/workflow pages
	configStore   *config.ConfigStore
	baseInterval  time.Duration
	serverAddr    string
	caFile        string
	agentDir      string
	version       string
	assistant     *ai.Assistant
	hub           *spanner.Hub
	docsDir       string
	docsNav       []DocsNavSection
	enforcer      *license.Enforcer
	peerSvc       *peer.Service
	authEnabled   bool
	authMethod    string
	reportBuilder  *ReportBuilder
	integrationsCfg config.IntegrationsSection
	grcCfg          config.GRCSection
	branding      config.BrandingSection
	cveSearcher   CVESearcher
}

// CVESearcher provides CVE impact search across the fleet.
// Implemented by *api.CVEHandler via adapter in main.go.
type CVESearcher interface {
	SearchCVE(cveID string) (totalHosts int, hosts []CVEMatch)
}

// CVEMatch is a single host affected by a CVE.
type CVEMatch struct {
	Hostname  string
	Package   string
	Installed string
	Fixed     string
	Severity  string
	Scanner   string
}

// NewHandler creates a UI handler wired to real database queries.
// The assistant parameter is optional — pass nil to disable the Ask page.
// The customDS parameter is optional — when provided, it replaces the default
// DB-backed DataSource (used by FleetIndex integration).
// The docsDir parameter specifies the path to the docs directory for the
// built-in documentation browser (pass "" to disable).
func NewHandler(queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, caFile, agentDir, version string, assistant *ai.Assistant, hub *spanner.Hub, customDS DataSource, docsDir string, authMethod string, enforcer *license.Enforcer) (*Handler, error) {
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

	h := &Handler{
		renderer:     renderer,
		dataSource:   ds,
		queries:      queries,
		configStore:  configStore,
		baseInterval: baseInterval,
		serverAddr:   serverAddr,
		caFile:       caFile,
		agentDir:     agentDir,
		version:      version,
		assistant:    assistant,
		hub:          hub,
		docsDir:      docsDir,
		enforcer:     enforcer,
		authEnabled:  true,
		authMethod:   authMethod,
	}

	if docsDir != "" {
		h.docsNav = buildDocsNav(docsDir)
	}

	return h, nil
}

// SetPeerService sets the peer replication service for the admin peers page.
// Call after NewHandler when peer replication is configured.
func (h *Handler) SetPeerService(svc *peer.Service) {
	h.peerSvc = svc
}

// SetReportBuilder wires the compliance report builder for HTMX fragment rendering.
// Call after NewHandler to avoid import cycles between ui and server/api.
func (h *Handler) SetReportBuilder(rb *ReportBuilder) {
	h.reportBuilder = rb
}

// SetIntegrationsConfig stores the integrations config for dashboard status display.
func (h *Handler) SetIntegrationsConfig(integrations config.IntegrationsSection, grc config.GRCSection) {
	h.integrationsCfg = integrations
	h.grcCfg = grc
}

// SetCVESearcher attaches the CVE search capability.
func (h *Handler) SetCVESearcher(s CVESearcher) {
	h.cveSearcher = s
}

// SetBranding configures custom branding for the web UI.
func (h *Handler) SetBranding(b config.BrandingSection) {
	h.branding = b
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

	// Branding overrides — serve custom logo/favicon from filesystem.
	if h.branding.LogoPath != "" {
		mux.HandleFunc("GET /branding/logo", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, h.branding.LogoPath)
		})
	}
	if h.branding.FaviconPath != "" {
		mux.HandleFunc("GET /branding/favicon", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, h.branding.FaviconPath)
		})
	}

	// Page routes
	mux.HandleFunc("GET /{$}", h.Dashboard)
	mux.HandleFunc("GET /fleet", h.Fleet)
	mux.HandleFunc("GET /envoys", h.Envoys)
	mux.HandleFunc("GET /envoys/{id}", h.EnvoyDetail)
	mux.HandleFunc("GET /envoys/{id}/trace", h.EnvoyTrace)
	mux.HandleFunc("GET /ui/api/envoys/{id}/security", h.EnvoySecurityTraits)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /compliance", h.Compliance)
	mux.HandleFunc("GET /compliance/executive", h.ComplianceExecutive)
	mux.HandleFunc("GET /compliance/evidence", h.EvidenceCenter)
	mux.HandleFunc("GET /compliance/evidence/bundle", h.EvidenceBundleDownload)
	mux.HandleFunc("POST /api/v1/compliance/waivers", h.CreateWaiver)
	mux.HandleFunc("DELETE /api/v1/compliance/waivers/{id}", h.DeleteWaiver)
	mux.HandleFunc("GET /compliance/{framework}", h.ComplianceFramework)
	mux.HandleFunc("GET /task", h.TaskList)
	mux.HandleFunc("GET /task/{id}", h.TaskDetail)
	mux.HandleFunc("GET /workflows", h.Workflows)
	mux.HandleFunc("GET /workflows/{id}", h.WorkflowDetail)
	mux.HandleFunc("GET /query", h.Query)
	mux.HandleFunc("GET /audit", h.Audit)
	mux.HandleFunc("GET /audit/verify", h.AuditVerify)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /ui/fragments/dashboard-fleet", h.DashboardFleet)
	mux.HandleFunc("GET /ui/fragments/fleet-stats", h.FleetStats)
	mux.HandleFunc("GET /ui/fragments/dashboard-risk", h.DashboardRisk)
	mux.HandleFunc("GET /ui/fragments/dashboard-activity", h.DashboardActivity)
	mux.HandleFunc("GET /ui/fragments/compliance-status", h.ComplianceStatus)
	mux.HandleFunc("GET /ui/fragments/compliance-frameworks", h.ComplianceFrameworksFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-snapshot", h.EvidenceSnapshotFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-mttr", h.EvidenceMTTRFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-controls/{slug}", h.EvidenceControlsFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-control-drill/{slug}/{controlID}", h.EvidenceControlDrillFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-coverage/{slug}", h.EvidenceCoverageFragment)
	mux.HandleFunc("GET /ui/fragments/evidence-audit", h.EvidenceAuditFragment)
	mux.HandleFunc("GET /ui/fragments/dashboard-integrations", h.DashboardIntegrationsFragment)
	mux.HandleFunc("GET /ui/fragments/compliance-report/{slug}", h.ComplianceReportFragment)
	mux.HandleFunc("GET /ui/fragments/compliance-executive", h.ComplianceExecutiveFragment)
	mux.HandleFunc("GET /ui/fragments/ask-status", h.AskStatus)
	mux.HandleFunc("GET /ui/fragments/cve-search", h.CVESearchFragment)
	mux.HandleFunc("GET /ui/fragments/envoys-table", h.EnvoysTable)
	mux.HandleFunc("GET /ui/fragments/envoy-drift/{id}", h.EnvoyDriftFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-runs/{id}", h.EnvoyRunsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-traits/{id}", h.EnvoyTraitsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-config/{id}", h.EnvoyConfigFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-trace/{id}", h.EnvoyTraceFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-compliance/{id}", h.EnvoyComplianceFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-attack-surface/{id}", h.EnvoyAttackSurfaceFragment)
	mux.HandleFunc("GET /envoys/{id}/compliance/{framework}", h.EnvoyComplianceDetail)
	mux.HandleFunc("GET /ui/fragments/envoy-standard-coverage/{id}/{framework}", h.EnvoyStandardCoverageFragment)
	mux.HandleFunc("GET /ui/fragments/run-results/{id}", h.RunResultsFragment)

	// Spanner pages (only registered when running as hub)
	if h.hub != nil {
		mux.HandleFunc("GET /spanner", h.Spanner)
		mux.HandleFunc("GET /spanner/spokes/{id}", h.SpokeDetail)
	}

	// Docs pages (only registered when docsDir is configured and exists)
	if h.docsDir != "" {
		mux.HandleFunc("GET /docs", h.DocsIndex)
		mux.HandleFunc("GET /docs/{path...}", h.DocsPage)
	}

	// Security page — always available. AI chat is optional.
	mux.HandleFunc("GET /ask", h.AskPage)
	if h.assistant != nil {
		mux.HandleFunc("POST /ui/ask", h.AskStream)
	}

	// Combined operations page (tasks + workflows)
	mux.HandleFunc("GET /operations", h.Operations)

	// License page
	mux.HandleFunc("GET /license", h.License)

	// Admin functions are CLI-only (vigogo). No admin pages in web UI.
}

// --- Page data types ---

// PageData holds common fields for all page templates.
type PageData struct {
	Title             string
	ActiveNav         string
	BodyClass         string // extra CSS class on <body>
	Version           string
	AIEnabled         bool
	AIProviderName    string // "Claude", "Ollama", "OpenAI", etc.
	SpannerEnabled    bool
	DocsEnabled       bool
	PeerEnabled       bool
	AuthEnabled       bool
	AuthMethod        string
	CurrentUser       *AuthUser
	LicenseWarning    string // non-empty when license is near or over limits
	PendingChanges    int    // count of pending config changes awaiting approval
	OrgName           string // custom org name from branding config
	CustomLogo        bool   // true when a custom logo path is configured
	CustomFavicon     bool   // true when a custom favicon path is configured
	Theme             string // "light" (default), "dark", or "auto"
}

// LicensePageData holds data for the license status page.
type LicensePageData struct {
	PageData
	Tier          string
	CustomerName  string
	LicenseID     string
	MaxEnvoys    int
	ActiveEnvoys int
	UsagePct      float64
	ExpiresAt     string
	DaysRemaining int
	Stage         string
	StageNum      int
	Violation     string
	IsFreeTier    bool
}

// AuthUser is the UI-visible subset of auth.User (avoids import cycle).
type AuthUser struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
}

// IsAdmin returns true if the user has the admin role.
func (u *AuthUser) IsAdmin() bool {
	return u != nil && u.Role == "admin"
}

// DashboardPageData holds data for the dashboard page.
type DashboardPageData struct {
	PageData
	TotalEnvoys     int
	ActiveEnvoys    int
	RecentFailures  int
	DriftRate       float64
	RecentRuns      []RunRow
	FleetRows       []FleetRow
	// Security posture
	CVECritical     int64
	CVEHigh         int64
	HardeningScore  int64
	RootkitWarnings int64
	MalwareDetected int64
	IntegrityIssues int64
	// Module convergence
	ConvergencePct   float64
	Converged       int
	RecurringDrift  int
	CompErrors      int
	LostContact     int
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore    int
	RiskCritical    int
	RiskHigh        int
	RiskMedium      int
	RiskLow         int
	RiskTopEnvoys      []RiskEnvoyRow
	RiskSparklineData  []float64
}

// RiskEnvoyRow is a summary row for a high-risk envoy on the dashboard.
type RiskEnvoyRow struct {
	EnvoyID  string
	Hostname string
	Score    int
	Level    string
}

// RiskSummaryData holds fleet-wide risk data for the dashboard.
type RiskSummaryData struct {
	AvgScore      int
	Critical      int
	High          int
	Medium        int
	Low           int
	TopRisks      []RiskEnvoyRow
	SparklineData []float64
}

// FleetRow is a envoy row for the dashboard fleet status table.
type FleetRow struct {
	ID       string
	Hostname string
	IP       string
	LastSeen time.Time
	Status   string
}

// RunRow is a single run displayed in a table.
type RunRow struct {
	ID           string
	EnvoyID      string
	Hostname     string
	StartedAt    time.Time
	Status       string
	ModuleCount  int64
	ChangedCount int64
	FailedCount  int64
}

// ConvergencePageData holds module convergence data (envoy enforcement status).
type ConvergencePageData struct {
	Total          int64
	Converged      int64
	RecurringDrift int64
	Errors         int64
	LostContact    int64
	NoData         int64
	ConvergencePct float64 // converged / (total - lost contact) * 100
	SparklineData  []float64
	NonConverged   []ConvergenceRow
}

// ConvergenceRow is a non-converged envoy.
type ConvergenceRow struct {
	ID            string
	Hostname      string
	Status        string
	LastSeen      time.Time
	LastRunStatus string
}

// StandardsPageData holds standards compliance data (framework control coverage).
type StandardsPageData struct {
	PageData
	HasStandards         bool
	StandardsPct         float64 // satisfied / total controls * 100
	SatisfiedControls    int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
}

// ComplianceReportPageData holds data for a per-framework compliance report page.
type ComplianceReportPageData struct {
	PageData
	FrameworkTitle string
	FrameworkSlug  string
}

// DashboardStatsFragmentData holds data for the stats fragment (HTMX auto-refresh).
type DashboardStatsFragmentData struct {
	TotalEnvoys     int
	ActiveEnvoys    int
	RecentFailures  int
	DriftRate       float64
	CVECritical     int64
	CVEHigh         int64
	HardeningScore  int64
	RootkitWarnings int64
	ConvergencePct   float64
	Converged       int
	RecurringDrift  int
	CompErrors      int
	LostContact     int
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore    int
	RiskCritical    int
	RiskHigh        int
	RiskMedium         int
	RiskLow            int
	RiskSparklineData  []float64
}

// EnvoyRow is a single envoy displayed in the envoys table.
type EnvoyRow struct {
	ID         string
	Hostname   string
	IPAddress  string
	EnrolledAt time.Time
	LastSeen   time.Time
	LastRunAt  time.Time
	LastRunID  string
	Status     string
}

// EnvoysPageData holds data for the envoys list page.
type EnvoysPageData struct {
	PageData
	Envoys       []EnvoyRow
	StatusFilter  string
	EnvoyCount   int
	HasMore       bool
	NextCursor    string // hostname of last row (cursor for next page)
}

// EnvoyDetailRunRow is a run with its per-module results for the envoy detail page.
type EnvoyDetailRunRow struct {
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

// EnvoyDetailPageData holds data for the envoy detail page.
type EnvoyDetailPageData struct {
	PageData
	Envoy         EnvoyRow
	Runs         []EnvoyDetailRunRow
	Traits       map[string]any
	DriftHistory []DriftPoint
}

// EnvoyTracePageData holds data for the envoy config trace page.
type EnvoyTracePageData struct {
	PageData
	Hostname string
	EnvoyID  string
	Trace    *config.ConfigTrace
}

// DriftPoint represents a single data point in the drift history timeline.
type DriftPoint struct {
	StartedAt    time.Time
	Status       string
	ChangedCount int64
	FailedCount  int64
}

// EnvoyRunsFragmentData holds data for the lazy-loaded runs fragment.
type EnvoyRunsFragmentData struct {
	Runs         []EnvoyDetailRunRow
	DriftHistory []DriftPoint
}

// EnvoyTraitsFragmentData holds data for the lazy-loaded traits fragment.
type EnvoyTraitsFragmentData struct {
	Traits map[string]any
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
	EnvoyID  string
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
	EnvoyID      string
	Hostname    string
	CreatedAt   time.Time
	Results     []PlanResultRow
	TotalCount  int
	CreateCount int
	UpdateCount int
	DeleteCount int
	SkipCount   int
}

// --- Docs types ---

// DocsNavSection represents a section in the docs navigation tree.
type DocsNavSection struct {
	Title       string
	Path        string // e.g. "getting-started"
	Pages       []DocsNavPage
	Subsections []DocsNavSection // nested sections (e.g. reference/executors)
}

// DocsNavPage represents a single page within a docs section.
type DocsNavPage struct {
	Title string
	Path  string // e.g. "getting-started/installation"
	Text  string // plain-text content for full-text search (stripped markdown)
}

// DocsCrumb represents a breadcrumb entry.
type DocsCrumb struct {
	Title string
	Path  string // empty for the current (last) crumb
}

// DocsPageData holds data for the docs page template.
type DocsPageData struct {
	PageData
	Content        template.HTML
	Breadcrumbs    []DocsCrumb
	NavTree        []DocsNavSection
	CurrentPath    string
	CurrentSection string
}

// docsSectionOrder defines the display order for documentation sections.
// Follows the user journey: get started → understand → operate → secure → comply → look up → try.
var docsSectionOrder = []string{
	"getting-started",
	"concepts",
	"operations",
	"security",
	"compliance",
	"reference",
	"examples",
}

// buildDocsNav walks the docs directory and builds a nav tree.
func buildDocsNav(docsDir string) []DocsNavSection {
	info, err := os.Stat(docsDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	// Read subdirectories.
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		slog.Warn("docs: failed to read directory", "dir", docsDir, "error", err)
		return nil
	}

	sectionMap := make(map[string]DocsNavSection)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		sectionPath := entry.Name()
		sectionTitle := extractMDTitle(filepath.Join(docsDir, sectionPath, "_index.md"))
		if sectionTitle == "" {
			sectionTitle = strings.ReplaceAll(sectionPath, "-", " ")
			sectionTitle = strings.Title(sectionTitle)
		}

		// Read pages in this section.
		pageFiles, err := os.ReadDir(filepath.Join(docsDir, sectionPath))
		if err != nil {
			continue
		}

		var pages []DocsNavPage
		var subsections []DocsNavSection
		for _, pf := range pageFiles {
			if pf.IsDir() {
				if strings.HasPrefix(pf.Name(), ".") {
					continue
				}
				// Build a subsection for nested directories.
				sub := buildSubsection(docsDir, sectionPath, pf.Name())
				subsections = append(subsections, sub)
				continue
			}
			if !strings.HasSuffix(pf.Name(), ".md") || pf.Name() == "_index.md" {
				continue
			}

			pageName := strings.TrimSuffix(pf.Name(), ".md")
			pageTitle := extractMDTitle(filepath.Join(docsDir, sectionPath, pf.Name()))
			if pageTitle == "" {
				pageTitle = strings.ReplaceAll(pageName, "-", " ")
				pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
			}

			// Read file content for full-text search index.
			var plainText string
			if raw, err := os.ReadFile(filepath.Join(docsDir, sectionPath, pf.Name())); err == nil {
				plainText = stripMarkdown(string(raw))
			}

			pages = append(pages, DocsNavPage{
				Title: pageTitle,
				Path:  sectionPath + "/" + pageName,
				Text:  plainText,
			})
		}

		sort.Slice(pages, func(i, j int) bool {
			return pages[i].Title < pages[j].Title
		})

		sectionMap[sectionPath] = DocsNavSection{
			Title:       sectionTitle,
			Path:        sectionPath,
			Pages:       pages,
			Subsections: subsections,
		}
	}

	// Order sections by the predefined list, then any remaining alphabetically.
	var nav []DocsNavSection
	seen := make(map[string]bool)
	for _, name := range docsSectionOrder {
		if sec, ok := sectionMap[name]; ok {
			nav = append(nav, sec)
			seen[name] = true
		}
	}
	for name, sec := range sectionMap {
		if !seen[name] {
			nav = append(nav, sec)
		}
	}

	return nav
}

// buildSubsection builds a DocsNavSection for a nested directory within a section.
func buildSubsection(docsDir, parentPath, dirName string) DocsNavSection {
	subPath := parentPath + "/" + dirName
	subTitle := extractMDTitle(filepath.Join(docsDir, subPath, "_index.md"))
	if subTitle == "" {
		subTitle = strings.ReplaceAll(dirName, "-", " ")
		subTitle = strings.Title(subTitle)
	}

	pageFiles, err := os.ReadDir(filepath.Join(docsDir, subPath))
	if err != nil {
		return DocsNavSection{Title: subTitle, Path: subPath}
	}

	var pages []DocsNavPage
	for _, pf := range pageFiles {
		if pf.IsDir() || !strings.HasSuffix(pf.Name(), ".md") || pf.Name() == "_index.md" {
			continue
		}

		pageName := strings.TrimSuffix(pf.Name(), ".md")
		pageTitle := extractMDTitle(filepath.Join(docsDir, subPath, pf.Name()))
		if pageTitle == "" {
			pageTitle = strings.ReplaceAll(pageName, "-", " ")
			pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
		}

		var plainText string
		if raw, err := os.ReadFile(filepath.Join(docsDir, subPath, pf.Name())); err == nil {
			plainText = stripMarkdown(string(raw))
		}

		pages = append(pages, DocsNavPage{
			Title: pageTitle,
			Path:  subPath + "/" + pageName,
			Text:  plainText,
		})
	}

	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Title < pages[j].Title
	})

	return DocsNavSection{
		Title: subTitle,
		Path:  subPath,
		Pages: pages,
	}
}

// extractMDTitle reads a markdown file and returns the first # heading.
func extractMDTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// stripMarkdown removes markdown syntax to produce plain text for search indexing.
// Strips headings (#), links, images, code fences, HTML tags, bold/italic markers,
// and collapses whitespace. Not a full parser — good enough for search.
func stripMarkdown(md string) string {
	var b strings.Builder
	inCodeFence := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)

		// Skip YAML frontmatter.
		if trimmed == "---" {
			continue
		}

		// Toggle code fences — skip fenced content.
		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			continue
		}

		// Strip heading markers.
		for strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimPrefix(trimmed, "#")
		}
		trimmed = strings.TrimSpace(trimmed)

		// Skip pure table separator lines (|---|---|).
		if len(trimmed) > 0 && trimmed[0] == '|' && !strings.ContainsAny(trimmed, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
			continue
		}

		// Strip table pipes.
		trimmed = strings.ReplaceAll(trimmed, "|", " ")

		if trimmed != "" {
			b.WriteString(trimmed)
			b.WriteByte(' ')
		}
	}

	text := b.String()

	// Strip markdown link/image syntax: [text](url) → text, ![alt](url) → alt
	for {
		i := strings.Index(text, "](")
		if i < 0 {
			break
		}
		// Find opening bracket.
		start := strings.LastIndex(text[:i], "[")
		if start < 0 {
			break
		}
		// Find closing paren.
		end := strings.Index(text[i:], ")")
		if end < 0 {
			break
		}
		linkText := text[start+1 : i]
		// Strip leading ! for images.
		if start > 0 && text[start-1] == '!' {
			start--
		}
		text = text[:start] + linkText + text[i+end+1:]
	}

	// Strip inline code backticks, bold, italic markers.
	// Preserve underscores — they're meaningful in technical identifiers
	// (stream_edit, os_family, etc.).
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")

	// Collapse whitespace.
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

// docsLinkRe matches href attributes in rendered HTML.
var docsLinkRe = regexp.MustCompile(`href="([^"]*)"`)

// rewriteDocsLinks rewrites relative markdown links in rendered HTML to
// absolute /docs/ paths. Handles .md extensions and _index references.
func rewriteDocsLinks(html string, baseDir string) string {
	return docsLinkRe.ReplaceAllStringFunc(html, func(match string) string {
		href := match[6 : len(match)-1] // strip href=" and trailing "

		// Skip absolute URLs, anchors, mailto, javascript, etc.
		if href == "" || href[0] == '/' || href[0] == '#' ||
			strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
			return match
		}

		// Split anchor from path.
		anchor := ""
		if i := strings.IndexByte(href, '#'); i >= 0 {
			anchor = href[i:]
			href = href[:i]
		}

		// Only rewrite links that look like internal docs (contain .md or no extension).
		if !strings.HasSuffix(href, ".md") && strings.Contains(href, ".") {
			return match // external file reference (e.g., image.png)
		}

		// Resolve relative path against the current document's directory.
		resolved := path.Clean(path.Join(baseDir, href))

		// Strip .md and _index suffixes.
		resolved = strings.TrimSuffix(resolved, ".md")
		resolved = strings.TrimSuffix(resolved, "/_index")

		return `href="/docs/` + resolved + anchor + `"`
	})
}

// DocsIndex renders the docs landing page.
func (h *Handler) DocsIndex(w http.ResponseWriter, r *http.Request) {
	content := ""
	indexPath := filepath.Join(h.docsDir, "_index.md")
	if raw, err := os.ReadFile(indexPath); err == nil {
		content = rewriteDocsLinks(RenderMarkdown(string(raw)), "")
	} else {
		content = "<h1>Documentation</h1><p>No docs found. Place markdown files in the docs directory.</p>"
	}

	data := DocsPageData{
		PageData: PageData{Title: "Documentation", ActiveNav: "docs"},
		Content:  template.HTML(content),
		NavTree:  h.docsNav,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "docs", data)
}

// DocsPage renders an individual docs page from disk.
func (h *Handler) DocsPage(w http.ResponseWriter, r *http.Request) {
	pagePath := r.PathValue("path")

	// Serve static assets (SVG, PNG, etc.) directly from the docs directory.
	ext := filepath.Ext(pagePath)
	if ext != "" && ext != ".md" {
		filePath := filepath.Clean(filepath.Join(h.docsDir, pagePath))
		if strings.Contains(filePath, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, filePath)
		return
	}

	// Strip .md extension — markdown files link to each other with .md
	// but the docs browser uses extensionless URLs.
	pagePath = strings.TrimSuffix(pagePath, ".md")

	// Sanitize: only allow alphanumeric, hyphens, underscores, slashes.
	pagePath = filepath.Clean(pagePath)
	if strings.Contains(pagePath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Try exact .md file first, then _index.md for directories.
	var raw []byte
	var err error
	var baseDir string // directory for resolving relative links
	mdPath := filepath.Join(h.docsDir, pagePath+".md")
	if raw, err = os.ReadFile(mdPath); err != nil {
		mdPath = filepath.Join(h.docsDir, pagePath, "_index.md")
		if raw, err = os.ReadFile(mdPath); err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		baseDir = pagePath // _index.md: relative links resolve from this dir
	} else {
		baseDir = path.Dir(pagePath) // regular file: resolve from parent dir
		if baseDir == "." {
			baseDir = ""
		}
	}

	content := rewriteDocsLinks(RenderMarkdown(string(raw)), baseDir)

	// Build breadcrumbs from path.
	parts := strings.Split(pagePath, "/")
	var breadcrumbs []DocsCrumb
	for i, part := range parts {
		title := strings.ReplaceAll(part, "-", " ")
		title = strings.ReplaceAll(title, "_", " ")
		// Try to find a better title from the nav tree.
		crumbPath := strings.Join(parts[:i+1], "/")
		if i == 0 {
			for _, sec := range h.docsNav {
				if sec.Path == part {
					title = sec.Title
					break
				}
			}
		} else if i == 1 {
			// Check subsections for a better title.
			for _, sec := range h.docsNav {
				if sec.Path == parts[0] {
					for _, sub := range sec.Subsections {
						if sub.Path == crumbPath {
							title = sub.Title
							break
						}
					}
					break
				}
			}
		}
		if i < len(parts)-1 {
			breadcrumbs = append(breadcrumbs, DocsCrumb{
				Title: title,
				Path:  crumbPath,
			})
		} else {
			breadcrumbs = append(breadcrumbs, DocsCrumb{Title: title})
		}
	}

	currentSection := ""
	if len(parts) > 0 {
		currentSection = parts[0]
	}

	data := DocsPageData{
		PageData:       PageData{Title: "Docs - " + breadcrumbs[len(breadcrumbs)-1].Title, ActiveNav: "docs"},
		Content:        template.HTML(content),
		Breadcrumbs:    breadcrumbs,
		NavTree:        h.docsNav,
		CurrentPath:    pagePath,
		CurrentSection: currentSection,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "docs", data)
}

// setCommon applies version, AI-enabled, spanner, auth, and license flags to page data.
func (h *Handler) setCommon(pd *PageData) {
	pd.Version = h.version
	pd.AIEnabled = h.assistant != nil
	if h.assistant != nil {
		name := h.assistant.ProviderName()
		if name != "" {
			pd.AIProviderName = strings.ToUpper(name[:1]) + name[1:]
		}
	}
	pd.SpannerEnabled = h.hub != nil
	pd.DocsEnabled = h.docsDir != ""
	pd.PeerEnabled = h.peerSvc != nil
	pd.AuthEnabled = h.authEnabled
	pd.AuthMethod = h.authMethod
	pd.OrgName = h.branding.OrgName
	pd.CustomLogo = h.branding.LogoPath != ""
	pd.CustomFavicon = h.branding.FaviconPath != ""
	pd.Theme = h.branding.Theme
	if pd.Theme == "" {
		pd.Theme = "light"
	}

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		if s.Stage > 0 {
			pd.LicenseWarning = s.Violation
		} else if s.MaxEnvoys > 0 {
			threshold := float64(s.MaxEnvoys) * 0.9
			if float64(s.ActiveEnvoys) >= threshold {
				pd.LicenseWarning = fmt.Sprintf("License usage at %d/%d nodes (%d%%) — approaching limit",
					s.ActiveEnvoys, s.MaxEnvoys, s.ActiveEnvoys*100/s.MaxEnvoys)
			}
		}
	}

	// Check for pending config changes (best-effort).
	if h.queries != nil {
		if pending, err := h.queries.ListPendingConfigChanges(context.Background()); err == nil {
			pd.PendingChanges = len(pending)
		}
	}
}

// setCommonWithRequest applies common fields and extracts the current user from context.
func (h *Handler) setCommonWithRequest(pd *PageData, r *http.Request) {
	h.setCommon(pd)
	if h.authEnabled {
		if u := auth.UserFromContext(r.Context()); u != nil {
			pd.CurrentUser = &AuthUser{
				ID:          u.ID,
				Username:    u.Username,
				DisplayName: u.DisplayName,
				Role:        u.Role,
			}
		}
	}
}

// --- Handlers ---

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)

	// Enrich with convergence data.
	conv := h.dataSource.ConvergenceData(h.baseInterval)
	data.ConvergencePct = conv.ConvergencePct
	data.Converged = int(conv.Converged)
	data.RecurringDrift = int(conv.RecurringDrift)
	data.CompErrors = int(conv.Errors)
	data.LostContact = int(conv.LostContact)

	// Enrich with security posture data.
	sec := h.dataSource.SecurityScanData()
	data.CVECritical = sec.UniqueCritical
	if data.CVECritical == 0 {
		data.CVECritical = sec.TrivyCritical
	}
	data.CVEHigh = sec.UniqueHigh
	if data.CVEHigh == 0 {
		data.CVEHigh = sec.TrivyHigh
	}
	data.HardeningScore = sec.LynisAvgScore
	data.RootkitWarnings = sec.RootkitWarning
	data.MalwareDetected = sec.ClamavInfected
	data.IntegrityIssues = sec.AideChanged

	// Enrich with standards compliance data.
	stds := h.computeStandardsSummary()
	data.StandardsPct = stds.Pct
	data.SatisfiedControls = stds.Satisfied
	data.PartialControls = stds.Partial
	data.NotSatisfiedControls = stds.NotSatisfied
	data.TotalControls = stds.Total

	// Enrich with risk posture data.
	rsk := h.dataSource.RiskData(h.baseInterval)
	data.RiskAvgScore = rsk.AvgScore
	data.RiskCritical = rsk.Critical
	data.RiskHigh = rsk.High
	data.RiskMedium = rsk.Medium
	data.RiskLow = rsk.Low
	data.RiskTopEnvoys = rsk.TopRisks
	data.RiskSparklineData = rsk.SparklineData

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "dashboard", data)
}

// Fleet renders the fleet operations page with buttons for runs, query, tasks, workflows.
func (h *Handler) Fleet(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	data.PageData.Title = "Fleet Operations"
	data.PageData.ActiveNav = "fleet"
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "fleet", data)
}

// FleetStats renders the fleet stat cards fragment for HTMX polling.
func (h *Handler) FleetStats(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	h.renderer.RenderFragment(w, "fleet", "fleet-stats", data)
}

// DashboardStats renders just the stat cards fragment for HTMX polling.
func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardStatsData(h.baseInterval)

	// Enrich with convergence and security data.
	conv := h.dataSource.ConvergenceData(h.baseInterval)
	data.ConvergencePct = conv.ConvergencePct
	data.Converged = int(conv.Converged)
	data.RecurringDrift = int(conv.RecurringDrift)
	data.CompErrors = int(conv.Errors)
	data.LostContact = int(conv.LostContact)

	sec := h.dataSource.SecurityScanData()
	data.CVECritical = sec.UniqueCritical
	if data.CVECritical == 0 {
		data.CVECritical = sec.TrivyCritical
	}
	data.CVEHigh = sec.UniqueHigh
	if data.CVEHigh == 0 {
		data.CVEHigh = sec.TrivyHigh
	}
	data.HardeningScore = sec.LynisAvgScore
	data.RootkitWarnings = sec.RootkitWarning

	stds := h.computeStandardsSummary()
	data.StandardsPct = stds.Pct
	data.SatisfiedControls = stds.Satisfied
	data.PartialControls = stds.Partial
	data.NotSatisfiedControls = stds.NotSatisfied
	data.TotalControls = stds.Total

	rsk := h.dataSource.RiskData(h.baseInterval)
	data.RiskAvgScore = rsk.AvgScore
	data.RiskCritical = rsk.Critical
	data.RiskHigh = rsk.High
	data.RiskMedium = rsk.Medium
	data.RiskLow = rsk.Low
	data.RiskSparklineData = rsk.SparklineData

	h.renderer.RenderFragment(w, "dashboard", "dashboard-stats", data)
}

// DashboardFleet renders the fleet status table fragment for HTMX polling.
func (h *Handler) DashboardFleet(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	h.renderer.RenderFragment(w, "dashboard", "dashboard-fleet", data)
}

// DashboardRisk renders the top-risks table fragment for HTMX polling.
func (h *Handler) DashboardRisk(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	rsk := h.dataSource.RiskData(h.baseInterval)
	data.RiskTopEnvoys = rsk.TopRisks
	h.renderer.RenderFragment(w, "dashboard", "dashboard-risk", data)
}

// DashboardActivity renders the recent activity table fragment for HTMX polling.
func (h *Handler) DashboardActivity(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval)
	h.renderer.RenderFragment(w, "dashboard", "dashboard-activity", data)
}

// IntegrationStatusRow is a single integration shown on the dashboard.
type IntegrationStatusRow struct {
	Name     string
	Category string // "alerting", "psa", "siem", "cmdb", "grc"
	Enabled  bool
	Events   int // number of subscribed event types (0 = all)
}

// DashboardIntegrationsData holds integration status for the dashboard fragment.
type DashboardIntegrationsData struct {
	Integrations []IntegrationStatusRow
	TotalEnabled int
}

// DashboardIntegrationsFragment renders the enabled integrations status.
func (h *Handler) DashboardIntegrationsFragment(w http.ResponseWriter, r *http.Request) {
	var rows []IntegrationStatusRow
	cfg := h.integrationsCfg

	add := func(name, category string, enabled bool, events []string) {
		rows = append(rows, IntegrationStatusRow{
			Name: name, Category: category, Enabled: enabled, Events: len(events),
		})
	}

	if cfg.Slack != nil {
		add("Slack", "alerting", cfg.Slack.Enabled, cfg.Slack.Events)
	}
	if cfg.PagerDuty != nil {
		add("PagerDuty", "alerting", cfg.PagerDuty.Enabled, cfg.PagerDuty.Events)
	}
	if cfg.Opsgenie != nil {
		add("Opsgenie", "alerting", cfg.Opsgenie.Enabled, cfg.Opsgenie.Events)
	}
	if cfg.Teams != nil {
		add("Microsoft Teams", "alerting", cfg.Teams.Enabled, cfg.Teams.Events)
	}
	if cfg.ConnectWise != nil {
		add("ConnectWise", "psa", cfg.ConnectWise.Enabled, cfg.ConnectWise.Events)
	}
	if cfg.Autotask != nil {
		add("Autotask", "psa", cfg.Autotask.Enabled, cfg.Autotask.Events)
	}
	if cfg.Splunk != nil {
		add("Splunk", "siem", cfg.Splunk.Enabled, cfg.Splunk.Events)
	}
	if cfg.Elastic != nil {
		add("Elasticsearch", "siem", cfg.Elastic.Enabled, cfg.Elastic.Events)
	}
	if cfg.Datadog != nil {
		add("Datadog", "siem", cfg.Datadog.Enabled, cfg.Datadog.Events)
	}
	if cfg.ServiceNow != nil {
		add("ServiceNow", "cmdb", cfg.ServiceNow.Enabled, cfg.ServiceNow.Events)
	}
	if cfg.NinjaRMM != nil {
		add("NinjaRMM", "psa", cfg.NinjaRMM.Enabled, cfg.NinjaRMM.Events)
	}
	if cfg.Kaseya != nil {
		add("Kaseya VSA", "psa", cfg.Kaseya.Enabled, cfg.Kaseya.Events)
	}
	if cfg.SumoLogic != nil {
		add("Sumo Logic", "siem", cfg.SumoLogic.Enabled, cfg.SumoLogic.Events)
	}
	if cfg.Sentinel != nil {
		add("Microsoft Sentinel", "siem", cfg.Sentinel.Enabled, cfg.Sentinel.Events)
	}
	if cfg.Device42 != nil {
		add("Device42", "cmdb", cfg.Device42.Enabled, cfg.Device42.Events)
	}
	if cfg.SnipeIT != nil {
		add("Snipe-IT", "cmdb", cfg.SnipeIT.Enabled, cfg.SnipeIT.Events)
	}
	for _, g := range h.grcCfg.Integrations {
		add(g.Name, "grc", g.Enabled, nil)
	}

	enabled := 0
	for _, r := range rows {
		if r.Enabled {
			enabled++
		}
	}

	data := DashboardIntegrationsData{
		Integrations: rows,
		TotalEnabled: enabled,
	}
	h.renderer.RenderFragment(w, "dashboard", "dashboard-integrations", data)
}

// Compliance renders the standards compliance overview page.
func (h *Handler) Compliance(w http.ResponseWriter, r *http.Request) {
	data := h.standardsData()
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance", data)
}

// ComplianceStatus renders the standards compliance gauge/breakdown fragment for HTMX polling.
func (h *Handler) ComplianceStatus(w http.ResponseWriter, r *http.Request) {
	data := h.standardsData()
	h.renderer.RenderFragment(w, "compliance", "compliance-status", data)
}

// standardsData builds a StandardsPageData from the executive report.
func (h *Handler) standardsData() StandardsPageData {
	data := StandardsPageData{
		PageData: PageData{Title: "Compliance", ActiveNav: "compliance"},
	}
	s := h.computeStandardsSummary()
	if s.Total > 0 {
		data.HasStandards = true
		data.StandardsPct = s.Pct
		data.SatisfiedControls = s.Satisfied
		data.PartialControls = s.Partial
		data.NotSatisfiedControls = s.NotSatisfied
		data.TotalControls = s.Total
	}
	return data
}

// standardsSummary computes fleet-wide standards compliance stats.
// Reused by the compliance page and dashboard.
type standardsSummary struct {
	Pct          float64
	Satisfied    int
	Partial      int
	NotSatisfied int
	Total        int
}

func (h *Handler) computeStandardsSummary() standardsSummary {
	// Use the executive report builder — it has the authoritative
	// per-framework satisfied/partial/not_satisfied counts.
	if h.reportBuilder != nil && h.reportBuilder.Executive != nil {
		bgReq, _ := http.NewRequest("GET", "/", nil)
		if report, err := h.reportBuilder.Executive(bgReq); err == nil {
			return h.standardsFromReport(report)
		}
	}
	return standardsSummary{}
}

// standardsFromReport extracts aggregate standards stats from an executive report.
func (h *Handler) standardsFromReport(report any) standardsSummary {
	// The report is *api.ExecutiveReport but we can't import it here.
	// Use the Frameworks field via reflection-free interface assertion.
	type frameworkLike struct {
		Name         string
		Satisfied    int
		Partial      int
		NotSatisfied int
		ScorePercent float64
	}
	type reportLike struct {
		Score struct {
			Percent float64
		}
		Frameworks []frameworkLike
	}

	// Marshal/unmarshal to avoid import cycle.
	data, err := json.Marshal(report)
	if err != nil {
		return standardsSummary{}
	}
	var r reportLike
	if err := json.Unmarshal(data, &r); err != nil {
		return standardsSummary{}
	}

	var s standardsSummary
	for _, fw := range r.Frameworks {
		s.Satisfied += fw.Satisfied
		s.Partial += fw.Partial
		s.NotSatisfied += fw.NotSatisfied
		s.Total += fw.Satisfied + fw.Partial + fw.NotSatisfied
	}
	if s.Total > 0 {
		s.Pct = math.Round(float64(s.Satisfied)/float64(s.Total)*1000) / 10
	}
	return s
}

// ComplianceExecutive renders the executive summary page.
func (h *Handler) ComplianceExecutive(w http.ResponseWriter, r *http.Request) {
	data := ComplianceReportPageData{
		PageData:       PageData{Title: "Executive Summary", ActiveNav: "compliance"},
		FrameworkTitle: "Executive Summary",
		FrameworkSlug:  "executive",
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance-executive", data)
}

// ComplianceFramework renders a per-framework compliance report page.
func (h *Handler) ComplianceFramework(w http.ResponseWriter, r *http.Request) {
	fw := r.PathValue("framework")

	// Map URL slugs to display names and API slugs.
	frameworks := map[string][2]string{
		"compliance":      {"HIPAA Compliance Report", "compliance"},
		"hitrust":         {"HITRUST CSF Compliance Report", "hitrust"},
		"soc2":            {"SOC 2 Trust Services Criteria Report", "soc2"},
		"pci":             {"PCI DSS v4.0 Compliance Report", "pci"},
		"cis":             {"CIS Benchmark Compliance Report", "cis"},
		"nist":            {"NIST 800-53 Compliance Report", "nist"},
		"iso27001":        {"ISO 27001 Compliance Report", "iso27001"},
		"gdpr":            {"GDPR Compliance Report", "gdpr"},
		"nerccip":         {"NERC CIP Compliance Report", "nerccip"},
		"cyberessentials": {"Cyber Essentials+ Compliance Report", "cyberessentials"},
		"iec62443":        {"IEC 62443 Industrial Cybersecurity Report", "iec62443"},
		"sox":             {"SOX IT General Controls Report", "sox"},
		"finra":           {"FINRA Compliance Report", "finra"},
		"mifid2":          {"MiFID II RTS 7 Compliance Report", "mifid2"},
		"ccss":             {"CCSS Cryptocurrency Security Report", "ccss"},
		"fda21cfr11":       {"FDA 21 CFR Part 11 Compliance Report", "fda21cfr11"},
		"gaming":           {"Gaming Commission Compliance Report", "gaming"},
		"ciswindows":        {"CIS Windows Server Benchmark Report", "ciswindows"},
	}

	info, ok := frameworks[fw]
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := ComplianceReportPageData{
		PageData:       PageData{Title: info[0], ActiveNav: "compliance"},
		FrameworkTitle: info[0],
		FrameworkSlug:  info[1],
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance-report", data)
}

// --- Evidence Center ---

// EvidenceFrameworkOption is a framework dropdown option.
type EvidenceFrameworkOption struct {
	Name string
	Slug string
}

// EvidenceCenterPageData holds data for the evidence center page.
type EvidenceCenterPageData struct {
	PageData
	FrameworkSlug string
	DateFrom      string
	DateTo        string
	Frameworks    []EvidenceFrameworkOption
}

// EvidenceSnapshotData holds data for the compliance snapshot fragment.
type EvidenceSnapshotData struct {
	SnapshotDate   string
	ConvergencePct  float64
	Converged      int64
	RecurringDrift int64
	Errors         int64
	LostContact    int64
	NoData         int64
}

// EvidenceMTTRData holds data for the MTTR metrics fragment.
type EvidenceMTTRData struct {
	MTTRDisplay    string
	P50Display     string
	P95Display     string
	TotalIncidents int64
	Resolved       int64
	OpenIncidents  int64
}

// EvidenceControlsData holds data for the controls table fragment.
type EvidenceControlsData struct {
	Slug           string
	FrameworkTitle string
	Controls []struct {
		ControlID string
		Title     string
		Status    string
		Evidence  string
	}
}

// EvidenceControlDrillData holds data for the per-control drill-down fragment.
type EvidenceControlDrillData struct {
	Envoys []EvidenceControlEnvoy
}

// EvidenceControlEnvoy is a single envoy's enforcement evidence for a control.
type EvidenceControlEnvoy struct {
	ID           string
	Hostname     string
	Module       string
	Status       string
	RunCount     int
	FailureCount int
	LastRun      string
}

// evidenceFrameworks returns the list of framework options for the dropdown.
func evidenceFrameworks() []EvidenceFrameworkOption {
	return []EvidenceFrameworkOption{
		{"HIPAA", "compliance"},
		{"HITRUST CSF", "hitrust"},
		{"SOC 2", "soc2"},
		{"PCI DSS v4.0", "pci"},
		{"CIS Benchmarks", "cis"},
		{"NIST 800-53", "nist"},
		{"ISO 27001", "iso27001"},
		{"GDPR", "gdpr"},
		{"NERC CIP", "nerccip"},
		{"Cyber Essentials+", "cyberessentials"},
		{"IEC 62443", "iec62443"},
		{"SOX", "sox"},
		{"FINRA", "finra"},
		{"MiFID II", "mifid2"},
		{"CCSS", "ccss"},
		{"FDA 21 CFR Part 11", "fda21cfr11"},
		{"Gaming Commission", "gaming"},
		{"CIS Windows", "ciswindows"},
	}
}

// formatDuration renders seconds as a human-readable duration.
func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}

// EvidenceCenter renders the audit evidence center page.
func (h *Handler) EvidenceCenter(w http.ResponseWriter, r *http.Request) {
	fw := r.URL.Query().Get("framework")
	if fw == "" {
		fw = "compliance" // default to HIPAA
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}

	data := EvidenceCenterPageData{
		PageData:      PageData{Title: "Audit Evidence Center", ActiveNav: "evidence"},
		FrameworkSlug: fw,
		DateFrom:      from,
		DateTo:        to,
		Frameworks:    evidenceFrameworks(),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance-evidence", data)
}

// EvidenceSnapshotFragment renders the compliance snapshot for a date.
func (h *Handler) EvidenceSnapshotFragment(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}

	snapshot, err := h.queries.GetConvergenceHistoryForDate(r.Context(), dateStr)
	if err != nil {
		// Fall back to live data
		summary := h.dataSource.ConvergenceData(h.baseInterval)
		total := summary.Converged + summary.RecurringDrift + summary.Errors + summary.LostContact + summary.NoData
		pct := float64(0)
		if total > 0 {
			pct = float64(summary.Converged) / float64(total) * 100
		}
		data := EvidenceSnapshotData{
			SnapshotDate:   dateStr + " (live)",
			ConvergencePct:  pct,
			Converged:      summary.Converged,
			RecurringDrift: summary.RecurringDrift,
			Errors:         summary.Errors,
			LostContact:    summary.LostContact,
			NoData:         summary.NoData,
		}
		h.renderer.RenderFragment(w, "compliance-evidence", "evidence-snapshot", data)
		return
	}

	total := snapshot.Total
	pct := float64(0)
	if total > 0 {
		pct = float64(snapshot.Converged) / float64(total) * 100
	}
	data := EvidenceSnapshotData{
		SnapshotDate:   snapshot.RecordedAt,
		ConvergencePct:  pct,
		Converged:      snapshot.Converged,
		RecurringDrift: snapshot.RecurringDrift,
		Errors:         snapshot.Errors,
		LostContact:    snapshot.LostContact,
		NoData:         snapshot.NoData,
	}
	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-snapshot", data)
}

// EvidenceMTTRFragment renders remediation metrics for a date range.
func (h *Handler) EvidenceMTTRFragment(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}

	stats, err := h.queries.ListMTTRStats(r.Context(), db.ListMTTRStatsParams{
		FromRecordedAt: from,
		ToRecordedAt:   to,
	})

	data := EvidenceMTTRData{}
	if err == nil && len(stats) > 0 {
		var totalIncidents, resolved, open, sumMTTR, maxP95 int64
		for _, s := range stats {
			totalIncidents += s.TotalIncidents
			resolved += s.Resolved
			open += s.OpenIncidents
			sumMTTR += s.MttrSeconds * s.Resolved // weighted
			if s.P95Seconds > maxP95 {
				maxP95 = s.P95Seconds
			}
		}
		avgMTTR := int64(0)
		if resolved > 0 {
			avgMTTR = sumMTTR / resolved
		}
		// Use latest day's P50 as representative
		p50 := stats[0].P50Seconds

		data.MTTRDisplay = formatDuration(avgMTTR)
		data.P50Display = formatDuration(p50)
		data.P95Display = formatDuration(maxP95)
		data.TotalIncidents = totalIncidents
		data.Resolved = resolved
		data.OpenIncidents = open
	} else {
		data.MTTRDisplay = "-"
		data.P50Display = "-"
		data.P95Display = "-"
	}

	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-mttr", data)
}

// EvidenceControlsFragment renders the controls table for a framework.
func (h *Handler) EvidenceControlsFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.reportBuilder == nil || h.reportBuilder.Framework == nil {
		http.Error(w, "report builder not configured", http.StatusInternalServerError)
		return
	}
	report, err := h.reportBuilder.Framework(r, slug)
	if err != nil {
		http.Error(w, "failed to load framework report", http.StatusInternalServerError)
		return
	}

	// Extract controls via JSON round-trip (avoids import cycle).
	raw, _ := json.Marshal(report)
	var parsed struct {
		Controls []struct {
			ControlID string `json:"control_id"`
			Title     string `json:"title"`
			Status    string `json:"status"`
			Evidence  string `json:"evidence"`
		} `json:"controls"`
	}
	json.Unmarshal(raw, &parsed)

	// Load active waivers for this framework.
	waivers, _ := h.queries.ListComplianceWaiversByFramework(r.Context(), slug)
	waiverMap := make(map[string]db.ComplianceWaiver)
	for _, w := range waivers {
		waiverMap[w.ControlID] = w
	}

	type controlWithWaiver struct {
		ControlID    string
		Title        string
		Status       string
		Evidence     string
		HasWaiver    bool
		WaiverReason string
		WaiverBy     string
		WaiverExpiry string
		WaiverID     string
	}

	data := struct {
		Slug           string
		FrameworkTitle string
		Controls       []controlWithWaiver
	}{
		Slug:           slug,
		FrameworkTitle: standardDisplayNameFromSlug(slug),
	}
	for _, c := range parsed.Controls {
		cw := controlWithWaiver{
			ControlID: c.ControlID,
			Title:     c.Title,
			Status:    c.Status,
			Evidence:  c.Evidence,
		}
		if w, ok := waiverMap[c.ControlID]; ok {
			cw.HasWaiver = true
			cw.WaiverReason = w.Reason
			cw.WaiverBy = w.Approver
			cw.WaiverID = w.ID
			if w.ExpiresAt.Valid {
				cw.WaiverExpiry = w.ExpiresAt.Time.Format("2006-01-02")
			}
		}
		data.Controls = append(data.Controls, cw)
	}
	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-controls", data)
}

// EvidenceControlDrillFragment renders per-envoy enforcement evidence for a control.
func (h *Handler) EvidenceControlDrillFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	controlID := r.PathValue("controlID")
	_ = slug // framework context (used if we need framework-specific logic later)

	if h.configStore == nil {
		h.renderer.RenderFragment(w, "compliance-evidence", "evidence-control-drill", EvidenceControlDrillData{})
		return
	}

	// Find envoys whose modules cover this control via compliance tags.
	type envoyModule struct {
		envoyID  string
		hostname string
		module   string
		status   string
	}
	var matches []envoyModule
	for _, t := range h.dataSource.EnvoysData(h.baseInterval, "", "", 10000).Envoys {
		trace := h.configStore.Trace(t.Hostname)
		if !trace.Matched {
			continue
		}
		for _, ct := range trace.ComplianceTags {
			for _, ids := range ct.Frameworks {
				for _, id := range ids {
					if id == controlID {
						matches = append(matches, envoyModule{
							envoyID:  t.ID,
							hostname: t.Hostname,
							module:   ct.Module,
							status:   t.Status,
						})
					}
				}
			}
		}
	}

	var envoys []EvidenceControlEnvoy
	for _, m := range matches {
		runs, _ := h.dataSource.EnvoyRunsData(m.envoyID)
		runCount := len(runs.Runs)
		failCount := 0
		lastRun := "-"
		for _, run := range runs.Runs {
			if run.Status == "failure" || run.Status == "partial" {
				failCount++
			}
		}
		if runCount > 0 {
			lastRun = runs.Runs[0].StartedAt.Local().Format("2006-01-02 15:04")
		}
		envoys = append(envoys, EvidenceControlEnvoy{
			ID:           m.envoyID,
			Hostname:     m.hostname,
			Module:       m.module,
			Status:       m.status,
			RunCount:     runCount,
			FailureCount: failCount,
			LastRun:      lastRun,
		})
	}

	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-control-drill", EvidenceControlDrillData{Envoys: envoys})
}

// EvidenceCoverageFragment renders the fleet coverage table for a framework.
func (h *Handler) EvidenceCoverageFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.reportBuilder == nil || h.reportBuilder.Framework == nil {
		http.Error(w, "report builder not configured", http.StatusInternalServerError)
		return
	}
	report, err := h.reportBuilder.Framework(r, slug)
	if err != nil {
		http.Error(w, "failed to load framework report", http.StatusInternalServerError)
		return
	}
	// The report contains .Fleet with all the coverage data.
	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-coverage", report)
}

// EvidenceAuditFragment renders the audit chain verification status.
func (h *Handler) EvidenceAuditFragment(w http.ResponseWriter, r *http.Request) {
	result, err := audit.Verify(r.Context(), h.queries)
	data := struct {
		Valid      bool
		EntryCount int64
		Error      string
	}{}
	if err == nil {
		data.Valid = result.Valid
		data.EntryCount = result.EntryCount
		data.Error = result.Error
	}
	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-audit", data)
}

// EvidenceBundleDownload generates a zip file containing all evidence artifacts.
func (h *Handler) EvidenceBundleDownload(w http.ResponseWriter, r *http.Request) {
	fw := r.URL.Query().Get("framework")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if fw == "" {
		fw = "compliance"
	}
	if from == "" {
		from = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}

	filename := fmt.Sprintf("vigo-evidence-%s-%s.zip", fw, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	zw := zip.NewWriter(w)
	defer zw.Close()

	// Framework report JSON
	if h.reportBuilder != nil && h.reportBuilder.Framework != nil {
		if report, err := h.reportBuilder.Framework(r, fw); err == nil {
			if f, err := zw.Create(fw + "-report.json"); err == nil {
				json.NewEncoder(f).Encode(report)
			}
		}
	}

	// Executive summary JSON
	if h.reportBuilder != nil && h.reportBuilder.Executive != nil {
		if report, err := h.reportBuilder.Executive(r); err == nil {
			if f, err := zw.Create("executive-summary.json"); err == nil {
				json.NewEncoder(f).Encode(report)
			}
		}
	}

	// Audit chain verification
	if result, err := audit.Verify(r.Context(), h.queries); err == nil {
		if f, err := zw.Create("audit-chain.json"); err == nil {
			json.NewEncoder(f).Encode(result)
		}
	}

	// Compliance history CSV
	if history, err := h.queries.ListConvergenceHistoryRange(r.Context(), db.ListConvergenceHistoryRangeParams{
		FromRecordedAt: from,
		ToRecordedAt:   to,
	}); err == nil && len(history) > 0 {
		if f, err := zw.Create("compliance-history.csv"); err == nil {
			fmt.Fprintln(f, "date,total,compliant,recurring_drift,errors,lost_contact,no_data")
			for _, h := range history {
				fmt.Fprintf(f, "%s,%d,%d,%d,%d,%d,%d\n",
					h.RecordedAt, h.Total, h.Converged, h.RecurringDrift, h.Errors, h.LostContact, h.NoData)
			}
		}
	}

	// MTTR stats CSV
	if stats, err := h.queries.ListMTTRStats(r.Context(), db.ListMTTRStatsParams{
		FromRecordedAt: from,
		ToRecordedAt:   to,
	}); err == nil && len(stats) > 0 {
		if f, err := zw.Create("mttr-stats.csv"); err == nil {
			fmt.Fprintln(f, "date,total_incidents,resolved,mttr_seconds,p50_seconds,p95_seconds,open_incidents")
			for _, s := range stats {
				fmt.Fprintf(f, "%s,%d,%d,%d,%d,%d,%d\n",
					s.RecordedAt, s.TotalIncidents, s.Resolved, s.MttrSeconds, s.P50Seconds, s.P95Seconds, s.OpenIncidents)
			}
		}
	}

	// Fleet inventory CSV
	envoyData := h.dataSource.EnvoysData(h.baseInterval, "", "", 100000)
	if len(envoyData.Envoys) > 0 {
		if f, err := zw.Create("fleet-inventory.csv"); err == nil {
			fmt.Fprintln(f, "hostname,id,ip_address,status,enrolled_at,last_seen,last_run")
			for _, e := range envoyData.Envoys {
				fmt.Fprintf(f, "%s,%s,%s,%s,%s,%s,%s\n",
					e.Hostname, e.ID, e.IPAddress, e.Status,
					e.EnrolledAt.Format(time.RFC3339),
					e.LastSeen.Format(time.RFC3339),
					e.LastRunAt.Format(time.RFC3339))
			}
		}
	}
}

// CreateWaiver creates a compliance waiver for a control.
func (h *Handler) CreateWaiver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Framework string `json:"framework"`
		ControlID string `json:"control_id"`
		Reason    string `json:"reason"`
		Approver  string `json:"approver"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Framework == "" || req.ControlID == "" || req.Reason == "" || req.Approver == "" {
		http.Error(w, "framework, control_id, reason, and approver are required", http.StatusBadRequest)
		return
	}

	var expiresAt sql.NullTime
	if req.ExpiresAt != "" {
		t, err := time.Parse("2006-01-02", req.ExpiresAt)
		if err != nil {
			http.Error(w, "invalid expires_at format (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		expiresAt = sql.NullTime{Time: t, Valid: true}
	}

	waiver, err := h.queries.CreateComplianceWaiver(r.Context(), db.CreateComplianceWaiverParams{
		Framework: req.Framework,
		ControlID: req.ControlID,
		Reason:    req.Reason,
		Approver:  req.Approver,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		slog.Error("failed to create compliance waiver", "error", err)
		http.Error(w, "failed to create waiver", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(waiver)
}

// DeleteWaiver deactivates a compliance waiver.
func (h *Handler) DeleteWaiver(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.queries.DeactivateComplianceWaiver(r.Context(), id); err != nil {
		http.Error(w, "failed to delete waiver", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ComplianceFrameworksFragment renders the frameworks grid as an HTMX fragment.
func (h *Handler) ComplianceFrameworksFragment(w http.ResponseWriter, r *http.Request) {
	if h.reportBuilder == nil || h.reportBuilder.Executive == nil {
		http.Error(w, "report builder not configured", http.StatusInternalServerError)
		return
	}
	data, err := h.reportBuilder.Executive(r)
	if err != nil {
		slog.Error("failed to build executive report for frameworks fragment", "error", err)
		http.Error(w, "failed to load framework data", http.StatusInternalServerError)
		return
	}
	h.renderer.RenderFragment(w, "compliance", "compliance-frameworks", data)
}

// ComplianceReportFragment renders the per-framework report content as an HTMX fragment.
func (h *Handler) ComplianceReportFragment(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.reportBuilder == nil || h.reportBuilder.Framework == nil {
		http.Error(w, "report builder not configured", http.StatusInternalServerError)
		return
	}
	data, err := h.reportBuilder.Framework(r, slug)
	if err != nil {
		slog.Error("failed to build framework report", "slug", slug, "error", err)
		http.Error(w, "failed to load report data", http.StatusInternalServerError)
		return
	}
	h.renderer.RenderFragment(w, "compliance-report", "compliance-report-content", data)
}

// ComplianceExecutiveFragment renders the executive summary content as an HTMX fragment.
func (h *Handler) ComplianceExecutiveFragment(w http.ResponseWriter, r *http.Request) {
	if h.reportBuilder == nil || h.reportBuilder.Executive == nil {
		http.Error(w, "report builder not configured", http.StatusInternalServerError)
		return
	}
	data, err := h.reportBuilder.Executive(r)
	if err != nil {
		slog.Error("failed to build executive report", "error", err)
		http.Error(w, "failed to load executive data", http.StatusInternalServerError)
		return
	}
	h.renderer.RenderFragment(w, "compliance-executive", "compliance-executive-content", data)
}

// Envoys renders the envoys list page with optional status filter and pagination.
func (h *Handler) Envoys(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	after := r.URL.Query().Get("after")
	data := h.dataSource.EnvoysData(h.baseInterval, statusFilter, after, 100)
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoys", data)
}

// EnvoyDetail renders the envoy detail page shell. Runs and traits are loaded
// asynchronously via HTMX fragment endpoints for instant page rendering.
func (h *Handler) EnvoyDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoy-detail", data)
}

// EnvoyTrace renders the config trace page for an envoy.
func (h *Handler) EnvoyTrace(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	trace := h.configStore.Trace(detail.Envoy.Hostname)

	data := EnvoyTracePageData{
		PageData: PageData{Title: "Config Trace: " + detail.Envoy.Hostname, ActiveNav: "envoys"},
		Hostname: detail.Envoy.Hostname,
		EnvoyID:  idStr,
		Trace:    trace,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoy-trace", data)
}

// EnvoySecurityTraits returns cached security_scan traits for a envoy as JSON.
// This uses FleetIndex data (no agent stream required).
func (h *Handler) EnvoySecurityTraits(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	traits, ok := h.dataSource.SecurityScanTraits(idStr)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"available":false}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(traits)
}

// EnvoyDriftFragment renders only the drift history table for an envoy.
func (h *Handler) EnvoyDriftFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.EnvoyRunsData(idStr)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-drift-fragment", data)
}

// EnvoyRunsFragment renders the recent runs table for an envoy (HTMX lazy load).
func (h *Handler) EnvoyRunsFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.EnvoyRunsData(idStr)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-runs-fragment", data)
}

// EnvoyTraitsFragment renders the traits fragment for an envoy (HTMX lazy load).
func (h *Handler) EnvoyTraitsFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.EnvoyTraitsData(idStr)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-traits-fragment", data)
}

// EnvoyConfigFragment renders the config YAML inline for an envoy.
func (h *Handler) EnvoyConfigFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	cfgData, err := h.dataSource.ConfigData(detail.Envoy.Hostname)
	if err != nil {
		h.renderer.RenderFragment(w, "envoy-detail", "envoy-config-fragment", ConfigPageData{
			Hostname: detail.Envoy.Hostname,
		})
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-config-fragment", cfgData)
}

// EnvoyTraceFragment renders the config trace inline for an envoy.
func (h *Handler) EnvoyTraceFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	trace := h.configStore.Trace(detail.Envoy.Hostname)
	data := EnvoyTracePageData{
		Hostname: detail.Envoy.Hostname,
		EnvoyID:  idStr,
		Trace:    trace,
	}
	h.renderer.RenderFragment(w, "envoy-trace", "envoy-trace-fragment", data)
}

// EnvoyComplianceData holds data for the per-envoy compliance tab.
type EnvoyComplianceData struct {
	EnvoyID    string
	Hostname   string
	Status     string
	Frameworks []EnvoyFrameworkCard
}

// EnvoyFrameworkCard is a compliance framework card for the envoy detail page.
type EnvoyFrameworkCard struct {
	Name         string
	Slug         string
	ScorePercent float64
	Satisfied    int
	Partial      int
	NotSatisfied int
}

// EnvoyComplianceDetailPageData holds data for the per-envoy standard compliance page.
type EnvoyComplianceDetailPageData struct {
	PageData
	EnvoyID        string
	Hostname       string
	FrameworkSlug  string
	FrameworkTitle string
}

// MissingControlRec is a missing control with recommended modules.
type MissingControlRec struct {
	ControlID          string
	RecommendedModules []string
}

// EnvoyStandardCoverageData holds data for the per-envoy standard coverage fragment.
type EnvoyStandardCoverageData struct {
	EnvoyID          string
	Hostname         string
	FrameworkTitle   string
	Coverage         *config.StandardCoverage
	MissingWithRecs  []MissingControlRec
	ModuleConvergence string // envoy convergence status (converged, failing, etc.)
	ModulePct        float64 // module convergence percentage (100 = all modules converged)
}

// AttackSurfaceData holds data for the envoy attack surface tab.
type AttackSurfaceData struct {
	Hostname       string
	RiskLevel      string
	RiskScore      int
	CVECritical    int
	CVEHigh        int
	CVEMedium      int
	CVELow         int
	HardeningScore int
	ListeningPorts int
	Ports          []PortEntry
	Vulnerabilities bool
	HasData        bool
}

// PortEntry is a listening port on an envoy.
type PortEntry struct {
	Port     string
	Protocol string
	Process  string
}

// EnvoyAttackSurfaceFragment renders the attack surface tab for an envoy.
func (h *Handler) EnvoyAttackSurfaceFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	// Extract security traits.
	traits, _ := h.dataSource.EnvoyTraitsData(idStr)
	data := AttackSurfaceData{Hostname: detail.Envoy.Hostname}

	if traits.Traits != nil {
		traitStr := func(key string) string {
			if v, ok := traits.Traits[key]; ok {
				return fmt.Sprintf("%v", v)
			}
			return ""
		}
		for k := range traits.Traits {
			vs := traitStr(k)
			switch k {
			case "security_scan.trivy.critical":
				fmt.Sscanf(vs, "%d", &data.CVECritical)
			case "security_scan.trivy.high":
				fmt.Sscanf(vs, "%d", &data.CVEHigh)
			case "security_scan.trivy.medium":
				fmt.Sscanf(vs, "%d", &data.CVEMedium)
			case "security_scan.trivy.low":
				fmt.Sscanf(vs, "%d", &data.CVELow)
			case "security_scan.lynis.score":
				fmt.Sscanf(vs, "%d", &data.HardeningScore)
			}
			// Collect listening ports.
			if strings.HasPrefix(k, "network.listening.") && strings.HasSuffix(k, ".port") {
				prefix := strings.TrimSuffix(k, ".port")
				data.Ports = append(data.Ports, PortEntry{
					Port:     vs,
					Protocol: traitStr(prefix + ".protocol"),
					Process:  traitStr(prefix + ".process"),
				})
			}
		}
	}

	data.ListeningPorts = len(data.Ports)
	data.Vulnerabilities = data.CVECritical+data.CVEHigh+data.CVEMedium+data.CVELow > 0
	data.HasData = data.Vulnerabilities || data.HardeningScore > 0 || len(data.Ports) > 0

	// Compute risk score: weighted sum.
	data.RiskScore = data.CVECritical*10 + data.CVEHigh*5 + data.CVEMedium*2 + data.CVELow
	if data.HardeningScore > 0 && data.HardeningScore < 50 {
		data.RiskScore += 20
	}
	data.RiskScore += len(data.Ports) // each open port adds 1

	switch {
	case data.RiskScore >= 50:
		data.RiskLevel = "critical"
	case data.RiskScore >= 20:
		data.RiskLevel = "high"
	case data.RiskScore >= 5:
		data.RiskLevel = "medium"
	default:
		data.RiskLevel = "low"
	}

	h.renderer.RenderFragment(w, "envoy-detail", "envoy-attack-surface-fragment", data)
}

// EnvoyComplianceFragment renders the compliance frameworks tab for an envoy.
// Shows per-envoy coverage from the compliance index (not fleet-wide scores).
func (h *Handler) EnvoyComplianceFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	data := EnvoyComplianceData{
		EnvoyID:  idStr,
		Hostname: detail.Envoy.Hostname,
		Status:   detail.Envoy.Status,
	}

	// Build per-envoy coverage for each standard in the compliance index.
	for slug, standardKey := range config.StandardKeyForSlug {
		coverage := h.configStore.EnvoyStandardCoverage(detail.Envoy.Hostname, standardKey)
		if coverage != nil && coverage.TotalControls > 0 {
			data.Frameworks = append(data.Frameworks, EnvoyFrameworkCard{
				Name:         standardDisplayName(standardKey),
				Slug:         slug,
				ScorePercent: coverage.CoveragePercent,
				Satisfied:    len(coverage.CoveredControls),
				Partial:      0,
				NotSatisfied: len(coverage.MissingControls),
			})
		}
	}
	sort.Slice(data.Frameworks, func(i, j int) bool {
		return data.Frameworks[i].Name < data.Frameworks[j].Name
	})

	h.renderer.RenderFragment(w, "envoy-detail", "envoy-compliance-fragment", data)
}

// EnvoyComplianceDetail renders the per-envoy compliance detail page for a specific standard.
func (h *Handler) EnvoyComplianceDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	fw := r.PathValue("framework")

	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	title := standardDisplayNameFromSlug(fw)
	if title == "" {
		http.NotFound(w, r)
		return
	}

	data := EnvoyComplianceDetailPageData{
		PageData:       PageData{Title: detail.Envoy.Hostname + " — " + title, ActiveNav: "envoys"},
		EnvoyID:        idStr,
		Hostname:       detail.Envoy.Hostname,
		FrameworkSlug:  fw,
		FrameworkTitle: title,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoy-compliance-detail", data)
}

// EnvoyStandardCoverageFragment renders the per-envoy standard coverage content.
func (h *Handler) EnvoyStandardCoverageFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	fw := r.PathValue("framework")

	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	standardKey, ok := config.StandardKeyForSlug[fw]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Module convergence: 100% if converged, 0% if errors/no data.
	modPct := float64(0)
	switch detail.Envoy.Status {
	case "converged":
		modPct = 100
	case "recurring drift":
		modPct = 75
	case "errors":
		modPct = 25
	}

	coverage := h.configStore.EnvoyStandardCoverage(detail.Envoy.Hostname, standardKey)

	// Build recommendations for missing controls.
	var missingRecs []MissingControlRec
	if coverage != nil {
		for _, controlID := range coverage.MissingControls {
			rec := MissingControlRec{ControlID: controlID}
			rec.RecommendedModules = h.configStore.RecommendModulesForControl(standardKey, controlID)
			missingRecs = append(missingRecs, rec)
		}
	}

	data := EnvoyStandardCoverageData{
		EnvoyID:          idStr,
		Hostname:         detail.Envoy.Hostname,
		FrameworkTitle:   standardDisplayNameFromSlug(fw),
		Coverage:         coverage,
		MissingWithRecs:  missingRecs,
		ModuleConvergence: detail.Envoy.Status,
		ModulePct:        modPct,
	}
	h.renderer.RenderFragment(w, "envoy-compliance-detail", "envoy-standard-coverage-fragment", data)
}

// standardDisplayName maps compliance tag keys to display names.
func standardDisplayName(key string) string {
	names := map[string]string{
		"hipaa":            "HIPAA",
		"hitrust":          "HITRUST CSF",
		"soc2":             "SOC 2",
		"pci-dss":          "PCI DSS v4.0",
		"cis":              "CIS Benchmarks",
		"nist-800-53":      "NIST 800-53",
		"iso-27001":        "ISO 27001",
		"gdpr":             "GDPR",
		"nerc-cip":         "NERC CIP",
		"cyber-essentials": "Cyber Essentials+",
		"iec-62443":        "IEC 62443",
		"sox":              "SOX",
		"finra":            "FINRA",
		"mifid2":           "MiFID II",
		"ccss":              "CCSS",
		"fda-21cfr11":       "FDA 21 CFR Part 11",
		"gaming":             "Gaming Commission",
		"cis-windows":        "CIS Windows",
	}
	if n, ok := names[key]; ok {
		return n
	}
	return key
}

// standardDisplayNameFromSlug maps URL slugs to display names.
func standardDisplayNameFromSlug(slug string) string {
	key, ok := config.StandardKeyForSlug[slug]
	if !ok {
		return ""
	}
	return standardDisplayName(key)
}

// fwSlugMap maps framework display names to URL slugs (same as render.go fwSlug).
func fwSlugMap(name string) string {
	slugs := map[string]string{
		"HIPAA":             "compliance",
		"HITRUST CSF":       "hitrust",
		"SOC 2":             "soc2",
		"PCI DSS v4.0":      "pci",
		"CIS Benchmarks":    "cis",
		"NIST 800-53":       "nist",
		"ISO 27001":         "iso27001",
		"GDPR":              "gdpr",
		"NERC CIP":          "nerccip",
		"Cyber Essentials+": "cyberessentials",
		"IEC 62443":         "iec62443",
		"SOX":               "sox",
		"FINRA":             "finra",
		"MiFID II":          "mifid2",
		"CCSS":               "ccss",
		"FDA 21 CFR Part 11": "fda21cfr11",
		"Gaming Commission":  "gaming",
		"CIS Windows":        "ciswindows",
	}
	if s, ok := slugs[name]; ok {
		return s
	}
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// Runs renders the runs list page with optional status filter.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	data := h.dataSource.RunsData(statusFilter)
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "run-detail", data)
}

// RunResultsFragment renders just the per-module results table for a run (HTMX fragment).
func (h *Handler) RunResultsFragment(w http.ResponseWriter, r *http.Request) {
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
	h.renderer.RenderFragment(w, "run-detail", "run-results-fragment", data)
}

// Config renders the config viewer page with syntax-highlighted raw YAML.
func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	data, err := h.dataSource.ConfigData(hostname)
	if err != nil {
		http.Error(w, "config not found", http.StatusNotFound)
		return
	}
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "plans", data)
}

// EnvoysTable renders the envoys table fragment for HTMX polling.
func (h *Handler) EnvoysTable(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	after := r.URL.Query().Get("after")
	data := h.dataSource.EnvoysData(h.baseInterval, statusFilter, after, 100)
	h.renderer.RenderFragment(w, "envoys", "envoys-table", data)
}

// --- Production data source ---

type dbDataSource struct {
	queries     *db.Queries
	configStore *config.ConfigStore
}

// envoyComplianceInfo computes convergence status for an envoy using recent runs.
func (ds *dbDataSource) envoyComplianceInfo(ctx context.Context, c db.Envoy, baseInterval time.Duration) (status string, runInfo *convergence.RunInfo) {
	lastSeen := time.Time{}
	if c.LastSeen.Valid {
		lastSeen = c.LastSeen.Time
	}

	latestRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: c.ID,
		Limit:    int64(convergence.RecurringDriftThreshold),
	})
	if err != nil {
		return convergence.ComputeConvergence(nil, lastSeen, baseInterval), nil
	}

	var recent []convergence.RunInfo
	for _, r := range latestRuns {
		recent = append(recent, convergence.RunInfo{
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
		})
	}

	if len(recent) > 0 {
		runInfo = &recent[0]
	}

	return convergence.ComputeConvergence(recent, lastSeen, baseInterval), runInfo
}

func (ds *dbDataSource) DashboardData(baseInterval time.Duration) DashboardPageData {
	ctx := context.Background()
	envoys, err := ds.queries.ListEnvoys(ctx)
	if err != nil {
		slog.Error("failed to list envoys", "error", err)
		return DashboardPageData{PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"}}
	}

	totalEnvoys := len(envoys)
	activeCount := 0
	driftedCount := 0
	failureCount := 0

	for _, n := range envoys {
		if n.Revoked != 0 {
			continue
		}
		if n.LastSeen.Valid && time.Since(n.LastSeen.Time) < time.Hour {
			activeCount++
		}

		status, _ := ds.envoyComplianceInfo(ctx, n, baseInterval)
		switch status {
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

	allRuns, err := ds.queries.ListAllRuns(ctx, 10)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
	}

	envoyMap := make(map[string]string, len(envoys))
	for _, n := range envoys {
		envoyMap[n.ID] = n.Hostname
	}

	recentRuns := make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		recentRuns = append(recentRuns, RunRow{
			ID:           r.ID,
			EnvoyID:      r.EnvoyID,
			Hostname:     envoyMap[r.EnvoyID],
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	fleetRows := make([]FleetRow, 0, len(envoys))
	for _, n := range envoys {
		if n.Revoked != 0 {
			continue
		}
		status, _ := ds.envoyComplianceInfo(ctx, n, baseInterval)
		var lastSeen time.Time
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}
		fleetRows = append(fleetRows, FleetRow{
			ID:       n.ID,
			Hostname: n.Hostname,
			IP:       n.IpAddress,
			LastSeen: lastSeen,
			Status:   status,
		})
	}

	return DashboardPageData{
		PageData:       PageData{Title: "Dashboard", ActiveNav: "dashboard"},
		TotalEnvoys:     totalEnvoys,
		ActiveEnvoys:    activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     recentRuns,
		FleetRows:      fleetRows,
	}
}

func (ds *dbDataSource) ConvergenceData(baseInterval time.Duration) ConvergencePageData {
	ctx := context.Background()
	envoys, err := ds.queries.ListEnvoys(ctx)
	if err != nil {
		slog.Error("failed to list envoys", "error", err)
		return ConvergencePageData{}
	}

	var statuses []string
	var notConverged []ConvergenceRow

	for _, n := range envoys {
		if n.Revoked != 0 {
			continue
		}
		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		status, _ := ds.envoyComplianceInfo(ctx, n, baseInterval)
		statuses = append(statuses, status)

		if status != "converged" {
			lastRunStatus := ""
			latestRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
				EnvoyID: n.ID,
				Limit:  1,
			})
			if err == nil && len(latestRuns) > 0 {
				lastRunStatus = latestRuns[0].Status
			}
			notConverged = append(notConverged, ConvergenceRow{
				ID:            n.ID,
				Hostname:      n.Hostname,
				Status:        status,
				LastSeen:      lastSeen,
				LastRunStatus: lastRunStatus,
			})
		}
	}

	summary := convergence.Summary(statuses)
	total := int64(len(statuses))
	convPct := float64(0)
	if total > 0 {
		convPct = float64(summary["converged"]) / float64(total) * 100
	}

	history, err := ds.queries.ListConvergenceHistory(ctx, 30)
	if err != nil {
		slog.Error("failed to list convergence history", "error", err)
	}

	sparkData := make([]float64, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		if h.Total > 0 {
			sparkData = append(sparkData, float64(h.Converged)/float64(h.Total)*100)
		}
	}

	return ConvergencePageData{
		ConvergencePct: convPct,
		Total:         total,
		Converged:     int64(summary["converged"]),
		RecurringDrift: int64(summary["recurring drift"]),
		Errors:         int64(summary["errors"]),
		LostContact:    int64(summary["lost contact"]),
		NoData:         int64(summary["no data"]),
		SparklineData: sparkData,
		NonConverged:  notConverged,
	}
}

func (ds *dbDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := ds.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalEnvoys:     d.TotalEnvoys,
		ActiveEnvoys:    d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (ds *dbDataSource) EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) EnvoysPageData {
	ctx := context.Background()
	envoys, err := ds.queries.ListEnvoys(ctx)
	if err != nil {
		slog.Error("failed to list envoys", "error", err)
		return EnvoysPageData{PageData: PageData{Title: "Envoys", ActiveNav: "envoys"}}
	}

	// Sort by hostname for consistent pagination.
	sort.Slice(envoys, func(i, j int) bool {
		return envoys[i].Hostname < envoys[j].Hostname
	})

	var rows []EnvoyRow
	for _, n := range envoys {
		if n.Revoked != 0 {
			continue
		}
		if afterHostname != "" && n.Hostname <= afterHostname {
			continue
		}
		status, _ := ds.envoyComplianceInfo(ctx, n, baseInterval)

		if statusFilter != "" && status != statusFilter {
			continue
		}

		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		var lastRunAt time.Time
		latestRuns, _ := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
			EnvoyID: n.ID,
			Limit:    1,
		})
		if len(latestRuns) > 0 && !latestRuns[0].StartedAt.IsZero() {
			lastRunAt = latestRuns[0].StartedAt
		}

		rows = append(rows, EnvoyRow{
			ID:         n.ID,
			Hostname:   n.Hostname,
			IPAddress:  n.IpAddress,
			EnrolledAt: n.EnrolledAt,
			LastSeen:   lastSeen,
			LastRunAt:  lastRunAt,
			Status:     status,
		})

		if limit > 0 && len(rows) > limit {
			break
		}
	}

	hasMore := len(rows) > limit && limit > 0
	if hasMore {
		rows = rows[:limit]
	}

	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor = rows[len(rows)-1].Hostname
	}

	return EnvoysPageData{
		PageData:     PageData{Title: "Envoys", ActiveNav: "envoys"},
		Envoys:      rows,
		StatusFilter: statusFilter,
		EnvoyCount:  len(rows),
		HasMore:      hasMore,
		NextCursor:   nextCursor,
	}
}

func (ds *dbDataSource) EnvoyDetailData(envoyID string, baseInterval time.Duration) (EnvoyDetailPageData, error) {
	ctx := context.Background()
	envoy, err := ds.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return EnvoyDetailPageData{}, fmt.Errorf("envoy not found: %w", err)
	}

	lastSeen := time.Time{}
	if envoy.LastSeen.Valid {
		lastSeen = envoy.LastSeen.Time
	}

	status, _ := ds.envoyComplianceInfo(ctx, envoy, baseInterval)

	// Shell only — runs and traits are loaded via HTMX fragments.
	return EnvoyDetailPageData{
		PageData: PageData{Title: "Envoy: " + envoy.Hostname, ActiveNav: "envoys"},
		Envoy: EnvoyRow{
			ID:         envoy.ID,
			Hostname:   envoy.Hostname,
			IPAddress:  envoy.IpAddress,
			EnrolledAt: envoy.EnrolledAt,
			LastSeen:   lastSeen,
			Status:     status,
		},
	}, nil
}

func (ds *dbDataSource) EnvoyRunsData(envoyID string) (EnvoyRunsFragmentData, error) {
	ctx := context.Background()
	envoy, err := ds.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return EnvoyRunsFragmentData{}, fmt.Errorf("envoy not found: %w", err)
	}

	dbRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: envoyID,
		Limit:    20,
	})
	if err != nil {
		slog.Error("failed to list runs for envoy", "error", err)
		return EnvoyRunsFragmentData{}, nil
	}

	// Batch-fetch all run results in one query to avoid N+1.
	runIDs := make([]string, len(dbRuns))
	for i, r := range dbRuns {
		runIDs[i] = r.ID
	}
	allResults, err := ds.queries.ListRunResultsByRunIDs(ctx, runIDs)
	if err != nil {
		slog.Error("failed to batch-fetch run results", "error", err)
	}

	// Group results by run ID.
	resultsByRun := make(map[string][]RunResultRow, len(dbRuns))
	for _, res := range allResults {
		errMsg := ""
		if res.ErrorMessage.Valid {
			errMsg = res.ErrorMessage.String
		}
		resultsByRun[res.RunID] = append(resultsByRun[res.RunID], RunResultRow{
			ModuleName:   res.ModuleName,
			ResourceType: res.ResourceType,
			ResourceName: res.ResourceName,
			ActionTaken:  res.ActionTaken,
			Changed:      res.Changed != 0,
			ErrorMessage: errMsg,
			DurationMs:   res.DurationMs,
		})
	}

	var runs []EnvoyDetailRunRow
	var driftHistory []DriftPoint
	for _, r := range dbRuns {
		runs = append(runs, EnvoyDetailRunRow{
			RunRow: RunRow{
				ID:           r.ID,
				Hostname:     envoy.Hostname,
				StartedAt:    r.StartedAt,
				Status:       r.Status,
				ModuleCount:  r.ModuleCount,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
			},
			Results: resultsByRun[r.ID],
		})
		driftHistory = append(driftHistory, DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return EnvoyRunsFragmentData{Runs: runs, DriftHistory: driftHistory}, nil
}

func (ds *dbDataSource) EnvoyTraitsData(envoyID string) (EnvoyTraitsFragmentData, error) {
	ctx := context.Background()
	traits := make(map[string]any)
	cf, err := ds.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err == nil {
		var raw map[string]any
		if json.Unmarshal([]byte(cf.Traits), &raw) == nil {
			flattenTraits("", raw, traits)
		}
	}
	return EnvoyTraitsFragmentData{Traits: traits}, nil
}

// flattenTraits recursively flattens nested JSON into dot-separated string
// values safe for template rendering.
func flattenTraits(prefix string, data map[string]any, out map[string]any) {
	for key, val := range data {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch v := val.(type) {
		case map[string]any:
			flattenTraits(fullKey, v, out)
		case string:
			out[fullKey] = v
		default:
			b, _ := json.Marshal(v)
			out[fullKey] = string(b)
		}
	}
}

func (ds *dbDataSource) RunsData(statusFilter string) RunsPageData {
	ctx := context.Background()
	allRuns, err := ds.queries.ListAllRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return RunsPageData{PageData: PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	// Build envoy hostname lookup to avoid N+1
	envoys, err := ds.queries.ListEnvoys(ctx)
	if err != nil {
		slog.Error("failed to list envoys for hostname lookup", "error", err)
	}
	envoyMap := make(map[string]string, len(envoys))
	for _, n := range envoys {
		envoyMap[n.ID] = n.Hostname
	}

	var rows []RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		rows = append(rows, RunRow{
			ID:           r.ID,
			EnvoyID:      r.EnvoyID,
			Hostname:     envoyMap[r.EnvoyID],
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

	envoy, err := ds.queries.GetEnvoyByID(ctx, r.EnvoyID)
	if err != nil {
		return RunDetailPageData{}, fmt.Errorf("envoy not found: %w", err)
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
		EnvoyID:   envoy.ID,
		Run: RunRow{
			ID:           r.ID,
			Hostname:     envoy.Hostname,
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
		PageData: PageData{Title: "Config: " + hostname, ActiveNav: "envoys"},
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

	envoy, err := ds.queries.GetEnvoyByID(ctx, p.EnvoyID)
	if err != nil {
		return PlanDetailPageData{}, fmt.Errorf("envoy not found: %w", err)
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
		PageData:    PageData{Title: "Plan Detail", ActiveNav: "envoys"},
		PlanID:      p.ID,
		EnvoyID:      envoy.ID,
		Hostname:    envoy.Hostname,
		CreatedAt:   p.CreatedAt,
		Results:     results,
		TotalCount:  len(results),
		CreateCount: createCount,
		UpdateCount: updateCount,
		DeleteCount: deleteCount,
		SkipCount:   skipCount,
	}, nil
}

func (ds *dbDataSource) SecurityScanData() SecurityScanPageData {
	// DB-backed datasource does not have access to flattened traits.
	// Return empty data — fleet DataSource handles this.
	return SecurityScanPageData{}
}

func (ds *dbDataSource) SecurityScanTraits(_ string) (map[string]string, bool) {
	return nil, false
}

func (ds *dbDataSource) RiskData(_ time.Duration) RiskSummaryData {
	// DB-backed datasource does not have access to flattened traits.
	// Return empty data — fleet DataSource handles this.
	return RiskSummaryData{}
}

// --- Task and Workflow page types ---

// TaskRow is a single task run displayed in the task list.
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

// TaskPageData holds data for the task list page.
type TaskPageData struct {
	PageData
	Tasks        []TaskRow
	StatusFilter string
	TaskCount    int
}

// TaskTargetRow is a per-envoy result in the task detail page.
type TaskTargetRow struct {
	ID         string
	EnvoyID    string
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
	PushRunID  string
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

// AuditPageData holds data for the audit log page.
type AuditPageData struct {
	PageData
	Events      []AuditEventRow
	TypeFilter  string
	QueryString string
	// Chain verification status (populated on page load).
	ChainValid  bool
	EntryCount  int64
	FirstBroken int64
	ChainError  string
}

// AuditEventRow is a single event displayed in the audit table.
type AuditEventRow struct {
	Timestamp string
	Type      string
	Operator  string
	Source    string // grpc, rest, auth, peer
	Summary   string
	DetailURL string // link to detail page (task, workflow) or empty
	Payload   string // extra detail for audit entries
}

// OperationsPageData holds data for the combined tasks + workflows page.
type OperationsPageData struct {
	PageData
	Tab            string
	Tasks          []TaskRow
	Workflows      []WorkflowRow
	TaskCount      int
	WorkflowCount  int
	StatusFilter   string
}

// Operations renders the combined tasks + workflows page.
func (h *Handler) Operations(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "tasks"
	}
	statusFilter := r.URL.Query().Get("status")
	ctx := r.Context()

	var taskRows []TaskRow
	var workflowRows []WorkflowRow

	if tab == "tasks" {
		pushRuns, _ := h.queries.ListPushRuns(ctx, 100)
		for _, t := range pushRuns {
			if statusFilter != "" && t.Status != statusFilter {
				continue
			}
			taskRows = append(taskRows, taskRowFromDB(t))
		}
	} else {
		runs, _ := h.queries.ListWorkflowRuns(ctx, 100)
		for _, wf := range runs {
			if statusFilter != "" && wf.Status != statusFilter {
				continue
			}
			workflowRows = append(workflowRows, workflowRowFromDB(wf))
		}
	}

	data := OperationsPageData{
		PageData:      PageData{Title: "Tasks & Workflows", ActiveNav: "operations"},
		Tab:           tab,
		Tasks:         taskRows,
		Workflows:     workflowRows,
		TaskCount:     len(taskRows),
		WorkflowCount: len(workflowRows),
		StatusFilter:  statusFilter,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "operations", data)
}

// TaskList renders the task history page.
func (h *Handler) TaskList(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	ctx := r.Context()

	pushRuns, err := h.queries.ListPushRuns(ctx, 100)
	if err != nil {
		slog.Error("failed to list task runs", "error", err)
	}

	var rows []TaskRow
	for _, t := range pushRuns {
		if statusFilter != "" && t.Status != statusFilter {
			continue
		}
		rows = append(rows, taskRowFromDB(t))
	}

	data := TaskPageData{
		PageData:     PageData{Title: "Tasks", ActiveNav: "task"},
		Tasks:        rows,
		StatusFilter: statusFilter,
		TaskCount:    len(rows),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "task", data)
}

// TaskDetail renders the task detail page with per-envoy results.
func (h *Handler) TaskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	pushRun, err := h.queries.GetPushRun(ctx, id)
	if err != nil {
		http.Error(w, "task run not found", http.StatusNotFound)
		return
	}

	targets, err := h.queries.ListPushRunTargets(ctx, id)
	if err != nil {
		slog.Error("failed to list task run targets", "error", err)
	}

	var tgtRows []TaskTargetRow
	for _, t := range targets {
		row := TaskTargetRow{
			ID:       t.ID,
			EnvoyID:  t.EnvoyID,
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
		PageData:  PageData{Title: "Task Detail", ActiveNav: "task"},
		Task:      taskRowFromDB(pushRun),
		Targets:   tgtRows,
		IsRunning: pushRun.Status == "running" || pushRun.Status == "pending",
	}
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
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
		if s.PushRunID.Valid {
			row.PushRunID = s.PushRunID.String
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
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "workflow-detail", data)
}

// Query renders the live query page.
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	data := QueryPageData{
		PageData: PageData{Title: "Live Query", ActiveNav: "query"},
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "query", data)
}

// Audit renders the audit log page with chain verification status.
func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	typeFilter := r.URL.Query().Get("type")

	// Build query string for HTMX auto-refresh.
	qs := ""
	if typeFilter != "" {
		qs = "type=" + typeFilter
	}

	// Fetch audit entries from all sources (same logic as API).
	var events []AuditEventRow

	// Tamper-evident audit entries (hash-chained).
	if typeFilter == "" || typeFilter == "audit_entry" {
		entries, err := h.queries.ListAuditEntries(ctx, 100)
		if err != nil {
			slog.Error("failed to list audit entries", "error", err)
		}
		for _, e := range entries {
			events = append(events, AuditEventRow{
				Timestamp: e.Timestamp.Local().Format("2006-01-02 15:04:05"),
				Type:      e.EventType,
				Operator:  e.Actor,
				Source:    e.Source,
				Summary:   e.Summary,
				Payload:   e.Payload,
			})
		}
	}

	// Config publishes.
	if typeFilter == "" || typeFilter == "config_publish" {
		publishes, err := h.queries.ListConfigPublishes(ctx, 100)
		if err == nil {
			for _, p := range publishes {
				var changed []string
				_ = json.Unmarshal([]byte(p.ChangedFiles), &changed)
				summary := fmt.Sprintf("%d files changed", len(changed))
				if len(changed) > 0 {
					summary += ": " + strings.Join(changed, ", ")
				}
				events = append(events, AuditEventRow{
					Timestamp: p.PublishedAt.Local().Format("2006-01-02 15:04:05"),
					Type:      "config_publish",
					Operator:  p.Operator,
					Source:    "rest",
					Summary:   summary,
				})
			}
		}
	}

	// Task runs.
	if typeFilter == "" || typeFilter == "task_run" {
		pushRuns, err := h.queries.ListPushRuns(ctx, 100)
		if err == nil {
			for _, pr := range pushRuns {
				events = append(events, AuditEventRow{
					Timestamp: pr.CreatedAt.Local().Format("2006-01-02 15:04:05"),
					Type:      "task_run",
					Operator:  pr.Operator,
					Source:    "rest",
					Summary:   pr.Command + " on " + pr.TargetGlob,
					DetailURL: "/task/" + pr.ID,
				})
			}
		}
	}

	// Workflow runs.
	if typeFilter == "" || typeFilter == "workflow_run" {
		workflowRuns, err := h.queries.ListWorkflowRuns(ctx, 100)
		if err == nil {
			for _, wr := range workflowRuns {
				events = append(events, AuditEventRow{
					Timestamp: wr.CreatedAt.Local().Format("2006-01-02 15:04:05"),
					Type:      "workflow_run",
					Operator:  wr.Operator,
					Source:    "rest",
					Summary:   wr.WorkflowName + " (" + wr.Status + ")",
					DetailURL: "/workflows/" + wr.ID,
				})
			}
		}
	}

	// Sort by timestamp descending.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp > events[j].Timestamp
	})

	// Verify chain integrity on page load.
	var chainValid bool
	var entryCount, firstBroken int64
	var chainError string
	result, err := audit.Verify(ctx, h.queries)
	if err != nil {
		chainError = err.Error()
	} else {
		chainValid = result.Valid
		entryCount = result.EntryCount
		firstBroken = result.FirstBroken
		chainError = result.Error
	}

	data := AuditPageData{
		PageData:    PageData{Title: "Audit Log", ActiveNav: "audit"},
		Events:      events,
		TypeFilter:  typeFilter,
		QueryString: qs,
		ChainValid:  chainValid,
		EntryCount:  entryCount,
		FirstBroken: firstBroken,
		ChainError:  chainError,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "audit", data)
}

// AuditVerify returns an HTML fragment showing the hash chain verification
// result with individual entries. Called via HTMX from the Verify Chain button.
func (h *Handler) AuditVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	result, err := audit.Verify(ctx, h.queries)
	if err != nil {
		fmt.Fprintf(w, `<span class="badge badge--failure">Verification error: %s</span>`, err.Error())
		return
	}

	if !result.Valid {
		fmt.Fprintf(w, `<div class="mt-1"><span class="badge badge--failure">CHAIN BROKEN at entry #%d</span><p class="text-muted mt-1">%s</p></div>`, result.FirstBroken, result.Error)
		return
	}

	// Chain is valid — show the entries.
	entries, err := h.queries.ListAuditEntries(ctx, 50)
	if err != nil {
		fmt.Fprintf(w, `<span class="badge badge--success">Chain valid (%d entries)</span>`, result.EntryCount)
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<div class="mt-1"><span class="badge badge--success">Chain valid — %d entries verified</span>`, result.EntryCount))
	b.WriteString(`<table class="table mt-1"><thead><tr><th>#</th><th>Timestamp</th><th>Type</th><th>Source</th><th>Actor</th><th>Summary</th><th>Hash</th></tr></thead><tbody>`)
	for _, e := range entries {
		hash := e.Hash
		if len(hash) > 12 {
			hash = hash[:12] + "..."
		}
		b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%s</td><td><span class="badge badge--info">%s</span></td><td><span class="badge badge--%s">%s</span></td><td>%s</td><td>%s</td><td class="text-muted">%s</td></tr>`,
			e.ID, e.Timestamp.Local().Format("2006-01-02 15:04:05"), e.EventType, e.Source, e.Source, e.Actor, e.Summary, hash))
	}
	b.WriteString(`</tbody></table></div>`)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, b.String())
}

// taskRowFromDB converts a DB PushRun to a TaskRow.
func taskRowFromDB(t db.PushRun) TaskRow {
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

// SpannerSpokeRow represents a spoke in the spanner UI table.
type SpannerSpokeRow struct {
	ID             string
	Addr           string
	Patterns       []string
	Healthy        bool
	EnvoyCount    int32
	Converged      int32
	RecurringDrift int32
	Errors         int32
	LostContact    int32
	NoData         int32
	ConvergencePct  float32
	LatencyMs      int64
	ConfigVersion  string
	Error          string
}

// SpannerPageData holds data for the spanner page.
type SpannerPageData struct {
	PageData
	SpokeCount          int
	HealthyCount        int
	TotalEnvoys         int32
	TotalConverged      int32
	TotalRecurringDrift int32
	TotalErrors         int32
	ConvergencePct       float32
	Spokes              []SpannerSpokeRow
	// HA peer replication status
	PeerEnabled       bool
	PeerRole          string
	PeerPrimaryAddr   string
	PeerLastSync      string
	PeerFilesSynced   int64
	PeerConfigVersion uint64
}

// Spanner renders the spanner hub status page.
func (h *Handler) Spanner(w http.ResponseWriter, r *http.Request) {
	statuses := h.hub.SpokeStatuses(r.Context())

	var spokes []SpannerSpokeRow
	healthyCount := 0
	var totalEnvoys, totalCompliant, totalRecurringDrift, totalErrors int32
	for _, s := range statuses {
		spokes = append(spokes, SpannerSpokeRow{
			ID:             s.ID,
			Addr:           s.Addr,
			Patterns:       s.Patterns,
			Healthy:        s.Healthy,
			EnvoyCount:    s.EnvoyCount,
			Converged:      s.Converged,
			RecurringDrift: s.RecurringDrift,
			Errors:         s.Errors,
			LostContact:    s.LostContact,
			NoData:         s.NoData,
			ConvergencePct:  s.ConvergencePct,
			LatencyMs:      s.LatencyMs,
			ConfigVersion:  fmt.Sprintf("%d", s.ConfigVersion),
			Error:          s.Error,
		})
		if s.Healthy {
			healthyCount++
		}
		totalEnvoys += s.EnvoyCount
		totalCompliant += s.Converged
		totalRecurringDrift += s.RecurringDrift
		totalErrors += s.Errors
	}

	var convPct float32
	if totalEnvoys > 0 {
		convPct = float32(totalCompliant) / float32(totalEnvoys) * 100
	}

	data := SpannerPageData{
		PageData:            PageData{Title: "Spanner", ActiveNav: "spanner"},
		SpokeCount:          len(spokes),
		HealthyCount:        healthyCount,
		TotalEnvoys:         totalEnvoys,
		TotalConverged:      totalCompliant,
		TotalRecurringDrift: totalRecurringDrift,
		TotalErrors:         totalErrors,
		ConvergencePct:       convPct,
		Spokes:              spokes,
	}

	// Add HA peer replication status if available.
	if h.peerSvc != nil {
		data.PeerEnabled = true
		if resp, err := h.peerSvc.Status(r.Context(), nil); err == nil {
			data.PeerRole = resp.Role
			data.PeerPrimaryAddr = resp.PrimaryAddr
			data.PeerFilesSynced = resp.FilesSynced
			data.PeerConfigVersion = resp.ConfigVersion
			if resp.LastSyncUnix > 0 {
				data.PeerLastSync = time.Unix(resp.LastSyncUnix, 0).Local().Format("2006-01-02 15:04:05")
			} else {
				data.PeerLastSync = "never"
			}
		}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "spanner", data)
}

// SpokeDetailPageData holds data for the spoke detail page.
type SpokeDetailPageData struct {
	PageData
	SpokeID        string
	Addr           string
	Healthy        bool
	EnvoyCount    int32
	Converged      int32
	RecurringDrift int32
	Errors         int32
	LostContact    int32
	NoData         int32
	ConvergencePct  float32
	LatencyMs      int64
	Envoys        []SpokeDetailEnvoyRow
	Runs           []SpokeDetailRunRow
}

// SpokeDetailEnvoyRow is a envoy on a spoke.
type SpokeDetailEnvoyRow struct {
	ID        string
	Hostname  string
	IPAddress string
	LastSeen  string
	Revoked   bool
}

// SpokeDetailRunRow is a run on a spoke.
type SpokeDetailRunRow struct {
	ID           string
	EnvoyID     string
	Status       string
	ModuleCount  int32
	ChangedCount int32
	FailedCount  int32
	StartedAt    string
}

// SpokeDetail renders the spoke detail page with envoys and runs.
func (h *Handler) SpokeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	statuses := h.hub.SpokeStatuses(ctx)
	var spoke *spanner.SpokeStatus
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

	// Fetch envoys and runs from this spoke.
	envoys, _ := h.hub.SpokeListEnvoys(ctx, id)
	runs, _ := h.hub.SpokeListRuns(ctx, id, 20)

	var envoyRows []SpokeDetailEnvoyRow
	for _, t := range envoys {
		row := SpokeDetailEnvoyRow{
			ID:        t.Id,
			Hostname:  t.Hostname,
			IPAddress: t.IpAddress,
			Revoked:   t.Revoked,
		}
		if t.LastSeen != nil {
			row.LastSeen = t.LastSeen.AsTime().Local().Format("2006-01-02 15:04:05")
		}
		envoyRows = append(envoyRows, row)
	}

	var runRows []SpokeDetailRunRow
	for _, run := range runs {
		row := SpokeDetailRunRow{
			ID:           run.Id,
			EnvoyID:     run.EnvoyId,
			Status:       run.Status,
			ModuleCount:  run.ModuleCount,
			ChangedCount: run.ChangedCount,
			FailedCount:  run.FailedCount,
		}
		if run.StartedAt != nil {
			row.StartedAt = run.StartedAt.AsTime().Local().Format("2006-01-02 15:04:05")
		}
		runRows = append(runRows, row)
	}

	data := SpokeDetailPageData{
		PageData:       PageData{Title: "Spoke: " + id, ActiveNav: "spanner"},
		SpokeID:        spoke.ID,
		Addr:           spoke.Addr,
		Healthy:        spoke.Healthy,
		EnvoyCount:    spoke.EnvoyCount,
		Converged:      spoke.Converged,
		RecurringDrift: spoke.RecurringDrift,
		Errors:         spoke.Errors,
		LostContact:    spoke.LostContact,
		NoData:         spoke.NoData,
		ConvergencePct:  spoke.ConvergencePct,
		LatencyMs:      spoke.LatencyMs,
		Envoys:        envoyRows,
		Runs:           runRows,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "spoke-detail", data)
}

// AskDriftRow represents a package drift entry for the ask/security page.
type AskDriftRow struct {
	Hostname         string
	PackageName      string
	InstalledVersion string
	DesiredVersion   string
}

// SecurityScanPageData holds aggregated security scan results for the ask/security page.
type SecurityScanPageData struct {
	// Fleet-wide totals (raw findings — same CVE affecting multiple packages counts multiple times).
	TrivyCritical  int64
	TrivyHigh      int64
	TrivyMedium    int64
	TrivyLow       int64
	// Unique CVE counts (deduplicated across the fleet).
	UniqueCritical int64
	UniqueHigh     int64
	LynisAvgScore  int64
	LynisWarnings  int64
	RootkitClean   int64
	RootkitWarning int64
	ClamavClean    int64
	ClamavInfected int64
	AideClean      int64
	AideChanged    int64
	ScannedHosts   int64
	TotalHosts     int64
	CVEs           []CVEEntry         // deduplicated critical+high CVEs (one per package)
	PerHost        []SecurityHostRow  // per-envoy breakdown
}

// CVEEntry is a single CVE grouped by package (latest CVE per package).
type CVEEntry struct {
	CVEID    string // highest CVE ID for this package
	Package  string // package name (or KB article for Windows)
	Severity string // "critical" or "high" (lowercase)
	Others   int64  // count of additional CVEs suppressed for this package
}

// SecurityHostRow holds per-envoy security scan summary.
type SecurityHostRow struct {
	ID             string
	Hostname       string
	TrivyCritical  int64
	TrivyHigh      int64
	TrivyMedium    int64
	TrivyLow       int64
	LynisScore     int64
	LynisWarnings  int64
	LynisSuggestions int64
	RootkitClean   bool
	RkhunterWarnings int64
	ChkrootkitInfected int64
	ClamavClean    bool
	ClamavInfected int64
	AideClean      bool
	AideAdded      int64
	AideRemoved    int64
	AideChanged    int64
	CVEs           []CVEEntry
	LastScan       string
}

// AskPage renders the AI convergence & security dashboard.
func (h *Handler) AskPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	convData := h.dataSource.ConvergenceData(h.baseInterval)

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

	secData := h.dataSource.SecurityScanData()

	// Query timeout = 2.5× check-in interval (agent needs to poll, promote, respond).
	queryTimeoutSec := int(h.baseInterval.Seconds()*2.5 + 10)
	if queryTimeoutSec < 30 {
		queryTimeoutSec = 30
	}

	// Load 30-day security posture trend.
	var cveTrend, hardeningTrend []float64
	if h.queries != nil {
		history, err := h.queries.ListSecurityPostureHistory(ctx, 30)
		if err == nil {
			// Reverse for chronological order (query returns DESC).
			for i := len(history) - 1; i >= 0; i-- {
				cveTrend = append(cveTrend, float64(history[i].CveCritical+history[i].CveHigh))
				hardeningTrend = append(hardeningTrend, float64(history[i].AvgHardening))
			}
		}
	}

	data := struct {
		PageData
		ConvergencePct    float64
		Total            int64
		Converged        int64
		RecurringDrift   int64
		Errors           int64
		LostContact      int64
		NoData           int64
		DriftCount       int
		DriftRows        []AskDriftRow
		Security         SecurityScanPageData
		QueryTimeoutSec  int
		CVETrend         []float64
		HardeningTrend   []float64
	}{
		PageData:        PageData{Title: "Security", ActiveNav: "ask"},
		ConvergencePct:   convData.ConvergencePct,
		Total:           convData.Total,
		Converged:       convData.Converged,
		RecurringDrift:  convData.RecurringDrift,
		Errors:          convData.Errors,
		LostContact:     convData.LostContact,
		NoData:          convData.NoData,
		DriftCount:      len(driftRows),
		DriftRows:       driftRows,
		Security:        secData,
		QueryTimeoutSec: queryTimeoutSec,
		CVETrend:        cveTrend,
		HardeningTrend:  hardeningTrend,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "ask", data)
}

// AskStatus renders the convergence+security left-panel fragment for HTMX polling.
func (h *Handler) AskStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	convData := h.dataSource.ConvergenceData(h.baseInterval)

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

	secData := h.dataSource.SecurityScanData()

	// Load 30-day security posture trend (same as AskPage).
	var cveTrend, hardeningTrend []float64
	if h.queries != nil {
		history, err := h.queries.ListSecurityPostureHistory(r.Context(), 30)
		if err == nil {
			for i := len(history) - 1; i >= 0; i-- {
				cveTrend = append(cveTrend, float64(history[i].CveCritical+history[i].CveHigh))
				hardeningTrend = append(hardeningTrend, float64(history[i].AvgHardening))
			}
		}
	}

	data := struct {
		ConvergencePct    float64
		Total            int64
		Converged        int64
		RecurringDrift   int64
		Errors           int64
		LostContact      int64
		NoData           int64
		DriftCount       int
		DriftRows        []AskDriftRow
		Security         SecurityScanPageData
		CVETrend         []float64
		HardeningTrend   []float64
	}{
		ConvergencePct:   convData.ConvergencePct,
		Total:           convData.Total,
		Converged:       convData.Converged,
		RecurringDrift:  convData.RecurringDrift,
		Errors:          convData.Errors,
		LostContact:     convData.LostContact,
		NoData:          convData.NoData,
		DriftCount:      len(driftRows),
		DriftRows:       driftRows,
		Security:        secData,
		CVETrend:        cveTrend,
		HardeningTrend:  hardeningTrend,
	}
	h.renderer.RenderFragment(w, "ask", "ask-status", data)
}

// CVESearchFragment handles GET /ui/fragments/cve-search — searches fleet for a CVE.
// Delegates to the CVE API handler for the actual search, then renders HTML.
func (h *Handler) CVESearchFragment(w http.ResponseWriter, r *http.Request) {
	cveID := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("cve")))
	if cveID == "" {
		w.Write([]byte(`<p class="text-muted">Enter a CVE ID to search.</p>`))
		return
	}

	// Use the CVE handler's search logic via the cveSearcher interface.
	if h.cveSearcher == nil {
		w.Write([]byte(`<p class="text-muted">CVE search not available.</p>`))
		return
	}

	totalHosts, matches := h.cveSearcher.SearchCVE(cveID)

	if totalHosts == 0 {
		fmt.Fprintf(w, `<p class="text-success">No hosts affected by %s</p>`, template.HTMLEscapeString(cveID))
		return
	}

	fmt.Fprintf(w, `<p><strong>%s</strong> — %d host(s) affected</p>`, template.HTMLEscapeString(cveID), totalHosts)
	fmt.Fprint(w, `<div class="table-container"><table class="table"><thead><tr><th>Hostname</th><th>Package</th><th>Installed</th><th>Fixed</th><th>Severity</th><th>Scanner</th></tr></thead><tbody>`)
	for _, m := range matches {
		fixed := m.Fixed
		if fixed == "" {
			fixed = "-"
		}
		sevClass := ""
		switch strings.ToLower(m.Severity) {
		case "critical":
			sevClass = "text-danger"
		case "high":
			sevClass = "text-warning"
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td class="mono">%s</td><td>%s</td><td>%s</td><td class="%s">%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(m.Hostname),
			template.HTMLEscapeString(m.Package),
			template.HTMLEscapeString(m.Installed),
			template.HTMLEscapeString(fixed),
			sevClass,
			template.HTMLEscapeString(m.Severity),
			template.HTMLEscapeString(m.Scanner))
	}
	fmt.Fprint(w, `</tbody></table></div>`)
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


// Admin handlers removed — all admin functions are CLI-only (vigogo).

// License renders the license status page.
func (h *Handler) License(w http.ResponseWriter, r *http.Request) {
	data := LicensePageData{
		PageData: PageData{Title: "License", ActiveNav: "license"},
	}

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		data.StageNum = int(s.Stage)
		data.Stage = s.Stage.String()
		data.ActiveEnvoys = s.ActiveEnvoys
		data.MaxEnvoys = s.MaxEnvoys
		data.ExpiresAt = s.ExpiresAt
		data.DaysRemaining = s.DaysRemaining
		data.Violation = s.Violation
		data.IsFreeTier = h.enforcer.IsFreeLicense()
		if s.MaxEnvoys > 0 {
			data.UsagePct = float64(s.ActiveEnvoys) / float64(s.MaxEnvoys) * 100
		}
		if s.License != nil {
			data.Tier = s.License.TierDisplayName()
			data.CustomerName = s.License.CustomerName
			data.LicenseID = s.License.LicenseID
		}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "license", data)
}
