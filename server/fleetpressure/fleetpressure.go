// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package fleetpressure is the single source of truth for translating
// per-envoy resource-and-repo traits into an "agent health" composite —
// FDs (`agent_self.*` / `host_self.*`), rootfs and inode pressure on `/`
// (`mountpoints.mounts./.*`), and package-repo health (`repo_health.*`).
// The dashboard rollup card, the `/agent-health` fleet page, and the
// per-envoy panel all consume types from this package so the same
// numbers and thresholds can never disagree across surfaces.
//
// The package owns *data*, not *rendering*: callers build whatever
// HTML / JSON / CSS classes they need, but every threshold decision
// flows through [Compute] (see envoyStatsSot feedback memory).
package fleetpressure

import (
	"strconv"
)

// Threshold breakpoints. Crossing the warn boundary trips a yellow
// classification; crossing fail trips a red one. Values are pinned
// to the "Surface agent + host pressure traits in the UI" pending
// entry — adjust here, not at the read sites.
const (
	OpenFDsWarn        = 250
	OpenFDsFail        = 500
	DescendantFDsWarn  = 200
	DescendantFDsFail  = 500
	HostFDPctWarn      = 50.0
	HostFDPctFail      = 80.0
	// Rootfs / inode breakpoints follow standard disk-pressure
	// practice: warn at 80% so there's room to act before fail at 90%.
	RootfsPctWarn      = 80.0
	RootfsPctFail      = 90.0
	InodePctWarn       = 80.0
	InodePctFail       = 90.0
	// Battery wear health is inverted vs the pressure metrics above —
	// lower is worse. Warn once a cell drops below 80% of its design
	// capacity, fail below 70% (a battery that won't hold a useful charge).
	BatteryHealthWarn  = 80.0
	BatteryHealthFail  = 70.0
)

// Severity is the threshold-classification result. Higher numeric
// values indicate worse states; the UI should display Fail in red,
// Warn in yellow, OK / Unknown in neutral.
type Severity int

const (
	// Unknown means the trait was missing or unparseable — distinct
	// from OK so the UI can render "—" instead of "0" / "fine".
	Unknown Severity = iota
	OK
	Warn
	Fail
)

// String returns the lower-case keyword used in HTML class
// suffixes (text-{warning,danger}, stat-card--{warn,fail}).
func (s Severity) String() string {
	switch s {
	case Fail:
		return "fail"
	case Warn:
		return "warn"
	case OK:
		return "ok"
	default:
		return "unknown"
	}
}

// Metric is one (value, severity) pair. The presence flag is
// separate from severity because zero-value uint64 is ambiguous —
// a missing trait and an envoy that genuinely has zero descendants
// look the same numerically but render differently.
type Metric struct {
	Value    uint64
	Present  bool
	Severity Severity
}

// PercentMetric mirrors Metric for floating-point values.
type PercentMetric struct {
	Value    float64
	Present  bool
	Severity Severity
}

// EnvoyPressure is the per-envoy projection. Mirrors the
// `agent_self.*` / `host_self.*` / `mountpoints.*` / `repo_health.*`
// trait shape — see `agent/src/traits/` for the authoritative
// definitions.
type EnvoyPressure struct {
	ID                 string
	Hostname           string

	// agent_self
	RSSBytes           Metric
	OpenFDs            Metric
	Threads            Metric
	Descendants        Metric
	DescendantOpenFDs  Metric
	UptimeSecs         Metric

	// host_self (Linux only — non-Linux envoys leave these absent)
	FDUsed             Metric
	FDMax              Metric
	FDPct              PercentMetric
	ProcCount          Metric
	OomRecent          Metric

	// mountpoints — rootfs (`/`) disk and inode pressure.
	RootfsPct          PercentMetric
	InodePct           PercentMetric

	// battery — worst-cell wear health (full ÷ design, %). Absent on
	// hosts with no `BAT*` node (servers/desktops). Lower is worse.
	BatteryHealth      PercentMetric

	// uptime — host (system) uptime in seconds (the `uptime.seconds`
	// trait). Informational, not a health signal — display only, like
	// the agent's own UptimeSecs.
	HostUptimeSecs     Metric

	// repo_health — package-repository health (apt / dnf). Absent
	// until the agent emits a `repo_health.status` trait; warnings
	// and errors counts surface for the per-envoy detail view.
	RepoHealth         RepoHealthMetric

	// AgentVersion is the version the envoy reported running;
	// ExpectedAgentVersion is the version the server would auto-update
	// this envoy's arch to (the per-(os,arch) dist binary). Both are
	// resolved by the fleet datasource — see AgentVersionCurrent. These
	// drive the separate green/yellow "Agent Version" surface and are
	// deliberately NOT folded into Health: version currency is an
	// orthogonal axis from resource pressure.
	AgentVersion         string
	ExpectedAgentVersion string

	// Health is the worst Severity across every colored field above —
	// FDs (open + descendant + host%), OOM-recent, rootfs%, inode%,
	// and repo-health. Drives the dashboard card's color, the
	// `/agent-health` row coloring, and the offender drilldown.
	// Unknown only if every colored field is also Unknown.
	Health             Severity
}

