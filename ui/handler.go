// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hobnob/hobnob/server/ai"
	"github.com/hobnob/hobnob/server/auth"
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/license"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ComplianceData(baseInterval time.Duration) CompliancePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	ThrallsData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) ThrallsPageData
	ThrallDetailData(thrallID string, baseInterval time.Duration) (ThrallDetailPageData, error)
	ThrallRunsData(thrallID string) (ThrallRunsFragmentData, error)
	ThrallFactsData(thrallID string) (ThrallFactsFragmentData, error)
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
	docsDir      string
	docsNav      []DocsNavSection
	enforcer     *license.Enforcer
	authEnabled  bool
	authMethod   string
}

// NewHandler creates a UI handler wired to real database queries.
// The assistant parameter is optional — pass nil to disable the Ask page.
// The customDS parameter is optional — when provided, it replaces the default
// DB-backed DataSource (used by FleetIndex integration).
// The docsDir parameter specifies the path to the docs directory for the
// built-in documentation browser (pass "" to disable).
func NewHandler(queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, caFile, agentDir, version string, assistant *ai.Assistant, hub *federation.Hub, customDS DataSource, docsDir string, authMethod string, enforcer *license.Enforcer) (*Handler, error) {
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
		baseInterval: baseInterval,
		serverAddr:   serverAddr,
		caFile:       caFile,
		agentDir:     agentDir,
		version:      version,
		assistant:    assistant,
		hub:          hub,
		docsDir:      docsDir,
		enforcer:     enforcer,
		authEnabled:  authMethod != "" && authMethod != "none",
		authMethod:   authMethod,
	}

	if docsDir != "" {
		h.docsNav = buildDocsNav(docsDir)
	}

	return h, nil
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
	mux.HandleFunc("GET /ui/fragments/thrall-runs/{id}", h.ThrallRunsFragment)
	mux.HandleFunc("GET /ui/fragments/thrall-facts/{id}", h.ThrallFactsFragment)

	// Federation pages (only registered when running as hub)
	if h.hub != nil {
		mux.HandleFunc("GET /federation", h.Federation)
		mux.HandleFunc("GET /federation/spokes/{id}", h.SpokeDetail)
	}

	// Docs pages (only registered when docsDir is configured and exists)
	if h.docsDir != "" {
		mux.HandleFunc("GET /docs", h.DocsIndex)
		mux.HandleFunc("GET /docs/{path...}", h.DocsPage)
	}

	// AI chat page (only registered when assistant is available)
	if h.assistant != nil {
		mux.HandleFunc("GET /ask", h.AskPage)
		mux.HandleFunc("POST /ui/ask", h.AskStream)
	}

	// License page
	mux.HandleFunc("GET /license", h.License)

	// Admin pages (only when auth is enabled)
	if h.authEnabled {
		mux.HandleFunc("GET /admin/users", h.AdminUsers)
		mux.HandleFunc("GET /admin/approved", h.AdminApproved)
		mux.HandleFunc("GET /admin/tokens", h.AdminTokens)
		mux.HandleFunc("GET /admin/webhooks", h.AdminWebhooks)
		mux.HandleFunc("GET /admin/doctor", h.AdminDoctor)
		mux.HandleFunc("GET /admin/backup", h.AdminBackup)
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
	AuthEnabled       bool
	AuthMethod        string
	CurrentUser       *AuthUser
	LicenseWarning    string // non-empty when license is near or over limits
}

