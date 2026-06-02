// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"sort"
)

// FleetFile is one parsed module file in a publish-time fleet
// snapshot. Path is the stockpile-relative source location used in
// finding messages so operators can locate the offending pair.
type FleetFile struct {
	Path string
	Data []byte
}

// fleetOrigin records where (file + resource name) a candidate-for-
// collision resource was declared. Used by LintFleet's collision
// reporters and by the helper functions below.
type fleetOrigin struct {
	path     string
	resource string
}

// LintFleet runs cross-module checks over a set of module files. The
// per-file Lint() catches every within-module issue; this entry
// point covers the few classes of bugs that only become visible when
// you compare modules against each other:
//
//   - Two modules that both manage the same `package` resource (by
//     package name + state combination) — at minimum a redundant
//     declaration; at worst a conflict (one says present, one says
//     absent) that the operator only finds out about when both
//     modules end up assigned to the same envoy and convergence
//     applies them in undefined order.
//
//   - Two modules with `file` resources writing to the same
//     `target_path:` — same shape, applied to filesystem state.
//
// Cross-file checks against hostcrates / roles / waivers (do listed
// modules exist? do referenced roles resolve?) are not in scope here
// — those need the full config-loader resolution to know about
// inheritance and default usercrates, which lives a layer up. This
// function only scans modules.
//
// LintFleet does not parse non-module files; pass them in if you
// like — they'll be skipped silently.
func LintFleet(files []FleetFile) []Finding {
	type packageKey struct {
		pkgName string
		state   string
	}
	type fileKey struct {
		targetPath string
	}

	packageOwners := make(map[packageKey][]fleetOrigin)
	fileOwners := make(map[fileKey][]fleetOrigin)

	for _, f := range files {
		mod, err := parse(repair(f.Data, &Result{}))
		if err != nil {
			continue // per-file Lint() already surfaced the parse error
		}
		for _, res := range mod.Resources {
			switch res.Type {
			case "package", "nonrepo_package":
				name := attrStr(res.Attrs, "package")
				if name == "" {
					name = res.Name // fall back to resource name
				}
				state := attrStr(res.Attrs, "state")
				k := packageKey{pkgName: name, state: state}
				packageOwners[k] = append(packageOwners[k], fleetOrigin{
					path:     f.Path,
					resource: res.Name,
				})
			case "file":
				target := attrStr(res.Attrs, "target_path")
				if target == "" {
					continue
				}
				k := fileKey{targetPath: target}
				fileOwners[k] = append(fileOwners[k], fleetOrigin{
					path:     f.Path,
					resource: res.Name,
				})
			}
		}
	}

	var out []Finding

	// Package collisions.
	for k, owners := range packageOwners {
		if len(owners) < 2 {
			continue
		}
		// Distinct files only — a single file declaring the same
		// package twice is caught by duplicate-resource-names.
		if uniqueByPath(owners) < 2 {
			continue
		}
		out = append(out, Finding{
			Severity: Warning,
			RuleID:   "fleet-duplicate-package",
			Message: fmt.Sprintf(
				"package %q (state=%q) is managed by %d configcrates: %s — if multiple configcrates end up assigned to the same envoy, convergence order is undefined",
				k.pkgName, defaultStr(k.state, "present"), len(owners),
				renderOriginList(owners),
			),
		})
	}

	// File collisions on target_path.
	for k, owners := range fileOwners {
		if len(owners) < 2 {
			continue
		}
		if uniqueByPath(owners) < 2 {
			continue
		}
		out = append(out, Finding{
			Severity: Warning,
			RuleID:   "fleet-duplicate-file",
			Message: fmt.Sprintf(
				"target_path %q is written by %d configcrates: %s — if multiple configcrates end up assigned to the same envoy, convergence order is undefined and the on-disk content is whichever wrote last",
				k.targetPath, len(owners), renderOriginList(owners),
			),
		})
	}

	// Sort by RuleID then Message for deterministic output.
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// uniqueByPath returns the number of distinct file paths in owners.
// Used to filter out the case where a single file declares the same
// package twice (which is duplicate-resource-names' problem, not a
// fleet-level concern).
func uniqueByPath(owners []fleetOrigin) int {
	seen := make(map[string]struct{}, len(owners))
	for _, o := range owners {
		seen[o.path] = struct{}{}
	}
	return len(seen)
}

// renderOriginList builds a readable "<file>:<resource>, <file>:<resource>"
// joined string. Sorted so the message is deterministic.
func renderOriginList(owners []fleetOrigin) string {
	pairs := make([]string, 0, len(owners))
	for _, o := range owners {
		pairs = append(pairs, fmt.Sprintf("%s:%s", o.path, o.resource))
	}
	sort.Strings(pairs)
	var s string
	for i, p := range pairs {
		if i > 0 {
			s += ", "
		}
		s += p
	}
	return s
}

// defaultStr returns d when s is empty.
func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
