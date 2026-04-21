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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	licfmt "git.alexander4.io/vigo-lic/license"
	"github.com/google/uuid"
	"github.com/daniel-alexander4/vigo/server/ai"
	"github.com/daniel-alexander4/vigo/server/audit"
	"github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/swarm"
	"github.com/daniel-alexander4/vigo/server/peer"
	"github.com/daniel-alexander4/vigo/server/spanner"
	"github.com/daniel-alexander4/vigo/server/vigorepomesh"
)

// DataSource abstracts data fetching for UI handlers.
// Implemented by dbDataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval time.Duration) DashboardPageData
	ConvergenceData(baseInterval time.Duration) ConvergencePageData
	DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData
	EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int, search string) EnvoysPageData
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
	RiskPostureData(baseInterval time.Duration) RiskPosturePageData
	CertExpirySummary() CertExpirySummary
	RunningCVESummary() RunningCVESummary
}

// RunningCVESummary aggregates `cve_running_correlation.summary.*`
// counts fleet-wide for the dashboard "Running CVEs" stat card.
// Sourced from per-envoy on-agent correlation traits; an envoy that
// has never emitted the trait contributes zero.
type RunningCVESummary struct {
	Total         int
	Critical      int
	High          int
	Medium        int
	Low           int
	HostsAffected int
}

// CertExpirySummary aggregates `cert_lifecycle` trait rows fleet-wide
// for the dashboard "expiring certs" stat card and related drill-downs.
// Expired (days_remaining < 0) plus expiring within 30 days contribute
// to the total; fresh certs are ignored.
type CertExpirySummary struct {
	// Total certs with days_remaining < 30 (includes already-expired).
	ExpiringSoon int
	// Subset counts for drill-down detail.
	Expired      int
	ExpiringIn7  int
	ExpiringIn30 int
	// Number of distinct envoys with at least one expiring or expired cert.
	HostsAffected int
	// Per-envoy breakdown — only hosts with at least one expiring/expired cert.
	PerHost []CertHostRow
}

// CertHostRow holds a single envoy's cert-expiry breakdown for the
// /security page drill-down table.
type CertHostRow struct {
	ID           string
	Hostname     string
	Expired      int
	ExpiringIn7  int
	ExpiringIn30 int
}

// ReportBuilder builds compliance reports for HTMX fragment rendering.
// Set via SetReportBuilder after construction to avoid import cycles.
type ReportBuilder struct {
	// Executive returns the executive summary report data (any is *api.ExecutiveReport).
	Executive func(r *http.Request) (any, error)
	// Framework returns a per-framework report (any is *api.ComplianceReport).
	Framework func(r *http.Request, slug string) (any, error)
	// EnvoyFrameworkStatus returns per-envoy compliance status for one
	// framework, using the same math as the framework fleet-row
	// partition. Single source of truth so the envoy-detail Compliance
	// tab and the /compliance/<slug> fleet row render identical numbers
	// for the same envoy. Returns ok=false when slug is unknown.
	EnvoyFrameworkStatus func(r *http.Request, hostname, slug string) (EnvoyFrameworkStatus, bool)
}

// EnvoyFrameworkStatus is one envoy's bucketed per-framework totals.
// Partition rule: a finding is counted once, in this priority order —
// module-tagged coverage, then architectural satisfaction (Status =
// "satisfied" with no module tag, e.g. bcrypt auth, mTLS, unique UUIDs),
// then waiver, then partial, else missing. Covered + Waived + Partial +
// Missing = Total.
type EnvoyFrameworkStatus struct {
	Covered         int
	Waived          int
	Partial         int
	Missing         int
	Total           int
	CoveragePercent float64
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
	agentVersion  string
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
	branding        config.BrandingSection
	cveSearcher        CVESearcher
	healthChecker      HealthChecker
	serverHostname     string
	globalObserveOnly  bool
	scrierEnabled      bool
	serverConfigPath   string
	approvalRequired   bool
	swarmTracker       *swarm.PeerTracker
	swarmFleetIndex    SwarmHostnameResolver
	swarmConfig        config.SwarmSection
	serverEnvoyHost    string
	traitLookup        TraitLookup
	fleetCache         *FleetCache
	vigorepoFleet      VigorepoFleetSource
}

// VigorepoFleetSource surfaces the aggregated fleet-wide vigorepo
// view. Used by the swarm list (to decorate vigorepo rows with
// mirror summary pills) and the swarm detail page (to render the
// Vigorepo Mesh card). Implemented by `*api.VigorepoHandler` in
// the main binary; a tiny interface so the UI package doesn't
// depend on the full aggregator handler type.
type VigorepoFleetSource interface {
	FleetSnapshot(ctx context.Context) (*vigorepomesh.Fleet, error)
}

// SetVigorepoFleet wires the fleet aggregator into the UI handler.
// Called from gateway.go after both handlers are constructed.
func (h *Handler) SetVigorepoFleet(src VigorepoFleetSource) {
	h.vigorepoFleet = src
}

// SetAgentVersion sets the detected agent binary version for display on the dashboard.
func (h *Handler) SetAgentVersion(v string) {
	h.agentVersion = v
}

// SetScrierEnabled marks whether Scrier remote access is available.
func (h *Handler) SetScrierEnabled(enabled bool) {
	h.scrierEnabled = enabled
}

// CVESearcher provides CVE impact search across the fleet.
// Implemented by *api.CVEHandler via adapter in main.go. When
// runningOnly is true, the search drops hosts that have no
// running-binary match for the CVE in their cve_running_correlation
// trait — the caller wants "affects running code" not just "package
// installed."
type CVESearcher interface {
	SearchCVE(cveID string, runningOnly bool) (totalHosts int, hosts []CVEMatch)
}

// HealthCheck is a single diagnostic check result for the health page.
type HealthCheck struct {
	Name    string
	Status  string // "pass", "warn", "fail", "skip"
	Message string
}

// HealthChecker runs server diagnostics.
// Implemented via adapter around *api.DoctorHandler in main.go.
type HealthChecker interface {
	RunChecks() (status string, checks []HealthCheck)
}

// TraitLookup provides read access to envoy traits for UI display.
// Implemented via adapter around FleetIndex in main.go.
type TraitLookup interface {
	// EnvoyTrait returns the value of a flattened trait key for an envoy, or "".
	EnvoyTrait(envoyID, key string) string
}

// CVEMatch is a single host affected by a CVE.
type CVEMatch struct {
	Hostname  string
	Package   string
	Installed string
	Fixed     string
	Severity  string
	Scanner   string
	// RunningBinaries enumerates currently-executing binaries on this
	// host affected by the CVE, sourced from the on-agent
	// cve_running_correlation trait. Empty when the host reports the
	// CVE but none of its running binaries trace back to an affected
	// package — common when a library is installed but nothing is
	// currently linked against it.
	RunningBinaries []CVERunningBinaryRef
}

