// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"
	"time"
)

func TestUnknownEnvoyLogThrottle(t *testing.T) {
	l := newUnknownEnvoyLog(10 * time.Minute)
	t0 := time.Now()

	if !l.shouldLog("env-a", t0) {
		t.Fatal("first sighting of env-a should log")
	}
	if l.shouldLog("env-a", t0.Add(time.Minute)) {
		t.Error("env-a within the interval should be throttled")
	}
	if !l.shouldLog("env-a", t0.Add(11*time.Minute)) {
		t.Error("env-a after the interval should log again")
	}
	// A different envoy is throttled independently.
	if !l.shouldLog("env-b", t0.Add(time.Minute)) {
		t.Error("first sighting of env-b should log even while env-a is throttled")
	}
}

func TestUnknownEnvoyLogThrottleEvictsWhenLarge(t *testing.T) {
	l := newUnknownEnvoyLog(10 * time.Minute)
	old := time.Now()
	for i := 0; i < 1100; i++ {
		l.shouldLog(uuidish(i), old)
	}
	// A fresh call far in the future triggers lazy eviction of the stale
	// (>2× interval old) entries; the map must not grow without bound.
	l.shouldLog("trigger", old.Add(time.Hour))
	if got := len(l.last); got > 1024 {
		t.Errorf("expected stale entries evicted (len ≤ 1024), got %d", got)
	}
}

func uuidish(i int) string {
	return "envoy-" + time.Unix(int64(i), 0).Format("150405.000000000")
}
