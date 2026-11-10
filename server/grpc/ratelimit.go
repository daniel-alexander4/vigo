// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/metrics"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Defaults for the rate_limit section. Rate limiting is off by default
// (rate_limit.enabled=false); these only apply on servers that opt in.
const (
	// 120/min ≈ the 1s default cadence (60/min) plus headroom for jitter and
	// force-push retries, so the cap does not throttle the default cadence the
	// moment an operator enables it.
	defaultCheckinPerEnvoy = 120
	defaultCheckinGlobal   = 500 // per second
	// Failed check-in verifications tolerated per source IP per minute. Only
	// failures count: a fleet behind one NAT address shares the bucket, and
	// healthy envoys never spend it. Agents back off on NotFound /
	// PermissionDenied, so a stray orphaned agent stays far below this.
	defaultCheckinFailuresPerIP = 60
	// maxIPFailureEntries caps the per-IP failure map. At the cap a random
	// entry is evicted to make room, so a spray from many addresses cannot grow
	// the map without bound (an attacker with that many addresses is not
	// stopped by a per-IP limit anyway).
	maxIPFailureEntries = 10000
)

// perEnvoyEntry holds a token-bucket limiter and the last time it was used.
// Shared by the per-envoy and per-IP-failure maps.
type perEnvoyEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// evictionLoops counts running eviction loops, so a test can prove they exit
// on stop without counting every goroutine in the process.
var evictionLoops atomic.Int64

// CheckInLimiter holds the three check-in rate limits:
//
//   - global (requests/s) — charged before signature verification, by design:
//     it protects server capacity and is keyed on nothing a caller controls.
//   - per-envoy (requests/min) — charged only AFTER the request's signature
//     verifies. Charging it on the unverified envoy_id let anyone drain a
//     victim's bucket with unsigned requests carrying the victim's id.
//   - per-source-IP failure budget (failures/min) — charged only by FAILED
//     verifications (unknown / revoked / bad signature / cert pin). Once an
//     address exhausts it, its check-ins are refused before any verify work.
//
// A nil *CheckInLimiter means rate limiting is disabled; every method is a
// no-op on nil.
type CheckInLimiter struct {
	global        *rate.Limiter
	perEnvoyLimit rate.Limit
	perEnvoyBurst int
	ipLimit       rate.Limit
	ipBurst       int
	ipMax         int
	m             *metrics.Metrics

	mu     sync.Mutex
	envoys map[string]*perEnvoyEntry
	ips    map[string]*perEnvoyEntry
}

// NewCheckInLimiter builds the limiter from the rate_limit section and starts
// its stale-entry eviction loop, which exits when stop closes (nil: runs for
// the process lifetime). m owns the throttle counter; nil is allowed in tests.
func NewCheckInLimiter(cfg config.RateLimitSection, m *metrics.Metrics, stop <-chan struct{}) *CheckInLimiter {
	perEnvoyRate := cfg.CheckinPerEnvoy
	if perEnvoyRate <= 0 {
		perEnvoyRate = defaultCheckinPerEnvoy
	}
	globalRate := cfg.CheckinGlobal
	if globalRate <= 0 {
		globalRate = defaultCheckinGlobal
	}
	ipRate := cfg.CheckinFailuresPerIP
	if ipRate <= 0 {
		ipRate = defaultCheckinFailuresPerIP
	}

	l := &CheckInLimiter{
		global:        rate.NewLimiter(rate.Limit(globalRate), globalRate),
		perEnvoyLimit: rate.Limit(float64(perEnvoyRate) / 60.0), // per minute → per second
		perEnvoyBurst: perEnvoyRate,
		ipLimit:       rate.Limit(float64(ipRate) / 60.0),
		ipBurst:       ipRate,
		ipMax:         maxIPFailureEntries,
		m:             m,
		envoys:        make(map[string]*perEnvoyEntry),
		ips:           make(map[string]*perEnvoyEntry),
	}

	// Evict stale entries every 10 minutes until stop closes. Each sweep is
	// guarded so a panic logs and the loop keeps evicting.
	daemon.SafeGo("ratelimit-evict", func() {
		evictionLoops.Add(1)
		defer evictionLoops.Add(-1)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				daemon.Guard("ratelimit-evict", func() {
					l.evictStale(time.Now().Add(-30 * time.Minute))
				})
			}
		}
	})
	return l
}

