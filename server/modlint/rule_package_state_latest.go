// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "fmt"

// packageStateLatestRule emits an info-level notice when a `package`
// or `nonrepo_package` resource declares `state: latest`.
//
// Why notice: `state: latest` makes the package manager check
// upstream for newer versions on every check-in cycle. On Debian/
// Ubuntu that's an `apt-get update` + `apt-cache policy` round-trip
// every 30s; on RHEL family it's a `yum check-update` or `dnf
// check-update`. Multiplied across a fleet, this creates a hot path
// that talks to mirrors constantly.
//
// Why not error: there are legitimate uses (lab fleets, dev VMs,
// always-bleeding-edge edge cases). The right call is the operator's;
// the linter's job is to make the cost visible at lint time so it
// isn't a surprise in production.
//
// Severity: Info — non-actionable for correctness. Operators who
// don't want the notice can pin a version explicitly.
type packageStateLatestRule struct{}

func (packageStateLatestRule) id() string { return "package-state-latest" }

func (packageStateLatestRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "package" && res.Type != "nonrepo_package" {
			continue
		}
		if attrStr(res.Attrs, "state") != "latest" {
			continue
		}
		out = append(out, Finding{
			Severity: Info,
			Line:     res.Line,
			Column:   res.Column,
			RuleID:   "package-state-latest",
			Message: fmt.Sprintf(
				"resource %q: state: latest queries upstream every check-in cycle — pin a version unless you intentionally want continuous-update behavior",
				res.Name,
			),
		})
	}
	return out
}

func init() { register(packageStateLatestRule{}) }
