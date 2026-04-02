// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/config"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// perEnvoyEntry holds a per-envoy rate limiter and the last time it was used.
type perEnvoyEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitInterceptor returns a unary server interceptor that enforces
// per-envoy and global rate limits on check-in RPCs. Non-check-in RPCs
// pass through without rate limiting.
func RateLimitInterceptor(cfg config.RateLimitSection) grpc.UnaryServerInterceptor {
	perEnvoyRate := cfg.CheckinPerEnvoy
	if perEnvoyRate <= 0 {
		perEnvoyRate = 6 // 6 per minute default
	}
	globalRate := cfg.CheckinGlobal
	if globalRate <= 0 {
		globalRate = 500 // 500 per second default
	}

	// Per-envoy: tokens per minute → rate.Limit is tokens per second.
	perEnvoyLimit := rate.Limit(float64(perEnvoyRate) / 60.0)
	globalLimiter := rate.NewLimiter(rate.Limit(globalRate), globalRate)

	var mu sync.Mutex
	envoyLimiters := make(map[string]*perEnvoyEntry)

	// Evict stale per-envoy entries every 10 minutes.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			cutoff := time.Now().Add(-30 * time.Minute)
			for id, entry := range envoyLimiters {
				if entry.lastSeen.Before(cutoff) {
					delete(envoyLimiters, id)
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
		if info.FullMethod != pb.VigoAgent_CheckIn_FullMethodName {
			return handler(ctx, req)
		}

		// Global rate limit.
		if !globalLimiter.Allow() {
			slog.Warn("rate limit: global check-in limit exceeded")
			return nil, status.Error(codes.ResourceExhausted, "global check-in rate limit exceeded")
		}

		// Per-envoy rate limit.
		if r, ok := req.(*pb.CheckInRequest); ok {
			envoyID := r.GetEnvoyId()
			if envoyID != "" {
				mu.Lock()
				entry, exists := envoyLimiters[envoyID]
				if !exists {
					entry = &perEnvoyEntry{
						limiter: rate.NewLimiter(perEnvoyLimit, perEnvoyRate),
					}
					envoyLimiters[envoyID] = entry
				}
				entry.lastSeen = time.Now()
				limiter := entry.limiter
				mu.Unlock()

				if !limiter.Allow() {
					slog.Warn("rate limit: per-envoy check-in limit exceeded",
						"envoy_id", envoyID)
					return nil, status.Error(codes.ResourceExhausted,
						"per-envoy check-in rate limit exceeded")
				}
			}
		}

		return handler(ctx, req)
	}
}
