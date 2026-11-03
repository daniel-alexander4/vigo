// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package certexpiry is the single source of truth for certificate-expiry
// derivation and bucketing, shared by risk scoring and the fleet/UI cert
// surfaces.
//
// days-remaining is deliberately NOT a stored or transmitted trait: it is a
// pure function of the certificate's not_after timestamp and the current time,
// so it would tick down daily and flap the ADR-033 inventory hash (forcing a
// full ~200 KB re-send per envoy per day). The agent emits only not_after;
// every server-side consumer derives days-remaining here.
package certexpiry

import (
	"time"

	"git.alexander4.io/vigo/server/uifmt"
)

// Expiry buckets, in days remaining. A certificate is "expiring soon" once it
// has fewer than SoonThreshold days left; UrgentThreshold marks the tighter
// near-term bucket.
const (
	UrgentThreshold = 7
	SoonThreshold   = 30
)

// DaysRemaining returns whole days between now and the certificate's not_after
// (negative once expired), derived from the agent-reported not_after timestamp.
// ok is false when notAfter is empty or unparseable — callers skip such rows,
// matching the prior behavior for an unparseable days_remaining value.
func DaysRemaining(notAfter string, now time.Time) (days int, ok bool) {
	if notAfter == "" {
		return 0, false
	}
	t, err := uifmt.ParseTimestamp(notAfter)
	if err != nil {
		return 0, false
	}
	d := t.Sub(now)
	// Integer division truncates toward zero, so a cert that expired
	// less than 24h ago would land at days==0 and mis-bucket as "expiring
	// soon" rather than Expired. Any already-expired cert (not_after at or
	// before now) must report a negative day count so the Expired bucket
	// (days < 0) is reachable in the first 24h after expiry.
	if d <= 0 {
		days := int(d / (24 * time.Hour))
		if days >= 0 {
			days = -1
		}
		return days, true
	}
	return int(d / (24 * time.Hour)), true
}

// Buckets accumulates certificates into the standard expiry buckets. A cert
// counts in at most one bucket; certs with >= SoonThreshold days left count in
// none.
type Buckets struct {
	Expired int // already expired (days < 0)
	In7     int // expiring within UrgentThreshold days
	In30    int // expiring within SoonThreshold days
}

// Add files one certificate's days-remaining into the matching bucket.
func (b *Buckets) Add(days int) {
	switch {
	case days < 0:
		b.Expired++
	case days < UrgentThreshold:
		b.In7++
	case days < SoonThreshold:
		b.In30++
	}
}
