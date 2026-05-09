// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package fleetpressure is the single source of truth for translating
// per-envoy `agent_self.*` and `host_self.*` traits into operator-
// facing pressure metrics. Both the dashboard rollup card and the
// per-envoy "Agent footprint" panel consume types from this package
// so the same numbers can never disagree across surfaces.
//
// The package owns *data*, not *rendering*: callers build whatever
// HTML / JSON / CSS classes they need, but every threshold decision
// flows through [Classify] (see envoyStatsSot feedback memory).
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
// `agent_self.*` / `host_self.*` trait shape — see
// `agent/src/traits/{agent_self,host_self}.rs` for the
// authoritative definitions.
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

	// Worst severity across the four colored fields. Drives the
	// dashboard card's offender selection.
	Overall            Severity
}

// FleetSummary is the dashboard rollup. Holds the loud numbers
// and the offender hostname so the card can drill down to the
// per-envoy view without re-walking the fleet.
type FleetSummary struct {
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
}

// Compute projects a single EnvoySource into an EnvoyPressure.
// Missing traits leave fields with Present=false; numeric parse
// errors are treated as missing (defensive: a malformed trait
// shouldn't poison the rollup).
func Compute(src EnvoySource) EnvoyPressure {
	p := EnvoyPressure{
		ID:       src.ID,
		Hostname: src.Hostname,
	}
	p.RSSBytes = uintMetric(src.Traits, "agent_self.rss_bytes", classifyNeutral)
	p.OpenFDs = uintMetric(src.Traits, "agent_self.open_fds", classifyOpenFDs)
	p.Threads = uintMetric(src.Traits, "agent_self.threads", classifyNeutral)
	p.Descendants = uintMetric(src.Traits, "agent_self.descendants", classifyNeutral)
	p.DescendantOpenFDs = uintMetric(src.Traits, "agent_self.descendant_open_fds", classifyDescendantFDs)
	p.UptimeSecs = uintMetric(src.Traits, "agent_self.uptime_secs", classifyNeutral)
	p.FDUsed = uintMetric(src.Traits, "host_self.fd_used", classifyNeutral)
	p.FDMax = uintMetric(src.Traits, "host_self.fd_max", classifyNeutral)
	p.FDPct = floatMetric(src.Traits, "host_self.fd_pct", classifyHostFDPct)
	p.ProcCount = uintMetric(src.Traits, "host_self.proc_count", classifyNeutral)
	p.OomRecent = uintMetric(src.Traits, "host_self.oom_recent", classifyOomRecent)
	if src.Traits == nil {
		// Distinguish "no data" from "everything OK" — leave
		// Overall as the zero value (Unknown).
		return p
	}
	p.Overall = worstOf(p.OpenFDs.Severity, p.DescendantOpenFDs.Severity, p.FDPct.Severity, p.OomRecent.Severity)
	return p
}

// Aggregate walks a slice of EnvoySources and produces the
// dashboard rollup. Iteration order follows the input slice;
// offender selection is stable (first envoy hitting the max
// wins, ties broken by input order).
func Aggregate(sources []EnvoySource) FleetSummary {
	var sum FleetSummary
	for _, src := range sources {
		p := Compute(src)
		hasAny := p.OpenFDs.Present || p.DescendantOpenFDs.Present ||
			p.FDPct.Present || p.OomRecent.Present
		if hasAny {
			sum.TotalReporting++
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
	return sum
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
