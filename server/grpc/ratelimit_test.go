// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRateLimitInterceptor_AllowsNonCheckIn(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerThrall: 1, CheckinGlobal: 1}
	interceptor := RateLimitInterceptor(cfg)

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/hobnob.HobnobAgent/Register"}

	for i := 0; i < 10; i++ {
		resp, err := interceptor(context.Background(), nil, info, handler)
		if err != nil {
			t.Fatalf("non-check-in RPC should not be rate limited, got: %v", err)
		}
		if resp != "ok" {
			t.Fatalf("unexpected response: %v", resp)
		}
	}
}

func TestRateLimitInterceptor_PerThrallLimit(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerThrall: 1, CheckinGlobal: 10000}
	interceptor := RateLimitInterceptor(cfg)

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: pb.HobnobAgent_CheckIn_FullMethodName}

	req := &pb.CheckInRequest{ThrallId: "test-thrall-001"}

	// First request should succeed (burst = 1).
	_, err := interceptor(context.Background(), req, info, handler)
	if err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}

	// Second request from same thrall should be rate-limited.
	_, err = interceptor(context.Background(), req, info, handler)
	if err == nil {
		t.Fatal("expected rate limit error for second request from same thrall")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got: %v", err)
	}

	// Different thrall should succeed.
	req2 := &pb.CheckInRequest{ThrallId: "test-thrall-002"}
	_, err = interceptor(context.Background(), req2, info, handler)
	if err != nil {
		t.Fatalf("different thrall should not be rate limited: %v", err)
	}
}

func TestRateLimitInterceptor_GlobalLimit(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerThrall: 10000, CheckinGlobal: 1}
	interceptor := RateLimitInterceptor(cfg)

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: pb.HobnobAgent_CheckIn_FullMethodName}

	// First request should succeed (burst = 1).
	req1 := &pb.CheckInRequest{ThrallId: "thrall-a"}
	_, err := interceptor(context.Background(), req1, info, handler)
	if err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}

	// Second request from different thrall should be globally rate-limited.
	req2 := &pb.CheckInRequest{ThrallId: "thrall-b"}
	_, err = interceptor(context.Background(), req2, info, handler)
	if err == nil {
		t.Fatal("expected global rate limit error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got: %v", err)
	}
}
