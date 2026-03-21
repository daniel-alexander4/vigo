// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package risk computes per-envoy and fleet-wide risk scores from security
// scan traits, convergence status, compliance gaps, and connectivity data.
// Scores range from 0 (lowest risk) to 100 (highest risk).
package risk

import (
	"sort"
	"strings"
	"time"
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
		r.Level = classify(0)
		return r
	}

	score := 0

	// --- Critical CVEs: 10 points each ---
	if n := traitInt(es.Traits, "security_scan.trivy.critical"); n > 0 {
		pts := clamp(int(n) * 10)
		score += pts
		r.Factors = append(r.Factors, Factor{"Critical CVEs", pts, pluralize(int(n), "critical CVE")})
	}

	// --- High CVEs: 5 points each ---
	if n := traitInt(es.Traits, "security_scan.trivy.high"); n > 0 {
		pts := clamp(int(n) * 5)
		score += pts
		r.Factors = append(r.Factors, Factor{"High CVEs", pts, pluralize(int(n), "high CVE")})
	}

	// --- Medium CVEs: 2 points each ---
	if n := traitInt(es.Traits, "security_scan.trivy.medium"); n > 0 {
		pts := clamp(int(n) * 2)
		score += pts
		r.Factors = append(r.Factors, Factor{"Medium CVEs", pts, pluralize(int(n), "medium CVE")})
	}

	// --- Hardening score: inverted (100 - lynis) ---
	if lynis := traitInt(es.Traits, "security_scan.lynis.score"); lynis > 0 {
		pts := clamp(100 - int(lynis))
		if pts > 0 {
			score += pts
			r.Factors = append(r.Factors, Factor{"Hardening Gap", pts, "Lynis score " + itoa(int(lynis)) + "/100"})
		}
	}

	// --- Rootkit/malware: +25 each ---
	if n := traitInt(es.Traits, "security_scan.chkrootkit.infected"); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Rootkit (chkrootkit)", pts, pluralize(int(n), "infection")})
	}
	if n := traitInt(es.Traits, "security_scan.rkhunter.warnings"); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Rootkit (rkhunter)", pts, pluralize(int(n), "warning")})
	}
	if n := traitInt(es.Traits, "security_scan.clamav.infected"); n > 0 {
		pts := clamp(int(n) * 25)
		score += pts
		r.Factors = append(r.Factors, Factor{"Malware (ClamAV)", pts, pluralize(int(n), "infected file")})
	}

	// --- Integrity breach (AIDE): +15 ---
	if n := traitInt(es.Traits, "security_scan.aide.changed"); n > 0 {
		score += 15
		r.Factors = append(r.Factors, Factor{"Integrity Breach (AIDE)", 15, pluralize(int(n), "changed file")})
	}

	// --- Open ports: +1 per port ---
	if portCount := countTraitPrefix(es.Traits, "ports.listening."); portCount > 0 {
		pts := clamp(portCount)
		score += pts
		r.Factors = append(r.Factors, Factor{"Open Ports", pts, pluralize(portCount, "listening port")})
	}

	// --- Convergence drift: +10 if recurring ---
	switch es.ConvergenceStatus {
	case "recurring drift":
		score += 10
		r.Factors = append(r.Factors, Factor{"Recurring Drift", 10, "convergence failing to hold"})
	case "errors":
		score += 15
		r.Factors = append(r.Factors, Factor{"Convergence Errors", 15, "last run had failures"})
	}

	// --- Stale check-in: +20 if lost contact ---
	if !es.LastSeen.IsZero() && time.Since(es.LastSeen) > 2*baseInterval {
		score += 20
		r.Factors = append(r.Factors, Factor{"Lost Contact", 20, "no check-in within 2× interval"})
	}

	// --- Windows update CVEs ---
	if n := countTraitPrefix(es.Traits, "security_scan.windows_updates.cves."); n > 0 {
		pts := clamp(n * 5)
		score += pts
		r.Factors = append(r.Factors, Factor{"Windows Update CVEs", pts, pluralize(n, "unpatched CVE")})
	}

	// --- Debsecan CVEs ---
	if n := countCVEEntries(es.Traits, "security_scan.debsecan.cves."); n > 0 {
		pts := clamp(n * 3)
		score += pts
		r.Factors = append(r.Factors, Factor{"Debian CVEs (debsecan)", pts, pluralize(n, "CVE")})
	}

	// Cap at 100.
	if score > 100 {
		score = 100
	}

	r.Score = score
	r.Level = classify(score)
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

// classify maps a 0-100 score to a risk level.
func classify(score int) Level {
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

// countCVEEntries counts distinct CVE entries by looking for ".cve_id" suffix
// under a given prefix (e.g., "security_scan.debsecan.cves.").
func countCVEEntries(traits map[string]string, prefix string) int {
	count := 0
	for k := range traits {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".cve_id") {
			count++
		}
	}
	return count
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
