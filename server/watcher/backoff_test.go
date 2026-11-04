// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package watcher

import (
	"testing"
	"time"
)

// TestBackoffSleepNeverCollapses: the backoff must stay at the cap however
// long the provider stays down — `1 << failures` wrapped to 0 at 64, making
// the sleep 0 (a hot loop against the provider).
func TestBackoffSleepNeverCollapses(t *testing.T) {
	interval := 10 * time.Second
	if got := backoffSleep(0, interval); got != interval {
		t.Errorf("backoffSleep(0) = %v, want %v", got, interval)
	}
	if got := backoffSleep(1, interval); got != 2*interval {
		t.Errorf("backoffSleep(1) = %v, want %v", got, 2*interval)
	}
	for _, f := range []int{5, 6, 31, 63, 64, 65, 1000, 1 << 30} {
		if got := backoffSleep(f, interval); got != maxBackoffMultiplier*interval {
			t.Errorf("backoffSleep(%d) = %v, want %v", f, got, maxBackoffMultiplier*interval)
		}
	}
}
