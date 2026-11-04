// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"context"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

// TestRehydrateSeedsRevokedIDs: rehydrate holds only non-revoked envoys, so it
// must seed the revoked-id set from the DB — otherwise a revoked envoy would
// read as unknown after a restart.
func TestRehydrateSeedsRevokedIDs(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	ctx := context.Background()
	active, err := q.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "active.example.com", IpAddress: "10.0.0.1", PubkeyPem: "x"})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := q.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "revoked.example.com", IpAddress: "10.0.0.2", PubkeyPem: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.RevokeEnvoy(ctx, revoked.ID); err != nil {
		t.Fatal(err)
	}

	fi := NewFleetIndex()
	if err := Rehydrate(ctx, fi, q, nil, time.Minute, 0, nil); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if !fi.IsRevokedID(revoked.ID) {
		t.Error("revoked envoy not in the revoked-id set after rehydrate")
	}
	if fi.IsRevokedID(active.ID) {
		t.Error("active envoy in the revoked-id set after rehydrate")
	}
	if fi.GetEnvoy(active.ID) == nil {
		t.Error("active envoy not held after rehydrate")
	}
	if got := fi.UnpinnedEnvoys(); len(got) != 1 || got[0].EnvoyID != active.ID {
		t.Errorf("UnpinnedEnvoys = %+v, want only the active, unpinned envoy", got)
	}
}

// TestRehydrateCarriesCertPin: check-in verification reads the cert pin from
// the fleet index, so rehydrate must load it. Without it every envoy reads as
// unpinned after a restart and is refused at check-in.
func TestRehydrateCarriesCertPin(t *testing.T) {
	sqlDB := testutil.OpenMigratedTestDB(t)
	q := db.New(sqlDB)
	ctx := context.Background()
	e, err := q.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "pinned.example.com", IpAddress: "10.0.0.3", PubkeyPem: "x"})
	if err != nil {
		t.Fatal(err)
	}
	const pin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := q.UpdateEnvoyCertPin(ctx, db.UpdateEnvoyCertPinParams{CertSpkiSha256: pin, ID: e.ID}); err != nil {
		t.Fatal(err)
	}

	fi := NewFleetIndex()
	if err := Rehydrate(ctx, fi, q, nil, time.Minute, 0, nil); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	got := fi.GetEnvoy(e.ID)
	if got == nil {
		t.Fatal("pinned envoy not held after rehydrate")
	}
	if got.CertSPKI != pin {
		t.Errorf("CertSPKI after rehydrate = %q, want %q", got.CertSPKI, pin)
	}
	if u := fi.UnpinnedEnvoys(); len(u) != 0 {
		t.Errorf("UnpinnedEnvoys = %+v, want none", u)
	}
}
