// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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

// TestBucketsFromTraits pins the one flattened-key walk risk scoring and the
// fleet cert card share: only cert_lifecycle.certificates.<N>.not_after keys,
// single-segment <N>, parseable timestamps.
func TestBucketsFromTraits(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	at := func(days int) string { return now.AddDate(0, 0, days).Format(time.RFC3339) }
	traits := map[string]string{
		"cert_lifecycle.certificates.0.not_after":   at(-2),  // expired
		"cert_lifecycle.certificates.1.not_after":   at(3),   // <7d
		"cert_lifecycle.certificates.2.not_after":   at(20),  // <30d
		"cert_lifecycle.certificates.3.not_after":   at(200), // fine
		"cert_lifecycle.certificates.4.not_after":   "garbage",
		"cert_lifecycle.certificates.5.x.not_after": at(-9), // nested: skipped
		"cert_lifecycle.certificates.6.expires":     at(-9), // not not_after: skipped
		"other.certificates.7.not_after":            at(-9), // wrong family: skipped
		"cert_lifecycle.certificates.not_after":     at(-9), // no index segment: skipped
	}
	b := BucketsFromTraits(traits, now)
	if b.Expired != 1 || b.In7 != 1 || b.In30 != 1 {
		t.Errorf("BucketsFromTraits = %+v, want 1 expired / 1 <7d / 1 <30d", b)
	}
}
