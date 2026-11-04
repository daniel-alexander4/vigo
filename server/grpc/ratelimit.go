// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/daemon"
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
// pass through without rate limiting. The stale-entry eviction goroutine
// exits when stop is closed (nil: runs for the process lifetime).
func RateLimitInterceptor(cfg config.RateLimitSection, stop <-chan struct{}) grpc.UnaryServerInterceptor {
	perEnvoyRate := cfg.CheckinPerEnvoy
	if perEnvoyRate <= 0 {
		// 120/min ≈ the 1s default cadence (60/min) plus headroom for
		// jitter and force-push retries. Rate limiting is off by default
		// (rate_limit.enabled=false); this only applies on shared/public
		// servers that opt in, so the cap must not throttle the default
		// cadence the moment an operator enables it.
		perEnvoyRate = 120
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

	// Evict stale per-envoy entries every 10 minutes until stop closes. Each
	// sweep is guarded so a panic logs and the loop keeps evicting.
	daemon.SafeGo("ratelimit-evict", func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				daemon.Guard("ratelimit-evict", func() {
					mu.Lock()
					defer mu.Unlock()
					cutoff := time.Now().Add(-30 * time.Minute)
					for id, entry := range envoyLimiters {
						if entry.lastSeen.Before(cutoff) {
							delete(envoyLimiters, id)
						}
					}
				})
			}
		}
	})

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
