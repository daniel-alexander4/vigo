// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/testutil"
)

// Spanner Slice 2-ii-a — the /fleet-snapshot endpoint: builds, signs,
// and serves this bolt's fleet-observability state. A peer verifies
// the signature against this bolt's roster pubkey before caching.

// newFleetSnapshotHandler builds a SpannerAdmissionHandler with a
// bolt identity and a fleet index seeded with the given envoys.
func newFleetSnapshotHandler(t *testing.T, envoys []fleet.EnvoyState) (*SpannerAdmissionHandler, ed25519.PublicKey) {
	t.Helper()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)
	id := testBoltIdentity(t)

	fi := fleet.NewFleetIndex()
	for _, es := range envoys {
		fi.AddEnvoy(es, nil)
	}

	h := NewSpannerAdmissionHandler(sqlDB, queries, id, "test-spanner", "bolt.example.com", []string{"*"}, nil, nil, nil)
	h.SetFleetIndex(fi)
	return h, id.PublicKey
}

func TestFleetSnapshotEndpointSignsAndServes(t *testing.T) {
	h, pubkey := newFleetSnapshotHandler(t, []fleet.EnvoyState{
		{ID: "e1", Hostname: "h1.example.com", IP: "10.0.0.1", ConvergenceStatus: "converged",
			EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC()},
		{ID: "e2", Hostname: "h2.example.com", IP: "10.0.0.2", ConvergenceStatus: "failed",
			EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC()},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/fleet-snapshot", nil)
	h.handleFleetSnapshot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("fleet-snapshot returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var snap spanner.FleetSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode fleet snapshot: %v", err)
	}
	if len(snap.Envoys) != 2 {
		t.Errorf("snapshot envoy count = %d, want 2", len(snap.Envoys))
	}
	if snap.Convergence.Total != 2 {
		t.Errorf("snapshot convergence Total = %d, want 2", snap.Convergence.Total)
	}
	if snap.BoltPubkey != hex.EncodeToString(pubkey) {
		t.Errorf("snapshot bolt_pubkey = %q, want the full local pubkey hex", snap.BoltPubkey)
	}

	// The payload must verify against this bolt's pubkey.
	if err := spanner.VerifyFleetSnapshot(pubkey, &snap); err != nil {
		t.Errorf("served fleet snapshot failed signature verification: %v", err)
	}

	// Tamper → verification must fail.
	snap.Convergence.Total = 999
	if err := spanner.VerifyFleetSnapshot(pubkey, &snap); err == nil {
		t.Error("verification accepted a tampered served snapshot")
	}
}

func TestFleetSnapshotEndpoint503WithoutFleetIndex(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	h := NewSpannerAdmissionHandler(sqlDB, db.New(sqlDB), testBoltIdentity(t),
		"test-spanner", "bolt.example.com", []string{"*"}, nil, nil, nil)
	// No SetFleetIndex call → 503.

	rec := httptest.NewRecorder()
	h.handleFleetSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/spanner/fleet-snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fleet-snapshot without a fleet index returned %d, want 503", rec.Code)
	}
}

func TestFleetSnapshotEndpoint503WithoutBoltIdentity(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	h := NewSpannerAdmissionHandler(sqlDB, db.New(sqlDB), nil,
		"test-spanner", "bolt.example.com", []string{"*"}, nil, nil, nil)
	h.SetFleetIndex(fleet.NewFleetIndex())

	rec := httptest.NewRecorder()
	h.handleFleetSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/spanner/fleet-snapshot", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fleet-snapshot without a bolt identity returned %d, want 503", rec.Code)
	}
}
