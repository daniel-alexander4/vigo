// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grc

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/api"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/fleet"
)

// TestPushEvidence_LogsNoEndpointPath: a failed push logs the error, and
// the endpoint's path (where a token can live) must not ride along in it.
func TestPushEvidence_LogsNoEndpointPath(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const secret = "s3cr3tT0ken"
	intg := config.GRCIntegration{Name: "generic", Enabled: true, Endpoint: "http://127.0.0.1:1/hooks/" + secret}
	p := NewPusher([]config.GRCIntegration{intg}, fleet.NewFleetIndex(), nil)
	p.client.Timeout = 2 * time.Second

	p.pushEvidence(context.Background(), intg)
	// An unparsable endpoint goes through the request-creation branch.
	bad := intg
	bad.Endpoint = "http://[::1/hooks/" + secret
	p.pushEvidence(context.Background(), bad)

	out := buf.String()
	if !strings.Contains(out, "GRC push: request failed") || !strings.Contains(out, "request creation failed") {
		t.Fatalf("expected both failure logs, got:\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("log carries the endpoint path:\n%s", out)
	}
}

type fakeScorer map[string]*api.FrameworkScore

func (f fakeScorer) ComputeFrameworkScoreCtx(_ context.Context, slug string) (*api.FrameworkScore, bool) {
	s, ok := f[slug]
	return s, ok
}

// TestBuildPayload_FromCanonicalScore: the framework evidence is the
// scorer's fleet aggregate verbatim (waived and partial included), and every
// control carries its fleet-partition status.
func TestBuildPayload_FromCanonicalScore(t *testing.T) {
	score := &api.FrameworkScore{
		Slug: "pci", Name: "PCI DSS v4.0", Total: 4,
		Fleet: api.FrameworkFleetAggregate{ActiveEnvoys: 2, Total: 4, Covered: 1, Waived: 1, Partial: 1, Missing: 1, CoveragePercent: 37.5},
		Controls: []api.ControlFinding{
			{ControlID: "c-all", Title: "all"}, {ControlID: "c-waived", Title: "waived"},
			{ControlID: "c-some", Title: "some"}, {ControlID: "c-none", Title: "none"},
		},
		PerControl: map[string]api.ControlFleetPartition{
			"c-all":    {Covered: 2},
			"c-waived": {Waived: 2},
			"c-some":   {Covered: 1, Missing: 1},
			"c-none":   {Missing: 2},
		},
	}
	intg := config.GRCIntegration{Name: "generic", Frameworks: map[string]string{"pci-dss": "pci-4", "no-such-fw": "x"}}
	p := NewPusher(nil, fleet.NewFleetIndex(), fakeScorer{"pci": score})

	payload := p.buildPayload(context.Background(), intg)
	if len(payload.Frameworks) != 1 {
		t.Fatalf("frameworks = %d, want 1 (unknown key skipped)", len(payload.Frameworks))
	}
	fe := payload.Frameworks[0]
	if fe.Satisfied != 1 || fe.Waived != 1 || fe.Partial != 1 || fe.NotSatisfied != 1 || fe.TotalControls != 4 {
		t.Errorf("summary buckets = %+v, want 1/1/1/1 of 4", fe)
	}
	if fe.CoveragePercent != 37.5 || fe.EnvoyCount != 2 || fe.GRCFrameworkID != "pci-4" {
		t.Errorf("framework header = %+v", fe)
	}
	want := map[string]string{"c-all": "satisfied", "c-waived": "waived", "c-some": "partial", "c-none": "not_satisfied"}
	if len(fe.Controls) != len(want) {
		t.Fatalf("controls = %d, want %d", len(fe.Controls), len(want))
	}
	for _, c := range fe.Controls {
		if c.Status != want[c.ID] {
			t.Errorf("control %s status = %q, want %q", c.ID, c.Status, want[c.ID])
		}
	}
}
