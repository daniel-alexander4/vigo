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

	"git.alexander4.io/vigo/server/fleetpressure"
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

	// Should have auto-refresh trigger. With the test handler's baseInterval
	// of 5m, the new formula min(checkin, 5m) renders as "300s".
	if !strings.Contains(body, `hx-trigger="every 300s"`) {
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

func TestEnvoysHandlerRendersFilterDrawer(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/envoys", nil)
	rec := httptest.NewRecorder()
	h.Envoys(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Envoys returned %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()

	// Drawer aside is mounted and identifiable by JS for open/close.
	if !strings.Contains(body, `id="envoy-filter-drawer"`) {
		t.Error("Envoys page missing the filter drawer aside (id=envoy-filter-drawer)")
	}
	// "Filter" toggle button is present and wired to the delegated JS hook
	// (data-action, dispatched by ui-actions.js — no inline onclick).
	if !strings.Contains(body, `data-action="toggle-filter-drawer"`) {
		t.Error("Envoys page missing the Filter toggle button")
	}
	// At least one mock-supplied pill renders with the page-scoped URL.
	// BasePath prefixes the path-relative URL the enumerator emits.
	if !strings.Contains(body, `href="/envoys?env=production"`) {
		t.Error("Envoys page missing env pill href; drawer didn't wire BasePath into pill URLs")
	}
	// Read-only conv/drift/reach chips render as <span> with the
	// chip-readonly class above the table (filtering now goes through
	// the drawer). Drawer pills are still <a>'s — that's by design;
	// this assertion is about the chip bar specifically.
	if !strings.Contains(body, "filter-bar__chip-readonly") {
		t.Error("Envoys page chips above the table should carry filter-bar__chip-readonly class")
	}
	if !strings.Contains(body, `<span class="btn btn--sm filter-bar__chip-readonly`) {
		t.Error("Envoys page filter bar should render chips as <span>, not <a>")
	}
	// Single "Fleet Envoy Status" heading over the three orthogonal-axis
	// groups, with the axes relabeled to full words.
	if !strings.Contains(body, "Fleet Envoy Status") {
		t.Error("Envoys page filter bar should carry the single Fleet Envoy Status heading")
	}
	for _, axis := range []string{"Convergence", "Drift", "Reachability"} {
		if !strings.Contains(body, ">"+axis+"<") {
			t.Errorf("Envoys page filter bar missing the %q axis sub-label", axis)
		}
	}
	// The retired terse "Failure"/"Reach" axis labels must be gone.
	for _, gone := range []string{`__label">Failure<`, `__label">Reach<`} {
		if strings.Contains(body, gone) {
			t.Errorf("Envoys page filter bar still shows the retired axis label %q", gone)
		}
	}
	// Failed is a Convergence-axis state (conv=failed), no longer mis-grouped
	// under Drift; the pill must point at the conv filter.
	if !strings.Contains(body, "Failed") || !strings.Contains(body, "conv=failed") {
		t.Error("Envoys page filter bar missing the Failed pill in the Convergence group")
	}
}

func TestEnvoysHandlerStatusFilter(t *testing.T) {
	h := newTestHandler(t)

	// Failure-axis filter; the test fixture has web-02 in "failed" and
	// web-01 in "converged".
	req := httptest.NewRequest(http.MethodGet, "/envoys?conv=failed", nil)
	rec := httptest.NewRecorder()

	h.Envoys(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Envoys filtered returned %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// failed envoy should be present
	if !strings.Contains(body, "web-02.example.com") {
		t.Error("Filtered envoys page should show failed envoy web-02.example.com")
	}

	// converged envoy should NOT be present
	if strings.Contains(body, "web-01.example.com") {
		t.Error("Filtered envoys page should NOT show converged envoy when filtering for failed")
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

func TestEnvoyFootprintFragmentRendersWhenPresent(t *testing.T) {
	h := newTestHandler(t)
	envoyID := h.dataSource.(*mockDataSource).data.envoys[0].id

	// Override the mock so EnvoyPressure returns populated values —
	// the fragment must render the warn/fail rows and the field
	// labels. The default mock returns ok=false; this exercises the
	// real branch.
	h.dataSource = &footprintMock{base: h.dataSource}

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-footprint/"+envoyID.String(), nil)
	req.SetPathValue("id", envoyID.String())
	rec := httptest.NewRecorder()

	h.EnvoyFootprintFragment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("EnvoyFootprintFragment returned %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Open file descriptors",
		"Host FD usage",
		"Recent OOM events",
		"Resident memory",
		// Agent Health banner at top (composite pill + drilldown).
		"Agent Health",
		`href="/agent-health"`,
		// Disk & Repos group showing rootfs, inodes, and repo health.
		"Disk &amp; Repos",
		"Rootfs",
		"Inode",
		"Package repositories",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("EnvoyFootprintFragment body missing %q", want)
		}
	}
	// The retired intro paragraph must not reappear.
	if strings.Contains(body, "Agent process footprint, host FD pressure") {
		t.Errorf("EnvoyFootprintFragment body still rendering retired intro paragraph")
	}
}

func TestEnvoyFootprintFragmentEmptyState(t *testing.T) {
	h := newTestHandler(t)
	envoyID := h.dataSource.(*mockDataSource).data.envoys[0].id

	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/envoy-footprint/"+envoyID.String(), nil)
	req.SetPathValue("id", envoyID.String())
	rec := httptest.NewRecorder()

	h.EnvoyFootprintFragment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("EnvoyFootprintFragment returned %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "No agent or host telemetry yet") {
		t.Errorf("expected empty-state message when EnvoyPressure returns ok=false; got: %s", rec.Body.String())
	}
}

func TestDashboardPressureFragmentRenders(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/fragments/dashboard-pressure", nil)
	rec := httptest.NewRecorder()

	h.DashboardPressureFragment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DashboardPressureFragment returned %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Agent Health") {
		t.Errorf("dashboard agent-health card missing title; body: %s", body)
	}
	if !strings.Contains(body, `href="/agent-health"`) {
		t.Errorf("dashboard agent-health card missing drilldown link to /agent-health; body: %s", body)
	}
}

// footprintMock wraps the default mock and overrides only the
// EnvoyPressure call so the populated-render path can be exercised.
type footprintMock struct {
	base DataSource
}

func (m *footprintMock) DashboardData(d, o time.Duration) DashboardPageData {
	return m.base.DashboardData(d, o)
}
func (m *footprintMock) ConvergenceData(d, o time.Duration) ConvergencePageData {
	return m.base.ConvergenceData(d, o)
}
func (m *footprintMock) DashboardStatsData(d, o time.Duration) DashboardStatsFragmentData {
	return m.base.DashboardStatsData(d, o)
}
func (m *footprintMock) EnvoysData(d, o time.Duration, conv, drift, reach, q, env, tag, role, os, arch, module, compliance, sortCol, sortDir string, offset, limit int) EnvoysPageData {
	return m.base.EnvoysData(d, o, conv, drift, reach, q, env, tag, role, os, arch, module, compliance, sortCol, sortDir, offset, limit)
}
func (m *footprintMock) EnvoyDetailData(id string, d, o time.Duration) (EnvoyDetailPageData, error) {
	return m.base.EnvoyDetailData(id, d, o)
}
func (m *footprintMock) EnvoyRunsData(id string) (EnvoyRunsFragmentData, error) {
	return m.base.EnvoyRunsData(id)
}
func (m *footprintMock) EnvoyTraitsData(id string) (EnvoyTraitsFragmentData, error) {
	return m.base.EnvoyTraitsData(id)
}
func (m *footprintMock) RunsData(s string) RunsPageData { return m.base.RunsData(s) }
func (m *footprintMock) RunDetailData(id string) (RunDetailPageData, error) {
	return m.base.RunDetailData(id)
}
func (m *footprintMock) EnvoyDriftLabel(id string) string  { return m.base.EnvoyDriftLabel(id) }
func (m *footprintMock) ConfigData(h string) (ConfigPageData, error) { return m.base.ConfigData(h) }
func (m *footprintMock) PlanDetailData(id string) (PlanDetailPageData, error) {
	return m.base.PlanDetailData(id)
}
func (m *footprintMock) SecurityScanData() SecurityScanPageData { return m.base.SecurityScanData() }
func (m *footprintMock) SecurityScanTraits(id string) (map[string]string, bool) {
	return m.base.SecurityScanTraits(id)
}
func (m *footprintMock) SecurityScanContext(id string) (string, map[string]string, bool) {
	return m.base.SecurityScanContext(id)
}
func (m *footprintMock) RiskData(d, o time.Duration) RiskSummaryData { return m.base.RiskData(d, o) }
func (m *footprintMock) RiskPostureData(d, o time.Duration) RiskPosturePageData {
	return m.base.RiskPostureData(d, o)
}
func (m *footprintMock) CertExpirySummary() CertExpirySummary { return m.base.CertExpirySummary() }
func (m *footprintMock) EnvoyCertDetails(id, b string) ([]CertDetail, string, bool) {
	return m.base.EnvoyCertDetails(id, b)
}
func (m *footprintMock) RunningCVESummary() RunningCVESummary { return m.base.RunningCVESummary() }
func (m *footprintMock) FleetPressure() fleetpressure.FleetSummary {
	return fleetpressure.FleetSummary{
		MaxOpenFDs:         640,
		MaxOpenFDsHost:     "alpha.example.com",
		MaxOpenFDsID:       "00000000-0000-0000-0000-000000000001",
		MaxOpenFDsSeverity: fleetpressure.Fail,
		HighFDPctCount:     2,
		HighFDPctExample:   "bravo.example.com",
		RecentOomCount:     0,
		TotalReporting:     5,
	}
}
func (m *footprintMock) FleetPressureDetail() (fleetpressure.FleetSummary, []fleetpressure.EnvoyPressure) {
	return m.FleetPressure(), nil
}
func (m *footprintMock) EnvoyFreshness(id string) (time.Time, time.Duration, bool) {
	return m.base.EnvoyFreshness(id)
}
func (m *footprintMock) EnvoyPressure(_ string) (fleetpressure.EnvoyPressure, bool) {
	return fleetpressure.EnvoyPressure{
		ID:                "00000000-0000-0000-0000-000000000001",
		Hostname:          "alpha.example.com",
		RSSBytes:          fleetpressure.Metric{Value: 64 * 1024 * 1024, Present: true, Severity: fleetpressure.OK},
		OpenFDs:           fleetpressure.Metric{Value: 640, Present: true, Severity: fleetpressure.Fail},
		Threads:           fleetpressure.Metric{Value: 12, Present: true, Severity: fleetpressure.OK},
		Descendants:       fleetpressure.Metric{Value: 1, Present: true, Severity: fleetpressure.OK},
		DescendantOpenFDs: fleetpressure.Metric{Value: 50, Present: true, Severity: fleetpressure.OK},
		UptimeSecs:        fleetpressure.Metric{Value: 86400, Present: true, Severity: fleetpressure.OK},
		FDUsed:            fleetpressure.Metric{Value: 8192, Present: true, Severity: fleetpressure.OK},
		FDMax:             fleetpressure.Metric{Value: 32768, Present: true, Severity: fleetpressure.OK},
		FDPct:             fleetpressure.PercentMetric{Value: 25.0, Present: true, Severity: fleetpressure.OK},
		ProcCount:         fleetpressure.Metric{Value: 200, Present: true, Severity: fleetpressure.OK},
		OomRecent:         fleetpressure.Metric{Value: 0, Present: true, Severity: fleetpressure.OK},
		RootfsPct:         fleetpressure.PercentMetric{Value: 46.0, Present: true, Severity: fleetpressure.OK},
		InodePct:          fleetpressure.PercentMetric{Value: 12.0, Present: true, Severity: fleetpressure.OK},
		RepoHealth:        fleetpressure.RepoHealthMetric{Status: "ok", Present: true, Severity: fleetpressure.OK},
		Health:            fleetpressure.Fail,
	}, true
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
	for _, col := range []string{"Run", "Hostname", "Status", "Configcrates"} {
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
		envoyID:     td.envoys[1].id,
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

	// Agent-health column replaces the old Root FS + Repos pair —
	// one composite pill per row, drilling to /agent-health.
	if !strings.Contains(body, "Agent Health") {
		t.Errorf("EnvoysTable fragment missing 'Agent Health' column header; body: %s", body)
	}
	// Pill drills into the per-envoy Agent Health tab — landing on the
	// fleet /agent-health view is one extra click away from the hostname
	// column. Drilldown target is /envoys/<id>#footprint.
	if !strings.Contains(body, `#footprint" class="badge-link"`) {
		t.Errorf("EnvoysTable fragment missing per-envoy #footprint drilldown link; body: %s", body)
	}
	// The two folded columns must not reappear.
	if strings.Contains(body, ">Root FS<") || strings.Contains(body, ">Repos<") {
		t.Errorf("EnvoysTable fragment still rendering retired Root FS / Repos columns; body: %s", body)
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
}

type testRun struct {
	id           uuid.UUID
	envoyID      uuid.UUID
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
	envoyID  uuid.UUID
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
	envoys    []testEnvoy
	runs      []testRun
	history   []testConvergenceHistory
	plans     []testPlan
	configDir string
	traits    map[uuid.UUID]map[string]any
}

type testConvergenceHistory struct {
	total     int64
	converged int64
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
	os.WriteFile(filepath.Join(configDir, "web-01.example.com.yaml"), []byte("envoy: web-01.example.com\nvars:\n  dsn: \"secret:vigo/db/dsn\"\nconfigcrates:\n  - name: test\n    type: file\n"), 0644)

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
				status:    "failed",
			},
		},
		runs: []testRun{
			{
				id:          runID,
				envoyID:     envoy1ID,
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
				envoyID:  envoy1ID,
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

func (m *mockDataSource) DashboardData(baseInterval, offlineThreshold time.Duration) DashboardPageData {
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
		if c.status == "changed" || c.status == "diverged" {
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
			ConfigcrateCount:  r.moduleCount,
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
		TotalEnvoys:    totalEnvoys,
		ActiveEnvoys:   activeCount,
		RecentFailures: failureCount,
		DriftRate:      driftRate,
		RecentRuns:     runs,
		FleetRows:      fleetRows,
	}
}

func (m *mockDataSource) ConvergenceData(baseInterval, offlineThreshold time.Duration) ConvergencePageData {
	total := len(m.data.envoys)
	counts := map[string]int64{
		"converged": 0, "changed": 0, "diverged": 0, "failed": 0, "stale": 0, "unknown": 0,
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
		Total:          int64(total),
		Converged:      counts["converged"],
		Degraded:       counts["degraded"],
		Failed:         counts["failed"],
		NoData:         counts["unknown"],
		Changed:        counts["changed"],
		Diverged:       counts["diverged"],
		Offline:        counts["stale"],
		SparklineData:  sparkData,
		NonConverged:   nonConverged,
	}
}

func (m *mockDataSource) DashboardStatsData(baseInterval, offlineThreshold time.Duration) DashboardStatsFragmentData {
	d := m.DashboardData(baseInterval, offlineThreshold)
	return DashboardStatsFragmentData{
		TotalEnvoys:    d.TotalEnvoys,
		ActiveEnvoys:   d.ActiveEnvoys,
		RecentFailures: d.RecentFailures,
		DriftRate:      d.DriftRate,
	}
}

func (m *mockDataSource) EnvoysData(baseInterval, offlineThreshold time.Duration, convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, moduleFilter, complianceFilter, sortCol, sortDir string, offset, limit int) EnvoysPageData {
	var rows []EnvoyRow
	for _, c := range m.data.envoys {
		if convFilter != "" && c.status != convFilter {
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

	total := len(rows)
	if offset < 0 {
		offset = 0
	}
	var page []EnvoyRow
	if offset < total {
		end := total
		if limit > 0 && offset+limit < total {
			end = offset + limit
		}
		page = rows[offset:end]
	}

	active := 0
	for _, v := range []string{convFilter, driftFilter, reachFilter, search, envFilter, tagFilter, roleFilter, osFilter, archFilter, moduleFilter, complianceFilter} {
		if v != "" {
			active++
		}
	}

	return EnvoysPageData{
		PageData: PageData{
			Title:     "Envoys",
			ActiveNav: "envoys",
			Version:   "test",
		},
		Envoys:            page,
		ConvFilter:        convFilter,
		DriftFilter:       driftFilter,
		ReachFilter:       reachFilter,
		Search:            search,
		SortColumn:        sortCol,
		SortDir:           sortDir,
		EnvoyCount:        len(page),
		MatchingTotal:     total,
		Offset:            offset,
		Limit:             limit,
		BasePath:          "/envoys",
		ActiveFilterCount: active,
		FilterAxes: []FilterAxis{
			{Key: "conv", Label: "Failure", IsEnum: true, Choices: []FilterChoice{
				{Value: "converged", Count: 1, URL: "?conv=converged"},
				{Value: "failed", Count: 1, URL: "?conv=failed"},
			}},
			{Key: "env", Label: "Env", Choices: []FilterChoice{
				{Value: "production", Count: 1, URL: "?env=production"},
			}},
		},
	}
}

func (m *mockDataSource) EnvoyDetailData(envoyID string, baseInterval, offlineThreshold time.Duration) (EnvoyDetailPageData, error) {
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
					ConfigcrateName:   res.moduleName,
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
					ConfigcrateCount:  r.moduleCount,
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
			ConfigcrateCount:  r.moduleCount,
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
					ConfigcrateName:   res.moduleName,
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
					ConfigcrateCount:  r.moduleCount,
					ChangedCount: r.changedCount,
					FailedCount:  r.failedCount,
				},
				Results: results,
			}, nil
		}
	}
	return RunDetailPageData{}, fmt.Errorf("run not found")
}

func (m *mockDataSource) EnvoyDriftLabel(envoyID string) string {
	return ""
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
					ConfigcrateName:    r.moduleName,
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
				EnvoyID:     p.envoyID.String(),
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

func (m *mockDataSource) EnvoyFreshness(_ string) (time.Time, time.Duration, bool) {
	return time.Time{}, 0, false
}

func (m *mockDataSource) EnvoyCertDetails(_, _ string) ([]CertDetail, string, bool) {
	return nil, "", false
}

func (m *mockDataSource) RiskData(_, _ time.Duration) RiskSummaryData {
	return RiskSummaryData{}
}

func (m *mockDataSource) RiskPostureData(_, _ time.Duration) RiskPosturePageData {
	return RiskPosturePageData{}
}

func (m *mockDataSource) CertExpirySummary() CertExpirySummary {
	return CertExpirySummary{}
}

func (m *mockDataSource) RunningCVESummary() RunningCVESummary {
	return RunningCVESummary{}
}

func (m *mockDataSource) FleetPressure() fleetpressure.FleetSummary {
	return fleetpressure.FleetSummary{}
}

func (m *mockDataSource) FleetPressureDetail() (fleetpressure.FleetSummary, []fleetpressure.EnvoyPressure) {
	return fleetpressure.FleetSummary{}, nil
}

func (m *mockDataSource) EnvoyPressure(_ string) (fleetpressure.EnvoyPressure, bool) {
	return fleetpressure.EnvoyPressure{}, false
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

// DocsPage must refuse, by direct URL, any top-level section that isn't in
// the published-section allowlist (adr/, internal.docs/) — the same rule
// the nav builder uses — even when the files are present on disk. Root
// pages and published sections must still be reachable (i.e. not 404 by
// the allowlist guard). Guards against the non-operator-docs leak.
func TestDocsPageRefusesNonPublishedSection(t *testing.T) {
	docsDir := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(docsDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("overview.md", "# Overview\n")
	mk("reference/_index.md", "# Reference\n")
	mk("reference/api.md", "# API\n")
	mk("internal.docs/_index.md", "# Internal\n")
	mk("internal.docs/strategy/pricing.md", "# Secret pricing\n")
	mk("adr/_index.md", "# ADRs\n")

	h := newTestHandler(t)
	h.docsDir = docsDir
	h.docsNav = buildDocsNav(docsDir)

	// internal.docs / adr must 404 by direct URL even though they exist.
	for _, p := range []string{"internal.docs", "internal.docs/strategy/pricing", "adr", "adr/_index"} {
		req := httptest.NewRequest(http.MethodGet, "/docs/"+p, nil)
		req.SetPathValue("path", p)
		rec := httptest.NewRecorder()
		h.DocsPage(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("DocsPage(%q) = %d, want 404 (non-published section must not serve)", p, rec.Code)
		}
	}

	// Published section + root page must not be blocked by the allowlist guard.
	for _, p := range []string{"reference/api", "overview"} {
		req := httptest.NewRequest(http.MethodGet, "/docs/"+p, nil)
		req.SetPathValue("path", p)
		rec := httptest.NewRecorder()
		h.DocsPage(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("DocsPage(%q) = 404, want it served (published/root content)", p)
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
