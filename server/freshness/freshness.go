// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package freshness owns the canonical "is this envoy stale?" predicate
// shared across every surface that classifies envoy reachability — the
// UI envoys page, the doctor API, the risk scorer's "Lost contact"
// factor, the fleet rehydrator, and the stale-envoy notifier. Holding
// the threshold in a single leaf package keeps fleet, risk, and the UI
// in agreement without forming an import cycle.
package freshness

import "time"

// StaleThreshold is the canonical "envoy is offline" boundary, expressed
// as 2.5 × the agent check-in interval. The 2.5× value gives an envoy
// half a check-in beat of grace before it's declared lost (one missed
// beat is normal jitter; two missed beats is a signal). The 30 s "just
// now" bucket in the UI's timeAgo helper is also well inside this
// window at the default 5 m interval, eliminating the label/color
// disagreement at the boundary.
func StaleThreshold(baseInterval time.Duration) time.Duration {
	return 5 * baseInterval / 2 // 2.5×, integer-safe
}

// IsStale reports whether an envoy's last check-in is older than the
// canonical stale threshold. A zero lastSeen is treated as "not stale"
// rather than "infinitely stale" — fresh enrollments that have never
// reported are handled by the caller, not by this predicate.
func IsStale(lastSeen time.Time, baseInterval time.Duration) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) > StaleThreshold(baseInterval)
}
