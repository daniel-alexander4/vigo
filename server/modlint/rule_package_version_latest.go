// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "fmt"

// packageVersionLatestRule emits an info-level notice when a `package`
// or `nonrepo_package` resource declares `version: latest`.
//
// Why notice: `version: latest` makes the package manager check
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
type packageVersionLatestRule struct{}

func (packageVersionLatestRule) id() string { return "package-version-latest" }

func (packageVersionLatestRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "package" && res.Type != "nonrepo_package" {
			continue
		}
		if attrStr(res.Attrs, "version") != "latest" {
			continue
		}
		out = append(out, Finding{
			Severity: Info,
			Line:     res.Line,
			Column:   res.Column,
			RuleID:   "package-version-latest",
			Message: fmt.Sprintf(
				"resource %q: version: latest queries upstream every check-in cycle — pin a version unless you intentionally want continuous-update behavior",
				res.Name,
			),
		})
	}
	return out
}

func init() { register(packageVersionLatestRule{}) }