// LicensePageData holds data for the license status page.
type LicensePageData struct {
	PageData
	Tier          string
	CustomerName  string
	LicenseID     string
	MaxThralls    int
	ActiveThralls int
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
	Thralls       []ThrallRow
	StatusFilter  string
	ThrallCount   int
	HasMore       bool
	NextCursor    string // hostname of last row (cursor for next page)
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

// ThrallRunsFragmentData holds data for the lazy-loaded runs fragment.
type ThrallRunsFragmentData struct {
	Runs         []ThrallDetailRunRow
	DriftHistory []DriftPoint
}

// ThrallFactsFragmentData holds data for the lazy-loaded facts fragment.
type ThrallFactsFragmentData struct {
	Facts map[string]any
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

// --- Docs types ---

// DocsNavSection represents a section in the docs navigation tree.
type DocsNavSection struct {
	Title string
	Path  string // e.g. "getting-started"
	Pages []DocsNavPage
}

// DocsNavPage represents a single page within a docs section.
type DocsNavPage struct {
	Title string
	Path  string // e.g. "getting-started/installation"
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

// AdminUsersPageData holds data for the admin users page.
type AdminUsersPageData struct {
	PageData
	Users []AdminUserRow
}

// AdminUserRow is a single user displayed in the admin users table.
type AdminUserRow struct {
	ID          string
	Username    string
	DisplayName string
	Email       string
	Role        string
	CreatedAt   time.Time
}

// AdminApprovedPageData holds data for the admin approved patterns page.
type AdminApprovedPageData struct {
	PageData
}

// AdminTokensPageData holds data for the admin tokens page.
type AdminTokensPageData struct {
	PageData
	ServerAddr string
}

// AdminWebhooksPageData holds data for the admin webhooks page.
type AdminWebhooksPageData struct {
	PageData
}

// AdminDoctorPageData holds data for the admin doctor page.
type AdminDoctorPageData struct {
	PageData
}

// AdminBackupPageData holds data for the admin backup page.
type AdminBackupPageData struct {
	PageData
	BackupURL    string
	SyncInterval string
	Retention    string
}

// docsSectionOrder defines the display order for documentation sections.
var docsSectionOrder = []string{
	"getting-started",
	"concepts",
	"executors",
	"traits",
	"cli",
	"api",
	"operations",
	"ui",
	"ai",
	"examples",
	"migration",
	"architecture",
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
		for _, pf := range pageFiles {
			if pf.IsDir() || !strings.HasSuffix(pf.Name(), ".md") || pf.Name() == "_index.md" {
				continue
			}

			pageName := strings.TrimSuffix(pf.Name(), ".md")
			pageTitle := extractMDTitle(filepath.Join(docsDir, sectionPath, pf.Name()))
			if pageTitle == "" {
				pageTitle = strings.ReplaceAll(pageName, "-", " ")
				pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
			}

			pages = append(pages, DocsNavPage{
				Title: pageTitle,
				Path:  sectionPath + "/" + pageName,
			})
		}

		sort.Slice(pages, func(i, j int) bool {
			return pages[i].Title < pages[j].Title
		})

		sectionMap[sectionPath] = DocsNavSection{
			Title: sectionTitle,
			Path:  sectionPath,
			Pages: pages,
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
		if i == 0 {
			for _, sec := range h.docsNav {
				if sec.Path == part {
					title = sec.Title
					break
				}
			}
		}
		if i < len(parts)-1 {
			breadcrumbs = append(breadcrumbs, DocsCrumb{
				Title: title,
				Path:  strings.Join(parts[:i+1], "/"),
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

// setCommon applies version, AI-enabled, federation, auth, and license flags to page data.
func (h *Handler) setCommon(pd *PageData) {
	pd.Version = h.version
	pd.AIEnabled = h.assistant != nil
	pd.FederationEnabled = h.hub != nil
	pd.AuthEnabled = h.authEnabled
	pd.AuthMethod = h.authMethod

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		if s.Stage > 0 {
			pd.LicenseWarning = s.Violation
		} else if s.MaxThralls > 0 {
			threshold := float64(s.MaxThralls) * 0.9
			if float64(s.ActiveThralls) >= threshold {
				pd.LicenseWarning = fmt.Sprintf("License usage at %d/%d nodes (%d%%) — approaching limit",
					s.ActiveThralls, s.MaxThralls, s.ActiveThralls*100/s.MaxThralls)
			}
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
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance", data)
}

// Thralls renders the thralls list page with optional status filter and pagination.
func (h *Handler) Thralls(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	after := r.URL.Query().Get("after")
	data := h.dataSource.ThrallsData(h.baseInterval, statusFilter, after, 100)
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "thralls", data)
}

// ThrallDetail renders the thrall detail page shell. Runs and facts are loaded
// asynchronously via HTMX fragment endpoints for instant page rendering.
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
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "thrall-detail", data)
}

// ThrallRunsFragment renders the runs + drift history fragment for a thrall (HTMX lazy load).
func (h *Handler) ThrallRunsFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid thrall ID", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.ThrallRunsData(idStr)
	if err != nil {
		http.Error(w, "thrall not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "thrall-detail", "thrall-runs-fragment", data)
}

// ThrallFactsFragment renders the facts/traits fragment for a thrall (HTMX lazy load).
func (h *Handler) ThrallFactsFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid thrall ID", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.ThrallFactsData(idStr)
	if err != nil {
		http.Error(w, "thrall not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "thrall-detail", "thrall-facts-fragment", data)
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

// ThrallsTable renders the thralls table fragment for HTMX polling.
func (h *Handler) ThrallsTable(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	after := r.URL.Query().Get("after")
	data := h.dataSource.ThrallsData(h.baseInterval, statusFilter, after, 100)
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

func (ds *dbDataSource) ThrallsData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int) ThrallsPageData {
	ctx := context.Background()
	thralls, err := ds.queries.ListThralls(ctx)
	if err != nil {
		slog.Error("failed to list thralls", "error", err)
		return ThrallsPageData{PageData: PageData{Title: "Thralls", ActiveNav: "thralls"}}
	}

	// Sort by hostname for consistent pagination.
	sort.Slice(thralls, func(i, j int) bool {
		return thralls[i].Hostname < thralls[j].Hostname
	})

	var rows []ThrallRow
	for _, n := range thralls {
		if n.Revoked != 0 {
			continue
		}
		if afterHostname != "" && n.Hostname <= afterHostname {
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

	return ThrallsPageData{
		PageData:     PageData{Title: "Thralls", ActiveNav: "thralls"},
		Thralls:      rows,
		StatusFilter: statusFilter,
		ThrallCount:  len(rows),
		HasMore:      hasMore,
		NextCursor:   nextCursor,
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

	// Shell only — runs and facts are loaded via HTMX fragments.
	return ThrallDetailPageData{
		PageData: PageData{Title: "Thrall: " + thrall.Hostname, ActiveNav: "thralls"},
		Thrall: ThrallRow{
			ID:         thrall.ID,
			Hostname:   thrall.Hostname,
			IPAddress:  thrall.IpAddress,
			EnrolledAt: thrall.EnrolledAt,
			LastSeen:   lastSeen,
			Status:     status,
		},
	}, nil
}

func (ds *dbDataSource) ThrallRunsData(thrallID string) (ThrallRunsFragmentData, error) {
	ctx := context.Background()
	thrall, err := ds.queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		return ThrallRunsFragmentData{}, fmt.Errorf("thrall not found: %w", err)
	}

	dbRuns, err := ds.queries.ListRunsByThrall(ctx, db.ListRunsByThrallParams{
		ThrallID: thrallID,
		Limit:    20,
	})
	if err != nil {
		slog.Error("failed to list runs for thrall", "error", err)
		return ThrallRunsFragmentData{}, nil
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

	var runs []ThrallDetailRunRow
	var driftHistory []DriftPoint
	for _, r := range dbRuns {
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
			Results: resultsByRun[r.ID],
		})
		driftHistory = append(driftHistory, DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
		})
	}

	return ThrallRunsFragmentData{Runs: runs, DriftHistory: driftHistory}, nil
}

func (ds *dbDataSource) ThrallFactsData(thrallID string) (ThrallFactsFragmentData, error) {
	ctx := context.Background()
	facts := make(map[string]any)
	cf, err := ds.queries.GetLatestThrallTraits(ctx, thrallID)
	if err == nil {
		_ = json.Unmarshal([]byte(cf.Traits), &facts)
	}
	return ThrallFactsFragmentData{Facts: facts}, nil
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
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
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
	h.setCommonWithRequest(&data.PageData, r)
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

// AdminUsers renders the admin users management page.
func (h *Handler) AdminUsers(w http.ResponseWriter, r *http.Request) {
	// Require admin role.
	if h.authEnabled {
		u := auth.UserFromContext(r.Context())
		if u == nil || !u.IsAdmin() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	users, err := h.queries.ListUsers(r.Context())
	if err != nil {
		slog.Error("listing users for admin page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var rows []AdminUserRow
	for _, u := range users {
		rows = append(rows, AdminUserRow{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Email:       u.Email,
			Role:        u.Role,
			CreatedAt:   u.CreatedAt,
		})
	}

	data := AdminUsersPageData{
		PageData: PageData{Title: "Users", ActiveNav: "admin-users"},
		Users:    rows,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-users", data)
}

// requireAdmin checks that the current user is an authenticated admin.
// Returns false (and writes a 403) if not authorized.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.authEnabled {
		u := auth.UserFromContext(r.Context())
		if u == nil || !u.IsAdmin() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
	}
	return true
}

// AdminApproved renders the admin approved enrollment patterns page.
func (h *Handler) AdminApproved(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	data := AdminApprovedPageData{
		PageData: PageData{Title: "Approved Patterns", ActiveNav: "admin-approved"},
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-approved", data)
}

// AdminTokens renders the admin tokens management page.
func (h *Handler) AdminTokens(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	data := AdminTokensPageData{
		PageData:   PageData{Title: "Tokens", ActiveNav: "admin-tokens"},
		ServerAddr: h.serverAddr,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-tokens", data)
}

// AdminWebhooks renders the admin webhooks management page.
func (h *Handler) AdminWebhooks(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	data := AdminWebhooksPageData{
		PageData: PageData{Title: "Webhooks", ActiveNav: "admin-webhooks"},
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-webhooks", data)
}

// AdminDoctor renders the admin health check page.
func (h *Handler) AdminDoctor(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	data := AdminDoctorPageData{
		PageData: PageData{Title: "Health Check", ActiveNav: "admin-doctor"},
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-doctor", data)
}

// AdminBackup renders the admin backup management page.
func (h *Handler) AdminBackup(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	data := AdminBackupPageData{
		PageData: PageData{Title: "Backup", ActiveNav: "admin-backup"},
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "admin-backup", data)
}

// License renders the license status page.
func (h *Handler) License(w http.ResponseWriter, r *http.Request) {
	data := LicensePageData{
		PageData: PageData{Title: "License", ActiveNav: "license"},
	}

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		data.StageNum = int(s.Stage)
		data.Stage = s.Stage.String()
		data.ActiveThralls = s.ActiveThralls
		data.MaxThralls = s.MaxThralls
		data.ExpiresAt = s.ExpiresAt
		data.DaysRemaining = s.DaysRemaining
		data.Violation = s.Violation
		data.IsFreeTier = h.enforcer.IsFreeLicense()
		if s.MaxThralls > 0 {
			data.UsagePct = float64(s.ActiveThralls) / float64(s.MaxThralls) * 100
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
