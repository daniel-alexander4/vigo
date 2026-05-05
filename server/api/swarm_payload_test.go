// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
)

func boolp(v bool) *bool { return &v }

// newGatedHandler builds a LodeHandler with just enough plumbing to
// exercise the gate. seedDir is set non-empty so the existing 503 guard
// doesn't short-circuit before the gate check.
func newGatedHandler(t *testing.T, swarmCfg config.SwarmSection, queries *db.Queries) *LodeHandler {
	t.Helper()
	h := NewLodeHandler(t.TempDir(), nil, nil, nil, queries)
	h.SetSwarmConfig(swarmCfg)
	h.SetAuditWriter(audit.NewWriter(queries))
	return h
}

func TestPayloadGate_AllowsWhenAllEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.SwarmSection
	}{
		{
			name: "payload.enabled nil (operator default)",
			cfg:  config.SwarmSection{Enabled: true},
		},
		{
			name: "payload.enabled explicit true",
			cfg: config.SwarmSection{
				Enabled: true,
				Payload: config.SwarmPayloadSection{Enabled: boolp(true)},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &LodeHandler{}
			h.SetSwarmConfig(c.cfg)
			ok, reason := h.payloadGate()
			if !ok || reason != "" {
				t.Fatalf("expected (true, \"\"), got (%v, %q)", ok, reason)
			}
		})
	}
}

func TestPayloadGate_DeniesWhenFleetDisabled(t *testing.T) {
	h := &LodeHandler{}
	h.SetSwarmConfig(config.SwarmSection{Enabled: false})
	ok, reason := h.payloadGate()
	if ok {
		t.Fatal("expected denial when swarm.enabled is false")
	}
	if !strings.Contains(reason, "fleet-wide") {
		t.Errorf("reason should call out fleet-wide kill switch, got %q", reason)
	}
}

func TestPayloadGate_DeniesWhenPayloadDisabled(t *testing.T) {
	h := &LodeHandler{}
	h.SetSwarmConfig(config.SwarmSection{
		Enabled: true,
		Payload: config.SwarmPayloadSection{Enabled: boolp(false)},
	})
	ok, reason := h.payloadGate()
	if ok {
		t.Fatal("expected denial when swarm.payload.enabled is false")
	}
	if !strings.Contains(reason, "payload substrate") {
		t.Errorf("reason should call out payload substrate, got %q", reason)
	}
}

func TestDistribute_GatedReturns403AndAudits(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()

	cfg := config.SwarmSection{
		Enabled: true,
		Payload: config.SwarmPayloadSection{Enabled: boolp(false)},
	}
	h := newGatedHandler(t, cfg, queries)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/payload/distribute", strings.NewReader("ignored"))
	req.Header.Set("X-Payload-Label", "anything")
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "payload substrate") {
		t.Errorf("body should explain the gate, got: %s", rec.Body.String())
	}

	// Verify the denial event hash-chained into the audit log.
	row := sqlDB.QueryRow(`SELECT event_type, summary FROM audit_entries WHERE event_type = 'swarm.payload.denied' LIMIT 1`)
	var eventType, summary string
	if err := row.Scan(&eventType, &summary); err != nil {
		t.Fatalf("expected swarm.payload.denied audit row, got error: %v", err)
	}
	if !strings.Contains(summary, "payload substrate") {
		t.Errorf("audit summary should explain the gate, got %q", summary)
	}
}

func TestDistribute_AllowedWhenNotGated_StillGuardsOnSeedDir(t *testing.T) {
	// Sanity: when the gate allows, the existing seedDir-empty 503 guard
	// continues to apply on a handler built with NewLodeHandler("", ...).
	// This pins the gate as a pre-check that doesn't break the existing
	// substrate-not-enabled response.
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()
	h := NewLodeHandler("", nil, nil, nil, queries)
	h.SetSwarmConfig(config.SwarmSection{Enabled: true})
	h.SetAuditWriter(audit.NewWriter(queries))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/payload/distribute", strings.NewReader(""))
	req.Header.Set("X-Payload-Label", "anything")
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (no seedDir), got %d", rec.Code)
	}
}
