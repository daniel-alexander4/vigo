// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"runtime"
	"testing"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRateLimitInterceptor_AllowsNonCheckIn(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerEnvoy: 1, CheckinGlobal: 1}
	interceptor := RateLimitInterceptor(cfg, stopOnCleanup(t))

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/vigo.VigoAgent/Register"}

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

func TestRateLimitInterceptor_PerEnvoyLimit(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerEnvoy: 1, CheckinGlobal: 10000}
	interceptor := RateLimitInterceptor(cfg, stopOnCleanup(t))

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_CheckIn_FullMethodName}

	req := &pb.CheckInRequest{EnvoyId: "test-envoy-001"}

	// First request should succeed (burst = 1).
	_, err := interceptor(context.Background(), req, info, handler)
	if err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}

	// Second request from same envoy should be rate-limited.
	_, err = interceptor(context.Background(), req, info, handler)
	if err == nil {
		t.Fatal("expected rate limit error for second request from same envoy")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got: %v", err)
	}

	// Different envoy should succeed.
	req2 := &pb.CheckInRequest{EnvoyId: "test-envoy-002"}
	_, err = interceptor(context.Background(), req2, info, handler)
	if err != nil {
		t.Fatalf("different envoy should not be rate limited: %v", err)
	}
}

func TestRateLimitInterceptor_GlobalLimit(t *testing.T) {
	cfg := config.RateLimitSection{Enabled: true, CheckinPerEnvoy: 10000, CheckinGlobal: 1}
	interceptor := RateLimitInterceptor(cfg, stopOnCleanup(t))

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: pb.VigoAgent_CheckIn_FullMethodName}

	// First request should succeed (burst = 1).
	req1 := &pb.CheckInRequest{EnvoyId: "envoy-a"}
	_, err := interceptor(context.Background(), req1, info, handler)
	if err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}

	// Second request from different envoy should be globally rate-limited.
	req2 := &pb.CheckInRequest{EnvoyId: "envoy-b"}
	_, err = interceptor(context.Background(), req2, info, handler)
	if err == nil {
		t.Fatal("expected global rate limit error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got: %v", err)
	}
}

// stopOnCleanup returns a stop channel closed when the test ends, so each
// interceptor's eviction goroutine exits with its test.
func stopOnCleanup(t *testing.T) <-chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	return stop
}

// TestRateLimitInterceptor_EvictionStopsOnStop: the eviction goroutine used to
// run forever (one leaked goroutine per constructed interceptor). Closing stop
// must end it.
func TestRateLimitInterceptor_EvictionStopsOnStop(t *testing.T) {
	base := runtime.NumGoroutine()
	stop := make(chan struct{})
	for range 20 {
		_ = RateLimitInterceptor(config.RateLimitSection{Enabled: true}, stop)
	}
	if n := runtime.NumGoroutine(); n < base+20 {
		t.Fatalf("expected 20 eviction goroutines running (base %d, now %d)", base, n)
	}
	close(stop)
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Errorf("eviction goroutines still running after stop: base %d, now %d", base, n)
	}
}
