// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package freshness owns the canonical "is this envoy stale?" predicate
// shared across every surface that classifies envoy reachability — the
// UI envoys page, the doctor API, the risk scorer's "Lost contact"
// factor, the fleet rehydrator, and the stale-envoy notifier. Holding
// the threshold in a single leaf package keeps fleet, risk, and the UI
// in agreement without forming an import cycle.
//
// The threshold is per-envoy: it scales with the agent's observed
// inter-CheckIn cadence rather than the operator's *configured*
// interval, because configured `checkin.interval` is aspirational —
// agents whose convergence cycle exceeds the configured interval
// achieve a slower actual cadence, and the staleness threshold has to
// track reality. Bootstrap (no observed cycle yet) falls back to the
// configured interval.
package freshness

import "time"

// MaxReportedCycle clamps the agent-reported inter-CheckIn delta the
// server will trust. A compromised or buggy agent could otherwise
// report a huge value and hide itself from offline detection
// indefinitely; capping the input bounds the worst-case threshold to
// 2.5×30min = 75min. Operators with genuinely slower cycles should
// raise `checkin.interval` to match.
const MaxReportedCycle = 30 * time.Minute

// override, when non-zero, replaces the default 2.5× rule with a literal
// duration. Set once at server startup from cfg.CheckIn.OfflineThreshold;
// never mutated at runtime. Read-only after init so no mutex is needed.
var override time.Duration

// SetOverride installs a literal stale threshold that bypasses the
// default 2.5×-the-effective-interval calculation. Pass 0 to clear the
// override and revert to the default. Called from server startup wiring
// based on the operator's `checkin.offline_threshold:` value in
// server.yaml. Not safe to call after the server has started serving
// traffic — the package-level read is unsynchronised on the hot path.
func SetOverride(d time.Duration) {
	override = d
}

// effectiveInterval returns the larger of configured and observed.
// Configured is the operator's intent; observed is the agent's actual
// inter-CheckIn cadence. We never go below configured (a fast agent
// shouldn't get a tighter window than the operator asked for); we
// widen to observed when the agent is genuinely slower than configured.
func effectiveInterval(configured, observed time.Duration) time.Duration {
	if observed > configured {
		return observed
	}
	return configured
}

// StaleThresholdFromCycle resolves the staleness boundary for one
// envoy. Default is 2.5 × max(configured, observed) — a half-beat of
// grace lets one missed check-in count as jitter; two missed beats is
// a real signal. Operator override (`checkin.offline_threshold:`)
// replaces the default with a literal duration when set.
func StaleThresholdFromCycle(configured, observed time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	eff := effectiveInterval(configured, observed)
	return 5 * eff / 2 // 2.5×, integer-safe
}

// IsStaleFromCycle reports whether an envoy's last check-in is older
// than its per-envoy stale threshold. A zero lastSeen is treated as
// "not stale" rather than "infinitely stale" — fresh enrollments that
// have never reported are handled by the caller, not by this
// predicate. observed = 0 means "no cycle reported yet" (bootstrap),
// in which case the threshold is derived from configured alone.
func IsStaleFromCycle(lastSeen time.Time, configured, observed time.Duration) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) > StaleThresholdFromCycle(configured, observed)
}
