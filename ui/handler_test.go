package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	for _, want := range []string{"Total Clients", "Active (1h)", "Recent Failures", "Drift Rate"} {
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
		clients: []testClient{
			{
				id:       uuid.New(),
				hostname: "ok-node.example.com",
				lastSeen: time.Now(),
				status:   "compliant",
			},
			{
				id:       uuid.New(),
				hostname: "drifted-node.example.com",
				lastSeen: time.Now(),
				status:   "drifted",
			},
		},
	}

	h := &Handler{
		renderer:     renderer,
		dataSource:   &mockDataSource{data: td},
		baseInterval: 30 * time.Minute,
		version:      "test",
	}

	req := httptest.NewRequest(http.MethodGet, "/compliance", nil)
	rec := httptest.NewRecorder()

	h.Compliance(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "drifted-node.example.com") {
		t.Error("Compliance page should show non-compliant client hostname")
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
		{"Clients", "/clients", h.Clients},
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

// --- Test helpers ---

type testClient struct {
	id       uuid.UUID
	hostname string
	lastSeen time.Time
	status   string
	lastRun  *testRun
}

type testRun struct {
	id           uuid.UUID
	clientID     uuid.UUID
	hostname     string
	startedAt    time.Time
	status       string
	moduleCount  int32
	changedCount int32
	failedCount  int32
}

type testData struct {
	clients   []testClient
	runs      []testRun
	history   []testComplianceHistory
}

type testComplianceHistory struct {
	recordedAt pgtype.Date
	total      int32
	compliant  int32
}

// newTestHandler creates a Handler with test data but no real DB.
// It uses the DataSource interface to inject mock data.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()

	renderer, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error: %v", err)
	}

	now := time.Now()
	td := &testData{
		clients: []testClient{
			{
				id:       uuid.New(),
				hostname: "web-01.example.com",
				lastSeen: now,
				status:   "compliant",
			},
			{
				id:       uuid.New(),
				hostname: "web-02.example.com",
				lastSeen: now.Add(-2 * time.Hour),
				status:   "stale",
			},
		},
		runs: []testRun{
			{
				id:          uuid.New(),
				hostname:    "web-01.example.com",
				startedAt:   now.Add(-5 * time.Minute),
				status:      "success",
				moduleCount: 3,
			},
		},
		history: []testComplianceHistory{
			{total: 10, compliant: 8},
			{total: 10, compliant: 9},
			{total: 10, compliant: 7},
		},
	}

	return &Handler{
		renderer:     renderer,
		dataSource:   &mockDataSource{data: td},
		baseInterval: 30 * time.Minute,
		version:      "test",
	}
}

// mockDataSource satisfies the DataSource interface with test data.
type mockDataSource struct {
	data *testData
}

func (m *mockDataSource) DashboardData(baseInterval time.Duration) DashboardPageData {
	totalClients := len(m.data.clients)
	activeCount := 0
	failureCount := 0
	driftedCount := 0

	for _, c := range m.data.clients {
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
	if totalClients > 0 {
		driftRate = float64(driftedCount) / float64(totalClients) * 100
	}

	runs := make([]RunRow, 0, len(m.data.runs))
	for _, r := range m.data.runs {
		runs = append(runs, RunRow{
			ID:           r.id,
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
		TotalClients:   totalClients,
		ActiveClients:  activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     runs,
	}
}

func (m *mockDataSource) ComplianceData(baseInterval time.Duration) CompliancePageData {
	total := len(m.data.clients)
	counts := map[string]int32{
		"compliant": 0, "drifted": 0, "failing": 0, "stale": 0, "unknown": 0,
	}

	var nonCompliant []ComplianceRow
	for _, c := range m.data.clients {
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
		CompliancePct:    pct,
		Total:            int32(total),
		Compliant:        counts["compliant"],
		Drifted:          counts["drifted"],
		Failing:          counts["failing"],
		Stale:            counts["stale"],
		Unknown:          counts["unknown"],
		SparklineData:    sparkData,
		NonCompliant:     nonCompliant,
	}
}

func (m *mockDataSource) DashboardStatsData(baseInterval time.Duration) DashboardStatsFragmentData {
	d := m.DashboardData(baseInterval)
	return DashboardStatsFragmentData{
		TotalClients:   d.TotalClients,
		ActiveClients:  d.ActiveClients,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}
