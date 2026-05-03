// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package risk computes per-envoy and fleet-wide risk scores from security
// scan traits, convergence status, compliance gaps, and connectivity data.
// Scores range from 0 (lowest risk) to 100 (highest risk).
package risk

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/traits"
)

// Level classifies a numeric risk score into a human-readable category.
type Level string

const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelCritical Level = "critical"
)

// EnvoyInput is the minimal data needed from an envoy to compute its risk score.
// Decoupled from fleet.EnvoyState to avoid import cycles.
type EnvoyInput struct {
	ID                string
	Hostname          string
	Revoked           bool
	LastSeen          time.Time
	ConvergenceStatus string
	Drift             convergence.DriftLevel
	Traits            map[string]string
}

// Factor is a single contributing factor to an envoy's risk score.
type Factor struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Detail string `json:"detail"`
}

// EnvoyRisk is the computed risk score for a single envoy.
type EnvoyRisk struct {
	EnvoyID  string   `json:"envoy_id"`
	Hostname string   `json:"hostname"`
	Score    int      `json:"score"`
	Level    Level    `json:"level"`
	Factors  []Factor `json:"factors"`
}

// FleetRisk is the aggregated risk posture for the entire fleet.
type FleetRisk struct {
	TotalEnvoys  int            `json:"total_envoys"`
	ScoredEnvoys int            `json:"scored_envoys"`
	AvgScore     float64        `json:"avg_score"`
	MaxScore     int            `json:"max_score"`
	Distribution map[Level]int  `json:"distribution"`
	TopRisks     []EnvoyRisk    `json:"top_risks"`
	Envoys       []EnvoyRisk    `json:"envoys"`
}

