// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryUnaryInterceptor recovers a panic in a unary handler — or in any
// inner interceptor — and converts it to a codes.Internal error instead of
// letting it crash the whole vigosrv process. grpc-go does NOT recover handler
// panics, so without this a single nil-deref in a handler takes down gRPC
// streams, REST, metrics, and every other goroutine with it.
//
// Chain it OUTERMOST (first in the unary chain) so it also backstops the
// signature-verify and rate-limit interceptors. The recover + log route through
// the daemon SoT (server/daemon); the gRPC-specific teardown — count the panic,
// return Internal — is this interceptor's onPanic. The panic value is logged
// server-side with its stack but never returned to the caller.
func RecoveryUnaryInterceptor(m *metrics.Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer daemon.Recover(info.FullMethod, func(any) {
			if m != nil {
				m.GRPCHandlerPanicsTotal.WithLabelValues(info.FullMethod).Inc()
			}
			err = status.Error(codes.Internal, "internal server error")
		})
		return handler(ctx, req)
	}
}

// RecoveryStreamInterceptor is the streaming sibling. It backstops panics in
// stream handlers (AgentStream, TunnelStream, …) — the most exposed surface,
// since the server otherwise registers no stream interceptor at all. A panic in
// a stream handler's own goroutine becomes a codes.Internal stream close.
//
// NOTE: like every recover, it only catches panics in the handler's OWN
// goroutine — goroutines a stream handler spawns must route their own recover
// through daemon.Recover/SafeGo/Guard.
func RecoveryStreamInterceptor(m *metrics.Metrics) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer daemon.Recover(info.FullMethod, func(any) {
			if m != nil {
				m.GRPCHandlerPanicsTotal.WithLabelValues(info.FullMethod).Inc()
			}
			err = status.Error(codes.Internal, "internal server error")
		})
		return handler(srv, ss)
	}
}
