// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import "fmt"

// HealthBanner carries the engineer-first status strip rendered at the
// top of every /payload, /longdrawer, /gitback, /lockbox page. The
// problems list doubles as the "problems table" rendered under the
// banner when non-empty — the banner + table together tell an on-call
// operator "is anything broken, and where" before any totals or
// browse tables.
type HealthBanner struct {
	Level    string // "ok" | "warn" | "err"
	Headline string
	Problems []HealthProblem
}

// HealthProblem is one row in the problems table. Severity drives the
// per-row badge color; Link is optional and deep-links to a drill-down
// so the operator can act on the problem in one click.
type HealthProblem struct {
	Kind     string
	Subject  string
	Detail   string
	Link     string
	Severity string // "warn" | "err"
}

// finalizeHealthBanner picks the page-wide Level from the worst problem
// severity and composes the headline. "err" wins over "warn"; an empty
// problems slice is the healthy case.
func finalizeHealthBanner(scope string, problems []HealthProblem, healthyHeadline string) HealthBanner {
	if len(problems) == 0 {
		return HealthBanner{Level: "ok", Headline: healthyHeadline}
	}
	level := "warn"
	for _, p := range problems {
		if p.Severity == "err" {
			level = "err"
			break
		}
	}
	headline := fmt.Sprintf("%s: %d problem", scope, len(problems))
	if len(problems) != 1 {
		headline += "s"
	}
	return HealthBanner{Level: level, Headline: headline, Problems: problems}
}
