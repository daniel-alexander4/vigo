// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"

	"git.alexander4.io/vigo/server/i18n"
)

// HealthBanner carries the engineer-first status strip rendered at the
// top of every /payload, /longdrawer, /gitback, /lockbox page. The
// problems list doubles as the "problems table" rendered under the
// banner when non-empty — the banner + table together tell an on-call
// operator "is anything broken, and where" before any totals or
// browse tables.
type HealthBanner struct {
	Locale   string // per-request locale for {{T}} inside the health-banner partial
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
// problems slice is the healthy case. The "scope: N problem(s)" headline
// is locale-aware (one/many keys carry the interpolated count, which the
// flat catalog can't pluralize on its own); scope is the subsystem proper
// name and stays English. Locale is stamped on the banner so the
// health-banner partial's {{T}}/{{tEnum}} calls resolve.
func finalizeHealthBanner(locale, scope string, problems []HealthProblem, healthyHeadline string) HealthBanner {
	if len(problems) == 0 {
		return HealthBanner{Level: "ok", Headline: healthyHeadline, Locale: locale}
	}
	level := "warn"
	for _, p := range problems {
		if p.Severity == "err" {
			level = "err"
			break
		}
	}
	key := "health_banner.problems_many"
	if len(problems) == 1 {
		key = "health_banner.problems_one"
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, key), scope, len(problems))
	return HealthBanner{Level: level, Headline: headline, Problems: problems, Locale: locale}
}