// CVERunningBinaryRef is the UI-facing reference to a single running
// binary affected by a CVE.
type CVERunningBinaryRef struct {
	BinaryPath     string
	Package        string
	PackageVersion string
	PidCount       int
	Deleted        bool
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

// SetHealthChecker attaches the health check capability.
func (h *Handler) SetHealthChecker(hc HealthChecker) {
	h.healthChecker = hc
}

// SetBranding configures custom branding for the web UI.
func (h *Handler) SetBranding(b config.BrandingSection) {
	h.branding = b
}

// SetServerHostname sets the hostname used to identify the server's own envoy
// for the compliance page. Read from server.yaml server.hostname.
func (h *Handler) SetServerHostname(hostname string) {
	h.serverHostname = hostname
}

// SetServerConfigPath wires the on-disk path to server.yaml. The Server
// Config UI page re-reads this file on every request so the page reflects the
// live file rather than the struct loaded at startup.
func (h *Handler) SetServerConfigPath(path string) {
	h.serverConfigPath = path
}

// SetPublishApprovalRequired wires the publish.approval_required flag from
// server.yaml. Gates the Approvals sidebar entry — it only renders when
// approval workflows are actually in use.
func (h *Handler) SetPublishApprovalRequired(v bool) {
	h.approvalRequired = v
}

// SetGlobalObserveOnly sets the global observe-only flag from server.yaml.
func (h *Handler) SetGlobalObserveOnly(v bool) {
	h.globalObserveOnly = v
	if ds, ok := h.dataSource.(*dbDataSource); ok {
		ds.globalObserveOnly = v
	}
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
	mux.HandleFunc("GET /envoys", h.Envoys)
	mux.HandleFunc("GET /envoys/{id}", h.EnvoyDetail)
	mux.HandleFunc("GET /envoys/{id}/trace", h.EnvoyTrace)
	mux.HandleFunc("GET /envoys/{id}/trace/viz", h.EnvoyTraceViz)
	mux.HandleFunc("GET /ui/api/envoys/{id}/security", h.EnvoySecurityTraits)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /runs/{id}/viz", h.RunDetailViz)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /server/config", h.ServerConfig)
	mux.HandleFunc("GET /server/config/{section}", h.ServerConfigSection)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /risk", h.RiskPosture)
	mux.HandleFunc("GET /compliance", h.Compliance)
	mux.HandleFunc("GET /compliance/executive", h.ComplianceExecutive)
	mux.HandleFunc("GET /integrations", h.Integrations)
	mux.HandleFunc("GET /integrations/{slug}", h.IntegrationDetailPage)
	mux.HandleFunc("GET /ui/fragments/integration/{slug}", h.IntegrationDetail)
	mux.HandleFunc("GET /compliance/evidence", h.EvidenceCenter)
	mux.HandleFunc("GET /compliance/evidence/bundle", h.EvidenceBundleDownload)
	mux.HandleFunc("POST /api/v1/compliance/waivers", h.CreateWaiver)
	mux.HandleFunc("DELETE /api/v1/compliance/waivers/{id}", h.DeleteWaiver)
	mux.HandleFunc("GET /compliance/waivers", h.ComplianceWaivers)
	mux.HandleFunc("GET /compliance/{framework}", h.ComplianceFramework)
	mux.HandleFunc("GET /compliance/{framework}/viz", h.ComplianceFrameworkViz)
	mux.HandleFunc("GET /task", h.TaskList)
	mux.HandleFunc("GET /task/{id}", h.TaskDetail)
	mux.HandleFunc("GET /workflows", h.Workflows)
	mux.HandleFunc("GET /workflows/{id}", h.WorkflowDetail)
	mux.HandleFunc("GET /audit", h.Audit)
	mux.HandleFunc("GET /audit/verify", h.AuditVerify)
	// Swarm pages — always registered because SetSwarm is called after RegisterRoutes.
	// The handlers check h.swarmTracker at request time.
	mux.HandleFunc("GET /swarm", h.Swarm)
	mux.HandleFunc("GET /swarm/{sha256}", h.Swarm)
	mux.HandleFunc("GET /ui/fragments/swarm-envoys", h.SwarmEnvoysFragment)

	mux.HandleFunc("GET /sandgorgon", h.Sandgorgon)
	mux.HandleFunc("GET /sandgorgon/assets/{id}", h.SandgorgonAsset)
	mux.HandleFunc("GET /sandgorgon/certificates", h.SandgorgonCertificates)
	mux.HandleFunc("GET /ui/fragments/sandgorgon-assets", h.SandgorgonAssetsFragment)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /viz", h.DashboardViz)
	mux.HandleFunc("GET /ui/fragments/dashboard-fleet", h.DashboardFleet)
	mux.HandleFunc("GET /ui/fragments/dashboard-viz-fleet", h.DashboardVizFleetFragment)
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
	mux.HandleFunc("GET /ui/fragments/security-status", h.SecurityStatus)
	mux.HandleFunc("GET /ui/fragments/cve-search", h.CVESearchFragment)
	mux.HandleFunc("GET /ui/fragments/envoys-table", h.EnvoysTable)
	mux.HandleFunc("GET /ui/fragments/envoy-drift/{id}", h.EnvoyDriftFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-runs/{id}", h.EnvoyRunsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-traits/{id}", h.EnvoyTraitsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-config/{id}", h.EnvoyConfigFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-trace/{id}", h.EnvoyTraceFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-compliance/{id}", h.EnvoyComplianceFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-attack-surface/{id}", h.EnvoyAttackSurfaceFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-sessions/{id}", h.EnvoyScrierSessionsFragment)
	mux.HandleFunc("GET /envoys/{id}/compliance/{framework}", h.EnvoyComplianceDetail)
	mux.HandleFunc("GET /ui/fragments/envoy-standard-coverage/{id}/{framework}", h.EnvoyStandardCoverageFragment)
	mux.HandleFunc("GET /ui/fragments/run-results/{id}", h.RunResultsFragment)

	// Under Review fragment endpoints
	mux.HandleFunc("GET /ui/fragments/inventory", h.InventoryFragment)
	mux.HandleFunc("GET /ui/fragments/inventory-group", h.InventoryGroupFragment)
	mux.HandleFunc("GET /ui/fragments/inventory-search", h.InventorySearchFragment)
	mux.HandleFunc("GET /ui/fragments/inventory-trait-index", h.InventoryTraitIndexFragment)
	mux.HandleFunc("GET /ui/fragments/inventory-explore", h.InventoryExploreFragment)
	mux.HandleFunc("GET /ui/fragments/inventory-explore-table", h.InventoryExploreTableFragment)
	mux.HandleFunc("GET /ui/export/inventory-explore.ods", h.InventoryExploreODSExport)
	mux.HandleFunc("GET /ui/fragments/approvals", h.ApprovalsFragment)
	mux.HandleFunc("GET /ui/fragments/health", h.HealthFragment)

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
	mux.HandleFunc("GET /security", h.SecurityPage)
	if h.assistant != nil {
		mux.HandleFunc("POST /ui/ask", h.AskStream)
	}

	// Combined operations page (tasks + workflows)
	mux.HandleFunc("GET /operations", h.Operations)

	// License page
	mux.HandleFunc("GET /license", h.License)

	// Support page
	mux.HandleFunc("GET /support", h.Support)

	// Under Review pages
	mux.HandleFunc("GET /inventory", h.Inventory)
	mux.HandleFunc("GET /inventory/hardware/{hwtype}/{label}", h.InventoryHardwareDetail)
	mux.HandleFunc("GET /inventory/{id}", h.InventoryDetail)
	mux.HandleFunc("GET /ui/export/inventory.csv", h.InventoryCSVExport)
	mux.HandleFunc("GET /approvals", h.Approvals)
	mux.HandleFunc("GET /health", h.Health)

	// Admin functions are CLI-only (vigocli). No admin pages in web UI.
}

// --- Page data types ---

// PageData holds common fields for all page templates.
type PageData struct {
	Title             string
	ActiveNav         string
	BodyClass         string // extra CSS class on <body>
	Version           string
	PollInterval      string // HTMX polling interval, e.g. "30s" (2× check-in interval)
	PollIntervalSlow  string // slower poll for less-critical fragments, e.g. "60s"
	CheckInInterval   string // configured check-in interval, e.g. "15s", "5m0s"
	AIEnabled         bool
	AIProviderName    string // "Claude", "Ollama", "OpenAI", etc.
	SpannerEnabled    bool
	DocsEnabled       bool
	PeerEnabled       bool
	SwarmEnabled      bool
	AuthEnabled       bool
	AuthMethod        string
	CurrentUser       *AuthUser
	LicenseWarning    string // non-empty when license is near or over limits
	PendingChanges    int    // count of pending config changes awaiting approval
	ApprovalRequired  bool   // publish.approval_required in server.yaml — gates the Approvals sidebar entry
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

// SupportPageData holds data for the support options page. The tier
// rows mirror license.Tiers at build time; the active tier is
// determined from the license's MaxEnvoys band.
type SupportPageData struct {
	PageData
	Tiers            []license.Tier
	ActiveTier       string // name of the tier matching this license
	CustomerName     string
	LicenseID        string
	MaxEnvoys        int
	IsFreeTier       bool
	ContactEmail     string
	CriticalEligible bool // true when active tier qualifies for the Critical add-on
	CriticalFrom     string
	CriticalResponse string
	CriticalHours    string
	CriticalChannel  string
	CriticalPricing  string
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

// AgentPlatform represents an available agent binary (OS/architecture pair).
type AgentPlatform struct {
	OS   string
	Arch string
}

// DashboardPageData holds data for the dashboard page.
type DashboardPageData struct {
	PageData
	AgentVersion    string
	AgentPlatforms  []AgentPlatform
	TotalEnvoys     int
	ActiveEnvoys    int
	RecentFailures  int
	DriftRate       float64
	// LicenseCapacity drives the top-of-dashboard capacity banner. Empty
	// LicenseCapacityLevel means the banner is not rendered. "warning"
	// renders an orange notice (≥90% of cap), "critical" renders a red
	// notice (100% of cap — next enrollment will be rejected).
	LicenseCapacityLevel   string // "", "warning", or "critical"
	LicenseCapacityMessage string
	LicenseCapacityActive  int
	LicenseCapacityMax     int
	RecentRuns          []RunRow
	FleetRows           []FleetRow
	FleetRowsCapped     bool // true when fleet table is capped (more envoys exist)
	FleetRowsRemaining  int  // how many envoys not shown
	// Security posture
	CVECritical     int64
	CVEHigh         int64
	UniqueCVEs      int64
	HardeningScore  int64
	RootkitWarnings int64
	MalwareDetected int64
	IntegrityIssues int64
	// Module convergence
	ConvergencePct   float64
	Converged       int
	Relapsed        int
	Diverged        int
	CompFailed      int
	Offline     int
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	WaivedControls       int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore    int
	RiskCritical    int
	RiskHigh        int
	RiskMedium      int
	RiskLow         int
	RiskTopEnvoys []RiskEnvoyRow
	// Convergence sparkline
	ConvergenceSparklineData []float64
	ConvergenceSparklineMin  int
	ConvergenceSparklineMax  int
	// Compliance sparkline
	ComplianceSparklineData []float64
	ComplianceSparklineMin  int
	ComplianceSparklineMax  int
	// Swarm distribution (used by /viz and /fleet; no longer rendered on
	// the dashboard landing page).
	SwarmFiles []SwarmFileRow
	// Certificate lifecycle — fleet-wide expiry counts.
	CertsExpiringSoon  int
	CertsExpired       int
	CertsExpiringIn7   int
	CertsExpiringIn30  int
	CertHostsAffected  int
	// Running-CVE fleet aggregates.
	RunningCVEsTotal         int
	RunningCVEsCritical      int
	RunningCVEsHigh          int
	RunningCVEsMedium        int
	RunningCVEsLow           int
	RunningCVEHostsAffected  int
}

// SwarmFileRow is a summary row for a swarm file on the dashboard.
type SwarmFileRow struct {
	SHA256      string
	Label       string
	Size        string
	Source      string
	Complete    int
	Downloading int
	Pending     int
	Skipped     int
	Total       int
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
	AvgScore       int
	Critical       int
	High           int
	Medium         int
	Low            int
	TopRisks       []RiskEnvoyRow
	SparklineData  []float64
	SparklineMin   int
	SparklineMax   int
}

// RiskPosturePageData holds data for the dedicated risk posture page.
type RiskPosturePageData struct {
	PageData
	AvgScore       int
	MaxScore       int
	Critical       int
	High           int
	Medium         int
	Low            int
	LevelFilter    string
	SparklineData  []float64
	SparklineMin   int
	SparklineMax   int
	SparklineStart string // oldest date in the trend
	SparklineEnd   string // newest date in the trend
	SparklineDays  int
	Envoys         []RiskEnvoyDetail
	TopFactors     []RiskFactorSummary
}

// RiskEnvoyDetail is a single envoy with risk score and top factor.
type RiskEnvoyDetail struct {
	EnvoyID    string
	Hostname   string
	Score      int
	Level      string
	TopFactor  string
	FactorList []RiskFactorSummary
}

// RiskFactorSummary summarizes a risk factor across the fleet.
type RiskFactorSummary struct {
	Name       string
	TotalPoints int
	EnvoyCount int
}

// FleetRow is a envoy row for the dashboard fleet status table.
type FleetRow struct {
	ID          string
	Hostname    string
	IP          string
	LastSeen    time.Time
	Status      string
	LastRunID   string
	FailurePct  float64
	FailedCount int64
	ModuleCount int64
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
	FailurePct   float64 // 0-100: failed_count / module_count * 100
}

// ConvergencePageData holds module convergence data (envoy enforcement status).
type ConvergencePageData struct {
	Total          int64
	Converged      int64
	Relapsed       int64
	Diverged       int64
	Degraded       int64
	Failed         int64
	Offline    int64
	NoData         int64
	ConvergencePct float64 // converged / (total - offline) * 100
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
	// Fleet-wide aggregate.
	HasStandards         bool
	StandardsPct         float64 // satisfied / total controls * 100
	SatisfiedControls    int
	WaivedControls       int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Waiver alerts.
	TotalActiveWaivers   int
	ExpiringWaivers      int // waivers expiring within 30 days
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
	UniqueCVEs      int64
	HardeningScore  int64
	RootkitWarnings int64
	ConvergencePct   float64
	Converged       int
	Relapsed        int
	Diverged        int
	CompFailed      int
	Offline     int
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	WaivedControls       int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore    int
	RiskCritical    int
	RiskHigh        int
	RiskMedium         int
	RiskLow int
	// Convergence sparkline
	ConvergenceSparklineData []float64
	ConvergenceSparklineMin  int
	ConvergenceSparklineMax  int
	// Compliance sparkline
	ComplianceSparklineData []float64
	ComplianceSparklineMin  int
	ComplianceSparklineMax  int
	// Certificate lifecycle — fleet-wide expiry counts surfaced on the
	// Security Posture dashboard card.
	CertsExpiringSoon  int
	CertsExpired       int
	CertsExpiringIn7   int
	CertsExpiringIn30  int
	CertHostsAffected  int
	// Running-CVE fleet aggregates from the cve_running_correlation
	// trait. Surfaced as a dashboard stat card distinct from the
	// installed-CVE counts above (UniqueCVEs et al.).
	RunningCVEsTotal         int
	RunningCVEsCritical      int
	RunningCVEsHigh          int
	RunningCVEsMedium        int
	RunningCVEsLow           int
	RunningCVEHostsAffected  int
}

// EnvoyRow is a single envoy displayed in the envoys table.
type EnvoyRow struct {
	ID           string
	Hostname     string
	IPAddress    string
	EnrolledAt   time.Time
	LastSeen     time.Time
	LastRunAt    time.Time
	LastRunID    string
	Status       string
	FailurePct   float64 // 0-100: failed_count / module_count * 100
	ModuleCount  int64
	FailedCount  int64
	Offline      bool
	ObserveOnly  bool // observe-only mode: drift reporting only
	AgentVersion string
	DiskPercent  int    // root filesystem available percentage
	DiskStatus   string // "ok", "warn", "critical"
	RepoStatus   string // "ok", "degraded", ""
	BrokenRepos  int    // number of broken package repos
}

// EnvoysPageData holds data for the envoys list page.
type EnvoysPageData struct {
	PageData
	Envoys       []EnvoyRow
	StatusFilter  string
	Search        string
	SortColumn    string // active sort column (hostname, last_seen, status)
	SortDir       string // "asc" or "desc"
	EnvoyCount   int
	TotalEnvoys  int
	HasMore       bool
	NextCursor    string // hostname of last row (cursor for next page)
	// Top-of-page summary stats. ActiveEnvoys = TotalEnvoys - CountOffline;
	// DriftRate = 100 * (Relapsed + Diverged) / Total. Aggregates here rather
	// than on the dashboard avoid a round-trip for operators who live on
	// /envoys; they're cheap to compute from the counts we already have.
	ActiveEnvoys int
	DriftRate    float64
	// Status counts for filter buttons.
	CountConverged    int
	CountRelapsed     int
	CountDiverged     int
	CountDegraded     int
	CountFailed       int
	CountOffline  int
	CountNoData       int
	CountObserving    int
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
	Envoy          EnvoyRow
	Runs           []EnvoyDetailRunRow
	Traits         map[string]any
	DriftHistory   []DriftPoint
	ScrierEnabled  bool
	// Cloud metadata (populated from traits if available).
	CloudProvider     string // "aws", "gcp", "azure", etc. or "" if not cloud
	CloudRegion       string
	CloudZone         string
	CloudInstanceType string
	CloudInstanceID   string
	CloudAccountID    string
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
	ModuleCount  int64
	FailurePct   float64
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
	SortColumn   string
	SortDir      string
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

// ServerConfigCardRow is one card on the /server/config index page — one per
// top-level section of server.yaml.
type ServerConfigCardRow struct {
	Name        string // yaml key ("swarm", "auth", ...)
	Description string // one-line summary from the doc: struct tag
	Enabled     bool   // present and not explicitly disabled in the live file
	Present     bool   // present in the live file (even if disabled)
}

// ServerConfigPageData powers the card grid at /server/config.
type ServerConfigPageData struct {
	PageData
	ConfigPath string
	Cards      []ServerConfigCardRow
	Enabled    int
	Available  int
}

// ServerConfigFieldRow is one row in the "current values" or "available
// options" table on the detail page.
type ServerConfigFieldRow struct {
	Name        string
	Description string
	GoType      string
	Kind        string
	Options     string
	Default     string
	Example     string // concrete example or usage hint from the `example:` struct tag
	Value       string // current value from the live YAML (detail page only)
	HasValue    bool   // true when the live file defines this field
}

// ServerConfigSubsectionRow is one nested sub-section (e.g. an integration
// provider) rendered inside the detail page.
type ServerConfigSubsectionRow struct {
	Name        string
	Description string
	Enabled     bool
	Present     bool
	CurrentYAML string // pretty-printed live YAML for this subsection; empty when absent
	Fields      []ServerConfigFieldRow
}

// ServerConfigSectionPageData powers the detail page at /server/config/{section}.
type ServerConfigSectionPageData struct {
	PageData
	ConfigPath  string
	Section     string
	Description string
	DocsURL     string // relative path under /docs/ from the section's docs: struct tag
	Enabled     bool
	Present     bool
	CurrentYAML string                      // pretty-printed live YAML for this section; empty when absent
	ExampleYAML string                      // annotated starter template; rendered when CurrentYAML is empty
	Current     []ServerConfigFieldRow      // only populated when the section is enabled
	Available   []ServerConfigFieldRow      // every schema field (also shown when disabled)
	Subsections []ServerConfigSubsectionRow // nested structs (integrations, auth.oidc, etc.)
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
	Gated       bool             // requires paid license to view
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
	IsFreeTier     bool
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

// docsGatedSections lists documentation sections that require a paid license.
// Free-tier users see the nav entries but get an upgrade prompt instead of content.
var docsGatedSections = map[string]bool{
	"compliance": true,
	"concepts":   true,
	"security":   true,
}

// isDocsPathGated returns true if the given docs path falls under a gated section.
func isDocsPathGated(pagePath string) bool {
	section := strings.SplitN(pagePath, "/", 2)[0]
	return docsGatedSections[section]
}

var gatedDocsContent = template.HTML(`<div class="card" style="max-width:640px;margin:2rem auto;text-align:center;padding:3rem 2rem">
<h2>Commercial License Required</h2>
<p>This documentation is available with a paid Vigo license.</p>
<pre style="text-align:left"><code>vigocli license install --file=license.lic</code></pre>
<p style="margin-top:1.5rem"><a href="/license" class="btn">View License Status</a></p>
</div>`)

// buildDocsBreadcrumbs builds breadcrumb navigation from a docs page path.
func buildDocsBreadcrumbs(pagePath string, nav []DocsNavSection) ([]DocsCrumb, string) {
	parts := strings.Split(pagePath, "/")
	var breadcrumbs []DocsCrumb
	for i, part := range parts {
		title := strings.ReplaceAll(part, "-", " ")
		title = strings.ReplaceAll(title, "_", " ")
		crumbPath := strings.Join(parts[:i+1], "/")
		if i == 0 {
			for _, sec := range nav {
				if sec.Path == part {
					title = sec.Title
					break
				}
			}
		} else if i == 1 {
			for _, sec := range nav {
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
			breadcrumbs = append(breadcrumbs, DocsCrumb{Title: title, Path: crumbPath})
		} else {
			breadcrumbs = append(breadcrumbs, DocsCrumb{Title: title})
		}
	}
	currentSection := ""
	if len(parts) > 0 {
		currentSection = parts[0]
	}
	return breadcrumbs, currentSection
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
			Gated:       docsGatedSections[sectionPath],
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
		PageData:   PageData{Title: "Documentation", ActiveNav: "docs"},
		Content:    template.HTML(content),
		NavTree:    h.docsNav,
		IsFreeTier: h.enforcer != nil && h.enforcer.IsFreeLicense(),
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

	// Check license gating: free-tier users get an upgrade prompt for gated sections.
	isFreeTier := h.enforcer != nil && h.enforcer.IsFreeLicense()
	if isFreeTier && isDocsPathGated(pagePath) {
		breadcrumbs, currentSection := buildDocsBreadcrumbs(pagePath, h.docsNav)
		data := DocsPageData{
			PageData:       PageData{Title: "Docs - Commercial License Required", ActiveNav: "docs"},
			Content:        gatedDocsContent,
			Breadcrumbs:    breadcrumbs,
			NavTree:        h.docsNav,
			CurrentPath:    pagePath,
			CurrentSection: currentSection,
			IsFreeTier:     true,
		}
		h.setCommonWithRequest(&data.PageData, r)
		h.renderer.Render(w, "docs", data)
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

	// Watermark gated content for paid-tier users (visible footer + invisible ZW chars).
	if !isFreeTier && isDocsPathGated(pagePath) && h.enforcer != nil {
		status := h.enforcer.GetStatus()
		licID := status.License.LicenseID
		custName := status.License.CustomerName
		content = licfmt.EncodeWatermark(content, licID)
		content += fmt.Sprintf(`<div class="docs-watermark">Licensed to %s — %s</div>`,
			template.HTMLEscapeString(custName),
			template.HTMLEscapeString(licID))
	}

	breadcrumbs, currentSection := buildDocsBreadcrumbs(pagePath, h.docsNav)

	data := DocsPageData{
		PageData:       PageData{Title: "Docs - " + breadcrumbs[len(breadcrumbs)-1].Title, ActiveNav: "docs"},
		Content:        template.HTML(content),
		Breadcrumbs:    breadcrumbs,
		NavTree:        h.docsNav,
		CurrentPath:    pagePath,
		CurrentSection: currentSection,
		IsFreeTier:     isFreeTier,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "docs", data)
}

// setCommon applies version, AI-enabled, spanner, auth, and license flags to page data.
func (h *Handler) setCommon(pd *PageData) {
	pd.Version = h.version
	poll := 30 * time.Second
	if h.baseInterval < 15*time.Second {
		poll = 2 * h.baseInterval
	}
	pd.PollInterval = fmt.Sprintf("%.0fs", poll.Seconds())
	slow := 2 * poll
	if slow < 60*time.Second {
		slow = 60 * time.Second
	}
	pd.PollIntervalSlow = fmt.Sprintf("%.0fs", slow.Seconds())
	pd.CheckInInterval = h.baseInterval.String()
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
	pd.SwarmEnabled = h.swarmTracker != nil
	pd.AuthEnabled = h.authEnabled
	pd.AuthMethod = h.authMethod
	pd.OrgName = h.branding.OrgName
	pd.CustomLogo = h.branding.LogoPath != ""
	pd.CustomFavicon = h.branding.FaviconPath != ""
	pd.Theme = h.branding.Theme
	if pd.Theme == "" {
		pd.Theme = "light"
	}
	pd.ApprovalRequired = h.approvalRequired

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

// agentPlatforms scans agentDir for available OS/arch agent binaries.
func (h *Handler) agentPlatforms() []AgentPlatform {
	if h.agentDir == "" {
		return nil
	}
	var platforms []AgentPlatform
	osEntries, err := os.ReadDir(h.agentDir)
	if err != nil {
		return nil
	}
	for _, osEntry := range osEntries {
		if !osEntry.IsDir() {
			continue
		}
		goos := osEntry.Name()
		archEntries, err := os.ReadDir(filepath.Join(h.agentDir, goos))
		if err != nil {
			continue
		}
		for _, archEntry := range archEntries {
			if !archEntry.IsDir() {
				continue
			}
			goarch := archEntry.Name()
			bin := "vigo"
			if goos == "windows" {
				bin = "vigo.exe"
			}
			if _, err := os.Stat(filepath.Join(h.agentDir, goos, goarch, bin)); err == nil {
				platforms = append(platforms, AgentPlatform{OS: goos, Arch: goarch})
			}
		}
	}
	sort.Slice(platforms, func(i, j int) bool {
		if platforms[i].OS != platforms[j].OS {
			return platforms[i].OS < platforms[j].OS
		}
		return platforms[i].Arch < platforms[j].Arch
	})
	return platforms
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
	data := h.dashboardData()

	// Enrich with version and platform data.
	data.AgentVersion = h.agentVersion
	if data.AgentVersion == "" {
		data.AgentVersion = h.version
	}
	data.AgentPlatforms = h.agentPlatforms()

	// Enrich with security posture data (all CVE counts are unique/deduplicated).
	sec := h.dataSource.SecurityScanData()
	data.CVECritical = sec.UniqueCritical
	data.CVEHigh = sec.UniqueHigh
	data.UniqueCVEs = sec.UniqueCVEs
	data.HardeningScore = sec.LynisAvgScore
	data.RootkitWarnings = sec.RootkitWarning
	data.MalwareDetected = sec.ClamavInfected
	data.IntegrityIssues = sec.AideChanged

	// Enrich with standards compliance data.
	stds := h.computeStandardsSummary()
	data.StandardsPct = stds.Pct
	data.SatisfiedControls = stds.Satisfied
	data.WaivedControls = stds.Waived
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

	// Enrich with swarm distribution data (still populated for /viz and
	// /fleet which share this data type; the dashboard page no longer
	// renders the swarm table itself).
	if h.swarmTracker != nil {
		data.SwarmFiles = h.swarmFiles()
	}

	// Enrich with sparkline history.
	data.ConvergenceSparklineData, data.ConvergenceSparklineMin, data.ConvergenceSparklineMax = h.convergenceSparkline()
	data.ComplianceSparklineData, data.ComplianceSparklineMin, data.ComplianceSparklineMax = h.complianceSparkline()

	// Certificate expiry summary.
	certs := h.dataSource.CertExpirySummary()
	data.CertsExpiringSoon = certs.ExpiringSoon
	data.CertsExpired = certs.Expired
	data.CertsExpiringIn7 = certs.ExpiringIn7
	data.CertsExpiringIn30 = certs.ExpiringIn30
	data.CertHostsAffected = certs.HostsAffected

	// Running-CVE fleet aggregates.
	runCVEs := h.dataSource.RunningCVESummary()
	data.RunningCVEsTotal = runCVEs.Total
	data.RunningCVEsCritical = runCVEs.Critical
	data.RunningCVEsHigh = runCVEs.High
	data.RunningCVEsMedium = runCVEs.Medium
	data.RunningCVEsLow = runCVEs.Low
	data.RunningCVEHostsAffected = runCVEs.HostsAffected

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "dashboard", data)
}

// convergenceSparkline returns the last 30 days of fleet convergence %
// (converged / active), oldest-first so the sparkline reads left-to-right.
// Returns zeros when the history table is empty or unreadable.
func (h *Handler) convergenceSparkline() (data []float64, min, max int) {
	if h.queries == nil {
		return nil, 0, 0
	}
	history, err := h.queries.ListConvergenceHistory(context.Background(), 30)
	if err != nil {
		slog.Error("dashboard: list convergence_history", "error", err)
		return nil, 0, 0
	}
	data = make([]float64, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		row := history[i]
		active := row.Total - row.Offline
		if active > 0 {
			data = append(data, float64(row.Converged)/float64(active)*100)
		}
	}
	min, max = sparklineBounds(data)
	return data, min, max
}

// complianceSparkline returns the last 30 days of fleet compliance coverage %
// averaged across all frameworks, oldest-first. When compliance_history is
// empty (no frameworks configured, or server is < 24h old) the slice is nil
// and the caller suppresses the sparkline.
func (h *Handler) complianceSparkline() (data []float64, min, max int) {
	if h.queries == nil {
		return nil, 0, 0
	}
	// 30 days × up to 20 frameworks — matches the executive report window
	// so the snapshot table sees a single consumer for sizing assumptions.
	rows, err := h.queries.ListComplianceHistoryAllFrameworks(context.Background(), 30*20)
	if err != nil {
		slog.Error("dashboard: list compliance_history", "error", err)
		return nil, 0, 0
	}
	if len(rows) == 0 {
		return nil, 0, 0
	}
	// Average score_percent per snapshot_date. sqlc returns rows ordered
	// by snapshot_date DESC, framework — group by date then reverse so
	// the sparkline reads oldest→newest.
	byDate := make(map[string]*struct{ sum, n float64 })
	order := make([]string, 0, 30)
	for _, r := range rows {
		agg, ok := byDate[r.SnapshotDate]
		if !ok {
			agg = &struct{ sum, n float64 }{}
			byDate[r.SnapshotDate] = agg
			order = append(order, r.SnapshotDate)
		}
		agg.sum += r.ScorePercent
		agg.n++
	}
	data = make([]float64, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		agg := byDate[order[i]]
		if agg.n > 0 {
			data = append(data, agg.sum/agg.n)
		}
	}
	min, max = sparklineBounds(data)
	return data, min, max
}

// sparklineBounds returns rounded min/max of a percentage series.
func sparklineBounds(data []float64) (min, max int) {
	if len(data) == 0 {
		return 0, 0
	}
	mn, mx := data[0], data[0]
	for _, v := range data {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return int(mn + 0.5), int(mx + 0.5)
}

// dashboardData builds dashboard page data using the fleet cache (single query)
// instead of loading all envoys + N+1 per-envoy run queries.
func (h *Handler) dashboardData() DashboardPageData {
	data := DashboardPageData{
		PageData: PageData{Title: "Dashboard", ActiveNav: "dashboard"},
	}

	if h.fleetCache != nil {
		counts := h.fleetCache.Counts()
		data.TotalEnvoys = counts.Total
		data.ActiveEnvoys = counts.Total - counts.Offline
		data.RecentFailures = counts.Failed
		if counts.Total > 0 {
			data.DriftRate = float64(counts.Relapsed+counts.Diverged) / float64(counts.Total) * 100
		}
		data.ConvergencePct = 0
		if counts.Total > 0 {
			data.ConvergencePct = float64(counts.Converged) / float64(counts.Total) * 100
		}
		data.Converged = counts.Converged
		data.Relapsed = counts.Relapsed
		data.Diverged = counts.Diverged
		data.CompFailed = counts.Failed
		data.Offline = counts.Offline

		// Fleet table: worst 50 envoys.
		worst := h.fleetCache.WorstEntries(50)
		data.FleetRows = make([]FleetRow, 0, len(worst))
		for _, e := range worst {
			data.FleetRows = append(data.FleetRows, FleetRow{
				ID:         e.ID,
				Hostname:    e.Hostname,
				IP:          e.IPAddress,
				LastSeen:    e.LastSeen,
				Status:      e.Status,
				LastRunID:   e.LastRunID,
				FailurePct:  e.FailurePct,
				FailedCount: e.FailedCount,
				ModuleCount: e.ModuleCount,
			})
		}
		data.FleetRowsCapped = len(worst) < counts.Total
		data.FleetRowsRemaining = counts.Total - len(worst)
	} else {
		// Fallback: use legacy DataSource (e.g. FleetIndex custom DS).
		legacy := h.dataSource.DashboardData(h.baseInterval)
		return legacy
	}

	// License capacity banner: render once we know the live count and the
	// license cap. Orange at ≥90% of cap, red at 100% (next enrollment
	// will be rejected by the hard gate). Free tier is still subject to
	// the same rules — a community user at the cap should see the banner too.
	if h.enforcer != nil {
		if st := h.enforcer.GetStatus(); st.MaxEnvoys > 0 {
			active := data.TotalEnvoys
			max := st.MaxEnvoys
			data.LicenseCapacityActive = active
			data.LicenseCapacityMax = max
			switch {
			case active >= max:
				data.LicenseCapacityLevel = "critical"
				data.LicenseCapacityMessage = fmt.Sprintf("License at capacity: %d / %d nodes. New enrollments will be rejected until you decommission a node or upgrade your license.", active, max)
			case active*10 >= max*9: // ≥90%
				data.LicenseCapacityLevel = "warning"
				data.LicenseCapacityMessage = fmt.Sprintf("License nearing capacity: %d / %d nodes (%d slots remaining). Plan an upgrade before you hit the limit.", active, max, max-active)
			}
		}
	}

	// Recent runs (always from DB, lightweight query).
	ctx := context.Background()
	allRuns, err := h.queries.ListRunsWithHostname(ctx, 10)
	if err != nil {
		slog.Error("failed to list recent runs", "error", err)
	}
	data.RecentRuns = make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		var failPct float64
		if r.ModuleCount > 0 {
			failPct = float64(r.FailedCount) / float64(r.ModuleCount) * 100
		}
		data.RecentRuns = append(data.RecentRuns, RunRow{
			ID:           r.ID,
			EnvoyID:      r.EnvoyID,
			Hostname:     r.Hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
			FailurePct:   failPct,
		})
	}

	return data
}

// swarmFiles returns a summary of all swarm blobs and seeds for the dashboard.
func (h *Handler) swarmFiles() []SwarmFileRow {
	var rows []SwarmFileRow

	for sha256, seed := range h.swarmTracker.ListSeeds() {
		// The new "source" return from StatusSummary isn't surfaced on
		// the dashboard summary row yet — the dashboard folds source
		// into "complete" at render time for the done/total display.
		// Discarded explicitly so a later refactor that needs it has
		// an obvious place to hook in.
		c, d, p, sk, _, _, t := h.swarmTracker.StatusSummary(sha256)
		rows = append(rows, SwarmFileRow{
			SHA256:      sha256,
			Label:       seed.Label,
			Size:        humanSize(int64(seed.Size)),
			Source:      "seed",
			Complete:    c,
			Downloading: d,
			Pending:     p,
			Skipped:     sk,
			Total:       t,
		})
	}
	return rows
}

// DashboardStats renders just the stat cards fragment for HTMX polling.
func (h *Handler) DashboardStats(w http.ResponseWriter, r *http.Request) {
	var data DashboardStatsFragmentData

	if h.fleetCache != nil {
		counts := h.fleetCache.Counts()
		data.TotalEnvoys = counts.Total
		data.ActiveEnvoys = counts.Total - counts.Offline
		data.RecentFailures = counts.Failed
		if counts.Total > 0 {
			data.DriftRate = float64(counts.Relapsed+counts.Diverged) / float64(counts.Total) * 100
			data.ConvergencePct = float64(counts.Converged) / float64(counts.Total) * 100
		}
		data.Converged = counts.Converged
		data.Relapsed = counts.Relapsed
		data.Diverged = counts.Diverged
		data.CompFailed = counts.Failed
		data.Offline = counts.Offline
	} else {
		d := h.dataSource.DashboardStatsData(h.baseInterval)
		conv := h.dataSource.ConvergenceData(h.baseInterval)
		d.ConvergencePct = conv.ConvergencePct
		d.Converged = int(conv.Converged)
		d.Relapsed = int(conv.Relapsed)
		d.Diverged = int(conv.Diverged)
		d.CompFailed = int(conv.Failed)
		d.Offline = int(conv.Offline)
		data = d
	}

	sec := h.dataSource.SecurityScanData()
	data.CVECritical = sec.UniqueCritical
	data.CVEHigh = sec.UniqueHigh
	data.UniqueCVEs = sec.UniqueCVEs
	data.HardeningScore = sec.LynisAvgScore
	data.RootkitWarnings = sec.RootkitWarning

	stds := h.computeStandardsSummary()
	data.StandardsPct = stds.Pct
	data.SatisfiedControls = stds.Satisfied
	data.WaivedControls = stds.Waived
	data.PartialControls = stds.Partial
	data.NotSatisfiedControls = stds.NotSatisfied
	data.TotalControls = stds.Total

	rsk := h.dataSource.RiskData(h.baseInterval)
	data.RiskAvgScore = rsk.AvgScore
	data.RiskCritical = rsk.Critical
	data.RiskHigh = rsk.High
	data.RiskMedium = rsk.Medium
	data.RiskLow = rsk.Low

	data.ConvergenceSparklineData, data.ConvergenceSparklineMin, data.ConvergenceSparklineMax = h.convergenceSparkline()
	data.ComplianceSparklineData, data.ComplianceSparklineMin, data.ComplianceSparklineMax = h.complianceSparkline()

	certs := h.dataSource.CertExpirySummary()
	data.CertsExpiringSoon = certs.ExpiringSoon
	data.CertsExpired = certs.Expired
	data.CertsExpiringIn7 = certs.ExpiringIn7
	data.CertsExpiringIn30 = certs.ExpiringIn30
	data.CertHostsAffected = certs.HostsAffected

	runCVEs := h.dataSource.RunningCVESummary()
	data.RunningCVEsTotal = runCVEs.Total
	data.RunningCVEsCritical = runCVEs.Critical
	data.RunningCVEsHigh = runCVEs.High
	data.RunningCVEsMedium = runCVEs.Medium
	data.RunningCVEsLow = runCVEs.Low
	data.RunningCVEHostsAffected = runCVEs.HostsAffected

	h.renderer.RenderFragment(w, "dashboard", "dashboard-stats", data)
}

// DashboardFleet renders the fleet status table fragment for HTMX polling.
func (h *Handler) DashboardFleet(w http.ResponseWriter, r *http.Request) {
	data := h.dashboardData()
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
	Slug     string // URL-friendly name for detail links (e.g., "pagerduty")
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

// IntegrationCategory groups integrations by type for the integrations page.
type IntegrationCategory struct {
	Title        string
	Integrations []IntegrationStatusRow
}

// IntegrationsPageData holds data for the integrations page.
type IntegrationsPageData struct {
	PageData
	Categories   []IntegrationCategory
	EnabledCount int
	TotalCount   int
}

// Integrations renders the integrations overview page showing all supported
// integrations with their enabled/disabled status.
func (h *Handler) Integrations(w http.ResponseWriter, r *http.Request) {
	cfg := h.integrationsCfg

	type def struct {
		name     string
		slug     string
		category string
		enabled  bool
		events   int
	}

	var all []def
	a := func(name, slug, cat string, enabled bool, events int) {
		all = append(all, def{name, slug, cat, enabled, events})
	}
	e := func(s []string) int { return len(s) }

	// Alerting / Chat
	if cfg.Slack != nil { a("Slack", "slack", "Alerting / Chat", cfg.Slack.Enabled, e(cfg.Slack.Events)) } else { a("Slack", "slack", "Alerting / Chat", false, 0) }
	if cfg.PagerDuty != nil { a("PagerDuty", "pagerduty", "Alerting / Chat", cfg.PagerDuty.Enabled, e(cfg.PagerDuty.Events)) } else { a("PagerDuty", "pagerduty", "Alerting / Chat", false, 0) }
	if cfg.Opsgenie != nil { a("Opsgenie", "opsgenie", "Alerting / Chat", cfg.Opsgenie.Enabled, e(cfg.Opsgenie.Events)) } else { a("Opsgenie", "opsgenie", "Alerting / Chat", false, 0) }
	if cfg.Teams != nil { a("Microsoft Teams", "teams", "Alerting / Chat", cfg.Teams.Enabled, e(cfg.Teams.Events)) } else { a("Microsoft Teams", "teams", "Alerting / Chat", false, 0) }
	// ITSM / Ticketing
	if cfg.ConnectWise != nil { a("ConnectWise", "connectwise", "ITSM / Ticketing", cfg.ConnectWise.Enabled, e(cfg.ConnectWise.Events)) } else { a("ConnectWise", "connectwise", "ITSM / Ticketing", false, 0) }
	if cfg.Autotask != nil { a("Autotask", "autotask", "ITSM / Ticketing", cfg.Autotask.Enabled, e(cfg.Autotask.Events)) } else { a("Autotask", "autotask", "ITSM / Ticketing", false, 0) }
	if cfg.ServiceNow != nil { a("ServiceNow", "servicenow", "ITSM / Ticketing", cfg.ServiceNow.Enabled, e(cfg.ServiceNow.Events)) } else { a("ServiceNow", "servicenow", "ITSM / Ticketing", false, 0) }
	if cfg.Jira != nil { a("Jira", "jira", "ITSM / Ticketing", cfg.Jira.Enabled, e(cfg.Jira.Events)) } else { a("Jira", "jira", "ITSM / Ticketing", false, 0) }
	if cfg.Zendesk != nil { a("Zendesk", "zendesk", "ITSM / Ticketing", cfg.Zendesk.Enabled, e(cfg.Zendesk.Events)) } else { a("Zendesk", "zendesk", "ITSM / Ticketing", false, 0) }
	if cfg.Freshservice != nil { a("Freshservice", "freshservice", "ITSM / Ticketing", cfg.Freshservice.Enabled, e(cfg.Freshservice.Events)) } else { a("Freshservice", "freshservice", "ITSM / Ticketing", false, 0) }
	if cfg.Linear != nil { a("Linear", "linear", "ITSM / Ticketing", cfg.Linear.Enabled, e(cfg.Linear.Events)) } else { a("Linear", "linear", "ITSM / Ticketing", false, 0) }
	if cfg.GitHubIssues != nil { a("GitHub Issues", "github-issues", "ITSM / Ticketing", cfg.GitHubIssues.Enabled, e(cfg.GitHubIssues.Events)) } else { a("GitHub Issues", "github-issues", "ITSM / Ticketing", false, 0) }
	// SIEM / Observability
	if cfg.Splunk != nil { a("Splunk", "splunk", "SIEM / Observability", cfg.Splunk.Enabled, e(cfg.Splunk.Events)) } else { a("Splunk", "splunk", "SIEM / Observability", false, 0) }
	if cfg.Elastic != nil { a("Elasticsearch", "elastic", "SIEM / Observability", cfg.Elastic.Enabled, e(cfg.Elastic.Events)) } else { a("Elasticsearch", "elastic", "SIEM / Observability", false, 0) }
	if cfg.Datadog != nil { a("Datadog", "datadog", "SIEM / Observability", cfg.Datadog.Enabled, e(cfg.Datadog.Events)) } else { a("Datadog", "datadog", "SIEM / Observability", false, 0) }
	if cfg.SumoLogic != nil { a("Sumo Logic", "sumologic", "SIEM / Observability", cfg.SumoLogic.Enabled, e(cfg.SumoLogic.Events)) } else { a("Sumo Logic", "sumologic", "SIEM / Observability", false, 0) }
	if cfg.Sentinel != nil { a("Microsoft Sentinel", "sentinel", "SIEM / Observability", cfg.Sentinel.Enabled, e(cfg.Sentinel.Events)) } else { a("Microsoft Sentinel", "sentinel", "SIEM / Observability", false, 0) }
	// RMM
	if cfg.NinjaRMM != nil { a("NinjaRMM", "ninjarmm", "RMM", cfg.NinjaRMM.Enabled, e(cfg.NinjaRMM.Events)) } else { a("NinjaRMM", "ninjarmm", "RMM", false, 0) }
	if cfg.Kaseya != nil { a("Kaseya VSA", "kaseya", "RMM", cfg.Kaseya.Enabled, e(cfg.Kaseya.Events)) } else { a("Kaseya VSA", "kaseya", "RMM", false, 0) }
	// Asset Management
	if cfg.Device42 != nil { a("Device42", "device42", "Asset Management", cfg.Device42.Enabled, e(cfg.Device42.Events)) } else { a("Device42", "device42", "Asset Management", false, 0) }
	if cfg.SnipeIT != nil { a("Snipe-IT", "snipeit", "Asset Management", cfg.SnipeIT.Enabled, e(cfg.SnipeIT.Events)) } else { a("Snipe-IT", "snipeit", "Asset Management", false, 0) }

	// Group by category preserving order.
	categoryOrder := []string{"Alerting / Chat", "ITSM / Ticketing", "SIEM / Observability", "RMM", "Asset Management"}
	catMap := make(map[string][]IntegrationStatusRow)
	enabled := 0
	for _, d := range all {
		catMap[d.category] = append(catMap[d.category], IntegrationStatusRow{
			Name: d.name, Slug: d.slug, Category: d.category, Enabled: d.enabled, Events: d.events,
		})
		if d.enabled {
			enabled++
		}
	}
	var categories []IntegrationCategory
	for _, title := range categoryOrder {
		if rows, ok := catMap[title]; ok {
			categories = append(categories, IntegrationCategory{Title: title, Integrations: rows})
		}
	}

	data := IntegrationsPageData{
		PageData:     PageData{Title: "Integrations", ActiveNav: "integrations"},
		Categories:   categories,
		EnabledCount: enabled,
		TotalCount:   len(all),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "integrations", data)
}

// IntegrationDetail renders the example config fragment for a specific integration.
func (h *Handler) IntegrationDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	example, ok := integrationExamples[slug]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.renderer.RenderFragment(w, "integrations", "integration-detail-fragment", example)
}

// IntegrationDetailPage renders a full page for a single integration with
// example config and (if enabled) the live config with secrets masked.
func (h *Handler) IntegrationDetailPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	example, ok := integrationExamples[slug]
	if !ok {
		http.NotFound(w, r)
		return
	}

	enabled, eventCount, liveYAML := h.integrationLiveConfig(slug)

	data := struct {
		PageData
		Integration  IntegrationExample
		Enabled      bool
		EventCount   int
		LiveYAML     string
	}{
		PageData:    PageData{Title: example.Name + " Integration", ActiveNav: "integrations"},
		Integration: example,
		Enabled:     enabled,
		EventCount:  eventCount,
		LiveYAML:    liveYAML,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "integration-detail", data)
}

// integrationLiveConfig returns the enabled status, event count, and a
// YAML representation of the live config for an integration (secrets masked).
func (h *Handler) integrationLiveConfig(slug string) (bool, int, string) {
	cfg := h.integrationsCfg
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		if strings.HasPrefix(s, "secret:") {
			return s // already a secret ref — show the ref, not the value
		}
		if len(s) <= 4 {
			return "****"
		}
		return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
	}
	fmtEvents := func(events []string) string {
		if len(events) == 0 {
			return "[]  # all events"
		}
		parts := make([]string, len(events))
		for i, e := range events {
			parts[i] = fmt.Sprintf("%q", e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}

	switch slug {
	case "slack":
		c := cfg.Slack
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  slack:\n    enabled: true\n    webhook_url: %q\n    events: %s\n    min_severity: %s", mask(c.WebhookURL), fmtEvents(c.Events), c.MinSeverity)
	case "pagerduty":
		c := cfg.PagerDuty
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  pagerduty:\n    enabled: true\n    routing_key: %q\n    events: %s\n    min_severity: %s\n    auto_resolve: %v", mask(c.RoutingKey), fmtEvents(c.Events), c.MinSeverity, c.AutoResolve)
	case "opsgenie":
		c := cfg.Opsgenie
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  opsgenie:\n    enabled: true\n    api_key: %q\n    events: %s\n    min_severity: %s", mask(c.APIKey), fmtEvents(c.Events), c.MinSeverity)
	case "teams":
		c := cfg.Teams
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  teams:\n    enabled: true\n    webhook_url: %q\n    events: %s\n    min_severity: %s", mask(c.WebhookURL), fmtEvents(c.Events), c.MinSeverity)
	case "connectwise":
		c := cfg.ConnectWise
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  connectwise:\n    enabled: true\n    url: %q\n    company_id: %s\n    public_key: %s\n    private_key: %q\n    board_id: %d\n    events: %s", c.URL, c.CompanyID, c.PublicKey, mask(c.PrivateKey), c.BoardID, fmtEvents(c.Events))
	case "autotask":
		c := cfg.Autotask
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  autotask:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    queue_id: %d\n    events: %s", c.URL, c.Username, mask(c.Password), c.QueueID, fmtEvents(c.Events))
	case "servicenow":
		c := cfg.ServiceNow
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  servicenow:\n    enabled: true\n    instance: %s\n    username: %s\n    password: %q\n    events: %s", c.Instance, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "jira":
		c := cfg.Jira
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  jira:\n    enabled: true\n    url: %q\n    email: %s\n    api_token: %q\n    project: %s\n    issue_type: %s\n    events: %s", c.URL, c.Email, mask(c.APIToken), c.Project, c.IssueType, fmtEvents(c.Events))
	case "zendesk":
		c := cfg.Zendesk
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  zendesk:\n    enabled: true\n    subdomain: %s\n    email: %s\n    api_token: %q\n    events: %s", c.Subdomain, c.Email, mask(c.APIToken), fmtEvents(c.Events))
	case "freshservice":
		c := cfg.Freshservice
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  freshservice:\n    enabled: true\n    domain: %s\n    api_key: %q\n    events: %s", c.Domain, mask(c.APIKey), fmtEvents(c.Events))
	case "linear":
		c := cfg.Linear
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  linear:\n    enabled: true\n    api_key: %q\n    team_id: %s\n    events: %s", mask(c.APIKey), c.TeamID, fmtEvents(c.Events))
	case "github-issues":
		c := cfg.GitHubIssues
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  github_issues:\n    enabled: true\n    owner: %s\n    repo: %s\n    token: %q\n    events: %s", c.Owner, c.Repo, mask(c.Token), fmtEvents(c.Events))
	case "splunk":
		c := cfg.Splunk
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  splunk:\n    enabled: true\n    url: %q\n    token: %q\n    index: %s\n    events: %s", c.URL, mask(c.Token), c.Index, fmtEvents(c.Events))
	case "elastic":
		c := cfg.Elastic
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  elastic:\n    enabled: true\n    url: %q\n    index: %s\n    api_key: %q\n    events: %s", c.URL, c.Index, mask(c.APIKey), fmtEvents(c.Events))
	case "datadog":
		c := cfg.Datadog
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  datadog:\n    enabled: true\n    api_key: %q\n    site: %s\n    events: %s", mask(c.APIKey), c.Site, fmtEvents(c.Events))
	case "sumologic":
		c := cfg.SumoLogic
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  sumologic:\n    enabled: true\n    url: %q\n    events: %s", mask(c.URL), fmtEvents(c.Events))
	case "sentinel":
		c := cfg.Sentinel
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  sentinel:\n    enabled: true\n    workspace_id: %s\n    shared_key: %q\n    events: %s", c.WorkspaceID, mask(c.SharedKey), fmtEvents(c.Events))
	case "ninjarmm":
		c := cfg.NinjaRMM
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  ninjarmm:\n    enabled: true\n    url: %q\n    client_id: %s\n    client_secret: %q\n    events: %s", c.URL, c.ClientID, mask(c.ClientSecret), fmtEvents(c.Events))
	case "kaseya":
		c := cfg.Kaseya
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  kaseya:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    events: %s", c.URL, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "device42":
		c := cfg.Device42
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  device42:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    events: %s", c.URL, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "snipeit":
		c := cfg.SnipeIT
		if c == nil || !c.Enabled { return false, 0, "" }
		return true, len(c.Events), fmt.Sprintf("integrations:\n  snipeit:\n    enabled: true\n    url: %q\n    api_token: %q\n    events: %s", c.URL, mask(c.Token), fmtEvents(c.Events))
	}
	return false, 0, ""
}

// IntegrationExample holds display data for an integration detail view.
type IntegrationExample struct {
	Name        string
	Slug        string
	Description string
	ExampleYAML string
	Events      string // supported event types
}

// integrationExamples maps slug → example config for each supported integration.
var integrationExamples = map[string]IntegrationExample{
	"slack": {Name: "Slack", Slug: "slack", Description: "Sends formatted messages with severity-colored attachments to a Slack channel via incoming webhook.",
		Events: "All event types", ExampleYAML: "integrations:\n  slack:\n    enabled: true\n    webhook_url: \"secret:vigo/slack/webhook-url\"\n    events: [\"run.failure\", \"run.blocked\", \"security.rootkit\", \"drift.detected\"]\n    min_severity: warning"},
	"pagerduty": {Name: "PagerDuty", Slug: "pagerduty", Description: "Triggers incidents via PagerDuty Events API v2 with automatic severity mapping, dedup keys, and auto-resolve.",
		Events: "All event types", ExampleYAML: "integrations:\n  pagerduty:\n    enabled: true\n    routing_key: \"secret:vigo/pagerduty/routing-key\"\n    events: [\"security.rootkit\", \"security.malware\", \"convergence.threshold\"]\n    min_severity: warning\n    auto_resolve: true"},
	"opsgenie": {Name: "Opsgenie", Slug: "opsgenie", Description: "Creates alerts via Opsgenie REST API with P1-P3 priority mapping based on event severity.",
		Events: "All event types", ExampleYAML: "integrations:\n  opsgenie:\n    enabled: true\n    api_key: \"secret:vigo/opsgenie/api-key\"\n    events: [\"security.rootkit\", \"security.malware\"]\n    min_severity: critical"},
	"teams": {Name: "Microsoft Teams", Slug: "teams", Description: "Sends adaptive cards to a Microsoft Teams channel via incoming webhook with severity-colored themes.",
		Events: "All event types", ExampleYAML: "integrations:\n  teams:\n    enabled: true\n    webhook_url: \"secret:vigo/teams/webhook-url\"\n    events: [\"run.failure\", \"drift.detected\"]\n    min_severity: warning"},
	"connectwise": {Name: "ConnectWise", Slug: "connectwise", Description: "Creates service tickets in ConnectWise Manage on drift and failure events.",
		Events: "All event types", ExampleYAML: "integrations:\n  connectwise:\n    enabled: true\n    url: \"https://api-na.myconnectwise.net/v4_6_release/apis/3.0\"\n    company_id: yourcompany\n    public_key: your-public-key\n    private_key: \"secret:vigo/connectwise/private-key\"\n    board_id: 1\n    events: [\"run.failure\", \"security.rootkit\"]"},
	"autotask": {Name: "Autotask", Slug: "autotask", Description: "Creates tickets in Autotask/Datto PSA with queue routing.",
		Events: "All event types", ExampleYAML: "integrations:\n  autotask:\n    enabled: true\n    url: \"https://webservices.autotask.net/atservicesrest/v1.0\"\n    username: vigo-api\n    password: \"secret:vigo/autotask/password\"\n    queue_id: 12345\n    events: [\"run.failure\"]"},
	"servicenow": {Name: "ServiceNow", Slug: "servicenow", Description: "Creates incidents in ServiceNow with urgency and impact mapping. Also supports CMDB CI sync.",
		Events: "All event types", ExampleYAML: "integrations:\n  servicenow:\n    enabled: true\n    instance: yourcompany.service-now.com\n    username: vigo\n    password: \"secret:vigo/servicenow/password\"\n    events: [\"run.failure\", \"security.rootkit\"]"},
	"jira": {Name: "Jira", Slug: "jira", Description: "Creates issues in Jira Cloud or Server via REST API.",
		Events: "All event types", ExampleYAML: "integrations:\n  jira:\n    enabled: true\n    url: \"https://yourcompany.atlassian.net\"\n    project: OPS\n    issue_type: Task\n    api_token: \"secret:vigo/jira/api-token\"\n    events: [\"run.failure\", \"security.rootkit\"]"},
	"zendesk": {Name: "Zendesk", Slug: "zendesk", Description: "Creates support tickets in Zendesk via API.",
		Events: "All event types", ExampleYAML: "integrations:\n  zendesk:\n    enabled: true\n    subdomain: yourcompany\n    email: vigo@yourcompany.com\n    api_token: \"secret:vigo/zendesk/api-token\"\n    events: [\"run.failure\"]"},
	"freshservice": {Name: "Freshservice", Slug: "freshservice", Description: "Creates tickets in Freshservice via API.",
		Events: "All event types", ExampleYAML: "integrations:\n  freshservice:\n    enabled: true\n    domain: yourcompany.freshservice.com\n    api_key: \"secret:vigo/freshservice/api-key\"\n    events: [\"run.failure\"]"},
	"linear": {Name: "Linear", Slug: "linear", Description: "Creates issues in Linear via GraphQL API with priority mapping.",
		Events: "All event types", ExampleYAML: "integrations:\n  linear:\n    enabled: true\n    api_key: \"secret:vigo/linear/api-key\"\n    team_id: your-team-id\n    events: [\"run.failure\", \"security.rootkit\"]"},
	"github-issues": {Name: "GitHub Issues", Slug: "github-issues", Description: "Creates issues in a GitHub repository.",
		Events: "All event types", ExampleYAML: "integrations:\n  github_issues:\n    enabled: true\n    owner: your-org\n    repo: vigo-alerts\n    token: \"secret:vigo/github/token\"\n    events: [\"run.failure\"]"},
	"splunk": {Name: "Splunk", Slug: "splunk", Description: "Forwards events to Splunk HTTP Event Collector (HEC) with source and index routing.",
		Events: "All event types", ExampleYAML: "integrations:\n  splunk:\n    enabled: true\n    url: \"https://splunk.example.com:8088\"\n    token: \"secret:vigo/splunk/hec-token\"\n    index: vigo\n    events: []  # empty = all events"},
	"elastic": {Name: "Elasticsearch", Slug: "elastic", Description: "Indexes events directly into Elasticsearch.",
		Events: "All event types", ExampleYAML: "integrations:\n  elastic:\n    enabled: true\n    url: \"https://elasticsearch.example.com:9200\"\n    index: vigo-events\n    api_key: \"secret:vigo/elastic/api-key\"\n    events: []"},
	"datadog": {Name: "Datadog", Slug: "datadog", Description: "Sends events to Datadog Events API with alert type mapping.",
		Events: "All event types", ExampleYAML: "integrations:\n  datadog:\n    enabled: true\n    api_key: \"secret:vigo/datadog/api-key\"\n    site: datadoghq.com\n    events: []"},
	"sumologic": {Name: "Sumo Logic", Slug: "sumologic", Description: "Forwards events to a Sumo Logic HTTP source endpoint.",
		Events: "All event types", ExampleYAML: "integrations:\n  sumologic:\n    enabled: true\n    url: \"https://endpoint.collection.us2.sumologic.com/receiver/v1/http/...\"\n    events: []"},
	"sentinel": {Name: "Microsoft Sentinel", Slug: "sentinel", Description: "Forwards events to Azure Log Analytics via the Data Collector API for Microsoft Sentinel.",
		Events: "All event types", ExampleYAML: "integrations:\n  sentinel:\n    enabled: true\n    workspace_id: your-workspace-id\n    shared_key: \"secret:vigo/sentinel/shared-key\"\n    events: []"},
	"ninjarmm": {Name: "NinjaRMM", Slug: "ninjarmm", Description: "Creates tickets in NinjaRMM platform.",
		Events: "All event types", ExampleYAML: "integrations:\n  ninjarmm:\n    enabled: true\n    url: \"https://app.ninjarmm.com\"\n    client_id: your-client-id\n    client_secret: \"secret:vigo/ninjarmm/client-secret\"\n    events: [\"run.failure\"]"},
	"kaseya": {Name: "Kaseya VSA", Slug: "kaseya", Description: "Creates tickets in Kaseya VSA.",
		Events: "All event types", ExampleYAML: "integrations:\n  kaseya:\n    enabled: true\n    url: \"https://vsa.yourcompany.com\"\n    username: vigo-api\n    password: \"secret:vigo/kaseya/password\"\n    events: [\"run.failure\"]"},
	"device42": {Name: "Device42", Slug: "device42", Description: "Syncs envoy inventory as devices in Device42 for CMDB tracking.",
		Events: "All event types", ExampleYAML: "integrations:\n  device42:\n    enabled: true\n    url: \"https://device42.example.com\"\n    username: vigo\n    password: \"secret:vigo/device42/password\"\n    events: [\"envoy.enrolled\"]"},
	"snipeit": {Name: "Snipe-IT", Slug: "snipeit", Description: "Syncs envoy inventory as hardware assets in Snipe-IT.",
		Events: "All event types", ExampleYAML: "integrations:\n  snipeit:\n    enabled: true\n    url: \"https://snipeit.example.com/api/v1\"\n    api_token: \"secret:vigo/snipeit/api-token\"\n    events: [\"envoy.enrolled\"]"},
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
		data.WaivedControls = s.Waived
		data.PartialControls = s.Partial
		data.NotSatisfiedControls = s.NotSatisfied
		data.TotalControls = s.Total
	}

	// Compute waiver alerts from ConfigStore.
	if h.configStore != nil {
		now := time.Now()
		soonThreshold := now.AddDate(0, 0, 30)
		for _, controls := range h.configStore.AllWaivers() {
			for _, w := range controls {
				data.TotalActiveWaivers++
				if w.Expires != "" {
					if exp, err := time.Parse("2006-01-02", w.Expires); err == nil && exp.Before(soonThreshold) {
						data.ExpiringWaivers++
					}
				}
			}
		}
	}

	return data
}

// standardsSummary computes fleet-wide standards compliance stats.
// Reused by the compliance page and dashboard.
type standardsSummary struct {
	Pct          float64
	Satisfied    int
	Waived       int
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
		Name         string  `json:"name"`
		Satisfied    int     `json:"satisfied"`
		Waived       int     `json:"waived"`
		Partial      int     `json:"partial"`
		NotSatisfied int     `json:"not_satisfied"`
		ScorePercent float64 `json:"score_percent"`
	}
	type reportLike struct {
		Score struct {
			Percent float64 `json:"percent"`
		} `json:"score"`
		Frameworks []frameworkLike `json:"frameworks"`
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
		s.Waived += fw.Waived
		s.Partial += fw.Partial
		s.NotSatisfied += fw.NotSatisfied
		s.Total += fw.Satisfied + fw.Waived + fw.Partial + fw.NotSatisfied
	}
	if s.Total > 0 {
		s.Pct = math.Round(float64(s.Satisfied+s.Waived)/float64(s.Total)*1000) / 10
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

// ComplianceWaivers renders a page listing all active compliance waivers.
func (h *Handler) ComplianceWaivers(w http.ResponseWriter, r *http.Request) {
	type waiverRow struct {
		Framework    string
		FrameworkKey string
		Control      string
		Reason       string
		ApprovedBy   string
		Expires      string
		Enforced     bool // true if the control is also covered by modules
		ExpiringSoon bool
	}

	var rows []waiverRow
	now := time.Now()
	soonThreshold := now.AddDate(0, 0, 30)

	if h.configStore != nil {
		allWaivers := h.configStore.AllWaivers()

		// Build set of all covered controls per standard across fleet.
		coveredControls := make(map[string]map[string]bool) // standard → controlID → true

		for stdKey := range allWaivers {
			controlIndex := h.configStore.ComplianceControlIndex(stdKey)
			if controlIndex == nil {
				continue
			}
			if _, ok := coveredControls[stdKey]; !ok {
				coveredControls[stdKey] = make(map[string]bool)
			}
			for controlID := range controlIndex {
				coveredControls[stdKey][controlID] = true
			}
		}

		// Build rows sorted by expiry (soonest first).
		for stdKey, controls := range allWaivers {
			displayName := standardDisplayName(stdKey)
			covered := coveredControls[stdKey]
			for controlID, w := range controls {
				row := waiverRow{
					Framework:    displayName,
					FrameworkKey: stdKey,
					Control:      controlID,
					Reason:       w.Reason,
					ApprovedBy:   w.ApprovedBy,
					Expires:      w.Expires,
					Enforced:     covered[controlID],
				}
				if w.Expires != "" {
					if exp, err := time.Parse("2006-01-02", w.Expires); err == nil && exp.Before(soonThreshold) {
						row.ExpiringSoon = true
					}
				}
				rows = append(rows, row)
			}
		}

		// Sort: expiring soonest first, then no-expiry last.
		sort.Slice(rows, func(i, j int) bool {
			ei, ej := rows[i].Expires, rows[j].Expires
			if ei == "" && ej == "" {
				return rows[i].Framework+rows[i].Control < rows[j].Framework+rows[j].Control
			}
			if ei == "" {
				return false
			}
			if ej == "" {
				return true
			}
			return ei < ej
		})
	}

	// Collect distinct frameworks for filter buttons.
	fwSeen := make(map[string]bool)
	var frameworks []string
	for _, row := range rows {
		if !fwSeen[row.Framework] {
			fwSeen[row.Framework] = true
			frameworks = append(frameworks, row.Framework)
		}
	}
	sort.Strings(frameworks)

	// Apply filters.
	fwFilter := r.URL.Query().Get("framework")
	typeFilter := r.URL.Query().Get("type") // "enforced", "waived-only"
	var filtered []waiverRow
	for _, row := range rows {
		if fwFilter != "" && row.Framework != fwFilter {
			continue
		}
		if typeFilter == "enforced" && !row.Enforced {
			continue
		}
		if typeFilter == "waived-only" && row.Enforced {
			continue
		}
		filtered = append(filtered, row)
	}

	data := struct {
		PageData
		Waivers    []waiverRow
		Frameworks []string
		FwFilter   string
		TypeFilter string
	}{
		PageData:   PageData{Title: "Compliance Waivers", ActiveNav: "compliance"},
		Waivers:    filtered,
		Frameworks: frameworks,
		FwFilter:   fwFilter,
		TypeFilter: typeFilter,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "compliance-waivers", data)
}

// RiskPosture renders the dedicated risk posture page.
func (h *Handler) RiskPosture(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.RiskPostureData(h.baseInterval)
	data.PageData = PageData{Title: "Risk Posture", ActiveNav: "risk"}
	levelFilter := r.URL.Query().Get("level")
	data.LevelFilter = levelFilter
	if levelFilter != "" {
		var filtered []RiskEnvoyDetail
		for _, e := range data.Envoys {
			if e.Level == levelFilter {
				filtered = append(filtered, e)
			}
		}
		data.Envoys = filtered
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "risk", data)
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
		"cisubuntu":         {"CIS Ubuntu Linux Benchmark Report", "cisubuntu"},
		"cisrhel":           {"CIS RHEL Benchmark Report", "cisrhel"},
		"abacyber":          {"ABA Cybersecurity Compliance Report", "abacyber"},
		"nydfscyber":        {"NY DFS 23 NYCRR 500 Compliance Report", "nydfscyber"},
	}

	info, ok := frameworks[fw]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Check if this framework is enabled.
	if standardKey, hasKey := config.StandardKeyForSlug[fw]; hasKey {
		if h.configStore != nil && !h.configStore.IsStandardEnabled(standardKey) {
			http.NotFound(w, r)
			return
		}
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
	Relapsed       int64
	Diverged       int64
	Failed         int64
	Offline    int64
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
	FailurePct   float64
	FailedCount  int64
	ModuleCount  int64
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
		total := summary.Converged + summary.Relapsed + summary.Diverged + summary.Failed + summary.Offline + summary.NoData
		pct := float64(0)
		if total > 0 {
			pct = float64(summary.Converged) / float64(total) * 100
		}
		data := EvidenceSnapshotData{
			SnapshotDate:   dateStr + " (live)",
			ConvergencePct:  pct,
			Converged:      summary.Converged,
			Relapsed:       summary.Relapsed,
			Diverged:       summary.Diverged,
			Failed:         summary.Failed,
			Offline:    summary.Offline,
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
		Relapsed:       snapshot.Relapsed,
		Diverged:       snapshot.Diverged,
		Failed:         snapshot.Failed,
		Offline:    snapshot.Offline,
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

	// Load active waivers: config-file waivers + DB waivers.
	type waiverInfo struct {
		Reason  string
		By      string
		Expiry  string
		ID      string // DB waivers have an ID
	}
	waiverMap := make(map[string]waiverInfo)
	// Config-file waivers (envoys-tree compliance.vgo).
	if h.configStore != nil {
		if stdKey, ok := config.StandardKeyForSlug[slug]; ok {
			if fileWaivers := h.configStore.Waivers(stdKey); len(fileWaivers) > 0 {
				for controlID, w := range fileWaivers {
					waiverMap[controlID] = waiverInfo{
						Reason: w.Reason,
						By:     w.ApprovedBy,
						Expiry: w.Expires,
					}
				}
			}
		}
	}
	// DB waivers (override file-based if both exist for same control).
	if dbWaivers, err := h.queries.ListComplianceWaiversByFramework(r.Context(), slug); err == nil {
		for _, w := range dbWaivers {
			wi := waiverInfo{Reason: w.Reason, By: w.Approver, ID: w.ID}
			if w.ExpiresAt.Valid {
				wi.Expiry = w.ExpiresAt.Time.Format("2006-01-02")
			}
			waiverMap[w.ControlID] = wi
		}
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
		// Waiver summary
		TotalWaivers    int
		EnforcedWaived  int  // controls that are both satisfied and waived
		WaivedOnly      int  // controls waived but not enforced
		ExpiringSoon    int  // waivers expiring within 30 days
	}{
		Slug:           slug,
		FrameworkTitle: standardDisplayNameFromSlug(slug),
	}

	now := time.Now()
	soonThreshold := now.AddDate(0, 0, 30)

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
			cw.WaiverBy = w.By
			cw.WaiverExpiry = w.Expiry
			cw.WaiverID = w.ID

			data.TotalWaivers++
			if c.Status == "satisfied" {
				data.EnforcedWaived++
			} else {
				data.WaivedOnly++
			}
			if w.Expiry != "" {
				if exp, err := time.Parse("2006-01-02", w.Expiry); err == nil && exp.Before(soonThreshold) {
					data.ExpiringSoon++
				}
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
	// Use fleet cache for the full envoy list (no 100-envoy cap) and
	// avoid per-envoy run queries — the fleet cache already has status.
	type envoyModule struct {
		envoyID     string
		hostname    string
		module      string
		status      string
		failurePct  float64
		failedCount int64
		moduleCount int64
	}
	var matches []envoyModule

	// Get all envoys from fleet cache or fall back to DataSource.
	type envoyInfo struct {
		ID          string
		Hostname    string
		Status      string
		FailurePct  float64
		FailedCount int64
		ModuleCount int64
	}
	var allEnvoys []envoyInfo
	if h.fleetCache != nil {
		for _, e := range h.fleetCache.Entries() {
			allEnvoys = append(allEnvoys, envoyInfo{ID: e.ID, Hostname: e.Hostname, Status: e.Status, FailurePct: e.FailurePct, FailedCount: e.FailedCount, ModuleCount: e.ModuleCount})
		}
	} else {
		for _, e := range h.dataSource.EnvoysData(h.baseInterval, "", "", 0, "").Envoys {
			allEnvoys = append(allEnvoys, envoyInfo{ID: e.ID, Hostname: e.Hostname, Status: e.Status, FailurePct: e.FailurePct, FailedCount: e.FailedCount, ModuleCount: e.ModuleCount})
		}
	}

	for _, t := range allEnvoys {
		trace := h.configStore.Trace(t.Hostname)
		if !trace.Matched {
			continue
		}
		for _, ct := range trace.ComplianceTags {
			for _, ids := range ct.Frameworks {
				for _, id := range ids {
					if id == controlID {
						matches = append(matches, envoyModule{
							envoyID:     t.ID,
							hostname:    t.Hostname,
							module:      ct.Module,
							status:      t.Status,
							failurePct:  t.FailurePct,
							failedCount: t.FailedCount,
							moduleCount: t.ModuleCount,
						})
					}
				}
			}
		}
	}

	var envoys []EvidenceControlEnvoy
	for _, m := range matches {
		envoys = append(envoys, EvidenceControlEnvoy{
			ID:          m.envoyID,
			Hostname:    m.hostname,
			Module:      m.module,
			Status:      m.status,
			FailurePct:  m.failurePct,
			FailedCount: m.failedCount,
			ModuleCount: m.moduleCount,
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
			fmt.Fprintln(f, "date,total,converged,relapsed,diverged,errors,offline,no_data")
			for _, h := range history {
				fmt.Fprintf(f, "%s,%d,%d,%d,%d,%d,%d,%d\n",
					h.RecordedAt, h.Total, h.Converged, h.Relapsed, h.Diverged, h.Failed, h.Offline, h.NoData)
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
	envoyData := h.dataSource.EnvoysData(h.baseInterval, "", "", 100000, "")
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
	search := r.URL.Query().Get("search")
	sortCol := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "asc"
	}

	data := h.dataSource.EnvoysData(h.baseInterval, statusFilter, after, 100, search)
	h.enrichEnvoyRows(data.Envoys)

	// Apply client-requested sort (in-memory over the page of 100 rows).
	sortEnvoyRows(data.Envoys, sortCol, sortDir)
	data.SortColumn = sortCol
	data.SortDir = sortDir

	// Populate status counts from fleet cache (single precomputed query)
	// instead of loading all envoys + N+1 run queries via ConvergenceData.
	if h.fleetCache != nil {
		counts := h.fleetCache.Counts()
		data.CountConverged = counts.Converged
		data.CountRelapsed = counts.Relapsed
		data.CountDiverged = counts.Diverged
		data.CountDegraded = counts.Degraded
		data.CountFailed = counts.Failed
		data.CountOffline = counts.Offline
		data.CountNoData = counts.NoData
		data.TotalEnvoys = counts.Total
	} else {
		conv := h.dataSource.ConvergenceData(h.baseInterval)
		data.CountConverged = int(conv.Converged)
		data.CountRelapsed = int(conv.Relapsed)
		data.CountDiverged = int(conv.Diverged)
		data.CountFailed = int(conv.Failed)
		data.CountOffline = int(conv.Offline)
		data.CountNoData = int(conv.NoData)
		data.TotalEnvoys = int(conv.Total)
	}
	data.ActiveEnvoys = data.TotalEnvoys - data.CountOffline
	if data.TotalEnvoys > 0 {
		data.DriftRate = float64(data.CountRelapsed+data.CountDiverged) / float64(data.TotalEnvoys) * 100
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoys", data)
}

// sortEnvoyRows sorts envoy rows in-memory by the requested column.
func sortEnvoyRows(rows []EnvoyRow, col, dir string) {
	if col == "" || len(rows) < 2 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		var less bool
		switch col {
		case "hostname":
			less = rows[i].Hostname < rows[j].Hostname
		case "last_seen":
			less = rows[i].LastSeen.Before(rows[j].LastSeen)
		case "last_run":
			less = rows[i].LastRunAt.Before(rows[j].LastRunAt)
		case "status":
			less = rows[i].Status < rows[j].Status
		case "ip":
			less = rows[i].IPAddress < rows[j].IPAddress
		default:
			return false
		}
		if dir == "desc" {
			return !less
		}
		return less
	})
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
	data.ScrierEnabled = h.scrierEnabled
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
// Populated via ReportBuilder.EnvoyFrameworkStatus, which partitions findings
// with the same math used by the /compliance/<slug> fleet row — so both pages
// render identical numbers for the same envoy.
type EnvoyFrameworkCard struct {
	Name         string
	Slug         string
	ScorePercent float64
	Satisfied    int
	Waived       int
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
	ModuleConvergence string // envoy convergence status (converged, failed, etc.)
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
// EnvoyScrierSessionsFragment renders the Scrier session history for an envoy.
func (h *Handler) EnvoyScrierSessionsFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	sessions, err := h.queries.ListScrierSessionsByEnvoy(r.Context(), idStr)
	if err != nil {
		fmt.Fprintf(w, `<p class="text-muted">No session data available.</p>`)
		return
	}
	if len(sessions) == 0 {
		fmt.Fprintf(w, `<p class="text-muted" style="padding:20px;">No remote access sessions recorded for this envoy.</p>`)
		return
	}

	fmt.Fprintf(w, `<table class="table"><thead><tr><th>User</th><th>Protocol</th><th>Port</th><th>Started</th><th>Duration</th><th>Status</th></tr></thead><tbody>`)
	for _, s := range sessions {
		duration := "-"
		if s.DurationMs.Valid {
			d := time.Duration(s.DurationMs.Int64) * time.Millisecond
			duration = d.Round(time.Second).String()
		}
		ended := "-"
		if s.EndedAt.Valid {
			ended = s.EndedAt.Time.Local().Format("2006-01-02 15:04")
		}
		_ = ended // used below in title attribute

		statusCSS := "info"
		switch s.Status {
		case "active":
			statusCSS = "success"
		case "closed":
			statusCSS = "muted"
		case "error":
			statusCSS = "danger"
		}

		fmt.Fprintf(w, `<tr><td>%s</td><td class="mono">%s</td><td>%d</td><td>%s</td><td>%s</td><td><span class="badge badge--%s">%s</span></td></tr>`,
			template.HTMLEscapeString(s.Username),
			template.HTMLEscapeString(s.Protocol),
			s.TargetPort,
			s.StartedAt.Local().Format("2006-01-02 15:04"),
			duration,
			statusCSS,
			template.HTMLEscapeString(s.Status),
		)
	}
	fmt.Fprintf(w, `</tbody></table>`)
}

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
// Per-framework numbers are routed through ReportBuilder.EnvoyFrameworkStatus
// so this card matches the /compliance/<slug> fleet-row for the same envoy.
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

	for slug, standardKey := range config.StandardKeyForSlug {
		if h.configStore != nil && !h.configStore.IsStandardEnabled(standardKey) {
			continue
		}
		if h.reportBuilder == nil || h.reportBuilder.EnvoyFrameworkStatus == nil {
			continue
		}
		status, ok := h.reportBuilder.EnvoyFrameworkStatus(r, detail.Envoy.Hostname, slug)
		if !ok || status.Total == 0 {
			continue
		}
		data.Frameworks = append(data.Frameworks, EnvoyFrameworkCard{
			Name:         standardDisplayName(standardKey),
			Slug:         slug,
			ScorePercent: status.CoveragePercent,
			Satisfied:    status.Covered,
			Waived:       status.Waived,
			Partial:      status.Partial,
			NotSatisfied: status.Missing,
		})
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

	// Check if this framework is enabled.
	if standardKey, hasKey := config.StandardKeyForSlug[fw]; hasKey {
		if h.configStore != nil && !h.configStore.IsStandardEnabled(standardKey) {
			http.NotFound(w, r)
			return
		}
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

	// Check if this framework is enabled.
	if h.configStore != nil && !h.configStore.IsStandardEnabled(standardKey) {
		http.NotFound(w, r)
		return
	}

	// Module convergence: 100% if converged, 0% if errors/no data.
	modPct := float64(0)
	switch detail.Envoy.Status {
	case "converged":
		modPct = 100
	case "relapsed":
		modPct = 75
	case "diverged":
		modPct = 50
	case "failed":
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
		"CIS Ubuntu":         "cisubuntu",
		"CIS RHEL":           "cisrhel",
	}
	if s, ok := slugs[name]; ok {
		return s
	}
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// Runs renders the runs list page with optional status filter.
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	sortCol := r.URL.Query().Get("sort")
	sortDir := r.URL.Query().Get("dir")
	if sortDir != "asc" && sortDir != "desc" {
		sortDir = "desc" // runs default newest-first
	}

	data := h.dataSource.RunsData(statusFilter)
	sortRunRows(data.Runs, sortCol, sortDir)
	data.SortColumn = sortCol
	data.SortDir = sortDir
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "runs", data)
}

// sortRunRows sorts run rows in-memory by the requested column.
func sortRunRows(rows []RunRow, col, dir string) {
	if col == "" || len(rows) < 2 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		var less bool
		switch col {
		case "hostname":
			less = rows[i].Hostname < rows[j].Hostname
		case "status":
			less = rows[i].Status < rows[j].Status
		case "time":
			less = rows[i].StartedAt.Before(rows[j].StartedAt)
		case "changed":
			less = rows[i].ChangedCount < rows[j].ChangedCount
		case "failed":
			less = rows[i].FailedCount < rows[j].FailedCount
		default:
			return false
		}
		if dir == "desc" {
			return !less
		}
		return less
	})
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

// adminOnly blocks non-admin users with a 403 and renders nothing.
// Returns true when the handler should continue; false when it should return.
func (h *Handler) adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if !h.authEnabled {
		return true
	}
	u := auth.UserFromContext(r.Context())
	if u == nil || !u.IsAdmin() {
		http.Error(w, "admin role required", http.StatusForbidden)
		return false
	}
	return true
}

// ServerConfig renders the card grid at /server/config. One card per
// top-level section of server.yaml — enabled or disabled based on the live
// on-disk file.
func (h *Handler) ServerConfig(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	state, err := h.readServerYAMLState()
	if err != nil {
		slog.Error("reading server.yaml for server-config UI", "path", h.serverConfigPath, "error", err)
		http.Error(w, "server.yaml unavailable", http.StatusInternalServerError)
		return
	}
	schema := config.ServerConfigSchema()

	data := ServerConfigPageData{
		PageData:   PageData{Title: "Server Config", ActiveNav: "server-config"},
		ConfigPath: h.serverConfigPath,
		Available:  len(schema),
	}
	for _, s := range schema {
		var present, enabled bool
		if st := state.Section(s.Name); st != nil {
			present = st.Present
			enabled = st.Enabled
		}
		if enabled {
			data.Enabled++
		}
		data.Cards = append(data.Cards, ServerConfigCardRow{
			Name:        s.Name,
			Description: s.Description,
			Enabled:     enabled,
			Present:     present,
		})
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "server-config", data)
}

// ServerConfigSection renders the detail page for a single top-level section
// of server.yaml at /server/config/{section}.
func (h *Handler) ServerConfigSection(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	name := r.PathValue("section")
	schema := config.FindSchemaSection(name)
	if schema == nil {
		http.Error(w, "unknown section", http.StatusNotFound)
		return
	}
	state, err := h.readServerYAMLState()
	if err != nil {
		slog.Error("reading server.yaml for server-config UI", "path", h.serverConfigPath, "error", err)
		http.Error(w, "server.yaml unavailable", http.StatusInternalServerError)
		return
	}
	sec := state.Section(name)

	// Detail page prefers the `long:` tag (2-3 sentence umbrella) so the
	// section page carries the full explanation; listing-grid cards use the
	// short `doc:` tag instead. Fall back to Description when Long is unset
	// so older sections without a long tag still render something.
	desc := schema.Long
	if desc == "" {
		desc = schema.Description
	}
	data := ServerConfigSectionPageData{
		PageData:    PageData{Title: "Server Config — " + name, ActiveNav: "server-config"},
		ConfigPath:  h.serverConfigPath,
		Section:     name,
		Description: desc,
		DocsURL:     schema.DocsURL,
		ExampleYAML: config.ExampleSectionYAML(name),
	}
	if sec != nil {
		data.Present = sec.Present
		data.Enabled = sec.Enabled
		if yaml, err := sec.MarshalYAML(); err == nil {
			data.CurrentYAML = yaml
		} else {
			slog.Error("marshaling section yaml", "section", name, "error", err)
		}
	}

	// Available fields always render — they are the schema.
	for _, f := range schema.Fields {
		data.Available = append(data.Available, buildFieldRow(f, nil))
	}
	// Current values render only when the section is actually enabled on disk.
	if sec != nil && sec.Enabled {
		for _, f := range schema.Fields {
			row := buildFieldRow(f, sec)
			if row.HasValue {
				data.Current = append(data.Current, row)
			}
		}
	}

	// Nested sub-sections (integrations.*, auth.oidc, etc.).
	for _, sub := range schema.Subsections {
		var subState *config.SectionState
		if sec != nil {
			subState = sec.SubsectionState(sub.Name)
		}
		row := ServerConfigSubsectionRow{
			Name:        sub.Name,
			Description: sub.Description,
		}
		if subState != nil {
			row.Present = subState.Present
			row.Enabled = subState.Enabled
			if yaml, err := subState.MarshalYAML(); err == nil {
				row.CurrentYAML = yaml
			}
		}
		for _, f := range sub.Fields {
			row.Fields = append(row.Fields, buildFieldRow(f, subState))
		}
		data.Subsections = append(data.Subsections, row)
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "server-config-section", data)
}

// readServerYAMLState reads the live server.yaml off disk. The path is set
// at startup via SetServerConfigPath; an empty path returns an empty state
// (the UI will show every section as disabled rather than 500).
func (h *Handler) readServerYAMLState() (*config.ServerYAMLState, error) {
	if h.serverConfigPath == "" {
		return &config.ServerYAMLState{}, nil
	}
	return config.ReadServerYAMLState(h.serverConfigPath)
}

// buildFieldRow renders a single SchemaField as a UI row, filling in the
// current value from the raw YAML when the section exists.
func buildFieldRow(f config.SchemaField, sec *config.SectionState) ServerConfigFieldRow {
	row := ServerConfigFieldRow{
		Name:        f.Name,
		Description: f.Description,
		GoType:      f.GoType,
		Kind:        f.Kind,
		Options:     f.Options,
		Default:     f.Default,
		Example:     f.Example,
	}
	if sec != nil {
		v := sec.FieldValue(f.Name)
		if v != "" {
			row.Value = v
			row.HasValue = true
		}
	}
	return row
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
	search := r.URL.Query().Get("search")
	data := h.dataSource.EnvoysData(h.baseInterval, statusFilter, after, 100, search)
	h.enrichEnvoyRows(data.Envoys)
	h.renderer.RenderFragment(w, "envoys", "envoys-table", data)
}

// --- Production data source ---

type dbDataSource struct {
	queries           *db.Queries
	configStore       *config.ConfigStore
	globalObserveOnly bool
}

// envoyComplianceInfo computes convergence status for an envoy using recent runs.
func (ds *dbDataSource) envoyComplianceInfo(ctx context.Context, c db.Envoy, baseInterval time.Duration) (status string, runInfo *convergence.RunInfo) {
	latestRuns, err := ds.queries.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
		EnvoyID: c.ID,
		Limit:    int64(convergence.DivergedThreshold),
	})
	if err != nil {
		return convergence.ComputeConvergence(nil), nil
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

	return convergence.ComputeConvergence(recent), runInfo
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
	driftCount := 0
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
		case "relapsed", "diverged":
			driftCount++
		case "failed":
			failureCount++
		}
	}

	driftRate := float64(0)
	if totalEnvoys > 0 {
		driftRate = float64(driftCount) / float64(totalEnvoys) * 100
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
		Relapsed:       int64(summary["relapsed"]),
		Diverged:       int64(summary["diverged"]),
		Failed:         int64(summary["failed"]),
		Offline:    int64(summary["offline"]),
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

func (ds *dbDataSource) EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int, search string) EnvoysPageData {
	ctx := context.Background()

	// Fetch limit+1 to detect if there are more rows.
	fetchLimit := int64(limit + 1)
	if limit <= 0 {
		fetchLimit = 100001
	}

	// Single query: envoys joined with their latest run (no N+1).
	envoys, err := ds.queries.ListEnvoysPagedWithRun(ctx, afterHostname, search, fetchLimit)
	if err != nil {
		slog.Error("failed to list envoys", "error", err)
		return EnvoysPageData{PageData: PageData{Title: "Envoys", ActiveNav: "envoys"}}
	}

	var rows []EnvoyRow
	for _, n := range envoys {
		if limit > 0 && len(rows) >= limit {
			break
		}

		// Compute convergence from the latest run status.
		// For the paged view we only have 1 run, so we can determine
		// converged/failed/no-data but not relapsed/diverged (which need 2-3 runs).
		// This is acceptable for the paginated list — the fleet cache provides
		// accurate counts for the filter buttons.
		// Compute convergence status from failure percentage thresholds.
		var status string
		if !n.LastRunStatus.Valid {
			status = "no data"
		} else if n.LastRunFailed.Valid && n.LastRunModules.Valid && n.LastRunModules.Int64 > 0 && n.LastRunFailed.Int64 > 0 {
			pct := convergence.FailurePct(n.LastRunFailed.Int64, n.LastRunModules.Int64)
			if pct >= convergence.DegradedMaxPct {
				status = "failed"
			} else {
				status = "degraded"
			}
		} else {
			switch n.LastRunStatus.String {
			case "failure":
				status = "failed"
			case "blocked":
				status = "diverged"
			case "success", "partial":
				if n.LastRunChanged.Valid && n.LastRunChanged.Int64 > 0 {
					status = "relapsed"
				} else {
					status = "converged"
				}
			default:
				status = "no data"
			}
		}

		lastSeen := time.Time{}
		if n.LastSeen.Valid {
			lastSeen = n.LastSeen.Time
		}

		offline := !lastSeen.IsZero() && time.Since(lastSeen) > 2*baseInterval

		// Filter: "offline" matches reachability, everything else matches convergence status.
		if statusFilter == "offline" && !offline {
			continue
		} else if statusFilter != "" && statusFilter != "offline" && status != statusFilter {
			continue
		}

		var lastRunAt time.Time
		var lastRunID string
		if n.LastRunAt.Valid {
			lastRunAt = n.LastRunAt.Time
		}
		if n.LastRunID.Valid {
			lastRunID = n.LastRunID.String
		}

		// Check if this envoy is in observe-only mode: global (server.yaml) OR per-entry.
		observeOnly := ds.globalObserveOnly
		if ds.configStore != nil {
			if _, cfg := ds.configStore.Match(n.Hostname); cfg != nil {
				observeOnly = observeOnly || cfg.ObserveOnly
			}
		}
		if observeOnly {
			status = "observing"
		}

		var failurePct float64
		var failedCount, moduleCount int64
		if n.LastRunModules.Valid && n.LastRunModules.Int64 > 0 {
			moduleCount = n.LastRunModules.Int64
			failedCount = n.LastRunFailed.Int64
			failurePct = float64(failedCount) / float64(moduleCount) * 100
		}

		rows = append(rows, EnvoyRow{
			ID:           n.ID,
			Hostname:     n.Hostname,
			IPAddress:    n.IPAddress,
			EnrolledAt:   n.EnrolledAt,
			LastSeen:     lastSeen,
			LastRunAt:    lastRunAt,
			LastRunID:    lastRunID,
			Status:       status,
			FailurePct:   failurePct,
			ModuleCount:  moduleCount,
			FailedCount:  failedCount,
			Offline:      offline,
			ObserveOnly:  observeOnly,
			AgentVersion: n.AgentVersion,
		})
	}

	hasMore := len(envoys) > limit && limit > 0
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		nextCursor = rows[len(rows)-1].Hostname
	}

	return EnvoysPageData{
		PageData:     PageData{Title: "Envoys", ActiveNav: "envoys"},
		Envoys:       rows,
		StatusFilter: statusFilter,
		Search:       search,
		EnvoyCount:   len(rows),
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

	// Extract cloud metadata from traits (best-effort, non-fatal).
	var cloudProvider, cloudRegion, cloudZone, cloudInstanceType, cloudInstanceID, cloudAccountID string
	if cf, tErr := ds.queries.GetLatestEnvoyTraits(ctx, envoyID); tErr == nil {
		var raw map[string]any
		if json.Unmarshal([]byte(cf.Traits), &raw) == nil {
			if cloud, ok := raw["cloud"].(map[string]any); ok {
				if v, ok := cloud["provider"].(string); ok && v != "none" {
					cloudProvider = v
				}
				if v, ok := cloud["region"].(string); ok {
					cloudRegion = v
				}
				if v, ok := cloud["zone"].(string); ok {
					cloudZone = v
				}
				if v, ok := cloud["instance_type"].(string); ok {
					cloudInstanceType = v
				}
				if v, ok := cloud["instance_id"].(string); ok {
					cloudInstanceID = v
				}
				if v, ok := cloud["account_id"].(string); ok {
					cloudAccountID = v
				}
			}
		}
	}

	// Shell only — runs and traits are loaded via HTMX fragments.
	return EnvoyDetailPageData{
		PageData: PageData{Title: "Envoy: " + envoy.Hostname, ActiveNav: "envoys"},
		Envoy: EnvoyRow{
			ID:           envoy.ID,
			Hostname:     envoy.Hostname,
			IPAddress:    envoy.IpAddress,
			EnrolledAt:   envoy.EnrolledAt,
			LastSeen:     lastSeen,
			Status:       status,
			Offline:      !lastSeen.IsZero() && time.Since(lastSeen) > 2*baseInterval,
			AgentVersion: envoy.AgentVersion,
		},
		CloudProvider:     cloudProvider,
		CloudRegion:       cloudRegion,
		CloudZone:         cloudZone,
		CloudInstanceType: cloudInstanceType,
		CloudInstanceID:   cloudInstanceID,
		CloudAccountID:    cloudAccountID,
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
		var runPct float64
		if r.ModuleCount > 0 {
			runPct = float64(r.FailedCount) / float64(r.ModuleCount) * 100
		}
		runs = append(runs, EnvoyDetailRunRow{
			RunRow: RunRow{
				ID:           r.ID,
				Hostname:     envoy.Hostname,
				StartedAt:    r.StartedAt,
				Status:       r.Status,
				ModuleCount:  r.ModuleCount,
				ChangedCount: r.ChangedCount,
				FailedCount:  r.FailedCount,
				FailurePct:   runPct,
			},
			Results: resultsByRun[r.ID],
		})
		var driftPct float64
		if r.ModuleCount > 0 {
			driftPct = float64(r.FailedCount) / float64(r.ModuleCount) * 100
		}
		driftHistory = append(driftHistory, DriftPoint{
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
			ModuleCount:  r.ModuleCount,
			FailurePct:   driftPct,
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
// values safe for template rendering. Arrays are expanded with indexed keys
// (e.g., blockdevices.sda.model, gpu.devices.0.name).
func flattenTraits(prefix string, data map[string]any, out map[string]any) {
	for key, val := range data {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch v := val.(type) {
		case map[string]any:
			flattenTraits(fullKey, v, out)
		case []any:
			for i, elem := range v {
				elemKey := fullKey + "." + strconv.Itoa(i)
				switch e := elem.(type) {
				case map[string]any:
					flattenTraits(elemKey, e, out)
				default:
					b, _ := json.Marshal(e)
					out[elemKey] = string(b)
				}
			}
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

	// Single query: runs joined with envoy hostname (no separate ListEnvoys).
	allRuns, err := ds.queries.ListRunsWithHostname(ctx, 100)
	if err != nil {
		slog.Error("failed to list runs", "error", err)
		return RunsPageData{PageData: PageData{Title: "Runs", ActiveNav: "runs"}}
	}

	var rows []RunRow
	for _, r := range allRuns {
		if statusFilter != "" && r.Status != statusFilter {
			continue
		}
		var failPct float64
		if r.ModuleCount > 0 {
			failPct = float64(r.FailedCount) / float64(r.ModuleCount) * 100
		}
		rows = append(rows, RunRow{
			ID:           r.ID,
			EnvoyID:      r.EnvoyID,
			Hostname:     r.Hostname,
			StartedAt:    r.StartedAt,
			Status:       r.Status,
			ModuleCount:  r.ModuleCount,
			ChangedCount: r.ChangedCount,
			FailedCount:  r.FailedCount,
			FailurePct:   failPct,
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
			FailurePct:   convergence.FailurePct(r.FailedCount, r.ModuleCount),
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

func (ds *dbDataSource) RiskPostureData(_ time.Duration) RiskPosturePageData {
	return RiskPosturePageData{}
}

func (ds *dbDataSource) CertExpirySummary() CertExpirySummary {
	// DB-backed datasource does not have access to flattened traits.
	// Fleet DataSource computes the real values.
	return CertExpirySummary{}
}

func (ds *dbDataSource) RunningCVESummary() RunningCVESummary {
	// Same reason as CertExpirySummary — trait aggregation lives on
	// the Fleet DataSource.
	return RunningCVESummary{}
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

// AuditPageData holds data for the audit log page.
type AuditPageData struct {
	PageData
	Events      []AuditEventRow
	EventTypes  []string // distinct event types for filter buttons
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

// Audit renders the audit log page with chain verification status.
func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	typeFilter := r.URL.Query().Get("type")

	// Build query string for HTMX auto-refresh.
	qs := ""
	if typeFilter != "" {
		qs = "type=" + typeFilter
	}

	// Fetch audit entries from all sources (always, to build type list).
	var allEvents []AuditEventRow

	// Tamper-evident audit entries (hash-chained).
	entries, err := h.queries.ListAuditEntries(ctx, 100)
	if err != nil {
		slog.Error("failed to list audit entries", "error", err)
	}
	for _, e := range entries {
		allEvents = append(allEvents, AuditEventRow{
			Timestamp: e.Timestamp.Local().Format("2006-01-02 15:04:05"),
			Type:      e.EventType,
			Operator:  e.Actor,
			Source:    e.Source,
			Summary:   e.Summary,
			Payload:   e.Payload,
		})
	}

	// Config publishes.
	publishes, err := h.queries.ListConfigPublishes(ctx, 100)
	if err == nil {
		for _, p := range publishes {
			var changed []string
			_ = json.Unmarshal([]byte(p.ChangedFiles), &changed)
			summary := fmt.Sprintf("%d files changed", len(changed))
			if len(changed) > 0 {
				summary += ": " + strings.Join(changed, ", ")
			}
			allEvents = append(allEvents, AuditEventRow{
				Timestamp: p.PublishedAt.Local().Format("2006-01-02 15:04:05"),
				Type:      "config_publish",
				Operator:  p.Operator,
				Source:    "rest",
				Summary:   summary,
			})
		}
	}

	// Task runs.
	pushRuns, err := h.queries.ListPushRuns(ctx, 100)
	if err == nil {
		for _, pr := range pushRuns {
			allEvents = append(allEvents, AuditEventRow{
				Timestamp: pr.CreatedAt.Local().Format("2006-01-02 15:04:05"),
				Type:      "task_run",
				Operator:  pr.Operator,
				Source:    "rest",
				Summary:   pr.Command + " on " + pr.TargetGlob,
				DetailURL: "/task/" + pr.ID,
			})
		}
	}

	// Workflow runs.
	workflowRuns, err := h.queries.ListWorkflowRuns(ctx, 100)
	if err == nil {
		for _, wr := range workflowRuns {
			allEvents = append(allEvents, AuditEventRow{
				Timestamp: wr.CreatedAt.Local().Format("2006-01-02 15:04:05"),
				Type:      "workflow_run",
				Operator:  wr.Operator,
				Source:    "rest",
				Summary:   wr.WorkflowName + " (" + wr.Status + ")",
				DetailURL: "/workflows/" + wr.ID,
			})
		}
	}

	// Collect distinct event types for filter buttons.
	typeSeen := make(map[string]bool)
	for _, e := range allEvents {
		typeSeen[e.Type] = true
	}
	var eventTypes []string
	for t := range typeSeen {
		eventTypes = append(eventTypes, t)
	}
	sort.Strings(eventTypes)

	// Filter by type if requested.
	var events []AuditEventRow
	if typeFilter == "" {
		events = allEvents
	} else {
		for _, e := range allEvents {
			if e.Type == typeFilter {
				events = append(events, e)
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
		EventTypes:  eventTypes,
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
	Relapsed       int32
	Diverged       int32
	Failed         int32
	Offline    int32
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
	TotalConverged  int32
	TotalRelapsed   int32
	TotalDiverged   int32
	TotalFailed     int32
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
	var totalEnvoys, totalCompliant, totalRelapsed, totalDiverged, totalFailed int32
	for _, s := range statuses {
		spokes = append(spokes, SpannerSpokeRow{
			ID:             s.ID,
			Addr:           s.Addr,
			Patterns:       s.Patterns,
			Healthy:        s.Healthy,
			EnvoyCount:    s.EnvoyCount,
			Converged:      s.Converged,
			Relapsed:       s.Relapsed,
			Diverged:       s.Diverged,
			Failed:         s.Failed,
			Offline:    s.Offline,
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
		totalRelapsed += s.Relapsed
		totalDiverged += s.Diverged
		totalFailed += s.Failed
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
		TotalRelapsed:       totalRelapsed,
		TotalDiverged:       totalDiverged,
		TotalFailed:         totalFailed,
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
	Relapsed       int32
	Diverged       int32
	Failed         int32
	Offline    int32
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
	FailurePct   float64
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
		var spokePct float64
		if run.ModuleCount > 0 {
			spokePct = float64(run.FailedCount) / float64(run.ModuleCount) * 100
		}
		row := SpokeDetailRunRow{
			ID:           run.Id,
			EnvoyID:     run.EnvoyId,
			Status:       run.Status,
			ModuleCount:  run.ModuleCount,
			ChangedCount: run.ChangedCount,
			FailedCount:  run.FailedCount,
			FailurePct:   spokePct,
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
		Relapsed:       spoke.Relapsed,
		Diverged:       spoke.Diverged,
		Failed:         spoke.Failed,
		Offline:    spoke.Offline,
		NoData:         spoke.NoData,
		ConvergencePct:  spoke.ConvergencePct,
		LatencyMs:      spoke.LatencyMs,
		Envoys:        envoyRows,
		Runs:           runRows,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "spoke-detail", data)
}

// SecurityDriftRow represents a package drift entry for the security page.
type SecurityDriftRow struct {
	Hostname         string
	PackageName      string
	InstalledVersion string
	DesiredVersion   string
}

// SecurityScanPageData holds aggregated security scan results for the security page.
type SecurityScanPageData struct {
	// Fleet-wide totals (raw findings — same CVE affecting multiple packages counts multiple times).
	TrivyCritical  int64
	TrivyHigh      int64
	TrivyMedium    int64
	TrivyLow       int64
	// Unique CVE counts (deduplicated across the fleet).
	UniqueCVEs     int64 // total unique CVE IDs
	UniqueCritical int64
	UniqueHigh     int64
	UniqueMedium   int64
	UniqueLow      int64
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
	CVEs           []CVEEntry         // deduplicated CVEs across fleet (one per CVE ID)
	PerHost        []SecurityHostRow  // per-envoy breakdown
}

// CVEEntry is a single CVE keyed by CVE ID (deduplicated across scanners).
type CVEEntry struct {
	CVEID    string // CVE identifier (e.g., "CVE-2024-1234")
	Severity string // "critical", "high", "medium", or "low" (lowercase)
	Packages int64  // number of packages affected by this CVE
}

// SecurityHostRow holds per-envoy security scan summary.
type SecurityHostRow struct {
	ID             string
	Hostname       string
	TrivyCritical  int64 // raw scanner count
	TrivyHigh      int64
	TrivyMedium    int64
	TrivyLow       int64
	UniqueCritical int64 // unique CVE IDs (agent-authoritative)
	UniqueHigh     int64
	UniqueMedium   int64
	UniqueLow      int64
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

// SecurityPage renders the fleet security dashboard — convergence,
// CVE counts, hardening scores, and the AI chat drawer overlay.
func (h *Handler) SecurityPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	convData := h.dataSource.ConvergenceData(h.baseInterval)

	var driftRows []SecurityDriftRow
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
				driftRows = append(driftRows, SecurityDriftRow{
					Hostname:         row.Hostname,
					PackageName:      row.PackageName,
					InstalledVersion: installed,
					DesiredVersion:   desired,
				})
			}
		}
	}

	secData := h.dataSource.SecurityScanData()
	certData := h.dataSource.CertExpirySummary()

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
		Relapsed         int64
		Diverged         int64
		Failed           int64
		Offline      int64
		NoData           int64
		DriftCount       int
		DriftRows        []SecurityDriftRow
		Security         SecurityScanPageData
		Certs            CertExpirySummary
		QueryTimeoutSec  int
		CVETrend         []float64
		HardeningTrend   []float64
	}{
		PageData:        PageData{Title: "Security", ActiveNav: "security"},
		ConvergencePct:   convData.ConvergencePct,
		Total:           convData.Total,
		Converged:       convData.Converged,
		Relapsed:        convData.Relapsed,
		Diverged:        convData.Diverged,
		Failed:          convData.Failed,
		Offline:     convData.Offline,
		NoData:          convData.NoData,
		DriftCount:      len(driftRows),
		DriftRows:       driftRows,
		Security:        secData,
		Certs:           certData,
		QueryTimeoutSec: queryTimeoutSec,
		CVETrend:        cveTrend,
		HardeningTrend:  hardeningTrend,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "security", data)
}

// SecurityStatus renders the convergence+security left-panel fragment for HTMX polling.
func (h *Handler) SecurityStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	convData := h.dataSource.ConvergenceData(h.baseInterval)

	var driftRows []SecurityDriftRow
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
				driftRows = append(driftRows, SecurityDriftRow{
					Hostname:         row.Hostname,
					PackageName:      row.PackageName,
					InstalledVersion: installed,
					DesiredVersion:   desired,
				})
			}
		}
	}

	secData := h.dataSource.SecurityScanData()
	certData := h.dataSource.CertExpirySummary()

	// Load 30-day security posture trend (same as SecurityPage).
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
		Relapsed         int64
		Diverged         int64
		Failed           int64
		Offline      int64
		NoData           int64
		DriftCount       int
		DriftRows        []SecurityDriftRow
		Security         SecurityScanPageData
		Certs            CertExpirySummary
		CVETrend         []float64
		HardeningTrend   []float64
	}{
		ConvergencePct:   convData.ConvergencePct,
		Total:           convData.Total,
		Converged:       convData.Converged,
		Relapsed:        convData.Relapsed,
		Diverged:        convData.Diverged,
		Failed:          convData.Failed,
		Offline:     convData.Offline,
		NoData:          convData.NoData,
		DriftCount:      len(driftRows),
		DriftRows:       driftRows,
		Security:        secData,
		Certs:           certData,
		CVETrend:        cveTrend,
		HardeningTrend:  hardeningTrend,
	}
	h.renderer.RenderFragment(w, "security", "security-status", data)
}

// CVESearchFragment handles GET /ui/fragments/cve-search — searches fleet for a CVE.
// Delegates to the CVE API handler for the actual search, then renders HTML.
// Accepts ?running_only=true to drop hosts that report the CVE but have
// no running-binary match in their cve_running_correlation trait.
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

	runningOnly := r.URL.Query().Get("running_only") == "true"
	totalHosts, matches := h.cveSearcher.SearchCVE(cveID, runningOnly)

	if totalHosts == 0 {
		if runningOnly {
			fmt.Fprintf(w, `<p class="text-success">No hosts have running binaries affected by %s</p>`, template.HTMLEscapeString(cveID))
		} else {
			fmt.Fprintf(w, `<p class="text-success">No hosts affected by %s</p>`, template.HTMLEscapeString(cveID))
		}
		return
	}

	suffix := ""
	if runningOnly {
		suffix = " with running binaries affected"
	}
	fmt.Fprintf(w, `<p><strong>%s</strong> — %d host(s)%s</p>`, template.HTMLEscapeString(cveID), totalHosts, suffix)
	fmt.Fprint(w, `<div class="table-container"><table class="table"><thead><tr><th>Hostname</th><th>Package</th><th>Installed</th><th>Fixed</th><th>Severity</th><th>Scanner</th><th>Running Binaries</th></tr></thead><tbody>`)
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
		running := renderRunningBinariesCell(m.RunningBinaries)
		fmt.Fprintf(w, `<tr><td>%s</td><td class="mono">%s</td><td>%s</td><td>%s</td><td class="%s">%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(m.Hostname),
			template.HTMLEscapeString(m.Package),
			template.HTMLEscapeString(m.Installed),
			template.HTMLEscapeString(fixed),
			sevClass,
			template.HTMLEscapeString(m.Severity),
			template.HTMLEscapeString(m.Scanner),
			running)
	}
	fmt.Fprint(w, `</tbody></table></div>`)
}

// renderRunningBinariesCell produces the HTML content for the
// "Running Binaries" table cell on the CVE search result. Empty list
// renders as an em dash so the cell doesn't collapse to zero width.
// Each binary path appears on its own line; a `(deleted)` tag is
// appended when the on-disk file has been replaced but the process
// is still holding the old inode.
func renderRunningBinariesCell(bins []CVERunningBinaryRef) string {
	if len(bins) == 0 {
		return `<span class="text-muted">—</span>`
	}
	var b strings.Builder
	for i, rb := range bins {
		if i > 0 {
			b.WriteString("<br>")
		}
		b.WriteString(`<span class="mono">`)
		b.WriteString(template.HTMLEscapeString(rb.BinaryPath))
		b.WriteString(`</span>`)
		if rb.Deleted {
			b.WriteString(` <span class="text-warning" title="binary on disk was replaced or deleted — the running process is holding the old inode">(deleted)</span>`)
		}
		if rb.PidCount > 1 {
			fmt.Fprintf(&b, ` <span class="text-muted">×%d</span>`, rb.PidCount)
		}
	}
	return b.String()
}

// AskStream handles POST /ui/ask — proxies to the AI assistant with server-side markdown rendering.
func (h *Handler) AskStream(w http.ResponseWriter, r *http.Request) {
	if h.assistant == nil {
		http.Error(w, "AI assistant not enabled", http.StatusNotFound)
		return
	}

	var req struct {
		Question string `json:"question"`
		Context  struct {
			Envoy     string `json:"envoy,omitempty"`
			RunID     string `json:"run_id,omitempty"`
			Framework string `json:"framework,omitempty"`
			Page      string `json:"page,omitempty"`
		} `json:"context,omitempty"`
		History []struct {
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

	aiReq := ai.AskRequest{
		Question: req.Question,
		Context: ai.AskContext{
			Envoy:     req.Context.Envoy,
			RunID:     req.Context.RunID,
			Framework: req.Context.Framework,
			Page:      req.Context.Page,
		},
	}
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


// Admin handlers removed — all admin functions are CLI-only (vigocli).

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

// Support renders the support options page: the full tier schedule
// from license.Tiers with the customer's current tier highlighted,
// plus Critical Support add-on info and the support inbox.
func (h *Handler) Support(w http.ResponseWriter, r *http.Request) {
	data := SupportPageData{
		PageData:         PageData{Title: "Support", ActiveNav: "support"},
		Tiers:            license.Tiers,
		ContactEmail:     license.SupportContactEmail,
		CriticalFrom:     license.CriticalSupportAddOn.EligibleFrom,
		CriticalResponse: license.CriticalSupportAddOn.ResponseTime,
		CriticalHours:    license.CriticalSupportAddOn.Hours,
		CriticalChannel:  license.CriticalSupportAddOn.Channel,
		CriticalPricing:  license.CriticalSupportAddOn.Pricing,
	}

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		data.MaxEnvoys = s.MaxEnvoys
		data.IsFreeTier = h.enforcer.IsFreeLicense()
		if s.License != nil {
			data.CustomerName = s.License.CustomerName
			data.LicenseID = s.License.LicenseID
		}
		if t := license.TierForNodes(s.MaxEnvoys); t != nil {
			data.ActiveTier = t.Name
			// Growth and above qualify for the Critical Support add-on.
			// Community is index 0, Starter 1, Growth 2.
			for i := range license.Tiers {
				if license.Tiers[i].Name == t.Name && i >= 2 {
					data.CriticalEligible = true
					break
				}
			}
		}
	}

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "support", data)
}

// --- Under Review pages ---

// inventoryGroupRow is a single row in a grouped inventory view.
type inventoryGroupRow struct {
	Label string
	Count int
	Hosts []string // first N hostnames
}

// inventoryChangeRow is a single trait change detected between snapshots.
type inventoryChangeRow struct {
	Hostname string
	Key      string
	OldValue string
	NewValue string
	When     string
}

// Inventory renders the fleet inventory dashboard with summary cards,
// grouped views, change log, and a power-user filter bar.
func (h *Handler) Inventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	where := r.URL.Query().Get("where")
	show := r.URL.Query().Get("show")
	groupBy := r.URL.Query().Get("group")
	if groupBy == "" {
		groupBy = "os"
	}

	all, _ := h.queries.GetAllLatestTraits(ctx)

	// --- Summary cards ---
	totalEnvoys := len(all)
	osCounts := map[string]int{}
	archCounts := map[string]int{}
	staleCount := 0
	staleCutoff := time.Now().Add(-7 * 24 * time.Hour)

	type parsedEnvoy struct {
		ID       string
		Hostname string
		IP       string
		Traits   map[string]any
		Flat     map[string]string
		Collected time.Time
	}
	var parsed []parsedEnvoy

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flatMap := make(map[string]any)
		flattenTraits("", traits, flatMap)
		flat := make(map[string]string, len(flatMap))
		for k, v := range flatMap {
			flat[k] = fmt.Sprint(v)
		}
		parsed = append(parsed, parsedEnvoy{
			ID: t.ID, Hostname: t.Hostname, IP: t.IPAddress,
			Traits: traits, Flat: flat, Collected: t.CollectedAt,
		})

		distro := inventoryTraverse(traits, "os.distro")
		version := inventoryTraverse(traits, "os.version")
		osLabel := strings.TrimSpace(distro)
		if version != "-" && version != "" {
			osLabel = strings.TrimSpace(osLabel + " " + version)
		}
		if osLabel != "" && osLabel != "-" {
			osCounts[osLabel]++
		}

		arch := inventoryTraverse(traits, "os.arch")
		if arch != "-" && arch != "" {
			archCounts[arch]++
		}

		if t.CollectedAt.Before(staleCutoff) {
			staleCount++
		}
	}

	// --- Fleet health alerts ---
	type healthAlert struct {
		Level   string // "danger", "warning"
		Title   string
		Count   int
		Hosts   []string
	}
	var alerts []healthAlert

	// EOL OS check.
	var eolHosts, eolSoonHosts []string
	for _, p := range parsed {
		distro := inventoryTraverse(p.Traits, "os.distro")
		version := inventoryTraverse(p.Traits, "os.version")
		if distro == "-" || distro == "" {
			continue
		}
		if e := checkEOL(distro, version); e != nil {
			eolHosts = append(eolHosts, p.Hostname+" ("+e.Label+")")
		} else if e := checkEOLSoon(distro, version, 180*24*time.Hour); e != nil {
			eolSoonHosts = append(eolSoonHosts, p.Hostname+" ("+e.Label+", EOL "+e.EOL.Format("Jan 2006")+")")
		}
	}
	if len(eolHosts) > 0 {
		alerts = append(alerts, healthAlert{Level: "danger", Title: "End-of-life OS", Count: len(eolHosts), Hosts: eolHosts})
	}
	if len(eolSoonHosts) > 0 {
		alerts = append(alerts, healthAlert{Level: "warning", Title: "OS end-of-life within 6 months", Count: len(eolSoonHosts), Hosts: eolSoonHosts})
	}

	// Disk usage >90% check.
	var diskFullHosts []string
	for _, p := range parsed {
		for k, v := range p.Flat {
			if strings.HasSuffix(k, ".percent") && strings.HasPrefix(k, "filesystem.") {
				if pct, err := strconv.ParseFloat(v, 64); err == nil && pct >= 90 {
					diskFullHosts = append(diskFullHosts, p.Hostname+" ("+k+": "+v+"%)")
					break // one alert per host
				}
			}
		}
	}
	if len(diskFullHosts) > 0 {
		alerts = append(alerts, healthAlert{Level: "warning", Title: "Disk usage above 90%", Count: len(diskFullHosts), Hosts: diskFullHosts})
	}

	// Critical CVEs check.
	var cveHosts []string
	for _, p := range parsed {
		crit := inventoryTraverse(p.Traits, "security_scan.cve_critical")
		if crit != "-" && crit != "" && crit != "0" {
			cveHosts = append(cveHosts, p.Hostname+" ("+crit+" critical)")
		}
	}
	if len(cveHosts) > 0 {
		alerts = append(alerts, healthAlert{Level: "danger", Title: "Critical CVEs detected", Count: len(cveHosts), Hosts: cveHosts})
	}

	// Fleet diversity score.
	uniqueOS := len(osCounts)
	uniqueArch := len(archCounts)

	// --- Grouped view ---
	groupField := "os.distro"
	groupLabel := "OS Distribution"
	switch groupBy {
	case "os":
		groupField = "os.distro"
		groupLabel = "OS Distribution"
	case "version":
		groupField = "" // special: distro + version
		groupLabel = "OS Version"
	case "arch":
		groupField = "os.arch"
		groupLabel = "Architecture"
	case "kernel":
		groupField = "kernel.version"
		groupLabel = "Kernel Version"
	case "virtual":
		groupField = "virtual.system"
		groupLabel = "Virtualization"
	case "product":
		groupField = "dmi.product_name"
		groupLabel = "Model"
	case "board":
		groupField = "dmi.board_name"
		groupLabel = "Motherboard"
	case "bios":
		groupField = "dmi.bios_version"
		groupLabel = "BIOS / Firmware"
	case "tpm":
		groupField = "tpm.version"
		groupLabel = "TPM Version"
	case "secureboot":
		groupField = "secureboot.enabled"
		groupLabel = "Secure Boot"
	case "cpu":
		groupField = "processors.model"
		groupLabel = "CPU Model"
	case "gpu":
		groupField = "" // special: nested array
		groupLabel = "GPU"
	case "disk":
		groupField = "" // special: nested object
		groupLabel = "Disk Model"
	case "location":
		groupField = "" // special: config directory path
		groupLabel = "Location / Org"
	}

	// Build hostname→dirPath map for location grouping.
	var dirPaths map[string]string
	if groupBy == "location" && h.configStore != nil {
		matchPaths := h.configStore.MatchDirPaths()
		dirPaths = make(map[string]string, len(parsed))
		for _, p := range parsed {
			// Try exact match first, then check glob patterns.
			if path, ok := matchPaths[p.Hostname]; ok {
				dirPaths[p.Hostname] = path
			} else {
				dirPaths[p.Hostname] = "(unmatched)"
			}
		}
	}

	groupCounts := map[string][]string{} // label → hostnames
	for _, p := range parsed {
		// Multi-value groups: one envoy can contribute to multiple groups.
		switch groupBy {
		case "gpu":
			labels := inventoryAllGPUs(p.Traits)
			if len(labels) == 0 {
				groupCounts["(not reported)"] = append(groupCounts["(not reported)"], p.Hostname)
			} else {
				for _, label := range labels {
					groupCounts[label] = append(groupCounts[label], p.Hostname)
				}
			}
		case "disk":
			labels := inventoryAllBlockDevices(p.Traits, "blockdevices")
			if len(labels) == 0 {
				groupCounts["(not reported)"] = append(groupCounts["(not reported)"], p.Hostname)
			} else {
				for _, label := range labels {
					groupCounts[label] = append(groupCounts[label], p.Hostname)
				}
			}
		default:
			var label string
			switch groupBy {
			case "version":
				d := inventoryTraverse(p.Traits, "os.distro")
				v := inventoryTraverse(p.Traits, "os.version")
				label = strings.TrimSpace(d)
				if v != "-" && v != "" {
					label = strings.TrimSpace(label + " " + v)
				}
			case "location":
				if path, ok := dirPaths[p.Hostname]; ok {
					label = path
				}
			default:
				label = inventoryTraverse(p.Traits, groupField)
			}
			if label == "-" || label == "" {
				label = "(not reported)"
			}
			groupCounts[label] = append(groupCounts[label], p.Hostname)
		}
	}
	var groups []inventoryGroupRow
	for label, hosts := range groupCounts {
		sort.Strings(hosts)
		preview := hosts
		if len(preview) > 5 {
			preview = preview[:5]
		}
		groups = append(groups, inventoryGroupRow{Label: label, Count: len(hosts), Hosts: preview})
	}
	// Sort: known groups by count descending, "(not reported)" always last.
	sort.Slice(groups, func(i, j int) bool {
		iUnknown := groups[i].Label == "(not reported)"
		jUnknown := groups[j].Label == "(not reported)"
		if iUnknown != jUnknown {
			return jUnknown // unknown goes last
		}
		return groups[i].Count > groups[j].Count
	})

	// --- Change log ---
	var changes []inventoryChangeRow
	pairs, _ := h.queries.GetTraitPairs(ctx)
	for _, p := range pairs {
		if p.PreviousTraits == "" {
			continue
		}
		var curr, prev map[string]any
		if json.Unmarshal([]byte(p.CurrentTraits), &curr) != nil {
			continue
		}
		if json.Unmarshal([]byte(p.PreviousTraits), &prev) != nil {
			continue
		}
		currFlat := make(map[string]any)
		prevFlat := make(map[string]any)
		flattenTraits("", curr, currFlat)
		flattenTraits("", prev, prevFlat)
		for k, cv := range currFlat {
			cs := fmt.Sprint(cv)
			ps := "-"
			if pv, ok := prevFlat[k]; ok {
				ps = fmt.Sprint(pv)
			}
			if cs != ps {
				changes = append(changes, inventoryChangeRow{
					Hostname: p.Hostname,
					Key:      k,
					OldValue: ps,
					NewValue: cs,
					When:     p.CurrentCollected.Local().Format("2006-01-02 15:04"),
				})
			}
		}
		// Detect removed traits.
		for k, pv := range prevFlat {
			if _, ok := currFlat[k]; !ok {
				changes = append(changes, inventoryChangeRow{
					Hostname: p.Hostname,
					Key:      k,
					OldValue: fmt.Sprint(pv),
					NewValue: "(removed)",
					When:     p.CurrentCollected.Local().Format("2006-01-02 15:04"),
				})
			}
		}
	}
	// Sort by time descending, limit to 50.
	sort.Slice(changes, func(i, j int) bool { return changes[i].When > changes[j].When })
	if len(changes) > 50 {
		changes = changes[:50]
	}

	// --- OS summary for cards ---
	var osSummary []inventoryGroupRow
	for label, count := range osCounts {
		osSummary = append(osSummary, inventoryGroupRow{Label: label, Count: count})
	}
	sort.Slice(osSummary, func(i, j int) bool { return osSummary[i].Count > osSummary[j].Count })
	if len(osSummary) > 6 {
		osSummary = osSummary[:6]
	}

	data := struct {
		PageData
		Where        string
		Show         string
		GroupBy      string
		GroupLabel   string
		Total        int
		StaleCount   int
		OSSummary    []inventoryGroupRow
		ArchCounts   map[string]int
		Groups       []inventoryGroupRow
		Changes      []inventoryChangeRow
		ChangeCount  int
		Alerts       []healthAlert
		UniqueOS     int
		UniqueArch   int
	}{
		PageData:     PageData{Title: "Inventory", ActiveNav: "inventory"},
		Where:        where,
		Show:         show,
		GroupBy:      groupBy,
		GroupLabel:   groupLabel,
		Total:        totalEnvoys,
		StaleCount:   staleCount,
		OSSummary:    osSummary,
		ArchCounts:   archCounts,
		Groups:       groups,
		Changes:      changes,
		ChangeCount:  len(changes),
		Alerts:       alerts,
		UniqueOS:     uniqueOS,
		UniqueArch:   uniqueArch,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "inventory", data)
}

// InventoryDetail renders the per-envoy inventory page showing all collected
// traits grouped by collector namespace.
func (h *Handler) InventoryDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	// Get envoy info.
	envoy, err := h.queries.GetEnvoyByID(r.Context(), idStr)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	// Get latest traits.
	var rawTraits map[string]any
	flat := make(map[string]string)
	if cf, err := h.queries.GetLatestEnvoyTraits(r.Context(), idStr); err == nil {
		if json.Unmarshal([]byte(cf.Traits), &rawTraits) == nil {
			flatMap := make(map[string]any)
			flattenTraits("", rawTraits, flatMap)
			for k, v := range flatMap {
				flat[k] = fmt.Sprint(v)
			}
		}
	}

	// Group by namespace (first segment of dot-path).
	type traitGroup struct {
		Name   string
		Traits []struct{ Key, Value string }
	}
	groupMap := make(map[string][]struct{ Key, Value string })
	for k, v := range flat {
		ns := k
		if idx := strings.IndexByte(k, '.'); idx > 0 {
			ns = k[:idx]
		}
		groupMap[ns] = append(groupMap[ns], struct{ Key, Value string }{k, v})
	}
	var groups []traitGroup
	for ns, traits := range groupMap {
		sort.Slice(traits, func(i, j int) bool { return traits[i].Key < traits[j].Key })
		groups = append(groups, traitGroup{Name: ns, Traits: traits})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	data := struct {
		PageData
		EnvoyID  string
		Hostname string
		IP       string
		Groups   []traitGroup
		Total    int
	}{
		PageData: PageData{Title: "Inventory: " + envoy.Hostname, ActiveNav: "inventory"},
		EnvoyID:  idStr,
		Hostname: envoy.Hostname,
		IP:       envoy.IpAddress,
		Groups:   groups,
		Total:    len(flat),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "inventory-detail", data)
}

// InventoryHardwareDetail shows a dedicated page for a specific piece of hardware,
// listing every envoy that has it installed with version/firmware details.
func (h *Handler) InventoryHardwareDetail(w http.ResponseWriter, r *http.Request) {
	hwType := r.PathValue("hwtype")
	label := r.PathValue("label")
	if hwType == "" || label == "" {
		http.Error(w, "missing type or label", http.StatusBadRequest)
		return
	}

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Define columns per hardware type.
	type hwCol struct {
		Header string
		Path   string // dot-path, or empty for special handling
	}
	var cols []hwCol
	var typeLabel string

	switch hwType {
	case "cpu":
		typeLabel = "CPU"
		cols = []hwCol{
			{"Cores", "processors.physical_cores"},
			{"Threads", "processors.logical_cores"},
			{"Clock (MHz)", "processors.mhz"},
			{"Microcode", "processors.microcode"},
			{"Architecture", "os.arch"},
		}
	case "gpu":
		typeLabel = "GPU"
		cols = []hwCol{
			{"Driver", ""},
			{"Driver Version", ""},
			{"Firmware", ""},
			{"VBIOS", ""},
			{"Memory (MB)", ""},
			{"Vendor", ""},
		}
	case "disk":
		typeLabel = "Disk"
		cols = []hwCol{
			{"Device", ""},
			{"Firmware", ""},
			{"Size", ""},
		}
	case "product":
		typeLabel = "Product"
		cols = []hwCol{
			{"Vendor", "dmi.product_vendor"},
			{"Board", "dmi.board_name"},
			{"BIOS", "dmi.bios_version"},
			{"BIOS Date", "dmi.bios_date"},
			{"Chassis", "dmi.chassis_type"},
		}
	case "board":
		typeLabel = "Board"
		cols = []hwCol{
			{"Vendor", "dmi.board_vendor"},
			{"Product", "dmi.product_name"},
			{"BIOS", "dmi.bios_version"},
			{"BIOS Date", "dmi.bios_date"},
		}
	case "bios":
		typeLabel = "BIOS"
		cols = []hwCol{
			{"Vendor", "dmi.bios_vendor"},
			{"Date", "dmi.bios_date"},
			{"Product", "dmi.product_name"},
			{"Board", "dmi.board_name"},
		}
	case "tpm":
		typeLabel = "TPM"
		cols = []hwCol{
			{"Firmware", "tpm.firmware_version"},
			{"Manufacturer", "tpm.manufacturer"},
		}
	case "os":
		typeLabel = "OS"
		cols = []hwCol{
			{"Version", "os.version"},
			{"Kernel", "kernel.version"},
			{"Architecture", "os.arch"},
			{"Family", "os.family"},
		}
	case "version":
		typeLabel = "OS Version"
		cols = []hwCol{
			{"Kernel", "kernel.version"},
			{"Architecture", "os.arch"},
			{"Family", "os.family"},
		}
	case "kernel":
		typeLabel = "Kernel"
		cols = []hwCol{
			{"OS", "os.distro"},
			{"OS Version", "os.version"},
			{"Architecture", "os.arch"},
		}
	default:
		typeLabel = strings.Title(hwType)
		cols = []hwCol{
			{"OS", "os.distro"},
			{"Version", "os.version"},
			{"Arch", "os.arch"},
		}
	}

	// Match envoys.
	type hwRow struct {
		ID       string
		Hostname string
		IP       string
		Fields   []string
	}
	var rows []hwRow

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}

		matched := false
		var specialFields []string

		switch hwType {
		case "disk":
			if bd, ok := traits["blockdevices"].(map[string]any); ok {
				for devName, v := range bd {
					dev, ok := v.(map[string]any)
					if !ok {
						continue
					}
					model := strings.TrimSpace(fmt.Sprint(dev["model"]))
					if model == "<nil>" {
						model = ""
					}
					devLabel := model
					if sizeBytes, ok := dev["size_bytes"].(float64); ok && sizeBytes > 0 && model != "" {
						gb := sizeBytes / (1024 * 1024 * 1024)
						if gb >= 1024 {
							devLabel = fmt.Sprintf("%s (%.1f TB, %s)", model, gb/1024, devName)
						} else {
							devLabel = fmt.Sprintf("%s (%.0f GB, %s)", model, gb, devName)
						}
					}
					if devLabel == label {
						matched = true
						fw := fmt.Sprint(dev["firmware_rev"])
						if fw == "<nil>" {
							fw = "-"
						}
						size := "-"
						if sb, ok := dev["size_bytes"].(float64); ok && sb > 0 {
							gb := sb / (1024 * 1024 * 1024)
							if gb >= 1024 {
								size = fmt.Sprintf("%.1f TB", gb/1024)
							} else {
								size = fmt.Sprintf("%.0f GB", gb)
							}
						}
						specialFields = []string{devName, fw, size}
						break
					}
				}
			}
		case "gpu":
			if ns, ok := traits["gpu"].(map[string]any); ok {
				if arr, ok := ns["devices"].([]any); ok {
					for _, elem := range arr {
						m, ok := elem.(map[string]any)
						if !ok {
							continue
						}
						name, _ := m["name"].(string)
						vendor, _ := m["vendor"].(string)
						driver, _ := m["driver"].(string)
						device, _ := m["device"].(string)
						fw, _ := m["firmware_version"].(string)
						driverVer, _ := m["driver_version"].(string)
						vbios, _ := m["vbios_version"].(string)
						memMB := "-"
						if mem, ok := m["memory_mb"].(float64); ok && mem > 0 {
							memMB = fmt.Sprintf("%.0f", mem)
						}

						var gpuLabel string
						switch {
						case name != "":
							gpuLabel = name
							if vendor != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(vendor)) {
								gpuLabel = vendor + " " + name
							}
						case driver != "":
							gpuLabel = driver
							if device != "" {
								gpuLabel = driver + " (" + device + ")"
							}
						case device != "":
							gpuLabel = device
						}
						if gpuLabel == label {
							matched = true
							orDash := func(s string) string {
								if s == "" {
									return "-"
								}
								return s
							}
							specialFields = []string{orDash(driver), orDash(driverVer), orDash(fw), orDash(vbios), memMB, orDash(vendor)}
							break
						}
					}
				}
			}
		default:
			groupField := ""
			switch hwType {
			case "os":
				groupField = "os.distro"
			case "version":
				d := inventoryTraverse(traits, "os.distro")
				v := inventoryTraverse(traits, "os.version")
				check := strings.TrimSpace(d)
				if v != "-" && v != "" {
					check = strings.TrimSpace(check + " " + v)
				}
				if check == label || (check == "" && label == "(not reported)") {
					matched = true
				}
			case "arch":
				groupField = "os.arch"
			case "kernel":
				groupField = "kernel.version"
			case "virtual":
				groupField = "virtual.system"
			case "cpu":
				groupField = "processors.model"
			case "product":
				groupField = "dmi.product_name"
			case "board":
				groupField = "dmi.board_name"
			case "bios":
				groupField = "dmi.bios_version"
			case "tpm":
				groupField = "tpm.version"
			case "secureboot":
				groupField = "secureboot.enabled"
			}
			if groupField != "" {
				val := inventoryTraverse(traits, groupField)
				if val == label || ((val == "-" || val == "") && label == "(not reported)") {
					matched = true
				}
			}
		}

		if !matched {
			continue
		}

		row := hwRow{ID: t.ID, Hostname: t.Hostname, IP: t.IPAddress}
		if len(specialFields) > 0 {
			row.Fields = specialFields
		} else {
			for _, col := range cols {
				if col.Path != "" {
					row.Fields = append(row.Fields, inventoryTraverse(traits, col.Path))
				} else {
					row.Fields = append(row.Fields, "-")
				}
			}
		}
		rows = append(rows, row)
	}

	type hwColTmpl struct {
		Header string
	}
	var colsTmpl []hwColTmpl
	for _, c := range cols {
		colsTmpl = append(colsTmpl, hwColTmpl{Header: c.Header})
	}

	data := struct {
		PageData
		HWType    string
		TypeLabel string
		Label     string
		Cols      []hwColTmpl
		Rows      []hwRow
		Count     int
	}{
		PageData:  PageData{Title: typeLabel + ": " + label, ActiveNav: "inventory"},
		HWType:    hwType,
		TypeLabel: typeLabel,
		Label:     label,
		Cols:      colsTmpl,
		Rows:      rows,
		Count:     len(rows),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "inventory-hardware", data)
}

// InventoryCSVExport generates a CSV download of the fleet inventory with
// auditor-friendly columns: hostname, IP, OS, version, kernel, architecture,
// serial number, enrolled date, last check-in, and compliance status.
func (h *Handler) InventoryCSVExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := h.queries.GetAllLatestTraits(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fleet-inventory-%s.csv"`, time.Now().Format("2006-01-02")))

	fmt.Fprintln(w, "Hostname,IP Address,OS,Version,Kernel,Architecture,Serial Number,Product,BIOS Version,TPM,Secure Boot,Virtual,Docker,Collected At")
	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		get := func(path string) string {
			v := inventoryTraverse(traits, path)
			if v == "-" {
				return ""
			}
			return v
		}
		fmt.Fprintf(w, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			csvEscape(t.Hostname),
			csvEscape(t.IPAddress),
			csvEscape(get("os.distro")),
			csvEscape(get("os.version")),
			csvEscape(get("kernel.version")),
			csvEscape(get("os.arch")),
			csvEscape(get("dmi.product_vendor")),
			csvEscape(get("dmi.product_name")),
			csvEscape(get("dmi.bios_version")),
			csvEscape(get("tpm.version")),
			csvEscape(get("secureboot.enabled")),
			csvEscape(get("virtual.system")),
			csvEscape(get("docker.version")),
			csvEscape(t.CollectedAt.Format(time.RFC3339)),
		)
	}
}

// csvEscape wraps a value in quotes if it contains commas, quotes, or newlines.
func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// Approvals renders the config approvals page.
func (h *Handler) Approvals(w http.ResponseWriter, r *http.Request) {
	data := PageData{Title: "Config Approvals", ActiveNav: "approvals"}
	h.setCommonWithRequest(&data, r)
	h.renderer.Render(w, "approvals", data)
}

// Health renders the system health diagnostics page.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	data := PageData{Title: "System Health", ActiveNav: "health"}
	h.setCommonWithRequest(&data, r)
	h.renderer.Render(w, "health", data)
}

// SetTraitLookup sets the trait provider for envoy health indicators.
func (h *Handler) SetTraitLookup(tl TraitLookup) {
	h.traitLookup = tl
}

// SetFleetCache attaches the precomputed fleet status cache.
// Call after NewHandler — required for efficient dashboard/envoys rendering.
func (h *Handler) SetFleetCache(fc *FleetCache) {
	h.fleetCache = fc
}

// enrichEnvoyRows populates disk and repo health fields from traits.
func (h *Handler) enrichEnvoyRows(rows []EnvoyRow) {
	if h.traitLookup == nil {
		return
	}
	for i := range rows {
		// Agent version from traits (always available, even if DB column is empty).
		if rows[i].AgentVersion == "" {
			if v := h.traitLookup.EnvoyTrait(rows[i].ID, "identity.agent_version"); v != "" {
				rows[i].AgentVersion = v
			}
		}
		// Root filesystem available.
		if pct := h.traitLookup.EnvoyTrait(rows[i].ID, "filesystem./.percent"); pct != "" {
			if v, err := strconv.ParseFloat(pct, 64); err == nil {
				avail := 100 - int(v)
				if avail < 0 {
					avail = 0
				}
				rows[i].DiskPercent = avail
				switch {
				case avail <= 10:
					rows[i].DiskStatus = "critical"
				case avail <= 20:
					rows[i].DiskStatus = "warn"
				default:
					rows[i].DiskStatus = "ok"
				}
			}
		}
		// Package repo health. broken_repo_count is only present when > 0.
		// If package_updates.count exists, the collector has run.
		if cnt := h.traitLookup.EnvoyTrait(rows[i].ID, "package_updates.broken_repo_count"); cnt != "" {
			if v, err := strconv.Atoi(cnt); err == nil {
				rows[i].BrokenRepos = v
				rows[i].RepoStatus = "degraded"
			}
		} else if h.traitLookup.EnvoyTrait(rows[i].ID, "package_updates.count") != "" {
			// Collector ran but no broken repos → healthy.
			rows[i].RepoStatus = "ok"
		}
	}
}

// statusBadgeCSS converts a status string to a CSS class suffix.
func statusBadgeCSS(status string) string {
	switch strings.ToLower(status) {
	case "converged":
		return "success"
	case "relapsed":
		return "warning"
	case "diverged":
		return "danger"
	case "failed", "offline":
		return "danger"
	case "no data":
		return "info"
	default:
		return "info"
	}
}

// InventoryFragment returns the fleet inventory table via HTMX, with trait
// data, filtering (?where=key=value), and column projection (?show=fields).
func (h *Handler) InventoryFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Cursor-based pagination: fetch 200 envoys at a time instead of the full fleet.
	const pageSize = 200
	cursor := r.URL.Query().Get("after")

	all, err := h.queries.GetAllLatestTraitsPaged(r.Context(), cursor, int64(pageSize+1))
	if err != nil {
		fmt.Fprintf(w, `<p class="text-muted">Error loading inventory: %s</p>`, template.HTMLEscapeString(err.Error()))
		return
	}
	if len(all) == 0 && cursor == "" {
		fmt.Fprintf(w, `<p class="text-muted" style="padding:20px;">No envoys with trait data. Traits are collected after the first check-in.</p>`)
		return
	}

	hasMore := len(all) > pageSize
	if hasMore {
		all = all[:pageSize]
	}

	// Parse where filters.
	whereParam := r.URL.Query().Get("where")
	var filters []struct{ path, value string }
	if whereParam != "" {
		for _, clause := range strings.Split(whereParam, ",") {
			clause = strings.TrimSpace(clause)
			if idx := strings.IndexByte(clause, '='); idx > 0 {
				filters = append(filters, struct{ path, value string }{clause[:idx], clause[idx+1:]})
			}
		}
	}

	// Parse show columns.
	showParam := r.URL.Query().Get("show")
	var showFields []string
	if showParam != "" {
		for _, f := range strings.Split(showParam, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				showFields = append(showFields, f)
			}
		}
	}

	// Default columns when none specified.
	if len(showFields) == 0 {
		showFields = []string{"os.distro", "os.version", "os.arch", "virtual.system", "uptime.days"}
	}

	type invRow struct {
		ID          string
		Hostname    string
		IPAddress   string
		CollectedAt string
		Fields      []string // values for each showField
	}

	var rows []invRow
	for _, t := range all {
		var traits map[string]any
		if err := json.Unmarshal([]byte(t.Traits), &traits); err != nil {
			continue
		}

		// Apply where filters (AND logic).
		match := true
		for _, f := range filters {
			val := inventoryTraverse(traits, f.path)
			if val != f.value {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		row := invRow{
			ID:          t.ID,
			Hostname:    t.Hostname,
			IPAddress:   t.IPAddress,
			CollectedAt: t.CollectedAt.Local().Format("2006-01-02 15:04"),
		}
		for _, field := range showFields {
			row.Fields = append(row.Fields, inventoryTraverse(traits, field))
		}
		rows = append(rows, row)
	}

	// Render count.
	if cursor == "" {
		fmt.Fprintf(w, `<p class="text-muted" style="margin-bottom:8px;">%d envoys`, len(rows))
		if hasMore {
			fmt.Fprintf(w, `+`)
		}
		if len(filters) > 0 {
			fmt.Fprintf(w, ` (filtered)`)
		}
		fmt.Fprintf(w, `</p>`)
	}

	if len(rows) == 0 && cursor == "" {
		fmt.Fprintf(w, `<p class="text-muted" style="padding:20px;">No envoys match the filter criteria.</p>`)
		return
	}

	// Render table (only include wrapper on first page).
	if cursor == "" {
		fmt.Fprintf(w, `<table class="table"><thead><tr><th>Hostname</th><th>IP</th>`)
		for _, f := range showFields {
			fmt.Fprintf(w, `<th>%s</th>`, template.HTMLEscapeString(f))
		}
		fmt.Fprintf(w, `<th>Collected</th></tr></thead><tbody id="inventory-tbody">`)
	}
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td><a href="/inventory/%s">%s</a></td><td class="mono">%s</td>`,
			template.HTMLEscapeString(row.ID),
			template.HTMLEscapeString(row.Hostname),
			template.HTMLEscapeString(row.IPAddress))
		for _, val := range row.Fields {
			fmt.Fprintf(w, `<td>%s</td>`, template.HTMLEscapeString(val))
		}
		fmt.Fprintf(w, `<td class="text-muted">%s</td></tr>`, row.CollectedAt)
	}
	if cursor == "" {
		fmt.Fprintf(w, `</tbody></table>`)
	}

	// Infinite scroll trigger for next page.
	if hasMore && len(all) > 0 {
		nextCursor := all[len(all)-1].Hostname
		params := fmt.Sprintf("after=%s", url.QueryEscape(nextCursor))
		if whereParam != "" {
			params += "&where=" + url.QueryEscape(whereParam)
		}
		if showParam != "" {
			params += "&show=" + url.QueryEscape(showParam)
		}
		fmt.Fprintf(w,
			`<tr hx-get="/ui/fragments/inventory?%s" hx-trigger="revealed" hx-swap="afterend" hx-target="#inventory-tbody" hx-select="tr" style="height:1px;"><td colspan="%d"></td></tr>`,
			params, len(showFields)+3)
	}
}

// InventoryGroupFragment returns a detail table for a specific group value,
// showing all matching envoys with relevant trait columns for that group type.
// InventoryTraitIndexFragment returns a grouped list of all trait namespaces
// and their keys so operators can browse available traits.
func (h *Handler) InventoryTraitIndexFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		return
	}

	// Collect all unique keys with counts.
	keyCounts := map[string]int{}
	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)
		for k := range flat {
			keyCounts[k]++
		}
	}

	// Group by namespace (prefix before first dot).
	type keyInfo struct {
		Key   string
		Count int
	}
	namespaces := map[string][]keyInfo{}
	for k, c := range keyCounts {
		ns := k
		if idx := strings.IndexByte(k, '.'); idx >= 0 {
			ns = k[:idx]
		}
		namespaces[ns] = append(namespaces[ns], keyInfo{Key: k, Count: c})
	}

	// Sort namespace names and keys within each.
	var nsNames []string
	for ns := range namespaces {
		nsNames = append(nsNames, ns)
	}
	sort.Strings(nsNames)
	for _, ns := range nsNames {
		sort.Slice(namespaces[ns], func(i, j int) bool {
			return namespaces[ns][i].Key < namespaces[ns][j].Key
		})
	}

	closeModal := `document.getElementById('trait-index-modal').innerHTML='';`

	// Modal overlay.
	fmt.Fprint(w, `<div style="position:fixed; inset:0; background:rgba(0,0,0,0.5); z-index:1000; display:flex; align-items:center; justify-content:center;" onclick="if(event.target===this){`+closeModal+`}">`)
	fmt.Fprint(w, `<div style="background:var(--color-card-bg); border-radius:10px; width:90%; max-width:640px; max-height:80vh; display:flex; flex-direction:column; box-shadow:0 8px 32px rgba(0,0,0,0.3);">`)

	// Header.
	fmt.Fprintf(w, `<div style="display:flex; justify-content:space-between; align-items:center; padding:16px 20px; border-bottom:1px solid var(--color-border);">`)
	fmt.Fprintf(w, `<strong style="font-size:16px;">Trait Index — %d traits across %d namespaces</strong>`, len(keyCounts), len(nsNames))
	fmt.Fprintf(w, `<button class="btn btn--sm" onclick="%s">Close</button>`, closeModal)
	fmt.Fprint(w, `</div>`)

	// Search field.
	fmt.Fprint(w, `<div style="padding:12px 20px; border-bottom:1px solid var(--color-border);">`)
	fmt.Fprint(w, `<input id="trait-index-search" type="text" placeholder="Filter traits..." `+
		`style="width:100%; padding:6px 10px; border:1px solid var(--color-border); border-radius:6px; background:var(--color-bg); color:var(--color-text); font-size:14px; outline:none;" `+
		`oninput="(function(q){`+
		`var rows=document.querySelectorAll('#trait-index-body [data-trait-key]');`+
		`var hdrs=document.querySelectorAll('#trait-index-body [data-ns-header]');`+
		`q=q.toLowerCase();`+
		`rows.forEach(function(r){r.style.display=(!q||r.getAttribute('data-trait-key').toLowerCase().indexOf(q)!==-1)?'':'none';});`+
		`hdrs.forEach(function(h){`+
		`var ns=h.getAttribute('data-ns-header');`+
		`var any=false;`+
		`rows.forEach(function(r){if(r.getAttribute('data-ns')===ns&&r.style.display!=='none')any=true;});`+
		`h.style.display=any||!q?'':'none';`+
		`});`+
		`})(this.value)">`)
	fmt.Fprint(w, `</div>`)

	// Scrollable body.
	fmt.Fprint(w, `<div id="trait-index-body" style="overflow-y:auto; flex:1;">`)
	for _, ns := range nsNames {
		keys := namespaces[ns]
		escapedNS := template.HTMLEscapeString(ns)
		fmt.Fprintf(w, `<div data-ns-header="%s" style="padding:6px 20px; background:var(--color-bg); font-weight:600; font-size:12px; text-transform:uppercase; color:var(--color-text-muted); border-bottom:1px solid var(--color-border); position:sticky; top:0;">%s (%d)</div>`,
			escapedNS, escapedNS, len(keys))
		for _, ki := range keys {
			escapedKey := template.HTMLEscapeString(ki.Key)
			fmt.Fprintf(w, `<div class="trait-search__hit" data-trait-key="%s" data-ns="%s" style="cursor:pointer; padding:4px 20px;" `+
				`hx-get="/ui/fragments/inventory-explore?key=%s" `+
				`hx-target="#trait-explore-panel" hx-swap="innerHTML">`+
				`<span class="trait-search__key">%s</span>`+
				`<span class="trait-search__count">%d envoy%s</span>`+
				`</div>`,
				escapedKey,
				escapedNS,
				url.QueryEscape(ki.Key),
				escapedKey,
				ki.Count,
				func() string {
					if ki.Count != 1 {
						return "s"
					}
					return ""
				}())
		}
	}
	fmt.Fprint(w, `</div>`) // scrollable body
	fmt.Fprint(w, `</div>`) // modal card
	fmt.Fprint(w, `</div>`) // overlay
	fmt.Fprint(w, `<script>document.getElementById('trait-index-search').focus();</script>`)
}

// InventorySearchFragment returns trait key/value matches as HTML for the
// typeahead search on the inventory page.
func (h *Handler) InventorySearchFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		return // don't search until at least 2 characters
	}
	qLower := strings.ToLower(q)

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		return
	}

	// Aggregate keys and sample values from all envoys.
	type traitHit struct {
		Key    string
		Sample string
		Count  int
	}
	keyValues := map[string]string{}  // key → first non-empty sample value
	keyCounts := map[string]int{}     // key → number of envoys reporting it
	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)
		for k, v := range flat {
			keyCounts[k]++
			if _, exists := keyValues[k]; !exists {
				s := fmt.Sprint(v)
				if s != "" && s != "<nil>" {
					keyValues[k] = s
				}
			}
		}
	}

	// Match against query.
	var hits []traitHit
	for k, sample := range keyValues {
		if strings.Contains(strings.ToLower(k), qLower) ||
			strings.Contains(strings.ToLower(sample), qLower) {
			hits = append(hits, traitHit{Key: k, Sample: sample, Count: keyCounts[k]})
		}
	}

	// Sort: key matches first, then by key name.
	sort.Slice(hits, func(i, j int) bool {
		iKey := strings.Contains(strings.ToLower(hits[i].Key), qLower)
		jKey := strings.Contains(strings.ToLower(hits[j].Key), qLower)
		if iKey != jKey {
			return iKey
		}
		return hits[i].Key < hits[j].Key
	})

	// Cap results.
	if len(hits) > 30 {
		hits = hits[:30]
	}

	if len(hits) == 0 {
		fmt.Fprintf(w, `<div class="trait-search__empty">No traits matching "%s"</div>`,
			template.HTMLEscapeString(q))
		return
	}

	fmt.Fprint(w, `<div class="trait-search__results">`)
	for _, hit := range hits {
		sample := hit.Sample
		if len(sample) > 60 {
			sample = sample[:60] + "..."
		}
		escapedKey := template.HTMLEscapeString(hit.Key)
		fmt.Fprintf(w, `<div class="trait-search__hit" style="cursor:pointer;" `+
			`hx-get="/ui/fragments/inventory-explore?key=%s" `+
			`hx-target="#trait-explore-panel" hx-swap="innerHTML">`+
			`<span class="trait-search__key">%s</span>`+
			`<span class="trait-search__sample">%s</span>`+
			`<span class="trait-search__count">%d envoy%s</span>`+
			`</div>`,
			url.QueryEscape(hit.Key),
			escapedKey,
			template.HTMLEscapeString(sample),
			hit.Count,
			func() string {
				if hit.Count != 1 {
					return "s"
				}
				return ""
			}())
	}
	fmt.Fprint(w, `</div>`)
}

// InventoryExploreFragment renders sibling trait checkboxes + a grouped table
// when the user clicks a trait from the search results.
func (h *Handler) InventoryExploreFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	key := r.URL.Query().Get("key")
	if key == "" {
		return
	}

	// Determine namespace (prefix before first dot).
	namespace := key
	if idx := strings.IndexByte(key, '.'); idx >= 0 {
		namespace = key[:idx]
	}

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<p>Error: %s</p>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Collect all sibling keys in this namespace.
	siblingSet := map[string]bool{}
	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)
		for k := range flat {
			if strings.HasPrefix(k, namespace+".") {
				siblingSet[k] = true
			}
		}
	}

	var siblings []string
	for k := range siblingSet {
		siblings = append(siblings, k)
	}
	sort.Strings(siblings)

	// Render: header, checkboxes, and initial table.
	escapedKey := template.HTMLEscapeString(key)
	escapedNS := template.HTMLEscapeString(namespace)

	fmt.Fprintf(w, `<div class="trait-explore" style="margin-top:12px;">`)
	fmt.Fprintf(w, `<div style="display:flex; align-items:center; justify-content:space-between; margin-bottom:8px;">`)
	fmt.Fprintf(w, `<h3 style="margin:0;">Exploring: <code>%s</code></h3>`, escapedKey)
	fmt.Fprintf(w, `<button type="submit" form="trait-explore-form" formaction="/ui/export/inventory-explore.ods" class="btn btn--sm btn--primary" style="margin-right:6px;" hx-disable>Export ODS</button>`)
	fmt.Fprintf(w, `<button class="btn btn--sm" onclick="document.getElementById('trait-explore-panel').innerHTML='';document.getElementById('trait-search-results').innerHTML='';">Close</button>`)
	fmt.Fprintf(w, `</div>`)
	fmt.Fprintf(w, `<p class="text-muted" style="font-size:12px; margin-bottom:8px;">Group by <strong>%s</strong>. Select columns from the <code>%s</code> namespace:</p>`, escapedKey, escapedNS)

	// Checkboxes form — triggers table reload on change.
	fmt.Fprintf(w, `<form id="trait-explore-form" method="get" style="display:flex; flex-wrap:wrap; gap:6px 16px; margin-bottom:12px;">`)
	fmt.Fprintf(w, `<input type="hidden" name="key" value="%s">`, template.HTMLEscapeString(key))
	for _, sib := range siblings {
		checked := ""
		if sib == key || sib != key {
			// Pre-check all siblings by default.
			checked = " checked"
		}
		shortName := sib
		if strings.HasPrefix(sib, namespace+".") {
			shortName = sib[len(namespace)+1:]
		}
		fmt.Fprintf(w, `<label style="font-size:12px; display:flex; align-items:center; gap:3px; cursor:pointer;">`+
			`<input type="checkbox" name="col" value="%s"%s `+
			`hx-get="/ui/fragments/inventory-explore-table" `+
			`hx-include="#trait-explore-form" `+
			`hx-target="#trait-explore-table" `+
			`hx-swap="innerHTML" `+
			`hx-trigger="change">`+
			`<code>%s</code></label>`,
			template.HTMLEscapeString(sib), checked,
			template.HTMLEscapeString(shortName))
	}
	fmt.Fprintf(w, `</form>`)

	// Table placeholder — loaded immediately.
	fmt.Fprintf(w, `<div id="trait-explore-table" `+
		`hx-get="/ui/fragments/inventory-explore-table" `+
		`hx-include="#trait-explore-form" `+
		`hx-trigger="load" `+
		`hx-swap="innerHTML">`+
		`<p class="text-muted">Loading...</p></div>`)
	fmt.Fprintf(w, `</div>`)
}

// InventoryExploreTableFragment renders the grouped results table for the trait
// explore panel. Groups envoys by the selected key and shows checked columns.
func (h *Handler) InventoryExploreTableFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	key := r.URL.Query().Get("key")
	cols := r.URL.Query()["col"]
	if key == "" {
		return
	}

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<p>Error: %s</p>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Namespace for short column headers.
	namespace := key
	if idx := strings.IndexByte(key, '.'); idx >= 0 {
		namespace = key[:idx]
	}

	// Group envoys by the key value.
	type envoyRow struct {
		ID       string
		Hostname string
		Fields   []string
	}
	type groupEntry struct {
		Label string
		Rows  []envoyRow
	}
	groupMap := map[string]*groupEntry{}
	var groupOrder []string

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)

		val := "-"
		if v, ok := flat[key]; ok {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				val = s
			}
		}

		var fields []string
		for _, col := range cols {
			if v, ok := flat[col]; ok {
				s := fmt.Sprint(v)
				if s == "<nil>" || s == "" {
					s = "-"
				}
				fields = append(fields, s)
			} else {
				fields = append(fields, "-")
			}
		}

		if _, exists := groupMap[val]; !exists {
			groupMap[val] = &groupEntry{Label: val}
			groupOrder = append(groupOrder, val)
		}
		groupMap[val].Rows = append(groupMap[val].Rows, envoyRow{
			ID: t.ID, Hostname: t.Hostname, Fields: fields,
		})
	}

	sort.Strings(groupOrder)

	if len(groupOrder) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No envoys with this trait.</p>`)
		return
	}

	// Short column names (strip namespace prefix).
	shortCols := make([]string, len(cols))
	for i, c := range cols {
		if strings.HasPrefix(c, namespace+".") {
			shortCols[i] = c[len(namespace)+1:]
		} else {
			shortCols[i] = c
		}
	}

	// Render one table per group value.
	for _, label := range groupOrder {
		g := groupMap[label]
		fmt.Fprintf(w, `<div style="margin-bottom:16px;">`)
		fmt.Fprintf(w, `<h4 style="margin:0 0 4px;">%s <span class="text-muted" style="font-weight:400; font-size:12px;">(%d)</span></h4>`,
			template.HTMLEscapeString(label), len(g.Rows))
		if len(cols) > 0 {
			fmt.Fprint(w, `<table class="table"><thead><tr><th>Hostname</th>`)
			for _, sc := range shortCols {
				fmt.Fprintf(w, `<th>%s</th>`, template.HTMLEscapeString(sc))
			}
			fmt.Fprint(w, `</tr></thead><tbody>`)
			for _, row := range g.Rows {
				fmt.Fprintf(w, `<tr><td><a href="/inventory/%s">%s</a></td>`,
					template.HTMLEscapeString(row.ID),
					template.HTMLEscapeString(row.Hostname))
				for _, f := range row.Fields {
					fmt.Fprintf(w, `<td class="mono" style="font-size:12px;">%s</td>`,
						template.HTMLEscapeString(f))
				}
				fmt.Fprint(w, `</tr>`)
			}
			fmt.Fprint(w, `</tbody></table>`)
		} else {
			// No columns selected — just show hostnames.
			fmt.Fprint(w, `<div style="font-size:13px; color:var(--color-muted);">`)
			for i, row := range g.Rows {
				if i > 0 {
					fmt.Fprint(w, `, `)
				}
				fmt.Fprintf(w, `<a href="/inventory/%s">%s</a>`,
					template.HTMLEscapeString(row.ID),
					template.HTMLEscapeString(row.Hostname))
			}
			fmt.Fprint(w, `</div>`)
		}
		fmt.Fprint(w, `</div>`)
	}
}

// InventoryExploreODSExport generates an ODS spreadsheet from the current
// trait explore results (grouping key + selected columns).
func (h *Handler) InventoryExploreODSExport(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	cols := r.URL.Query()["col"]
	if key == "" {
		http.Error(w, "missing key parameter", http.StatusBadRequest)
		return
	}

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	namespace := key
	if idx := strings.IndexByte(key, '.'); idx >= 0 {
		namespace = key[:idx]
	}

	// Short column names.
	shortCols := make([]string, len(cols))
	for i, c := range cols {
		if strings.HasPrefix(c, namespace+".") {
			shortCols[i] = c[len(namespace)+1:]
		} else {
			shortCols[i] = c
		}
	}

	// Build rows: hostname, group value, column values.
	type odsRow struct {
		Hostname   string
		GroupValue string
		Fields     []string
	}
	var rows []odsRow

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)

		groupVal := "-"
		if v, ok := flat[key]; ok {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				groupVal = s
			}
		}

		var fields []string
		for _, col := range cols {
			if v, ok := flat[col]; ok {
				s := fmt.Sprint(v)
				if s == "<nil>" || s == "" {
					s = ""
				}
				fields = append(fields, s)
			} else {
				fields = append(fields, "")
			}
		}

		rows = append(rows, odsRow{Hostname: t.Hostname, GroupValue: groupVal, Fields: fields})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].GroupValue != rows[j].GroupValue {
			return rows[i].GroupValue < rows[j].GroupValue
		}
		return rows[i].Hostname < rows[j].Hostname
	})

	// Generate ODS (ZIP with XML content).
	w.Header().Set("Content-Type", "application/vnd.oasis.opendocument.spreadsheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="inventory-%s.ods"`, namespace))

	zw := zip.NewWriter(w)
	defer zw.Close()

	// mimetype must be first entry, stored uncompressed.
	mh := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mf, _ := zw.CreateHeader(mh)
	mf.Write([]byte("application/vnd.oasis.opendocument.spreadsheet"))

	// META-INF/manifest.xml
	manf, _ := zw.Create("META-INF/manifest.xml")
	manf.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.2">
 <manifest:file-entry manifest:full-path="/" manifest:version="1.2" manifest:media-type="application/vnd.oasis.opendocument.spreadsheet"/>
 <manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>
