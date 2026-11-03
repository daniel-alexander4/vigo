// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"sync"
	"time"
)

// unknownEnvoyLog throttles "check-in from unknown envoy" warnings to once
// per envoy per interval, so an orphaned agent (a UUID with no DB row, e.g.
// after a server DB reset) or a stale client can't flood the log retrying a
// dead identity. The map is bounded by lazy eviction of entries older than
// 2× the interval once it grows past a cap, so a flood of distinct unknown
// ids can't grow it without bound.
type unknownEnvoyLog struct {
	mu       sync.Mutex
	last     map[string]time.Time
	interval time.Duration
}

func newUnknownEnvoyLog(interval time.Duration) *unknownEnvoyLog {
	return &unknownEnvoyLog{last: make(map[string]time.Time), interval: interval}
}

// shouldLog reports whether a warning for envoyID should be emitted at `now`,
// recording the time when it returns true.
func (l *unknownEnvoyLog) shouldLog(envoyID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[envoyID]; ok && now.Sub(t) < l.interval {
		return false
	}
	if len(l.last) > 1024 {
		for id, t := range l.last {
			if now.Sub(t) > 2*l.interval {
				delete(l.last, id)
			}
		}
	}
	l.last[envoyID] = now
	return true
}

// defaultUnknownEnvoyLog throttles the unknown-envoy warning to once per
// envoy per 10 minutes.
var defaultUnknownEnvoyLog = newUnknownEnvoyLog(10 * time.Minute)
