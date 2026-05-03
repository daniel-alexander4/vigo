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

// override, when non-zero, replaces the default 2.5× rule with a literal
// duration. Set once at server startup from cfg.CheckIn.OfflineThreshold;
// never mutated at runtime. Read-only after init so no mutex is needed.
var override time.Duration

// SetOverride installs a literal stale threshold that bypasses the
// default 2.5×-the-check-in-interval calculation. Pass 0 to clear the
// override and revert to the default. Called from server startup wiring
// based on the operator's `checkin.offline_threshold:` value in
// server.yaml. Not safe to call after the server has started serving
// traffic — the package-level read is unsynchronised on the hot path.
func SetOverride(d time.Duration) {
	override = d
}

// StaleThreshold is the canonical "envoy is offline" boundary. By default
// it's 2.5 × the agent check-in interval — a half-beat of grace lets one
// missed check-in count as jitter; two missed beats is a real signal. An
// operator override (`checkin.offline_threshold:` in server.yaml) replaces
// the default with a literal duration when set. Used by every staleness
// surface (UI envoys page, doctor API, risk scorer "Lost contact" factor,
// fleet rehydrator, stale-envoy notifier) so reachability classification
// can never disagree across surfaces.
func StaleThreshold(baseInterval time.Duration) time.Duration {
	if override > 0 {
		return override
	}
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
