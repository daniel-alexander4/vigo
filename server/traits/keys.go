// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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

	// SCAP / STIG compliance scan summary.
	ScapProfile       = "security_scan.scap.profile"
	ScapScore         = "security_scan.scap.score"
	ScapPass          = "security_scan.scap.pass"
	ScapFail          = "security_scan.scap.fail"
	ScapNotApplicable = "security_scan.scap.not_applicable"
	ScapHighFail      = "security_scan.scap.high_fail"
	ScapMediumFail    = "security_scan.scap.medium_fail"
	ScapLowFail       = "security_scan.scap.low_fail"
	ScapTotalRules    = "security_scan.scap.total_rules"
	ScapClean         = "security_scan.scap.clean"
	ScapLastScan      = "security_scan.scap.last_scan"

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

	// Running-binary CVE correlation. The agent joins scanner output
	// (trivy + debsecan) with /proc/*/exe + dpkg/rpm ownership, so
	// each row describes a CVE affecting a binary currently executing.
	// Entries are indexed: cve_running_correlation.running_cves.0.cve_id, etc.
	PrefixRunningCVEs           = "cve_running_correlation.running_cves."
	RunningCVEFieldID           = "cve_id"
	RunningCVEFieldSeverity     = "severity"
	RunningCVEFieldPackage      = "package"
	RunningCVEFieldPkgVersion   = "package_version"
	RunningCVEFieldBinaryPath   = "binary_path"
	RunningCVEFieldPidCount     = "pid_count"
	RunningCVEFieldDeleted      = "deleted"

	// Summary counts for the cve_running_correlation collector — useful
	// as direct trait queries without iterating the flattened rows.
	RunningCVETotal    = "cve_running_correlation.summary.total"
	RunningCVECritical = "cve_running_correlation.summary.critical"
	RunningCVEHigh     = "cve_running_correlation.summary.high"
	RunningCVEMedium   = "cve_running_correlation.summary.medium"
	RunningCVELow      = "cve_running_correlation.summary.low"
)

// Host power-loss detection (`host_self` collector). The agent records on the
// way down whether each session ended gracefully and reports what it found at
// the next boot — the server cannot derive this, because a hard cut is defined
// by the absence of anything being written.
//
// These are the Go half of a cross-language pair; the Rust emitter is
// `agent/src/traits/host_self.rs`. Nothing in either toolchain compares them,
// so `test/audit` TestPowerTraitKeysMatchAgent does.
const (
	// PowerDetectorState is "armed" | "first_boot" | "unsupported". The two
	// counts below are reported ONLY when armed — a zero from a detector that
	// has never observed a boot transition must not read as a healthy zero.
	PowerDetectorState = "host_self.power_detector_state"

	// UncleanShutdowns30d counts host power losses in a rolling 30-day window.
	UncleanShutdowns30d = "host_self.unclean_shutdowns_30d"

	// BootTransitions30d counts ALL reboots in the same window — the
	// denominator that makes a zero numerator readable.
	BootTransitions30d = "host_self.boot_transitions_30d"

	// LastUncleanDate is the newest power loss as YYYY-MM-DD. Day granularity
	// is deliberate: on a personal machine a wall-clock stamp records when a
	// named person was at it, and every viewer can read traits.
	LastUncleanDate = "host_self.last_unclean_date"
)
