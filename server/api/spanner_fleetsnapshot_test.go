// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"crypto/ed25519"
	"database/sql"
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

// authedPull seeds a fresh, unrevoked roster member into q and returns a GET
// to path carrying that member's valid Ed25519 pull-proof header — enough to
// clear authenticatePull so a test can exercise the serve/503 logic behind
// it. authenticatePull matches on pubkey + revoked state and does not
// re-verify the admission signature, so a placeholder admission sig is fine.
func authedPull(t *testing.T, q *db.Queries, path string) *http.Request {
	t.Helper()
	signer := testBoltIdentity(t)
	pubHex := hex.EncodeToString(signer.PublicKey)
	if err := q.InsertBolt(t.Context(), db.InsertBoltParams{
		Pubkey:     pubHex,
		Hostname:   "member.example.com",
		AdmittedAt: time.Now().Unix(),
		AdmittedBy: pubHex,
		Signature:  []byte("placeholder"),
	}); err != nil {
		t.Fatalf("seed roster member: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	spanner.SignPullRequest(req, signer.SigningKey, pubHex)
	return req
}

func TestFleetSnapshotEndpointSignsAndServes(t *testing.T) {
	h, pubkey := newFleetSnapshotHandler(t, []fleet.EnvoyState{
		{ID: "e1", Hostname: "h1.example.com", IP: "10.0.0.1", ConvergenceStatus: "converged",
			EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC()},
		{ID: "e2", Hostname: "h2.example.com", IP: "10.0.0.2", ConvergenceStatus: "failed",
			EnrolledAt: time.Now().UTC(), LastSeen: time.Now().UTC()},
	})

	rec := httptest.NewRecorder()
	req := authedPull(t, h.queries, "/api/v1/spanner/fleet-snapshot")
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

// TestSnapshotAuthGate proves the finding fix: the two session-less gossip
// pull endpoints refuse an unsigned caller, a caller signed by a non-roster
// key, and a caller signed by a revoked member — closing the anonymous
// full-user-directory read.
func TestSnapshotAuthGate(t *testing.T) {
	h, _ := newFleetSnapshotHandler(t, nil)

	// (a) unsigned → 403 on both endpoints.
	for name, hf := range map[string]http.HandlerFunc{
		"snapshot":       h.handleSnapshot,
		"fleet-snapshot": h.handleFleetSnapshot,
	} {
		rec := httptest.NewRecorder()
		hf(rec, httptest.NewRequest(http.MethodGet, "/api/v1/spanner/"+name, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("unsigned %s pull returned %d, want 403", name, rec.Code)
		}
	}

	// (b) signed by a key not in the roster → 403.
	stranger := testBoltIdentity(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
	spanner.SignPullRequest(req, stranger.SigningKey, hex.EncodeToString(stranger.PublicKey))
	rec := httptest.NewRecorder()
	h.handleSnapshot(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-member pull returned %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// (c) signed by a member whose row is revoked → 403.
	revoked := testBoltIdentity(t)
	pubHex := hex.EncodeToString(revoked.PublicKey)
	if err := h.queries.InsertBolt(t.Context(), db.InsertBoltParams{
		Pubkey:     pubHex,
		Hostname:   "revoked.example.com",
		AdmittedAt: time.Now().Unix(),
		AdmittedBy: pubHex,
		RevokedAt:  sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
		Signature:  []byte("placeholder"),
	}); err != nil {
		t.Fatalf("seed revoked member: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/spanner/snapshot", nil)
	spanner.SignPullRequest(req2, revoked.SigningKey, pubHex)
	rec2 := httptest.NewRecorder()
	h.handleSnapshot(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("revoked-member pull returned %d, want 403; body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestFleetSnapshotEndpoint503WithoutFleetIndex(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	h := NewSpannerAdmissionHandler(sqlDB, db.New(sqlDB), testBoltIdentity(t),
		"test-spanner", "bolt.example.com", []string{"*"}, nil, nil, nil)
	// No SetFleetIndex call → 503.

	rec := httptest.NewRecorder()
	h.handleFleetSnapshot(rec, authedPull(t, h.queries, "/api/v1/spanner/fleet-snapshot"))
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
	h.handleFleetSnapshot(rec, authedPull(t, h.queries, "/api/v1/spanner/fleet-snapshot"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fleet-snapshot without a bolt identity returned %d, want 503", rec.Code)
	}
}
