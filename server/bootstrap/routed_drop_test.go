// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/testutil"
)

// TestDropRoutedLocalEnvoy: the REST bootstrap counterpart of the gRPC
// Register fix — a routed enrollment drops the local DB row as well as the
// FleetIndex entry, so rehydrate can't resurrect a phantom envoy.
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

	h := NewHandler("https://server.example.com:8443", "", t.TempDir(), "1.0.0", metrics.NewTestMetrics())
	h.SetFleetIndex(fi)
	h.SetQueries(queries)
	h.dropRoutedLocalEnvoy(ctx, e.ID)

	if _, ok, _ := fi.GetEnvoyLite(e.ID); ok {
		t.Error("FleetIndex still holds the routed envoy")
	}
	if _, err := queries.GetEnvoyByID(ctx, e.ID); err == nil {
		t.Error("local DB row survived a routed enrollment — rehydrate would resurrect it as a phantom envoy")
	}
}