// ScoreEnvoy computes the risk score for a single envoy.
// baseInterval is the expected check-in interval (used for stale detection).
func ScoreEnvoy(es EnvoyInput, baseInterval time.Duration) EnvoyRisk {
	r := EnvoyRisk{
		EnvoyID:  es.ID,
		Hostname: es.Hostname,
	}

	if es.Revoked {
		r.Score = 0
		r.Level = Classify(0)
		return r
	}

	score := 0

	// --- Critical CVEs: 10 points each (unique, cross-scanner deduped) ---
	if n := traitInt(es.Traits, traits.UniqueCritical); n > 0 {
		pts := clamp(int(n) * 10)
		score += pts
		r.Factors = append(r.Factors, Factor{"Critical CVEs", pts, pluralize(int(n), "critical CVE")})
	}

	// --- High CVEs: 5 points each ---
	if n := traitInt(es.Traits, traits.UniqueHigh); n > 0 {
		pts := clamp(int(n) * 5)
		score += pts
		r.Factors = append(r.Factors, Factor{"High CVEs", pts, pluralize(int(n), "high CVE")})
	}

	// --- Medium CVEs: 2 points each ---
	if n := traitInt(es.Traits, traits.UniqueMedium); n > 0 {
		pts := clamp(int(n) * 2)
		score += pts
		r.Factors = append(r.Factors, Factor{"Medium CVEs", pts, pluralize(int(n), "medium CVE")})
	}

	// --- Hardening score: inverted (100 - lynis) ---
	if lynis := traitInt(es.Traits, traits.LynisScore); lynis > 0 {
		pts := clamp(100 - int(lynis))
		if pts > 0 {
			score += pts
			r.Factors = append(r.Factors, Factor{"Hardening Gap", pts, "Lynis score " + itoa(int(lynis)) + "/100"})
		}
	}

	// --- Rootkit/malware: +25 each ---
	if n := traitInt(es.Traits, traits.ChkrootkitInfected); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Rootkit (chkrootkit)", pts, pluralize(int(n), "infection")})
	}
	if n := traitInt(es.Traits, traits.RkhunterWarnings); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Rootkit (rkhunter)", pts, pluralize(int(n), "warning")})
	}
	if n := traitInt(es.Traits, traits.ClamavInfected); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Malware (ClamAV)", pts, pluralize(int(n), "infected file")})
	}

	// --- Integrity breach (AIDE): +15 ---
	if n := traitInt(es.Traits, traits.AideChanged); n > 0 {
		score += 15
		r.Factors = append(r.Factors, Factor{"Integrity Breach (AIDE)", 15, pluralize(int(n), "changed file")})
	}

	// --- Open ports: +1 per port ---
	if portCount := countTraitPrefix(es.Traits, "ports.listening."); portCount > 0 {
		pts := clamp(portCount)
		score += pts
		r.Factors = append(r.Factors, Factor{"Open Ports", pts, pluralize(portCount, "listening port")})
	}

	// --- Expiring / expired certificates (cert_lifecycle trait) ---
	// Weighting: expired +10 each, <7d +5 each, <30d +1 each. Aligned
	// with the "rootkit/malware" severity tier — an expired serving
	// cert can be as operationally damaging as a missed patch.
	if expired, in7, in30 := classifyCertExpiry(es.Traits); expired+in7+in30 > 0 {
		pts := clamp(expired*10 + in7*5 + in30*1)
		if pts > 0 {
			score += pts
			r.Factors = append(r.Factors, Factor{
				"Expiring Certificates",
				pts,
				certExpiryDetail(expired, in7, in30),
			})
		}
	}

	// --- Running-CVE bonus (cve_running_correlation trait) ---
	// The installed-CVE factors above already reflect the base risk
	// of having vulnerable packages on disk. This factor adds a bonus
	// weight specifically when an affected binary is currently
	// executing — a running vulnerable process is strictly worse than
	// one that's merely installed. Bonus: +5 per running critical,
	// +2 per running high, +1 per running medium. Running lows don't
	// contribute — the signal noise outweighs the marginal risk.
	rc := int(traitInt(es.Traits, traits.RunningCVECritical))
	rh := int(traitInt(es.Traits, traits.RunningCVEHigh))
	rm := int(traitInt(es.Traits, traits.RunningCVEMedium))
	if rc+rh+rm > 0 {
		pts := clamp(rc*5 + rh*2 + rm*1)
		if pts > 0 {
			score += pts
			r.Factors = append(r.Factors, Factor{
				"Running CVEs",
				pts,
				runningCVEDetail(rc, rh, rm),
			})
		}
	}

	// --- Failure axis: Failed/Degraded contribute points. ---
	switch convergence.ClassifyStatus(es.ConvergenceStatus) {
	case convergence.StatusFailed:
		score += 15
		r.Factors = append(r.Factors, Factor{"Convergence Failed", 15, "last run had failures"})
	case convergence.StatusDegraded:
		score += 10
		r.Factors = append(r.Factors, Factor{"Degraded", 10, "1-79% of modules failed in the last run"})
	}

	// --- Drift axis: persistent mutation across many recent runs ---
	if es.Drift == convergence.DriftDiverged {
		score += 15
		r.Factors = append(r.Factors, Factor{"Diverged", 15, "modules keep mutating across recent runs"})
	}

	// --- Stale check-in: +20 if offline ---
	if freshness.IsStale(es.LastSeen, baseInterval) {
		score += 20
		r.Factors = append(r.Factors, Factor{"Offline", 20, "no check-in within 2.5× interval"})
	}

	// Note: Windows/debsecan CVEs are now included in the unique_* counts above
	// via cross-scanner dedup at the agent level. No separate scoring needed.

	// Cap at 100.
	if score > 100 {
		score = 100
	}

	r.Score = score
	r.Level = Classify(score)
	return r
}