// RepoHealthMetric mirrors Metric for the package-repo trait. The agent
// emits `repo_health.status` as one of "ok" / "warn" / "fail" (anything
// else, or a missing trait, leaves Status="" and Severity=Unknown);
// Warnings and Errors are integer counts from the last update parse.
type RepoHealthMetric struct {
	Status    string
	Warnings  uint64
	Errors    uint64
	Present   bool
	Severity  Severity
}

// FleetSummary is the dashboard rollup. Holds the loud numbers
// and the offender hostname so the card can drill down to the
// per-envoy view without re-walking the fleet.
type FleetSummary struct {
	// WorstSeverity is the worst per-envoy Health across the fleet —
	// drives the dashboard "Agent Health" card's color. Unknown only
	// when no envoy reported any of the colored fields.
	WorstSeverity        Severity
	// DegradedCount is the number of envoys whose Health is Warn or
	// Fail — drives the card's value.
	DegradedCount        int

	// MaxOpenFDs is the highest open_fds value seen across the
	// fleet, with the hostname that hit it. Both fields are zero
	// when no envoy reported the trait.
	MaxOpenFDs           uint64
	MaxOpenFDsHost       string
	MaxOpenFDsID         string
	MaxOpenFDsSeverity   Severity

	// Hosts with kernel-wide FD pressure > HostFDPctWarn.
	HighFDPctCount       int
	HighFDPctExample     string // one example hostname for the drilldown

	// Hosts that had OOM-killer activity in the recent kern.log
	// window (host_self.oom_recent == 1).
	RecentOomCount       int
	RecentOomExample     string

	// TotalReporting is the number of envoys with at least one of
	// the colored fields populated. Lets the UI distinguish
	// "everything green" from "no data yet."
	TotalReporting       int
}

// EnvoySource is the minimal projection of an envoy that this
// package consumes. The fleet's in-memory state and the test
// harness both produce this shape; depending on a primitive
// triple instead of `*fleet.EnvoyState` keeps the package free
// of upward dependencies (the fleet package needs to call us).
type EnvoySource struct {
	ID       string
	Hostname string
	Traits   map[string]string

	// AgentVersion is the envoy's reported agent version (column-first,
	// `identity.agent_version`-trait fallback). ExpectedAgentVersion is
	// the per-(os,arch) dist version the server would auto-update it to,
	// or "" when no binary is published for that arch. Both are stamped
	// by the fleet datasource; the fleetpressure package only compares
	// them — see AgentVersionCurrent.
	AgentVersion         string
	ExpectedAgentVersion string
}

