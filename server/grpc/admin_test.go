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
// GetConvergenceSummary path now routes through. It pins the full
// canonical model — the failure axis (converged/degraded/failed/
// no_data, summing to total), the drift axis (changed/diverged), and
// reachability (offline) — all carried through, none zeroed.
func TestConvergenceCountersToProto(t *testing.T) {
	resp := convergenceCountersToProto(spanner.ConvergenceCounters{
		Total: 20, Converged: 12, Degraded: 3, Failed: 3, NoData: 2,
		Changed: 5, Diverged: 4, Offline: 2,
	})
	if resp.Total != 20 || resp.Converged != 12 || resp.Degraded != 3 || resp.Failed != 3 || resp.NoData != 2 {
		t.Errorf("failure axis: Total=%d Converged=%d Degraded=%d Failed=%d NoData=%d, want 20/12/3/3/2",
			resp.Total, resp.Converged, resp.Degraded, resp.Failed, resp.NoData)
	}
	if got := resp.Converged + resp.Degraded + resp.Failed + resp.NoData; got != resp.Total {
		t.Errorf("failure axis sums to %d, want Total=%d", got, resp.Total)
	}
	if resp.Changed != 5 || resp.Diverged != 4 {
		t.Errorf("drift axis: Changed=%d Diverged=%d, want 5/4 (carried, not zeroed)", resp.Changed, resp.Diverged)
	}
	if resp.Offline != 2 {
		t.Errorf("Offline = %d, want 2 (carried, not zeroed)", resp.Offline)
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

func TestResolveListEnvoysPaging_Defaults(t *testing.T) {
	limit, offset := resolveListEnvoysPaging(&pb.ListEnvoysRequest{})
	if limit != listEnvoysDefaultLimit {
		t.Errorf("zero limit should resolve to server default (%d); got %d", listEnvoysDefaultLimit, limit)
	}
	if offset != 0 {
		t.Errorf("zero offset should resolve to 0; got %d", offset)
	}
}

func TestResolveListEnvoysPaging_ClampsAboveMax(t *testing.T) {
	limit, _ := resolveListEnvoysPaging(&pb.ListEnvoysRequest{Limit: 100000})
	if limit != listEnvoysMaxLimit {
		t.Errorf("limit above max should clamp to %d; got %d", listEnvoysMaxLimit, limit)
	}
}

func TestResolveListEnvoysPaging_NormalizesNegativeOffset(t *testing.T) {
	_, offset := resolveListEnvoysPaging(&pb.ListEnvoysRequest{Offset: -5})
	if offset != 0 {
		t.Errorf("negative offset should normalize to 0; got %d", offset)
	}
}

func TestSliceEnvoysPage(t *testing.T) {
	envoys := make([]*pb.Envoy, 1000)
	for i := range envoys {
		envoys[i] = &pb.Envoy{Hostname: "h"}
	}

	// First page.
	page, total := sliceEnvoysPage(envoys, 500, 0)
	if total != 1000 {
		t.Errorf("total should reflect full input (1000); got %d", total)
	}
	if len(page) != 500 {
		t.Errorf("page should be 500; got %d", len(page))
	}

	// Last page (partial).
	page, total = sliceEnvoysPage(envoys, 500, 750)
	if total != 1000 {
		t.Errorf("total stays at 1000; got %d", total)
	}
	if len(page) != 250 {
		t.Errorf("partial last page should be 250; got %d", len(page))
	}

	// Past the end — empty page but accurate total.
	page, total = sliceEnvoysPage(envoys, 500, 2000)
	if total != 1000 {
		t.Errorf("total stays at 1000 past the end; got %d", total)
	}
	if len(page) != 0 {
		t.Errorf("page past the end should be empty; got %d", len(page))
	}
}

func TestBuildListEnvoysResponse_HasMoreAndNextOffset(t *testing.T) {
	page := []*pb.Envoy{{Hostname: "a"}, {Hostname: "b"}}
	resp := buildListEnvoysResponse(page, 100, 50, 0)
	if !resp.HasMore {
		t.Errorf("HasMore should be true when offset+len < total")
	}
	if resp.NextOffset != 2 {
		t.Errorf("NextOffset should be offset+len(page) = 2; got %d", resp.NextOffset)
	}

	// Final page — HasMore false, NextOffset 0.
	resp = buildListEnvoysResponse(page, 2, 50, 0)
	if resp.HasMore {
		t.Errorf("HasMore should be false on final page")
	}
	if resp.NextOffset != 0 {
		t.Errorf("NextOffset should be 0 on final page; got %d", resp.NextOffset)
	}
}
