// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"sync"
	"time"
)

// unknownEnvoyLogCap bounds how many envoy ids an unknownEnvoyLog remembers.
const unknownEnvoyLogCap = 1024

// unknownEnvoyLog throttles "check-in from unknown envoy" warnings to once
// per envoy per interval, so an orphaned agent (a UUID with no DB row, e.g.
// after a server DB reset) or a stale client can't flood the log retrying a
// dead identity. It remembers at most `cap` ids: a fixed ring holds them in
// insertion order and a new id past the cap evicts the oldest, so a flood of
// distinct unknown ids costs O(1) per call and can't grow it. An evicted id
// simply logs again on its next attempt.
type unknownEnvoyLog struct {
	mu       sync.Mutex
	last     map[string]time.Time
	ring     []string // insertion order; len == cap once full
	next     int      // ring slot the next new id overwrites once full
	cap      int
	interval time.Duration
}

func newUnknownEnvoyLog(interval time.Duration) *unknownEnvoyLog {
	return newUnknownEnvoyLogCap(interval, unknownEnvoyLogCap)
}

func newUnknownEnvoyLogCap(interval time.Duration, capacity int) *unknownEnvoyLog {
	return &unknownEnvoyLog{
		last:     make(map[string]time.Time, capacity),
		ring:     make([]string, 0, capacity),
		cap:      capacity,
		interval: interval,
	}
}

// shouldLog reports whether a warning for envoyID should be emitted at `now`,
// recording the time when it returns true.
func (l *unknownEnvoyLog) shouldLog(envoyID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, known := l.last[envoyID]
	if known && now.Sub(t) < l.interval {
		return false
	}
	if !known {
		if len(l.ring) < l.cap {
			l.ring = append(l.ring, envoyID)
		} else {
			delete(l.last, l.ring[l.next])
			l.ring[l.next] = envoyID
			l.next = (l.next + 1) % l.cap
		}
	}
	l.last[envoyID] = now
	return true
}

// defaultUnknownEnvoyLog throttles the unknown-envoy warning to once per
// envoy per 10 minutes.
var defaultUnknownEnvoyLog = newUnknownEnvoyLog(10 * time.Minute)