// Compute projects a single EnvoySource into an EnvoyPressure.
// Missing traits leave fields with Present=false; numeric parse
// errors are treated as missing (defensive: a malformed trait
// shouldn't poison the rollup).
func Compute(src EnvoySource) EnvoyPressure {
	p := EnvoyPressure{
		ID:                   src.ID,
		Hostname:             src.Hostname,
		AgentVersion:         src.AgentVersion,
		ExpectedAgentVersion: src.ExpectedAgentVersion,
	}
	p.RSSBytes = uintMetric(src.Traits, "agent_self.rss_bytes", classifyNeutral)
	p.OpenFDs = uintMetric(src.Traits, "agent_self.open_fds", classifyOpenFDs)
	p.Threads = uintMetric(src.Traits, "agent_self.threads", classifyNeutral)
	p.Descendants = uintMetric(src.Traits, "agent_self.descendants", classifyNeutral)
	p.DescendantOpenFDs = uintMetric(src.Traits, "agent_self.descendant_open_fds", classifyDescendantFDs)
	p.UptimeSecs = UptimeMetric(src.Traits)
	p.FDUsed = uintMetric(src.Traits, "host_self.fd_used", classifyNeutral)
	p.FDMax = uintMetric(src.Traits, "host_self.fd_max", classifyNeutral)
	p.FDPct = floatMetric(src.Traits, "host_self.fd_pct", classifyHostFDPct)
	p.ProcCount = uintMetric(src.Traits, "host_self.proc_count", classifyNeutral)
	p.OomRecent = uintMetric(src.Traits, "host_self.oom_recent", classifyOomRecent)
	// Rootfs (`/`) disk + inode pressure. FlattenJSON uses dot
	// separators and doesn't escape slashes, so the `/` mount key is
	// literal — see server/fleet/index.go FlattenJSON + the trait
	// shape in agent/src/traits/mountpoints.rs.
	p.RootfsPct = floatMetric(src.Traits, "mountpoints.mounts./.used_pct", classifyDiskPct)
	p.InodePct = floatMetric(src.Traits, "mountpoints.mounts./.inode_pct", classifyInodePct)
	p.BatteryHealth = floatMetric(src.Traits, "battery.health_pct", classifyBatteryHealth)
	p.HostUptimeSecs = uintMetric(src.Traits, "uptime.seconds", classifyNeutral)
	p.RepoHealth = repoHealthMetric(src.Traits)
	if src.Traits == nil {
		// Distinguish "no data" from "everything OK" — leave
		// Health as the zero value (Unknown).
		return p
	}
	p.Health = worstOf(
		p.OpenFDs.Severity,
		p.DescendantOpenFDs.Severity,
		p.FDPct.Severity,
		p.OomRecent.Severity,
		p.RootfsPct.Severity,
		p.InodePct.Severity,
		p.BatteryHealth.Severity,
		p.RepoHealth.Severity,
	)
	return p
}

// AgentVersionCurrent reports whether the envoy is running the agent
// version the server would auto-update its arch to. It is green (true)
// only on a verified match — both the reported version and the expected
// per-(os,arch) dist version are known and equal. Every other case is
// "not current" (yellow): a real mismatch, no published binary for the
// envoy's arch (ExpectedAgentVersion == ""), or an agent that reported no
// version (AgentVersion == ""). It is never red: the version string is
// post-authentication, self-reported metadata (mTLS + per-request ed25519
// already gate access), so this is a currency signal, not a trust signal.
// Intentionally independent of Health — version currency is a separate
// axis from resource pressure.
func (e EnvoyPressure) AgentVersionCurrent() bool {
	return e.AgentVersion != "" && e.ExpectedAgentVersion != "" &&
		e.AgentVersion == e.ExpectedAgentVersion
}

// Aggregate walks a slice of EnvoySources and produces the
// dashboard rollup plus the per-envoy slice the `/agent-health`
// page table needs. Iteration order follows the input slice;
// offender selection is stable (first envoy hitting the max
// wins, ties broken by input order).
func Aggregate(sources []EnvoySource) (FleetSummary, []EnvoyPressure) {
	var sum FleetSummary
	out := make([]EnvoyPressure, 0, len(sources))
	for _, src := range sources {
		p := Compute(src)
		out = append(out, p)
		hasAny := p.OpenFDs.Present || p.DescendantOpenFDs.Present ||
			p.FDPct.Present || p.OomRecent.Present ||
			p.RootfsPct.Present || p.InodePct.Present ||
			p.BatteryHealth.Present || p.RepoHealth.Present
		if hasAny {
			sum.TotalReporting++
		}
		if p.Health >= Warn {
			sum.DegradedCount++
		}
		if p.Health != Unknown && p.Health > sum.WorstSeverity {
			sum.WorstSeverity = p.Health
		}
		if p.OpenFDs.Present && p.OpenFDs.Value > sum.MaxOpenFDs {
			sum.MaxOpenFDs = p.OpenFDs.Value
			sum.MaxOpenFDsHost = p.Hostname
			sum.MaxOpenFDsID = p.ID
			sum.MaxOpenFDsSeverity = p.OpenFDs.Severity
		}
		if p.FDPct.Present && p.FDPct.Severity >= Warn {
			sum.HighFDPctCount++
			if sum.HighFDPctExample == "" {
				sum.HighFDPctExample = p.Hostname
			}
		}
		if p.OomRecent.Present && p.OomRecent.Value != 0 {
			sum.RecentOomCount++
			if sum.RecentOomExample == "" {
				sum.RecentOomExample = p.Hostname
			}
		}
	}
	return sum, out
}

