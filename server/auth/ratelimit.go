// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// authLimiter bounds HTTP auth attempts per client IP. The HTTP login and
// TOTP paths have no equivalent of the gRPC RateLimitInterceptor, so without
// this an attacker holding a valid password (or a parked TOTP-pending
// session) could brute-force the 6-digit TOTP code unthrottled. The defaults
// are invisible to a human mistyping a password a few times while cutting
// automated brute force to a crawl.
//
// Same token-bucket-per-key shape as server/grpc RateLimitInterceptor, keyed
// on client IP instead of envoy ID. A stale-entry eviction loop keeps the map
// bounded under a churning source-IP attack.
type authLimiter struct {
	mu      sync.Mutex
	buckets map[string]*authBucketEntry
	limit   rate.Limit
	burst   int
}

type authBucketEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newAuthLimiter allows `burst` immediate attempts from a fresh IP, then one
// attempt per refill interval (perMinute attempts/minute sustained).
func newAuthLimiter(perMinute, burst int) *authLimiter {
	al := &authLimiter{
		buckets: make(map[string]*authBucketEntry),
		limit:   rate.Limit(float64(perMinute) / 60.0),
		burst:   burst,
	}
	go al.evictLoop()
	return al
}

// evictLoop drops per-IP buckets unused for 30 minutes so a rotating-source
// attack can't grow the map without bound. Runs for the process lifetime,
// matching the gRPC interceptor's eviction goroutine.
func (al *authLimiter) evictLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		al.mu.Lock()
		cutoff := time.Now().Add(-30 * time.Minute)
		for k, e := range al.buckets {
			if e.lastSeen.Before(cutoff) {
				delete(al.buckets, k)
			}
		}
		al.mu.Unlock()
	}
}

// allow consumes a token for key and reports whether the attempt may proceed.
func (al *authLimiter) allow(key string) bool {
	al.mu.Lock()
	e := al.buckets[key]
	if e == nil {
		e = &authBucketEntry{limiter: rate.NewLimiter(al.limit, al.burst)}
		al.buckets[key] = e
	}
	e.lastSeen = time.Now()
	limiter := e.limiter
	al.mu.Unlock()
	return limiter.Allow()
}

// clientIP returns the request's source IP with the port stripped, for use as
// a rate-limit key. Falls back to the raw RemoteAddr if it has no port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
