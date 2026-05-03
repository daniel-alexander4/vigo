// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDashboardHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.Dashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Dashboard returned %d, want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := rec.Body.String()

	// Stat cards should be present
	for _, want := range []string{"Total Envoys", "Active (1h)", "converged", "covered"} {
		if !strings.Contains(body, want) {
			t.Errorf("Dashboard missing stat card %q", want)
		}
	}

	// Should have auto-refresh trigger
	if !strings.Contains(body, `hx-trigger="every 30s"`) {
		t.Error("Dashboard missing hx-trigger for auto-refresh")
	}
}

func TestDashboardStatsFragment(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/dashboard-stats", nil)
	rec := httptest.NewRecorder()

	h.DashboardStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DashboardStats returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Fragment should NOT contain full HTML wrapper
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<html") {
		t.Error("DashboardStats fragment should not contain full HTML document")
	}

	// Should contain stat cards
	if !strings.Contains(body, "stat-card") {
		t.Error("DashboardStats fragment missing stat-card elements")
	}

	// Each column body is wrapped in a block-level <a> so the whole card
	// is clickable — drill-down affordance.
	for _, want := range []string{
		`href="/envoys"`, `href="/security"`, `href="/compliance"`, `href="/risk"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("DashboardStats fragment missing card drill-down link %q", want)
		}
	}
}

func TestComplianceHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/compliance", nil)
	rec := httptest.NewRecorder()

	h.Compliance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Compliance returned %d, want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := rec.Body.String()

	// Should contain compliance gauge
	if !strings.Contains(body, "compliance-gauge") {
		t.Error("Compliance page missing compliance-gauge")
	}
}

func TestComplianceHandlerNoFrameworks(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}

	td := &testData{
		envoys: []testEnvoy{
			{
				id:       uuid.New(),
				hostname: "ok-envoy.example.com",
				lastSeen: time.Now(),
				status:   "converged",
			},
		},
	}

	h := &Handler{
		renderer:     renderer,
		dataSource:   &mockDataSource{data: td},
		baseInterval: 5 * time.Minute,
		version:      "test",
	}

	req := httptest.NewRequest(http.MethodGet, "/compliance", nil)
	rec := httptest.NewRecorder()

	h.Compliance(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "No compliance frameworks configured") {
		t.Error("Compliance page should show no-frameworks message when none are configured")
	}
}

func TestAllPageHandlersContentType(t *testing.T) {
	h := newTestHandler(t)

	pages := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"Dashboard", "/", h.Dashboard},
		{"Compliance", "/compliance", h.Compliance},
		{"Envoys", "/envoys", h.Envoys},
		{"Runs", "/runs", h.Runs},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p.path, nil)
			rec := httptest.NewRecorder()
			p.handler(rec, req)

			ct := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s Content-Type = %q, want text/html", p.name, ct)
			}
		})
	}
}

// --- Task 1: New page tests ---

func TestEnvoysHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/envoys", nil)
	rec := httptest.NewRecorder()

	h.Envoys(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Envoys returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Table should have expected column headers
	for _, col := range []string{"Hostname", "IP Address", "Last Check-in", "Last Convergence", "Convergence"} {
		if !strings.Contains(body, col) {
			t.Errorf("Envoys page missing column header %q", col)
		}
	}

	// Should contain test envoy hostnames
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("Envoys page missing hostname web-01.example.com")
	}

	// Should show status badge
	if !strings.Contains(body, "badge") {
		t.Error("Envoys page missing status badge")
	}
}

func TestEnvoysHandlerStatusFilter(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/envoys?status=stale", nil)
	rec := httptest.NewRecorder()

	h.Envoys(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Envoys filtered returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// stale envoy should be present
	if !strings.Contains(body, "web-02.example.com") {
		t.Error("Filtered envoys page should show stale envoy web-02.example.com")
	}

	// compliant envoy should NOT be present
	if strings.Contains(body, "web-01.example.com") {
		t.Error("Filtered envoys page should NOT show compliant envoy when filtering for stale")
	}
}

func TestEnvoyDetailHandler(t *testing.T) {
	h := newTestHandler(t)

	envoyID := h.dataSource.(*mockDataSource).data.envoys[0].id
	req := httptest.NewRequest(http.MethodGet, "/envoys/"+envoyID.String(), nil)
	req.SetPathValue("id", envoyID.String())
	rec := httptest.NewRecorder()

	h.EnvoyDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("EnvoyDetail returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Should contain envoy hostname
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("EnvoyDetail page missing envoy hostname")
	}

	// Should contain traits panel
	if !strings.Contains(body, "traits") {
		t.Error("EnvoyDetail page missing traits panel")
	}
}

func TestEnvoyDetailHandler404(t *testing.T) {
	h := newTestHandler(t)

	nonExistent := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/envoys/"+nonExistent.String(), nil)
	req.SetPathValue("id", nonExistent.String())
	rec := httptest.NewRecorder()

	h.EnvoyDetail(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("EnvoyDetail for non-existent returned %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRunsHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/runs", nil)
	rec := httptest.NewRecorder()

	h.Runs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Runs returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Table should have expected column headers
	for _, col := range []string{"Run", "Hostname", "Status", "Modules"} {
		if !strings.Contains(body, col) {
			t.Errorf("Runs page missing column header %q", col)
		}
	}

	// Should contain test run hostname
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("Runs page missing hostname web-01.example.com")
	}
}

func TestRunsHandlerStatusFilter(t *testing.T) {
	td := defaultTestData()
	// Add a failure run
	failRunID := uuid.New()
	td.runs = append(td.runs, testRun{
		id:          failRunID,
		envoyID:    td.envoys[1].id,
		hostname:    "web-02.example.com",
		startedAt:   time.Now().Add(-10 * time.Minute),
		status:      "failure",
		moduleCount: 2,
		failedCount: 1,
	})

	h := newTestHandlerWithData(t, td)

	req := httptest.NewRequest(http.MethodGet, "/runs?status=failure", nil)
	rec := httptest.NewRecorder()

	h.Runs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Runs filtered returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Only failure run should be present
	if !strings.Contains(body, "web-02.example.com") {
		t.Error("Filtered runs page should show failure run for web-02")
	}
}

func TestRunDetailHandler(t *testing.T) {
	h := newTestHandler(t)

	runID := h.dataSource.(*mockDataSource).data.runs[0].id
	req := httptest.NewRequest(http.MethodGet, "/runs/"+runID.String(), nil)
	req.SetPathValue("id", runID.String())
	rec := httptest.NewRecorder()

	h.RunDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("RunDetail returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Should contain per-module results
	if !strings.Contains(body, "Action") {
		t.Error("RunDetail page missing Action column for per-module results")
	}

	// Should contain hostname
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("RunDetail page missing envoy hostname")
	}
}

func TestEnvoysTableFragment(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoys-table", nil)
	rec := httptest.NewRecorder()

	h.EnvoysTable(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("EnvoysTable returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Fragment should NOT contain full HTML wrapper
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<html") {
		t.Error("EnvoysTable fragment should not contain full HTML document")
	}

	// Should contain envoy hostnames
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("EnvoysTable fragment missing hostname")
	}
}

// --- Task 2: Config and Plans tests ---

func TestConfigHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/config/web-01.example.com", nil)
	req.SetPathValue("hostname", "web-01.example.com")
	rec := httptest.NewRecorder()

	h.Config(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Config returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Should contain syntax-highlighted YAML
	if !strings.Contains(body, "chroma") {
		t.Error("Config page missing chroma syntax highlighting")
	}

	// Should show secret: prefix NOT resolved
	if !strings.Contains(body, "secret:") {
		t.Error("Config page should show unresolved secret: references")
	}
}

func TestConfigHandler404(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/config/nonexistent.example.com", nil)
	req.SetPathValue("hostname", "nonexistent.example.com")
	rec := httptest.NewRecorder()

	h.Config(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("Config for non-existent returned %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPlansHandler(t *testing.T) {
	h := newTestHandler(t)

	planID := h.dataSource.(*mockDataSource).data.plans[0].id
	req := httptest.NewRequest(http.MethodGet, "/plans/"+planID.String(), nil)
	req.SetPathValue("id", planID.String())
	rec := httptest.NewRecorder()

	h.Plans(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Plans returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Should contain color-coded action badges
	if !strings.Contains(body, "badge--success") {
		t.Error("Plans page missing green badge for create action")
	}
	if !strings.Contains(body, "badge--info") {
		t.Error("Plans page missing blue badge for update action")
	}
	if !strings.Contains(body, "badge--danger") {
		t.Error("Plans page missing red badge for delete action")
	}
}

func TestServerBuildCompiles(t *testing.T) {
	// This test is a compile-time check -- if handler.go builds, we pass.
	// The real verification is `go build ./server/...` in CI.
	_ = &Handler{}
}

// --- Test helpers ---

type testEnvoy struct {
	id        uuid.UUID
	hostname  string
	ipAddress string
	enrolled  time.Time
	lastSeen  time.Time
	status    string
	lastRun   *testRun
}

type testRun struct {
	id           uuid.UUID
	envoyID       uuid.UUID
	hostname     string
	startedAt    time.Time
	status       string
	moduleCount  int64
	changedCount int64
	failedCount  int64
	results      []testRunResult
}

type testRunResult struct {
	moduleName   string
	resourceType string
	resourceName string
	actionTaken  string
	changed      bool
	errorMessage string
	durationMs   int64
}

type testPlan struct {
	id       uuid.UUID
	envoyID uuid.UUID
	hostname string
	created  time.Time
	results  []testPlanResult
}

type testPlanResult struct {
	moduleName    string
	resourceType  string
	resourceName  string
	plannedAction string
}

type testData struct {
	envoys      []testEnvoy
	runs       []testRun
	history    []testConvergenceHistory
	plans      []testPlan
	configDir  string
	traits     map[uuid.UUID]map[string]any
}

type testConvergenceHistory struct {
	recordedAt string
	total      int64
	converged  int64
}

// defaultTestData builds the standard test dataset.
func defaultTestData() *testData {
	now := time.Now()
	envoy1ID := uuid.New()
	envoy2ID := uuid.New()
	runID := uuid.New()
	planID := uuid.New()

	// Create a temp config dir with test YAML
	configDir, _ := os.MkdirTemp("", "vigo-config-test")
	os.WriteFile(filepath.Join(configDir, "web-01.example.com.yaml"), []byte("envoy: web-01.example.com\nvars:\n  dsn: \"secret:vigo/db/dsn\"\nmodules:\n  - name: test\n    type: file\n"), 0644)

	return &testData{
		envoys: []testEnvoy{
			{
				id:        envoy1ID,
				hostname:  "web-01.example.com",
				ipAddress: "10.0.0.1",
				enrolled:  now.Add(-24 * time.Hour),
				lastSeen:  now,
				status:    "converged",
			},
			{
				id:        envoy2ID,
				hostname:  "web-02.example.com",
				ipAddress: "10.0.0.2",
				enrolled:  now.Add(-48 * time.Hour),
				lastSeen:  now.Add(-2 * time.Hour),
				status:    "stale",
			},
		},
		runs: []testRun{
			{
				id:          runID,
				envoyID:    envoy1ID,
				hostname:    "web-01.example.com",
				startedAt:   now.Add(-5 * time.Minute),
				status:      "success",
				moduleCount: 3,
				results: []testRunResult{
					{moduleName: "nginx", resourceType: "package", resourceName: "nginx", actionTaken: "install", changed: true, durationMs: 500},
					{moduleName: "nginx", resourceType: "file", resourceName: "/etc/nginx/nginx.conf", actionTaken: "create", changed: true, durationMs: 10},
					{moduleName: "nginx", resourceType: "service", resourceName: "nginx", actionTaken: "restart", changed: true, durationMs: 200},
				},
			},
		},
		history: []testConvergenceHistory{
			{total: 10, converged: 8},
			{total: 10, converged: 9},
			{total: 10, converged: 7},
		},
		plans: []testPlan{
			{
				id:       planID,
				envoyID: envoy1ID,
				hostname: "web-01.example.com",
				created:  now.Add(-1 * time.Hour),
				results: []testPlanResult{
					{moduleName: "nginx", resourceType: "package", resourceName: "nginx", plannedAction: "create"},
					{moduleName: "nginx", resourceType: "file", resourceName: "/etc/nginx/nginx.conf", plannedAction: "update"},
					{moduleName: "cleanup", resourceType: "file", resourceName: "/tmp/old.log", plannedAction: "delete"},
					{moduleName: "skip-mod", resourceType: "service", resourceName: "skip-svc", plannedAction: "skip"},
				},
			},
		},
		configDir: configDir,
		traits: map[uuid.UUID]map[string]any{
			envoy1ID: {
				"os": map[string]any{
					"family": "debian",
					"name":   "Ubuntu",
				},
				"hardware": map[string]any{
					"cpu_count": 4,
				},
			},
		},
	}
}

// newTestHandler creates a Handler with default test data.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	td := defaultTestData()
	return newTestHandlerWithData(t, td)
}

// newTestHandlerWithData creates a Handler with the given test data.
func newTestHandlerWithData(t *testing.T, td *testData) *Handler {
	t.Helper()

	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}

	if td.configDir != "" {
		t.Cleanup(func() { os.RemoveAll(td.configDir) })
	}

	return &Handler{
		renderer:     renderer,
		dataSource:   &mockDataSource{data: td},
		baseInterval: 5 * time.Minute,
		version:      "test",
	}
}

// mockDataSource satisfies the DataSource interface with test data.
type mockDataSource struct {
	data *testData
}

func (m *mockDataSource) DashboardData(baseInterval time.Duration) DashboardPageData {
	totalEnvoys := len(m.data.envoys)
	activeCount := 0
	failureCount := 0
	driftCount := 0

	for _, c := range m.data.envoys {
		if time.Since(c.lastSeen) < time.Hour {
			activeCount++
		}
		if c.status == "failed" {
			failureCount++
		}
		if c.status == "relapsed" || c.status == "diverged" {
			driftCount++
		}
	}

	driftRate := float64(0)
	if totalEnvoys > 0 {
		driftRate = float64(driftCount) / float64(totalEnvoys) * 100
	}

	runs := make([]RunRow, 0, len(m.data.runs))
	for _, r := range m.data.runs {
		runs = append(runs, RunRow{
			ID:           r.id.String(),
			Hostname:     r.hostname,
			StartedAt:    r.startedAt,
			Status:       r.status,
			ModuleCount:  r.moduleCount,
			ChangedCount: r.changedCount,
			FailedCount:  r.failedCount,
		})
	}

	fleetRows := make([]FleetRow, 0, len(m.data.envoys))
	for _, c := range m.data.envoys {
		fleetRows = append(fleetRows, FleetRow{
			ID:       c.id.String(),
			Hostname: c.hostname,
			IP:       c.ipAddress,
			LastSeen: c.lastSeen,
			Status:   c.status,
		})
	}

	return DashboardPageData{
		PageData: PageData{
			Title:     "Dashboard",
			ActiveNav: "dashboard",
			Version:   "test",
		},
		TotalEnvoys:     totalEnvoys,
		ActiveEnvoys:    activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     runs,
		FleetRows:      fleetRows,
	}
}

func (m *mockDataSource) ConvergenceData(baseInterval time.Duration) ConvergencePageData {
	total := len(m.data.envoys)
	counts := map[string]int64{
		"converged": 0, "relapsed": 0, "diverged": 0, "failed": 0, "stale": 0, "unknown": 0,
	}

	var nonConverged []ConvergenceRow
	for _, c := range m.data.envoys {
		counts[c.status]++
		if c.status != "converged" {
			nonConverged = append(nonConverged, ConvergenceRow{
				Hostname: c.hostname,
				Status:   c.status,
				LastSeen: c.lastSeen,
			})
		}
	}

	pct := float64(0)
	if total > 0 {
		pct = float64(counts["converged"]) / float64(total) * 100
	}

	sparkData := make([]float64, 0, len(m.data.history))
	for _, h := range m.data.history {
		if h.total > 0 {
			sparkData = append(sparkData, float64(h.converged)/float64(h.total)*100)
		}
	}

	return ConvergencePageData{
		ConvergencePct: pct,
		Total:         int64(total),
		Converged:     counts["converged"],
		Relapsed:             counts["relapsed"],
		Diverged:             counts["diverged"],
		Failed:       counts["failed"],
		Offline:         counts["stale"],
		NoData:       counts["unknown"],
		SparklineData: sparkData,
		NonConverged:  nonConverged,
	}
}

func (m *mockDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := m.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalEnvoys:     d.TotalEnvoys,
		ActiveEnvoys:    d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (m *mockDataSource) EnvoysData(baseInterval time.Duration, statusFilter string, afterHostname string, limit int, search string) EnvoysPageData {
	var rows []EnvoyRow
	for _, c := range m.data.envoys {
		if statusFilter != "" && c.status != statusFilter {
			continue
		}
		rows = append(rows, EnvoyRow{
			ID:         c.id.String(),
			Hostname:   c.hostname,
			IPAddress:  c.ipAddress,
			EnrolledAt: c.enrolled,
			LastSeen:   c.lastSeen,
			Status:     c.status,
		})
	}

	return EnvoysPageData{
		PageData: PageData{
			Title:     "Envoys",
			ActiveNav: "envoys",
			Version:   "test",
		},
		Envoys:        rows,
		StatusFilter: statusFilter,
		EnvoyCount:    len(rows),
	}
}

func (m *mockDataSource) EnvoyDetailData(envoyID string, baseInterval time.Duration) (EnvoyDetailPageData, error) {
	var found *testEnvoy
	for i := range m.data.envoys {
		if m.data.envoys[i].id.String() == envoyID {
			found = &m.data.envoys[i]
			break
		}
	}
	if found == nil {
		return EnvoyDetailPageData{}, fmt.Errorf("envoy not found")
	}

	return EnvoyDetailPageData{
		PageData: PageData{
			Title:     "Envoy: " + found.hostname,
			ActiveNav: "envoys",
			Version:   "test",
		},
		Envoy: EnvoyRow{
			ID:         found.id.String(),
			Hostname:   found.hostname,
			IPAddress:  found.ipAddress,
			EnrolledAt: found.enrolled,
			LastSeen:   found.lastSeen,
			Status:     found.status,
		},
	}, nil
}

func (m *mockDataSource) EnvoyRunsData(envoyID string) (EnvoyRunsFragmentData, error) {
	var runs []EnvoyDetailRunRow
	for _, r := range m.data.runs {
		if r.envoyID.String() == envoyID {
			var results []RunResultRow
			for _, res := range r.results {
				results = append(results, RunResultRow{
					ModuleName:   res.moduleName,
					ResourceType: res.resourceType,
					ResourceName: res.resourceName,
					ActionTaken:  res.actionTaken,
					Changed:      res.changed,
					ErrorMessage: res.errorMessage,
					DurationMs:   res.durationMs,
				})
			}
			runs = append(runs, EnvoyDetailRunRow{
				RunRow: RunRow{
					ID:           r.id.String(),
					Hostname:     r.hostname,
					StartedAt:    r.startedAt,
					Status:       r.status,
					ModuleCount:  r.moduleCount,
					ChangedCount: r.changedCount,
					FailedCount:  r.failedCount,
				},
				Results: results,
			})
		}
	}
	return EnvoyRunsFragmentData{Runs: runs}, nil
}

func (m *mockDataSource) EnvoyTraitsData(envoyID string) (EnvoyTraitsFragmentData, error) {
	parsedID, _ := uuid.Parse(envoyID)
	traits := m.data.traits[parsedID]
	if len(traits) == 0 {
		return EnvoyTraitsFragmentData{}, nil
	}
	buf, err := json.Marshal(traits)
	if err != nil {
		return EnvoyTraitsFragmentData{}, nil
	}
	return BuildTraitsFragment(string(buf), time.Now()), nil
}

func (m *mockDataSource) RunsData(statusFilter string) RunsPageData {
	var rows []RunRow
	for _, r := range m.data.runs {
		if statusFilter != "" && r.status != statusFilter {
			continue
		}
		rows = append(rows, RunRow{
			ID:           r.id.String(),
			Hostname:     r.hostname,
			StartedAt:    r.startedAt,
			Status:       r.status,
			ModuleCount:  r.moduleCount,
			ChangedCount: r.changedCount,
			FailedCount:  r.failedCount,
		})
	}

	return RunsPageData{
		PageData: PageData{
			Title:     "Runs",
			ActiveNav: "runs",
			Version:   "test",
		},
		Runs:         rows,
		StatusFilter: statusFilter,
		RunCount:     len(rows),
	}
}

func (m *mockDataSource) RunDetailData(runID string) (RunDetailPageData, error) {
	for _, r := range m.data.runs {
		if r.id.String() == runID {
			var results []RunResultRow
			for _, res := range r.results {
				results = append(results, RunResultRow{
					ModuleName:   res.moduleName,
					ResourceType: res.resourceType,
					ResourceName: res.resourceName,
					ActionTaken:  res.actionTaken,
					Changed:      res.changed,
					ErrorMessage: res.errorMessage,
					DurationMs:   res.durationMs,
				})
			}
			return RunDetailPageData{
				PageData: PageData{
					Title:     "Run Detail",
					ActiveNav: "runs",
					Version:   "test",
				},
				EnvoyID: r.envoyID.String(),
				Run: RunRow{
					ID:           r.id.String(),
					Hostname:     r.hostname,
					StartedAt:    r.startedAt,
					Status:       r.status,
					ModuleCount:  r.moduleCount,
					ChangedCount: r.changedCount,
					FailedCount:  r.failedCount,
				},
				Results: results,
			}, nil
		}
	}
	return RunDetailPageData{}, fmt.Errorf("run not found")
}

func (m *mockDataSource) ConfigData(hostname string) (ConfigPageData, error) {
	if m.data.configDir == "" {
		return ConfigPageData{}, fmt.Errorf("no config directory")
	}
	path := filepath.Join(m.data.configDir, hostname+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigPageData{}, fmt.Errorf("config not found")
	}
	return ConfigPageData{
		PageData: PageData{
			Title:     "Config: " + hostname,
			ActiveNav: "envoys",
			Version:   "test",
		},
		Hostname: hostname,
		RawYAML:  string(data),
	}, nil
}

func (m *mockDataSource) PlanDetailData(planID string) (PlanDetailPageData, error) {
	for _, p := range m.data.plans {
		if p.id.String() == planID {
			var results []PlanResultRow
			createCount, updateCount, deleteCount, skipCount := 0, 0, 0, 0
			for _, r := range p.results {
				color := actionColor(r.plannedAction)
				results = append(results, PlanResultRow{
					ModuleName:    r.moduleName,
					ResourceType:  r.resourceType,
					ResourceName:  r.resourceName,
					PlannedAction: r.plannedAction,
					ActionColor:   color,
				})
				switch r.plannedAction {
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
				PageData: PageData{
					Title:     "Plan Detail",
					ActiveNav: "envoys",
					Version:   "test",
				},
				PlanID:      p.id.String(),
				EnvoyID:      p.envoyID.String(),
				Hostname:    p.hostname,
				CreatedAt:   p.created,
				Results:     results,
				TotalCount:  len(results),
				CreateCount: createCount,
				UpdateCount: updateCount,
				DeleteCount: deleteCount,
				SkipCount:   skipCount,
			}, nil
		}
	}
	return PlanDetailPageData{}, fmt.Errorf("plan not found")
}

func (m *mockDataSource) SecurityScanData() SecurityScanPageData {
	return SecurityScanPageData{}
}

func (m *mockDataSource) SecurityScanTraits(_ string) (map[string]string, bool) {
	return nil, false
}

func (m *mockDataSource) SecurityScanContext(_ string) (string, map[string]string, bool) {
	return "", nil, false
}

func (m *mockDataSource) EnvoyCertDetails(_, _ string) ([]CertDetail, string, bool) {
	return nil, "", false
}

func (m *mockDataSource) RiskData(_ time.Duration) RiskSummaryData {
	return RiskSummaryData{}
}

func (m *mockDataSource) RiskPostureData(_ time.Duration) RiskPosturePageData {
	return RiskPosturePageData{}
}

func (m *mockDataSource) CertExpirySummary() CertExpirySummary {
	return CertExpirySummary{}
}

func (m *mockDataSource) RunningCVESummary() RunningCVESummary {
	return RunningCVESummary{}
}

// actionColor maps a plan action to a badge CSS class.
func actionColor(action string) string {
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

func TestFilterGatedNavForFreeTier(t *testing.T) {
	nav := []DocsNavSection{
		{Title: "Getting Started", Path: "getting-started"},
		{Title: "Reference", Path: "reference"},
		{Title: "Compliance", Path: "compliance"},
		{Title: "Security", Path: "security"},
		{Title: "Operations", Path: "operations"},
	}
	filtered := filterGatedNavForFreeTier(nav)

	// Compliance and security must be dropped.
	for _, s := range filtered {
		if s.Path == "compliance" || s.Path == "security" {
			t.Errorf("free-tier nav contains gated section %q", s.Path)
		}
	}

	// Non-gated sections must survive in order.
	wantPaths := []string{"getting-started", "reference", "operations"}
	if len(filtered) != len(wantPaths) {
		t.Fatalf("filtered nav has %d sections, want %d", len(filtered), len(wantPaths))
	}
	for i, want := range wantPaths {
		if filtered[i].Path != want {
			t.Errorf("filtered[%d].Path = %q, want %q", i, filtered[i].Path, want)
		}
	}
}

func TestIsDocsPathGated(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"compliance/hipaa", true},
		{"compliance", true},
		{"security/self-protection", true},
		{"security", true},
		{"getting-started/install", false},
		{"reference/cli/config", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isDocsPathGated(c.path); got != c.want {
			t.Errorf("isDocsPathGated(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestPlatformComplianceHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/platform/compliance", nil)
	rec := httptest.NewRecorder()
	h.PlatformCompliance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PlatformCompliance returned %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Platform Compliance",
		"All transport encrypted with mTLS",
		"All frameworks",
		"HIPAA",
		"SOC 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("PlatformCompliance body missing %q", want)
		}
	}
}

func TestPlatformComplianceFrameworkFilter(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/platform/compliance?framework=soc2", nil)
	rec := httptest.NewRecorder()
	h.PlatformCompliance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PlatformCompliance soc2 filter returned %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	// SOC 2 has at least one mapped AppControl, so the empty-state message
	// must not appear.
	if strings.Contains(body, "No platform controls reference this framework") {
		t.Error("PlatformCompliance with framework=soc2 unexpectedly empty")
	}
	// The active chip should pick up btn--primary on the soc2 link.
	if !strings.Contains(body, `href="/platform/compliance?framework=soc2" class="btn btn--sm btn--primary"`) {
		t.Error("PlatformCompliance soc2 chip not marked primary")
	}
}

func TestPlatformComplianceUnknownFrameworkEmpty(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/platform/compliance?framework=does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.PlatformCompliance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PlatformCompliance unknown filter returned %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No platform controls reference this framework") {
		t.Error("PlatformCompliance with unknown framework should show empty-state message")
	}
}
