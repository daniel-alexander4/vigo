// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/audit"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
)

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
			name: "wildcard fleet-wide on",
			cfg: config.SwarmSection{
				Enabled:  []string{"*"},
				Filecast: config.FilecastSection{Enabled: []string{"*"}},
			},
		},
		{
			name: "explicit allow set with hosts",
			cfg: config.SwarmSection{
				Enabled:  []string{"annlap", "plex"},
				Filecast: config.FilecastSection{Enabled: []string{"annlap", "plex"}},
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
	h.SetSwarmConfig(config.SwarmSection{Enabled: nil})
	ok, reason := h.payloadGate()
	if ok {
		t.Fatal("expected denial when swarm.enabled is empty")
	}
	if !strings.Contains(reason, "fleet-wide") {
		t.Errorf("reason should call out fleet-wide kill switch, got %q", reason)
	}
}

func TestPayloadGate_DeniesWhenPayloadDisabled(t *testing.T) {
	h := &LodeHandler{}
	h.SetSwarmConfig(config.SwarmSection{
		Enabled:  []string{"*"},
		Filecast: config.FilecastSection{Enabled: nil},
	})
	ok, reason := h.payloadGate()
	if ok {
		t.Fatal("expected denial when swarm.filecast.enabled is empty")
	}
	if !strings.Contains(reason, "filecast subsystem") {
		t.Errorf("reason should call out filecast subsystem, got %q", reason)
	}
}

func TestDistribute_GatedReturns403AndAudits(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()

	cfg := config.SwarmSection{
		Enabled:  []string{"*"},
		Filecast: config.FilecastSection{Enabled: nil},
	}
	h := newGatedHandler(t, cfg, queries)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/distribute", strings.NewReader("ignored"))
	req.Header.Set("X-Filecast-Label", "anything")
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "filecast subsystem") {
		t.Errorf("body should explain the gate, got: %s", rec.Body.String())
	}

	// Verify the denial event hash-chained into the audit log.
	row := sqlDB.QueryRow(`SELECT event_type, summary FROM audit_entries WHERE event_type = 'swarm.filecast.denied' LIMIT 1`)
	var eventType, summary string
	if err := row.Scan(&eventType, &summary); err != nil {
		t.Fatalf("expected swarm.filecast.denied audit row, got error: %v", err)
	}
	if !strings.Contains(summary, "filecast subsystem") {
		t.Errorf("audit summary should explain the gate, got %q", summary)
	}
}

func TestDistribute_RejectsOversizeViaMaxSizeBytes(t *testing.T) {
	// Server-side enforcement: when swarm.filecast.max_size_bytes is set
	// and the request exceeds it, distribute returns 413 before writing
	// the payload to disk. Closes the bandwidth-glutton vector — a
	// 5 GiB upload to a fleet that wanted 100 MiB caps should never
	// hit the seedDir.
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()
	seedDir := t.TempDir()
	h := NewLodeHandler(seedDir, nil, nil, nil, queries)
	h.SetSwarmConfig(config.SwarmSection{
		Enabled: []string{"*"},
		Filecast: config.FilecastSection{
			Enabled:      []string{"*"},
			MaxSizeBytes: 100, // 100-byte cap for the test
		},
	})
	h.SetAuditWriter(audit.NewWriter(queries))

	body := strings.Repeat("x", 200) // 200 bytes — over the 100-byte cap
	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/distribute", strings.NewReader(body))
	req.Header.Set("X-Filecast-Label", "oversize")
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 from max_size_bytes guard, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max_size_bytes") {
		t.Errorf("body should reference max_size_bytes, got: %s", rec.Body.String())
	}
}

