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
//
// Every public predicate takes an explicit `offlineThreshold` operator
// override. Zero means "use the default 2.5× rule"; a positive value
// replaces the formula with a literal duration. Callers thread the
// configured override through their own state (typically a `*Server`
// field) rather than reading from a package global, so multiple
// servers in one process keep independent thresholds.
package freshness

import "time"

// MaxReportedCycle clamps the agent-reported inter-CheckIn delta the
// server will trust. A compromised or buggy agent could otherwise
// report a huge value and hide itself from offline detection
// indefinitely; capping the input bounds the worst-case threshold to
// 2.5×30min = 75min. Operators with genuinely slower cycles should
// raise `checkin.interval` to match.
const MaxReportedCycle = 30 * time.Minute

// MinStaleThreshold floors the default stale boundary so the predicate
// stays decoupled from the cadence at the fast end. With the 1s default
// check-in interval, a raw 2.5× rule would mark an envoy offline after
// 2.5s — a single GC pause, scheduler hiccup, or TCP retransmit would
// flap it. A 30s floor tolerates ~30 missed 1s-beats while still
// detecting a genuinely-offline envoy ~25× faster than the old 5m
// default's 12.5min boundary. The floor only bites below a ~12s
// effective cadence (where 2.5× < 30s); slower deployments are
// unaffected. An explicit `checkin.offline_threshold:` override bypasses
// the floor entirely — operator intent wins.
const MinStaleThreshold = 30 * time.Second

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
// envoy. Default is 2.5 × max(configured, observed), floored at
// MinStaleThreshold — a half-beat of grace lets one missed check-in
// count as jitter; two missed beats is a real signal; and the floor
// keeps the boundary sane at sub-12s cadences (see MinStaleThreshold).
// A positive offlineThreshold replaces the default with a literal
// duration (operator's `checkin.offline_threshold:` in server.yaml)
// and bypasses the floor; zero means "use the default formula."
func StaleThresholdFromCycle(configured, observed, offlineThreshold time.Duration) time.Duration {
	if offlineThreshold > 0 {
		return offlineThreshold
	}
	eff := effectiveInterval(configured, observed)
	stale := 5 * eff / 2 // 2.5×, integer-safe
	if stale < MinStaleThreshold {
		return MinStaleThreshold
	}
	return stale
}

// StaleScanInterval returns how often the background stale-checker sweep
// should run. The sweep is a full-fleet pass, so its cost scales with
// fleet size × frequency; coupling it to the check-in cadence made it run
// every 1s at the default — far more often than the staleness threshold
// can act on, for a marginal detection-latency gain. We instead derive the
// cadence from the threshold: scanning at half the tightest stale boundary
// keeps worst-case offline detection within ~1.5× the threshold while
// cutting sweep frequency (15s vs 1s at the 1s default's 30s floor).
//
// tightest = offlineThreshold override if set, else MinStaleThreshold — the
// floor is the smallest threshold any envoy can have on the default path,
// so deriving from it never under-scans. Floored at baseInterval: detection
// can't beat the check-in cadence anyway, and a sane config always has
// threshold ≥ cadence so the floor only guards a pathological sub-cadence
// override.
func StaleScanInterval(baseInterval, offlineThreshold time.Duration) time.Duration {
	tightest := MinStaleThreshold
	if offlineThreshold > 0 {
		tightest = offlineThreshold
	}
	scan := tightest / 2
	if scan < baseInterval {
		return baseInterval
	}
	return scan
}

// IsStaleFromCycle reports whether an envoy's last check-in is older
// than its per-envoy stale threshold. A zero lastSeen is treated as
// "not stale" rather than "infinitely stale" — fresh enrollments that
// have never reported are handled by the caller, not by this
// predicate. observed = 0 means "no cycle reported yet" (bootstrap),
// in which case the threshold is derived from configured alone.
func IsStaleFromCycle(lastSeen time.Time, configured, observed, offlineThreshold time.Duration) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) > StaleThresholdFromCycle(configured, observed, offlineThreshold)
}

// responsiveZeroConfigFallback is the threshold used when the caller
// has no configured interval to base a per-envoy window on (e.g. the
// admin-query routing path before the server has loaded its config).
// Matches the pre-0.38.13 hardcoded fallback in
// `(*AgentService).isRecentlyActive` so callers see no behavior
// change in that path.
const responsiveZeroConfigFallback = 15 * time.Minute

// ResponsiveThreshold returns the per-envoy "is this envoy responsive
// enough that admin operations should bother dispatching to it?"
// boundary. Slightly looser than the staleness threshold (1.2×) — an
// envoy that's strictly *stale* by the UI rule may still be worth a
// best-effort dispatch attempt during the brief window before it
// falls off the responsive list. Honors the same offlineThreshold
// knob as the stale path; an operator who has tightened that to a
// literal value gets the same value here too.
//
// When configured, observed, and offlineThreshold are all zero
// (boot-order edge cases, tests), returns
// `responsiveZeroConfigFallback` (15m) so the caller doesn't get a
// degenerate zero-window predicate.
func ResponsiveThreshold(configured, observed, offlineThreshold time.Duration) time.Duration {
	if configured == 0 && observed == 0 && offlineThreshold == 0 {
		return responsiveZeroConfigFallback
	}
	stale := StaleThresholdFromCycle(configured, observed, offlineThreshold)
	// 1.2× the stale threshold. Multiply first to keep integer-safe
	// when stale is small; division by 5 instead of float math to
	// avoid bringing in math.Round behavior.
	return stale * 6 / 5
}

// IsResponsive reports whether an envoy's last check-in is recent
// enough that it's worth dispatching admin work to it. Distinct from
// IsStaleFromCycle: the staleness predicate answers "should the UI
// show this envoy as offline?", IsResponsive answers "should I bother
// trying to reach this envoy right now?" Mostly identical, slightly
// looser margin so a freshly-stale envoy still gets one round of
// best-effort dispatch attempts before the routing layer skips it.
//
// Same zero-lastSeen rule as IsStaleFromCycle: never-reported envoys
// (zero LastSeen) are not responsive — fresh enrollments live in a
// separate code path.
func IsResponsive(lastSeen time.Time, configured, observed, offlineThreshold time.Duration) bool {
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen) <= ResponsiveThreshold(configured, observed, offlineThreshold)
}