</manifest:manifest>`))

	// content.xml
	cf, _ := zw.Create("content.xml")
	var buf strings.Builder
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 office:version="1.2">
<office:body><office:spreadsheet>
<table:table table:name="Inventory">`)

	// Header row.
	buf.WriteString(`<table:table-row>`)
	odsCell := func(s string) {
		escaped := strings.ReplaceAll(s, "&", "&amp;")
		escaped = strings.ReplaceAll(escaped, "<", "&lt;")
		escaped = strings.ReplaceAll(escaped, ">", "&gt;")
		escaped = strings.ReplaceAll(escaped, "\"", "&quot;")
		buf.WriteString(`<table:table-cell office:value-type="string"><text:p>`)
		buf.WriteString(escaped)
		buf.WriteString(`</text:p></table:table-cell>`)
	}
	odsCell("Hostname")

	// Short key name for the group column header.
	groupHeader := key
	if strings.HasPrefix(key, namespace+".") {
		groupHeader = key[len(namespace)+1:]
	}
	odsCell(groupHeader)

	for _, sc := range shortCols {
		odsCell(sc)
	}
	buf.WriteString(`</table:table-row>`)

	// Data rows.
	for _, row := range rows {
		buf.WriteString(`<table:table-row>`)
		odsCell(row.Hostname)
		odsCell(row.GroupValue)
		for _, f := range row.Fields {
			odsCell(f)
		}
		buf.WriteString(`</table:table-row>`)
	}

	buf.WriteString(`</table:table></office:spreadsheet></office:body></office:document-content>`)
	cf.Write([]byte(buf.String()))
}