func TestDistribute_PicksUpDefaultRetention(t *testing.T) {
	// Server-side fallback: when --retention is omitted (no
	// X-Payload-Retention header) and swarm.filecast.default_retention
	// is set, the manifest should record the configured default.
	// Compile-time fallback is "7d" — this test pins a non-default to
	// prove the config value is being read, not the constant.
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()
	seedDir := t.TempDir()
	h := NewLodeHandler(seedDir, nil, nil, nil, queries)
	h.SetSwarmConfig(config.SwarmSection{
		Enabled: []string{"*"},
		Filecast: config.FilecastSection{
			Enabled:          []string{"*"},
			DefaultRetention: "30d",
		},
	})
	h.SetAuditWriter(audit.NewWriter(queries))

	body := "small"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/distribute", strings.NewReader(body))
	req.Header.Set("X-Filecast-Label", "ret-test")
	// Note: no X-Payload-Retention header — we want the default to fire.
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// The manifest sidecar should carry the operator-configured
	// default retention. Reading the sidecar is the most direct way
	// to assert what was recorded — peer tracker / manifest store
	// both consume the same string.
	manifest, err := os.ReadFile(seedDir + "/ret-test.swarm")
	if err != nil {
		t.Fatalf("read manifest sidecar: %v", err)
	}
	if !strings.Contains(string(manifest), `"retention":"30d"`) {
		t.Errorf("manifest sidecar should contain the configured default retention 30d, got: %s", manifest)
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
	h.SetSwarmConfig(config.SwarmSection{
		Enabled:  []string{"*"},
		Filecast: config.FilecastSection{Enabled: []string{"*"}},
	})
	h.SetAuditWriter(audit.NewWriter(queries))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/distribute", strings.NewReader(""))
	req.Header.Set("X-Filecast-Label", "anything")
	rec := httptest.NewRecorder()

	h.Distribute(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (no seedDir), got %d", rec.Code)
	}
}

// TestFilecastPurge_DropsRevokedKeepsActive proves the purge verb GCs
// revoked tombstones while leaving active distributions untouched.
func TestFilecastPurge_DropsRevokedKeepsActive(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()

	const (
		active   = "1111111111111111111111111111111111111111111111111111111111111111"
		revokedA = "2222222222222222222222222222222222222222222222222222222222222222"
		revokedB = "3333333333333333333333333333333333333333333333333333333333333333"
	)
	manifest := swarm.NewManifestStore()
	manifest.RecordSeed(active, "keep-me", "*", "", "server", 10)
	manifest.RecordSeed(revokedA, "drop-a", "*", "", "server", 10)
	manifest.Revoke(revokedA)
	manifest.RecordSeed(revokedB, "drop-b", "*", "", "server", 10)
	manifest.Revoke(revokedB)

	h := NewLodeHandler(t.TempDir(), swarm.NewPeerTracker(60*time.Second), nil, manifest, queries)
	h.SetAuditWriter(audit.NewWriter(queries))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/purge", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Purged int `json:"purged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Purged != 2 {
		t.Fatalf("purged = %d, want 2", resp.Purged)
	}
	if _, ok := manifest.Get(active); !ok {
		t.Error("active entry was purged — purge must leave active distributions alone")
	}
	if _, ok := manifest.Get(revokedA); ok {
		t.Error("revoked entry survived the purge")
	}
}

// TestFilecastPurge_OlderThanScopesByAge proves ?older_than scopes the
// purge: a freshly-revoked tombstone is younger than the window and survives.
func TestFilecastPurge_OlderThanScopesByAge(t *testing.T) {
	sqlDB, queries := openAuditTestDB(t)
	defer sqlDB.Close()

	const fresh = "4444444444444444444444444444444444444444444444444444444444444444"
	manifest := swarm.NewManifestStore()
	manifest.RecordSeed(fresh, "fresh", "*", "", "server", 10)
	manifest.Revoke(fresh)

	h := NewLodeHandler(t.TempDir(), swarm.NewPeerTracker(60*time.Second), nil, manifest, queries)
	h.SetAuditWriter(audit.NewWriter(queries))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/swarm/filecast/purge?older_than=24h", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, req)

	var resp struct {
		Purged int `json:"purged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Purged != 0 {
		t.Fatalf("purged = %d, want 0 (fresh tombstone is younger than 24h)", resp.Purged)
	}
	if _, ok := manifest.Get(fresh); !ok {
		t.Error("fresh revoked tombstone should survive an older_than=24h purge")
	}
}
