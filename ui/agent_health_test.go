// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/fleetpressure"
)

// agentHealthMock wraps the default mock and serves a populated
// FleetPressureDetail so the /agent-health page renders the table path.
type agentHealthMock struct {
	DataSource
}

func (m *agentHealthMock) FleetPressureDetail() (fleetpressure.FleetSummary, []fleetpressure.EnvoyPressure) {
	return fleetpressure.FleetSummary{
			WorstSeverity: fleetpressure.Fail,
			DegradedCount: 2,
			TotalReporting: 3,
		}, []fleetpressure.EnvoyPressure{
			{
				ID: "00000000-0000-0000-0000-000000000001", Hostname: "alpha",
				OpenFDs:    fleetpressure.Metric{Value: 750, Present: true, Severity: fleetpressure.Fail},
				FDPct:      fleetpressure.PercentMetric{Value: 60.0, Present: true, Severity: fleetpressure.Warn},
				RootfsPct:  fleetpressure.PercentMetric{Value: 95.0, Present: true, Severity: fleetpressure.Fail},
				InodePct:   fleetpressure.PercentMetric{Value: 10.0, Present: true, Severity: fleetpressure.OK},
				RepoHealth: fleetpressure.RepoHealthMetric{Status: "warn", Warnings: 3, Present: true, Severity: fleetpressure.Warn},
				Health:     fleetpressure.Fail,
			},
			{
				ID: "00000000-0000-0000-0000-000000000002", Hostname: "bravo",
				OpenFDs: fleetpressure.Metric{Value: 100, Present: true, Severity: fleetpressure.OK},
				Health:  fleetpressure.OK,
			},
		}
}

func TestAgentHealthPage_RendersTableAndSummary(t *testing.T) {
	base := newTestHandler(t)
	base.dataSource = &agentHealthMock{DataSource: base.dataSource}
	req := httptest.NewRequest("GET", "/agent-health", nil)
	rec := httptest.NewRecorder()
	base.AgentHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Agent Health", "Worst Level", "Degraded Envoys", "Reporting",
		"alpha", "750", "95.0%", "warn", "Failing",
		`href="/envoys/00000000-0000-0000-0000-000000000001#footprint"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("agent-health page missing %q", want)
		}
	}
	// Worst-first sort: alpha (Fail) should appear before bravo (OK).
	if strings.Index(body, "alpha") > strings.Index(body, "bravo") {
		t.Error("expected Fail-severity envoy (alpha) to sort before OK envoy (bravo)")
	}
}

func TestAgentHealthPage_EmptyState(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest("GET", "/agent-health", nil)
	rec := httptest.NewRecorder()
	h.AgentHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No envoys reporting agent-health traits yet") {
		t.Errorf("expected empty-state message when FleetPressureDetail returns empty; got: %s", body)
	}
}