func (h *Handler) InventoryGroupFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	groupBy := r.URL.Query().Get("group")
	label := r.URL.Query().Get("label")
	if groupBy == "" || label == "" {
		return
	}

	all, err := h.queries.GetAllLatestTraits(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<p>Error: %s</p>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Determine which detail columns to show based on group type.
	type detailCol struct {
		Header string
		Path   string // dot-path into raw traits, or empty for special handling
	}
	var cols []detailCol
	switch groupBy {
	case "disk":
		cols = []detailCol{
			{"Device", ""},     // special: from blockdevices
			{"Model", ""},
			{"Firmware", ""},
			{"Size", ""},
		}
	case "gpu":
		cols = []detailCol{
			{"Name", ""},      // special: from gpu.devices
			{"Driver", ""},
			{"Firmware", ""},
			{"Vendor", ""},
		}
	case "cpu":
		cols = []detailCol{
			{"Model", "processors.model"},
			{"Cores", "processors.physical_cores"},
			{"Threads", "processors.logical_cores"},
			{"Microcode", "processors.microcode"},
			{"MHz", "processors.mhz"},
		}
	case "product":
		cols = []detailCol{
			{"Model", "dmi.product_name"},
			{"Vendor", "dmi.product_vendor"},
			{"Board", "dmi.board_name"},
			{"BIOS", "dmi.bios_version"},
			{"BIOS Date", "dmi.bios_date"},
		}
	case "board":
		cols = []detailCol{
			{"Board", "dmi.board_name"},
			{"Board Vendor", "dmi.board_vendor"},
			{"Product", "dmi.product_name"},
			{"BIOS", "dmi.bios_version"},
		}
	case "tpm":
		cols = []detailCol{
			{"Version", "tpm.version"},
			{"Firmware", "tpm.firmware_version"},
			{"Manufacturer", "tpm.manufacturer"},
		}
	case "secureboot":
		cols = []detailCol{
			{"Mode", "secureboot.mode"},
			{"Firmware Bits", "secureboot.firmware_bits"},
			{"BIOS", "dmi.bios_version"},
		}
	case "bios":
		cols = []detailCol{
			{"BIOS Version", "dmi.bios_version"},
			{"BIOS Vendor", "dmi.bios_vendor"},
			{"BIOS Date", "dmi.bios_date"},
			{"Product", "dmi.product_name"},
		}
	default:
		// Generic: show OS, arch, kernel.
		cols = []detailCol{
			{"OS", "os.distro"},
			{"Version", "os.version"},
			{"Arch", "os.arch"},
			{"Kernel", "kernel.version"},
		}
	}

	// Build the table of matching envoys.
	type detailRow struct {
		ID       string
		Hostname string
		Fields   []string
	}
	var rows []detailRow

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}

		// Check if this envoy matches the group label.
		matched := false
		var specialFields []string

		switch groupBy {
		case "disk":
			if bd, ok := traits["blockdevices"].(map[string]any); ok {
				for devName, v := range bd {
					dev, ok := v.(map[string]any)
					if !ok {
						continue
					}
					model := strings.TrimSpace(fmt.Sprint(dev["model"]))
					if model == "<nil>" {
						model = ""
					}
					// Build the same label as inventoryAllBlockDevices.
					devLabel := model
					if sizeBytes, ok := dev["size_bytes"].(float64); ok && sizeBytes > 0 && model != "" {
						gb := sizeBytes / (1024 * 1024 * 1024)
						if gb >= 1024 {
							devLabel = fmt.Sprintf("%s (%.1f TB, %s)", model, gb/1024, devName)
						} else {
							devLabel = fmt.Sprintf("%s (%.0f GB, %s)", model, gb, devName)
						}
					}
					if devLabel == label {
						matched = true
						fw := fmt.Sprint(dev["firmware_rev"])
						if fw == "<nil>" {
							fw = "-"
						}
						size := "-"
						if sb, ok := dev["size_bytes"].(float64); ok && sb > 0 {
							gb := sb / (1024 * 1024 * 1024)
							if gb >= 1024 {
								size = fmt.Sprintf("%.1f TB", gb/1024)
							} else {
								size = fmt.Sprintf("%.0f GB", gb)
							}
						}
						specialFields = []string{devName, model, fw, size}
						break
					}
				}
			}
		case "gpu":
			if ns, ok := traits["gpu"].(map[string]any); ok {
				if arr, ok := ns["devices"].([]any); ok {
					for _, elem := range arr {
						m, ok := elem.(map[string]any)
						if !ok {
							continue
						}
						// Build GPU label the same way as inventoryAllGPUs.
						name, _ := m["name"].(string)
						vendor, _ := m["vendor"].(string)
						driver, _ := m["driver"].(string)
						device, _ := m["device"].(string)
						fw, _ := m["firmware_version"].(string)
						var gpuLabel string
						switch {
						case name != "":
							gpuLabel = name
							if vendor != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(vendor)) {
								gpuLabel = vendor + " " + name
							}
						case driver != "":
							gpuLabel = driver
							if device != "" {
								gpuLabel = driver + " (" + device + ")"
							}
						case device != "":
							gpuLabel = device
						}
						if gpuLabel == label {
							matched = true
							if fw == "" {
								fw = "-"
							}
							if vendor == "" {
								vendor = "-"
							}
							specialFields = []string{name, driver, fw, vendor}
							break
						}
					}
				}
			}
		default:
			// Generic matching via group field.
			groupField := ""
			switch groupBy {
			case "os":
				groupField = "os.distro"
			case "version":
				d := inventoryTraverse(traits, "os.distro")
				v := inventoryTraverse(traits, "os.version")
				check := strings.TrimSpace(d)
				if v != "-" && v != "" {
					check = strings.TrimSpace(check + " " + v)
				}
				if check == label || (check == "" && label == "(not reported)") {
					matched = true
				}
			case "arch":
				groupField = "os.arch"
			case "kernel":
				groupField = "kernel.version"
			case "virtual":
				groupField = "virtual.system"
			case "cpu":
				groupField = "processors.model"
			case "product":
				groupField = "dmi.product_name"
			case "board":
				groupField = "dmi.board_name"
			case "bios":
				groupField = "dmi.bios_version"
			case "tpm":
				groupField = "tpm.version"
			case "secureboot":
				groupField = "secureboot.enabled"
			}
			if groupField != "" {
				val := inventoryTraverse(traits, groupField)
				if val == label || ((val == "-" || val == "") && label == "(not reported)") {
					matched = true
				}
			}
		}

		if !matched {
			continue
		}

		row := detailRow{ID: t.ID, Hostname: t.Hostname}
		if len(specialFields) > 0 {
			row.Fields = specialFields
		} else {
			for _, col := range cols {
				if col.Path != "" {
					row.Fields = append(row.Fields, inventoryTraverse(traits, col.Path))
				} else {
					row.Fields = append(row.Fields, "-")
				}
			}
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		fmt.Fprintf(w, `<p class="text-muted" style="padding:8px;">No matching envoys found.</p>`)
		return
	}

	fmt.Fprintf(w, `<table class="table" style="margin:8px 0;"><thead><tr><th>Hostname</th>`)
	for _, col := range cols {
		fmt.Fprintf(w, `<th>%s</th>`, template.HTMLEscapeString(col.Header))
	}
	fmt.Fprintf(w, `</tr></thead><tbody>`)
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td><a href="/inventory/%s">%s</a></td>`,
			template.HTMLEscapeString(row.ID),
			template.HTMLEscapeString(row.Hostname))
		for _, val := range row.Fields {
			fmt.Fprintf(w, `<td class="mono" style="font-size:12px;">%s</td>`, template.HTMLEscapeString(val))
		}
		fmt.Fprintf(w, `</tr>`)
	}
	fmt.Fprintf(w, `</tbody></table>`)
}

// inventoryTraverse walks a nested map using a dot-separated path and returns
// the string value, or "-" if the path doesn't exist.
func inventoryTraverse(data map[string]any, path string) string {
	parts := strings.Split(path, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return "-"
		}
		current, ok = m[part]
		if !ok {
			return "-"
		}
	}
	return fmt.Sprint(current)
}

// inventoryAllArrayFields extracts a field from every element of a nested
// array: data[namespace][arrayKey][*][field]. Returns all non-empty values.
// inventoryAllGPUs builds a label for each GPU device from the best available
// fields: name (from lspci), vendor+driver, or device (card0) as last resort.
func inventoryAllGPUs(data map[string]any) []string {
	ns, ok := data["gpu"].(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := ns["devices"].([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var result []string
	for _, elem := range arr {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		vendor, _ := m["vendor"].(string)
		driver, _ := m["driver"].(string)
		device, _ := m["device"].(string)

		var label string
		switch {
		case name != "":
			label = name
			if vendor != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(vendor)) {
				label = vendor + " " + name
			}
		case driver != "":
			label = driver
			if device != "" {
				label = driver + " (" + device + ")"
			}
		case device != "":
			label = device
		default:
			continue
		}
		result = append(result, label)
	}
	return result
}

func inventoryAllArrayFields(data map[string]any, namespace, arrayKey, field string) []string {
	ns, ok := data[namespace].(map[string]any)
	if !ok {
		return nil
	}
	arr, ok := ns[arrayKey].([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var result []string
	for _, elem := range arr {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		if val, ok := m[field]; ok {
			s := fmt.Sprint(val)
			if s != "" {
				result = append(result, s)
			}
		}
	}
	return result
}

// inventoryAllBlockDevices returns the model (with size) for every block device
// that has a model field. Block devices are keyed by device name (sda, nvme0n1).
func inventoryAllBlockDevices(data map[string]any, namespace string) []string {
	ns, ok := data[namespace].(map[string]any)
	if !ok {
		return nil
	}
	var result []string
	for devName, v := range ns {
		dev, ok := v.(map[string]any)
		if !ok {
			continue
		}
		model, ok := dev["model"]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(model))
		if s == "" {
			continue
		}
		// Append size if available for richer grouping.
		if sizeBytes, ok := dev["size_bytes"].(float64); ok && sizeBytes > 0 {
			gb := sizeBytes / (1024 * 1024 * 1024)
			if gb >= 1024 {
				s = fmt.Sprintf("%s (%.1f TB, %s)", s, gb/1024, devName)
			} else {
				s = fmt.Sprintf("%s (%.0f GB, %s)", s, gb, devName)
			}
		}
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// ApprovalsFragment returns the pending config changes table via HTMX.
func (h *Handler) ApprovalsFragment(w http.ResponseWriter, r *http.Request) {
	changes, err := h.queries.ListPendingConfigChanges(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		fmt.Fprintf(w, `<p>Error loading changes: %s</p>`, template.HTMLEscapeString(err.Error()))
		return
	}
	if len(changes) == 0 {
		fmt.Fprintf(w, `<p>No pending config changes.</p>`)
		return
	}
	fmt.Fprintf(w, `<table class="table"><thead><tr><th>ID</th><th>Status</th><th>Requested By</th><th>Requested At</th><th>Summary</th></tr></thead><tbody>`)
	for _, c := range changes {
		statusCSS := "info"
		switch c.Status {
		case "approved":
			statusCSS = "success"
		case "rejected":
			statusCSS = "danger"
		case "pending":
			statusCSS = "warning"
		}
		fmt.Fprintf(w, `<tr><td>%d</td><td><span class="badge badge--%s">%s</span></td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			c.ID, statusCSS, template.HTMLEscapeString(c.Status),
			template.HTMLEscapeString(c.RequestedBy),
			c.RequestedAt.Local().Format("2006-01-02 15:04"),
			template.HTMLEscapeString(c.DiffSummary))
	}
	fmt.Fprintf(w, `</tbody></table>`)
}

