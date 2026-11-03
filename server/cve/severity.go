// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package cve owns the canonical CVE severity vocabulary. Both CLI and
// UI surfaces classify input severity strings through this package and
// then apply their own rendering (ANSI for CLI, CSS class for UI).
package cve

import "strings"

// SeverityLevel is the canonical bucket a CVE severity string lands in.
// The numeric ordering is meaningful: higher values are more severe, so
// callers can compare with > / >= when filtering.
type SeverityLevel int

const (
	SevUnassigned SeverityLevel = iota
	SevInfo
	SevLow
	SevMedium
	SevHigh
	SevCritical
)

// Classify normalizes a severity string ("Critical", "high", "MEDIUM",
// "informational", "") into the canonical bucket. Unknown vocabularies
// land in SevUnassigned; callers that want to detect this should compare
// to SevUnassigned explicitly.
func Classify(s string) SeverityLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return SevCritical
	case "high":
		return SevHigh
	case "medium":
		return SevMedium
	case "low":
		return SevLow
	case "info", "informational":
		return SevInfo
	default:
		return SevUnassigned
	}
}

// Norm returns the canonical lowercase form for URLs, JSON keys, and
// CSS class suffixes. Empty input yields "unassigned".
func Norm(s string) string {
	if s == "" {
		return "unassigned"
	}
	return strings.ToLower(strings.TrimSpace(s))
}
