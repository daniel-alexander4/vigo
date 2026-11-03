// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
)

// TestRecoveryUnaryInterceptor_RecoversPanic proves a panicking unary handler is
// turned into a codes.Internal error instead of crashing the process — if the
// panic escaped, the test binary would be gone before the assertions run — and
// that the panic counter ticks.
func TestRecoveryUnaryInterceptor_RecoversPanic(t *testing.T) {
	m := metrics.NewTestMetrics()
	interceptor := isgrpc.RecoveryUnaryInterceptor(m)
	info := &grpc.UnaryServerInfo{FullMethod: "/vigo.VigoAgent/CheckIn"}

	resp, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		panic("boom")
	})

	if resp != nil {
		t.Fatalf("expected nil resp on panic, got %v", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected codes.Internal, got %v (err=%v)", status.Code(err), err)
	}
	// The panic value must not leak to the caller.
	if err != nil && err.Error() == "boom" {
		t.Fatal("panic value leaked into the returned error")
	}
	if got := testutil.ToFloat64(m.GRPCHandlerPanicsTotal.WithLabelValues(info.FullMethod)); got != 1 {
		t.Fatalf("expected panic counter = 1, got %v", got)
	}
}

// TestRecoveryUnaryInterceptor_PassesThrough confirms a normal handler's
// response and error flow through untouched and the counter stays at zero.
func TestRecoveryUnaryInterceptor_PassesThrough(t *testing.T) {
	m := metrics.NewTestMetrics()
	interceptor := isgrpc.RecoveryUnaryInterceptor(m)
	info := &grpc.UnaryServerInfo{FullMethod: "/vigo.VigoAgent/CheckIn"}

	resp, err := interceptor(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected resp passthrough %q, got %v", "ok", resp)
	}
	if got := testutil.ToFloat64(m.GRPCHandlerPanicsTotal.WithLabelValues(info.FullMethod)); got != 0 {
		t.Fatalf("expected panic counter = 0 on a clean call, got %v", got)
	}
}

// TestRecoveryStreamInterceptor_RecoversPanic proves a panicking stream handler
// (the AgentStream/TunnelStream class) is contained as a codes.Internal stream
// close instead of crashing the process.
func TestRecoveryStreamInterceptor_RecoversPanic(t *testing.T) {
	m := metrics.NewTestMetrics()
	interceptor := isgrpc.RecoveryStreamInterceptor(m)
	info := &grpc.StreamServerInfo{FullMethod: "/vigo.VigoAgent/AgentStream"}

	err := interceptor(nil, nil, info, func(any, grpc.ServerStream) error {
		panic("boom")
	})

	if status.Code(err) != codes.Internal {
		t.Fatalf("expected codes.Internal, got %v (err=%v)", status.Code(err), err)
	}
	if got := testutil.ToFloat64(m.GRPCHandlerPanicsTotal.WithLabelValues(info.FullMethod)); got != 1 {
		t.Fatalf("expected panic counter = 1, got %v", got)
	}
}