// ScoreFleet computes the fleet-wide risk summary from a slice of envoy inputs.
// topN controls how many highest-risk envoys are included in TopRisks (0 = default 10).
func ScoreFleet(envoys []EnvoyInput, baseInterval time.Duration, topN int) FleetRisk {
	if topN <= 0 {
		topN = 10
	}

	fr := FleetRisk{
		TotalEnvoys:  len(envoys),
		Distribution: map[Level]int{LevelLow: 0, LevelMedium: 0, LevelHigh: 0, LevelCritical: 0},
	}

	var totalScore int64
	for _, es := range envoys {
		if es.Revoked {
			continue
		}
		er := ScoreEnvoy(es, baseInterval)
		fr.Envoys = append(fr.Envoys, er)
		fr.ScoredEnvoys++
		totalScore += int64(er.Score)
		fr.Distribution[er.Level]++
		if er.Score > fr.MaxScore {
			fr.MaxScore = er.Score
		}
	}

	if fr.ScoredEnvoys > 0 {
		fr.AvgScore = float64(totalScore) / float64(fr.ScoredEnvoys)
	}

	// Sort by score descending for top risks.
	sort.Slice(fr.Envoys, func(i, j int) bool {
		return fr.Envoys[i].Score > fr.Envoys[j].Score
	})

	if len(fr.Envoys) > topN {
		fr.TopRisks = fr.Envoys[:topN]
	} else {
		fr.TopRisks = fr.Envoys
	}

	return fr
}

// Classify maps a 0-100 score to a risk level. Single source of truth
// for the score → level threshold mapping; CLI and UI both consume this
// rather than re-implementing the thresholds locally.
func Classify(score int) Level {
	switch {
	case score >= 70:
		return LevelCritical
	case score >= 40:
		return LevelHigh
	case score >= 20:
		return LevelMedium
	default:
		return LevelLow
	}
}

// traitInt parses a trait value as an int64, returning 0 if absent or unparseable.
func traitInt(traits map[string]string, key string) int64 {
	v, ok := traits[key]
	if !ok {
		return 0
	}
	var n int64
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}

// countTraitPrefix counts the number of distinct keys with the given prefix.
func countTraitPrefix(traits map[string]string, prefix string) int {
	count := 0
	for k := range traits {
		if strings.HasPrefix(k, prefix) {
			count++
		}
	}
	return count
}

// classifyCertExpiry walks the flattened `cert_lifecycle.certificates.N.days_remaining`
// values and buckets each into expired / expiring-within-7-days / expiring-within-30-days.
// Rows without a parseable days_remaining value are skipped. Certs with more than
// 30 days remaining don't contribute to any bucket.
func classifyCertExpiry(traitMap map[string]string) (expired, in7, in30 int) {
	const prefix = "cert_lifecycle.certificates."
	const suffix = ".days_remaining"
	for k, v := range traitMap {
		if !strings.HasPrefix(k, prefix) || !strings.HasSuffix(k, suffix) {
			continue
		}
		inner := k[len(prefix) : len(k)-len(suffix)]
		// Single segment only — skip nested keys from future trait shapes.
		if strings.Contains(inner, ".") {
			continue
		}
		days, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		switch {
		case days < 0:
			expired++
		case days < 7:
			in7++
		case days < 30:
			in30++
		}
	}
	return
}

// certExpiryDetail renders the factor detail string, emitting only the
// non-zero buckets so a host with one expired cert and no soon-expiring
// ones reads "1 expired", not "1 expired, 0 <7d, 0 <30d".
func certExpiryDetail(expired, in7, in30 int) string {
	var parts []string
	if expired > 0 {
		parts = append(parts, pluralize(expired, "expired cert"))
	}
	if in7 > 0 {
		parts = append(parts, itoa(in7)+" <7d")
	}
	if in30 > 0 {
		parts = append(parts, itoa(in30)+" <30d")
	}
	return strings.Join(parts, ", ")
}

// runningCVEDetail renders the factor detail string for the
// Running-CVE bonus: only non-zero buckets appear, ordered by severity.
func runningCVEDetail(critical, high, medium int) string {
	var parts []string
	if critical > 0 {
		parts = append(parts, itoa(critical)+" critical")
	}
	if high > 0 {
		parts = append(parts, itoa(high)+" high")
	}
	if medium > 0 {
		parts = append(parts, itoa(medium)+" medium")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " running"
}


// clamp ensures a point value is non-negative.
func clamp(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// pluralize returns "N thing" or "N things".
func pluralize(n int, thing string) string {
	s := itoa(n) + " " + thing
	if n != 1 {
		s += "s"
	}
	return s
}

// itoa is a minimal int-to-string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