// HealthFragment returns the system health diagnostics via HTMX.
func (h *Handler) HealthFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.healthChecker == nil {
		fmt.Fprintf(w, `<p>Health checks not available.</p>`)
		return
	}
	status, checks := h.healthChecker.RunChecks()
	passCount := 0
	for _, c := range checks {
		if c.Status == "pass" {
			passCount++
		}
	}
	statusCSS := "success"
	if status == "degraded" {
		statusCSS = "warning"
	} else if status == "unhealthy" {
		statusCSS = "danger"
	}
	fmt.Fprintf(w, `<div class="stat-grid">`)
	fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Overall Status</div><div class="stat-card__value text-%s">%s</div></div>`, statusCSS, template.HTMLEscapeString(status))
	fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Checks Passed</div><div class="stat-card__value">%d / %d</div></div>`, passCount, len(checks))
	fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Version</div><div class="stat-card__value" style="font-size:1rem">%s</div></div>`, template.HTMLEscapeString(h.version))
	fmt.Fprintf(w, `</div>`)
	fmt.Fprintf(w, `<div class="card mt-3"><h2>Diagnostic Checks</h2><table class="table"><thead><tr><th>Check</th><th>Status</th><th>Details</th></tr></thead><tbody>`)
	for _, c := range checks {
		checkCSS := "info"
		switch c.Status {
		case "pass":
			checkCSS = "success"
		case "warn":
			checkCSS = "warning"
		case "fail":
			checkCSS = "danger"
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td><span class="badge badge--%s">%s</span></td><td>%s</td></tr>`,
			template.HTMLEscapeString(c.Name), checkCSS, template.HTMLEscapeString(c.Status), template.HTMLEscapeString(c.Message))
	}
	fmt.Fprintf(w, `</tbody></table></div>`)
}
