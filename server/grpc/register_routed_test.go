// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/testutil"
)

// TestDropRoutedLocalEnvoy: when Register routes an enrollment to the owning
// bolt, the local registration must be dropped from BOTH the FleetIndex and the
// DB. Removing only the index entry left the row behind, and rehydrate brought
// it back on restart as a phantom offline envoy.
func TestDropRoutedLocalEnvoy(t *testing.T) {
	ctx := context.Background()
	sqlDB := testutil.OpenMigratedTestDB(t)
	queries := db.New(sqlDB)
	e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "routed.example.com", IpAddress: "10.0.0.5", PubkeyPem: "pk"})
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{ID: e.ID, Hostname: e.Hostname}, nil)

	svc := &AgentService{queries: queries, fleetIndex: fi}
	svc.dropRoutedLocalEnvoy(ctx, e.ID)

	if _, ok, _ := fi.GetEnvoyLite(e.ID); ok {
		t.Error("FleetIndex still holds the routed envoy")
	}
	if _, err := queries.GetEnvoyByID(ctx, e.ID); err == nil {
		t.Error("local DB row survived a routed enrollment — rehydrate would resurrect it as a phantom envoy")
	}
}
