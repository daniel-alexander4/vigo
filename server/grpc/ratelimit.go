// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// perThrallEntry holds a per-thrall rate limiter and the last time it was used.
type perThrallEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitInterceptor returns a unary server interceptor that enforces
// per-thrall and global rate limits on check-in RPCs. Non-check-in RPCs
// pass through without rate limiting.
func RateLimitInterceptor(cfg config.RateLimitSection) grpc.UnaryServerInterceptor {
	perThrallRate := cfg.CheckinPerThrall
	if perThrallRate <= 0 {
		perThrallRate = 6 // 6 per minute default
	}
	globalRate := cfg.CheckinGlobal
	if globalRate <= 0 {
		globalRate = 500 // 500 per second default
	}

	// Per-thrall: tokens per minute → rate.Limit is tokens per second.
	perThrallLimit := rate.Limit(float64(perThrallRate) / 60.0)
	globalLimiter := rate.NewLimiter(rate.Limit(globalRate), globalRate)

	var mu sync.Mutex
	thrallLimiters := make(map[string]*perThrallEntry)

	// Evict stale per-thrall entries every 10 minutes.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			cutoff := time.Now().Add(-30 * time.Minute)
			for id, entry := range thrallLimiters {
				if entry.lastSeen.Before(cutoff) {
					delete(thrallLimiters, id)
				}
			}
			mu.Unlock()
		}
	}()

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Only rate-limit CheckIn RPCs.
		if info.FullMethod != pb.HobnobAgent_CheckIn_FullMethodName {
			return handler(ctx, req)
		}

		// Global rate limit.
		if !globalLimiter.Allow() {
			slog.Warn("rate limit: global check-in limit exceeded")
			return nil, status.Error(codes.ResourceExhausted, "global check-in rate limit exceeded")
		}

		// Per-thrall rate limit.
		if r, ok := req.(*pb.CheckInRequest); ok {
			thrallID := r.GetThrallId()
			if thrallID != "" {
				mu.Lock()
				entry, exists := thrallLimiters[thrallID]
				if !exists {
					entry = &perThrallEntry{
						limiter: rate.NewLimiter(perThrallLimit, perThrallRate),
					}
					thrallLimiters[thrallID] = entry
				}
				entry.lastSeen = time.Now()
				limiter := entry.limiter
				mu.Unlock()

				if !limiter.Allow() {
					slog.Warn("rate limit: per-thrall check-in limit exceeded",
						"thrall_id", thrallID)
					return nil, status.Error(codes.ResourceExhausted,
						"per-thrall check-in rate limit exceeded")
				}
			}
		}

		return handler(ctx, req)
	}
}