// UptimeMetric extracts the agent's process uptime (the `agent_self.uptime_secs`
// trait) as a neutral Metric. Exported so surfaces outside the full pressure
// rollup — e.g. the envoy-detail hero card — read uptime through this one SoT
// instead of re-parsing the trait inline. nil/missing traits yield Present=false.
func UptimeMetric(traits map[string]string) Metric {
	return uintMetric(traits, "agent_self.uptime_secs", classifyNeutral)
}

// ---------- internal helpers ----------

func uintMetric(traits map[string]string, key string, classify func(uint64) Severity) Metric {
	raw, ok := traits[key]
	if !ok || raw == "" {
		return Metric{Severity: Unknown}
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return Metric{Severity: Unknown}
	}
	return Metric{Value: v, Present: true, Severity: classify(v)}
}

func floatMetric(traits map[string]string, key string, classify func(float64) Severity) PercentMetric {
	raw, ok := traits[key]
	if !ok || raw == "" {
		return PercentMetric{Severity: Unknown}
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return PercentMetric{Severity: Unknown}
	}
	return PercentMetric{Value: v, Present: true, Severity: classify(v)}
}

func classifyNeutral(_ uint64) Severity   { return OK }

func classifyOpenFDs(v uint64) Severity {
	switch {
	case v > OpenFDsFail:
		return Fail
	case v > OpenFDsWarn:
		return Warn
	default:
		return OK
	}
}

func classifyDescendantFDs(v uint64) Severity {
	switch {
	case v > DescendantFDsFail:
		return Fail
	case v > DescendantFDsWarn:
		return Warn
	default:
		return OK
	}
}

func classifyHostFDPct(v float64) Severity {
	switch {
	case v > HostFDPctFail:
		return Fail
	case v > HostFDPctWarn:
		return Warn
	default:
		return OK
	}
}

func classifyOomRecent(v uint64) Severity {
	if v != 0 {
		return Warn
	}
	return OK
}

func classifyDiskPct(v float64) Severity {
	switch {
	case v > RootfsPctFail:
		return Fail
	case v > RootfsPctWarn:
		return Warn
	default:
		return OK
	}
}

func classifyInodePct(v float64) Severity {
	switch {
	case v > InodePctFail:
		return Fail
	case v > InodePctWarn:
		return Warn
	default:
		return OK
	}
}

// classifyBatteryHealth is inverted relative to the disk/FD classifiers —
// battery wear health is a "higher is better" percentage, so a low value is
// the bad state.
func classifyBatteryHealth(v float64) Severity {
	switch {
	case v < BatteryHealthFail:
		return Fail
	case v < BatteryHealthWarn:
		return Warn
	default:
		return OK
	}
}

// repoHealthMetric derives package-repo health from the agent's
// `package_updates` trait. The agent emits `package_updates.count` on
// every platform whose package manager it understands (apt / dnf /
// yum / brew); on unsupported platforms it emits -1, and the
// broken_repo_count key is absent. Anything we can't classify
// confidently leaves Severity=Unknown so the composite isn't tainted
// by envoys that simply can't report (no collector run yet, or a
// platform without a matching package-manager probe).
//
// No warn middle state exists for this trait — the agent only
// distinguishes "ok" from "fail" by counting broken repos.
func repoHealthMetric(traits map[string]string) RepoHealthMetric {
	if traits == nil {
		return RepoHealthMetric{Severity: Unknown}
	}
	count, ok := traits["package_updates.count"]
	if !ok || count == "" || count == "-1" {
		return RepoHealthMetric{Severity: Unknown}
	}
	broken, _ := strconv.ParseUint(traits["package_updates.broken_repo_count"], 10, 64)
	if broken > 0 {
		return RepoHealthMetric{
			Status:   "fail",
			Errors:   broken,
			Present:  true,
			Severity: Fail,
		}
	}
	return RepoHealthMetric{
		Status:   "ok",
		Present:  true,
		Severity: OK,
	}
}

func worstOf(severities ...Severity) Severity {
	worst := OK
	for _, s := range severities {
		// Unknown never escalates worst — a missing trait shouldn't
		// outvote a real OK reading from another field.
		if s == Unknown {
			continue
		}
		if s > worst {
			worst = s
		}
	}
	return worst
}
