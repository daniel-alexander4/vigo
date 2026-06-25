// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/compliance"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/cve"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleetpressure"
	fwreg "git.alexander4.io/vigo/server/frameworks"
	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/swarm/gitbackmesh"
	"git.alexander4.io/vigo/server/swarm/lockboxmesh"
	"git.alexander4.io/vigo/server/swarm/longdrawermesh"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
	"git.alexander4.io/vigo/server/traits"
	"github.com/google/uuid"
)

// titleCaser title-cases ASCII display strings (section names, hardware
// type labels). Replacement for the deprecated strings.Title.
var titleCaser = cases.Title(language.Und)

// DataSource abstracts data fetching for UI handlers.
// Implemented by fleet.DataSource (production) and mockDataSource (test).
type DataSource interface {
	DashboardData(baseInterval, offlineThreshold time.Duration) DashboardPageData
	ConvergenceData(baseInterval, offlineThreshold time.Duration) ConvergencePageData
	DashboardStatsData(baseInterval, offlineThreshold time.Duration) DashboardStatsFragmentData
	EnvoysData(baseInterval, offlineThreshold time.Duration, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter, sortCol, sortDir string, offset, limit int) EnvoysPageData
	EnvoyDetailData(envoyID string, baseInterval, offlineThreshold time.Duration) (EnvoyDetailPageData, error)
	EnvoyRunsData(envoyID string) (EnvoyRunsFragmentData, error)
	EnvoyTraitsData(envoyID string) (EnvoyTraitsFragmentData, error)
	RunsData(statusFilter string) RunsPageData
	RunDetailData(runID string) (RunDetailPageData, error)
	// EnvoyDriftLabel returns the envoy's current drift-axis label
	// ("none" / "changed" / "diverged") or "" if the envoy isn't in the
	// FleetIndex. Used by the dashboard recent-runs widget builder to
	// stamp RunRow.EnvoyDriftLabel without copying the full envoy state.
	EnvoyDriftLabel(envoyID string) string
	ConfigData(hostname string) (ConfigPageData, error)
	PlanDetailData(planID string) (PlanDetailPageData, error)
	SecurityScanData() SecurityScanPageData
	SecurityScanTraits(envoyID string) (map[string]string, bool)
	// SecurityScanContext returns the hostname alongside cached security_scan
	// traits for a single envoy. Used by the /ui/fragments/security-envoy/{id}
	// drawer handler which needs both to dispatch a live-detail query to the
	// agent and to render operator-facing header chrome.
	SecurityScanContext(envoyID string) (hostname string, traits map[string]string, ok bool)
	RiskData(baseInterval, offlineThreshold time.Duration) RiskSummaryData
	RiskPostureData(baseInterval, offlineThreshold time.Duration) RiskPosturePageData
	CertExpirySummary() CertExpirySummary
	// EnvoyCertDetails returns the expiring-or-expired certificates
	// for a single envoy, sorted most-urgent first. Filter narrows to a
	// specific urgency bucket: "expired" (days<0), "7d" (0..6), "30d"
	// (7..29), or "" for all three combined. Empty when the envoy has
	// no cert_lifecycle trait rows matching the filter.
	EnvoyCertDetails(envoyID, bucket string) ([]CertDetail, string, bool)
	RunningCVESummary() RunningCVESummary
	// FleetPressure rolls up per-envoy resource + repo-health telemetry
	// into the dashboard "Agent Health" card via the fleetpressure SoT
	// package.
	FleetPressure() fleetpressure.FleetSummary
	// FleetPressureDetail returns the same rollup plus every per-envoy
	// projection in one walk — feeds the `/agent-health` fleet table.
	FleetPressureDetail() (fleetpressure.FleetSummary, []fleetpressure.EnvoyPressure)
	// EnvoyPressure returns the per-envoy projection used by the
	// envoy-detail "Agent Health" tab. Bool is false when the envoy
	// is unknown to the fleet index.
	EnvoyPressure(envoyID string) (fleetpressure.EnvoyPressure, bool)
	// EnvoyFreshness returns the envoy's last-check-in timestamp and
	// smoothed observed cadence — feeds responsiveness checks (e.g.
	// the security drawer's pre-flight banner before a live dispatch).
	// `ok=false` when the envoy is unknown.
	EnvoyFreshness(envoyID string) (lastSeen time.Time, observedCycle time.Duration, ok bool)
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

// CertDetail is one certificate row shown in the /security page cert-
// expiry expansion. Assembled by DataSource.EnvoyCertDetails from the
// envoy's `cert_lifecycle.certificates.*` traits.
type CertDetail struct {
	Path          string   // absolute path on disk
	CN            string   // common name (may be empty)
	SANs          []string // subject alternative names
	Issuer        string   // issuer distinguished name
	NotAfter      string   // RFC3339 end-of-validity
	DaysRemaining int      // negative when already expired
	IsCA          bool
}

// ReportBuilder builds compliance reports for HTMX fragment rendering.
// Set via SetReportBuilder after construction to avoid import cycles.
type ReportBuilder struct {
	// Executive returns the executive summary report data (any is *api.ExecutiveReport).
	Executive func(r *http.Request) (any, error)
	// Framework returns a per-framework report (any is *api.ComplianceReport).
	Framework func(r *http.Request, slug string) (any, error)
	// EnvoyFrameworkStatus returns per-envoy compliance status for one
	// framework, using the unified scorer's partition. Single source of
	// truth so the envoy-detail Compliance tab and the /compliance/<slug>
	// fleet row render identical numbers for the same envoy. Returns
	// ok=false when slug is unknown.
	EnvoyFrameworkStatus func(r *http.Request, hostname, slug string) (EnvoyFrameworkStatus, bool)
	// EnvoyFrameworkDetail returns the per-envoy partition detail for one
	// framework: Status totals plus a per-control breakdown. Powers the
	// /envoys/<id>/compliance/<slug> drilldown, which must match the
	// Standards tab card that linked to it.
	EnvoyFrameworkDetail func(r *http.Request, hostname, slug string) (*EnvoyFrameworkDetail, bool)
	// AuditStatus returns the cached audit-chain verification status (the
	// report layer's serve-stale-while-revalidate value), so the ambient
	// audit badges share one SoT instead of each running an O(N) full
	// chain walk on load. nil when no report builder is wired (tests) —
	// callers fall back to a direct VerifyFull.
	AuditStatus func(r *http.Request) AuditChainStatus
}

// AuditChainStatus mirrors the report layer's cached audit-chain status
// for the ambient UI badges. Same fields the badges render; populated from
// api.ReportHandler.AuditStatus via the wiring closure.
type AuditChainStatus struct {
	Valid       bool
	EntryCount  int64
	FirstBroken int64
	Error       string
}

// EnvoyFrameworkStatus is one envoy's bucketed per-framework totals.
// Partition rule: a finding is counted once, in this priority order —
// module-tagged coverage, then Connwaer-trait escalation (Status =
// "satisfied" set by a fleet-trait check on the envoy itself), then
// waiver, then partial, else missing. Vigo's own architectural posture
// is surfaced separately via /platform/compliance and never inflates
// these per-envoy buckets. Covered + Waived + Partial + Missing = Total.
type EnvoyFrameworkStatus struct {
	Covered         int
	Waived          int
	Partial         int
	Missing         int
	Total           int
	CoveragePercent float64
}

// EnvoyFrameworkDetail pairs the partition totals with a per-control
// breakdown for rendering the envoy-framework drilldown page. The sum
// of each status bucket count across Controls equals the corresponding
// field in Status.
type EnvoyFrameworkDetail struct {
	Status   EnvoyFrameworkStatus
	Controls []ControlStatusRow
}

// ControlStatusRow is one control's status for one envoy, classified by
// the unified scorer's partition rule. Status values: "covered" (module
// tag or Connwaer trait escalation), "waived", "partial", "missing".
//
// Scope captures who is responsible for satisfying the control:
// "enforced" (Vigo modules), "attested" (Vigo evidence), "documented"
// (operator uploads — see /api/v1/compliance/docs), or
// "not_applicable" (out of deployment scope). The UI surfaces scope
// alongside Status so an operator looking at a missing control sees
// whether they need to assign a module or upload a document.
type ControlStatusRow struct {
	ControlID               string
	Title                   string
	Status                  string
	Scope                   string
	Configcrates            []string // present when Status == "covered"
	Evidence                string
	RecommendedConfigcrates []string // present when Status == "missing"
}

// Handler serves all UI pages.
type Handler struct {
	renderer          *Renderer
	dataSource        DataSource
	queries           *db.Queries // direct DB access for task/workflow pages
	configStore       *config.ConfigStore
	baseInterval      time.Duration
	offlineThreshold  time.Duration
	serverAddr        string
	caFile            string
	agentDir          string
	version           string
	agentVersion      string
	assistant         *ai.Assistant
	spannerAdmin      *spanner.SpannerAdmin
	docsDir           string
	docsNavByLocale   map[string][]DocsNavSection
	enforcer          *license.Enforcer
	peerSvc           *peer.Service
	authEnabled       bool
	authMethod        string
	reportBuilder     *ReportBuilder
	integrationsCfg   config.IntegrationsSection
	grcCfg            config.GRCSection
	branding          config.BrandingSection
	i18n              config.I18nSection
	grafana           config.GrafanaSection
	cveSearcher       CVESearcher
	healthChecker     HealthChecker
	serverHostname    string
	globalObserveOnly bool
	scrierEnabled     bool
	serverConfigPath  string
	swarmTracker      *swarm.PeerTracker
	swarmFleetIndex   SwarmHostnameResolver
	swarmConfig       config.SwarmSection
	traitLookup       TraitLookup
	gitbackFleet      GitbackFleetSource
	puddleFleet       PuddleFleetSource
	curatorFleet      CuratorFleetSource
	poolqFleet        PoolqFleetSource
	mesh              MeshSource
	swarmAPI          SwarmSource
	traitQuery        TraitQueryDispatcher
	complianceDocs    ComplianceDocsListing
	securityLive      *securityLiveCache // 24h cache of security_scan_detail dispatch results
}

// DispatchOutcome categorizes the result of a single-envoy live trait
// dispatch so fragment handlers can render distinct operator messages
// for offline / timeout / responded-but-empty / responded-with-data
// — distinctions the previous `found bool` collapsed.
type DispatchOutcome int

const (
	// DispatchResponded — agent answered with usable data.
	DispatchResponded DispatchOutcome = iota
	// DispatchNoData — agent answered, but with no usable data
	// (found=false, empty value, or a payload that decodes to nothing).
	DispatchNoData
	// DispatchOffline — envoy is not in the connected set and has no
	// useful cached value for the requested (often virtual) trait.
	DispatchOffline
	// DispatchTimeout — dispatched to the agent but no response landed
	// inside the timeout window.
	DispatchTimeout
)

// TraitQueryDispatcher dispatches a single live trait-value query to one
// envoy on behalf of a UI fragment handler. Implemented by an adapter over
// server/grpc.AgentService.DispatchQuery wired in at server startup — kept
// as a minimal interface here so the ui package doesn't import server/grpc.
type TraitQueryDispatcher interface {
	DispatchTraitQuery(ctx context.Context, traitPath, hostname string, timeout time.Duration) (value string, outcome DispatchOutcome, err error)
}

// SetTraitQueryDispatcher wires the live-query dispatcher used by fragment
// handlers that need to reach an agent on demand (currently: the security
// drawer's live-detail fetch). Nil means the page falls back to
// cached-trait rendering only.
func (h *Handler) SetTraitQueryDispatcher(d TraitQueryDispatcher) {
	h.traitQuery = d
}

// GitbackFleetSource surfaces the aggregated fleet-wide gitback
// view. Used by the swarm list (to decorate gitback rows with
// mirror summary pills) and the swarm detail page (to render the
// Gitback Mesh card). Implemented by `*api.GitbackHandler` in
// the main binary; a tiny interface so the UI package doesn't
// depend on the full aggregator handler type.
type GitbackFleetSource interface {
	FleetSnapshot(ctx context.Context) (*gitbackmesh.Fleet, error)
	EnvoyProjects(ctx context.Context, envoyID string) []gitbackmesh.EnvoyProject
}

// SetGitbackFleet wires the fleet aggregator into the UI handler.
// Called from gateway.go after both handlers are constructed.
func (h *Handler) SetGitbackFleet(src GitbackFleetSource) {
	h.gitbackFleet = src
}

// PuddleFleetSource surfaces the aggregated fleet-wide puddle view
// (ADR-014) — the puddle topology + health the `/puddle` page renders.
// Implemented by `*api.PuddleHandler` in the main binary; a tiny
// interface so the UI package doesn't depend on the full handler type.
type PuddleFleetSource interface {
	FleetSnapshot(ctx context.Context) (*puddlemesh.Fleet, error)
}

// SetPuddleFleet wires the puddle aggregator into the UI handler.
func (h *Handler) SetPuddleFleet(src PuddleFleetSource) {
	h.puddleFleet = src
}

// CuratorFleetSource surfaces the aggregated fleet-wide curator
// view (signed catalog + operator-block table) — the same data the
// `/api/v1/swarm/curator/list` REST endpoint serves. Implemented by
// `*api.CuratorHandler` in the main binary; declared as a tiny
// interface here so the ui package doesn't depend on the full handler
// type. Slice 5b consumer.
type CuratorFleetSource interface {
	FleetSnapshot(ctx context.Context) (*curatormesh.Catalog, []db.CuratorBlockedArtifact, error)
}

// SetCuratorFleet wires the curator aggregator into the UI handler.
// Called from server/main.go after the curator handler is constructed
// (curator is mounted from main.go, not gateway.go, because it depends
// on the swarm manifest store + the catalog feed reactor).
func (h *Handler) SetCuratorFleet(src CuratorFleetSource) {
	h.curatorFleet = src
}

// PoolqFleetSource surfaces the aggregated fleet-wide poolq view (signed
// message log per topic + operator-block table) — the same data the
// `/api/v1/swarm/poolq/*` REST endpoints serve. Implemented by
// `*api.PoolqHandler` in the main binary; tiny interface so the ui
// package doesn't depend on the full handler type (matches the curator
// pattern).
type PoolqFleetSource interface {
	FleetSnapshot(ctx context.Context) (*poolqmesh.Snapshot, []db.PoolqBlockedTopic, error)
}

// SetPoolqFleet wires the poolq aggregator into the UI handler. Called
// from server.go alongside SetCuratorFleet.
func (h *Handler) SetPoolqFleet(src PoolqFleetSource) {
	h.poolqFleet = src
}

// MeshSource surfaces the longdrawer + lockbox fleet aggregators.
// Implemented by `*api.MeshHandler`; tiny interface so the UI
// package doesn't depend on the full handler type.
type MeshSource interface {
	SyncSnapshot(ctx context.Context) (*longdrawermesh.Fleet, error)
	CryptSnapshot(ctx context.Context) (*lockboxmesh.Fleet, error)
	EnvoyLongdrawerFiles(ctx context.Context, envoyID string) []longdrawermesh.EnvoyFile
	EnvoyLockboxFiles(ctx context.Context, envoyID string) []lockboxmesh.EnvoyFile
}

// SetMesh wires the longdrawer/lockbox fleet aggregators into the
// UI handler. Called from gateway.go.
func (h *Handler) SetMesh(src MeshSource) {
	h.mesh = src
}

// ComplianceDocsListing is a UI-facing slice of operator-uploaded
// compliance documents attached to a (framework, control_id). The
// concrete store lives in server/compliancedocs but the UI package
// references it through this small interface to avoid a server-on-ui
// import edge.
type ComplianceDocsListing interface {
	DocsForControl(ctx context.Context, framework, controlID string) ([]ComplianceDocsListingItem, error)
}

// ComplianceDocsListingItem is the minimal projection the UI needs to
// render one attached document in the per-control drill-down.
type ComplianceDocsListingItem struct {
	ID         string
	Filename   string
	DocType    string
	MimeType   string
	SizeBytes  int64
	SHA256     string
	UploadedBy string
	UploadedAt string
}

// SetComplianceDocs wires the compliance-docs listing source into the
// UI handler. Optional — when nil, the per-control drill-down hides
// its Documents section. Called from gateway.go after both handlers
// are constructed.
func (h *Handler) SetComplianceDocs(src ComplianceDocsListing) {
	h.complianceDocs = src
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
// Scope is "server" for diagnostics that describe Vigo itself and
// "fleet" for ones that describe managed envoys; the Vigo Health page
// renders only server-scope checks.
type HealthCheck struct {
	Name    string
	Status  string // "pass", "warn", "fail", "skip"
	Scope   string // "server", "fleet"
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
	// EnvoyTagsByHostname returns the operator-assigned tags for the
	// envoy with the given hostname, or nil if the hostname is unknown
	// or has no tags. Used by `buildPeerVisualMap` to honor
	// `viz.color=<name|hex>` overrides.
	EnvoyTagsByHostname(hostname string) []string
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
func NewHandler(queries *db.Queries, configStore *config.ConfigStore, baseInterval, offlineThreshold time.Duration, serverAddr, caFile, agentDir, version string, assistant *ai.Assistant, spannerAdmin *spanner.SpannerAdmin, customDS DataSource, docsDir string, authMethod string, enforcer *license.Enforcer) (*Handler, error) {
	renderer, err := NewRenderer()
	if err != nil {
		return nil, err
	}

	if customDS == nil {
		return nil, fmt.Errorf("ui.NewHandler: a data source is required")
	}
	ds := customDS

	h := &Handler{
		renderer:         renderer,
		dataSource:       ds,
		queries:          queries,
		configStore:      configStore,
		baseInterval:     baseInterval,
		offlineThreshold: offlineThreshold,
		serverAddr:       serverAddr,
		caFile:           caFile,
		agentDir:         agentDir,
		version:          version,
		assistant:        assistant,
		spannerAdmin:     spannerAdmin,
		docsDir:          docsDir,
		enforcer:         enforcer,
		authEnabled:      true,
		authMethod:       authMethod,
		securityLive:     newSecurityLiveCache(),
	}

	if docsDir != "" {
		h.docsNavByLocale = buildDocsNavByLocale(docsDir)
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

// SetGrafana stores the optional Grafana URL. When set, the /health
// page shows deep links to the bundled Vigo dashboards.
func (h *Handler) SetGrafana(g config.GrafanaSection) {
	h.grafana = g
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

// SetGlobalObserveOnly sets the global observe-only flag from server.yaml.
func (h *Handler) SetGlobalObserveOnly(v bool) {
	h.globalObserveOnly = v
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
	mux.HandleFunc("GET /envoys/tree", h.EnvoyTree)
	mux.HandleFunc("GET /envoys/{id}", h.EnvoyDetail)
	mux.HandleFunc("GET /envoys/{id}/trace", h.EnvoyTrace)
	mux.HandleFunc("GET /envoys/{id}/trace/viz", h.EnvoyTraceViz)
	mux.HandleFunc("GET /fleet/config-tree", h.ConfigTree)
	mux.HandleFunc("GET /ui/fragments/envoy-swarm-load/{id}", h.EnvoySwarmLoadFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-footprint/{id}", h.EnvoyFootprintFragment)
	mux.HandleFunc("GET /ui/fragments/dashboard-pressure", h.DashboardPressureFragment)
	mux.HandleFunc("GET /ui/api/envoys/{id}/security", h.EnvoySecurityTraits)
	mux.HandleFunc("GET /ui/fragments/security-envoy/{id}", h.SecurityEnvoyFragment)
	mux.HandleFunc("GET /runs", h.Runs)
	mux.HandleFunc("GET /runs/{id}", h.RunDetail)
	mux.HandleFunc("GET /config/{hostname}", h.Config)
	mux.HandleFunc("GET /server/config", h.ServerConfig)
	mux.HandleFunc("GET /server/config/{section}", h.ServerConfigSection)
	mux.HandleFunc("GET /plans/{id}", h.Plans)
	mux.HandleFunc("GET /risk", h.RiskPosture)
	mux.HandleFunc("GET /agent-health", h.AgentHealth)
	mux.HandleFunc("GET /compliance", h.Compliance)
	mux.HandleFunc("GET /compliance/executive", h.ComplianceExecutive)
	mux.HandleFunc("GET /integrations", h.Integrations)
	mux.HandleFunc("GET /integrations/{slug}", h.IntegrationDetailPage)
	mux.HandleFunc("GET /ui/fragments/integration/{slug}", h.IntegrationDetail)
	mux.HandleFunc("GET /compliance/evidence", h.EvidenceCenter)
	mux.HandleFunc("GET /compliance/evidence/bundle", h.EvidenceBundleDownload)
	mux.HandleFunc("GET /compliance/waivers", h.ComplianceWaivers)
	mux.HandleFunc("GET /compliance/{framework}", h.ComplianceFramework)
	mux.HandleFunc("GET /compliance/{framework}/viz", h.ComplianceFrameworkViz)
	mux.HandleFunc("GET /task", h.TaskList)
	mux.HandleFunc("GET /task/{id}", h.TaskDetail)
	mux.HandleFunc("GET /workflows", h.Workflows)
	mux.HandleFunc("GET /workflows/{id}", h.WorkflowDetail)
	mux.HandleFunc("GET /audit", h.Audit)
	mux.HandleFunc("GET /audit/verify", h.AuditVerify)
	mux.HandleFunc("GET /platform/compliance", h.PlatformCompliance)
	// Filecast pages (admin-distributed files riding the swarm substrate) —
	// always registered because SetSwarm is called after RegisterRoutes.
	// The handlers check h.swarmTracker at request time.
	mux.HandleFunc("GET /swarm", h.Swarm)
	mux.HandleFunc("GET /swarm/filecast", h.Filecast)
	mux.HandleFunc("GET /swarm/filecast/{sha256}", h.Filecast)
	mux.HandleFunc("GET /ui/fragments/swarm-filecast-envoys", h.FilecastEnvoysFragment)

	// Longdrawer, lockbox, gitback observability — sibling pages
	// under the Swarm nav group. Each admin-only; viewer sees a 403.
	mux.HandleFunc("GET /swarm/longdrawer", h.Longdrawer)
	mux.HandleFunc("GET /ui/fragments/swarm-longdrawer-user/{user}", h.EchoUserDrilldown)
	mux.HandleFunc("GET /swarm/lockbox", h.Lockbox)
	mux.HandleFunc("GET /ui/fragments/swarm-lockbox-user/{user}", h.LockboxUserDrilldown)
	mux.HandleFunc("GET /swarm/puddle", h.Puddle)
	mux.HandleFunc("GET /swarm/puddle/{pubkey}", h.Puddle)
	mux.HandleFunc("GET /swarm/gitback", h.Gitback)
	mux.HandleFunc("GET /swarm/gitback/{project_id}", h.Gitback)
	mux.HandleFunc("GET /swarm/curator", h.Curator)
	mux.HandleFunc("GET /swarm/curator/{artifact_id}", h.Curator)
	mux.HandleFunc("GET /swarm/poolq", h.Poolq)
	mux.HandleFunc("GET /swarm/poolq/{topic_id}", h.Poolq)
	// Backwards-compat: /mesh → /longdrawer (or /lockbox when ?tab=crypt).
	// Keep for one minor release so muscle-memory URLs don't 404.
	mux.HandleFunc("GET /mesh", h.MeshRedirect)

	// Sandgorgon UI surface deferred to v2: nav menu and page routes
	// removed in this release. Server/CLI/DB/REST handlers preserved
	// so the v2 release can re-expose without re-plumbing.
	// mux.HandleFunc("GET /sandgorgon", h.Sandgorgon)
	// mux.HandleFunc("GET /sandgorgon/assets/{id}", h.SandgorgonAsset)
	// mux.HandleFunc("GET /sandgorgon/certificates", h.SandgorgonCertificates)
	// mux.HandleFunc("GET /ui/fragments/sandgorgon-assets", h.SandgorgonAssetsFragment)

	// HTMX fragment endpoints
	mux.HandleFunc("GET /ui/fragments/dashboard-stats", h.DashboardStats)
	mux.HandleFunc("GET /ui/fragments/dashboard-fleet", h.DashboardFleet)
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
	mux.HandleFunc("GET /ui/fragments/envoys-stats", h.EnvoysStats)
	mux.HandleFunc("GET /ui/fragments/envoy-runs/{id}", h.EnvoyRunsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-traits/{id}", h.EnvoyTraitsFragment)
	mux.HandleFunc("GET /ui/fragments/envoy-trait-group/{id}", h.EnvoyTraitGroupFragment)
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
	mux.HandleFunc("GET /ui/fragments/health", h.HealthFragment)

	// Spanner pages — registered only in spanner mode (peer-equal;
	// standalone has no spanner admin and no federation to show).
	if h.spannerAdmin != nil {
		mux.HandleFunc("GET /spanner", h.Spanner)
		mux.HandleFunc("GET /spanner/bolts/{id}", h.BoltDetail)
	}

	// Docs pages (only registered when docsDir is configured and exists)
	if h.docsDir != "" {
		mux.HandleFunc("GET /docs", h.DocsIndex)
		mux.HandleFunc("GET /docs/{path...}", h.DocsPage)
	}

	// Security page — always available. AI chat is optional.
	mux.HandleFunc("GET /security", h.SecurityPage)
	mux.HandleFunc("GET /security/certs/{id}", h.SecurityCerts)
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
	mux.HandleFunc("GET /health", h.Health)

	// Admin functions are CLI-only (vigocli). No admin pages in web UI.
}

// --- Page data types ---

// PageData holds common fields for all page templates.
type PageData struct {
	Title           string
	ActiveNav       string
	BodyClass       string // extra CSS class on <body>
	Version         string
	PollInterval    string // HTMX polling interval — clamp(checkin.base_interval, 5s, 5m). Tracks the check-in interval but never faster than 5s (sub-5s refreshes just hammer the server) nor slower than 5m (long-interval fleets shouldn't look frozen).
	CheckInInterval string // configured check-in interval, e.g. "15s", "5m0s"
	AIEnabled       bool
	AIProviderName  string // "Claude", "Ollama", "OpenAI", etc.
	SpannerEnabled  bool
	DocsEnabled     bool
	PeerEnabled     bool
	SwarmEnabled    bool
	AuthEnabled     bool
	AuthMethod      string
	// NoEnvoys is true when zero envoys are enrolled — the first-run
	// state. The shared nav partial collapses to the brand block only,
	// and the dashboard renders the enrollment panel.
	NoEnvoys       bool
	CurrentUser    *AuthUser
	LicenseWarning string // non-empty when an enforcement violation is active (server-supplied string)
	// LicenseNearLimit gates the "approaching limit" banner, built in the
	// template from the raw counts below so the sentence localizes via
	// {{T}}. Mutually exclusive with LicenseWarning (the violation path).
	LicenseNearLimit bool
	LicenseActive    int    // active node count, for the approaching-limit banner
	LicenseMax       int    // licensed node cap, for the approaching-limit banner
	LicensePct       int    // active/max as a whole percent, for the approaching-limit banner
	OrgName          string // custom org name from branding config
	CustomLogo       bool   // true when a custom logo path is configured
	CustomFavicon    bool   // true when a custom favicon path is configured
	Theme            string // "light" (default), "dark", or "auto"
	// Locale is the resolved per-request locale (set in setCommonWithRequest);
	// templates read it as $.Locale for the {{T}} helper. Empty falls back to
	// English. Locales is the topbar picker's option set — nil (the default,
	// and whenever i18n is disabled or only one locale is available) hides the
	// picker.
	Locale  string
	Locales []LocaleChoice
}

// LicensePageData holds data for the license status page.
type LicensePageData struct {
	PageData
	Tier          string
	CustomerName  string
	LicenseID     string
	MaxEnvoys     int
	ActiveEnvoys  int
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
	Permissions []string
}

// IsAdmin returns true if the user has the admin role.
func (u *AuthUser) IsAdmin() bool {
	return u != nil && u.Role == "admin"
}

// IsCompliance returns true if the user has the compliance role —
// GRC staff and compliance-document uploaders. Parallels IsAdmin /
// IsViewer semantics for nav-side role labelling; authorization
// itself goes through HasPermission.
func (u *AuthUser) IsCompliance() bool {
	return u != nil && u.Role == "compliance"
}

// HasPermission returns true if the user has the given permission.
// Admins always have all permissions regardless of the stored slice
// — same fast-path as auth.User.HasPermission.
func (u *AuthUser) HasPermission(perm string) bool {
	if u == nil {
		return false
	}
	if u.Role == "admin" {
		return true
	}
	for _, p := range u.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// AgentPlatform represents an available agent binary (OS/architecture pair).
type AgentPlatform struct {
	OS   string
	Arch string
}

// DashboardPageData holds data for the dashboard page.
type DashboardPageData struct {
	PageData
	AgentVersion   string
	AgentPlatforms []AgentPlatform
	// BootstrapCommand is the agent-enrollment one-liner shown on the
	// first-run panel, addressed to the server's configured hostname.
	BootstrapCommand string
	TotalEnvoys      int
	ActiveEnvoys     int
	RecentFailures   int
	DriftRate        float64
	// LicenseCapacity drives the top-of-dashboard capacity banner. Empty
	// LicenseCapacityLevel means the banner is not rendered. "warning"
	// renders an orange notice (≥90% of cap), "critical" renders a red
	// notice (100% of cap — next enrollment will be rejected).
	LicenseCapacityLevel  string // "", "warning", or "critical"
	LicenseCapacityActive int
	LicenseCapacityMax    int
	// LicenseCapacitySlotsRemaining is max-active, shown in the
	// "nearing capacity" message; the template builds the localized
	// sentence from these raw counts via {{T}} + printf.
	LicenseCapacitySlotsRemaining int
	RecentRuns                    []RunRow
	FleetRows                     []FleetRow
	FleetRowsCapped               bool // true when fleet table is capped (more envoys exist)
	FleetRowsRemaining            int  // how many envoys not shown
	// Security posture
	CVECritical     int64
	CVEHigh         int64
	UniqueCVEs      int64
	HardeningScore  int64
	RootkitWarnings int64
	MalwareDetected int64
	IntegrityIssues int64
	// Module convergence
	ConvergencePct float64
	Converged      int
	Degraded       int
	Diverged       int // drift-axis: persistent change loops
	CompFailed     int
	Offline        int
	// Additional fields needed by the envoy-filter-bar partial when
	// rendered on the dashboard. The partial reads .Count* / .NoData /
	// .Changed / .Observing / .OfflineThreshold; the dashboard call
	// site remaps the existing fields via `dict` and supplies these.
	NoData           int
	Changed          int // drift-axis: last run had changes (Diverged ⊆ Changed)
	Observing        int
	OfflineThreshold time.Duration
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	WaivedControls       int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore  int
	RiskCritical  int
	RiskHigh      int
	RiskMedium    int
	RiskLow       int
	RiskTopEnvoys []RiskEnvoyRow
	// Convergence sparkline
	ConvergenceSparklineData []float64
	ConvergenceSparklineMin  int
	ConvergenceSparklineMax  int
	// Compliance sparkline
	ComplianceSparklineData []float64
	ComplianceSparklineMin  int
	ComplianceSparklineMax  int
	// Swarm distribution (used by /fleet; no longer rendered on the
	// dashboard landing page).
	SwarmFiles []SwarmFileRow
	// Certificate lifecycle — fleet-wide expiry counts.
	CertsExpiringSoon int
	CertsExpired      int
	CertsExpiringIn7  int
	CertsExpiringIn30 int
	CertHostsAffected int
	// Running-CVE fleet aggregates.
	RunningCVEsTotal        int
	RunningCVEsCritical     int
	RunningCVEsHigh         int
	RunningCVEsMedium       int
	RunningCVEsLow          int
	RunningCVEHostsAffected int
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
	AvgScore      int
	Critical      int
	High          int
	Medium        int
	Low           int
	TopRisks      []RiskEnvoyRow
	SparklineData []float64
	SparklineMin  int
	SparklineMax  int
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
	Name        string
	TotalPoints int
	EnvoyCount  int
}

// FleetRow is a envoy row for the dashboard fleet status table.
type FleetRow struct {
	ID       string
	Hostname string
	IP       string
	LastSeen time.Time
	Status   string
	// Drift mirrors EnvoyRow.Drift — drives the Diverged-row peach badge.
	// DriftLabel is the precomputed string form so the template stays method-free.
	Drift         convergence.DriftLevel
	DriftLabel    string
	LastRunID     string
	FailurePct    float64
	ChangedCount  int64
	FailedCount   int64
	ResourceCount int64
	// HealthSeverity is the composite fleetpressure.EnvoyPressure.Health
	// for this row — same SoT the /envoys "Agent Health" column and the
	// /agent-health fleet page consume. Populated in datasource.go alongside
	// the rest of the row so the dashboard, envoys, and agent-health
	// surfaces can never disagree on severity.
	HealthSeverity fleetpressure.Severity
	// HealthSummary is the English one-line per-axis breakdown; kept for
	// callers/tests that read the precomputed string. Templates render the
	// hover title via {{healthSummaryL $.Locale .Pressure}} off Pressure
	// instead, so the title localizes per request.
	HealthSummary string
	// Pressure is the raw per-envoy fleetpressure projection backing the
	// Agent Health pill's localized hover title; nil when no agent-health
	// trait has been reported (HealthSeverity Unknown).
	Pressure *fleetpressure.EnvoyPressure
}

// RunRow is a single run displayed in a table.
type RunRow struct {
	ID         string
	EnvoyID    string
	Hostname   string
	StartedAt  time.Time
	FinishedAt time.Time // zero when still running; powers Duration
	Status     string
	// Trigger is what kicked off the run — "scheduled" (normal cycle)
	// or "force-push" (operator-initiated). Lets the dashboard pick
	// operator actions out of the scheduled heartbeat.
	Trigger       string
	ResourceCount int64
	ChangedCount  int64
	FailedCount   int64
	FailurePct    float64 // 0-100: failed_count / resource_count * 100
	// Outcome is the per-run failure-axis bucket — "converged",
	// "degraded", "failed", or "no data" — derived once at row build
	// time via convergence.ComputeStatus so the dashboard can show the
	// verdict next to the ratio without each surface re-deriving it.
	Outcome string
	// EnvoyDriftLabel is the envoy's *current* drift level — "none",
	// "changed", "diverged" — surfaced per-run so the convergence badge
	// can paint orange (badge--diverged) for every run row of an envoy
	// that's currently Diverged, matching the envoy header pill. Per-run
	// drift isn't a thing (drift is per-envoy), but the operator-facing
	// expectation is that a Diverged envoy's runs visually link to its
	// header — see ui/render.go's convergenceBadgeClass.
	EnvoyDriftLabel string
	// TopFailedConfigcrate is the first module that produced a non-empty
	// error in this run, or empty when nothing failed. Populated by
	// db.ListRunsWithHostname's subquery; lets operators triage from
	// the dashboard without clicking into run-detail.
	TopFailedConfigcrate string
	// Duration is StartedAt → FinishedAt, carried raw so the dashboard
	// can render it locale-aware via {{friendlyDurationL}} ("2m13s" /
	// "2 min 13 s"); zero when still running.
	Duration time.Duration
}

// ConvergencePageData holds module convergence data (envoy enforcement status).
type ConvergencePageData struct {
	Total          int64
	Converged      int64
	Degraded       int64
	Failed         int64
	NoData         int64
	Changed        int64 // drift axis: last run had changes
	Diverged       int64 // drift axis: 5+ consecutive runs with changes
	Offline        int64
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
	TotalActiveWaivers int
	ExpiringWaivers    int // waivers expiring within 30 days
}

// ComplianceReportPageData holds data for a per-framework compliance report page.
type ComplianceReportPageData struct {
	PageData
	FrameworkTitle string
	FrameworkSlug  string
}

// DashboardStatsFragmentData holds data for the stats fragment (HTMX auto-refresh).
type DashboardStatsFragmentData struct {
	Locale          string // per-request locale for {{T}}/{{tEnum}} in the polled fragment
	TotalEnvoys     int
	ActiveEnvoys    int
	RecentFailures  int
	DriftRate       float64
	CVECritical     int64
	CVEHigh         int64
	UniqueCVEs      int64
	HardeningScore  int64
	RootkitWarnings int64
	ConvergencePct  float64
	Converged       int
	Degraded        int
	Diverged        int // drift-axis bucket
	CompFailed      int
	Offline         int
	// Standards compliance
	StandardsPct         float64
	SatisfiedControls    int
	WaivedControls       int
	PartialControls      int
	NotSatisfiedControls int
	TotalControls        int
	// Risk posture
	RiskAvgScore int
	RiskCritical int
	RiskHigh     int
	RiskMedium   int
	RiskLow      int
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
	CertsExpiringSoon int
	CertsExpired      int
	CertsExpiringIn7  int
	CertsExpiringIn30 int
	CertHostsAffected int
	// Running-CVE fleet aggregates from the cve_running_correlation
	// trait. Surfaced as a dashboard stat card distinct from the
	// installed-CVE counts above (UniqueCVEs et al.).
	RunningCVEsTotal        int
	RunningCVEsCritical     int
	RunningCVEsHigh         int
	RunningCVEsMedium       int
	RunningCVEsLow          int
	RunningCVEHostsAffected int
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
	// Drift is the canonical drift-axis bucket from convergence.ComputeDrift
	// (None / Changed / Diverged). Carried alongside Status so the table can
	// render the Diverged-envoy's convergence ratio in the same orange the
	// filter chip uses — without it the per-row badge would only consider
	// the failure axis and a Diverged envoy would look identical to any other
	// Degraded one. DriftLabel is the precomputed lowercase string ("none" /
	// "changed" / "diverged") so the template never has to call a method on
	// a typed int — `.DriftLabel` is a plain string field and reflection
	// surprises are off the table.
	Drift         convergence.DriftLevel
	DriftLabel    string
	FailurePct    float64 // 0-100: failed_count / resource_count * 100
	ResourceCount int64
	FailedCount   int64
	ChangedCount  int64 // changes in the latest run; drives the drift-axis pill counts and the convergence-ratio badge text
	Offline       bool
	ObserveOnly   bool // observe-only mode: drift reporting only
	// StaleThreshold is the resolved per-envoy stale threshold (2.5×
	// max(configured_interval, observed_cycle)), carried raw so the
	// template can render it locale-aware via {{friendlyDurationL}}.
	// Zero only if never derivable; the table guards with {{if}}.
	StaleThreshold time.Duration
	AgentVersion   string
	// HealthSeverity is the composite fleetpressure.EnvoyPressure.Health
	// for this row — the worst severity across FDs, host FD%,
	// OOM-recent, rootfs %, inode %, and repo-health. Drives the
	// /envoys "Agent Health" pill color; the pill links to /agent-health.
	// Unknown when no agent-health trait has been reported yet.
	HealthSeverity fleetpressure.Severity
	// HealthSummary is the English one-line per-axis breakdown; kept for
	// callers/tests that read the precomputed string. Templates render the
	// hover title via {{healthSummaryL $.Locale .Pressure}} off Pressure
	// so the title localizes per request.
	HealthSummary string
	// Pressure is the raw per-envoy fleetpressure projection backing the
	// Agent Health pill's localized hover title; nil when no agent-health
	// trait has been reported (HealthSeverity Unknown).
	Pressure *fleetpressure.EnvoyPressure

	// Slice-2 envoy-filter axes (ADR-032). All populated by the envoyRow
	// builder so the canonical row stays the SoT for what every envoys-
	// view chip can filter on. Tags/OS/Arch come from EnvoyState directly;
	// Environment/Roles/Configcrates from ConfigStore.Match's resolved config;
	// ComplianceFrameworks from ConfigStore.EnvoyFrameworks (union of the
	// standards whose covered controls intersect the envoy's modules).
	Tags                 []string
	OS                   string // Traits["os.distro"]
	Arch                 string // Traits["arch"]
	Environment          string
	Roles                []string
	Configcrates         []string
	ComplianceFrameworks []string
}

// FilterAxis is one section of the envoy filter drawer (env, tag, role,
// os, arch, module, compliance, conv, drift, reach). Free-text axes are
// multi-select (CSV toggle); IsEnum axes are single-select (set/clear).
type FilterAxis struct {
	Key     string         // query string param ("env", "conv", ...)
	Label   string         // pill section heading rendered in the drawer
	IsEnum  bool           // true → single-select; false → multi-select CSV
	Choices []FilterChoice // ordered: enum follows EnumOrder; free-text is by count desc
}

// FilterChoice is one selectable pill within a FilterAxis. URL is the
// pre-composed toggle URL the pill's <a href> emits — selecting an
// unselected pill adds it (or replaces, for enums); selecting an active
// pill removes it.
type FilterChoice struct {
	Value    string
	Count    int
	Selected bool
	URL      string
}

// EnvoysPageData holds data for the envoys list page.
type EnvoysPageData struct {
	PageData
	Envoys []EnvoyRow
	// Filter state — independent across the three axes. ConvFilter
	// matches EnvoyRow.Status (failure axis); DriftFilter
	// matches the drift bucket; ReachFilter matches reachability /
	// observability flags. All three combine with AND.
	ConvFilter  string
	DriftFilter string
	ReachFilter string
	Search      string
	// ADR-032 slice-2 chip axes — case-insensitive substring with
	// OR-within-axis (comma-separated values), AND across axes.
	EnvFilter         string
	TagFilter         string
	RoleFilter        string
	OSFilter          string
	ArchFilter        string
	ConfigcrateFilter string
	ComplianceFilter  string
	SortColumn        string // active sort column (status, hostname, last_seen, last_run, ip)
	SortDir           string // "asc" or "desc"
	EnvoyCount        int    // rows on this page after filter+offset
	MatchingTotal     int    // total entries matching filter (across all pages)
	TotalEnvoys       int    // fleet-wide total, regardless of filter
	HasMore           bool
	Offset            int
	Limit             int
	NextOffset        int
	// RenderedCap is true when the operator has paged to the hard DOM cap;
	// the table footer renders a "refine your filter" sentinel instead of
	// another load-more trigger so the DOM cannot grow unbounded.
	RenderedCap    bool
	RenderedCapMax int
	// Top-of-page summary stats. ActiveEnvoys = TotalEnvoys - CountOffline;
	// DriftRate = 100 * Diverged / Total. Aggregates here rather than on
	// the dashboard avoids a round-trip for operators who live on
	// /envoys; they're cheap to compute from the counts we already have.
	ActiveEnvoys int
	DriftRate    float64
	// Status counts for the three pill rows.
	// Failure axis (sums to TotalEnvoys):
	CountConverged int
	CountDegraded  int
	CountFailed    int
	CountNoData    int
	// Drift axis (overlapping; CountDiverged ⊆ CountChanged):
	CountChanged  int
	CountDiverged int
	// Reachability + observability:
	CountOffline   int
	CountObserving int
	// Module Health: fleet-wide failed/total module sums from every
	// envoy's most-recent run. Drives the "MODULES FAILING" stat card
	// at the top of the page — gives the operator the absolute "is
	// anything actually broken" reading independent of how envoys
	// bucket. ConfigcrateHealthPct = 100 * Failed / Total (0 when Total==0).
	FleetFailedConfigcrates int64
	FleetTotalConfigcrates  int64
	ConfigcrateHealthPct    float64
	// OfflineThreshold is the resolved staleness threshold, carried raw
	// so the Offline pill's title can render it locale-aware via
	// {{friendlyDurationL}} ("15m" / "15 min") — letting operators see
	// how long an envoy must have been silent to land in that bucket
	// without consulting server.yaml.
	OfflineThreshold time.Duration
	// FilterAxes is the per-axis pill data rendered inside the filter
	// drawer. Built by fleet.EnumerateFilterAxes from the filtered row
	// set + the request's query params; drawer template renders each
	// axis as a pill grid, conv/drift/reach included.
	FilterAxes        []FilterAxis
	ActiveFilterCount int    // number of axes with at least one selection (drives the "Filter (N active)" button label)
	BasePath          string // page path the drawer pills should anchor against ("/envoys"); tree page overrides to "/envoys/tree"
}

// EnvoyDetailRunRow is a run with its per-module results for the envoy detail page.
type EnvoyDetailRunRow struct {
	RunRow
	Results []RunResultRow
}

// RunResultRow is a single run result (per-module) displayed in a table.
type RunResultRow struct {
	ConfigcrateName string
	ResourceType    string
	ResourceName    string
	ActionTaken     string
	Changed         bool
	ErrorMessage    string
	DurationMs      int64
	// Drift is the drift-axis bucket for this row's module as of the
	// run being viewed (window: this run + DivergedThreshold-1 prior).
	// Populated by RunDetailData. DriftLabel mirrors Drift.String() so
	// templates stay method-free.
	Drift      convergence.DriftLevel
	DriftLabel string
}

// EnvoyDetailPageData holds data for the envoy detail page.
type EnvoyDetailPageData struct {
	PageData
	Envoy         EnvoyRow
	Runs          []EnvoyDetailRunRow
	Traits        map[string]any
	ScrierEnabled bool
	// UptimeSecs is the agent's process uptime, surfaced at-a-glance in the
	// hero card. Read through the fleetpressure SoT; Present=false hides it.
	UptimeSecs fleetpressure.Metric
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

// EnvoyRunsFragmentData holds data for the lazy-loaded runs fragment.
type EnvoyRunsFragmentData struct {
	Locale string // per-request locale for {{T}}/{{tEnum}} in the fragment
	Runs   []EnvoyDetailRunRow
}

// EnvoyTraitsFragmentData holds data for the lazy-loaded traits fragment.
// Tree carries the original nested trait structure for grouped rendering;
// Summary surfaces the most-asked host facts as stat-cards; RawJSON gives
// power users a copy-paste escape hatch. EnvoyID is the dotted-path URL
// segment templates use to construct per-group lazy-load hx-get URLs.
type EnvoyTraitsFragmentData struct {
	Locale      string // per-request locale for {{T}}/{{tEnum}} in the fragment
	EnvoyID     string
	Tree        []TraitNode
	LeafCount   int
	CollectedAt time.Time
	Summary     TraitSummary
	RawJSON     string
	HasData     bool
}

// EnvoyTraitGroupFragmentData is the render shape for one group's lazy-
// loaded subtree. The fragment renders Children recursively using the
// existing `trait-node` template — only what's inside this group, never
// the rest of the tree.
type EnvoyTraitGroupFragmentData struct {
	Locale   string // per-request locale for {{T}}/{{tEnum}} in the fragment
	EnvoyID  string
	Path     string
	Children []TraitNode
}

// EnvoyTraitsFilterFragmentData is the render shape for a substring
// filter response. Matches is a flat list of every leaf whose Path
// contains the query (case-insensitive substring match); rendered as
// `.trait-leaf` rows inside `#traits-tree`, replacing the collapsed
// tree while a query is active.
type EnvoyTraitsFilterFragmentData struct {
	Locale  string // per-request locale for {{T}}/{{tEnum}} in the fragment
	Query   string
	Matches []TraitNode
	Total   int // total leaves searched, for the "N / M" counter
}

// TraitNode is one node in the rendered trait tree. A node is either a
// scalar leaf (Children == nil, Value populated) or a group of children.
// Path is the full dotted key path used by the client-side filter.
type TraitNode struct {
	Key       string
	Path      string
	Value     string
	Children  []TraitNode
	LeafCount int
	IsArray   bool
}

// TraitSummary is the host-facts strip rendered above the trait tree.
// Empty fields render as "—" so the strip width stays stable across envoys
// with different collector coverage.
type TraitSummary struct {
	OS           string // e.g. "ubuntu 22.04"
	Kernel       string
	Arch         string
	CPUCores     string // logical cores; "physical / logical" when both known
	CPUModel     string
	MemoryHuman  string
	AgentVersion string
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
	EnvoyID string
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
	ConfigcrateName string
	ResourceType    string
	ResourceName    string
	PlannedAction   string
	ActionColor     string
}

// PlanDetailPageData holds data for the plan detail page.
type PlanDetailPageData struct {
	PageData
	PlanID      string
	EnvoyID     string
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
	// SearchIndexJSON is the flattened page index ({t,p,x} per page) emitted
	// as a <script type="application/json"> data island for docs.js — keeps
	// the search corpus out of an inline <script> (CSP script-src 'self').
	SearchIndexJSON template.JS
}

// buildDocsSearchIndex flattens the nav tree into the search corpus docs.js
// consumes: every page's title (t), path (p), and full-text (x), across each
// section's own pages and its subsections' pages. Mirrors the iteration the
// docs template previously did inline.
func buildDocsSearchIndex(nav []DocsNavSection) template.JS {
	type idxPage struct {
		T string `json:"t"`
		P string `json:"p"`
		X string `json:"x"`
	}
	var pages []idxPage
	for _, sec := range nav {
		for _, p := range sec.Pages {
			pages = append(pages, idxPage{p.Title, p.Path, p.Text})
		}
		for _, sub := range sec.Subsections {
			for _, p := range sub.Pages {
				pages = append(pages, idxPage{p.Title, p.Path, p.Text})
			}
		}
	}
	b, err := json.Marshal(pages)
	if err != nil {
		return template.JS("[]")
	}
	return template.JS(b)
}

// docsSectionOrder is the allowlist of documentation sections rendered in
// the in-product nav AND their display order. Sections present in
// `docs/` but absent from this list are deliberately hidden from the web
// UI (they ship for website/repo consumers but aren't operator-facing).
// Follows the engineer journey: get started → understand the system →
// operate → secure → comply → look up → try.
var docsSectionOrder = []string{
	"start",
	"howto",
	"concepts",
	"reference",
	"examples",
}

// isPublishedDocsSection reports whether a top-level docs section is in
// the `docsSectionOrder` allowlist — the single source of truth for
// "what the in-product docs serve". Both the nav builder and DocsPage
// consult it so they can't disagree on which sections are public.
// Non-operator content (adr/, internal.docs/) is absent from the
// allowlist and so is hidden from the nav AND refused by direct URL.
func isPublishedDocsSection(section string) bool {
	for _, s := range docsSectionOrder {
		if s == section {
			return true
		}
	}
	return false
}

// docsTopSegment returns the first path segment of a cleaned docs page
// path (the candidate section name). `internal.docs/strategy/x` → `internal.docs`;
// `overview` → `overview`.
func docsTopSegment(pagePath string) string {
	p := strings.TrimPrefix(filepath.Clean("/"+pagePath), "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// isDocsSectionDir reports whether `<docsDir>/<section>` is a directory —
// i.e. the segment names a docs section rather than a root-level page.
func isDocsSectionDir(docsDir, section string) bool {
	info, err := os.Stat(filepath.Join(docsDir, section))
	return err == nil && info.IsDir()
}

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

// docsNavForLocale returns the prebuilt nav tree for a locale, falling back to
// the default-locale nav when no localized nav was built.
func (h *Handler) docsNavForLocale(locale string) []DocsNavSection {
	if nav, ok := h.docsNavByLocale[locale]; ok {
		return nav
	}
	return h.docsNavByLocale[i18n.DefaultLocaleFallback]
}

// buildDocsNavByLocale builds one nav tree per available locale. The default
// ("en") nav is always built; for each locale subdirectory present under
// docsDir (e.g. docs/fr/) a parallel nav is built whose page STRUCTURE/URLs
// are identical to English — only the displayed titles (and search text)
// differ, sourced from the localized file when it exists and falling back to
// the English file otherwise. The doc tree is ~70 pages, so building a handful
// of navs at startup is cheap.
func buildDocsNavByLocale(docsDir string) map[string][]DocsNavSection {
	out := map[string][]DocsNavSection{
		i18n.DefaultLocaleFallback: buildDocsNav(docsDir, i18n.DefaultLocaleFallback),
	}
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		loc := entry.Name()
		if loc == i18n.DefaultLocaleFallback {
			continue
		}
		// Only treat top-level dirs that look like locale codes (i.e. not a
		// docs section) as locale roots. A locale dir mirrors the English
		// tree; a docs section (start/, howto/, …) is in docsSectionOrder.
		if isPublishedDocsSection(loc) {
			continue
		}
		out[loc] = buildDocsNav(docsDir, loc)
	}
	return out
}

// docsNavTitle returns the title for a docs page in the given locale: the
// localized file's first `#` heading when docs/<locale>/<relPath> exists,
// else the English file's. relPath is relative to docsDir (e.g.
// "start/overview.md").
func docsNavTitle(docsDir, locale, relPath string) string {
	if locale != i18n.DefaultLocaleFallback {
		if t := extractMDTitle(filepath.Join(docsDir, locale, relPath)); t != "" {
			return t
		}
	}
	return extractMDTitle(filepath.Join(docsDir, relPath))
}

// docsNavText returns the search-index plain text for a docs page in the given
// locale, preferring the localized file's content and falling back to English.
func docsNavText(docsDir, locale, relPath string) string {
	if locale != i18n.DefaultLocaleFallback {
		if raw, err := os.ReadFile(filepath.Join(docsDir, locale, relPath)); err == nil {
			return stripMarkdown(stripDocI18nMarker(string(raw)))
		}
	}
	if raw, err := os.ReadFile(filepath.Join(docsDir, relPath)); err == nil {
		return stripMarkdown(string(raw))
	}
	return ""
}

// buildDocsNav walks the docs directory and builds a nav tree. The page
// structure/URLs are always sourced from the English tree; titles and search
// text are resolved per-locale (localized file when present, English fallback).
func buildDocsNav(docsDir, locale string) []DocsNavSection {
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
		sectionTitle := docsNavTitle(docsDir, locale, filepath.Join(sectionPath, "_index.md"))
		if sectionTitle == "" {
			sectionTitle = strings.ReplaceAll(sectionPath, "-", " ")
			sectionTitle = titleCaser.String(sectionTitle)
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
				// Build a subsection for nested directories — but only when it
				// holds navigable pages. Asset-only subdirs (howto/grafana/
				// dashboard JSON, howto/_pdf/ rendered PDFs) would otherwise
				// render as empty nav entries; they stay reachable by direct
				// link via DocsPage's static-asset branch, just not in the nav.
				sub := buildSubsection(docsDir, locale, sectionPath, pf.Name())
				if len(sub.Pages) > 0 {
					subsections = append(subsections, sub)
				}
				continue
			}
			if !strings.HasSuffix(pf.Name(), ".md") || pf.Name() == "_index.md" {
				continue
			}

			pageName := strings.TrimSuffix(pf.Name(), ".md")
			relPath := filepath.Join(sectionPath, pf.Name())
			pageTitle := docsNavTitle(docsDir, locale, relPath)
			if pageTitle == "" {
				pageTitle = strings.ReplaceAll(pageName, "-", " ")
				pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
			}

			// Read file content for full-text search index.
			plainText := docsNavText(docsDir, locale, relPath)

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

	// Render only sections listed in docsSectionOrder. Sections present
	// on disk but absent from the allowlist are intentionally hidden
	// from the in-product UI (non-operator content: adr/, etc.).
	var nav []DocsNavSection
	for _, name := range docsSectionOrder {
		if sec, ok := sectionMap[name]; ok {
			nav = append(nav, sec)
		}
	}
	return nav
}

// buildSubsection builds a DocsNavSection for a nested directory within a
// section. Page structure comes from the English tree; titles/text resolve
// per-locale (localized file when present, English fallback).
func buildSubsection(docsDir, locale, parentPath, dirName string) DocsNavSection {
	subPath := parentPath + "/" + dirName
	subTitle := docsNavTitle(docsDir, locale, filepath.Join(subPath, "_index.md"))
	if subTitle == "" {
		subTitle = strings.ReplaceAll(dirName, "-", " ")
		subTitle = titleCaser.String(subTitle)
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
		relPath := filepath.Join(subPath, pf.Name())
		pageTitle := docsNavTitle(docsDir, locale, relPath)
		if pageTitle == "" {
			pageTitle = strings.ReplaceAll(pageName, "-", " ")
			pageTitle = strings.ReplaceAll(pageTitle, "_", " ")
		}

		plainText := docsNavText(docsDir, locale, relPath)

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

// docI18nMarkerRe matches the leading provenance marker that the French-doc
// generator writes as the first line of every translated file:
//
//	<!-- vigo-i18n source_sha:HEX -->
//
// HEX is the lowercase sha256 of the English source's raw bytes (the drift
// audit checks it; see test/audit TestFrenchDocsNotStale).
var docI18nMarkerRe = regexp.MustCompile(`^<!-- vigo-i18n source_sha:[0-9a-fA-F]+ -->[ \t]*\r?\n`)

// stripDocI18nMarker removes a leading `<!-- vigo-i18n source_sha:... -->`
// marker line (and a single following blank line, if present) so the comment
// never renders and extractMDTitle still finds the first `#` heading. Files
// without the marker pass through unchanged.
func stripDocI18nMarker(s string) string {
	loc := docI18nMarkerRe.FindStringIndex(s)
	if loc == nil {
		return s
	}
	s = s[loc[1]:]
	// Drop one immediately-following blank line.
	if strings.HasPrefix(s, "\r\n") {
		return s[2:]
	}
	if strings.HasPrefix(s, "\n") {
		return s[1:]
	}
	return s
}

// readDocFile reads a docs markdown file for the given locale. For a
// non-default locale it tries docs/<locale>/<relPath> first and falls back to
// the English docs/<relPath>. relPath is relative to docsDir. Returns the raw
// bytes and whether a file was found.
func (h *Handler) readDocFile(locale, relPath string) ([]byte, bool) {
	if locale != i18n.DefaultLocaleFallback {
		if raw, err := os.ReadFile(filepath.Join(h.docsDir, locale, relPath)); err == nil {
			return raw, true
		}
	}
	if raw, err := os.ReadFile(filepath.Join(h.docsDir, relPath)); err == nil {
		return raw, true
	}
	return nil, false
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
	locale := h.localeFromRequest(r)
	content := ""
	if raw, ok := h.readDocFile(locale, "_index.md"); ok {
		content = rewriteDocsLinks(RenderMarkdown(stripDocI18nMarker(string(raw))), "")
	} else {
		content = "<h1>Documentation</h1><p>No docs found. Place markdown files in the docs directory.</p>"
	}

	nav := h.docsNavForLocale(locale)

	data := DocsPageData{
		PageData:        PageData{Title: "Documentation", ActiveNav: "docs"},
		Content:         template.HTML(content),
		NavTree:         nav,
		SearchIndexJSON: buildDocsSearchIndex(nav),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "docs", data)
}

// DocsPage renders an individual docs page from disk.
func (h *Handler) DocsPage(w http.ResponseWriter, r *http.Request) {
	pagePath := r.PathValue("path")

	// Published-section allowlist — the same `docsSectionOrder` rule the
	// nav builder uses. A request whose top-level segment names a docs
	// directory that isn't in the allowlist (adr/, internal.docs/) gets
	// the same 404 as a missing page, whether or not the files happen to
	// be on disk — so non-operator content never serves by direct URL.
	// Defense in depth behind .dockerignore; covers markdown pages AND
	// static assets. Root-level pages (overview.md, glossary.md) aren't
	// directories, so they fall through and serve normally.
	if section := docsTopSegment(pagePath); section != "" &&
		isDocsSectionDir(h.docsDir, section) && !isPublishedDocsSection(section) {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

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

	locale := h.localeFromRequest(r)

	// Try exact .md file first, then _index.md for directories. Each attempt
	// prefers the localized file (docs/<locale>/<relPath>) and falls back to
	// English; the chosen file determines baseDir for relative-link resolution.
	// URLs stay locale-free — the request locale (cookie) selects the content.
	var raw []byte
	var baseDir string // directory for resolving relative links
	if r2, ok := h.readDocFile(locale, pagePath+".md"); ok {
		raw = r2
		baseDir = path.Dir(pagePath) // regular file: resolve from parent dir
		if baseDir == "." {
			baseDir = ""
		}
	} else if r2, ok := h.readDocFile(locale, filepath.Join(pagePath, "_index.md")); ok {
		raw = r2
		baseDir = pagePath // _index.md: relative links resolve from this dir
	} else {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	// Strip the i18n provenance marker BEFORE rendering and title extraction so
	// the comment never renders and the first `#` heading is still the title.
	content := rewriteDocsLinks(RenderMarkdown(stripDocI18nMarker(string(raw))), baseDir)

	nav := h.docsNavForLocale(locale)

	breadcrumbs, currentSection := buildDocsBreadcrumbs(pagePath, nav)

	data := DocsPageData{
		PageData:        PageData{Title: "Docs - " + breadcrumbs[len(breadcrumbs)-1].Title, ActiveNav: "docs"},
		Content:         template.HTML(content),
		Breadcrumbs:     breadcrumbs,
		NavTree:         nav,
		CurrentPath:     pagePath,
		CurrentSection:  currentSection,
		SearchIndexJSON: buildDocsSearchIndex(nav),
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "docs", data)
}

// noEnvoys reports whether zero envoys are enrolled — the first-run state
// that drives the dashboard enrollment panel and the collapsed nav. Backed
// by the O(1) FleetIndex convergence counter via the data source.
func (h *Handler) noEnvoys() bool {
	return h.dataSource.DashboardStatsData(h.baseInterval, h.offlineThreshold).TotalEnvoys == 0
}

// formatPollInterval renders the UI's HTMX poll cadence as a duration
// string, clamped to [5s, 5m] regardless of the check-in interval. The
// cadence tracks the configured check-in interval in between, but never
// polls faster than every 5s (a human doesn't need sub-5s refreshes, and
// at a 1s check-in default that would hammer the fragment endpoints for no
// added value) and never slower than every 5m (so a long-interval fleet
// doesn't look frozen). A zero/negative input falls back to the cap.
func formatPollInterval(checkIn time.Duration) string {
	const minPoll = 5 * time.Second
	const maxPoll = 5 * time.Minute
	if checkIn <= 0 || checkIn > maxPoll {
		return fmt.Sprintf("%.0fs", maxPoll.Seconds())
	}
	if checkIn < minPoll {
		return fmt.Sprintf("%.0fs", minPoll.Seconds())
	}
	return fmt.Sprintf("%.0fs", checkIn.Seconds())
}

// setCommon applies version, AI-enabled, spanner, auth, and license flags to page data.
func (h *Handler) setCommon(pd *PageData) {
	pd.Version = h.version
	pd.PollInterval = formatPollInterval(h.baseInterval)
	pd.CheckInInterval = h.baseInterval.String()
	pd.AIEnabled = h.assistant != nil
	if h.assistant != nil {
		name := h.assistant.ProviderName()
		if name != "" {
			pd.AIProviderName = strings.ToUpper(name[:1]) + name[1:]
		}
	}
	pd.SpannerEnabled = h.spannerAdmin != nil
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
	pd.Locales = h.localeChoices()

	// NoEnvoys gates the first-run UI — empty nav + dashboard enrollment panel.
	pd.NoEnvoys = h.noEnvoys()

	if h.enforcer != nil {
		s := h.enforcer.GetStatus()
		if s.Stage > 0 {
			pd.LicenseWarning = s.Violation
		} else if s.MaxEnvoys > 0 {
			threshold := float64(s.MaxEnvoys) * 0.9
			if float64(s.ActiveEnvoys) >= threshold {
				pd.LicenseNearLimit = true
				pd.LicenseActive = s.ActiveEnvoys
				pd.LicenseMax = s.MaxEnvoys
				pd.LicensePct = s.ActiveEnvoys * 100 / s.MaxEnvoys
			}
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
	pd.Locale = h.localeFromRequest(r)
	if h.authEnabled {
		if u := auth.UserFromContext(r.Context()); u != nil {
			pd.CurrentUser = &AuthUser{
				ID:          u.ID,
				Username:    u.Username,
				DisplayName: u.DisplayName,
				Role:        u.Role,
				Permissions: u.Permissions,
			}
		}
	}
}

// --- Handlers ---

// Dashboard renders the main dashboard page. Users without
// fleet.read (i.e., the compliance role) get redirected to
// /compliance instead — the dashboard is a fleet-state surface and
// has no compliance-shaped fallback worth rendering.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	// In the first-run (zero-envoy) state the dashboard is universal
	// onboarding — show the enrollment panel to every operator, including
	// roles without fleet.read that are otherwise routed to /compliance.
	if h.authEnabled && !h.noEnvoys() {
		if u := auth.UserFromContext(r.Context()); u != nil && !u.HasPermission(auth.PermFleetRead) {
			http.Redirect(w, r, "/compliance", http.StatusFound)
			return
		}
	}

	// Per-step timing for slow-dashboard diagnosis. One slog.Info line per
	// render; each step's elapsed ms is its own attribute so log parsers
	// can pivot on the slowest sub-step without parsing free-form text.
	start := time.Now()
	step := func() func() int64 {
		t := time.Now()
		return func() int64 { return time.Since(t).Milliseconds() }
	}

	doneDashboard := step()
	data := h.dashboardData()
	msDashboard := doneDashboard()

	// Enrich with version and platform data.
	doneAgentPlatforms := step()
	data.AgentVersion = h.agentVersion
	if data.AgentVersion == "" {
		data.AgentVersion = h.version
	}
	data.AgentPlatforms = h.agentPlatforms()
	msAgentPlatforms := doneAgentPlatforms()

	// First-run enrollment command, addressed to the server's configured
	// hostname so it's deterministic regardless of how the operator
	// reached the UI. Rendered by the dashboard's no-envoys panel.
	bootstrapHost := h.serverHostname
	if bootstrapHost == "" {
		bootstrapHost = "localhost"
	}
	data.BootstrapCommand = fmt.Sprintf("curl -sSfk https://%s:8443/bootstrap | sudo sh", bootstrapHost)

	// Enrich with security posture data (all CVE counts are unique/deduplicated).
	doneSecurity := step()
	sec := h.dataSource.SecurityScanData()
	data.CVECritical = sec.UniqueCritical
	data.CVEHigh = sec.UniqueHigh
	data.UniqueCVEs = sec.UniqueCVEs
	data.HardeningScore = sec.LynisAvgScore
	data.RootkitWarnings = sec.RootkitWarning
	data.MalwareDetected = sec.ClamavInfected
	data.IntegrityIssues = sec.AideChanged
	msSecurity := doneSecurity()

	// Enrich with standards compliance data.
	doneStandards := step()
	stds := h.computeStandardsSummary()
	data.StandardsPct = stds.Pct
	data.SatisfiedControls = stds.Satisfied
	data.WaivedControls = stds.Waived
	data.PartialControls = stds.Partial
	data.NotSatisfiedControls = stds.NotSatisfied
	data.TotalControls = stds.Total
	msStandards := doneStandards()

	// Enrich with risk posture data.
	doneRisk := step()
	rsk := h.dataSource.RiskData(h.baseInterval, h.offlineThreshold)
	data.RiskAvgScore = rsk.AvgScore
	data.RiskCritical = rsk.Critical
	data.RiskHigh = rsk.High
	data.RiskMedium = rsk.Medium
	data.RiskLow = rsk.Low
	data.RiskTopEnvoys = rsk.TopRisks
	msRisk := doneRisk()

	// Enrich with swarm distribution data (still populated for /fleet
	// which shares this data type; the dashboard page no longer renders
	// the swarm table itself).
	doneSwarm := step()
	if h.swarmTracker != nil {
		data.SwarmFiles = h.swarmFiles()
	}
	msSwarm := doneSwarm()

	// Enrich with sparkline history.
	doneSparklines := step()
	data.ConvergenceSparklineData, data.ConvergenceSparklineMin, data.ConvergenceSparklineMax = h.convergenceSparkline()
	data.ComplianceSparklineData, data.ComplianceSparklineMin, data.ComplianceSparklineMax = h.complianceSparkline()
	msSparklines := doneSparklines()

	// Certificate expiry summary.
	doneCerts := step()
	certs := h.dataSource.CertExpirySummary()
	data.CertsExpiringSoon = certs.ExpiringSoon
	data.CertsExpired = certs.Expired
	data.CertsExpiringIn7 = certs.ExpiringIn7
	data.CertsExpiringIn30 = certs.ExpiringIn30
	data.CertHostsAffected = certs.HostsAffected
	msCerts := doneCerts()

	// Running-CVE fleet aggregates.
	doneRunningCVEs := step()
	runCVEs := h.dataSource.RunningCVESummary()
	data.RunningCVEsTotal = runCVEs.Total
	data.RunningCVEsCritical = runCVEs.Critical
	data.RunningCVEsHigh = runCVEs.High
	data.RunningCVEsMedium = runCVEs.Medium
	data.RunningCVEsLow = runCVEs.Low
	data.RunningCVEHostsAffected = runCVEs.HostsAffected
	msRunningCVEs := doneRunningCVEs()

	doneRender := step()
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "dashboard", data)
	msRender := doneRender()

	slog.Info("dashboard render timing",
		"total_ms", time.Since(start).Milliseconds(),
		"dashboard_data_ms", msDashboard,
		"agent_platforms_ms", msAgentPlatforms,
		"security_scan_ms", msSecurity,
		"standards_ms", msStandards,
		"risk_data_ms", msRisk,
		"swarm_files_ms", msSwarm,
		"sparklines_ms", msSparklines,
		"certs_ms", msCerts,
		"running_cves_ms", msRunningCVEs,
		"render_ms", msRender,
	)
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

// dashboardData builds dashboard page data from the live FleetIndex
// via the data source (single in-memory snapshot, no N+1 queries),
// then layers on the license banner and recent-runs DB query.
func (h *Handler) dashboardData() DashboardPageData {
	data := h.dataSource.DashboardData(h.baseInterval, h.offlineThreshold)

	// License capacity banner: render once we know the live count and the
	// license cap. Orange at ≥90% of cap, red at 100% (next enrollment
	// will be rejected by the hard gate). Free tier is still subject to
	// the same rules — a community user at the cap should see the banner too.
	// ActiveEnvoys is the enforcer's mode-aware count (the licensed-count SoT:
	// local FleetIndex locally, spanner-wide in aggregate mode) so the banner
	// agrees with the /license page and header warning frame-for-frame.
	if h.enforcer != nil {
		if st := h.enforcer.GetStatus(); st.MaxEnvoys > 0 {
			active := st.ActiveEnvoys
			max := st.MaxEnvoys
			data.LicenseCapacityActive = active
			data.LicenseCapacityMax = max
			switch {
			case active >= max:
				data.LicenseCapacityLevel = "critical"
			case active*10 >= max*9: // ≥90%
				data.LicenseCapacityLevel = "warning"
				data.LicenseCapacitySlotsRemaining = max - active
			}
		}
	}

	// Recent runs (always from DB, lightweight query). h.queries is nil
	// only in the early-boot window and unit tests — skip the query
	// rather than panic; the rest of the dashboard still renders.
	var allRuns []db.RunWithHostname
	if h.queries != nil {
		ctx := context.Background()
		var err error
		allRuns, err = h.queries.ListRunsWithHostname(ctx, 10)
		if err != nil {
			slog.Error("failed to list recent runs", "error", err)
		}
	}
	data.RecentRuns = make([]RunRow, 0, len(allRuns))
	for _, r := range allRuns {
		var failPct float64
		if r.ResourceCount > 0 {
			failPct = float64(r.FailedCount) / float64(r.ResourceCount) * 100
		}
		var finished time.Time
		if r.FinishedAt.Valid {
			finished = r.FinishedAt.Time
		}
		var dur time.Duration
		if !finished.IsZero() && !r.StartedAt.IsZero() {
			dur = finished.Sub(r.StartedAt)
		}
		outcome := convergence.ComputeStatus(convergence.RunInfo{
			Status:        r.Status,
			ChangedCount:  r.ChangedCount,
			FailedCount:   r.FailedCount,
			ResourceCount: r.ResourceCount,
		}).String()
		data.RecentRuns = append(data.RecentRuns, RunRow{
			ID:                   r.ID,
			EnvoyID:              r.EnvoyID,
			Hostname:             r.Hostname,
			StartedAt:            r.StartedAt,
			FinishedAt:           finished,
			Status:               r.Status,
			Trigger:              r.Trigger,
			ResourceCount:        r.ResourceCount,
			ChangedCount:         r.ChangedCount,
			FailedCount:          r.FailedCount,
			FailurePct:           failPct,
			Outcome:              outcome,
			EnvoyDriftLabel:      h.dataSource.EnvoyDriftLabel(r.EnvoyID),
			TopFailedConfigcrate: r.TopFailedConfigcrate,
			Duration:             dur,
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
	data := h.dataSource.DashboardStatsData(h.baseInterval, h.offlineThreshold)
	data.Locale = h.localeFromRequest(r)

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

	rsk := h.dataSource.RiskData(h.baseInterval, h.offlineThreshold)
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
	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "dashboard", "dashboard-fleet", data)
}

// DashboardActivity renders the recent activity table fragment for HTMX polling.
func (h *Handler) DashboardActivity(w http.ResponseWriter, r *http.Request) {
	data := h.dataSource.DashboardData(h.baseInterval, h.offlineThreshold)
	data.Locale = h.localeFromRequest(r)
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
	Locale       string // per-request locale for {{T}}/{{tEnum}} in the fragment
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
		Locale:       h.localeFromRequest(r),
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
	if cfg.Slack != nil {
		a("Slack", "slack", "Alerting / Chat", cfg.Slack.Enabled, e(cfg.Slack.Events))
	} else {
		a("Slack", "slack", "Alerting / Chat", false, 0)
	}
	if cfg.PagerDuty != nil {
		a("PagerDuty", "pagerduty", "Alerting / Chat", cfg.PagerDuty.Enabled, e(cfg.PagerDuty.Events))
	} else {
		a("PagerDuty", "pagerduty", "Alerting / Chat", false, 0)
	}
	if cfg.Opsgenie != nil {
		a("Opsgenie", "opsgenie", "Alerting / Chat", cfg.Opsgenie.Enabled, e(cfg.Opsgenie.Events))
	} else {
		a("Opsgenie", "opsgenie", "Alerting / Chat", false, 0)
	}
	if cfg.Teams != nil {
		a("Microsoft Teams", "teams", "Alerting / Chat", cfg.Teams.Enabled, e(cfg.Teams.Events))
	} else {
		a("Microsoft Teams", "teams", "Alerting / Chat", false, 0)
	}
	// ITSM / Ticketing
	if cfg.ConnectWise != nil {
		a("ConnectWise", "connectwise", "ITSM / Ticketing", cfg.ConnectWise.Enabled, e(cfg.ConnectWise.Events))
	} else {
		a("ConnectWise", "connectwise", "ITSM / Ticketing", false, 0)
	}
	if cfg.Autotask != nil {
		a("Autotask", "autotask", "ITSM / Ticketing", cfg.Autotask.Enabled, e(cfg.Autotask.Events))
	} else {
		a("Autotask", "autotask", "ITSM / Ticketing", false, 0)
	}
	if cfg.ServiceNow != nil {
		a("ServiceNow", "servicenow", "ITSM / Ticketing", cfg.ServiceNow.Enabled, e(cfg.ServiceNow.Events))
	} else {
		a("ServiceNow", "servicenow", "ITSM / Ticketing", false, 0)
	}
	if cfg.Jira != nil {
		a("Jira", "jira", "ITSM / Ticketing", cfg.Jira.Enabled, e(cfg.Jira.Events))
	} else {
		a("Jira", "jira", "ITSM / Ticketing", false, 0)
	}
	if cfg.Zendesk != nil {
		a("Zendesk", "zendesk", "ITSM / Ticketing", cfg.Zendesk.Enabled, e(cfg.Zendesk.Events))
	} else {
		a("Zendesk", "zendesk", "ITSM / Ticketing", false, 0)
	}
	if cfg.Freshservice != nil {
		a("Freshservice", "freshservice", "ITSM / Ticketing", cfg.Freshservice.Enabled, e(cfg.Freshservice.Events))
	} else {
		a("Freshservice", "freshservice", "ITSM / Ticketing", false, 0)
	}
	if cfg.Linear != nil {
		a("Linear", "linear", "ITSM / Ticketing", cfg.Linear.Enabled, e(cfg.Linear.Events))
	} else {
		a("Linear", "linear", "ITSM / Ticketing", false, 0)
	}
	if cfg.GitHubIssues != nil {
		a("GitHub Issues", "github-issues", "ITSM / Ticketing", cfg.GitHubIssues.Enabled, e(cfg.GitHubIssues.Events))
	} else {
		a("GitHub Issues", "github-issues", "ITSM / Ticketing", false, 0)
	}
	// SIEM / Observability
	if cfg.Splunk != nil {
		a("Splunk", "splunk", "SIEM / Observability", cfg.Splunk.Enabled, e(cfg.Splunk.Events))
	} else {
		a("Splunk", "splunk", "SIEM / Observability", false, 0)
	}
	if cfg.Elastic != nil {
		a("Elasticsearch", "elastic", "SIEM / Observability", cfg.Elastic.Enabled, e(cfg.Elastic.Events))
	} else {
		a("Elasticsearch", "elastic", "SIEM / Observability", false, 0)
	}
	if cfg.Datadog != nil {
		a("Datadog", "datadog", "SIEM / Observability", cfg.Datadog.Enabled, e(cfg.Datadog.Events))
	} else {
		a("Datadog", "datadog", "SIEM / Observability", false, 0)
	}
	if cfg.SumoLogic != nil {
		a("Sumo Logic", "sumologic", "SIEM / Observability", cfg.SumoLogic.Enabled, e(cfg.SumoLogic.Events))
	} else {
		a("Sumo Logic", "sumologic", "SIEM / Observability", false, 0)
	}
	if cfg.Sentinel != nil {
		a("Microsoft Sentinel", "sentinel", "SIEM / Observability", cfg.Sentinel.Enabled, e(cfg.Sentinel.Events))
	} else {
		a("Microsoft Sentinel", "sentinel", "SIEM / Observability", false, 0)
	}
	if cfg.Loki != nil {
		a("Grafana Loki", "loki", "SIEM / Observability", cfg.Loki.Enabled, e(cfg.Loki.Events))
	} else {
		a("Grafana Loki", "loki", "SIEM / Observability", false, 0)
	}
	// RMM
	if cfg.NinjaRMM != nil {
		a("NinjaRMM", "ninjarmm", "RMM", cfg.NinjaRMM.Enabled, e(cfg.NinjaRMM.Events))
	} else {
		a("NinjaRMM", "ninjarmm", "RMM", false, 0)
	}
	if cfg.Kaseya != nil {
		a("Kaseya VSA", "kaseya", "RMM", cfg.Kaseya.Enabled, e(cfg.Kaseya.Events))
	} else {
		a("Kaseya VSA", "kaseya", "RMM", false, 0)
	}
	// Asset Management
	if cfg.Device42 != nil {
		a("Device42", "device42", "Asset Management", cfg.Device42.Enabled, e(cfg.Device42.Events))
	} else {
		a("Device42", "device42", "Asset Management", false, 0)
	}
	if cfg.SnipeIT != nil {
		a("Snipe-IT", "snipeit", "Asset Management", cfg.SnipeIT.Enabled, e(cfg.SnipeIT.Events))
	} else {
		a("Snipe-IT", "snipeit", "Asset Management", false, 0)
	}

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
	example.Locale = h.localeFromRequest(r)
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
		Integration IntegrationExample
		Enabled     bool
		EventCount  int
		LiveYAML    string
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
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  slack:\n    enabled: true\n    webhook_url: %q\n    events: %s\n    min_severity: %s", mask(c.WebhookURL), fmtEvents(c.Events), c.MinSeverity)
	case "pagerduty":
		c := cfg.PagerDuty
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  pagerduty:\n    enabled: true\n    routing_key: %q\n    events: %s\n    min_severity: %s\n    auto_resolve: %v", mask(c.RoutingKey), fmtEvents(c.Events), c.MinSeverity, c.AutoResolve)
	case "opsgenie":
		c := cfg.Opsgenie
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  opsgenie:\n    enabled: true\n    api_key: %q\n    events: %s\n    min_severity: %s", mask(c.APIKey), fmtEvents(c.Events), c.MinSeverity)
	case "teams":
		c := cfg.Teams
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  teams:\n    enabled: true\n    webhook_url: %q\n    events: %s\n    min_severity: %s", mask(c.WebhookURL), fmtEvents(c.Events), c.MinSeverity)
	case "connectwise":
		c := cfg.ConnectWise
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  connectwise:\n    enabled: true\n    url: %q\n    company_id: %s\n    public_key: %s\n    private_key: %q\n    board_id: %d\n    events: %s", c.URL, c.CompanyID, c.PublicKey, mask(c.PrivateKey), c.BoardID, fmtEvents(c.Events))
	case "autotask":
		c := cfg.Autotask
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  autotask:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    queue_id: %d\n    events: %s", c.URL, c.Username, mask(c.Password), c.QueueID, fmtEvents(c.Events))
	case "servicenow":
		c := cfg.ServiceNow
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  servicenow:\n    enabled: true\n    instance: %s\n    username: %s\n    password: %q\n    events: %s", c.Instance, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "jira":
		c := cfg.Jira
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  jira:\n    enabled: true\n    url: %q\n    email: %s\n    api_token: %q\n    project: %s\n    issue_type: %s\n    events: %s", c.URL, c.Email, mask(c.APIToken), c.Project, c.IssueType, fmtEvents(c.Events))
	case "zendesk":
		c := cfg.Zendesk
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  zendesk:\n    enabled: true\n    subdomain: %s\n    email: %s\n    api_token: %q\n    events: %s", c.Subdomain, c.Email, mask(c.APIToken), fmtEvents(c.Events))
	case "freshservice":
		c := cfg.Freshservice
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  freshservice:\n    enabled: true\n    domain: %s\n    api_key: %q\n    events: %s", c.Domain, mask(c.APIKey), fmtEvents(c.Events))
	case "linear":
		c := cfg.Linear
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  linear:\n    enabled: true\n    api_key: %q\n    team_id: %s\n    events: %s", mask(c.APIKey), c.TeamID, fmtEvents(c.Events))
	case "github-issues":
		c := cfg.GitHubIssues
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  github_issues:\n    enabled: true\n    owner: %s\n    repo: %s\n    token: %q\n    events: %s", c.Owner, c.Repo, mask(c.Token), fmtEvents(c.Events))
	case "splunk":
		c := cfg.Splunk
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  splunk:\n    enabled: true\n    url: %q\n    token: %q\n    index: %s\n    events: %s", c.URL, mask(c.Token), c.Index, fmtEvents(c.Events))
	case "elastic":
		c := cfg.Elastic
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  elastic:\n    enabled: true\n    url: %q\n    index: %s\n    api_key: %q\n    events: %s", c.URL, c.Index, mask(c.APIKey), fmtEvents(c.Events))
	case "datadog":
		c := cfg.Datadog
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  datadog:\n    enabled: true\n    api_key: %q\n    site: %s\n    events: %s", mask(c.APIKey), c.Site, fmtEvents(c.Events))
	case "sumologic":
		c := cfg.SumoLogic
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  sumologic:\n    enabled: true\n    url: %q\n    events: %s", mask(c.URL), fmtEvents(c.Events))
	case "sentinel":
		c := cfg.Sentinel
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  sentinel:\n    enabled: true\n    workspace_id: %s\n    shared_key: %q\n    events: %s", c.WorkspaceID, mask(c.SharedKey), fmtEvents(c.Events))
	case "loki":
		c := cfg.Loki
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		auth := ""
		if c.BearerToken != "" {
			auth = fmt.Sprintf("\n    bearer_token: %q", mask(c.BearerToken))
		} else if c.Username != "" {
			auth = fmt.Sprintf("\n    username: %s\n    password: %q", c.Username, mask(c.Password))
		}
		tenant := ""
		if c.TenantID != "" {
			tenant = fmt.Sprintf("\n    tenant_id: %s", c.TenantID)
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  loki:\n    enabled: true\n    url: %q%s%s\n    events: %s", c.URL, tenant, auth, fmtEvents(c.Events))
	case "ninjarmm":
		c := cfg.NinjaRMM
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  ninjarmm:\n    enabled: true\n    url: %q\n    client_id: %s\n    client_secret: %q\n    events: %s", c.URL, c.ClientID, mask(c.ClientSecret), fmtEvents(c.Events))
	case "kaseya":
		c := cfg.Kaseya
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  kaseya:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    events: %s", c.URL, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "device42":
		c := cfg.Device42
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  device42:\n    enabled: true\n    url: %q\n    username: %s\n    password: %q\n    events: %s", c.URL, c.Username, mask(c.Password), fmtEvents(c.Events))
	case "snipeit":
		c := cfg.SnipeIT
		if c == nil || !c.Enabled {
			return false, 0, ""
		}
		return true, len(c.Events), fmt.Sprintf("integrations:\n  snipeit:\n    enabled: true\n    url: %q\n    api_token: %q\n    events: %s", c.URL, mask(c.Token), fmtEvents(c.Events))
	}
	return false, 0, ""
}

// IntegrationExample holds display data for an integration detail view.
type IntegrationExample struct {
	Locale      string // per-request locale for {{T}}/{{tEnum}} in the fragment
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
	"loki": {Name: "Grafana Loki", Slug: "loki", Description: "Forwards events as labeled log streams to Loki's push API. Low-cardinality labels (service, severity, event_name); high-cardinality fields in the JSON body. Audit-trail rows arrive as audit.<eventType> events.",
		Events: "All event types", ExampleYAML: "integrations:\n  loki:\n    enabled: true\n    url: \"https://logs-prod-us-central1.grafana.net\"\n    tenant_id: \"123456\"\n    username: \"123456\"\n    password: \"secret:vigo/loki/password\"\n    events: []"},
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

// PlatformControlFramework is the framework pivot rendered on a
// platform-compliance card.
type PlatformControlFramework struct {
	Slug       string
	Label      string
	ControlIDs []string
}

// PlatformControlView is the per-card render shape for /platform/compliance.
type PlatformControlView struct {
	ID          string
	Title       string
	Description string
	Evidence    string
	Frameworks  []PlatformControlFramework
}

// PlatformFrameworkChip is a filter-bar entry on /platform/compliance.
type PlatformFrameworkChip struct {
	Slug  string
	Label string
}

// PlatformCompliancePageData backs the /platform/compliance template.
type PlatformCompliancePageData struct {
	PageData
	Controls        []PlatformControlView
	Frameworks      []PlatformFrameworkChip
	ActiveFramework string
}

// PlatformCompliance renders the read-only product-level compliance
// posture page — what the Vigo server and agent satisfy by construction,
// independent of any envoy's module assignment. Source of truth:
// server/compliance.AppControls.
func (h *Handler) PlatformCompliance(w http.ResponseWriter, r *http.Request) {
	active := r.URL.Query().Get("framework")

	// Collect every framework slug referenced from any AppControl so the
	// chip row only shows frameworks that actually have content.
	chipSeen := make(map[string]bool)
	var chips []PlatformFrameworkChip
	for _, c := range compliance.AppControls {
		for _, fw := range c.Frameworks {
			if chipSeen[fw.Slug] {
				continue
			}
			chipSeen[fw.Slug] = true
			chips = append(chips, PlatformFrameworkChip{
				Slug:  fw.Slug,
				Label: compliance.FrameworkLabel(fw.Slug),
			})
		}
	}
	sort.Slice(chips, func(i, j int) bool { return chips[i].Label < chips[j].Label })

	// Build the cards. If a framework filter is active, drop controls that
	// don't reference that framework.
	var controls []PlatformControlView
	for _, c := range compliance.AppControls {
		if active != "" {
			match := false
			for _, fw := range c.Frameworks {
				if fw.Slug == active {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		view := PlatformControlView{
			ID:          c.ID,
			Title:       c.Title,
			Description: c.Description,
			Evidence:    c.Evidence,
		}
		for _, fw := range c.Frameworks {
			view.Frameworks = append(view.Frameworks, PlatformControlFramework{
				Slug:       fw.Slug,
				Label:      compliance.FrameworkLabel(fw.Slug),
				ControlIDs: fw.ControlIDs,
			})
		}
		controls = append(controls, view)
	}

	data := PlatformCompliancePageData{
		PageData:        PageData{Title: "Platform Compliance", ActiveNav: "platform-compliance"},
		Controls:        controls,
		Frameworks:      chips,
		ActiveFramework: active,
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "platform-compliance", data)
}

// ComplianceStatus renders the standards compliance gauge/breakdown fragment for HTMX polling.
func (h *Handler) ComplianceStatus(w http.ResponseWriter, r *http.Request) {
	data := h.standardsData()
	data.Locale = h.localeFromRequest(r)
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

	// Compute waiver alerts from the config-file waivers (waivers.vgo) —
	// the single source the unified scorer uses, so the badge count
	// matches the rows shown on /compliance/waivers.
	soonThreshold := time.Now().AddDate(0, 0, 30)
	if h.configStore != nil {
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

// computeStandardsSummary reads the most-recent per-framework compliance
// snapshot from `compliance_history` and aggregates it into the six
// dashboard/`/compliance` headline numbers. The snapshot is written by
// ReportHandler.PersistComplianceSnapshot — daily plus on every successful
// config publish — through the same unified scorer the audit report
// uses, so dashboard numbers stay consistent with /compliance/executive.
// Off the request hot path: a SQL read + tiny aggregation, no live walk
// of frameworks × envoys × controls.
func (h *Handler) computeStandardsSummary() standardsSummary {
	if h.queries == nil {
		return standardsSummary{}
	}
	ctx := context.Background()
	snapshotDate, err := h.queries.GetLatestComplianceSnapshotDate(ctx)
	if err != nil {
		// sql.ErrNoRows on a fresh server before the first snapshot lands;
		// callers render an empty-state. Any other error is logged and
		// also returns empty so the page still renders.
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("standards summary: latest snapshot date query failed", "error", err)
		}
		return standardsSummary{}
	}
	rows, err := h.queries.ListComplianceSnapshotByDate(ctx, snapshotDate)
	if err != nil {
		slog.Warn("standards summary: snapshot rows query failed", "error", err, "date", snapshotDate)
		return standardsSummary{}
	}
	return aggregateStandardsSnapshot(rows)
}

// aggregateStandardsSnapshot folds the per-framework rows of a single
// compliance_history snapshot date into the six headline counts shown
// on the dashboard / `/compliance`. Pure function — extracted so the
// unweighted-mean and no-waiver-double-count invariants can be pinned
// without standing up a real database.
//
// Invariants enforced (and tested):
//   - overall percent is the unweighted mean of per-framework percents,
//     not a control-count-weighted average
//   - per-framework buckets are disjoint, so waivers never inflate Total
//     beyond the framework's real control count
//   - frameworks with zero controls (disabled / no data) are skipped
//     entirely — they don't drag the mean toward zero
func aggregateStandardsSnapshot(rows []db.ListComplianceSnapshotByDateRow) standardsSummary {
	var s standardsSummary
	var pctSum float64
	pctCount := 0
	for _, fw := range rows {
		fwTotal := int(fw.Satisfied + fw.Waived + fw.Partial + fw.NotSatisfied)
		if fwTotal == 0 {
			continue
		}
		s.Satisfied += int(fw.Satisfied)
		s.Waived += int(fw.Waived)
		s.Partial += int(fw.Partial)
		s.NotSatisfied += int(fw.NotSatisfied)
		s.Total += fwTotal
		pctSum += fw.ScorePercent
		pctCount++
	}
	if pctCount > 0 {
		s.Pct = math.Round(pctSum/float64(pctCount)*10) / 10
	}
	if s.Pct < 0 || s.Pct > 100 {
		slog.Warn("standards summary percent outside [0,100] — aggregator invariant broken",
			"percent", s.Pct, "satisfied", s.Satisfied, "waived", s.Waived,
			"partial", s.Partial, "not_satisfied", s.NotSatisfied, "total", s.Total)
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

	// Collect config-file waivers (waivers.vgo) into a
	// (standardKey → controlID → entry) map — the single waiver source.
	type mergedEntry struct {
		reason     string
		approvedBy string
		expires    string
	}
	merged := make(map[string]map[string]mergedEntry) // standardKey → controlID → entry

	if h.configStore != nil {
		for stdKey, controls := range h.configStore.AllWaivers() {
			if _, ok := merged[stdKey]; !ok {
				merged[stdKey] = make(map[string]mergedEntry)
			}
			for controlID, w := range controls {
				merged[stdKey][controlID] = mergedEntry{
					reason:     w.Reason,
					approvedBy: w.ApprovedBy,
					expires:    w.Expires,
				}
			}
		}
	}

	if h.configStore != nil {
		// Build set of all covered controls per standard across fleet.
		coveredControls := make(map[string]map[string]bool) // standard → controlID → true

		for stdKey := range merged {
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

		for stdKey, controls := range merged {
			displayName := standardDisplayName(stdKey)
			covered := coveredControls[stdKey]
			for controlID, entry := range controls {
				row := waiverRow{
					Framework:    displayName,
					FrameworkKey: stdKey,
					Control:      controlID,
					Reason:       entry.reason,
					ApprovedBy:   entry.approvedBy,
					Expires:      entry.expires,
					Enforced:     covered[controlID],
				}
				if entry.expires != "" {
					if exp, err := time.Parse("2006-01-02", entry.expires); err == nil && exp.Before(soonThreshold) {
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
	data := h.dataSource.RiskPostureData(h.baseInterval, h.offlineThreshold)
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

	// Map URL slugs to display names and API slugs (registry-derived; see
	// complianceReportTitles).
	frameworks := complianceReportTitles()

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
	Locale         string // per-request locale for {{T}}/{{tEnum}} in the fragment
	SnapshotDate   string
	ConvergencePct float64
	Converged      int64
	Degraded       int64
	Failed         int64
	NoData         int64
	Changed        int64 // drift axis
	Diverged       int64 // drift axis: persistent
	Offline        int64
}

// EvidenceMTTRData holds data for the MTTR metrics fragment.
type EvidenceMTTRData struct {
	Locale         string // per-request locale for {{T}}/{{tEnum}} in the fragment
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
	Controls       []struct {
		ControlID string
		Title     string
		Status    string
		Evidence  string
	}
}

// EvidenceControlDrillData holds data for the per-control drill-down fragment.
type EvidenceControlDrillData struct {
	Locale    string // per-request locale for {{T}}/{{tEnum}} in the fragment
	Slug      string
	ControlID string
	Envoys    []EvidenceControlEnvoy
	Docs      []EvidenceControlDoc
}

// EvidenceControlDoc is one operator-uploaded compliance document
// linked to (Slug, ControlID). Surfaced under the drill-down so the
// auditor or operator can see which artifacts back the control.
type EvidenceControlDoc struct {
	ID         string
	Filename   string
	DocType    string
	MimeType   string
	SizeBytes  int64
	SHA256     string
	UploadedBy string
	UploadedAt string
}

// EvidenceControlEnvoy is a single envoy's enforcement evidence for a control.
type EvidenceControlEnvoy struct {
	ID            string
	Hostname      string
	Configcrate   string
	Status        string
	FailurePct    float64
	FailedCount   int64
	ResourceCount int64
	RunCount      int
	FailureCount  int
	LastRun       string
}

// evidenceFrameworks returns the list of framework options for the dropdown.
// Derived from the framework registry (server/frameworks), the single source
// of truth, plus the "CIS Benchmarks" aggregation — a roll-up across the
// three CIS benchmarks with no registry entry of its own.
func evidenceFrameworks() []EvidenceFrameworkOption {
	opts := make([]EvidenceFrameworkOption, 0, len(fwreg.All)+1)
	for _, f := range fwreg.All {
		opts = append(opts, EvidenceFrameworkOption{f.ShortName, f.Slug})
	}
	opts = append(opts, EvidenceFrameworkOption{"CIS Benchmarks", "cis"})
	return opts
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
		summary := h.dataSource.ConvergenceData(h.baseInterval, h.offlineThreshold)
		// total here already excludes Offline (it sums the failure-axis
		// statuses), so it is the active denominator. Route through the helper.
		total := summary.Converged + summary.Degraded + summary.Failed + summary.NoData
		pct := convergence.ConvergedPct(int64(summary.Converged), int64(total))
		data := EvidenceSnapshotData{
			SnapshotDate:   dateStr + " (live)",
			ConvergencePct: pct,
			Converged:      summary.Converged,
			Degraded:       summary.Degraded,
			Failed:         summary.Failed,
			NoData:         summary.NoData,
			Changed:        summary.Changed,
			Diverged:       summary.Diverged,
			Offline:        summary.Offline,
		}
		data.Locale = h.localeFromRequest(r)
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
		ConvergencePct: pct,
		Converged:      snapshot.Converged,
		Degraded:       snapshot.Degraded,
		Failed:         snapshot.Failed,
		NoData:         snapshot.NoData,
		Changed:        snapshot.Changed,
		Diverged:       snapshot.Diverged,
		Offline:        snapshot.Offline,
	}
	data.Locale = h.localeFromRequest(r)
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

	data.Locale = h.localeFromRequest(r)
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
	_ = json.Unmarshal(raw, &parsed) // malformed input yields empty controls

	// Load active waivers from the config-file source (waivers.vgo).
	type waiverInfo struct {
		Reason string
		By     string
		Expiry string
	}
	waiverMap := make(map[string]waiverInfo)
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

	type controlWithWaiver struct {
		ControlID    string
		Title        string
		Status       string
		Evidence     string
		HasWaiver    bool
		WaiverReason string
		WaiverBy     string
		WaiverExpiry string
	}

	data := struct {
		Locale         string
		Slug           string
		FrameworkTitle string
		Controls       []controlWithWaiver
		// Waiver summary
		TotalWaivers   int
		EnforcedWaived int // controls that are both satisfied and waived
		WaivedOnly     int // controls waived but not enforced
		ExpiringSoon   int // waivers expiring within 30 days
	}{
		Locale:         h.localeFromRequest(r),
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
		h.renderer.RenderFragment(w, "compliance-evidence", "evidence-control-drill", EvidenceControlDrillData{Locale: h.localeFromRequest(r)})
		return
	}

	// Find envoys whose modules cover this control via compliance tags.
	// Use fleet cache for the full envoy list (no 100-envoy cap) and
	// avoid per-envoy run queries — the fleet cache already has status.
	type envoyConfigcrate struct {
		envoyID     string
		hostname    string
		configcrate string
		status      string
		failurePct  float64
		failedCount int64
		moduleCount int64
	}
	var matches []envoyConfigcrate

	// Get all envoys from fleet cache or fall back to DataSource.
	type envoyInfo struct {
		ID            string
		Hostname      string
		Status        string
		FailurePct    float64
		FailedCount   int64
		ResourceCount int64
	}
	var allEnvoys []envoyInfo
	for _, e := range h.dataSource.EnvoysData(h.baseInterval, h.offlineThreshold, "", "", "", "", "", "", "", "", "", "", "", "", "", 0, 0).Envoys {
		allEnvoys = append(allEnvoys, envoyInfo{ID: e.ID, Hostname: e.Hostname, Status: e.Status, FailurePct: e.FailurePct, FailedCount: e.FailedCount, ResourceCount: e.ResourceCount})
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
						matches = append(matches, envoyConfigcrate{
							envoyID:     t.ID,
							hostname:    t.Hostname,
							configcrate: ct.Configcrate,
							status:      t.Status,
							failurePct:  t.FailurePct,
							failedCount: t.FailedCount,
							moduleCount: t.ResourceCount,
						})
					}
				}
			}
		}
	}

	var envoys []EvidenceControlEnvoy
	for _, m := range matches {
		envoys = append(envoys, EvidenceControlEnvoy{
			ID:            m.envoyID,
			Hostname:      m.hostname,
			Configcrate:   m.configcrate,
			Status:        m.status,
			FailurePct:    m.failurePct,
			FailedCount:   m.failedCount,
			ResourceCount: m.moduleCount,
		})
	}

	data := EvidenceControlDrillData{Locale: h.localeFromRequest(r), Slug: slug, ControlID: controlID, Envoys: envoys}

	// Resolve the compliance tag key for the URL slug so doc lookups
	// match how compliance.vgo and module tags reference frameworks
	// (e.g., URL slug "compliance" → standard key "hipaa").
	if h.complianceDocs != nil {
		standardKey := slug
		if mapped, ok := config.StandardKeyForSlug[slug]; ok {
			standardKey = mapped
		}
		if items, err := h.complianceDocs.DocsForControl(r.Context(), standardKey, controlID); err == nil {
			data.Docs = make([]EvidenceControlDoc, 0, len(items))
			for _, it := range items {
				data.Docs = append(data.Docs, EvidenceControlDoc(it))
			}
		}
	}

	h.renderer.RenderFragment(w, "compliance-evidence", "evidence-control-drill", data)
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
	data := struct {
		Valid      bool
		EntryCount int64
		Error      string
	}{}
	// Ambient badge: read the report layer's cached status (no O(N) walk on
	// load). Fall back to a direct verify only when no report builder is
	// wired (bare-handler tests).
	if h.reportBuilder != nil && h.reportBuilder.AuditStatus != nil {
		s := h.reportBuilder.AuditStatus(r)
		data.Valid, data.EntryCount, data.Error = s.Valid, s.EntryCount, s.Error
	} else if result, err := audit.VerifyFull(r.Context(), h.queries); err == nil {
		data.Valid, data.EntryCount, data.Error = result.Valid, result.EntryCount, result.Error
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
	if result, err := audit.VerifyFull(r.Context(), h.queries); err == nil {
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
			fmt.Fprintln(f, "date,total,converged,degraded,failed,no_data,changed,diverged,offline")
			for _, h := range history {
				fmt.Fprintf(f, "%s,%d,%d,%d,%d,%d,%d,%d,%d\n",
					h.RecordedAt, h.Total, h.Converged, h.Degraded, h.Failed, h.NoData, h.Changed, h.Diverged, h.Offline)
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
	envoyData := h.dataSource.EnvoysData(h.baseInterval, h.offlineThreshold, "", "", "", "", "", "", "", "", "", "", "", "", "", 0, 100000)
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

// envoysPageLimit is the page size for the envoys list "load more"
// trigger. envoysRenderedCap is the hard cap on visible rows — past
// this, growth is taxed without value, and the operator should refine
// the filter instead. See SME-#1 scale-resilience checklist.
const (
	envoysPageLimit   = 100
	envoysRenderedCap = 1000
)

// Envoys renders the envoys list page with optional axis filters
// (?conv=, ?drift=, ?reach=) and pagination.
func (h *Handler) Envoys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	convFilter := q.Get("conv")
	driftFilter := q.Get("drift")
	reachFilter := q.Get("reach")
	search := q.Get("search")
	envFilter := q.Get("env")
	tagFilter := q.Get("tag")
	roleFilter := q.Get("role")
	osFilter := q.Get("os")
	archFilter := q.Get("arch")
	configcrateFilter := q.Get("configcrate")
	complianceFilter := q.Get("compliance")
	sortCol, sortDir := normalizeEnvoySort(q.Get("sort"), q.Get("dir"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	data := h.envoysPageData(convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter, sortCol, sortDir, offset)
	h.enrichEnvoyRows(data.Envoys)

	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "envoys", data)
}

// normalizeEnvoySort canonicalizes the sort URL params. Empty / unknown
// columns default to "status" — the triage-first lens that surfaces the
// rows the operator actually came to look at on a 10k-envoy fleet.
// Direction defaults: "status" / "ip" / "hostname" → asc; temporal
// columns → desc (newest-first).
func normalizeEnvoySort(col, dir string) (string, string) {
	switch col {
	case "status", "hostname", "ip", "last_seen", "last_run":
	default:
		col = "status"
	}
	if dir != "asc" && dir != "desc" {
		switch col {
		case "last_run", "last_seen":
			dir = "desc"
		default:
			dir = "asc"
		}
	}
	return col, dir
}

// envoysPageData builds EnvoysPageData from the data source — the
// single source of truth for both the rows in the table AND the counts
// in the pills above it. The hard render cap (envoysRenderedCap) is
// applied here, handler-side: the request limit is clamped before the
// data-source call and the load-more sentinel is set after.
func (h *Handler) envoysPageData(convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter, sortCol, sortDir string, offset int) EnvoysPageData {
	// Cap the request at the hard render limit. Past envoysRenderedCap
	// rows we stop emitting load-more triggers and surface a sentinel.
	limit := envoysPageLimit
	if offset+limit > envoysRenderedCap {
		limit = envoysRenderedCap - offset
		if limit < 0 {
			limit = 0
		}
	}

	data := h.dataSource.EnvoysData(h.baseInterval, h.offlineThreshold, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, configcrateFilter, complianceFilter, sortCol, sortDir, offset, limit)

	// Render-cap sentinel: when paging reaches the hard DOM cap, the
	// footer renders a "refine your filter" notice instead of another
	// load-more trigger so the DOM cannot grow unbounded.
	nextOffset := offset + len(data.Envoys)
	renderedCap := nextOffset >= envoysRenderedCap && nextOffset < data.MatchingTotal
	data.HasMore = data.HasMore && !renderedCap
	data.Limit = envoysPageLimit
	data.RenderedCap = renderedCap
	data.RenderedCapMax = envoysRenderedCap
	return data
}

// EnvoyDetail renders the envoy detail page shell. Runs and traits are loaded
// asynchronously via HTMX fragment endpoints for instant page rendering.
func (h *Handler) EnvoyDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	data, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
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

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
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
	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-runs-fragment", data)
}

// EnvoyTraitsFragment renders the traits tab on the envoy-detail page.
//
// Two modes, selected by `?q=`:
//
//   - No q: renders the full tab body — stats stat-cards, filter toolbar,
//     and the trait tree as top-level group shells. Each shell lazy-fetches
//     its children via EnvoyTraitGroupFragment on first expand. This keeps
//     the initial payload tiny (~tens of KB even for envoys with thousands
//     of leaves) so the browser doesn't choke on DOM construction.
//   - q=<query>: renders just a flat list of matching leaves inside the
//     #traits-tree wrapper. Substring match on the dotted path. Searches
//     the WHOLE tree, regardless of which groups are expanded, so a filter
//     can't miss leaves inside un-expanded groups.
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
	data.EnvoyID = idStr
	data.Locale = h.localeFromRequest(r)
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		h.renderer.RenderFragment(w, "envoy-detail", "envoy-traits-filter-fragment", EnvoyTraitsFilterFragmentData{
			Locale:  h.localeFromRequest(r),
			Query:   q,
			Matches: flattenMatchingLeaves(data.Tree, q),
			Total:   data.LeafCount,
		})
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-traits-fragment", data)
}

// EnvoyTraitGroupFragment renders the children of one trait group, used
// by the lazy `<details>` shells in the traits tab. The `path` query
// parameter is a dotted path into the tree (e.g. `os.kernel`); the
// handler walks to that node and renders its Children via the existing
// `trait-node` template. Invalid path → 404 so a stale URL doesn't
// silently return an empty body.
func (h *Handler) EnvoyTraitGroupFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	data, err := h.dataSource.EnvoyTraitsData(idStr)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	node, ok := findTraitNode(data.Tree, path)
	if !ok || len(node.Children) == 0 {
		http.Error(w, "trait group not found", http.StatusNotFound)
		return
	}
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-trait-group-fragment", EnvoyTraitGroupFragmentData{
		Locale:   h.localeFromRequest(r),
		EnvoyID:  idStr,
		Path:     path,
		Children: node.Children,
	})
}

// findTraitNode walks a trait tree to the node at `path` (dotted notation
// like `os.kernel`). Returns the node and true if found, or an empty node
// and false. Linear walk per segment — fine for typical trees; the deepest
// real trees observed are ~5 levels.
func findTraitNode(tree []TraitNode, path string) (TraitNode, bool) {
	if path == "" {
		return TraitNode{}, false
	}
	segments := strings.Split(path, ".")
	nodes := tree
	for i, seg := range segments {
		var found *TraitNode
		for j := range nodes {
			if nodes[j].Key == seg {
				found = &nodes[j]
				break
			}
		}
		if found == nil {
			return TraitNode{}, false
		}
		if i == len(segments)-1 {
			return *found, true
		}
		nodes = found.Children
	}
	return TraitNode{}, false
}

// flattenMatchingLeaves walks the tree and returns every leaf whose Path
// contains the query as a case-insensitive substring. Used by the filter
// endpoint so a search hits leaves regardless of which groups the
// operator has expanded.
func flattenMatchingLeaves(tree []TraitNode, query string) []TraitNode {
	q := strings.ToLower(query)
	var matches []TraitNode
	var walk func(nodes []TraitNode)
	walk = func(nodes []TraitNode) {
		for _, n := range nodes {
			if len(n.Children) > 0 {
				walk(n.Children)
				continue
			}
			if strings.Contains(strings.ToLower(n.Path), q) {
				matches = append(matches, n)
			}
		}
	}
	walk(tree)
	return matches
}

// EnvoyConfigFragment renders the config YAML inline for an envoy.
func (h *Handler) EnvoyConfigFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}
	cfgData, err := h.dataSource.ConfigData(detail.Envoy.Hostname)
	if err != nil {
		errData := ConfigPageData{
			Hostname: detail.Envoy.Hostname,
		}
		errData.Locale = h.localeFromRequest(r)
		h.renderer.RenderFragment(w, "envoy-detail", "envoy-config-fragment", errData)
		return
	}
	cfgData.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-config-fragment", cfgData)
}

// EnvoyTraceFragment renders the config trace inline for an envoy.
func (h *Handler) EnvoyTraceFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
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
	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "envoy-trace", "envoy-trace-fragment", data)
}

// EnvoyComplianceData holds data for the per-envoy compliance tab.
type EnvoyComplianceData struct {
	Locale     string // per-request locale for {{T}}/{{tEnum}} in the fragment
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
	ControlID               string
	RecommendedConfigcrates []string
}

// EnvoyStandardCoverageData holds data for the per-envoy standard coverage fragment.
// Status and Controls come from the unified scorer so the drilldown's header
// numbers match the Standards tab card that linked to it; Covered +
// Waived + Partial + Missing = Total by construction.
type EnvoyStandardCoverageData struct {
	Locale                 string // per-request locale for {{T}}/{{tEnum}} in the fragment
	EnvoyID                string
	Hostname               string
	FrameworkTitle         string
	Status                 EnvoyFrameworkStatus
	Controls               []ControlStatusRow
	ConfigcrateConvergence string  // envoy convergence status (converged, failed, etc.)
	ConfigcratePct         float64 // module convergence percentage (100 = all modules converged)
	Unavailable            bool    // true when no findings could be produced (framework disabled, slug unknown, etc.)
}

// AttackSurfaceData holds data for the envoy attack surface tab.
//
// RiskScore / RiskLevel / Factors come from [risk.ScoreEnvoy], the same
// canonical scorer that powers /risk and /api/v1/risk/{envoyID}. Display
// counts (CVECritical etc.) come from the same flat trait map the scorer
// reads, so the headline numbers and the underlying factor breakdown
// always tell the same story. Each headline stat and factor row carries
// a drill-down URL pointing at the page where an operator can see the
// supporting evidence — fleet-wide /risk and /security panels for the
// CVE / hardening / rootkit / malware / cert factors, /envoys/{id}/trace
// for convergence factors.
type AttackSurfaceData struct {
	Locale          string // per-request locale for {{T}}/{{tEnum}} in the fragment
	Hostname        string
	EnvoyID         string
	RiskLevel       string
	RiskScore       int
	RiskURL         string
	Factors         []AttackFactor
	CVECritical     int
	CVECriticalURL  string
	CVEHigh         int
	CVEHighURL      string
	CVEMedium       int
	CVEMediumURL    string
	CVELow          int
	CVELowURL       string
	HardeningScore  int
	HardeningURL    string
	ListeningPorts  int
	Ports           []string
	Vulnerabilities bool
	HasData         bool
}

// AttackFactor wraps a [risk.Factor] with a drill-down URL pointing at
// the fleet-wide page that lists the supporting evidence for the factor
// name (e.g. Critical CVEs → /security#sec-panel-high). URL is
// empty for factor types with no canonical drill-down today (Offline,
// Open Ports — the on-page Listening Ports table is the destination).
type AttackFactor struct {
	risk.Factor
	URL string
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
		fmt.Fprintf(w, `<p class="text-muted u-p-20px">No remote access sessions recorded for this envoy.</p>`)
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
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	// Build the flat trait map, then route Score/Level/Factors through
	// risk.ScoreEnvoy — the same canonical scorer that drives /risk and
	// /api/v1/risk/{envoyID}. The two surfaces must never disagree on a
	// single envoy's risk numbers; any divergence is a SoT bug.
	flat := buildFlatTraits(r.Context(), h.queries, idStr)

	revoked := false
	if h.queries != nil {
		if envoy, qErr := h.queries.GetEnvoyByID(r.Context(), idStr); qErr == nil {
			revoked = envoy.Revoked != 0
		}
	}

	var observedCycle time.Duration
	if _, oc, ok := h.dataSource.EnvoyFreshness(idStr); ok {
		observedCycle = oc
	}
	er := risk.ScoreEnvoy(risk.EnvoyInput{
		ID:                idStr,
		Hostname:          detail.Envoy.Hostname,
		Revoked:           revoked,
		LastSeen:          detail.Envoy.LastSeen,
		ConvergenceStatus: detail.Envoy.Status,
		Traits:            flat,
		ObservedCycle:     observedCycle,
	}, h.baseInterval, h.offlineThreshold)

	data := AttackSurfaceData{
		Hostname:       detail.Envoy.Hostname,
		EnvoyID:        idStr,
		RiskScore:      er.Score,
		RiskLevel:      string(er.Level),
		RiskURL:        "/risk?level=" + string(er.Level),
		Factors:        attackFactorsWithLinks(er.Factors, idStr),
		CVECriticalURL: "/security#sec-panel-high",
		CVEHighURL:     "/security#sec-panel-high",
		// /security only surfaces dedicated panels for critical and
		// high CVEs — medium/low aren't broken out, so we leave the
		// drill-down URL empty for those and the template renders a
		// plain count.
		HardeningURL: "/security#sec-panel-lynis",
	}

	// Headline counts read the same deduped trait keys the scorer reads,
	// so the per-severity badges agree with the points contributed by
	// the "Critical CVEs" / "High CVEs" / "Medium CVEs" risk factors.
	data.CVECritical = atoiTrait(flat, traits.UniqueCritical)
	data.CVEHigh = atoiTrait(flat, traits.UniqueHigh)
	data.CVEMedium = atoiTrait(flat, traits.UniqueMedium)
	data.CVELow = atoiTrait(flat, traits.UniqueLow)
	data.HardeningScore = atoiTrait(flat, traits.LynisScore)

	for k, v := range flat {
		if strings.HasPrefix(k, "ports.listening.") {
			data.Ports = append(data.Ports, v)
		}
	}
	sort.Slice(data.Ports, func(i, j int) bool {
		// Numeric sort so 22, 80, 443, 8080 read in order rather than
		// lexicographically (which would put 8080 before 80).
		ai, _ := strconv.Atoi(data.Ports[i])
		bi, _ := strconv.Atoi(data.Ports[j])
		return ai < bi
	})
	data.ListeningPorts = len(data.Ports)

	data.Vulnerabilities = data.CVECritical+data.CVEHigh+data.CVEMedium+data.CVELow > 0
	data.HasData = data.RiskScore > 0 || data.Vulnerabilities || data.HardeningScore > 0 || len(data.Ports) > 0

	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "envoy-detail", "envoy-attack-surface-fragment", data)
}

// buildFlatTraits loads the latest trait snapshot for an envoy and
// flattens it into the map[string]string shape risk.ScoreEnvoy consumes.
// Returns an empty (non-nil) map on any error so callers can index it
// without nil-checks.
func buildFlatTraits(ctx context.Context, q *db.Queries, envoyID string) map[string]string {
	out := make(map[string]string)
	if q == nil {
		return out
	}
	cf, err := q.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(cf.Traits), &raw); err != nil {
		return out
	}
	tmp := make(map[string]any)
	flattenTraits("", raw, tmp)
	for k, v := range tmp {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

func atoiTrait(traits map[string]string, key string) int {
	if v, ok := traits[key]; ok {
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// attackFactorsWithLinks decorates each [risk.Factor] with the URL where
// an operator can drill into the supporting evidence. Factor names are
// authored in server/risk/score.go — keep this map in sync when new
// factor types are added there. Empty URL means "no canonical
// drill-down" and the template renders the factor name as plain text.
func attackFactorsWithLinks(factors []risk.Factor, envoyID string) []AttackFactor {
	out := make([]AttackFactor, len(factors))
	for i, f := range factors {
		out[i] = AttackFactor{Factor: f, URL: factorDrillURL(f.Name, envoyID)}
	}
	return out
}

func factorDrillURL(name, envoyID string) string {
	switch name {
	case "Critical CVEs":
		return "/security#sec-panel-high"
	case "High CVEs":
		return "/security#sec-panel-high"
	case "Medium CVEs":
		// /security has no dedicated medium panel; send operators to
		// the top of the page for fleet-wide context.
		return "/security"
	case "Hardening Gap":
		return "/security#sec-panel-lynis"
	case "Rootkit (chkrootkit)", "Rootkit (rkhunter)":
		return "/security#sec-panel-rootkit"
	case "Malware (ClamAV)":
		return "/security#sec-panel-malware"
	case "Integrity Breach (AIDE)":
		return "/security#sec-panel-integrity"
	case "Expiring Certificates":
		return "/security/certs/" + envoyID
	case "Running CVEs":
		return "/security#sec-panel-high"
	case "Degraded", "Diverged", "Convergence Failed":
		return "/envoys/" + envoyID + "/trace"
	}
	// Open Ports, Offline — no canonical drill-down, on-page detail
	// (Listening Ports table, Last Seen badge) is the destination.
	return ""
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
	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
	if err != nil {
		http.Error(w, "envoy not found", http.StatusNotFound)
		return
	}

	data := EnvoyComplianceData{
		Locale:   h.localeFromRequest(r),
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

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
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
// Routes through ReportBuilder.EnvoyFrameworkDetail so the header numbers
// match the Standards tab card that linked here (same partition math).
func (h *Handler) EnvoyStandardCoverageFragment(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	fw := r.PathValue("framework")

	if _, err := uuid.Parse(idStr); err != nil {
		http.Error(w, "invalid envoy ID", http.StatusBadRequest)
		return
	}

	detail, err := h.dataSource.EnvoyDetailData(idStr, h.baseInterval, h.offlineThreshold)
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

	// Module convergence: 100% if converged, scales down with the
	// failure-axis bucket. Drift no longer enters this number — an
	// envoy whose modules keep changing but pass cleanly is at 100%
	// here, and drift is surfaced separately as a pill.
	modPct := float64(0)
	switch convergence.ClassifyStatus(detail.Envoy.Status) {
	case convergence.StatusConverged:
		modPct = 100
	case convergence.StatusDegraded:
		modPct = 50
	case convergence.StatusFailed:
		modPct = 25
	}

	data := EnvoyStandardCoverageData{
		Locale:                 h.localeFromRequest(r),
		EnvoyID:                idStr,
		Hostname:               detail.Envoy.Hostname,
		FrameworkTitle:         standardDisplayNameFromSlug(fw),
		ConfigcrateConvergence: detail.Envoy.Status,
		ConfigcratePct:         modPct,
	}

	if h.reportBuilder != nil && h.reportBuilder.EnvoyFrameworkDetail != nil {
		if d, ok := h.reportBuilder.EnvoyFrameworkDetail(r, detail.Envoy.Hostname, fw); ok {
			data.Status = d.Status
			data.Controls = d.Controls
		} else {
			data.Unavailable = true
		}
	} else {
		data.Unavailable = true
	}

	h.renderer.RenderFragment(w, "envoy-compliance-detail", "envoy-standard-coverage-fragment", data)
}

// standardDisplayName maps compliance tag keys to display names.
func standardDisplayName(key string) string {
	names := map[string]string{
		"hipaa":             "HIPAA",
		"hitrust":           "HITRUST CSF",
		"soc2":              "SOC 2",
		"pci-dss":           "PCI DSS v4.0",
		"cis":               "CIS Benchmarks",
		"nist-800-53":       "NIST 800-53",
		"iso-27001":         "ISO 27001",
		"gdpr":              "GDPR",
		"nerc-cip":          "NERC CIP",
		"cyber-essentials":  "Cyber Essentials+",
		"iec-62443":         "IEC 62443",
		"sox":               "SOX",
		"finra":             "FINRA",
		"mifid2":            "MiFID II",
		"ccss":              "CCSS",
		"fda-21cfr11":       "FDA 21 CFR Part 11",
		"gaming-ukgc":       "UKGC",
		"gaming-nevada-gcb": "Nevada GCB",
		"gaming-mga":        "MGA",
		"cis-windows":       "CIS Windows",
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
	data.Locale = h.localeFromRequest(r)
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

// requireAuditRead blocks users without audit.read with a 403 and
// renders nothing. The raw hash-chained audit log is admin/viewer-only
// (the compliance role is denied audit.read); the nav hides the link
// but the handler must gate it too. Mirrors the REST audit handler,
// which gates on auth.PermAuditRead. Returns true when the handler
// should continue; false when it should return.
func (h *Handler) requireAuditRead(w http.ResponseWriter, r *http.Request) bool {
	if !h.authEnabled {
		return true
	}
	u := auth.UserFromContext(r.Context())
	if u == nil || !u.HasPermission(auth.PermAuditRead) {
		http.Error(w, "audit.read permission required", http.StatusForbidden)
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

// EnvoysTable renders the envoys table fragment. Used for cursor-based
// pagination (the `revealed` sentinel that loads the next page) — no
// longer auto-polled, since polling at the table boundary wipes any
// rows the user has already scrolled into.
func (h *Handler) EnvoysTable(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortCol, sortDir := normalizeEnvoySort(q.Get("sort"), q.Get("dir"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	data := h.envoysPageData(
		q.Get("conv"), q.Get("drift"), q.Get("reach"), q.Get("search"),
		q.Get("env"), q.Get("tag"), q.Get("role"), q.Get("os"),
		q.Get("arch"), q.Get("configcrate"), q.Get("compliance"),
		sortCol, sortDir, offset,
	)
	h.enrichEnvoyRows(data.Envoys)
	data.Locale = h.localeFromRequest(r)
	h.renderer.RenderFragment(w, "envoys", "envoys-table", data)
}

// EnvoysStats renders the live stat-cards (Total + Reachable) plus the
// filter-pill counts. Polled at the page's standard interval so all the
// fleet-wide totals stay accurate; the search/sort toolbar and the
// table itself are outside this region so the operator's typed query
// and scrolled position survive every refresh tick.
func (h *Handler) EnvoysStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortCol, sortDir := normalizeEnvoySort(q.Get("sort"), q.Get("dir"))
	data := h.envoysPageData(
		q.Get("conv"), q.Get("drift"), q.Get("reach"), q.Get("search"),
		q.Get("env"), q.Get("tag"), q.Get("role"), q.Get("os"),
		q.Get("arch"), q.Get("configcrate"), q.Get("compliance"),
		sortCol, sortDir, 0,
	)
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.RenderFragment(w, "envoys", "envoys-stats", data)
}

// BuildTraitsFragment turns a raw traits JSON payload into the data shape the
// envoy-traits-fragment template renders. Returns a zero-value fragment with
// HasData=false when the JSON is empty or malformed so the empty state can
// distinguish "no check-in" from "checked-in but no payload."
func BuildTraitsFragment(rawJSON string, collectedAt time.Time) EnvoyTraitsFragmentData {
	if strings.TrimSpace(rawJSON) == "" || rawJSON == "{}" {
		return EnvoyTraitsFragmentData{CollectedAt: collectedAt}
	}
	var nested map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &nested); err != nil || len(nested) == 0 {
		return EnvoyTraitsFragmentData{CollectedAt: collectedAt}
	}
	tree, leafCount := buildTraitTree("", nested)
	pretty := rawJSON
	if buf, err := json.MarshalIndent(nested, "", "  "); err == nil {
		pretty = string(buf)
	}
	return EnvoyTraitsFragmentData{
		Tree:        tree,
		LeafCount:   leafCount,
		CollectedAt: collectedAt,
		Summary:     extractTraitSummary(nested),
		RawJSON:     pretty,
		HasData:     true,
	}
}

// buildTraitTree walks a nested traits map and returns its sorted, render-
// ready tree along with the total leaf count under it. Map keys are sorted
// alphabetically; array indices preserve insertion order. Each node carries
// its full dotted Path so the client-side filter can match on it.
func buildTraitTree(prefix string, data map[string]any) ([]TraitNode, int) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	nodes := make([]TraitNode, 0, len(keys))
	total := 0
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		node, leaves := traitNodeFromValue(k, path, data[k])
		nodes = append(nodes, node)
		total += leaves
	}
	return nodes, total
}

func traitNodeFromValue(key, path string, val any) (TraitNode, int) {
	switch v := val.(type) {
	case map[string]any:
		children, leaves := buildTraitTree(path, v)
		return TraitNode{Key: key, Path: path, Children: children, LeafCount: leaves}, leaves
	case []any:
		children := make([]TraitNode, 0, len(v))
		total := 0
		for i, elem := range v {
			idx := strconv.Itoa(i)
			childPath := path + "." + idx
			child, leaves := traitNodeFromValue(idx, childPath, elem)
			children = append(children, child)
			total += leaves
		}
		if total == 0 {
			total = 1
		}
		return TraitNode{Key: key, Path: path, Children: children, LeafCount: total, IsArray: true}, total
	default:
		return TraitNode{Key: key, Path: path, Value: formatTraitValue(v), LeafCount: 1}, 1
	}
}

// formatTraitValue renders a scalar trait value as the operator-facing
// string. Strings pass through untouched; everything else is JSON-encoded
// so booleans, numbers, and null read predictably.
func formatTraitValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// extractTraitSummary pulls the most-asked host facts out of the nested
// trait tree for the stat-cards strip. Missing collectors leave fields
// empty; the template renders empty fields as an em-dash so the strip
// width stays consistent across envoys.
func extractTraitSummary(data map[string]any) TraitSummary {
	var s TraitSummary
	if osMap, ok := data["os"].(map[string]any); ok {
		distro := lookupTraitString(osMap, "distro")
		if distro == "" {
			distro = lookupTraitString(osMap, "family")
		}
		if v := lookupTraitString(osMap, "version"); v != "" {
			s.OS = strings.TrimSpace(distro + " " + v)
		} else {
			s.OS = distro
		}
		s.Kernel = lookupTraitString(osMap, "kernel")
		s.Arch = lookupTraitString(osMap, "arch")
	}
	if procMap, ok := data["processors"].(map[string]any); ok {
		phys := lookupTraitInt(procMap, "physical_cores")
		log := lookupTraitInt(procMap, "logical_cores")
		switch {
		case phys > 0 && log > 0 && phys != log:
			s.CPUCores = fmt.Sprintf("%d / %d", phys, log)
		case log > 0:
			s.CPUCores = strconv.FormatInt(log, 10)
		case phys > 0:
			s.CPUCores = strconv.FormatInt(phys, 10)
		}
		s.CPUModel = lookupTraitString(procMap, "model")
	}
	if memMap, ok := data["memory"].(map[string]any); ok {
		if total := lookupTraitInt(memMap, "total"); total > 0 {
			s.MemoryHuman = humanSize(total)
		}
	}
	return s
}

func lookupTraitString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func lookupTraitInt(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
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
	Workflows     []WorkflowRow
	StatusFilter  string
	WorkflowCount int
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
	Tab           string
	Tasks         []TaskRow
	Workflows     []WorkflowRow
	TaskCount     int
	WorkflowCount int
	StatusFilter  string
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
		taskRuns, _ := h.queries.ListTaskRuns(ctx, 100)
		for _, t := range taskRuns {
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

	taskRun, err := h.queries.GetTaskRun(ctx, id)
	if err != nil {
		http.Error(w, "task run not found", http.StatusNotFound)
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
		Task:      taskRowFromDB(taskRun),
		Targets:   tgtRows,
		IsRunning: taskRun.Status == "running" || taskRun.Status == "pending",
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

// Audit renders the audit log page with chain verification status.
func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuditRead(w, r) {
		return
	}
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
	taskRuns, err := h.queries.ListTaskRuns(ctx, 100)
	if err == nil {
		for _, pr := range taskRuns {
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

	// Chain status on page load: read the report layer's cached value
	// (serve-stale-while-revalidate) so the page doesn't run an O(N) full
	// chain walk on every load. The explicit Verify-Chain button
	// (AuditVerify) still does a fresh full walk. Fall back to a direct
	// verify only when no report builder is wired (bare-handler tests).
	var chainValid bool
	var entryCount, firstBroken int64
	var chainError string
	if h.reportBuilder != nil && h.reportBuilder.AuditStatus != nil {
		s := h.reportBuilder.AuditStatus(r)
		chainValid, entryCount, firstBroken, chainError = s.Valid, s.EntryCount, s.FirstBroken, s.Error
	} else if result, err := audit.VerifyFull(ctx, h.queries); err == nil {
		chainValid, entryCount, firstBroken, chainError = result.Valid, result.EntryCount, result.FirstBroken, result.Error
	} else {
		chainError = err.Error()
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
	if !h.requireAuditRead(w, r) {
		return
	}
	ctx := r.Context()

	result, err := audit.VerifyFull(ctx, h.queries)
	if err != nil {
		fmt.Fprintf(w, `<span class="badge badge--failure">Verification error: %s</span>`, template.HTMLEscapeString(err.Error()))
		return
	}

	if !result.Valid {
		fmt.Fprintf(w, `<div class="mt-1"><span class="badge badge--failure">CHAIN BROKEN at entry #%d</span><p class="text-muted mt-1">%s</p></div>`, result.FirstBroken, template.HTMLEscapeString(result.Error))
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
		// Audit entries carry operator-influenceable free text (actor,
		// summary, and the patterns/hostnames folded into them). Escape
		// every interpolated value — source also lands inside a class
		// attribute, so an unescaped quote would break out of it.
		source := template.HTMLEscapeString(e.Source)
		b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%s</td><td><span class="badge badge--info">%s</span></td><td><span class="badge badge--%s">%s</span></td><td>%s</td><td>%s</td><td class="text-muted">%s</td></tr>`,
			e.ID, e.Timestamp.Local().Format("2006-01-02 15:04:05"), template.HTMLEscapeString(e.EventType), source, source,
			template.HTMLEscapeString(e.Actor), template.HTMLEscapeString(e.Summary), template.HTMLEscapeString(hash)))
	}
	b.WriteString(`</tbody></table></div>`)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, b.String())
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

// SpannerBoltRow represents one peer bolt in the spanner UI table.
// Sourced from the gossip-backed observability cache (Slice 2-ii-a) —
// peer bolts are identified by their Ed25519 pubkey-hex.
type SpannerBoltRow struct {
	BoltPubkey         string
	EnvoyCount         int
	Converged          int
	Degraded           int
	Failed             int
	NoData             int
	Offline            int
	ConvergencePct     float64
	SnapshotAgeSeconds int64
	// IsLocal marks the bolt rendering this page — its own data comes
	// live from the local fleet index, not the gossip cache (which holds
	// peers only), so the page lists every bolt equally regardless of
	// which one you're viewing.
	IsLocal bool
}

// SpannerPageData holds data for the spanner page.
type SpannerPageData struct {
	PageData
	BoltCount      int
	TotalEnvoys    int
	TotalConverged int
	TotalDegraded  int
	TotalFailed    int
	ConvergencePct float64
	Bolts          []SpannerBoltRow
	// HA peer replication status
	PeerEnabled       bool
	PeerRole          string
	PeerPrimaryAddr   string
	PeerLastSync      string
	PeerFilesSynced   int64
	PeerConfigVersion uint64
}

// Spanner renders the spanner federation status page from the
// gossip-backed observability cache — one row per peer bolt (ADR-026
// peer-equal model). Registered only in spanner mode.
func (h *Handler) Spanner(w http.ResponseWriter, r *http.Request) {
	var bolts []SpannerBoltRow
	var totalEnvoys, totalConverged, totalDegraded, totalFailed, totalOffline int
	now := time.Now()

	// The local bolt — gossip excludes self, so fold in its own live row
	// so every bolt is listed equally regardless of which one serves the
	// page. SnapshotAge stays 0 (the data is live, not a cached snapshot).
	if lp := h.spannerAdmin.LocalPubkey(); lp != "" {
		conv := h.spannerAdmin.LocalConvergence()
		local := SpannerBoltRow{
			BoltPubkey: lp,
			EnvoyCount: len(h.spannerAdmin.LocalEnvoys()),
			Converged:  conv.Converged,
			Degraded:   conv.Degraded,
			Failed:     conv.Failed,
			NoData:     conv.NoData,
			Offline:    conv.Offline,
			IsLocal:    true,
		}
		if active := local.EnvoyCount - local.Offline; active > 0 {
			local.ConvergencePct = float64(local.Converged) / float64(active) * 100
		}
		bolts = append(bolts, local)
		totalEnvoys += local.EnvoyCount
		totalConverged += local.Converged
		totalDegraded += local.Degraded
		totalFailed += local.Failed
		totalOffline += local.Offline
	}

	for pubkey, p := range h.spannerAdmin.AllPeerObservability() {
		row := SpannerBoltRow{
			BoltPubkey: pubkey,
			EnvoyCount: len(p.Envoys),
			Converged:  p.Convergence.Converged,
			Degraded:   p.Convergence.Degraded,
			Failed:     p.Convergence.Failed,
			NoData:     p.Convergence.NoData,
			Offline:    p.Convergence.Offline,
		}
		if active := row.EnvoyCount - row.Offline; active > 0 {
			row.ConvergencePct = float64(row.Converged) / float64(active) * 100
		}
		if u := p.UpdatedAt(); !u.IsZero() {
			row.SnapshotAgeSeconds = int64(now.Sub(u).Seconds())
		}
		bolts = append(bolts, row)
		totalEnvoys += row.EnvoyCount
		totalConverged += row.Converged
		totalDegraded += row.Degraded
		totalFailed += row.Failed
		totalOffline += row.Offline
	}
	// Stable order — map iteration is random; sort by pubkey.
	sort.Slice(bolts, func(i, j int) bool { return bolts[i].BoltPubkey < bolts[j].BoltPubkey })

	// Lost-contact envoys excluded from the compliance percentage.
	var convPct float64
	if active := totalEnvoys - totalOffline; active > 0 {
		convPct = float64(totalConverged) / float64(active) * 100
	}

	data := SpannerPageData{
		PageData:       PageData{Title: "Spanner", ActiveNav: "spanner"},
		BoltCount:      len(bolts),
		TotalEnvoys:    totalEnvoys,
		TotalConverged: totalConverged,
		TotalDegraded:  totalDegraded,
		TotalFailed:    totalFailed,
		ConvergencePct: convPct,
		Bolts:          bolts,
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

// BoltDetailPageData holds data for the per-bolt detail page — the
// gossip-backed observability snapshot for one peer bolt.
type BoltDetailPageData struct {
	PageData
	BoltPubkey         string
	EnvoyCount         int
	Converged          int
	Degraded           int
	Failed             int
	NoData             int
	Offline            int
	ConvergencePct     float64
	SnapshotAgeSeconds int64
	Envoys             []BoltDetailEnvoyRow
	IsLocal            bool
}

// BoltDetailEnvoyRow is an envoy on a bolt.
type BoltDetailEnvoyRow struct {
	ID        string
	Hostname  string
	IPAddress string
	LastSeen  string
	Revoked   bool
}

// BoltDetail renders the per-bolt detail page from the gossip-backed
// observability cache — convergence breakdown + the bolt's envoy
// roster. Runs are never gossiped, so they are not shown here; use
// `vigocli spanner bolt runs` or the per-envoy detail page.
func (h *Handler) BoltDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Resolve the bolt's envoy roster + convergence. The gossip cache holds
	// peers only, so the local bolt's own detail comes live from the local
	// fleet index — without this, clicking your own row would 404.
	var (
		envoys  []spanner.EnvoyLite
		conv    spanner.ConvergenceCounters
		isLocal bool
		updated time.Time
	)
	if id == h.spannerAdmin.LocalPubkey() {
		envoys = h.spannerAdmin.LocalEnvoys()
		conv = h.spannerAdmin.LocalConvergence()
		isLocal = true
	} else if p := h.spannerAdmin.PeerObservability(id); p != nil {
		envoys = p.Envoys
		conv = p.Convergence
		updated = p.UpdatedAt()
	} else {
		http.Error(w, "bolt not found", http.StatusNotFound)
		return
	}

	var envoyRows []BoltDetailEnvoyRow
	for _, e := range envoys {
		row := BoltDetailEnvoyRow{
			ID:        e.ID,
			Hostname:  e.Hostname,
			IPAddress: e.IP,
			Revoked:   e.Revoked,
		}
		if e.LastSeenMs > 0 {
			row.LastSeen = time.UnixMilli(e.LastSeenMs).Local().Format("2006-01-02 15:04:05")
		}
		envoyRows = append(envoyRows, row)
	}

	data := BoltDetailPageData{
		PageData:   PageData{Title: "Bolt: " + id, ActiveNav: "spanner"},
		BoltPubkey: id,
		EnvoyCount: len(envoys),
		Converged:  conv.Converged,
		Degraded:   conv.Degraded,
		Failed:     conv.Failed,
		NoData:     conv.NoData,
		Offline:    conv.Offline,
		Envoys:     envoyRows,
		IsLocal:    isLocal,
	}
	if active := data.EnvoyCount - data.Offline; active > 0 {
		data.ConvergencePct = float64(data.Converged) / float64(active) * 100
	}
	if !updated.IsZero() {
		data.SnapshotAgeSeconds = int64(time.Since(updated).Seconds())
	}
	h.setCommonWithRequest(&data.PageData, r)
	h.renderer.Render(w, "bolt-detail", data)
}

// SecurityScanPageData holds aggregated security scan results for the security page.
type SecurityScanPageData struct {
	// Unique CVE counts — distinct CVE IDs, deduplicated across the fleet
	// and across scanners. The only CVE-count vocabulary the UI aggregates;
	// raw per-scanner match counts live in the per-host drawer only.
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
	// SCAP/STIG fleet counts — counts only, never an averaged compliance
	// score (cross-profile averages are meaningless). ScapScanned is hosts
	// reporting any SCAP scan; ScapClean is those with no failed rules;
	// ScapHighFail is those with at least one high-severity failure.
	ScapScanned  int64
	ScapClean    int64
	ScapHighFail int64
	ScannedHosts int64
	TotalHosts   int64
	CVEs         []CVEEntry        // deduplicated CVEs across fleet (one per CVE ID)
	PerHost      []SecurityHostRow // per-envoy breakdown
	// AnyTruncated is true when at least one host's per-severity unique
	// counts (unique_critical+high+medium+low, agent-authoritative full
	// totals) exceed the length of the host's CVE list (capped at the
	// agent's MAX_CVES). Surfaces as a banner above the per-host panel
	// so operators know the fleet UniqueCVEs stat may undercount.
	AnyTruncated bool
}

// CVEEntry is a single CVE keyed by CVE ID (deduplicated across scanners).
type CVEEntry struct {
	CVEID    string // CVE identifier (e.g., "CVE-2024-1234")
	Severity string // "critical", "high", "medium", or "low" (lowercase)
	Packages int64  // number of packages affected by this CVE
}

// SecurityHostRow holds per-envoy security scan summary.
type SecurityHostRow struct {
	ID                 string
	Hostname           string
	UniqueCritical     int64 // distinct CVE IDs per severity, deduped across scanners (agent-authoritative)
	UniqueHigh         int64
	UniqueMedium       int64
	UniqueLow          int64
	LynisScore         int64
	LynisWarnings      int64
	LynisSuggestions   int64
	RootkitClean       bool
	RkhunterWarnings   int64
	ChkrootkitInfected int64
	ClamavClean        bool
	ClamavInfected     int64
	AideClean          bool
	AideAdded          int64
	AideRemoved        int64
	AideChanged        int64
	CVEs               []CVEEntry
	LastScan           string
	// Truncated is true when sum(UniqueCritical+High+Medium+Low) exceeds
	// len(CVEs) — i.e., the agent's MAX_CVES cap dropped some entries
	// from the wire. Per-host columns stay accurate; only the fleet
	// dedup misses the dropped ones.
	Truncated bool
}

// SecurityPage renders the fleet security dashboard — convergence,
// CVE counts, hardening scores, and the AI chat drawer overlay.
func (h *Handler) SecurityPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	convData := h.dataSource.ConvergenceData(h.baseInterval, h.offlineThreshold)

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
		ConvergencePct  float64
		Total           int64
		Converged       int64
		Degraded        int64
		Failed          int64
		NoData          int64
		Changed         int64
		Diverged        int64
		Offline         int64
		Security        SecurityScanPageData
		Certs           CertExpirySummary
		QueryTimeoutSec int
		CVETrend        []float64
		HardeningTrend  []float64
	}{
		PageData:        PageData{Title: "Security", ActiveNav: "security"},
		ConvergencePct:  convData.ConvergencePct,
		Total:           convData.Total,
		Converged:       convData.Converged,
		Degraded:        convData.Degraded,
		Failed:          convData.Failed,
		NoData:          convData.NoData,
		Changed:         convData.Changed,
		Diverged:        convData.Diverged,
		Offline:         convData.Offline,
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
	convData := h.dataSource.ConvergenceData(h.baseInterval, h.offlineThreshold)

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
		Locale         string
		ConvergencePct float64
		Total          int64
		Converged      int64
		Degraded       int64
		Failed         int64
		NoData         int64
		Changed        int64
		Diverged       int64
		Offline        int64
		Security       SecurityScanPageData
		Certs          CertExpirySummary
		CVETrend       []float64
		HardeningTrend []float64
	}{
		Locale:         h.localeFromRequest(r),
		ConvergencePct: convData.ConvergencePct,
		Total:          convData.Total,
		Converged:      convData.Converged,
		Degraded:       convData.Degraded,
		Failed:         convData.Failed,
		NoData:         convData.NoData,
		Changed:        convData.Changed,
		Diverged:       convData.Diverged,
		Offline:        convData.Offline,
		Security:       secData,
		Certs:          certData,
		CVETrend:       cveTrend,
		HardeningTrend: hardeningTrend,
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
		switch cve.Classify(m.Severity) {
		case cve.SevCritical:
			sevClass = "text-danger"
		case cve.SevHigh:
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
		// ActiveEnvoys is the enforcer's mode-aware count (the licensed-count
		// SoT) — the local FleetIndex count in local mode, the spanner-wide
		// aggregate in aggregate mode — so the page reflects what the license
		// actually gates and agrees with the dashboard capacity banner +
		// header warning, which read the same GetStatus() value.
		data.ActiveEnvoys = s.ActiveEnvoys
		data.MaxEnvoys = s.MaxEnvoys
		data.ExpiresAt = s.ExpiresAt
		data.DaysRemaining = s.DaysRemaining
		data.Violation = s.Violation
		data.IsFreeTier = h.enforcer.IsFreeLicense()
		if s.MaxEnvoys > 0 {
			data.UsagePct = float64(data.ActiveEnvoys) / float64(s.MaxEnvoys) * 100
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
		ID        string
		Hostname  string
		IP        string
		Traits    map[string]any
		Flat      map[string]string
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
	locale := h.localeFromRequest(r)
	type healthAlert struct {
		Level string // "danger", "warning"
		Title string
		Count int
		Hosts []string
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
		alerts = append(alerts, healthAlert{Level: "danger", Title: i18n.CatalogT(locale, "health_alert.eol_os"), Count: len(eolHosts), Hosts: eolHosts})
	}
	if len(eolSoonHosts) > 0 {
		alerts = append(alerts, healthAlert{Level: "warning", Title: i18n.CatalogT(locale, "health_alert.eol_os_soon"), Count: len(eolSoonHosts), Hosts: eolSoonHosts})
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
		alerts = append(alerts, healthAlert{Level: "warning", Title: i18n.CatalogT(locale, "health_alert.disk_high"), Count: len(diskFullHosts), Hosts: diskFullHosts})
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
		Where       string
		Show        string
		GroupBy     string
		GroupLabel  string
		Total       int
		StaleCount  int
		OSSummary   []inventoryGroupRow
		ArchCounts  map[string]int
		Groups      []inventoryGroupRow
		Changes     []inventoryChangeRow
		ChangeCount int
		Alerts      []healthAlert
		UniqueOS    int
		UniqueArch  int
	}{
		PageData:    PageData{Title: "Inventory", ActiveNav: "inventory"},
		Where:       where,
		Show:        show,
		GroupBy:     groupBy,
		GroupLabel:  groupLabel,
		Total:       totalEnvoys,
		StaleCount:  staleCount,
		OSSummary:   osSummary,
		ArchCounts:  archCounts,
		Groups:      groups,
		Changes:     changes,
		ChangeCount: len(changes),
		Alerts:      alerts,
		UniqueOS:    uniqueOS,
		UniqueArch:  uniqueArch,
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
		typeLabel = titleCaser.String(hwType)
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

// GrafanaDashboard is one bundled-dashboard deep link for the /health page.
type GrafanaDashboard struct {
	Name string
	URL  string
}

// HealthPageData extends PageData with the optional Grafana dashboard
// links, shown only when a Grafana URL is configured.
type HealthPageData struct {
	PageData
	GrafanaDashboards []GrafanaDashboard
}

// bundledDashboards is the fixed uid/name set Vigo ships under
// docs/howto/grafana/. The /health page deep-links each at
// <grafana.url>/d/<uid> when a Grafana URL is configured.
var bundledDashboards = []struct{ UID, Name string }{
	{"vigo-overview", "Fleet Overview"},
	{"vigo-convergence", "Convergence"},
	{"vigo-risk", "Risk Posture"},
	{"vigo-security", "Security Posture"},
	{"vigo-swarm", "Swarm & Spanner"},
}

// grafanaDashboards returns the bundled-dashboard deep links, or nil
// when no Grafana URL is configured.
func (h *Handler) grafanaDashboards() []GrafanaDashboard {
	base := strings.TrimRight(h.grafana.URL, "/")
	if base == "" {
		return nil
	}
	out := make([]GrafanaDashboard, len(bundledDashboards))
	for i, d := range bundledDashboards {
		out[i] = GrafanaDashboard{Name: d.Name, URL: base + "/d/" + d.UID}
	}
	return out
}

// Health renders the Vigo Health diagnostics page.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	data := HealthPageData{PageData: PageData{Title: "Vigo Health", ActiveNav: "health"}}
	h.setCommonWithRequest(&data.PageData, r)
	data.GrafanaDashboards = h.grafanaDashboards()
	h.renderer.Render(w, "health", data)
}

// SetTraitLookup sets the trait provider for envoy health indicators.
func (h *Handler) SetTraitLookup(tl TraitLookup) {
	h.traitLookup = tl
}

// enrichEnvoyRows populates the agent-version display field and the
// row's composite Agent Health pill. The pill is sourced exclusively
// from the fleetpressure SoT (server/fleetpressure) — never recomputed
// from raw traits here — so the /envoys "Agent Health" column, the
// dashboard "Agent Health" card, and the /agent-health table all agree
// on the same severity classification per envoy.
func (h *Handler) enrichEnvoyRows(rows []EnvoyRow) {
	for i := range rows {
		// Agent version from traits (always available, even if DB column is empty).
		if rows[i].AgentVersion == "" && h.traitLookup != nil {
			if v := h.traitLookup.EnvoyTrait(rows[i].ID, "identity.agent_version"); v != "" {
				rows[i].AgentVersion = v
			}
		}
		// Agent Health composite. Routed through fleetpressure — the
		// canonical per-envoy projection that already covers FDs, host
		// FD%, OOM-recent, rootfs %, inode %, and repo-health on one
		// severity axis. The pill color follows Health; the hover
		// title lists the contributing per-axis values so operators
		// see the breakdown without leaving the page.
		if h.dataSource != nil {
			if p, ok := h.dataSource.EnvoyPressure(rows[i].ID); ok {
				rows[i].HealthSeverity = p.Health
				if p.Health != fleetpressure.Unknown {
					rows[i].HealthSummary = HealthSummary(p)
					pCopy := p
					rows[i].Pressure = &pCopy
				}
			}
		}
	}
}

// HealthSummary renders the English one-line per-axis breakdown shown on
// every Agent Health pill hover (envoys table, dashboard fleet table,
// per-envoy footprint status card). Only axes the agent actually
// reported (Present) appear; the order matches the /agent-health
// table columns for operator muscle memory. Exported so the
// FleetRow builder in server/fleet/datasource.go can populate the
// raw row without a parallel formatter; the templates render the
// hover-title via the locale-aware healthSummaryL helper off the
// row's *Pressure field, so this English form is the fallback only.
func HealthSummary(p fleetpressure.EnvoyPressure) string {
	return healthSummaryLocale("", p)
}

// healthSummaryLocale is the locale-aware core. The axis labels carry
// interpolated counts, so each is a printf-style catalog key (en + fr);
// the empty/English locale keeps the output byte-identical to the
// original source via the symmetric en.* entries.
func healthSummaryLocale(locale string, p fleetpressure.EnvoyPressure) string {
	var parts []string
	if p.RootfsPct.Present {
		parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.rootfs"), p.RootfsPct.Value))
	}
	if p.InodePct.Present {
		parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.inode"), p.InodePct.Value))
	}
	if p.RepoHealth.Present {
		switch {
		case p.RepoHealth.Errors > 0:
			parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.repos_broken"), p.RepoHealth.Errors))
		case p.RepoHealth.Warnings > 0:
			parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.repos_warn"), p.RepoHealth.Warnings))
		default:
			parts = append(parts, i18n.CatalogT(locale, "health_summary.repos_ok"))
		}
	}
	if p.OpenFDs.Present {
		parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.fds"), p.OpenFDs.Value))
	}
	if p.FDPct.Present {
		parts = append(parts, fmt.Sprintf(i18n.CatalogT(locale, "health_summary.host_fd"), p.FDPct.Value))
	}
	if p.OomRecent.Present && p.OomRecent.Value > 0 {
		parts = append(parts, i18n.CatalogT(locale, "health_summary.recent_oom"))
	}
	return strings.Join(parts, ", ")
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
		fmt.Fprintf(w, `<p class="text-muted u-p-20px">No envoys with trait data. Traits are collected after the first check-in.</p>`)
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
		fmt.Fprintf(w, `<p class="text-muted u-mb-8px">%d envoys`, len(rows))
		if hasMore {
			fmt.Fprintf(w, `+`)
		}
		if len(filters) > 0 {
			fmt.Fprintf(w, ` (filtered)`)
		}
		fmt.Fprintf(w, `</p>`)
	}

	if len(rows) == 0 && cursor == "" {
		fmt.Fprintf(w, `<p class="text-muted u-p-20px">No envoys match the filter criteria.</p>`)
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
			`<tr class="u-h-1px" hx-get="/ui/fragments/inventory?%s" hx-trigger="revealed" hx-swap="afterend" hx-target="#inventory-tbody" hx-select="tr"><td colspan="%d"></td></tr>`,
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

	// Modal overlay. data-overlay-close closes the modal on a backdrop click
	// (handled in ui-actions.js, which only fires when the overlay itself is
	// the click target — not its children).
	fmt.Fprint(w, `<div class="trait-index-modal" data-overlay-close="trait-index-modal">`)
	fmt.Fprint(w, `<div class="trait-index-modal__panel">`)

	// Header.
	fmt.Fprintf(w, `<div class="trait-index-modal__header">`)
	fmt.Fprintf(w, `<strong class="trait-index-modal__title">Trait Index — %d traits across %d namespaces</strong>`, len(keyCounts), len(nsNames))
	fmt.Fprint(w, `<button class="btn btn--sm" data-action="close-trait-modal">Close</button>`)
	fmt.Fprint(w, `</div>`)

	// Search field.
	fmt.Fprint(w, `<div class="trait-index-modal__search">`)
	fmt.Fprint(w, `<input id="trait-index-search" type="text" placeholder="Filter traits..." `+
		`class="trait-index-modal__search-input" `+
		`data-input="trait-filter">`)
	fmt.Fprint(w, `</div>`)

	// Scrollable body.
	fmt.Fprint(w, `<div id="trait-index-body" class="trait-index-modal__body">`)
	for _, ns := range nsNames {
		keys := namespaces[ns]
		escapedNS := template.HTMLEscapeString(ns)
		fmt.Fprintf(w, `<div class="trait-index-modal__ns" data-ns-header="%s">%s (%d)</div>`,
			escapedNS, escapedNS, len(keys))
		for _, ki := range keys {
			escapedKey := template.HTMLEscapeString(ki.Key)
			fmt.Fprintf(w, `<div class="trait-search__hit" data-trait-key="%s" data-ns="%s" `+
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
	// Focus is applied by ui-actions.js on htmx:afterSettle (no inline script).
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
	keyValues := map[string]string{} // key → first non-empty sample value
	keyCounts := map[string]int{}    // key → number of envoys reporting it
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
		fmt.Fprintf(w, `<div class="trait-search__hit" `+
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

	// Siblings sharing the prefix-up-to-last-dot of the clicked key are
	// pre-checked by default. For flat namespaces (e.g. os.distro) this
	// degenerates to "all siblings"; for sub-namespaced ones (e.g.
	// blockdevices.nvme0n1.firmware_rev) it scopes to the same instance,
	// so unrelated devices' columns don't fill the table with dashes.
	parentPrefix := key
	if idx := strings.LastIndexByte(key, '.'); idx >= 0 {
		parentPrefix = key[:idx]
	}
	checkPrefix := parentPrefix + "."

	fmt.Fprint(w, `<div class="trait-explore">`)
	fmt.Fprint(w, `<div class="trait-explore__header">`)
	fmt.Fprint(w, `<div class="card-heading-row">`)
	fmt.Fprintf(w, `<h3>Exploring: <code>%s</code></h3>`, escapedKey)
	fmt.Fprint(w, `<div class="trait-explore__header-actions">`)
	fmt.Fprint(w, `<button type="submit" form="trait-explore-form" formaction="/ui/export/inventory-explore.ods" class="btn btn--sm btn--primary" hx-disable>Export ODS</button>`)
	fmt.Fprint(w, `<button class="btn btn--sm" data-action="close-trait-explore">Close</button>`)
	fmt.Fprint(w, `</div></div>`)
	fmt.Fprintf(w, `<p class="trait-explore__description">Grouped by <code>%s</code> &middot; columns from the <code>%s</code> namespace.</p>`, escapedKey, escapedNS)
	fmt.Fprint(w, `</div>`)

	// Checkbox cluster — labelled "Columns:" with a Reset button. Each
	// checkbox triggers a table reload on change; Reset reverts the form
	// to its initial pre-checked state then re-runs the table fetch.
	fmt.Fprintf(w, `<form id="trait-explore-form" method="get" class="trait-explore__cols">`)
	fmt.Fprintf(w, `<input type="hidden" name="key" value="%s">`, template.HTMLEscapeString(key))
	fmt.Fprintf(w, `<span class="trait-explore__cols-label">Columns</span>`)
	for _, sib := range siblings {
		checked := ""
		if strings.HasPrefix(sib, checkPrefix) {
			checked = " checked"
		}
		shortName := sib
		if strings.HasPrefix(sib, namespace+".") {
			shortName = sib[len(namespace)+1:]
		}
		fmt.Fprintf(w, `<label class="trait-explore__col-toggle">`+
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
	// Reset reverts checkboxes to the smart-pre-checked default. type="reset"
	// fires the browser's form-reset *after* click handlers run, so htmx
	// must wait a tick before reading the post-reset form state.
	fmt.Fprintf(w, `<span class="trait-explore__cols-actions">`+
		`<button type="reset" form="trait-explore-form" class="btn btn--sm" `+
		`hx-get="/ui/fragments/inventory-explore-table" `+
		`hx-include="#trait-explore-form" `+
		`hx-target="#trait-explore-table" `+
		`hx-swap="innerHTML" `+
		`hx-trigger="click delay:50ms">Reset</button>`+
		`</span>`)
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
//
// Display rules:
//   - The grouped-by `key` is omitted from the on-screen column set; its value
//     is already the group section row, so showing it as a column duplicates
//     information. The ODS export keeps it (denormalized for spreadsheet pivots).
//   - Columns ending in `.size_bytes` render as humanized sizes with the raw
//     byte count in the `title` attribute for audit precision.
//   - Group order: row count desc, then label asc; envoys missing the trait
//     ("not reported") render last under a pseudo-label.
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

	// Filter cols to drop the grouped-by key from the on-screen render.
	displayCols := make([]string, 0, len(cols))
	for _, c := range cols {
		if c == key {
			continue
		}
		displayCols = append(displayCols, c)
	}

	// Sentinel label for envoys missing the trait — sorts last via pre-pended
	// high-codepoint marker, swapped out at render time.
	const missingLabel = "￿not reported"

	type envoyRow struct {
		ID       string
		Hostname string
		Fields   []string // values for displayCols, in order
	}
	type groupEntry struct {
		Label string // raw value (or missingLabel sentinel)
		Rows  []envoyRow
	}
	groupMap := map[string]*groupEntry{}

	for _, t := range all {
		var traits map[string]any
		if json.Unmarshal([]byte(t.Traits), &traits) != nil {
			continue
		}
		flat := map[string]any{}
		flattenTraits("", traits, flat)

		label := missingLabel
		if v, ok := flat[key]; ok {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				label = s
			}
		}

		fields := make([]string, len(displayCols))
		for i, col := range displayCols {
			if v, ok := flat[col]; ok {
				s := fmt.Sprint(v)
				if s == "<nil>" || s == "" {
					s = "-"
				}
				fields[i] = s
			} else {
				fields[i] = "-"
			}
		}

		g, exists := groupMap[label]
		if !exists {
			g = &groupEntry{Label: label}
			groupMap[label] = g
		}
		g.Rows = append(g.Rows, envoyRow{ID: t.ID, Hostname: t.Hostname, Fields: fields})
	}

	if len(groupMap) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No envoys with this trait.</p>`)
		return
	}

	// Sort: row count desc, then label asc; missing-trait sentinel last (its
	// label sorts highest via the ￿ prefix).
	groups := make([]*groupEntry, 0, len(groupMap))
	for _, g := range groupMap {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		iMissing := groups[i].Label == missingLabel
		jMissing := groups[j].Label == missingLabel
		if iMissing != jMissing {
			return jMissing // missing goes last
		}
		if len(groups[i].Rows) != len(groups[j].Rows) {
			return len(groups[i].Rows) > len(groups[j].Rows)
		}
		return groups[i].Label < groups[j].Label
	})
	// Hostnames within each group: alphabetic for stability.
	for _, g := range groups {
		sort.Slice(g.Rows, func(i, j int) bool { return g.Rows[i].Hostname < g.Rows[j].Hostname })
	}

	// Empty-cols path: just hostname lists per group (no table chrome needed).
	if len(displayCols) == 0 {
		for _, g := range groups {
			fmt.Fprint(w, `<div class="u-mb-12px">`)
			fmt.Fprint(w, `<div class="u-fs-13px-mb-4px">`)
			fmt.Fprint(w, renderGroupHeading(key, g.Label, len(g.Rows), missingLabel))
			fmt.Fprint(w, `</div>`)
			fmt.Fprint(w, `<div class="u-fs-13px-c-var-color-muted">`)
			for i, row := range g.Rows {
				if i > 0 {
					fmt.Fprint(w, `, `)
				}
				fmt.Fprintf(w, `<a href="/inventory/%s">%s</a>`,
					template.HTMLEscapeString(row.ID),
					template.HTMLEscapeString(row.Hostname))
			}
			fmt.Fprint(w, `</div></div>`)
		}
		return
	}

	// Short column names (strip namespace prefix).
	shortCols := make([]string, len(displayCols))
	for i, c := range displayCols {
		if strings.HasPrefix(c, namespace+".") {
			shortCols[i] = c[len(namespace)+1:]
		} else {
			shortCols[i] = c
		}
	}

	// Single table: sticky thead, group section rows interleave with envoy rows.
	colspan := len(displayCols) + 1
	fmt.Fprint(w, `<div class="trait-explore__wrap">`)
	fmt.Fprint(w, `<table class="table trait-explore__table"><thead><tr><th>Hostname</th>`)
	for _, sc := range shortCols {
		fmt.Fprintf(w, `<th>%s</th>`, template.HTMLEscapeString(sc))
	}
	fmt.Fprint(w, `</tr></thead><tbody>`)
	for _, g := range groups {
		fmt.Fprintf(w, `<tr class="trait-explore__group-row"><td colspan="%d">%s</td></tr>`,
			colspan, renderGroupHeading(key, g.Label, len(g.Rows), missingLabel))
		for _, row := range g.Rows {
			fmt.Fprintf(w, `<tr><td><a href="/inventory/%s">%s</a></td>`,
				template.HTMLEscapeString(row.ID),
				template.HTMLEscapeString(row.Hostname))
			for i, f := range row.Fields {
				fmt.Fprintf(w, `<td class="trait-explore__val">%s</td>`,
					formatTraitCell(displayCols[i], f))
			}
			fmt.Fprint(w, `</tr>`)
		}
	}
	fmt.Fprint(w, `</tbody></table></div>`)
}

// renderGroupHeading produces the "<key> = <value>  (n)" line for a group
// section row. The missingLabel sentinel renders as italicized "not reported".
func renderGroupHeading(key, label string, count int, missingLabel string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<span class="trait-explore__group-key">%s</span>`,
		template.HTMLEscapeString(key))
	if label == missingLabel {
		sb.WriteString(`<span class="trait-explore__group-eq">&middot;</span>`)
		sb.WriteString(`<span class="trait-explore__group-value trait-explore__group-value--missing">not reported</span>`)
	} else {
		sb.WriteString(`<span class="trait-explore__group-eq">=</span>`)
		fmt.Fprintf(&sb, `<span class="trait-explore__group-value">%s</span>`,
			template.HTMLEscapeString(label))
	}
	fmt.Fprintf(&sb, `<span class="trait-explore__group-count">%d</span>`, count)
	return sb.String()
}

// formatTraitCell renders a flattened trait value for the explore table.
// Columns ending in `.size_bytes` get humanized with raw bytes preserved in
// the `title` attribute so audit precision survives.
func formatTraitCell(col, val string) string {
	if val == "-" {
		return template.HTMLEscapeString(val)
	}
	if strings.HasSuffix(col, ".size_bytes") {
		// Trait values flatten through fmt.Sprint, so float64 (JSON number)
		// arrives as "1.234e+09" or a plain decimal. ParseFloat handles both.
		if n, err := strconv.ParseFloat(val, 64); err == nil && n >= 0 {
			return fmt.Sprintf(`<span title="%s bytes">%s</span>`,
				template.HTMLEscapeString(val),
				template.HTMLEscapeString(humanSize(int64(n))))
		}
	}
	return template.HTMLEscapeString(val)
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
	_, _ = mf.Write([]byte("application/vnd.oasis.opendocument.spreadsheet"))

	// META-INF/manifest.xml
	manf, _ := zw.Create("META-INF/manifest.xml")
	_, _ = manf.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
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
	_, _ = cf.Write([]byte(buf.String()))
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
			{"Device", ""}, // special: from blockdevices
			{"Model", ""},
			{"Firmware", ""},
			{"Size", ""},
		}
	case "gpu":
		cols = []detailCol{
			{"Name", ""}, // special: from gpu.devices
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
		fmt.Fprintf(w, `<p class="text-muted u-p-8px">No matching envoys found.</p>`)
		return
	}

	fmt.Fprintf(w, `<table class="table u-m-8px-0"><thead><tr><th>Hostname</th>`)
	for _, col := range cols {
		fmt.Fprintf(w, `<th>%s</th>`, template.HTMLEscapeString(col.Header))
	}
	fmt.Fprintf(w, `</tr></thead><tbody>`)
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td><a href="/inventory/%s">%s</a></td>`,
			template.HTMLEscapeString(row.ID),
			template.HTMLEscapeString(row.Hostname))
		for _, val := range row.Fields {
			fmt.Fprintf(w, `<td class="mono u-fs-12px">%s</td>`, template.HTMLEscapeString(val))
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

// HealthFragment returns the Vigo Health diagnostics via HTMX.
//
// Renders only server-scope checks — fleet conditions like stale envoys
// belong on dashboard/envoy surfaces and must not influence the
// server's headline status. Skips count as not-applicable: they are
// shown in the table but excluded from the pass-rate denominator and
// from the overall status rollup.
func (h *Handler) HealthFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.healthChecker == nil {
		fmt.Fprintf(w, `<p>Health checks not available.</p>`)
		return
	}
	_, allChecks := h.healthChecker.RunChecks()

	serverChecks := make([]HealthCheck, 0, len(allChecks))
	for _, c := range allChecks {
		// Treat empty scope as server for forward compatibility with
		// any check that hasn't been tagged yet.
		if c.Scope == "" || c.Scope == "server" {
			serverChecks = append(serverChecks, c)
		}
	}

	passCount, skipCount := 0, 0
	status := "healthy"
	for _, c := range serverChecks {
		switch c.Status {
		case "pass":
			passCount++
		case "skip":
			skipCount++
		case "warn":
			if status == "healthy" {
				status = "degraded"
			}
		case "fail":
			status = "unhealthy"
		}
	}
	applicable := len(serverChecks) - skipCount

	statusCSS := "success"
	if status == "degraded" {
		statusCSS = "warning"
	} else if status == "unhealthy" {
		statusCSS = "danger"
	}
	fmt.Fprintf(w, `<div class="stat-grid">`)
	fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Overall Status</div><div class="stat-card__value text-%s">%s</div></div>`, statusCSS, template.HTMLEscapeString(status))
	if skipCount > 0 {
		fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Checks Passed</div><div class="stat-card__value">%d / %d <span class="u-fs-0-65em-fw-normal">(%d skipped)</span></div></div>`, passCount, applicable, skipCount)
	} else {
		fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Checks Passed</div><div class="stat-card__value">%d / %d</div></div>`, passCount, applicable)
	}
	fmt.Fprintf(w, `<div class="stat-card"><div class="stat-card__label">Version</div><div class="stat-card__value u-fs-1rem">%s</div></div>`, template.HTMLEscapeString(h.version))
	fmt.Fprintf(w, `</div>`)
	fmt.Fprintf(w, `<div class="card mt-3"><h2>Diagnostic Checks</h2><table class="table"><thead><tr><th>Check</th><th>Status</th><th>Details</th></tr></thead><tbody>`)
	for _, c := range serverChecks {
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
