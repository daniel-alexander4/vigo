// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"
)

// TestStreamBackoff checks the un-jittered base sequence (exponential, capped)
// and that every returned delay lands in the full-jitter band [base/2, base].
func TestStreamBackoff(t *testing.T) {
	var b streamBackoff
	// 5,10,20,40,80,160 then capped at 300 (5m) — 160×2=320 clamps to 300.
	wantBaseSec := []int{5, 10, 20, 40, 80, 160, 300, 300, 300}
	for i, sec := range wantBaseSec {
		base := time.Duration(sec) * time.Second
		d := b.next()
		if d < base/2 || d > base {
			t.Errorf("next() call %d: got %v, want in [%v, %v]", i, d, base/2, base)
		}
	}
}
