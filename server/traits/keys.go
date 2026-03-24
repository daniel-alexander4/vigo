// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package traits

// Security scan trait key constants. All consumers of security_scan.* traits
// should use these constants instead of hardcoding key strings.

const (
	// PrefixSecurityScan is the top-level prefix for all security scan traits.
	PrefixSecurityScan = "security_scan."

	// PrefixCVEs is the prefix for the unified (cross-scanner) CVE entry list.
	// Entries are indexed: security_scan.cves.0.id, security_scan.cves.0.severity, etc.
	PrefixCVEs = "security_scan.cves."

	// Unified unique CVE counts (agent-authoritative, deduplicated across scanners).
	UniqueCritical = "security_scan.unique_critical"
	UniqueHigh     = "security_scan.unique_high"
	UniqueMedium   = "security_scan.unique_medium"
	UniqueLow      = "security_scan.unique_low"

	// CVE entry field suffixes (appended to PrefixCVEs + index + ".").
	CVEFieldID       = "id"
	CVEFieldSeverity = "severity"
	CVEFieldPackages = "packages"

	// Per-scanner raw counts (not deduplicated — show what each scanner found).
	TrivyCritical = "security_scan.trivy.critical"
	TrivyHigh     = "security_scan.trivy.high"
	TrivyMedium   = "security_scan.trivy.medium"
	TrivyLow      = "security_scan.trivy.low"
	TrivyTotal    = "security_scan.trivy.total"
	TrivyLastScan = "security_scan.trivy.last_scan"

	DebsecanHigh       = "security_scan.debsecan.high"
	DebsecanMedium     = "security_scan.debsecan.medium"
	DebsecanLow        = "security_scan.debsecan.low"
	DebsecanUnassigned = "security_scan.debsecan.unassigned"
	DebsecanTotal      = "security_scan.debsecan.total"
	DebsecanLastScan   = "security_scan.debsecan.last_scan"

	WinUpdatesCritical  = "security_scan.windows_updates.critical"
	WinUpdatesImportant = "security_scan.windows_updates.important"
	WinUpdatesModerate  = "security_scan.windows_updates.moderate"
	WinUpdatesLow       = "security_scan.windows_updates.low"
	WinUpdatesMissing   = "security_scan.windows_updates.missing"
	WinUpdatesLastScan  = "security_scan.windows_updates.last_scan"

	// Lynis hardening.
	LynisScore       = "security_scan.lynis.score"
	LynisWarnings    = "security_scan.lynis.warnings"
	LynisSuggestions = "security_scan.lynis.suggestions"
	LynisLastScan    = "security_scan.lynis.last_scan"

	// Rootkit detection.
	ChkrootkitClean    = "security_scan.chkrootkit.clean"
	ChkrootkitInfected = "security_scan.chkrootkit.infected"

	RkhunterClean    = "security_scan.rkhunter.clean"
	RkhunterWarnings = "security_scan.rkhunter.warnings"

	// Malware detection.
	ClamavClean    = "security_scan.clamav.clean"
	ClamavInfected = "security_scan.clamav.infected"

	// File integrity.
	AideClean   = "security_scan.aide.clean"
	AideAdded   = "security_scan.aide.added"
	AideRemoved = "security_scan.aide.removed"
	AideChanged = "security_scan.aide.changed"
)
