// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import "fmt"

// fileContentSourceConflictRule rejects a `file` resource that sets
// both `content:` and `source:`.
//
// The agent's file executor reads from one or the other, never both —
// the precedence between them is implementation-defined. The
// canonical validator catches the *missing* case (neither set) but
// not the *both* case, which is a much sneakier bug: it lints clean,
// converges, and silently uses whichever the executor happens to
// prefer that day.
//
// Resolution is operator-only: pick one. The linter has no basis to
// guess which the operator meant.
type fileContentSourceConflictRule struct{}

func (fileContentSourceConflictRule) id() string { return "file-content-source-conflict" }

func (fileContentSourceConflictRule) apply(mod *configcrate, _ Options) []Finding {
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "file" {
			continue
		}
		hasContent := attrStr(res.Attrs, "content") != ""
		hasSource := attrStr(res.Attrs, "source") != ""
		if hasContent && hasSource {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "file-content-source-conflict",
				Message: fmt.Sprintf(
					"resource %q: file resource sets both 'content' and 'source' — pick one (precedence between them is implementation-defined)",
					res.Name,
				),
			})
		}
	}
	return out
}

func init() { register(fileContentSourceConflictRule{}) }
