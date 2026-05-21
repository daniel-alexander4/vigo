// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/spanner"
)

func TestGenerateToken(t *testing.T) {
	svc := &AdminService{
		baseInterval: 5 * time.Minute,
	}

	resp, err := svc.GenerateToken(context.Background(), &pb.GenerateTokenRequest{})
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}

	if resp.Token == "" {
		t.Fatal("GenerateToken() returned empty token")
	}

	// Token should be 64 hex chars (32 bytes)
	if len(resp.Token) != 64 {
		t.Errorf("GenerateToken() token length = %d, want 64", len(resp.Token))
	}

	// Token should be valid hex
	if _, err := hex.DecodeString(resp.Token); err != nil {
		t.Errorf("GenerateToken() token is not valid hex: %v", err)
	}

	// Two tokens should differ (randomness check)
	resp2, err := svc.GenerateToken(context.Background(), &pb.GenerateTokenRequest{})
	if err != nil {
		t.Fatalf("GenerateToken() second call error: %v", err)
	}
	if resp.Token == resp2.Token {
		t.Error("GenerateToken() returned same token twice -- not random")
	}
}

func TestAddApprovedPatternValidation(t *testing.T) {
	svc := &AdminService{
		baseInterval: 5 * time.Minute,
	}

	tests := []struct {
		name    string
		req     *pb.AddApprovedPatternRequest
		wantErr bool
	}{
		{
			name:    "empty pattern",
			req:     &pb.AddApprovedPatternRequest{Pattern: "", Token: "abc123"},
			wantErr: true,
		},
		{
			name:    "empty token",
			req:     &pb.AddApprovedPatternRequest{Pattern: "*.web.example.com", Token: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AddApprovedPattern(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddApprovedPattern() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestForcePushValidation(t *testing.T) {
	svc := &AdminService{
		baseInterval: 5 * time.Minute,
	}

	// Invalid UUID should return error
	_, err := svc.ForcePush(context.Background(), &pb.ForcePushRequest{EnvoyId: "not-a-uuid"})
	if err == nil {
		t.Error("ForcePush() with invalid UUID should return error")
	}
}

// --- Spanner peer-equal admin-read dispatch (Slice 2-ii-a) ---

// TestListEnvoysSpannerCacheAggregate exercises the dispatch path
// where a non-hub spanner bolt aggregates ListEnvoys from the
// observability cache (local fleetIndex + every cached peer). The
// response is the same *pb.Envoy shape the pure-local path returns.
func TestListEnvoysSpannerCacheAggregate(t *testing.T) {
	now := time.Now()
	obs := spanner.NewObservabilityCache()
	obs.UpdateFull("peerA", []spanner.EnvoyLite{{ID: "a1"}, {ID: "a2"}}, spanner.ConvergenceCounters{}, now)
	obs.UpdateFull("peerB", []spanner.EnvoyLite{{ID: "b1"}}, spanner.ConvergenceCounters{}, now)

	sa := spanner.NewSpannerAdmin(nil, obs, nil, "ownbolt", spanner.FallbackAdminLocal, "1530")
	sa.SetLocalSources(
		func() spanner.ConvergenceCounters { return spanner.ConvergenceCounters{} },
		func() []spanner.EnvoyLite { return []spanner.EnvoyLite{{ID: "local-1", Hostname: "lh1"}} },
	)

	svc := &AdminService{spannerAdmin: sa}
	resp, err := svc.ListEnvoys(context.Background(), &pb.ListEnvoysRequest{})
	if err != nil {
		t.Fatalf("ListEnvoys: %v", err)
	}
	if len(resp.Envoys) != 4 {
		t.Fatalf("cache-aggregated envoy count = %d, want 4 (1 local + 3 peers)", len(resp.Envoys))
	}
	// Shape parity: every entry is a populated *pb.Envoy.
	for _, e := range resp.Envoys {
		if e.Id == "" {
			t.Error("aggregated envoy has empty Id — wrong shape")
		}
	}
}

// TestGetConvergenceSummarySpannerCacheAggregate exercises the
// cache-aggregate convergence path and asserts snapshot staleness is
// surfaced in the response.
func TestGetConvergenceSummarySpannerCacheAggregate(t *testing.T) {
	obs := spanner.NewObservabilityCache()
	old := time.Now().Add(-90 * time.Second)
	obs.UpdateFull("peerA", nil, spanner.ConvergenceCounters{Total: 5, Converged: 5}, old)
	obs.UpdateFull("peerB", nil, spanner.ConvergenceCounters{Total: 3, Converged: 1, Failed: 2}, time.Now())

	sa := spanner.NewSpannerAdmin(nil, obs, nil, "ownbolt", spanner.FallbackAdminLocal, "1530")
	sa.SetLocalSources(
		func() spanner.ConvergenceCounters {
			return spanner.ConvergenceCounters{Total: 2, Converged: 2}
		},
		func() []spanner.EnvoyLite { return nil },
	)

	svc := &AdminService{spannerAdmin: sa}
	resp, err := svc.GetConvergenceSummary(context.Background(), &pb.GetConvergenceSummaryRequest{})
	if err != nil {
		t.Fatalf("GetConvergenceSummary: %v", err)
	}
	if resp.Total != 10 || resp.Converged != 8 || resp.Failed != 2 {
		t.Errorf("aggregate = Total %d / Converged %d / Failed %d, want 10 / 8 / 2",
			resp.Total, resp.Converged, resp.Failed)
	}
	// ConvergencePct: 8 converged / 10 active (no offline) = 80%.
	if resp.ConvergencePct < 79.9 || resp.ConvergencePct > 80.1 {
		t.Errorf("ConvergencePct = %v, want ~80", resp.ConvergencePct)
	}
	// Staleness: the oldest contributing peer is ~90s old.
	if resp.SnapshotAgeSeconds < 80 {
		t.Errorf("SnapshotAgeSeconds = %d, want >= 80 (oldest peer snapshot age)", resp.SnapshotAgeSeconds)
	}
}

// TestConvergenceCountersToProto guards the converter every
// GetConvergenceSummary path now routes through. In particular it
// pins Diverged and Offline as carried through — the pre-2-ii-a
// hand-rolled standalone handler reported both as always-0. Relapsed
// stays zero: it is a stale proto field with no canonical counterpart,
// retired by the filed convergence-model reconciliation.
func TestConvergenceCountersToProto(t *testing.T) {
	resp := convergenceCountersToProto(spanner.ConvergenceCounters{
		Total: 20, Converged: 12, Degraded: 3, Failed: 3, NoData: 2,
		Changed: 5, Diverged: 4, Offline: 2,
	})
	if resp.Total != 20 || resp.Converged != 12 || resp.Failed != 3 || resp.NoData != 2 {
		t.Errorf("failure axis: Total=%d Converged=%d Failed=%d NoData=%d, want 20/12/3/2",
			resp.Total, resp.Converged, resp.Failed, resp.NoData)
	}
	if resp.Diverged != 4 {
		t.Errorf("Diverged = %d, want 4 (must be carried, not zeroed)", resp.Diverged)
	}
	if resp.Offline != 2 {
		t.Errorf("Offline = %d, want 2 (must be carried, not zeroed)", resp.Offline)
	}
	if resp.Relapsed != 0 {
		t.Errorf("Relapsed = %d, want 0 (stale field, no canonical source)", resp.Relapsed)
	}
}

// TestGetConvergenceSummaryStandalone checks the standalone path —
// no hub, no spannerAdmin — is wired to the canonical fleet-index
// source via the shared converter and is nil-safe when no fleet index
// is present.
func TestGetConvergenceSummaryStandalone(t *testing.T) {
	svc := &AdminService{}
	resp, err := svc.GetConvergenceSummary(context.Background(), &pb.GetConvergenceSummaryRequest{})
	if err != nil {
		t.Fatalf("GetConvergenceSummary: %v", err)
	}
	if resp == nil {
		t.Fatal("standalone GetConvergenceSummary returned a nil response")
	}
}
