// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package ui

import (
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
	for _, want := range []string{"Total Thralls", "Active (1h)", "Recent Failures", "Drift Rate"} {
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

	// Should contain sparkline SVG (if history data exists)
	if !strings.Contains(body, "sparkline") {
		t.Error("Compliance page missing sparkline")
	}
}

func TestComplianceHandlerShowsNonCompliant(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}

	td := &testData{
		thralls: []testThrall{
			{
				id:       uuid.New(),
				hostname: "ok-thrall.example.com",
				lastSeen: time.Now(),
				status:   "compliant",
			},
			{
				id:       uuid.New(),
				hostname: "drifted-thrall.example.com",
				lastSeen: time.Now(),
				status:   "drifted",
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
	if !strings.Contains(body, "drifted-thrall.example.com") {
		t.Error("Compliance page should show non-compliant thrall hostname")
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
		{"Thralls", "/thralls", h.Thralls},
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

func TestThrallsHandler(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/thralls", nil)
	rec := httptest.NewRecorder()

	h.Thralls(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Thralls returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Table should have expected column headers
	for _, col := range []string{"Hostname", "IP Address", "Enrolled", "Last Seen", "Status"} {
		if !strings.Contains(body, col) {
			t.Errorf("Thralls page missing column header %q", col)
		}
	}

	// Should contain test thrall hostnames
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("Thralls page missing hostname web-01.example.com")
	}

	// Should show status badge
	if !strings.Contains(body, "badge") {
		t.Error("Thralls page missing status badge")
	}
}

func TestThrallsHandlerStatusFilter(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/thralls?status=stale", nil)
	rec := httptest.NewRecorder()

	h.Thralls(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Thralls filtered returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// stale thrall should be present
	if !strings.Contains(body, "web-02.example.com") {
		t.Error("Filtered thralls page should show stale thrall web-02.example.com")
	}

	// compliant thrall should NOT be present
	if strings.Contains(body, "web-01.example.com") {
		t.Error("Filtered thralls page should NOT show compliant thrall when filtering for stale")
	}
}

func TestThrallDetailHandler(t *testing.T) {
	h := newTestHandler(t)

	thrallID := h.dataSource.(*mockDataSource).data.thralls[0].id
	req := httptest.NewRequest(http.MethodGet, "/thralls/"+thrallID.String(), nil)
	req.SetPathValue("id", thrallID.String())
	rec := httptest.NewRecorder()

	h.ThrallDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ThrallDetail returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Should contain thrall hostname
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("ThrallDetail page missing thrall hostname")
	}

	// Should contain facts panel
	if !strings.Contains(body, "facts") {
		t.Error("ThrallDetail page missing facts panel")
	}
}

func TestThrallDetailHandler404(t *testing.T) {
	h := newTestHandler(t)

	nonExistent := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/thralls/"+nonExistent.String(), nil)
	req.SetPathValue("id", nonExistent.String())
	rec := httptest.NewRecorder()

	h.ThrallDetail(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("ThrallDetail for non-existent returned %d, want %d", rec.Code, http.StatusNotFound)
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
		thrallID:    td.thralls[1].id,
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
		t.Error("RunDetail page missing thrall hostname")
	}
}

func TestThrallsTableFragment(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/thralls-table", nil)
	rec := httptest.NewRecorder()

	h.ThrallsTable(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ThrallsTable returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// Fragment should NOT contain full HTML wrapper
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<html") {
		t.Error("ThrallsTable fragment should not contain full HTML document")
	}

	// Should contain thrall hostnames
	if !strings.Contains(body, "web-01.example.com") {
		t.Error("ThrallsTable fragment missing hostname")
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

type testThrall struct {
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
	thrallID       uuid.UUID
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
	thrallID uuid.UUID
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
	thralls      []testThrall
	runs       []testRun
	history    []testComplianceHistory
	plans      []testPlan
	configDir  string
	facts      map[uuid.UUID]map[string]any
}

type testComplianceHistory struct {
	recordedAt string
	total      int64
	compliant  int64
}

// defaultTestData builds the standard test dataset.
func defaultTestData() *testData {
	now := time.Now()
	thrall1ID := uuid.New()
	thrall2ID := uuid.New()
	runID := uuid.New()
	planID := uuid.New()

	// Create a temp config dir with test YAML
	configDir, _ := os.MkdirTemp("", "hobnob-config-test")
	os.WriteFile(filepath.Join(configDir, "web-01.example.com.yaml"), []byte("thrall: web-01.example.com\nvars:\n  dsn: \"secret:hobnob/db/dsn\"\nmodules:\n  - name: test\n    type: file\n"), 0644)

	return &testData{
		thralls: []testThrall{
			{
				id:        thrall1ID,
				hostname:  "web-01.example.com",
				ipAddress: "10.0.0.1",
				enrolled:  now.Add(-24 * time.Hour),
				lastSeen:  now,
				status:    "compliant",
			},
			{
				id:        thrall2ID,
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
				thrallID:    thrall1ID,
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
		history: []testComplianceHistory{
			{total: 10, compliant: 8},
			{total: 10, compliant: 9},
			{total: 10, compliant: 7},
		},
		plans: []testPlan{
			{
				id:       planID,
				thrallID: thrall1ID,
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
		facts: map[uuid.UUID]map[string]any{
			thrall1ID: {
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
	totalThralls := len(m.data.thralls)
	activeCount := 0
	failureCount := 0
	driftedCount := 0

	for _, c := range m.data.thralls {
		if time.Since(c.lastSeen) < time.Hour {
			activeCount++
		}
		if c.status == "failing" {
			failureCount++
		}
		if c.status == "drifted" {
			driftedCount++
		}
	}

	driftRate := float64(0)
	if totalThralls > 0 {
		driftRate = float64(driftedCount) / float64(totalThralls) * 100
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

	return DashboardPageData{
		PageData: PageData{
			Title:     "Dashboard",
			ActiveNav: "dashboard",
			Version:   "test",
		},
		TotalThralls:     totalThralls,
		ActiveThralls:    activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     runs,
	}
}

func (m *mockDataSource) ComplianceData(baseInterval time.Duration) CompliancePageData {
	total := len(m.data.thralls)
	counts := map[string]int64{
		"compliant": 0, "drifted": 0, "failing": 0, "stale": 0, "unknown": 0,
	}

	var nonCompliant []ComplianceRow
	for _, c := range m.data.thralls {
		counts[c.status]++
		if c.status != "compliant" {
			nonCompliant = append(nonCompliant, ComplianceRow{
				Hostname: c.hostname,
				Status:   c.status,
				LastSeen: c.lastSeen,
			})
		}
	}

	pct := float64(0)
	if total > 0 {
		pct = float64(counts["compliant"]) / float64(total) * 100
	}

	sparkData := make([]float64, 0, len(m.data.history))
	for _, h := range m.data.history {
		if h.total > 0 {
			sparkData = append(sparkData, float64(h.compliant)/float64(h.total)*100)
		}
	}

	return CompliancePageData{
		PageData: PageData{
			Title:     "Compliance",
			ActiveNav: "compliance",
			Version:   "test",
		},
		CompliancePct: pct,
		Total:         int64(total),
		Compliant:     counts["compliant"],
		Drifted:       counts["drifted"],
		Failing:       counts["failing"],
		Stale:         counts["stale"],
		Unknown:       counts["unknown"],
		SparklineData: sparkData,
		NonCompliant:  nonCompliant,
	}
}

func (m *mockDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := m.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalThralls:     d.TotalThralls,
		ActiveThralls:    d.ActiveThralls,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (m *mockDataSource) ThrallsData(baseInterval time.Duration, statusFilter string) ThrallsPageData {
	var rows []ThrallRow
	for _, c := range m.data.thralls {
		if statusFilter != "" && c.status != statusFilter {
			continue
		}
		rows = append(rows, ThrallRow{
			ID:         c.id.String(),
			Hostname:   c.hostname,
			IPAddress:  c.ipAddress,
			EnrolledAt: c.enrolled,
			LastSeen:   c.lastSeen,
			Status:     c.status,
		})
	}

	return ThrallsPageData{
		PageData: PageData{
			Title:     "Thralls",
			ActiveNav: "thralls",
			Version:   "test",
		},
		Thralls:        rows,
		StatusFilter: statusFilter,
		ThrallCount:    len(rows),
	}
}

func (m *mockDataSource) ThrallDetailData(thrallID string, baseInterval time.Duration) (ThrallDetailPageData, error) {
	var found *testThrall
	for i := range m.data.thralls {
		if m.data.thralls[i].id.String() == thrallID {
			found = &m.data.thralls[i]
			break
		}
	}
	if found == nil {
		return ThrallDetailPageData{}, fmt.Errorf("thrall not found")
	}

	thrallRow := ThrallRow{
		ID:         found.id.String(),
		Hostname:   found.hostname,
		IPAddress:  found.ipAddress,
		EnrolledAt: found.enrolled,
		LastSeen:   found.lastSeen,
		Status:     found.status,
	}

	var runs []ThrallDetailRunRow
	var driftHistory []DriftPoint
	for _, r := range m.data.runs {
		if r.thrallID.String() == thrallID {
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
			runs = append(runs, ThrallDetailRunRow{
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
			driftHistory = append(driftHistory, DriftPoint{
				StartedAt:    r.startedAt,
				Status:       r.status,
				ChangedCount: r.changedCount,
				FailedCount:  r.failedCount,
			})
		}
	}

	parsedID, _ := uuid.Parse(thrallID)
	facts := m.data.facts[parsedID]
	if facts == nil {
		facts = map[string]any{}
	}

	return ThrallDetailPageData{
		PageData: PageData{
			Title:     "Thrall: " + found.hostname,
			ActiveNav: "thralls",
			Version:   "test",
		},
		Thrall:         thrallRow,
		Runs:         runs,
		Facts:        facts,
		DriftHistory: driftHistory,
	}, nil
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
				ThrallID: r.thrallID.String(),
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
			ActiveNav: "thralls",
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
					ActiveNav: "thralls",
					Version:   "test",
				},
				PlanID:      p.id.String(),
				ThrallID:      p.thrallID.String(),
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