// evictStale drops every per-envoy and per-IP entry not used since cutoff.
func (l *CheckInLimiter) evictStale(cutoff time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range []map[string]*perEnvoyEntry{l.envoys, l.ips} {
		for k, entry := range m {
			if entry.lastSeen.Before(cutoff) {
				delete(m, k)
			}
		}
	}
}

// UnaryInterceptor enforces the global check-in limit. It runs before
// signature verification; the per-envoy and per-IP limits are charged by the
// signature-verify interceptor, which knows whether the request verified.
// Non-check-in RPCs pass through.
func (l *CheckInLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if info.FullMethod != pb.VigoAgent_CheckIn_FullMethodName {
			return handler(ctx, req)
		}
		if !l.global.Allow() {
			slog.Warn("rate limit: global check-in limit exceeded")
			return nil, status.Error(codes.ResourceExhausted, "global check-in rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// allowEnvoy charges one token from a VERIFIED envoy's bucket. Call only after
// the request's signature has verified.
func (l *CheckInLimiter) allowEnvoy(envoyID string) error {
	if l == nil || envoyID == "" {
		return nil
	}
	l.mu.Lock()
	entry, exists := l.envoys[envoyID]
	if !exists {
		entry = &perEnvoyEntry{limiter: rate.NewLimiter(l.perEnvoyLimit, l.perEnvoyBurst)}
		l.envoys[envoyID] = entry
	}
	entry.lastSeen = time.Now()
	limiter := entry.limiter
	l.mu.Unlock()

	if !limiter.Allow() {
		slog.Warn("rate limit: per-envoy check-in limit exceeded", "envoy_id", envoyID)
		return status.Error(codes.ResourceExhausted, "per-envoy check-in rate limit exceeded")
	}
	return nil
}

// ipThrottled reports whether ip has exhausted its failed-verification budget.
// It reads the bucket without charging it — only failures charge.
func (l *CheckInLimiter) ipThrottled(ip string) bool {
	if l == nil || ip == "" {
		return false
	}
	l.mu.Lock()
	entry := l.ips[ip]
	var limiter *rate.Limiter
	if entry != nil {
		limiter = entry.limiter
	}
	l.mu.Unlock()
	if limiter == nil {
		return false
	}
	return limiter.Tokens() < 1
}

// recordFailure charges one failed verification to ip's bucket. At the entry
// cap a random existing entry (Go map iteration order) is evicted first.
func (l *CheckInLimiter) recordFailure(ip string) {
	if l == nil || ip == "" {
		return
	}
	l.mu.Lock()
	entry, exists := l.ips[ip]
	if !exists {
		if len(l.ips) >= l.ipMax {
			for k := range l.ips {
				delete(l.ips, k)
				break
			}
		}
		entry = &perEnvoyEntry{limiter: rate.NewLimiter(l.ipLimit, l.ipBurst)}
		l.ips[ip] = entry
	}
	entry.lastSeen = time.Now()
	limiter := entry.limiter
	l.mu.Unlock()
	limiter.Allow()
}

// ipEntries returns the per-IP map size (tests).
func (l *CheckInLimiter) ipEntries() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips)
}

// throttledIP is the ResourceExhausted refusal for an address over its
// failed-verification budget.
func (l *CheckInLimiter) throttledIP(ip string) error {
	if l.m != nil {
		l.m.CheckinIPThrottled.Inc()
	}
	if ipThrottleLog.shouldLog(ip, time.Now()) {
		slog.Warn("rate limit: source address exceeded its failed check-in verification budget; refusing its check-ins",
			"source_ip", ip)
	}
	return status.Error(codes.ResourceExhausted, "too many failed check-in verifications from this address")
}

// ipThrottleLog throttles the per-IP refusal warning to once per address per
// window (same pattern as defaultUnknownEnvoyLog).
var ipThrottleLog = newUnknownEnvoyLog(10 * time.Minute)

// peerIP returns the caller's source IP for a network (TCP) peer, or "" for a
// local-socket or missing peer — the operator UDS path is never IP-limited.
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	switch a := p.Addr.(type) {
	case *net.TCPAddr:
		return a.IP.String()
	case *net.UnixAddr:
		return ""
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return a.String()
		}
		return host
	}
}
