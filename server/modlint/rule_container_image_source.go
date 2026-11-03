// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import "fmt"

// containerImageSourceRule validates the `image:` / `artifact:` invariants
// on a `container` resource:
//
//   - The two are mutually exclusive (operator-time check — the agent
//     re-validates this at apply time, but catching it during lint saves
//     a convergence cycle).
//   - For state=running (the default), one of them must be set. State
//     `absent` / `stopped` doesn't need an image source — the container
//     either doesn't exist or already does.
//
// Cleanly mirrors the agent's runtime validation; modlint's job is to
// turn a future runtime error into a current lint error.
type containerImageSourceRule struct{}

func (containerImageSourceRule) id() string { return "container-image-source" }

func (containerImageSourceRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "container" {
			continue
		}
		hasImage := attrStr(res.Attrs, "image") != ""
		hasArtifact := attrStr(res.Attrs, "artifact") != ""

		if hasImage && hasArtifact {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "container-image-source",
				Message: fmt.Sprintf(
					"resource %q: container resource sets both 'image' and 'artifact' — pick one (registry pull or curator pull)",
					res.Name,
				),
			})
			continue
		}

		// state defaults to "running" when unset.
		state := attrStr(res.Attrs, "state")
		if state == "" {
			state = "running"
		}
		if state == "running" && !hasImage && !hasArtifact {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "container-image-source",
				Message: fmt.Sprintf(
					"resource %q: container resource with state=running needs 'image' or 'artifact'",
					res.Name,
				),
			})
		}
	}
	return out
}

func init() { register(containerImageSourceRule{}) }
