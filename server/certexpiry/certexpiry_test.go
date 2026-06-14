// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package certexpiry

import (
	"testing"
	"time"
)

func TestDaysRemaining(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		notAfter string
		wantDays int
		wantOK   bool
	}{
		{"empty", "", 0, false},
		{"unparseable", "soon", 0, false},
		{"future", now.AddDate(0, 0, 30).Format(time.RFC3339), 30, true},
		{"expired", now.AddDate(0, 0, -5).Format(time.RFC3339), -5, true},
		// Just-expired (<24h ago) must round to a negative day count so the
		// Expired bucket is reachable; integer truncation would yield 0.
		{"expired-1h", now.Add(-1 * time.Hour).Format(time.RFC3339), -1, true},
		{"expired-23h", now.Add(-23 * time.Hour).Format(time.RFC3339), -1, true},
		{"rfc3339nano", now.Add(48 * time.Hour).Format(time.RFC3339Nano), 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			days, ok := DaysRemaining(c.notAfter, now)
			if ok != c.wantOK || (ok && days != c.wantDays) {
				t.Errorf("DaysRemaining(%q) = (%d,%v), want (%d,%v)", c.notAfter, days, ok, c.wantDays, c.wantOK)
			}
		})
	}
}

// TestJustExpiredBucketsAsExpired pins the D42 fix end-to-end: a cert that
// expired 1h ago derives a negative day count and lands in the Expired
// bucket, not the In7 ("<7d") bucket.
func TestJustExpiredBucketsAsExpired(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	days, ok := DaysRemaining(now.Add(-1*time.Hour).Format(time.RFC3339), now)
	if !ok {
		t.Fatalf("DaysRemaining ok=false for a parseable cert")
	}
	var b Buckets
	b.Add(days)
	if b.Expired != 1 || b.In7 != 0 || b.In30 != 0 {
		t.Errorf("just-expired cert bucketed as %+v, want {Expired:1 In7:0 In30:0}", b)
	}
}

func TestBucketsAdd(t *testing.T) {
	var b Buckets
	for _, d := range []int{-10, -1, 0, 3, 6, 7, 20, 29, 30, 100} {
		b.Add(d)
	}
	// expired: -10,-1 (2); <7: 0,3,6 (3); <30: 7,20,29 (3); 30 and 100 → none.
	if b.Expired != 2 || b.In7 != 3 || b.In30 != 3 {
		t.Errorf("buckets = %+v, want {Expired:2 In7:3 In30:3}", b)
	}
}
